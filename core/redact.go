package core

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact.go implements §7.4 redaction. Redaction is a two-phase operation:
// RedactText/RedactArea build a plan of regions to remove per page, and
// RedactAndFlatten permanently rewrites each page's content stream to strip the
// text-showing operators inside those regions (not merely paint over them —
// .claude/SECURITY.md §1) and returns a new document. The plan is stored on the
// document until flattened; the source document is never modified in place.

// RedactText marks the text a regex pattern matches (e.g. a national ID or
// credit-card number) for removal and returns the matched regions. Nothing is
// removed until RedactAndFlatten.
//
// Matching runs over the Unicode text extraction reads (/ToUnicode, as
// ExtractText decodes it), one text-showing operator at a time: the strings
// of a TJ array, at any nesting depth, are matched as one text, so kerning
// does not split a match. A matched operator is removed whatever its size or
// position (a zero-size or off-page run included). The /ActualText, /Alt and
// /E entries of marked-content property lists are matched too: a match strips
// that entry and removes the text shown inside the marked sequence, and the
// page is redacted even if the sequence shows no text. A font whose glyphs
// draw something other than what its /ToUnicode says is out of scope; use
// RedactArea for a geometric guarantee.
//
// Returns ErrPageNotFound if pageIndex is out of range, ErrInvalidPDF if the
// document was not opened from bytes, or a regexp compile error — argument
// errors that leave the plan untouched. Any later failure also marks the plan
// incomplete, so RedactAndFlatten refuses to run rather than save the page
// without the text asked for: a *RedactionError (matching
// ErrRedactionUnsupported) for a page that cannot be redacted safely — content
// other readers could read differently, which could hide matching text, text
// shown through a form XObject, tiling pattern, soft-mask group or Type 3
// glyph procedure, or a redaction budget exceeded; ErrInvalidPDF (wrapped)
// for page content that cannot be decoded; or ErrInternal.
func (d *Document) RedactText(pageIndex int, pattern string) (_ []Rect, err error) {
	accepted := false
	// Deferred before guard so it runs after guard has turned a panic into err.
	defer func() {
		if err != nil && accepted {
			d.failRedactionPlan(err)
		}
	}()
	defer guard("RedactText", pageIndex, &err)
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	if d.resolver == nil || pageIndex >= len(d.pageInfos) {
		return nil, ErrInvalidPDF
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	accepted = true
	injectTestPanic("RedactText", pageIndex)
	page, err := d.redactionPage(pageIndex)
	if err != nil {
		return nil, err
	}
	m := page.Match(re)
	rects := make([]Rect, len(m.Rects))
	for i, r := range m.Rects {
		rects[i] = Rect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
	}

	func() {
		d.mu.Lock()
		defer d.mu.Unlock() // released even on a panic, so failRedactionPlan can lock
		d.addRedactionsLocked(pageIndex, rects)
		if len(m.Marks) > 0 {
			if d.redactMarks == nil {
				d.redactMarks = map[int][]redact.Mark{}
			}
			d.redactMarks[pageIndex] = append(d.redactMarks[pageIndex], m.Marks...)
		}
		if len(m.Needles) > 0 {
			if d.redactNeedles == nil {
				d.redactNeedles = map[int][]string{}
			}
			d.redactNeedles[pageIndex] = append(d.redactNeedles[pageIndex], m.Needles...)
		}
	}()
	return rects, nil
}

// RedactArea marks a region (in PDF user space) on a page for redaction. A
// rect with a negative width or height is normalized (flipped) first.
// RedactAndFlatten removes everything the page paints that meets the region —
// touching its edge, or collapsed to a point inside it, counts — whole: every
// text-showing operator, and every image (a full-page scan under any region
// is blanked entirely), inline image, form XObject without text, painted path
// and shading. A pattern colour, colour space or ExtGState (such as a soft
// mask) that only removed content painted with goes too. What is removed this
// way is not written anywhere else in the output either — not through a
// resource dictionary another page shares, nor a form's or pattern's
// resources — unless a page of the output still paints it. The page's
// marked-content /ActualText, /Alt and /E entries are stripped too.
//
// Returns ErrPageNotFound if pageIndex is out of range, or ErrInvalidArgument
// (and marks nothing) if any coordinate of rect is NaN or infinite.
func (d *Document) RedactArea(pageIndex int, rect Rect) error {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return ErrPageNotFound
	}
	for _, v := range []float64{rect.X, rect.Y, rect.Width, rect.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: redaction region must be finite", ErrInvalidArgument)
		}
	}
	if rect.Width < 0 {
		rect.X, rect.Width = rect.X+rect.Width, -rect.Width
	}
	if rect.Height < 0 {
		rect.Y, rect.Height = rect.Y+rect.Height, -rect.Height
	}
	d.mu.Lock()
	d.addRedactionsLocked(pageIndex, []Rect{rect})
	if d.redactRegions == nil {
		d.redactRegions = map[int][]Rect{}
	}
	d.redactRegions[pageIndex] = append(d.redactRegions[pageIndex], rect)
	d.mu.Unlock()
	return nil
}

// RedactAndFlatten PERMANENTLY removes the planned content and returns a new
// document. On each page with a plan:
//   - every text-showing operator RedactText matched, and everything that
//     meets a RedactArea region (see RedactArea), is deleted from the page's
//     content stream;
//   - every marked-content /ActualText, /Alt and /E entry is stripped;
//   - the page keeps only the resources its remaining content uses, and only
//     the page-dictionary entries needed to paint it (its annotations,
//     thumbnail, metadata and the like are not written);
//   - a solid black rectangle is drawn over each region and each RedactText
//     match, for display.
//
// References from other pages to a redacted page (a link destination, an
// annotation's /P) point at its redacted copy; its original annotations are
// written nowhere. Images, forms, patterns and soft masks removed under a
// region are written nowhere unless content kept in the output uses them; a
// resource dictionary that lists one keeps its other entries. To know, each
// page that is not redacted but can reach one is walked, within one
// redaction budget for the call (past it the call fails with a
// budget_exceeded *RedactionError naming that page).
//
// The source document is unchanged; the returned document carries no FluxDocs
// annotation layer (it is re-opened from the redacted bytes). This is
// IRREVERSIBLE: the removed content does not exist in the output.
//
// Returns ErrInvalidPDF if the document was not opened from bytes, a
// "redaction plan incomplete" error wrapping the first failure if any earlier
// RedactText/AutoRedactPII call failed after accepting its arguments (the plan
// may be missing regions; re-open the source and redact again), a
// *RedactionError (matching ErrRedactionUnsupported) if a page to redact
// cannot be redacted safely (redaction fails closed rather than miss content
// other viewers render; ExtractText and RenderPage stay lenient on such
// content), ErrInvalidPDF (wrapped) for page content that cannot be decoded, a
// build/re-open error, or ErrInternal if a planned mark no longer fits its
// page (a bug). With no pending redactions it returns a faithful copy.
func (d *Document) RedactAndFlatten() (_ *Document, err error) {
	defer guard("RedactAndFlatten", -1, &err)
	if d.resolver == nil || len(d.pageInfos) != len(d.Pages) {
		return nil, ErrInvalidPDF
	}

	d.mu.Lock()
	if d.redactErr != nil {
		planErr := d.redactErr
		d.mu.Unlock()
		return nil, fmt.Errorf("redaction plan incomplete: %w", planErr)
	}
	plan := make(map[int][]Rect, len(d.redactions))
	for i, rs := range d.redactions {
		plan[i] = append([]Rect(nil), rs...)
	}
	regions := make(map[int][]Rect, len(d.redactRegions))
	for i, rs := range d.redactRegions {
		regions[i] = append([]Rect(nil), rs...)
	}
	marks := make(map[int][]redact.Mark, len(d.redactMarks))
	for i, ms := range d.redactMarks {
		marks[i] = append([]redact.Mark(nil), ms...)
	}
	d.mu.Unlock()

	sources := make([]parse.PageSource, len(d.pageInfos))
	outs := map[int]redact.Output{}
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
		if rects := plan[i]; len(rects) > 0 || len(marks[i]) > 0 {
			page, err := d.redactionPage(i)
			if err != nil {
				return nil, err
			}
			out, err := page.Rewrite(toRedactRects(regions[i]), toRedactRects(rects), marks[i])
			if errors.Is(err, redact.ErrMarkMismatch) {
				return nil, fmt.Errorf("%w: page %d: %v", ErrInternal, i, err)
			}
			if err != nil {
				// The cause is not quoted: resolver errors can quote the file.
				return nil, fmt.Errorf("%w: page %d resources cannot be read for redaction", ErrInvalidPDF, i)
			}
			outs[i] = out
			src.ReplaceContent = out.Content
			src.ReplaceResources = out.Resources
			// Only what paints the page is copied: annotations, thumbnails
			// and metadata may carry the removed content.
			src.Redacted = true
		}
		sources[i] = src
	}
	// What was removed under a region must not survive through another
	// route (a shared resource dictionary, a form's resources).
	deny, err := d.redactionDenySet(outs)
	if err != nil {
		return nil, err
	}
	if len(sources) > 0 {
		sources[0].Deny = deny
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		// The copier's error can quote the file (a token of an object that
		// does not parse); it is not reported.
		return nil, fmt.Errorf("%w: the redacted document cannot be built", ErrInvalidPDF)
	}
	return OpenBytes(built)
}

// redactionPage reads a page for redaction: its content (PageContentSegments,
// so a token, comment or inline image split across the streams of a
// /Contents array is refused — viewers that concatenate the streams,
// separate them or parse each alone would show different text), scanned once
// with the strict scanner, inline-image colour space names resolved through
// the page's resource scopes, and every route to another content stream
// walked (redact.AnalyzePage).
//
// Returns the page model, or the failure as redactionScanError reports it.
func (d *Document) redactionPage(pageIndex int) (*redact.Page, error) {
	seg, err := d.resolver.PageContentSegments(d.pageInfos[pageIndex])
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	page, err := redact.AnalyzePage(d.resolver, d.pageInfos[pageIndex], seg)
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	return page, nil
}

// redactionScanError reports a failure to read a page safely for redaction.
// A refusal (content other readers could read differently, text drawn
// through a route redaction does not rewrite, an unsupported XObject, a
// budget) is a *RedactionError naming the page and reason, not ErrInvalidPDF
// (decision D2): viewers open such files. Content too large to read is a
// budget refusal too. Anything else — a stream that does not decode, an
// object that does not resolve — is a broken file: ErrInvalidPDF.
func redactionScanError(page int, err error) error {
	var refusal *redact.Refusal
	switch {
	case errors.As(err, &refusal):
		return &RedactionError{Page: page, Reason: RedactionReason(refusal.Reason), Detail: refusal.Error(), Err: refusal.Err}
	case errors.Is(err, parse.ErrDecodeLimit):
		return &RedactionError{Page: page, Reason: RedactionBudgetExceeded,
			Detail: "page content exceeds the decode limit", Err: err}
	}
	// The cause is not quoted: decode and resolver errors can quote the file.
	return fmt.Errorf("%w: page %d content cannot be read for redaction", ErrInvalidPDF, page)
}

// failRedactionPlan records the first failure that left the redaction plan
// incomplete (see redactErr). Later failures keep the first one.
func (d *Document) failRedactionPlan(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.redactErr == nil {
		d.redactErr = err
	}
}

// addRedactionsLocked appends regions to the plan for a page. Called under mu.
func (d *Document) addRedactionsLocked(pageIndex int, rects []Rect) {
	if len(rects) == 0 {
		return
	}
	if d.redactions == nil {
		d.redactions = map[int][]Rect{}
	}
	d.redactions[pageIndex] = append(d.redactions[pageIndex], rects...)
}

// toRedactRects converts core rects into the redact package's rect type.
func toRedactRects(rects []Rect) []redact.Rect {
	out := make([]redact.Rect, len(rects))
	for i, r := range rects {
		out[i] = redact.Rect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
	}
	return out
}

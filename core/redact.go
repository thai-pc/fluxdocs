package core

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/thai-pc/fluxdocs/core/docops/redact"
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
// errors that leave the plan untouched. Any later failure (reading the page,
// ErrInternal, ErrInvalidPDF for page content the strict scanner cannot read —
// operands it would discard or that a viewer could pair differently, which
// could hide matching text — or a *redact.RefusalError if the page paints text
// through a Form XObject (matching redact.ErrFormXObjectText), a tiling
// pattern, a soft-mask group or a Type 3 glyph procedure, or exceeds a
// redaction budget) also marks the plan incomplete, so RedactAndFlatten
// refuses to run rather than save the page without the text asked for.
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
// rect with a negative width or height is normalized (flipped) first. Every
// text-showing operator whose box meets the region — touching its edge, or
// collapsed to a point inside it, counts — is removed by RedactAndFlatten.
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

// RedactAndFlatten PERMANENTLY removes the planned content — every
// text-showing operator RedactText matched, and every one whose box meets a
// RedactArea region, is deleted from its page's content stream; matched
// marked-content property entries are stripped; and the page's original
// /Annots are dropped — then draws a solid black rectangle over each region
// and each RedactText match for display, and returns a new document. The
// source document is unchanged; the returned document carries no FluxDocs
// annotation layer (it is re-opened from the redacted bytes). This is IRREVERSIBLE: the removed text does not
// exist in the output.
//
// Returns ErrInvalidPDF if the document was not opened from bytes, a
// "redaction plan incomplete" error wrapping the first failure if any earlier
// RedactText/AutoRedactPII call failed after accepting its arguments (the plan
// may be missing regions; re-open the source and redact again), ErrInvalidPDF
// if a page to redact has content the strict scanner cannot read, a
// *redact.RefusalError if such a page paints text through a Form XObject
// (matching redact.ErrFormXObjectText), a tiling pattern, a soft-mask group or
// a Type 3 glyph procedure, or exceeds a redaction budget (redaction fails
// closed rather than miss text other viewers render; ExtractText and
// RenderPage stay lenient on such content), a build/re-open error, or
// ErrInternal if a planned mark no longer fits its page (a bug). With no
// pending redactions it returns a faithful copy.
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
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
		if rects := plan[i]; len(rects) > 0 || len(marks[i]) > 0 {
			page, err := d.redactionPage(i)
			if err != nil {
				return nil, err
			}
			content, resources, err := page.Rewrite(toRedactRects(regions[i]), toRedactRects(rects), marks[i])
			if errors.Is(err, redact.ErrMarkMismatch) {
				return nil, fmt.Errorf("%w: page %d: %v", ErrInternal, i, err)
			}
			if err != nil {
				return nil, fmt.Errorf("page %d: %w", i, err)
			}
			src.ReplaceContent = content
			src.ReplaceResources = resources
			src.DropAnnots = true // annotation objects may carry the removed text
		}
		sources[i] = src
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return nil, err
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
// Returns the page model, a read error, or the scan error or refusal wrapped
// by redactionScanError.
func (d *Document) redactionPage(pageIndex int) (*redact.Page, error) {
	seg, err := d.resolver.PageContentSegments(d.pageInfos[pageIndex])
	if err != nil {
		return nil, err
	}
	page, err := redact.AnalyzePage(d.resolver, d.pageInfos[pageIndex], seg)
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	return page, nil
}

// redactionScanError wraps a failure to read a page's content safely for
// redaction. A refusal (*redact.RefusalError: text drawn through a form,
// pattern, soft-mask group or Type 3 glyph procedure, an unsupported XObject,
// a budget) is reported as is ("page N shows text through a form XObject
// (object 6); redaction of form XObjects is not supported yet"); content the
// strict scanner cannot read is ErrInvalidPDF.
func redactionScanError(page int, err error) error {
	var refusal *redact.RefusalError
	if errors.As(err, &refusal) {
		return fmt.Errorf("page %d %w", page, err)
	}
	return fmt.Errorf("%w: page %d content cannot be scanned safely for redaction: %v", ErrInvalidPDF, page, err)
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

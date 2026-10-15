package core

import (
	"errors"
	"fmt"
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

// RedactText marks regions whose text matches a regex pattern (e.g. a national
// ID or credit-card number) and returns the matched regions. Nothing is removed
// until RedactAndFlatten.
//
// Returns ErrPageNotFound if pageIndex is out of range, ErrInvalidPDF if the
// document was not opened from bytes, or a regexp compile error — argument
// errors that leave the plan untouched. Any later failure (reading the page,
// ErrInternal, ErrInvalidPDF for page content the strict scanner cannot read —
// operands it would discard or that a viewer could pair differently, which
// could hide matching text — or an error matching redact.ErrFormXObjectText if
// the page draws text through a Form XObject) also marks the plan incomplete,
// so RedactAndFlatten refuses to run rather than save the page without the
// regions asked for.
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
	content, err := d.resolver.PageContent(d.pageInfos[pageIndex])
	if err != nil {
		return nil, err
	}

	if err := redact.CheckFormXObjects(d.resolver, d.pageInfos[pageIndex].Resources, content); err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	found, err := redact.FindTextRects(content, re)
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	rects := make([]Rect, len(found))
	for i, r := range found {
		rects[i] = Rect{X: r.X, Y: r.Y, Width: r.Width, Height: r.Height}
	}

	func() {
		d.mu.Lock()
		defer d.mu.Unlock() // released even on a panic, so failRedactionPlan can lock
		d.addRedactionsLocked(pageIndex, rects)
	}()
	return rects, nil
}

// RedactArea marks a region (in PDF user space) on a page for redaction.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) RedactArea(pageIndex int, rect Rect) error {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return ErrPageNotFound
	}
	d.mu.Lock()
	d.addRedactionsLocked(pageIndex, []Rect{rect})
	d.mu.Unlock()
	return nil
}

// RedactAndFlatten PERMANENTLY removes the content within every marked region —
// the intersecting text-showing operators are deleted from each page's content
// stream and the page's original /Annots are dropped — then draws a solid black
// rectangle over each region for display, and returns a new document. The source
// document is unchanged; the returned document carries no FluxDocs annotation
// layer (it is re-opened from the redacted bytes). This is IRREVERSIBLE: the
// removed text does not exist in the output.
//
// Returns ErrInvalidPDF if the document was not opened from bytes, a
// "redaction plan incomplete" error wrapping the first failure if any earlier
// RedactText/AutoRedactPII call failed after accepting its arguments (the plan
// may be missing regions; re-open the source and redact again), ErrInvalidPDF
// if a page with marked regions has content the strict scanner cannot read, an
// error matching redact.ErrFormXObjectText if such a page draws text through a
// Form XObject (redaction fails closed rather than miss text other viewers
// render; ExtractText and RenderPage stay lenient on such content), or a
// build/re-open error. With no pending redactions it returns a faithful copy.
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
	d.mu.Unlock()

	sources := make([]parse.PageSource, len(d.pageInfos))
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
		if rects := plan[i]; len(rects) > 0 {
			content, err := d.resolver.PageContent(d.pageInfos[i])
			if err != nil {
				return nil, err
			}
			if err := redact.CheckFormXObjects(d.resolver, d.pageInfos[i].Resources, content); err != nil {
				return nil, redactionScanError(i, err)
			}
			redacted, err := redact.RedactContent(content, toRedactRects(rects))
			if err != nil {
				return nil, redactionScanError(i, err)
			}
			src.ReplaceContent = redacted
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

// redactionScanError wraps a failure to read a page's content safely for
// redaction. Text drawn through a Form XObject is reported as is ("page N draws
// text through form XObject /Fm1; redaction of form XObjects is not supported
// yet", matching redact.ErrFormXObjectText); content the strict scanner cannot
// read is ErrInvalidPDF.
func redactionScanError(page int, err error) error {
	if errors.Is(err, redact.ErrFormXObjectText) {
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

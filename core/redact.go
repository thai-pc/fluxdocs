package core

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"

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
// position (a zero-size or off-page run included), and whole: removal is per
// operator, so a match removes everything that operator shows — when a
// producer writes a line or a paragraph with one TJ, the whole line or
// paragraph. The /ActualText, /Alt and
// /E entries of marked-content property lists are matched too: a match strips
// that entry and removes the text shown inside the marked sequence, and the
// page is redacted even if the sequence shows no text. A font whose glyphs
// draw something other than what its /ToUnicode says is out of scope; use
// RedactArea for a geometric guarantee.
//
// A matched string of 4 or more characters is also removed wherever else it
// occurs in the document — on other pages, in annotations, in the document
// information — when RedactAndFlatten runs (automatic extension; see
// RedactAndFlatten). Matching there ignores white-space and invisible
// characters and folds compatibility forms (NFKC), and is case-sensitive.
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
		if d.redactPatterns == nil {
			d.redactPatterns = map[int][]string{}
		}
		seen := false
		for _, p := range d.redactPatterns[pageIndex] {
			seen = seen || p == pattern
		}
		if !seen {
			d.redactPatterns[pageIndex] = append(d.redactPatterns[pageIndex], pattern)
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
// marked-content /ActualText, /Alt and /E entries are stripped too. The text
// of a run removed only because it met the region is, when it has 8 or more
// characters, also removed wherever else it occurs in the document
// (automatic extension; see RedactAndFlatten).
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
//   - every marked-content /ActualText, /Alt and /E entry, and every other
//     string of a property list (/Lang included), is stripped, and every
//     comment in the content is blanked;
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
// The text the plan removed is also removed where the plan did not reach
// (automatic extension, docs/design/redaction-hardening.md §6.11): every
// string RedactText or AutoRedactPII matched (4 or more characters) and the
// text of every run removed only because it met a RedactArea region (8 or
// more) is searched for — white-space and invisible characters ignored,
// NFKC, case-sensitive — on every page (the runs that show it are removed,
// with a black box, and the page is redacted like the others; a property-list
// entry that holds it is stripped; a page that holds it where it shows
// nothing — a comment, a property list, another operator's string — is
// redacted with no region, which removes it), in every annotation (its text,
// its form field's values, its appearance: it is removed with its popup and
// replies)
// and in the document information (the field is not written). Text removed
// this way is never searched for in turn. A page that must be searched but
// cannot be read for certain — its content or a unit it paints does not
// decode, or the strict scanner reports a loss in it, behind which a lenient
// reading may hide text — refuses the redaction. Whenever the plan is not empty,
// every file attachment annotation is removed too: its content cannot be
// inspected. The returned document's RedactionReport lists what was removed
// beyond the plan (no document text).
//
// The output is verified before it is returned, always: it is re-opened, its
// redacted pages are read again strictly (no glyph may meet a region, no
// RedactText pattern may still match, no property-list text may remain),
// and its objects are searched for the removed text. What the search finds
// on pages that were not redacted extends the redaction again, at most twice;
// anything else is a failure.
//
// The source document and its plan are unchanged; the returned document
// carries no FluxDocs annotation layer (it is re-opened from the redacted
// bytes). This is IRREVERSIBLE: the removed content does not exist in the
// output.
//
// Returns ErrInvalidPDF if the document was not opened from bytes, a
// "redaction plan incomplete" error wrapping the first failure if any earlier
// RedactText/AutoRedactPII call failed after accepting its arguments (the plan
// may be missing regions; re-open the source and redact again), a
// *RedactionError (matching ErrRedactionUnsupported) if a page to redact
// cannot be redacted safely (redaction fails closed rather than miss content
// other viewers render; ExtractText and RenderPage stay lenient on such
// content) — including a page automatic extension reaches that cannot be
// redacted, e.g. one that shows the removed text through a form (the detail
// then starts with "automatic extension") —, ErrInvalidPDF (wrapped) for page
// content that cannot be decoded, a build error, ErrRedactionVerification
// (wrapped; it matches ErrInternal) when the output would still hold removed
// content or automatic extension did not converge ("not_converged"), or
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
	boxes := make(map[int][]Rect, len(d.redactions))
	for i, rs := range d.redactions {
		boxes[i] = append([]Rect(nil), rs...)
	}
	regions := make(map[int][]Rect, len(d.redactRegions))
	for i, rs := range d.redactRegions {
		regions[i] = append([]Rect(nil), rs...)
	}
	marks := make(map[int][]redact.Mark, len(d.redactMarks))
	for i, ms := range d.redactMarks {
		marks[i] = append([]redact.Mark(nil), ms...)
	}
	matched := make([]string, 0)
	for _, ns := range d.redactNeedles {
		matched = append(matched, ns...)
	}
	patterns := make(map[int][]string, len(d.redactPatterns))
	for i, ps := range d.redactPatterns {
		patterns[i] = append([]string(nil), ps...)
	}
	d.mu.Unlock()

	plan, err := newRedactPlan(boxes, regions, marks, patterns)
	if err != nil {
		return nil, err
	}
	// The caller's pages, read and rewritten once: their removed text gives
	// the area needles (§6.11), and an unchanged page's rewrite is reused.
	models := map[int]*redact.Page{}
	cache := map[int]redact.Output{}
	for _, i := range sortedKeys(plan.user) {
		page, err := d.redactionPage(i)
		if err != nil {
			return nil, err
		}
		models[i] = page
		out, err := d.rewritePage(plan, i, page)
		if err != nil {
			return nil, err
		}
		cache[i] = out
	}
	ns := &needleSets{prop: &redact.Needles{}}
	for _, m := range matched {
		ns.addProp(m, tagMatch, redact.MinMatchNeedle)
	}
	for _, i := range sortedKeys(cache) {
		for _, t := range cache[i].AreaTexts {
			ns.addProp(t, tagArea, redact.MinAreaNeedle)
		}
	}
	budget := &callBudget{}
	prepassed := false
	if ns.prop.Len() > 0 && !redactHooks.noPrepass {
		if err := d.prepass(plan, ns, models, cache, budget); err != nil {
			return nil, err
		}
		prepassed = true
	}
	if !plan.empty() {
		var sc *redact.TextScanner
		if ns.prop.Len() > 0 && !redactHooks.noPrepass {
			sc = redact.NewTextScanner(d.resolver, ns.prop)
		}
		if err := d.annotationPolicy(plan, sc, budget); err != nil {
			return nil, err
		}
		if sc != nil {
			budget.used += sc.Decoded
		}
		if !redactHooks.noPrepass {
			d.infoPolicy(plan, ns)
		}
	}

	for round := 0; ; round++ {
		// A page whose plan changed is rewritten again.
		for i := range plan.changed {
			delete(cache, i)
		}
		plan.changed = map[int]bool{}
		built, outs, deny, err := d.buildRedacted(plan, models, cache)
		if err != nil {
			return nil, err
		}
		ns.resetRemoved(outs)
		if redactHooks.mutate != nil {
			built = redactHooks.mutate(built)
		}
		if redactHooks.noVerify {
			return OpenBytes(built)
		}
		out, res, err := d.verifyRedaction(built, plan, ns, prepassed, budget)
		if err != nil {
			return nil, err
		}
		if res.empty() {
			report := d.finishReport(plan)
			out.mu.Lock()
			out.redactionReport = &report
			out.mu.Unlock()
			return out, nil
		}
		if round == maxExtendRounds {
			return nil, errNotConverged
		}
		changed, err := d.applyResiduals(plan, res, ns, models, deny)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, verificationError("removed text survives where the redaction cannot extend to it")
		}
	}
}

// rewritePage rewrites page i (its strict model page) by the plan.
// Returns ErrInternal for a planned mark that does not fit the page (a
// bug), or ErrInvalidPDF when its resources cannot be read.
func (d *Document) rewritePage(plan *redactPlan, i int, page *redact.Page) (redact.Output, error) {
	out, err := page.Rewrite(toRedactRects(plan.regions[i]), toRedactRects(plan.boxes[i]), plan.marks[i])
	if errors.Is(err, redact.ErrMarkMismatch) {
		return redact.Output{}, fmt.Errorf("%w: page %d: %v", ErrInternal, i, err)
	}
	var rf *redact.Refusal
	if errors.As(err, &rf) {
		return redact.Output{}, redactionScanError(i, err)
	}
	if err != nil {
		// The cause is not quoted: resolver errors can quote the file.
		return redact.Output{}, fmt.Errorf("%w: page %d resources cannot be read for redaction", ErrInvalidPDF, i)
	}
	return out, nil
}

// buildRedacted builds the output of plan: each redacted page rewritten
// (cache holds rewrites still valid; models the pages' strict models), the
// removed annotations left out of the other pages, the objects only removed
// content used and the removed annotations denied, the removed /Info fields
// not written. Returns the bytes, the rewrites and the denied objects.
func (d *Document) buildRedacted(plan *redactPlan, models map[int]*redact.Page, cache map[int]redact.Output) ([]byte, map[int]redact.Output, map[int]bool, error) {
	sources := make([]parse.PageSource, len(d.pageInfos))
	outs := map[int]redact.Output{}
	for i, pi := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: pi}
		switch {
		case plan.redacted(i):
			out, ok := cache[i]
			if !ok {
				page := models[i]
				if page == nil {
					p, err := d.redactionPage(i)
					if err != nil {
						return nil, nil, nil, err
					}
					page = p
					models[i] = p
				}
				var err error
				if out, err = d.rewritePage(plan, i, page); err != nil {
					return nil, nil, nil, err
				}
				cache[i] = out
			}
			outs[i] = out
			src.ReplaceContent = out.Content
			src.ReplaceResources = out.Resources
			// Only what paints the page is copied: annotations, thumbnails
			// and metadata may carry the removed content.
			src.Redacted = true
		case len(plan.annots[i]) > 0 || len(plan.annotDeny) > 0:
			src.Page = d.withoutAnnots(pi, plan.annots[i], plan.annotDeny)
		}
		sources[i] = src
	}
	// What was removed under a region must not survive through another
	// route (a shared resource dictionary, a form's resources).
	removed, err := d.redactionDenySet(outs, func(i int) parse.PageInfo { return sources[i].Page })
	if err != nil {
		return nil, nil, nil, err
	}
	deny := map[int]bool{}
	for _, n := range removed {
		deny[n] = true
	}
	for n := range plan.annotDeny {
		deny[n] = true
	}
	// A redacted page's annotations (and their popups) are denied by the
	// copier too: a reference to one is dropped from another page's /Annots.
	for i := range outs {
		for _, n := range d.pageAnnotObjects(i) {
			deny[n] = true
		}
	}
	if len(sources) > 0 {
		list := make([]int, 0, len(deny))
		for n := range deny {
			list = append(list, n)
		}
		sort.Ints(list)
		sources[0].Deny = list
	}
	info := buildInfoDict(d)
	for k := range plan.info {
		delete(info, parse.Name(k))
	}
	built, err := parse.BuildDocument(sources, info)
	if err != nil {
		// The copier's error can quote the file (a token of an object that
		// does not parse); it is not reported.
		return nil, nil, nil, fmt.Errorf("%w: the redacted document cannot be built", ErrInvalidPDF)
	}
	return built, outs, deny, nil
}

// withoutAnnots returns pi with its /Annots lacking the entries at the
// indices in drop and the references to objects in deny (a copy of the page
// dictionary; the source is unchanged). pi is returned as it is when nothing
// is dropped.
func (d *Document) withoutAnnots(pi parse.PageInfo, drop map[int]string, deny map[int]bool) parse.PageInfo {
	v, err := d.resolver.Resolve(pi.Dict["Annots"])
	if err != nil {
		return pi
	}
	arr, _ := v.(parse.Array)
	kept := make(parse.Array, 0, len(arr))
	for j, a := range arr {
		_, gone := drop[j]
		if ref, ok := a.(parse.Reference); ok && deny[ref.Number] {
			gone = true
		}
		if !gone {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(arr) {
		return pi
	}
	dict := make(parse.Dict, len(pi.Dict))
	for k, val := range pi.Dict {
		dict[k] = val
	}
	dict["Annots"] = kept
	pi.Dict = dict
	return pi
}

// pageAnnotObjects returns the object numbers of page i's annotations and
// their popups.
func (d *Document) pageAnnotObjects(i int) []int {
	var out []int
	v, err := d.resolver.Resolve(d.pageInfos[i].Dict["Annots"])
	if err != nil {
		return nil
	}
	arr, _ := v.(parse.Array)
	for _, a := range arr {
		if ref, ok := a.(parse.Reference); ok {
			out = append(out, ref.Number)
		}
		if ad, ok, err := d.resolver.ResolveDict(a); err == nil && ok {
			if p, ok := ad["Popup"].(parse.Reference); ok {
				out = append(out, p.Number)
			}
		}
	}
	return out
}

// redactionPage reads a page for redaction: its content (PageContentSegments,
// so a token, comment or inline image split across the streams of a
// /Contents array is refused — viewers that concatenate the streams,
// separate them or parse each alone would show different text), scanned once
// with the strict scanner, inline-image colour space names resolved through
// the page's resource scopes, and every route to another content stream
// walked (redact.AnalyzePage). A model is read once per document and kept
// (redact_cache.go).
//
// Returns the page model, or the failure as redactionScanError reports it.
func (d *Document) redactionPage(pageIndex int) (*redact.Page, error) {
	pi := d.pageInfos[pageIndex]
	cache := d.modelCache()
	if page := cache.get(pageObjectNumber(pi)); page != nil {
		return page, nil
	}
	seg, err := d.resolver.PageContentSegments(pi)
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	page, err := redact.AnalyzePage(d.resolver, pi, seg)
	if err != nil {
		return nil, redactionScanError(pageIndex, err)
	}
	cache.put(pageObjectNumber(pi), page)
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

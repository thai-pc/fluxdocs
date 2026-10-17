package core

import (
	"errors"
	"fmt"
	"sort"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact_verify.go is the mandatory post-redaction verification (docs/design/
// redaction-hardening.md §5.6, decision D3): RedactAndFlatten re-opens its own
// output and refuses to return it while it still holds what was removed.
// There is no option to skip it.
//
//  1. Every redacted page is read again with the strict model: it must read
//     with no loss (we wrote it), no glyph run may meet a RedactArea region,
//     no text may match a RedactText pattern used on the page, and no text
//     may hold a propagating needle.
//  2. Every object is classified: R_m (reachable from redacted pages), R_u
//     (from the other pages), owned = R_m \ R_u, orphans (reachable from no
//     page, nor the catalog, page tree or /Info). Owned objects and orphans
//     are searched for every needle — the text of every removed run and
//     stripped property value too — read the way each object holds text
//     (redact.ObjectSearch). A hit is a bug: ErrRedactionVerification.
//  3. When there are propagating needles, what the other pages show (their
//     text, read with fonts, through every unit), their annotations, the
//     other objects they reach (by bytes) and /Info are searched for them. A
//     hit is a residual: automatic extension (redact_extend.go) attributes
//     it — the page's text, an annotation, the page's other objects (page
//     hygiene), an /Info field — and the plan is extended and rebuilt. A
//     needle in a unit Phase 1 cannot rewrite is that unit's refusal.
//  4. The output catalog holds only the page tree: outlines, XMP metadata,
//     the structure tree, name trees, the AcroForm and optional content are
//     not carried, and a change that carries them fails here until the
//     extension covers them.

// maxVerifyObjects bounds the objects verification classifies.
const maxVerifyObjects = 1 << 22

// errNotConverged is the verification failure when automatic extension
// still finds removed text after maxExtendRounds more builds.
var errNotConverged = fmt.Errorf("%w: automatic extension did not converge (not_converged)", ErrRedactionVerification)

// verificationError is a verification failure; the detail never holds
// document text.
func verificationError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRedactionVerification}, args...)...)
}

// residuals are the occurrences of propagating needles verification found
// outside the redacted pages, by location (output page and annotation
// indices; pages are in the same order as the source).
type residuals struct {
	content map[int]bool            // a page's text
	hygiene map[int]uint8           // a page's other objects: tags
	annots  map[int]map[int]uint8   // page -> index in its output /Annots -> tags
	info    map[string]bool         // /Info fields
	units   map[int]*redact.Refusal // pages showing a needle through a unit Phase 1 does not rewrite
}

func (r *residuals) empty() bool {
	return len(r.content) == 0 && len(r.hygiene) == 0 && len(r.annots) == 0 && len(r.info) == 0 && len(r.units) == 0
}

// verifyRedaction verifies built (see the file comment) and returns it
// opened, with the residuals that extend the plan.
//
// Returns ErrRedactionVerification (wrapped) for a failure, or a
// *RedactionError (budget_exceeded) past the call's budget.
func (d *Document) verifyRedaction(built []byte, plan *redactPlan, ns *needleSets, prepassed bool, budget *callBudget) (*Document, *residuals, error) {
	out, err := OpenBytes(built)
	if err != nil {
		return nil, nil, verificationError("the redacted output does not open")
	}
	r := out.resolver
	// A document with no page builds an empty page tree, which the lenient
	// reader opens as one empty page.
	if len(out.pageInfos) != len(d.pageInfos) && len(d.pageInfos) > 0 {
		return nil, nil, verificationError("the redacted output has %d pages, want %d", len(out.pageInfos), len(d.pageInfos))
	}
	cat, err := r.Catalog()
	if err != nil {
		return nil, nil, verificationError("the redacted output has no catalog")
	}
	for k := range cat {
		if k != "Type" && k != "Pages" {
			return nil, nil, verificationError("the redacted output's catalog carries more than the page tree")
		}
	}
	res := &residuals{content: map[int]bool{}, hygiene: map[int]uint8{}, annots: map[int]map[int]uint8{},
		info: map[string]bool{}, units: map[int]*redact.Refusal{}}

	// 1. The redacted pages, read again strictly.
	var decoded int64
	for i, pi := range out.pageInfos {
		if !plan.redacted(i) {
			continue
		}
		seg, err := r.PageContentSegments(pi)
		if err != nil {
			return nil, nil, verificationError("page %d of the redacted output cannot be read", i)
		}
		page, err := redact.AnalyzePage(r, pi, seg)
		if err != nil {
			var rf *redact.Refusal
			if errors.As(err, &rf) && rf.Reason == redact.ReasonBudgetExceeded {
				return nil, nil, redactionScanError(i, err)
			}
			return nil, nil, verificationError("page %d of the redacted output does not read strictly", i)
		}
		decoded += page.Decoded()
		if err := budget.spent(decoded); err != nil {
			return nil, nil, err
		}
		if page.RunsMeet(toRedactRects(plan.regions[i])) {
			return nil, nil, verificationError("a glyph run of page %d meets a redaction region", i)
		}
		for _, re := range plan.patterns[i] {
			if m := page.Match(re); len(m.Marks) > 0 {
				return nil, nil, verificationError("text of page %d still matches a RedactText pattern", i)
			}
		}
		if marks, _ := page.FindNeedles(ns.prop, nil); len(marks) > 0 {
			return nil, nil, verificationError("page %d still shows removed text", i)
		}
		if page.HasPropertyText() {
			return nil, nil, verificationError("a marked-content property list of page %d keeps its text", i)
		}
		if page.HiddenTags(ns.all, ns.raw) != 0 {
			return nil, nil, verificationError("the content of page %d holds removed text outside what it shows", i)
		}
	}
	budget.used += decoded

	// 3a. What the other pages show and their annotations hold, read with
	// fonts, when the pre-pass did not read the same content in the source:
	// the copy of a page that is not redacted shows what the source page
	// shows (its content, resources and annotations are copied as they
	// are; what the deny set removes, no kept content paints), so reading it
	// again would repeat the pre-pass. The byte-level search (3b) still
	// reads every object those pages reach.
	var walked map[*parse.Stream]bool
	if ns.prop.Len() > 0 && !prepassed {
		sc := redact.NewTextScanner(r, ns.prop)
		for i, pi := range out.pageInfos {
			if plan.redacted(i) {
				continue
			}
			hits, err := sc.Page(pi)
			if err != nil {
				return nil, nil, extensionScanError(i, err)
			}
			if hits.Unit != nil {
				res.units[i] = hits.Unit
			} else if hits.Disputed != nil {
				res.units[i] = hits.Disputed
			}
			if hits.Content {
				res.content[i] = true
			}
			if hits.Hidden != 0 {
				res.hygiene[i] |= hits.Hidden
			}
			annots, _ := r.Resolve(pi.Dict["Annots"])
			arr, _ := annots.(parse.Array)
			for j, a := range arr {
				ad, ok, err := r.ResolveDict(a)
				if err != nil || !ok {
					continue
				}
				hit, err := sc.Annotation(pi, ad)
				if err != nil {
					return nil, nil, extensionScanError(i, err)
				}
				if hit {
					res.addAnnot(i, j, tagMatch|tagArea)
				}
			}
			if err := budget.spent(sc.Decoded); err != nil {
				return nil, nil, err
			}
		}
		budget.used += sc.Decoded
		walked = sc.Walked
	}
	if ns.prop.Len() > 0 {
		if info := r.Trailer()["Info"]; info != nil {
			if id, ok, err := r.ResolveDict(info); err == nil && ok {
				for _, k := range []parse.Name{"Title", "Author", "Subject"} {
					if s, ok := id[k].(parse.String); ok && ns.prop.FindBytes(s.Value, nil) != 0 {
						res.info[string(k)] = true
					}
				}
			}
		}
	}

	// 2 and 3b. Every object, by bytes.
	if ns.all.Len() > 0 || ns.raw.Len() > 0 {
		if err := d.searchObjects(out, plan, ns, walked, res, budget); err != nil {
			return nil, nil, err
		}
	}
	return out, res, nil
}

// addAnnot records an annotation residual.
func (res *residuals) addAnnot(page, idx int, tags uint8) {
	if res.annots[page] == nil {
		res.annots[page] = map[int]uint8{}
	}
	res.annots[page][idx] |= tags
}

// searchObjects is checks 2 and 3b of verifyRedaction: owned objects and
// orphans for every needle (a hit fails), the objects only the other pages
// reach for the propagating needles (a hit is a residual, attributed to the
// pages and annotations that reach it). Content streams walked (with fonts)
// are not searched by bytes again for the propagating needles.
//
// A content stream whose bytes the pre-pass read with fonts in the source
// (ns.read) is not searched again either: its strings read as Latin-1 are
// one of the readings the pre-pass matched.
func (d *Document) searchObjects(out *Document, plan *redactPlan, ns *needleSets, walked map[*parse.Stream]bool, res *residuals, budget *callBudget) error {
	r := out.resolver
	roles, err := redact.ObjectRoles(r, maxVerifyObjects)
	if err != nil {
		return &RedactionError{Page: -1, Reason: RedactionBudgetExceeded, Detail: "the redacted output has too many objects to verify"}
	}
	// A page object belongs to its own side (closeInto does not add page
	// objects): its direct values (a direct annotation, /PieceInfo) are
	// searched with it.
	inM, inU := map[int]bool{}, map[int]bool{}
	for i, pi := range out.pageInfos {
		set := inU
		if plan.redacted(i) {
			set = inM
		}
		if n := pi.ObjectNumber(); n > 0 {
			set[n] = true
		}
		if _, err := closeInto(r, set, pageRoots(pi), closeAll); err != nil {
			return &RedactionError{Page: -1, Reason: RedactionBudgetExceeded, Detail: "the redacted output has too many objects to verify"}
		}
	}
	// The structure: the catalog, the page tree, /Info.
	structural := map[int]bool{}
	if ref, ok := r.Trailer()["Root"].(parse.Reference); ok {
		structural[ref.Number] = true
	}
	if ref, ok := r.Trailer()["Info"].(parse.Reference); ok {
		structural[ref.Number] = true
	}
	maxNum := r.MaxObjectNumber()
	for num := 1; num <= maxNum; num++ {
		if obj, err := r.Resolve(parse.Reference{Number: num}); err == nil {
			if d, ok := obj.(parse.Dict); ok {
				if t, _ := d["Type"].(parse.Name); t == "Pages" {
					structural[num] = true
				}
			}
		}
	}

	// Strings shown by content streams of the other pages are read as
	// Latin-1, a reading the pre-pass's text model matched: a hit there is
	// then one the pre-pass would have found (attributable to the page).
	search := &redact.ObjectSearch{R: r, Budget: maxRedactCallDecoded - budget.used}
	uHits := map[int]uint8{} // objects of R_u holding a propagating needle: their tags
	for num := 1; num <= maxNum; num++ {
		if structural[num] {
			continue
		}
		role, reached := roles[num]
		if !reached {
			role = redact.RoleData | redact.RoleUnreached
		}
		switch {
		case inU[num]:
			if ns.prop.Len() == 0 {
				continue
			}
			if role&redact.RoleContent != 0 {
				if obj, err := r.Resolve(parse.Reference{Number: num}); err == nil {
					if st, ok := obj.(*parse.Stream); ok && (walked[st] || ns.read.has(st)) {
						continue
					}
				}
			}
			search.Latin1Shown = true
			tags, err := search.Search(num, role, ns.prop, nil)
			switch {
			case errors.Is(err, redact.ErrUndecodable):
				// What it holds is unknown: treat it as holding every
				// needle, so the page or annotation that carries it drops
				// it (page hygiene, annotation removal), or the redaction
				// fails if neither can.
				uHits[num] = tagMatch | tagArea
			case err != nil:
				return budgetRefusal(err)
			case tags != 0:
				uHits[num] = tags
			}
		default: // owned (inM only) or an orphan
			if inM[num] && isRewrittenContent(out, plan, num) {
				continue // checked by the strict re-read (1)
			}
			search.Latin1Shown = false
			tags, err := search.Search(num, role, ns.all, ns.raw)
			if errors.Is(err, redact.ErrUndecodable) {
				return &RedactionError{Page: -1, Reason: RedactionContentAmbiguous,
					Detail: fmt.Sprintf("object %d of the output, which only redacted pages keep, does not decode, so it cannot be verified", num)}
			}
			if err != nil {
				return budgetRefusal(err)
			}
			if tags != 0 {
				where := "an orphan object"
				if inM[num] {
					where = "an object only redacted pages reach"
				}
				return verificationError("removed text survives in %s (object %d of the output)", where, num)
			}
		}
	}
	budget.used = maxRedactCallDecoded - search.Budget
	if len(uHits) > 0 {
		return attributeObjects(out, plan, ns, uHits, res)
	}
	return nil
}

// budgetRefusal reports an object search past the call's budget.
func budgetRefusal(err error) error {
	return &RedactionError{Page: -1, Reason: RedactionBudgetExceeded,
		Detail: "verifying the redacted output exceeds the redaction decode budget", Err: err}
}

// isRewrittenContent reports whether object num of the output is the
// content stream of a redacted page (the rewritten content).
func isRewrittenContent(out *Document, plan *redactPlan, num int) bool {
	for i, pi := range out.pageInfos {
		if !plan.redacted(i) {
			continue
		}
		switch c := pi.Dict["Contents"].(type) {
		case parse.Reference:
			if c.Number == num {
				return true
			}
		case parse.Array:
			for _, e := range c {
				if ref, ok := e.(parse.Reference); ok && ref.Number == num {
					return true
				}
			}
		}
	}
	return false
}

// maxAttributionVisits bounds the work of attributing residuals: the
// references followed to build the graph of what the pages that are not
// redacted reach, and the reverse walk from the hits (the deny set's call
// budget, maxPainterOps).
const maxAttributionVisits = 32 << 20

// attributeObjects attributes objects of R_u that hold a propagating needle:
// to each page that is not redacted and reaches one other than through its
// annotations (page hygiene), and to each annotation that reaches one. The
// graph of what those pages reach is built once and walked backwards from
// the hits (as the deny set does), so the cost is one pass over the graph,
// not one closure per page and annotation.
//
// Returns a *RedactionError (budget_exceeded) when the graph or the walk
// exceeds its bound.
func attributeObjects(out *Document, plan *redactPlan, ns *needleSets, hits map[int]uint8, res *residuals) error {
	r := out.resolver
	type pageRoots struct {
		page   int
		body   []int
		annots [][]int
	}
	var pages []pageRoots
	var roots [][]int
	for i, pi := range out.pageInfos {
		if plan.redacted(i) {
			continue
		}
		// A hit in the page object itself: in an annotation written directly
		// in its /Annots, or elsewhere in the dictionary (/PieceInfo, …).
		if tags, hit := hits[pi.ObjectNumber()]; hit && pi.ObjectNumber() > 0 {
			annots, _ := r.Resolve(pi.Dict["Annots"])
			arr, _ := annots.(parse.Array)
			for j, a := range arr {
				if _, direct := a.(parse.Dict); direct && redact.ValueTags(a, ns.prop) != 0 {
					res.addAnnot(i, j, tags)
				}
			}
			rest := make(parse.Dict, len(pi.Dict))
			for k, v := range pi.Dict {
				if k != "Annots" && k != "Parent" {
					rest[k] = v
				}
			}
			if t := redact.ValueTags(rest, ns.prop); t != 0 {
				res.hygiene[i] |= t
			}
		}
		pr := pageRoots{page: i}
		for k, v := range pi.Dict {
			if k != "Parent" && k != "Annots" {
				pr.body = appendRefs(pr.body, v)
			}
		}
		if _, own := pi.Dict["Resources"]; !own {
			pr.body = appendRefs(pr.body, pi.Resources)
		}
		annots, _ := r.Resolve(pi.Dict["Annots"])
		arr, _ := annots.(parse.Array)
		for _, a := range arr {
			pr.annots = append(pr.annots, appendRefs(nil, a))
		}
		pages = append(pages, pr)
		roots = append(roots, pr.body)
		roots = append(roots, pr.annots...)
	}
	refusal := &RedactionError{Page: -1, Reason: RedactionBudgetExceeded,
		Detail: "attributing the removed text found in the redacted output exceeds the redaction budget"}
	g, err := buildGraph(r, roots)
	if err != nil {
		return refusal
	}
	// One reverse walk per tag value (at most three: the needle sources),
	// so each page and annotation is attributed the sources it reaches.
	byTags := map[uint8]map[int]bool{}
	for n, t := range hits {
		if byTags[t] == nil {
			byTags[t] = map[int]bool{}
		}
		byTags[t][n] = true
	}
	visits := g.edges
	for tags, targets := range byTags {
		reach, v := g.reachers(targets)
		if visits += v; visits > maxAttributionVisits {
			return refusal
		}
		reaches := func(rs []int) bool {
			for _, n := range rs {
				if reach[n] {
					return true
				}
			}
			return false
		}
		for _, pr := range pages {
			if reaches(pr.body) {
				res.hygiene[pr.page] |= tags
			}
			for j, a := range pr.annots {
				if reaches(a) {
					res.addAnnot(pr.page, j, tags)
				}
			}
		}
	}
	return nil
}

// applyResiduals extends the plan by what verification found (see
// residuals), and reports whether the plan changed.
//
// Returns the refusal of a page whose text holds a needle that Phase 1
// cannot remove, or ErrRedactionVerification when a residual cannot be
// attributed.
func (d *Document) applyResiduals(plan *redactPlan, res *residuals, ns *needleSets, models map[int]*redact.Page, deny map[int]bool) (bool, error) {
	pages := func(m map[int]bool) []int {
		var out []int
		for k := range m {
			out = append(out, k)
		}
		sort.Ints(out)
		return out
	}
	if units := sortedKeys(res.units); len(units) > 0 {
		i := units[0]
		return false, extensionRefusal(redactionScanError(i, res.units[i]))
	}
	if redactHooks.noopExtension {
		return true, nil
	}
	changed := false
	for _, i := range pages(res.content) {
		page := models[i]
		if page == nil {
			p, err := d.redactionPage(i)
			if err != nil {
				return false, extensionRefusal(err)
			}
			page = p
		}
		marks, hits := page.FindNeedles(ns.prop, nil)
		if len(marks) == 0 {
			return false, verificationError("page %d shows removed text the redaction cannot attribute", i)
		}
		models[i] = page
		plan.extend(i, marks, hits)
		changed = true
	}
	for _, i := range sortedKeys(res.hygiene) {
		if plan.redacted(i) {
			continue
		}
		plan.hygieneFor(i, res.hygiene[i])
		changed = true
	}
	if len(res.annots) > 0 {
		all := d.sourceAnnots()
		removed := map[int]string{}
		for _, i := range sortedKeys(res.annots) {
			kept := d.keptAnnots(plan, i, deny)
			for j := range res.annots[i] {
				if j >= len(kept) {
					return false, verificationError("an annotation of page %d holds removed text the redaction cannot attribute", i)
				}
				for _, ar := range all {
					if ar.page == i && ar.idx == kept[j] {
						plan.removeAnnot(ar, "needle", removed)
						changed = true
					}
				}
			}
		}
		plan.closeAnnots(all, removed)
	}
	for _, k := range []string{"Author", "Subject", "Title"} {
		if res.info[k] && !plan.info[k] {
			plan.removeInfo(k)
			changed = true
		}
	}
	return changed, nil
}

// keptAnnots returns, for page i that is not redacted, the indices in the
// source's /Annots of the annotations the output's /Annots lists, in order:
// the copier writes every entry but the removed ones and references to
// denied objects.
func (d *Document) keptAnnots(plan *redactPlan, i int, deny map[int]bool) []int {
	v, err := d.resolver.Resolve(d.pageInfos[i].Dict["Annots"])
	if err != nil {
		return nil
	}
	arr, _ := v.(parse.Array)
	var out []int
	for j, a := range arr {
		if _, gone := plan.annots[i][j]; gone {
			continue
		}
		if ref, ok := a.(parse.Reference); ok && deny[ref.Number] {
			continue
		}
		out = append(out, j)
	}
	return out
}

// sortedKeys returns the keys of an int-keyed map in order.
func sortedKeys[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

package core

import (
	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact_owned.go is what automatic extension and verification share about
// the objects only redacted pages keep (docs/design/redaction-hardening.md
// §5.6 check 2, §6.11, C85): which objects of a built output those are, and
// how each is searched for removed text (classifyOutput, searchOwned, used by
// searchObjects). Removed text there fails verification. When every hit is
// in a unit a redacted page paints (a form, tiling pattern, soft-mask group
// or Type 3 glyph procedure) or in an object such a unit uses, Phase 1
// cannot remove it — it does not rewrite units — and RedactAndFlatten
// reports that unit's refusal instead (attributeOwned). Attribution runs only
// on such a failure, on the output verification opened and with the hits it
// found: one search, one budget charge, and verification's checks are
// reported first. The verifier itself is unchanged: what it searches and
// what fails it are the same, and a hit no unit accounts for stays a
// verification failure. A removed text that does not propagate (a one-digit
// match, area text under the propagation threshold, a stripped property
// value) is searched for there too, as is every object a unit uses (its
// /Metadata, its resources), not only its content.

// outputObjects classifies the objects of a built output by the pages that
// reach them (verifyRedaction check 2).
type outputObjects struct {
	roles map[int]redact.Role
	// inM and inU are the objects the redacted pages and the other pages
	// reach (R_m, R_u); a page object belongs to its own side.
	inM, inU map[int]bool
	// structural are the catalog, the page tree nodes and /Info.
	structural map[int]bool
	maxNum     int
}

// classifyOutput classifies the objects of out, built for plan.
//
// Returns a *RedactionError (budget_exceeded) past the object bounds.
func classifyOutput(out *Document, plan *redactPlan) (*outputObjects, error) {
	r := out.resolver
	roles, err := redact.ObjectRoles(r, maxVerifyObjects)
	if err != nil {
		return nil, &RedactionError{Page: -1, Reason: RedactionBudgetExceeded, Detail: "the redacted output has too many objects to verify"}
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
			return nil, &RedactionError{Page: -1, Reason: RedactionBudgetExceeded, Detail: "the redacted output has too many objects to verify"}
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
	return &outputObjects{roles: roles, inM: inM, inU: inU, structural: structural, maxNum: maxNum}, nil
}

// role returns the role object num is read with: the routes that reach it,
// or its keys when nothing does.
func (o *outputObjects) role(num int) redact.Role {
	if role, reached := o.roles[num]; reached {
		return role
	}
	return redact.RoleData | redact.RoleUnreached
}

// searchOwned returns the tags of every needle (removed text and
// propagating needles, ns.all and ns.raw) object num holds, num being an
// object only redacted pages reach or an orphan. The rewritten content of a
// redacted page is not searched: verification reads it again strictly.
//
// Returns redact.ErrUndecodable for a stream that does not decode, or a
// budget refusal.
func searchOwned(out *Document, plan *redactPlan, o *outputObjects, search *redact.ObjectSearch, ns *needleSets, num int) (uint8, error) {
	if o.inM[num] && isRewrittenContent(out, plan, num) {
		return 0, nil
	}
	search.Latin1Shown = false
	return search.Search(num, o.role(num), ns.all, ns.raw)
}

// ownedHitError is verification's failure on removed text in an object
// only redacted pages reach or in an orphan (searchObjects). It is the
// verification error (Error, Unwrap) and carries what attribution needs: the
// output verification opened and the objects holding removed text, in
// object order. complete is false when the search stopped before every such
// object was read (an object that does not decode, the budget): attribution
// then keeps the verification error.
type ownedHitError struct {
	err      error
	out      *Document
	hits     []int
	complete bool
}

func (e *ownedHitError) Error() string { return e.err.Error() }
func (e *ownedHitError) Unwrap() error { return e.err }

// attributeOwned returns the refusal for a verification failure on removed
// text in objects only redacted pages reach, when every one of them is a
// unit a redacted page paints (by every route the strict model walks:
// redact.PaintedUnits) or an object such a unit uses: the first redacted
// page's, in page order, preferring a unit that holds the text itself to one
// that uses an object holding it. Otherwise — an orphan, an object no
// painted unit reaches, a search that did not complete, a page that does
// not read — it returns the verification error. Either way no document is
// returned.
func (d *Document) attributeOwned(plan *redactPlan, e *ownedHitError) error {
	defer e.out.Close()
	if !e.complete || len(e.hits) == 0 {
		return e.err
	}
	out := e.out
	r := out.resolver
	type painted struct {
		page  int
		units []redact.Unit
	}
	var pages []painted
	var roots [][]int
	for i, pi := range out.pageInfos {
		if !plan.redacted(i) {
			continue
		}
		seg, err := r.PageContentSegments(pi)
		if err != nil {
			return e.err
		}
		units, err := redact.PaintedUnits(r, pi, seg)
		if err != nil {
			return e.err
		}
		pages = append(pages, painted{page: i, units: units})
		nums := make([]int, len(units))
		for j, u := range units {
			nums[j] = u.Num
		}
		roots = append(roots, nums)
	}
	// What the units reach, with reverse edges.
	g, err := buildGraph(r, roots)
	if err != nil {
		return e.err
	}
	hits := map[int]bool{}
	for _, n := range e.hits {
		if !g.seen[n] {
			return e.err // no painted unit accounts for it
		}
		hits[n] = true
	}
	reach, _ := g.reachers(hits)
	for _, direct := range []bool{true, false} {
		for _, p := range pages {
			for _, u := range p.units {
				if (direct && hits[u.Num]) || (!direct && reach[u.Num]) {
					return extensionRefusal(redactionScanError(p.page, u.HeldRefusal()))
				}
			}
		}
	}
	return e.err
}

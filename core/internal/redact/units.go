package redact

import (
	"sort"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// units.go lists the units a page paints (forms, tiling patterns, soft-mask
// groups, Type 3 glyph procedures), read the way AnalyzePage reads them: the
// objects automatic extension attributes removed text to when an object only
// redacted pages keep holds it (docs/design/redaction-hardening.md §6.11).

// Unit is a content stream a page paints through another route.
type Unit struct {
	Num  int // its object number
	kind unitKind
}

// PaintedUnits returns the units the page paints, by every route the strict
// model walks, sorted by object number (a unit written directly, with no
// object number, is not listed). content is the page's content as
// r.PageContentSegments assembles it.
//
// Returns AnalyzePage's error when the page does not read strictly.
func PaintedUnits(r *parse.Resolver, pi parse.PageInfo, content parse.ContentSegments) ([]Unit, error) {
	w := newWalker(r)
	w.units = map[int]unitKind{}
	if _, err := analyzePage(w, pi, content); err != nil {
		return nil, err
	}
	out := make([]Unit, 0, len(w.units))
	for n, k := range w.units {
		out = append(out, Unit{Num: n, kind: k})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Num < out[j].Num })
	return out, nil
}

// HeldRefusal is the refusal for removed text held by the unit, or by an
// object it uses, that only redacted pages keep: Phase 1 does not rewrite
// units, so it cannot be removed. The detail names the unit's route, not its
// object number (the output's numbering means nothing to the caller).
func (u Unit) HeldRefusal() *Refusal {
	return refuse(u.kind.reason(), "automatic extension: the removed text is also held by a %s a redacted page paints, or an object it uses",
		u.kind).(*Refusal)
}

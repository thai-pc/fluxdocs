package redact

import "github.com/thai-pc/fluxdocs/core/internal/fontmodel"

// fontSet is the current font as the text model knows it: every font a
// reader may have selected. A Tf name bound by more than one scope, or only
// by a fallback scope, can select a different font in another reader (§6.2),
// so readings and boxes cover each of them (I1). A nil entry is "no font":
// text decodes as Latin-1, as extraction decodes a missing font, and metrics
// are the em defaults. The empty set is the nil font.
type fontSet []*fontmodel.Font

// fonts returns the set, with the nil font when it is empty.
func (s fontSet) fonts() fontSet {
	if len(s) == 0 {
		return fontSet{nil}
	}
	return s
}

// primary is the font the innermost scope selects (what ISO readers use).
func (s fontSet) primary() *fontmodel.Font { return s.fonts()[0] }

// codes splits raw by the primary font; it is certain only if every font of
// the set splits it the same way.
func (s fontSet) codes(raw []byte) ([]fontmodel.Code, bool) {
	fs := s.fonts()
	codes, ok := fs[0].Codes(raw)
	if !ok {
		return nil, false
	}
	for _, f := range fs[1:] {
		other, ok := f.Codes(raw)
		if !ok || len(other) != len(codes) {
			return nil, false
		}
		for i := range other {
			if other[i] != codes[i] {
				return nil, false
			}
		}
	}
	return codes, true
}

// advance is the hull of the fonts' advance bounds for c.
func (s fontSet) advance(c fontmodel.Code) iv {
	var out iv
	for i, f := range s.fonts() {
		lo, hi := f.Advance(c)
		out = out.union(iv{lo, hi}, i > 0)
	}
	return out
}

// unknownAdvance is the hull of the fonts' bounds for a code of unknown value.
func (s fontSet) unknownAdvance() iv {
	var out iv
	for i, f := range s.fonts() {
		lo, hi := f.UnknownAdvance()
		out = out.union(iv{lo, hi}, i > 0)
	}
	return out
}

// ink is the hull of the fonts' ink bounds (x0, y0, x1, y1).
func (s fontSet) ink() (x0, y0, x1, y1 float64) {
	for i, f := range s.fonts() {
		a, b, c, d := f.Ink()
		if i == 0 {
			x0, y0, x1, y1 = a, b, c, d
			continue
		}
		x0, y0, x1, y1 = min(x0, a), min(y0, b), max(x1, c), max(y1, d)
	}
	return x0, y0, x1, y1
}

// emInk is ink for a display box: the em defaults [0, 1.1] × [-0.25, 0.9]
// for every font but Type 3, whose glyphs are sized by /FontMatrix and so
// keep their /FontBBox (see textRun.disp).
func (s fontSet) emInk() (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = 0, -0.25, 1.1, 0.9
	for _, f := range s.fonts() {
		if f.Kind() == fontmodel.Type3 {
			a, b, c, d := f.Ink()
			x0, y0, x1, y1 = min(x0, a), min(y0, b), max(x1, c), max(y1, d)
		}
	}
	return x0, y0, x1, y1
}

// vertical reports whether any font of the set writes vertically.
func (s fontSet) vertical() bool {
	for _, f := range s.fonts() {
		if f.Vertical() {
			return true
		}
	}
	return false
}

// unbounded reports whether any font of the set cannot be bounded.
func (s fontSet) unbounded() bool {
	for _, f := range s.fonts() {
		if f.Unbounded() {
			return true
		}
	}
	return false
}

// maxCodeLen is the longest code any font of the set can have.
func (s fontSet) maxCodeLen() int {
	n := 1
	for _, f := range s.fonts() {
		n = max(n, f.MaxCodeLen())
	}
	return n
}

// union returns the fonts of s and t, without repeats.
func (s fontSet) union(t fontSet) fontSet {
	out := append(fontSet(nil), s.fonts()...)
	for _, f := range t.fonts() {
		dup := false
		for _, g := range out {
			if g == f {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, f)
		}
	}
	return out
}

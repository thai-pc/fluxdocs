package render

import (
	"math"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/internal/psfont"
	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// psEntry is a parsed PostScript-flavored program (Type 1 or bare CFF) and
// the glyphs built from it during this render, shared by every glyphFont
// over the same program stream.
type psEntry struct {
	font   *psfont.Font
	glyphs map[psfont.GID]builtGlyph
	// adaptive flattens the outlines' curves by their device size
	// (flattenCubicAdaptive) instead of into cubicSegments lines each. It
	// is set for CFF programs, whose outlines are all cubics; Type 1
	// programs keep the fixed flattening their pages were rendered with.
	adaptive bool
}

// builtGlyph is a cached glyph build: its outline and width in glyph space
// and the validated matrix from that space to text space at size 1 (the
// FontMatrix; for a CID-keyed CFF, its Font DICT's), or ok false when the
// build failed (the glyph is not drawn).
type builtGlyph struct {
	segs   []psfont.Segment
	width  float64
	matrix matrix
	ok     bool
}

// parsePSEntry parses a Type 1 program. The copies the parser makes (PFB
// segments stripped, hex decoded, eexec and charstrings decrypted) are
// charged to the budget's decoded bytes, like the stream decode before them.
func parsePSEntry(data []byte, b *budget) any {
	f, err := psfont.ParseType1(data, psfont.Options{Charge: b.chargeDecoded})
	if err != nil {
		return nil
	}
	return &psEntry{font: f, glyphs: map[psfont.GID]builtGlyph{}}
}

// parseCFFEntry parses a bare CFF program (/FontFile3 /Type1C or
// /CIDFontType0C). Charstrings stay in data, which the program cache keeps
// alive; the lookup tables the parser builds (charset, names, the CID map)
// are charged to the budget's decoded bytes.
func parseCFFEntry(data []byte, b *budget) any {
	f, err := psfont.ParseCFF(data, psfont.Options{Charge: b.chargeDecoded})
	if err != nil {
		return nil
	}
	return &psEntry{font: f, glyphs: map[psfont.GID]builtGlyph{}, adaptive: true}
}

// psProgram draws a font resource's glyphs from a psEntry. A simple font's
// codes map to glyphs through its encoding (codeGlyph, built at load); a
// composite font's codes are CIDs (the Identity case), mapped to glyphs by
// the program (psfont.Font.GlyphByCID: the charset of a CID-keyed CFF, GID =
// CID otherwise, §9.7.4.2).
type psProgram struct {
	fl        *fontLoader
	e         *psEntry
	twoByte   bool
	codeGlyph [256]int32 // simple fonts: GID per code, -1 = none
}

// newPSProgram builds the code → glyph map of a simple font over e (design
// §2.1): each code's glyph name comes from the /Differences, else the base
// encoding the font dictionary names, else the program's built-in encoding,
// else StandardEncoding; the name selects a /CharStrings entry, and a name
// the program lacks selects .notdef (drawn, usually empty). A CID-keyed CFF
// has no glyph names, so as a simple font it draws nothing (design §2.1).
func newPSProgram(fl *fontLoader, e *psEntry, gf *glyphFont) *psProgram {
	p := &psProgram{fl: fl, e: e, twoByte: gf.twoByte}
	if gf.twoByte {
		return p
	}
	builtin, hasBuiltin := e.font.BuiltinEncoding()
	names := glyphNames(gf.model, &builtin, hasBuiltin)
	notdef, hasNotdef := e.font.GlyphByName(".notdef")
	for c := range p.codeGlyph {
		p.codeGlyph[c] = -1
		if g, ok := e.font.GlyphByName(names[c]); ok && names[c] != "" {
			p.codeGlyph[c] = int32(g)
		} else if hasNotdef {
			p.codeGlyph[c] = int32(notdef)
		}
	}
	return p
}

// glyphNames returns the glyph name of every code of a simple font (§9.6.6.1,
// §9.6.6.2): the font dictionary's /Differences entry, else its base encoding
// (/Encoding or /BaseEncoding naming WinAnsiEncoding or MacRomanEncoding),
// else builtin when the program has one, else StandardEncoding. "" means the
// code is unassigned.
func glyphNames(model *fontmodel.Font, builtin *[256]string, hasBuiltin bool) [256]string {
	names := std14.StandardEncoding()
	if hasBuiltin {
		names = *builtin
	}
	if n, ok := model.BaseEncoding(); ok {
		if enc, ok := std14.BaseEncoding(string(n)); ok {
			names = enc
		}
	}
	for c := range names {
		if n, ok := model.Difference(byte(c)); ok {
			names[c] = string(n)
		}
	}
	return names
}

func (p *psProgram) glyph(code uint32) (uint32, bool) {
	if p.twoByte {
		g, ok := p.e.font.GlyphByCID(code)
		return uint32(g), ok
	}
	if code > 255 || p.codeGlyph[code] < 0 {
		return 0, false
	}
	return uint32(p.codeGlyph[code]), true
}

// build returns glyph gid's outline, built once per render: the operators
// its charstring runs are charged to raster work (charstringOpCost each)
// when it is built, whether the build succeeds or not, and a cached glyph
// costs nothing more. Past maxCachedSegments it is built (and charged)
// every time. ok is false when the build fails or the budget has stopped.
func (p *psProgram) build(gid uint32) (builtGlyph, bool) {
	id := psfont.GID(gid)
	if g, ok := p.e.glyphs[id]; ok {
		return g, g.ok
	}
	g, err := p.e.font.Outline(id)
	if !p.fl.b.chargePaint(int64(g.Ops) * charstringOpCost) {
		return builtGlyph{}, false
	}
	bg := builtGlyph{segs: g.Segs, width: g.Width, matrix: matrix(g.Matrix), ok: err == nil}
	if n := int64(len(bg.segs)) + 1; p.fl.cachedSegs+n <= maxCachedSegments {
		p.fl.cachedSegs += n
		p.e.glyphs[id] = bg
	}
	return bg, bg.ok
}

// advanceEm returns the program's advance (hsbw or sbw, the CFF width)
// mapped through the glyph's FontMatrix, or 0.5 em when the glyph cannot be
// built.
func (p *psProgram) advanceEm(gid uint32) float64 {
	g, ok := p.build(gid)
	if !ok {
		return 0.5
	}
	return float64(g.width * g.matrix[0])
}

// subpaths maps the glyph through the FontMatrix (not a fixed 1/1000: fonts
// with 2048-unit or skewed matrices exist), the text state, the text matrix
// and the CTM, with every product rounded explicitly (mulExact, applyExact)
// so native and wasm builds compute the same points.
func (p *psProgram) subpaths(gid uint32, x glyphXform) ([][]point, bool) {
	g, ok := p.build(gid)
	if !ok {
		return nil, false
	}
	textState := matrix{float64(x.fontSize * x.hScale), 0, 0, x.fontSize, 0, x.rise}
	full := mulExact(mulExact(mulExact(g.matrix, textState), x.tm), x.ctm)
	return outlineToSubpaths(g.segs, full, p.e.adaptive), true
}

// outlineToSubpaths converts a glyph-space outline to device-space polylines,
// transforming every point by m and flattening cubic curves: into
// cubicSegments lines each, or, when adaptive, into as few as keep the
// polyline within flattenTolerance of the curve (flattenCubicAdaptive).
func outlineToSubpaths(segs []psfont.Segment, m matrix, adaptive bool) [][]point {
	tp := func(q psfont.Point) point {
		x, y := applyExact(m, q.X, q.Y)
		return point{x, y}
	}
	var subs [][]point
	var cur []point
	var last point
	for _, s := range segs {
		switch s.Op {
		case psfont.MoveTo:
			if len(cur) > 0 {
				subs = append(subs, cur)
			}
			last = tp(s.P[0])
			cur = []point{last}
		case psfont.LineTo:
			last = tp(s.P[0])
			cur = append(cur, last)
		case psfont.CubeTo:
			c1, c2, e := tp(s.P[0]), tp(s.P[1]), tp(s.P[2])
			if adaptive {
				cur = flattenCubicAdaptive(cur, last, c1, c2, e)
			} else {
				cur = flattenCubic(cur, last, c1, c2, e)
			}
			last = e
		}
	}
	if len(cur) > 0 {
		subs = append(subs, cur)
	}
	return subs
}

// flattenTolerance is the largest distance, in device pixels, that
// flattenCubicAdaptive lets its polyline stray from the curve.
const flattenTolerance = 0.125

// flattenCubicAdaptive appends a polyline approximation of the cubic Bézier
// p0 p1 p2 p3 (p0 already in dst) of n uniform segments, n the smallest
// count, at most cubicSegments, that keeps it within flattenTolerance of the
// curve. A polyline of n uniform segments strays at most M / (8n²), where M
// bounds the length of the curve's second derivative, and M ≤ 6·L where L is
// the length of the larger second difference of the control points, so
// n = ⌈√(6L / (8·tol))⌉ = ⌈√(6L)⌉ at tol 1/8. A NaN or infinite L gives
// cubicSegments.
// At cubicSegments it computes exactly flattenCubic's points. Every
// operation is rounded separately (no fused multiply-add), so the count and
// the points are the same on every target.
func flattenCubicAdaptive(dst []point, p0, p1, p2, p3 point) []point {
	// The squared length of a second difference, each product rounded.
	d2 := func(a, b, c point) float64 {
		x, y := (a.X-b.X)-(b.X-c.X), (a.Y-b.Y)-(b.Y-c.Y)
		return float64(x*x) + float64(y*y)
	}
	l := math.Sqrt(max(d2(p0, p1, p2), d2(p1, p2, p3)))
	n := cubicSegments
	if v := math.Ceil(math.Sqrt(float64(l * (0.75 / flattenTolerance)))); v < cubicSegments {
		n = max(int(v), 1)
	}
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		a := float64(u*u) * u
		b := float64(3*u*u) * t
		c := float64(3*u*t) * t
		e := float64(t*t) * t
		dst = append(dst, point{
			X: float64(a*p0.X) + float64(b*p1.X) + float64(c*p2.X) + float64(e*p3.X),
			Y: float64(a*p0.Y) + float64(b*p1.Y) + float64(c*p2.Y) + float64(e*p3.Y),
		})
	}
	return dst
}

// mulExact is matrix.mul with every product rounded to float64 before the
// sum. The Go spec lets a compiler fuse x*y + z into one multiply-add, which
// arm64 builds do and wasm builds do not; the explicit conversions forbid it,
// so glyph points are the same on every target (design §2.2, §6).
func mulExact(m, n matrix) matrix {
	return matrix{
		float64(m[0]*n[0]) + float64(m[1]*n[2]),
		float64(m[0]*n[1]) + float64(m[1]*n[3]),
		float64(m[2]*n[0]) + float64(m[3]*n[2]),
		float64(m[2]*n[1]) + float64(m[3]*n[3]),
		float64(m[4]*n[0]) + float64(m[5]*n[2]) + n[4],
		float64(m[4]*n[1]) + float64(m[5]*n[3]) + n[5],
	}
}

// applyExact is matrix.apply with explicit rounding (see mulExact).
func applyExact(m matrix, x, y float64) (float64, float64) {
	return float64(m[0]*x) + float64(m[2]*y) + m[4], float64(m[1]*x) + float64(m[3]*y) + m[5]
}

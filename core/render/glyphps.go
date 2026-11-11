package render

import (
	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/internal/psfont"
	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// psEntry is a parsed PostScript-flavored program (Type 1 today) and the
// glyphs built from it during this render, shared by every glyphFont over
// the same program stream.
type psEntry struct {
	font   *psfont.Font
	matrix matrix // the validated FontMatrix: glyph space to text space at size 1
	glyphs map[psfont.GID]builtGlyph
}

// builtGlyph is a cached glyph build: its outline and width in glyph space,
// or ok false when the build failed (the glyph is not drawn).
type builtGlyph struct {
	segs  []psfont.Segment
	width float64
	ok    bool
}

// parsePSEntry parses a Type 1 program. The copies the parser makes (PFB
// segments stripped, hex decoded, eexec and charstrings decrypted) are
// charged to the budget's decoded bytes, like the stream decode before them.
func parsePSEntry(data []byte, b *budget) any {
	f, err := psfont.ParseType1(data, psfont.Options{Charge: b.chargeDecoded})
	if err != nil {
		return nil
	}
	return &psEntry{font: f, matrix: matrix(f.Matrix()), glyphs: map[psfont.GID]builtGlyph{}}
}

// psProgram draws a font resource's glyphs from a psEntry. Codes map to
// glyphs through the font's encoding (codeGlyph, built at load).
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
// the program lacks selects .notdef (drawn, usually empty). A composite font
// uses its codes as glyph IDs.
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
	for c, n := range model.Differences() {
		names[c] = string(n)
	}
	return names
}

func (p *psProgram) glyph(code uint32) (uint32, bool) {
	if p.twoByte {
		return code, code < uint32(p.e.font.NumGlyphs())
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
	bg := builtGlyph{segs: g.Segs, width: g.Width, ok: err == nil}
	if n := int64(len(bg.segs)) + 1; p.fl.cachedSegs+n <= maxCachedSegments {
		p.fl.cachedSegs += n
		p.e.glyphs[id] = bg
	}
	return bg, bg.ok
}

// advanceEm returns the program's advance (hsbw or sbw) mapped through the
// FontMatrix, or 0.5 em when the glyph cannot be built.
func (p *psProgram) advanceEm(gid uint32) float64 {
	g, ok := p.build(gid)
	if !ok {
		return 0.5
	}
	return float64(g.width * p.e.matrix[0])
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
	full := mulExact(mulExact(mulExact(p.e.matrix, textState), x.tm), x.ctm)
	return outlineToSubpaths(g.segs, full), true
}

// outlineToSubpaths converts a glyph-space outline to device-space polylines,
// transforming every point by m and flattening cubic curves.
func outlineToSubpaths(segs []psfont.Segment, m matrix) [][]point {
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
			cur = flattenCubic(cur, last, c1, c2, e)
			last = e
		}
	}
	if len(cur) > 0 {
		subs = append(subs, cur)
	}
	return subs
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

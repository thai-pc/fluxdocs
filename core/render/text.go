package render

import (
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// drawText rasterizes a shown string with the current font and text state,
// advancing the text matrix by each glyph's width (plus character and word
// spacing). Invisible render modes (3, 7) advance without drawing. Every glyph,
// drawn or only advanced, is one budget step, so one Tj with a huge string is
// bounded like a huge operator list.
func (in *interp) drawText(raw []byte) {
	gf := in.gs.font
	if gf == nil || len(raw) == 0 {
		return
	}
	invisible := in.gs.renderMode == 3 || in.gs.renderMode == 7

	gf.forEachCode(raw, func(code uint32, isSpace bool) bool {
		if !in.b.step() {
			return false
		}
		gid, ok := gf.gid(code)
		if ok && !invisible {
			in.drawGlyph(gf, gid)
		}

		wEm := 0.5
		if ok {
			wEm = gf.advanceEm(gid)
		}
		tx := (wEm*in.gs.fontSize + in.gs.charSpace)
		if isSpace {
			tx += in.gs.wordSpace
		}
		tx *= in.gs.hScale
		in.tm = translate(tx, 0).mul(in.tm)
		return true
	})
}

// drawTextArray handles TJ: strings interleaved with numeric position
// adjustments. A number moves the pen by -adj/1000 * fontSize * hScale. Each
// array element is one budget step (on top of the glyphs its strings show).
func (in *interp) drawTextArray(operands []parse.Object) {
	var arr parse.Array
	for i := len(operands) - 1; i >= 0; i-- {
		if a, ok := operands[i].(parse.Array); ok {
			arr = a
			break
		}
	}
	for _, el := range arr {
		if !in.b.step() {
			return
		}
		switch v := el.(type) {
		case parse.String:
			in.drawText(v.Value)
		case parse.Integer, parse.Real:
			adj := -num(el) / 1000 * in.gs.fontSize * in.gs.hScale
			in.tm = translate(adj, 0).mul(in.tm)
		}
	}
}

// drawGlyph fills one glyph outline at the current text position. The glyph is
// loaded at glyphPPEM (so its coordinates are in thousandths of an em), scaled
// to text space by fontSize/1000, then mapped through the text matrix and CTM.
func (in *interp) drawGlyph(gf *glyphFont, gid sfnt.GlyphIndex) {
	segs, err := gf.sf.LoadGlyph(&gf.buf, gid, glyphPPEM, nil)
	if err != nil {
		return
	}
	sg := in.gs.fontSize / 1000.0
	// glyph -> text space (fontSize scale + horizontal scale + rise), then text
	// matrix, then CTM.
	glyphToText := matrix{sg * in.gs.hScale, 0, 0, sg, 0, in.gs.rise}
	full := glyphToText.mul(in.tm).mul(in.gs.ctm)

	subpaths := segmentsToSubpaths(segs, full)
	// Loading and flattening an outline costs more per point than fillPath's
	// fillPointCost setup charge: charge the difference, so a flood of tiny
	// visible glyphs is bounded like any other raster work.
	npts := 0
	for _, sp := range subpaths {
		npts += len(sp)
	}
	if !in.b.chargePaint(int64(npts) * glyphPointCost) {
		return
	}
	if len(subpaths) > 0 {
		in.rs.fillPath(subpaths, in.gs.fill, false) // nonzero winding (handles holes)
	}
}

// segmentsToSubpaths converts sfnt glyph segments to device-space polylines,
// transforming every point by m and flattening quadratic/cubic curves.
func segmentsToSubpaths(segs []sfnt.Segment, m matrix) [][]point {
	tp := func(p fixed.Point26_6) point {
		x, y := m.apply(float64(p.X)/64, float64(p.Y)/64)
		return point{x, y}
	}
	var subs [][]point
	var cur []point
	var last point
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			if len(cur) > 0 {
				subs = append(subs, cur)
			}
			last = tp(s.Args[0])
			cur = []point{last}
		case sfnt.SegmentOpLineTo:
			last = tp(s.Args[0])
			cur = append(cur, last)
		case sfnt.SegmentOpQuadTo:
			c := tp(s.Args[0])
			e := tp(s.Args[1])
			cur = flattenQuad(cur, last, c, e)
			last = e
		case sfnt.SegmentOpCubeTo:
			c1 := tp(s.Args[0])
			c2 := tp(s.Args[1])
			e := tp(s.Args[2])
			cur = flattenCubic(cur, last, c1, c2, e)
			last = e
		}
	}
	if len(cur) > 0 {
		subs = append(subs, cur)
	}
	return subs
}

// flattenQuad appends a polyline approximation of the quadratic Bézier from p0
// through control c to p1. p0 is assumed already present in dst.
func flattenQuad(dst []point, p0, c, p1 point) []point {
	const segments = 8
	for i := 1; i <= segments; i++ {
		t := float64(i) / segments
		u := 1 - t
		dst = append(dst, point{
			X: u*u*p0.X + 2*u*t*c.X + t*t*p1.X,
			Y: u*u*p0.Y + 2*u*t*c.Y + t*t*p1.Y,
		})
	}
	return dst
}

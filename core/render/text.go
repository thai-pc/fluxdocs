package render

import (
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// drawText rasterizes a shown string with the current font and text state,
// advancing the text matrix by each code's width w0 (glyphFont.advance: a
// simple font's /Widths) plus character and word spacing. Invisible render
// modes (3, 7) advance without painting; the clip modes (4–7) also add each
// glyph to the text object's clip (textclip.go), so a clip-mode string shown
// with no font discards that clip. Every glyph, drawn or only advanced, is
// one budget step, so one Tj with a huge string is bounded like a huge
// operator list.
func (in *interp) drawText(raw []byte) {
	gf := in.gs.font
	if len(raw) == 0 {
		return
	}
	mode := in.gs.renderMode
	clips := clipModes(mode)
	if gf == nil {
		if clips {
			in.spoilTextClip() // no outline to clip with (fail-visible)
		}
		return
	}
	paints := mode != 3 && mode != 7

	gf.forEachCode(raw, func(code uint32, isSpace bool) bool {
		if !in.b.step() {
			return false
		}
		gid, ok := gf.gid(code)
		switch {
		case clips:
			in.showClipGlyph(gf, gid, ok, paints)
		case ok && paints:
			in.drawGlyph(gf, gid)
		}

		tx := (gf.advance(code, gid, ok)*in.gs.fontSize + in.gs.charSpace)
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

// drawGlyph fills one glyph outline at the current text position.
func (in *interp) drawGlyph(gf *glyphFont, gid uint32) {
	if subpaths, ok := in.glyphOutline(gf, gid); ok && len(subpaths) > 0 {
		in.rs.fillPath(subpaths, in.gs.fill, false) // nonzero winding (handles holes)
	}
}

// glyphOutline returns one glyph's outline at the current text position, in
// device space: the program maps it from glyph space through the text state
// (font size, horizontal scale, rise), the text matrix and the CTM. ok is
// false when the outline cannot be built or the budget stops; an empty
// outline (a space) is ok.
func (in *interp) glyphOutline(gf *glyphFont, gid uint32) ([][]point, bool) {
	subpaths, ok := gf.prog.subpaths(gid, glyphXform{
		fontSize: in.gs.fontSize, hScale: in.gs.hScale, rise: in.gs.rise,
		tm: in.tm, ctm: in.gs.ctm,
	})
	if !ok {
		return nil, false
	}
	// Loading and flattening an outline costs more per point than fillPath's
	// fillPointCost setup charge: charge the difference, so a flood of tiny
	// glyphs is bounded like any other raster work.
	npts := 0
	for _, sp := range subpaths {
		npts += len(sp)
	}
	if !in.b.chargePaint(int64(npts) * glyphPointCost) {
		return nil, false
	}
	return subpaths, true
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

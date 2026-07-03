package render

import (
	"image/color"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// gstate is the subset of the PDF graphics state the Wave 1 rasterizer tracks:
// the current transform, the fill/stroke colors and line width, plus text state
// (font and the spacing parameters). Clipping, dash patterns, and blend modes
// are not yet modeled. The text matrix is NOT here — it lives on interp because
// BT resets it and q/Q do not save it.
type gstate struct {
	ctm       matrix
	fill      color.RGBA
	stroke    color.RGBA
	lineWidth float64 // user-space units

	// Text state (§9.3).
	font       *glyphFont
	fontSize   float64
	charSpace  float64 // Tc
	wordSpace  float64 // Tw
	hScale     float64 // Tz/100; 1.0 = normal
	leading    float64 // TL
	rise       float64 // Ts
	renderMode int     // Tr; 3 and 7 are invisible
}

// interp interprets a page's content operators onto a raster surface. It builds
// paths in device space (applying the CTM as points are added) and paints them
// on fill/stroke operators, and rasterizes text glyphs via the page's fonts.
// Image XObjects (Do) and shadings are ignored in Wave 1.
type interp struct {
	rs    *raster
	gs    gstate
	stack []gstate

	fonts map[parse.Name]*glyphFont
	tm    matrix // text matrix (set by Tm/Td/…, reset by BT)
	tlm   matrix // text line matrix

	// Current path, in device space. Each subpath is a polyline; closed marks
	// subpaths finished with h/re (relevant only to stroking).
	subpaths [][]point
	closed   []bool
	cur      []point // subpath under construction
	curX     float64 // current point, user space
	curY     float64
	startX   float64 // current subpath start, for closepath (h)
	startY   float64
}

// run interprets ops against the raster, using base as the initial CTM (PDF
// user space to device pixels) and fonts for text.
func run(rs *raster, base matrix, ops []parse.ContentOp, fonts map[parse.Name]*glyphFont) {
	in := &interp{
		rs: rs,
		gs: gstate{
			ctm:       base,
			fill:      color.RGBA{0, 0, 0, 0xff},
			stroke:    color.RGBA{0, 0, 0, 0xff},
			lineWidth: 1,
			hScale:    1,
		},
		fonts: fonts,
		tm:    identity(),
		tlm:   identity(),
	}
	for _, op := range ops {
		in.apply(op)
	}
}

func (in *interp) apply(op parse.ContentOp) {
	o := op.Operands
	switch op.Op {
	// --- graphics state ---
	case "q":
		in.stack = append(in.stack, in.gs)
	case "Q":
		if n := len(in.stack); n > 0 {
			in.gs = in.stack[n-1]
			in.stack = in.stack[:n-1]
		}
	case "cm":
		if len(o) >= 6 {
			in.gs.ctm = matrix{num(o[0]), num(o[1]), num(o[2]), num(o[3]), num(o[4]), num(o[5])}.mul(in.gs.ctm)
		}
	case "w":
		if len(o) >= 1 {
			in.gs.lineWidth = num(o[len(o)-1])
		}

	// --- path construction ---
	case "m":
		if len(o) >= 2 {
			in.moveTo(num(o[0]), num(o[1]))
		}
	case "l":
		if len(o) >= 2 {
			in.lineTo(num(o[0]), num(o[1]))
		}
	case "c":
		if len(o) >= 6 {
			in.curveTo(num(o[0]), num(o[1]), num(o[2]), num(o[3]), num(o[4]), num(o[5]))
		}
	case "v":
		if len(o) >= 4 { // first control point = current point
			in.curveTo(in.curX, in.curY, num(o[0]), num(o[1]), num(o[2]), num(o[3]))
		}
	case "y":
		if len(o) >= 4 { // second control point = endpoint
			in.curveTo(num(o[0]), num(o[1]), num(o[2]), num(o[3]), num(o[2]), num(o[3]))
		}
	case "re":
		if len(o) >= 4 {
			in.rect(num(o[0]), num(o[1]), num(o[2]), num(o[3]))
		}
	case "h":
		in.closeSub()

	// --- path painting ---
	case "f", "F":
		in.fill(false)
	case "f*":
		in.fill(true)
	case "S":
		in.strokeCurrent()
	case "s":
		in.closeSub()
		in.strokeCurrent()
	case "B", "b":
		if op.Op == "b" {
			in.closeSub()
		}
		in.fillKeep(false)
		in.strokeCurrent()
	case "B*", "b*":
		if op.Op == "b*" {
			in.closeSub()
		}
		in.fillKeep(true)
		in.strokeCurrent()
	case "n":
		in.clearPath()
	case "W", "W*":
		// Clipping is not modeled in Wave 1; the path is consumed by the
		// following painting operator (usually n) as usual.

	// --- color ---
	case "g":
		in.gs.fill = gray(o)
	case "G":
		in.gs.stroke = gray(o)
	case "rg":
		in.gs.fill = rgb(o)
	case "RG":
		in.gs.stroke = rgb(o)
	case "k":
		in.gs.fill = cmyk(o)
	case "K":
		in.gs.stroke = cmyk(o)
	case "sc", "scn":
		if c, ok := colorByComponents(o); ok {
			in.gs.fill = c
		}
	case "SC", "SCN":
		if c, ok := colorByComponents(o); ok {
			in.gs.stroke = c
		}

	// --- text state ---
	case "BT":
		in.tm = identity()
		in.tlm = identity()
	case "ET":
		// no-op; glyphs are drawn eagerly
	case "Tf":
		if len(o) >= 2 {
			if name, ok := o[len(o)-2].(parse.Name); ok {
				in.gs.font = in.fonts[name]
			}
			in.gs.fontSize = num(o[len(o)-1])
		}
	case "Tc":
		in.gs.charSpace = lastNum(o)
	case "Tw":
		in.gs.wordSpace = lastNum(o)
	case "Tz":
		in.gs.hScale = lastNum(o) / 100
	case "TL":
		in.gs.leading = lastNum(o)
	case "Ts":
		in.gs.rise = lastNum(o)
	case "Tr":
		in.gs.renderMode = int(lastNum(o))

	// --- text positioning ---
	case "Td":
		if len(o) >= 2 {
			in.textMove(num(o[0]), num(o[1]))
		}
	case "TD":
		if len(o) >= 2 {
			in.gs.leading = -num(o[1])
			in.textMove(num(o[0]), num(o[1]))
		}
	case "Tm":
		if len(o) >= 6 {
			in.tlm = matrix{num(o[0]), num(o[1]), num(o[2]), num(o[3]), num(o[4]), num(o[5])}
			in.tm = in.tlm
		}
	case "T*":
		in.textMove(0, -in.gs.leading)

	// --- text showing ---
	case "Tj":
		in.drawText(lastStr(o))
	case "'":
		in.textMove(0, -in.gs.leading)
		in.drawText(lastStr(o))
	case "\"":
		if len(o) >= 3 {
			in.gs.wordSpace = num(o[len(o)-3])
			in.gs.charSpace = num(o[len(o)-2])
		}
		in.textMove(0, -in.gs.leading)
		in.drawText(lastStr(o))
	case "TJ":
		in.drawTextArray(o)
	}
}

// textMove applies a Td-style relative move to the text line matrix.
func (in *interp) textMove(tx, ty float64) {
	in.tlm = translate(tx, ty).mul(in.tlm)
	in.tm = in.tlm
}

// lastNum returns the last numeric operand, or 0.
func lastNum(o []parse.Object) float64 {
	if len(o) == 0 {
		return 0
	}
	return num(o[len(o)-1])
}

// lastStr returns the bytes of the last String operand, or nil.
func lastStr(o []parse.Object) []byte {
	for i := len(o) - 1; i >= 0; i-- {
		if s, ok := o[i].(parse.String); ok {
			return s.Value
		}
	}
	return nil
}

// --- path helpers ---

func (in *interp) moveTo(x, y float64) {
	in.flushSub(false)
	dx, dy := in.gs.ctm.apply(x, y)
	in.cur = []point{{dx, dy}}
	in.curX, in.curY = x, y
	in.startX, in.startY = x, y
}

func (in *interp) lineTo(x, y float64) {
	if in.cur == nil {
		in.moveTo(x, y)
		return
	}
	dx, dy := in.gs.ctm.apply(x, y)
	in.cur = append(in.cur, point{dx, dy})
	in.curX, in.curY = x, y
}

func (in *interp) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	if in.cur == nil {
		in.moveTo(in.curX, in.curY)
	}
	p0x, p0y := in.gs.ctm.apply(in.curX, in.curY)
	p1x, p1y := in.gs.ctm.apply(x1, y1)
	p2x, p2y := in.gs.ctm.apply(x2, y2)
	p3x, p3y := in.gs.ctm.apply(x3, y3)
	in.cur = flattenCubic(in.cur, point{p0x, p0y}, point{p1x, p1y}, point{p2x, p2y}, point{p3x, p3y})
	in.curX, in.curY = x3, y3
}

func (in *interp) rect(x, y, w, h float64) {
	in.flushSub(false)
	pts := make([]point, 4)
	xs := [4][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	for i, c := range xs {
		dx, dy := in.gs.ctm.apply(c[0], c[1])
		pts[i] = point{dx, dy}
	}
	in.subpaths = append(in.subpaths, pts)
	in.closed = append(in.closed, true)
	in.curX, in.curY = x, y
	in.startX, in.startY = x, y
}

// closeSub marks the subpath under construction as closed.
func (in *interp) closeSub() {
	if len(in.cur) > 0 {
		in.curX, in.curY = in.startX, in.startY
		in.flushSub(true)
	}
}

// flushSub moves the subpath under construction into the completed set.
func (in *interp) flushSub(closed bool) {
	if len(in.cur) > 0 {
		in.subpaths = append(in.subpaths, in.cur)
		in.closed = append(in.closed, closed)
		in.cur = nil
	}
}

func (in *interp) clearPath() {
	in.subpaths = nil
	in.closed = nil
	in.cur = nil
}

func (in *interp) fill(evenOdd bool) {
	in.fillKeep(evenOdd)
	in.clearPath()
}

func (in *interp) fillKeep(evenOdd bool) {
	in.flushSub(false)
	in.rs.fillPath(in.subpaths, in.gs.fill, evenOdd)
}

func (in *interp) strokeCurrent() {
	in.flushSub(false)
	in.rs.strokePath(in.subpaths, in.closed, in.gs.stroke, in.gs.lineWidth*in.gs.ctm.scaleFactor())
	in.clearPath()
}

// --- operand and color helpers ---

// num converts a numeric operand to float64.
func num(o parse.Object) float64 {
	switch v := o.(type) {
	case parse.Integer:
		return float64(v)
	case parse.Real:
		return float64(v)
	default:
		return 0
	}
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 0xff
	}
	return uint8(v * 255)
}

func gray(o []parse.Object) color.RGBA {
	if len(o) < 1 {
		return color.RGBA{0, 0, 0, 0xff}
	}
	g := clampByte(num(o[len(o)-1]))
	return color.RGBA{g, g, g, 0xff}
}

func rgb(o []parse.Object) color.RGBA {
	if len(o) < 3 {
		return color.RGBA{0, 0, 0, 0xff}
	}
	n := len(o)
	return color.RGBA{clampByte(num(o[n-3])), clampByte(num(o[n-2])), clampByte(num(o[n-1])), 0xff}
}

func cmyk(o []parse.Object) color.RGBA {
	if len(o) < 4 {
		return color.RGBA{0, 0, 0, 0xff}
	}
	n := len(o)
	c, m, y, k := num(o[n-4]), num(o[n-3]), num(o[n-2]), num(o[n-1])
	return color.RGBA{
		clampByte((1 - c) * (1 - k)),
		clampByte((1 - m) * (1 - k)),
		clampByte((1 - y) * (1 - k)),
		0xff,
	}
}

// colorByComponents interprets an sc/scn operand list by counting its numeric
// components (1=gray, 3=RGB, 4=CMYK). A trailing pattern name is ignored.
func colorByComponents(o []parse.Object) (color.RGBA, bool) {
	var nums []parse.Object
	for _, e := range o {
		switch e.(type) {
		case parse.Integer, parse.Real:
			nums = append(nums, e)
		}
	}
	switch len(nums) {
	case 1:
		return gray(nums), true
	case 3:
		return rgb(nums), true
	case 4:
		return cmyk(nums), true
	default:
		return color.RGBA{}, false
	}
}

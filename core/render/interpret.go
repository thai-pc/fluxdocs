package render

import (
	"image/color"
	"math"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// gstate is the subset of the PDF graphics state the Wave 1 rasterizer tracks:
// the current transform, the fill/stroke colors and line width, the clip, plus
// text state (font and the spacing parameters). Dash patterns and blend modes
// are not yet modeled. The text matrix is NOT here — it lives on interp because
// BT resets it and q/Q do not save it.
type gstate struct {
	ctm       matrix
	fill      color.RGBA
	stroke    color.RGBA
	lineWidth float64 // user-space units

	// clip is the device-space clip (clip.go): a rectangle and an optional
	// shared mask. It lives in gstate so q/Q (and a Form XObject's implicit
	// q/Q) save and restore it. A form's /BBox, W/W* clip paths (§8.5.4) and
	// text objects shown in modes 4–7 (§9.3.6, textclip.go) narrow it.
	clip clipState

	// Text state (§9.3).
	font       *glyphFont
	fontSize   float64
	charSpace  float64 // Tc
	wordSpace  float64 // Tw
	hScale     float64 // Tz/100; 1.0 = normal
	leading    float64 // TL
	rise       float64 // Ts
	renderMode int     // Tr, 0–7 (textRenderMode); 3 and 7 are invisible
}

// interp interprets a page's content operators onto a raster surface. It builds
// paths in device space (applying the CTM as points are added) and paints them
// on fill/stroke operators, rasterizes text glyphs via the page's fonts, and
// draws a common subset of image XObjects (Do). Form XObjects and shadings are
// ignored in Wave 1.
type interp struct {
	rs    *raster
	b     *budget // == rs.b: interpreter steps, content bytes, path points (budget.go)
	gs    gstate
	stack []gstate

	// res and resources give the Do operator access to the object model and the
	// current resource scope so it can resolve XObject streams and fonts (§8.8).
	// resources and fonts are stack-scoped: on entry to a Form XObject they swap
	// to the form's /Resources and its fonts, then restore on exit (form.go).
	res       *parse.Resolver
	resources parse.Dict

	fonts *fontScope // the current content stream's /Font resources, loaded on first Tf
	tm    matrix     // text matrix (set by Tm/Td/…, reset by BT)
	tlm   matrix     // text line matrix

	// Form XObject recursion controls (SECURITY.md §2–3, see form.go).
	formDepth  int                            // current form nesting depth
	formPath   map[parse.Reference]bool       // XObject refs on the active recursion path (cycle guard)
	formBudget int                            // remaining form executions for this page render
	fontCache  map[parse.Reference]*fontScope // per-form font scopes, keyed by XObject reference
	stackFloor int                            // Q may not pop below this stack length (form q/Q isolation)
	skippedQ   int                            // q operators ignored past maxSaveDepth, awaiting their Q

	// Current path, in device space. Each subpath is a polyline; closed marks
	// subpaths finished with h/re (relevant only to stroking).
	subpaths [][]point
	closed   []bool
	cur      []point // subpath under construction
	curX     float64 // current point, user space
	curY     float64
	startX   float64 // current subpath start, for closepath (h)
	startY   float64

	// pathPoints counts the points in subpaths and cur, bounded by the budget's
	// path cap; painting or n resets it.
	pathPoints int64

	// pendingClip is the W (clipNonzero) or W* (clipEvenOdd) mark on the
	// current path, applied by the next painting operator after it paints
	// (clip.go). Like the path, it is not part of the graphics state: q/Q do
	// not touch it, and a form saves and clears it.
	pendingClip int8

	// formClips holds the clip mask of each active form's saved state, which
	// the form restores on exit (clip-mask liveness, clip.go).
	formClips []*clipMask

	// textClip is the clip accumulated by the open text object from the
	// glyphs it shows in modes 4–7, applied at ET (textclip.go); nil until
	// its first clip-mode glyph. Like the path, it is not graphics state.
	textClip *textClip
}

// maxSaveDepth caps the graphics-state stack (q nesting). ISO 32000-1 Annex C
// suggests a limit of 28 for conforming readers; real files rarely exceed a
// few dozen. Without a cap 8 Mi q operators (well within MaxOperators) saved
// 8 Mi graphics states, ≈ 3 GB, fatal under js/wasm's 4 GiB memory.
const maxSaveDepth = 1024

// run interprets a page's ops against the raster, using base as the initial CTM
// (PDF user space to device pixels). res and resources let the Do operator
// resolve image and form XObjects, and Tf the fonts, from the page's
// /Resources; both may be nil, in which case Do and text are no-ops. run is
// the top-level entry point: it builds the interpreter (initialising the
// form-recursion controls, the full-canvas clip and the render's font loader)
// and hands the operator loop to interpret.
func run(rs *raster, base matrix, ops []parse.ContentOp, res *parse.Resolver, resources parse.Dict) {
	in := newInterp(rs, base, res, resources)
	in.interpret(ops)
	in.dropTextClip() // a text object without ET narrows nothing
}

// newInterp returns the page-level interpreter run uses, before any operator.
func newInterp(rs *raster, base matrix, res *parse.Resolver, resources parse.Dict) *interp {
	fonts := newFontLoader(res, rs.b).scope(resources)
	return &interp{
		rs: rs,
		b:  rs.b,
		gs: gstate{
			ctm:       base,
			fill:      color.RGBA{0, 0, 0, 0xff},
			stroke:    color.RGBA{0, 0, 0, 0xff},
			lineWidth: 1,
			hScale:    1,
			clip:      fullClipState(rs.w, rs.h),
		},
		res:        res,
		resources:  resources,
		fonts:      fonts,
		tm:         identity(),
		tlm:        identity(),
		formPath:   map[parse.Reference]bool{},
		formBudget: maxFormBudget,
		fontCache:  map[parse.Reference]*fontScope{},
	}
}

// interpret runs the operator loop. It is the shared interpretation core used by
// both the page (from run) and every Form XObject (from execForm), so there is a
// single operator switch reused at every nesting level. Each operator is one
// budget step; the loop stops at the first operator after the budget stops.
func (in *interp) interpret(ops []parse.ContentOp) {
	for _, op := range ops {
		if !in.b.step() {
			return
		}
		in.apply(op)
	}
}

func (in *interp) apply(op parse.ContentOp) {
	o := op.Operands
	switch op.Op {
	// --- graphics state ---
	case "q":
		// Past maxSaveDepth a q saves nothing: it is counted, and the Q that
		// matches it is swallowed, so a hostile stream of q cannot grow the
		// stack without bound while a balanced deep nest still pairs up. State
		// changes made inside such an ignored pair are not undone.
		if len(in.stack) >= maxSaveDepth {
			in.skippedQ++
			break
		}
		in.stack = append(in.stack, in.gs)
	case "Q":
		// Q inside a text object (which ISO does not allow) discards its
		// clip accumulation (textclip.go): the clip it restores may be wider
		// than the box the accumulation covers.
		if in.textClip != nil {
			in.spoilTextClip()
		}
		if in.skippedQ > 0 {
			in.skippedQ--
			break
		}
		// Refuse to pop below stackFloor so a stray Q inside a Form XObject cannot
		// steal a caller's saved frame (form q/Q isolation, form.go).
		// restoreState also re-syncs the raster: Q may restore a wider clip.
		if len(in.stack) > in.stackFloor {
			in.restoreState()
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
		in.endPath()
	case "W":
		// Deferred (§8.5.4): the next painting operator paints with the old
		// clip, then intersects it with this path (applyPendingClip). The
		// last of W and W* before it sets the rule; more path construction
		// in between keeps the mark (design Q3).
		in.pendingClip = clipNonzero
	case "W*":
		in.pendingClip = clipEvenOdd

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
		in.dropTextClip() // a text object left open narrows nothing
		in.tm = identity()
		in.tlm = identity()
	case "ET":
		// Glyphs are drawn eagerly; ET applies the text object's clip, if
		// it showed glyphs in a clip mode (textclip.go).
		in.endTextClip()
	case "Tf":
		if len(o) >= 2 {
			if name, ok := o[len(o)-2].(parse.Name); ok {
				in.gs.font = in.fonts.font(name)
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
		in.gs.renderMode = textRenderMode(o)

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

	// --- XObjects ---
	case "Do":
		if len(o) >= 1 {
			if name, ok := o[len(o)-1].(parse.Name); ok {
				in.doXObject(name)
			}
		}
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

// textRenderMode returns the text rendering mode a Tr operand list sets
// (§9.3.6, Table 106): its last operand when that is exactly an integer 0–7
// (an integer-valued real such as 7.0 included), else 0, fill. Truncating
// instead read 4.5 as a clip mode and 3.5 as invisible text; Ghostscript
// renders any non-integer or out-of-range mode as fill. For a well-formed
// single operand, redaction's text model takes the same exact integers as
// modes and treats any other operand as undefined, a superset of fill
// (docs/design/redaction-hardening.md C54); it rejects other operand counts,
// where this reads the last.
func textRenderMode(o []parse.Object) int {
	if v := lastNum(o); v == math.Trunc(v) && v >= 0 && v <= 7 {
		return int(v)
	}
	return 0
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

// addPathPoints reserves n more points in the current path. It reports false,
// tripping the budget, when the path would exceed the path-point cap; the
// caller then drops the operator.
func (in *interp) addPathPoints(n int64) bool {
	if !in.b.checkPathPoints(in.pathPoints + n) {
		return false
	}
	in.pathPoints += n
	return true
}

func (in *interp) moveTo(x, y float64) {
	if !in.addPathPoints(1) {
		return
	}
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
	if !in.addPathPoints(1) {
		return
	}
	dx, dy := in.gs.ctm.apply(x, y)
	in.cur = append(in.cur, point{dx, dy})
	in.curX, in.curY = x, y
}

func (in *interp) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	if in.cur == nil {
		in.moveTo(in.curX, in.curY)
		if in.cur == nil {
			return // the cap refused the implicit moveto
		}
	}
	if !in.addPathPoints(cubicSegments) {
		return
	}
	p0x, p0y := in.gs.ctm.apply(in.curX, in.curY)
	p1x, p1y := in.gs.ctm.apply(x1, y1)
	p2x, p2y := in.gs.ctm.apply(x2, y2)
	p3x, p3y := in.gs.ctm.apply(x3, y3)
	in.cur = flattenCubic(in.cur, point{p0x, p0y}, point{p1x, p1y}, point{p2x, p2y}, point{p3x, p3y})
	in.curX, in.curY = x3, y3
}

func (in *interp) rect(x, y, w, h float64) {
	if !in.addPathPoints(4) {
		return
	}
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
	in.pathPoints = 0
}

// endPath ends the current path at a painting operator (or n): it applies a
// pending W/W* clip with the path, then clears it.
func (in *interp) endPath() {
	in.applyPendingClip()
	in.clearPath()
}

func (in *interp) fill(evenOdd bool) {
	in.fillKeep(evenOdd)
	in.endPath()
}

func (in *interp) fillKeep(evenOdd bool) {
	in.flushSub(false)
	in.rs.fillPath(in.subpaths, in.gs.fill, evenOdd)
}

func (in *interp) strokeCurrent() {
	in.flushSub(false)
	in.rs.strokePath(in.subpaths, in.closed, in.gs.stroke, in.gs.lineWidth*in.gs.ctm.scaleFactor())
	in.endPath()
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

package redact

import (
	"bytes"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// textmodel.go interprets one content unit (docs/design/redaction-hardening.md
// §5.2–5.3). For the page it builds the text model: every text-showing
// operator as one run, with its Unicode readings and a page-space box that
// over-approximates where its glyphs can be (§6.6), and every marked-content
// property list (§6.7). For a child unit it only checks that nothing shows
// text. In both it follows the routes to child units (walk.go).

// tjSpaceThreshold is text extraction's TJ word-gap rule (core/extract): an
// adjustment at least this negative reads as a space.
const tjSpaceThreshold = 100.0

// Vertical-writing bounds in em (WMode 1, §9.7.4.3): the vertical advance
// and the glyph ink around the origin, in both directions. The text model
// does not place glyphs by /DW2 or /W2; a vertical font whose metrics would
// place a glyph outside these bounds is refused (checkVertical). 1.5 em of
// ink covers the default /DW2 [880 -1000] with a substituted font's glyph
// box (fontmodel substitutedBBox reaches 0.45 em below the baseline).
const (
	verticalAdvanceEm = 1.1
	verticalInkEm     = 1.5
)

// checkVertical refuses a vertical font of fs whose /DW2 or /W2 cannot be
// read, or would move a glyph (its ink, shifted by the position vector) out
// of the box the text model gives it — the ink hull mirrored and widened to
// verticalInkEm (see place in show) — or advance it by more than
// verticalAdvanceEm (B-1 of the batch C review, round 5).
func checkVertical(fs fontSet) error {
	ix0, iy0, ix1, iy1 := fs.ink()
	xMin, xMax := min(ix0, -ix1, -verticalInkEm), max(ix1, verticalInkEm)
	yMin, yMax := min(iy0, -verticalInkEm), max(iy1, verticalInkEm)
	for _, f := range fs.fonts() {
		if !f.Vertical() {
			continue
		}
		vx, vy, wy, ok := f.VerticalMetrics()
		if !ok || ix0-vx[1] < xMin || ix1-vx[0] > xMax || iy0-vy[1] < yMin || iy1-vy[0] > yMax ||
			wy[0] < -verticalAdvanceEm || wy[1] > verticalAdvanceEm {
			return &Refusal{Reason: ReasonContentAmbiguous,
				Detail: "page content: a vertical font's /DW2 or /W2 places glyphs outside the bounds redaction models"}
		}
	}
	return nil
}

// textRun is one text-showing operator: its span, its readings of the shown
// text, and its page-space box.
type textRun struct {
	span       int // index of its operator in Page.spans
	start, end int
	// texts are the distinct readings of the shown text a reader may
	// produce, for every font of the set: the strings at any nesting depth
	// concatenated, decoded as ExtractText decodes them; extraction's own
	// reading (primary font, top-level strings, TJ word gaps as spaces); the
	// /Differences glyph-name reading; for a simple font the one-byte-per-code
	// reading (fontmodel ByteText); and the raw bytes as Latin-1.
	texts []string
	// box is minX, minY, maxX, maxY: everywhere the glyphs can reach, used
	// for region intersection. nonFinite is set when any coordinate
	// overflowed or the font cannot be bounded (the run then intersects every
	// region, §6.5).
	box       [4]float64
	nonFinite bool
	// disp is the box RedactText returns and paints over a match: the same
	// pen positions with the em ink extent ([-0.25, 0.9] em vertically, the
	// font's /FontBBox ignored) — display only, since a matched run is
	// removed by its span mark. A /FontBBox-sized box would cover, and as a
	// region remove, the lines above and below.
	disp [4]float64
	// use is the state the run paints with (by its text rendering mode).
	use consumes
	// epoch numbers the stretches of content in which the pen moves only by
	// what runs show: it changes at BT and at every line move (Td, TD, Tm,
	// T*, ' and "). Removing a run moves the later runs of its epoch.
	epoch int
}

// propList is one BDC or DP operator with a property list (§14.6.2).
type propList struct {
	span       int // index of its operator in Page.spans
	start, end int
	op         string     // "BDC" or "DP"
	tag        parse.Name // the marked-content tag
	inline     parse.Dict // the inline dictionary, or nil
	name       parse.Name // the /Properties name when not inline
	// texts are the decoded /ActualText, /Alt and /E values, per key, of
	// every list the operand resolves to.
	texts map[parse.Name][]string
	// runs [firstRun, endRun) are the runs inside the BDC…EMC sequence at any
	// depth (the rest of the content if it is never closed); empty for DP.
	firstRun, endRun int
	// uses are what a named list's /Properties lookup bound (nil for an
	// inline list): Rewrite writes a stripped copy instead of an entry that
	// has a text key, so the entry's object is then not kept by this page.
	uses useSet
}

// itemKind is what a paintItem paints.
type itemKind int

const (
	itemXObject itemKind = iota // Do: an image, or a form that shows no text
	itemInline                  // BI … EI
	itemPath                    // a painted path
	itemShading                 // sh
)

// paintItem is something the page content paints besides text: the span
// indices it occupies, and a page-space box over everything it can paint
// (§6.10). Rewrite removes an item whose box meets a region.
type paintItem struct {
	kind        itemKind
	first, last int // spans: the operator, or a path's first construction operator through its painting operator
	box         [4]float64
	unbounded   bool   // the box is unknown or not finite: it meets every region
	clip        bool   // a path that also clips (W/W*): only its painting stops
	uses        useSet // resources only this item looks up (Do, BI, sh)
	use         consumes
}

// meets reports whether the item meets a normalized region: closed
// intervals (§6.5).
func (it paintItem) meets(rc Rect) bool {
	if it.unbounded {
		return true
	}
	return it.box[0] <= rc.X+rc.Width && it.box[2] >= rc.X &&
		it.box[1] <= rc.Y+rc.Height && it.box[3] >= rc.Y
}

// meetsAny reports whether the item meets any of the normalized regions.
func (it paintItem) meetsAny(regions []Rect) bool {
	for _, rc := range regions {
		if it.meets(rc) {
			return true
		}
	}
	return false
}

// stateNode is a page operator that sets graphics state a later paint may
// consume and that names a resource: a colour space (cs, CS), a pattern
// colour (scn, SCN with a name) or an ExtGState (gs). op is its index in
// Page.states; parent is the operator its effect builds on (the colour space
// a pattern colour is in; the previous gs, whose parameters persist).
// Rewrite keeps such an operator, and the resources it names, only if a
// surviving paint consumes it (B3: a pattern or soft mask used only by
// removed paint must not survive).
type stateNode struct {
	op     int
	parent *stateNode
}

// stateOp is a state-setting operator of the page and what it looked up.
type stateOp struct {
	span int
	uses useSet
}

// consumes is the state a paint operator uses: the fill and stroke colours
// and the ExtGState parameters in effect.
type consumes struct {
	fill, stroke, gs *stateNode
}

// box4 is a page-space box (minX, minY, maxX, maxY) that may be unbounded.
type box4 struct {
	b         [4]float64
	unbounded bool
}

// emptyBox4 is the identity of union.
func emptyBox4() box4 {
	return box4{b: [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}}
}

// union returns the box covering a and o.
func (a box4) union(o box4) box4 {
	return box4{
		b:         [4]float64{min(a.b[0], o.b[0]), min(a.b[1], o.b[1]), max(a.b[2], o.b[2]), max(a.b[3], o.b[3])},
		unbounded: a.unbounded || o.unbounded,
	}
}

// placeBox maps the user-space rectangle x0..x1 × y0..y1 through m by its four
// corners; a non-finite result is unbounded.
func placeBox(m matrix, x0, y0, x1, y1 float64) box4 {
	out := emptyBox4()
	for _, c := range [4][2]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}} {
		x, y := m.apply(c[0], c[1])
		out.b[0], out.b[1] = min(out.b[0], x), min(out.b[1], y)
		out.b[2], out.b[3] = max(out.b[2], x), max(out.b[3], y)
	}
	for _, v := range out.b {
		if !isFinite(v) {
			out.unbounded = true
		}
	}
	return out
}

// pathState is the path under construction (§8.5.2): its control points'
// hull in user space and the CTM they were given under.
type pathState struct {
	first      int // span index of the first construction operator; -1 when none
	hull       [4]float64
	ctm        matrix
	ctmChanged bool // the CTM changed while the path was built
	clip       bool // W or W* seen
}

// gstate is the part of the graphics state the text model tracks (§8.4,
// §9.3): the CTM and the text state parameters.
type gstate struct {
	ctm   matrix
	fonts fontSet // the font: every font a reader may have selected
	size  iv      // Tfs: every size a reader may have set
	tc    float64 // character spacing
	tw    float64 // word spacing
	th    float64 // horizontal scaling Tz/100
	tl    float64 // leading
	rise  float64 // Ts

	// For what the page paints besides text (§6.10): the line width and
	// miter limit (§8.4.3). The clip is not tracked: FluxDocs' renderer does
	// not clip by W/W* or text (it shows what a clip hides), and readers
	// disagree on edge cases (Tr 4.5, a clip with no point), so D5 never
	// narrows a paint's box by a clip (C46).
	lw, ml float64

	// The state-setting operators in effect (page only; see stateNode), and
	// the text rendering mode, which decides what text paints with.
	fillCS, fill, strokeCS, stroke, extGS *stateNode
	tr                                    int
}

// iv is a closed interval of reals.
type iv struct{ lo, hi float64 }

func point(v float64) iv        { return iv{v, v} }
func (a iv) add(b iv) iv        { return iv{a.lo + b.lo, a.hi + b.hi} }
func (a iv) hull(b iv) iv       { return iv{min(a.lo, b.lo), max(a.hi, b.hi)} }
func (a iv) withZero() iv       { return a.hull(point(0)) }
func (a iv) scale(k float64) iv { x, y := a.lo*k, a.hi*k; return iv{min(x, y), max(x, y)} }
func (a iv) mulIv(b iv) iv      { return a.scale(b.lo).hull(a.scale(b.hi)) }
func (a iv) finite() bool       { return isFinite(a.lo) && isFinite(a.hi) }
func isFinite(v float64) bool   { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (a iv) shift(v float64) iv { return iv{a.lo + v, a.hi + v} }
func spanIv(a, b float64) iv    { return iv{min(a, b), max(a, b)} }
func (a iv) union(b iv, has bool) iv {
	if !has {
		return b
	}
	return a.hull(b)
}

// interp interprets one unit's spans.
type interp struct {
	w      *walker
	kind   unitKind
	num    int
	st     *parse.Stream // the unit's stream (nil for the page)
	sc     *scope
	depth  int   // the unit's depth (the page is 0)
	ownRes bool  // a child unit with its own /Resources
	page   *Page // the text model being built; nil for a child unit

	gs    gstate
	stack []gstate
	// tm and tlm are the text and text line matrices. The pen is uncertain
	// once glyphs of unknown width are shown: the true text matrix is
	// translate(dx, dy) × tm for some dx in penX, dy in penY (§6.6).
	tm, tlm    matrix
	penX, penY iv

	open []int // open marked-content sequences: prop index, or -1 for BMC

	path pathState // the path under construction (page only)
	idx  int       // the index of the span being interpreted

	// rb are the reading buffers of show, reused from run to run (S-1).
	rb readBuffers

	// epoch is the current textRun.epoch.
	epoch int
	// settle, when set, interprets the page as Rewrite will write it (see
	// settler).
	settle *settler
}

// settler makes a page interpretation follow the rewritten content (Page.
// settle): a run removed by the plan, or whose box meets a region where the
// rewritten content places it, is removed — it moves the pen no further — so
// the later runs of its epoch are placed where readers of the output place
// them.
type settler struct {
	regions []Rect
	removed func(span int) bool // removed by the plan (span marks, regions where the source places runs)
	extra   []int               // spans of runs removed only where the output places them
}

// readBuffers are the buffers show builds a run's readings in: one per
// reading, reused for every run of a unit.
type readBuffers struct {
	bufs [][]byte
	used []bool
}

// reset prepares n empty buffers.
func (r *readBuffers) reset(n int) {
	for len(r.bufs) < n {
		r.bufs = append(r.bufs, nil)
		r.used = append(r.used, false)
	}
	for i := 0; i < n; i++ {
		r.bufs[i] = r.bufs[i][:0]
		r.used[i] = false
	}
}

// texts returns the distinct readings among the first n used buffers, as
// strings (copies: the buffers are reused).
func (r *readBuffers) texts(n int) []string {
	distinct := 0
	for i := 0; i < n; i++ {
		if r.used[i] && r.first(i) == i {
			distinct++
		}
	}
	out := make([]string, 0, distinct)
	for i := 0; i < n; i++ {
		if r.used[i] && r.first(i) == i {
			out = append(out, string(r.bufs[i]))
		}
	}
	return out
}

// first is the index of the first used buffer equal to buffer i.
func (r *readBuffers) first(i int) int {
	for j := 0; j < i; j++ {
		if r.used[j] && bytes.Equal(r.bufs[j], r.bufs[i]) {
			return j
		}
	}
	return i
}

// run interprets spans: tiling patterns in the unit's own resources first
// (reachability), then each operator.
func (it *interp) run(spans []parse.ContentOpSpan) error {
	it.gs = gstate{ctm: identity(), th: 1, lw: 1, ml: 10}
	it.tm, it.tlm = identity(), identity()
	it.path.first = -1
	// The default colour spaces apply whenever the unit paints in a device
	// colour space (§8.6.5.6), with no operator naming them.
	if it.page != nil {
		it.w.uses = it.page.uses
	}
	for _, n := range [...]parse.Name{"DefaultGray", "DefaultRGB", "DefaultCMYK"} {
		it.w.note(it.sc, "ColorSpace", n)
	}
	// Patterns reached only by reachability are not used by the unit (page
	// or child): their walks are memoized, and the scn that selects one
	// replays its lookups (BL-B).
	outer := it.w.uses
	it.w.uses = nil
	err := it.w.reachPatterns(it.sc, it.kind, it.ownRes, it.depth+1)
	it.w.uses = outer
	if err != nil {
		return err
	}
	if err := it.w.visit(int64(len(spans))); err != nil {
		return err
	}
	for i := range spans {
		if it.page != nil {
			it.idx = i
			it.w.uses = it.page.uses
		}
		if err := it.op(&spans[i]); err != nil {
			return err
		}
	}
	if it.page != nil {
		for _, p := range it.open {
			if p >= 0 {
				it.page.props[p].endRun = len(it.page.runs)
			}
		}
	}
	return it.w.budgetErr
}

// shows refuses text in a child unit.
func (it *interp) shows() error {
	if it.w.lenient {
		return nil
	}
	return refuse(it.kind.reason(), "shows text through a %s", unitName(it.kind, it.num))
}

// op interprets one operator. The strict scanner has checked every operand
// count and the types of the operands used here (parse.operandsFit).
func (it *interp) op(s *parse.ContentOpSpan) error {
	o := s.Operands
	if it.w.lenient && s.Op == "BI" {
		// A lenient inline image (ScanContentSpansWithInlineImages): its
		// named colour space is a resource the page paints with.
		if len(o) == 1 {
			if d, ok := o[0].(parse.Dict); ok {
				it.noteInlineColorSpace(d["ColorSpace"])
			}
		}
		return nil
	}
	if it.w.lenient && !parse.OperandsFit(s.Op, o) {
		return nil // a lenient span the strict scan would refuse: skip it
	}
	switch s.Op {
	case "q":
		it.stack = append(it.stack, it.gs)
	case "Q":
		if n := len(it.stack); n > 0 {
			it.gs = it.stack[n-1]
			it.stack = it.stack[:n-1]
		}
	case "cm":
		it.gs.ctm = mat6(o).mul(it.gs.ctm)
	case "w":
		it.gs.lw = math.Abs(num(o[0]))
	case "M":
		it.gs.ml = math.Abs(num(o[0]))
	case "m", "l", "c", "v", "y", "h", "re", "W", "W*",
		"S", "s", "f", "F", "f*", "B", "B*", "b", "b*", "n":
		if it.page != nil {
			it.pathOp(s.Op, o)
		}
	case "cs", "CS":
		// A colour space resource (§8.6.8).
		n := it.newState(nil)
		it.w.note(it.sc, "ColorSpace", o[0].(parse.Name))
		if s.Op == "cs" {
			it.gs.fillCS, it.gs.fill = n, n
		} else {
			it.gs.strokeCS, it.gs.stroke = n, n
		}
	case "g", "rg", "k":
		it.gs.fillCS, it.gs.fill = nil, nil
	case "G", "RG", "K":
		it.gs.strokeCS, it.gs.stroke = nil, nil
	case "sc":
		it.gs.fill = it.gs.fillCS
	case "SC":
		it.gs.stroke = it.gs.strokeCS
	case "Tr":
		// Only an exact integer 0–7 is a rendering mode (Table 106);
		// Ghostscript renders any other value as mode 0, other readers may
		// not: undefined, so text consumes every state (textState).
		it.gs.tr = -1
		if v := num(o[0]); v == math.Trunc(v) && v >= 0 && v <= 7 {
			it.gs.tr = int(v)
		}
	case "sh":
		var uses useSet
		if it.page != nil {
			uses = useSet{}
			it.w.uses = uses
		}
		it.w.note(it.sc, "Shading", o[0].(parse.Name))
		if it.page != nil {
			// A shading fills the clip, and the clip is not tracked (see
			// gstate): it may paint the whole page.
			pb := it.page.box
			it.addItem(paintItem{kind: itemShading, first: it.idx, last: it.idx,
				box: [4]float64{pb.X, pb.Y, pb.X + pb.Width, pb.Y + pb.Height},
				use: consumes{gs: it.gs.extGS}}, uses)
		}
	case "BI":
		if it.page != nil {
			it.inlineImage(o)
		}

	case "BT":
		it.tm, it.tlm = identity(), identity()
		it.penX, it.penY = iv{}, iv{}
		it.epoch++
	case "Tc":
		it.gs.tc = num(o[0])
	case "Tw":
		it.gs.tw = num(o[0])
	case "Tz":
		it.gs.th = num(o[0]) / 100
	case "TL":
		it.gs.tl = num(o[0])
	case "Ts":
		it.gs.rise = num(o[0])
	case "Td":
		it.moveLine(num(o[0]), num(o[1]))
	case "TD":
		it.gs.tl = -num(o[1])
		it.moveLine(num(o[0]), num(o[1]))
	case "Tm":
		it.tlm = mat6(o)
		it.setLine()
	case "T*":
		it.moveLine(0, -it.gs.tl)

	case "Tf":
		cands, err := it.w.lookup(it.sc, "Font", o[0].(parse.Name))
		if err != nil {
			return err
		}
		for _, c := range cands {
			if err := it.w.checkFont(c.obj, it.sc, it.depth+1); err != nil {
				return err
			}
		}
		var fonts fontSet
		for _, c := range cands {
			fonts = append(fonts, it.w.font(c.obj))
		}
		if len(cands) == 0 || !cands[0].inner {
			fonts = append(fonts, nil) // a reader that does not fall back finds no font
		}
		if len(fonts) > maxFontSet {
			return budget("font candidate")
		}
		it.gs.fonts, it.gs.size = fonts, point(num(o[1]))
	case "gs":
		it.gs.extGS = it.newState(it.gs.extGS)
		cands, err := it.w.lookup(it.sc, "ExtGState", o[0].(parse.Name))
		if err != nil {
			return err
		}
		// gs /Font sets the font and size (§8.4.5, N6). When scopes disagree,
		// or only a fallback scope binds the name, a reader may keep the
		// current ones: the state covers every outcome.
		var fonts fontSet
		var sizes iv
		set := 0
		// Only the innermost scope's single binding is certain to apply; any
		// other outcome may keep the current line width and miter limit or
		// set any candidate's, so the larger is kept (D5 widening, I1).
		certain := len(cands) == 1 && cands[0].inner
		for _, c := range cands {
			p, err := it.w.extGState(c, it.sc, it.depth+1)
			if err != nil {
				return err
			}
			if p.hasFont {
				fonts = append(fonts, it.w.font(p.font))
				sizes = sizes.union(point(p.size), set > 0)
				set++
			}
			if p.hasLW {
				if certain {
					it.gs.lw = p.lw
				} else {
					it.gs.lw = max(it.gs.lw, p.lw)
				}
			}
			if p.hasML {
				if certain {
					it.gs.ml = p.ml
				} else {
					it.gs.ml = max(it.gs.ml, p.ml)
				}
			}
		}
		switch {
		case set == 0:
		case set == len(cands) && cands[0].inner && len(cands) == 1:
			it.gs.fonts, it.gs.size = fonts, sizes
		default:
			if fonts = it.gs.fonts.union(fonts); len(fonts) > maxFontSet {
				// A cap rather than a collapse to "no font": without the real
				// fonts the readings, and so RedactText, would silently miss
				// text (F3), and real pages never get near it.
				return budget("font candidate")
			}
			it.gs.fonts = fonts
			it.gs.size = it.gs.size.hull(sizes)
		}
	case "Do":
		var uses useSet
		if it.page != nil {
			uses = useSet{}
			it.w.uses = uses
		}
		cands, err := it.w.lookup(it.sc, "XObject", o[0].(parse.Name))
		if err != nil {
			return err
		}
		placed := emptyBox4()
		for _, c := range cands {
			image, err := it.w.xobject(c, it.sc, it.depth+1)
			if err != nil {
				return err
			}
			if it.page != nil {
				placed = placed.union(it.w.xobjectBox(c, image, it.gs.ctm))
			}
		}
		if it.page != nil && len(cands) > 0 {
			// A form inherits all the state, an image mask paints with the
			// fill colour.
			it.addItem(paintItem{kind: itemXObject, first: it.idx, last: it.idx,
				box: placed.b, unbounded: placed.unbounded, use: it.allState()}, uses)
		}
	case "scn", "SCN":
		cs, set := &it.gs.fillCS, &it.gs.fill
		if s.Op == "SCN" {
			cs, set = &it.gs.strokeCS, &it.gs.stroke
		}
		*set = *cs
		if name, ok := o[len(o)-1].(parse.Name); ok {
			*set = it.newState(*cs)
			cands, err := it.w.lookup(it.sc, "Pattern", name)
			if err != nil {
				return err
			}
			for _, c := range cands {
				if err := it.w.pattern(c.obj, it.sc, it.depth+1); err != nil {
					return err
				}
			}
		}

	case "d0", "d1":
		if it.kind == unitType3 {
			it.w.glyphWX[it.st] = num(o[0])
		}
	case "BMC":
		it.open = append(it.open, -1)
	case "BDC", "DP":
		return it.markedContent(s)
	case "EMC":
		if n := len(it.open); n > 0 {
			if p := it.open[n-1]; p >= 0 && it.page != nil {
				it.page.props[p].endRun = len(it.page.runs)
			}
			it.open = it.open[:n-1]
		}

	case "Tj":
		return it.show(s, nil, o[0])
	case "'":
		it.moveLine(0, -it.gs.tl)
		return it.show(s, nil, o[0])
	case "\"":
		it.gs.tw, it.gs.tc = num(o[0]), num(o[1])
		it.moveLine(0, -it.gs.tl)
		return it.show(s, nil, o[2])
	case "TJ":
		return it.show(s, o[0].(parse.Array), nil)
	}
	return nil
}

// noteInlineColorSpace notes the colour space names an inline image's
// /ColorSpace value holds: the name itself, or the names inside an array
// (an Indexed base), but not colour space family names.
func (it *interp) noteInlineColorSpace(cs parse.Object) {
	switch v := cs.(type) {
	case parse.Name:
		it.w.note(it.sc, "ColorSpace", v)
	case parse.Array:
		for _, e := range v {
			if n, ok := e.(parse.Name); ok && !colorSpaceFamilies[n] {
				it.w.note(it.sc, "ColorSpace", n)
			}
		}
	}
}

// newState records a state-setting operator of the page (see stateNode) and
// makes its lookups its own; nil for a child unit.
func (it *interp) newState(parent *stateNode) *stateNode {
	if it.page == nil {
		return nil
	}
	uses := useSet{}
	it.page.states = append(it.page.states, stateOp{span: it.idx, uses: uses})
	it.w.uses = uses
	return &stateNode{op: len(it.page.states) - 1, parent: parent}
}

// allState is every state a paint may consume.
func (it *interp) allState() consumes {
	return consumes{fill: it.gs.fill, stroke: it.gs.stroke, gs: it.gs.extGS}
}

// textState is the state text paints with in the current rendering mode
// (§9.3.6, Table 106): fill for 0, 2, 4, 6; stroke for 1, 2, 5, 6; the
// ExtGState (font, soft mask, alpha) always.
func (it *interp) textState() consumes {
	c := consumes{gs: it.gs.extGS}
	switch it.gs.tr {
	case 0, 2, 4, 6:
		c.fill = it.gs.fill
	}
	switch it.gs.tr {
	case 1, 2, 5, 6:
		c.stroke = it.gs.stroke
	}
	if it.gs.tr < 0 || it.gs.tr > 7 {
		c = it.allState() // undefined mode: assume anything
	}
	return c
}

// addItem records a paint item of the page, with the resources only it uses.
// Its box is not narrowed by any W/W* or text clip (see gstate).
func (it *interp) addItem(item paintItem, uses useSet) {
	if len(uses) > 0 {
		item.uses = uses
	}
	it.page.items = append(it.page.items, item)
}

// inlineImage records an inline image (a strict "BI" span, whose operand is
// its dictionary): the unit square under the CTM (§8.9.5). A named colour
// space is a resource only it uses.
func (it *interp) inlineImage(o []parse.Object) {
	uses := useSet{}
	it.w.uses = uses
	if d, ok := o[0].(parse.Dict); ok {
		if name, ok := d["ColorSpace"].(parse.Name); ok {
			it.w.note(it.sc, "ColorSpace", name)
		}
	}
	b := placeBox(it.gs.ctm, 0, 0, 1, 1)
	it.addItem(paintItem{kind: itemInline, first: it.idx, last: it.idx, box: b.b, unbounded: b.unbounded,
		use: consumes{fill: it.gs.fill, gs: it.gs.extGS}}, uses)
}

// pathOp interprets a path construction, clipping or painting operator on
// the page (§8.5.2–8.5.4): construction grows the path's hull; a painting
// operator records the path as a paint item (unless it is n; after W or W*
// the item keeps its clip when removed).
func (it *interp) pathOp(op string, o []parse.Object) {
	p := &it.path
	switch op {
	case "W", "W*":
		p.clip = true
		return
	case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*", "n":
		it.paintPath(op)
		return
	}
	if p.first < 0 {
		*p = pathState{first: it.idx, ctm: it.gs.ctm,
			hull: [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}}
	}
	if it.gs.ctm != p.ctm {
		p.ctmChanged = true
	}
	pt := func(x, y float64) {
		p.hull[0], p.hull[1] = min(p.hull[0], x), min(p.hull[1], y)
		p.hull[2], p.hull[3] = max(p.hull[2], x), max(p.hull[3], y)
	}
	switch op {
	case "re":
		x, y, w, h := num(o[0]), num(o[1]), num(o[2]), num(o[3])
		pt(x, y)
		pt(x+w, y+h)
	case "h":
	default: // m l c v y: every operand pair is a point
		for i := 0; i+1 < len(o); i += 2 {
			pt(num(o[i]), num(o[i+1]))
		}
	}
}

// paintPath ends the path under construction with a painting operator.
func (it *interp) paintPath(op string) {
	p := &it.path
	if p.first < 0 {
		*p = pathState{first: -1}
		return // nothing was built: nothing is painted or clipped
	}
	hull := p.hull
	if hull[0] > hull[2] { // only h: no point
		hull = [4]float64{0, 0, 0, 0}
	}
	shape := placeBox(p.ctm, hull[0], hull[1], hull[2], hull[3])
	if p.ctmChanged || it.gs.ctm != p.ctm {
		// The CTM changed while the path was built, or between its last
		// point and the painting operator: Ghostscript maps the points with
		// the CTM at painting, other readers with the CTM of each point.
		shape.unbounded = true
	}
	if op != "n" {
		painted := shape
		switch op {
		case "S", "s", "B", "B*", "b", "b*":
			// A stroke reaches up to half the line width past the path, and a
			// miter join up to half the miter length (§8.4.3.5); widen by the
			// whole of both in user space (I1).
			d := max(it.gs.lw, 1) * max(1, it.gs.ml)
			painted = placeBox(p.ctm, hull[0]-d, hull[1]-d, hull[2]+d, hull[3]+d)
			painted.unbounded = painted.unbounded || shape.unbounded
		}
		// A hairline or a thin line under a shrinking CTM still paints a
		// device pixel: at least 1 point around the shape on the page.
		painted.b = [4]float64{painted.b[0] - 1, painted.b[1] - 1, painted.b[2] + 1, painted.b[3] + 1}
		use := consumes{gs: it.gs.extGS}
		switch op {
		case "f", "F", "f*":
			use.fill = it.gs.fill
		case "S", "s":
			use.stroke = it.gs.stroke
		default:
			use.fill, use.stroke = it.gs.fill, it.gs.stroke
		}
		it.addItem(paintItem{kind: itemPath, first: p.first, last: it.idx,
			box: painted.b, unbounded: painted.unbounded, clip: p.clip, use: use}, nil)
	}
	*p = pathState{first: -1}
}

// moveLine is Td: the line matrix moves by (tx, ty) and the text matrix starts
// the new line, with no pen uncertainty (§9.4.2).
func (it *interp) moveLine(tx, ty float64) {
	it.tlm = translate(tx, ty).mul(it.tlm)
	it.setLine()
}

// setLine sets the text matrix to the line matrix.
func (it *interp) setLine() {
	it.tm = it.tlm
	it.penX, it.penY = iv{}, iv{}
	it.epoch++
}

// markedContent records a BDC or DP property list (page) or refuses one that
// carries text (child unit, §6.3).
func (it *interp) markedContent(s *parse.ContentOpSpan) error {
	var uses useSet
	if _, named := s.Operands[1].(parse.Name); named && it.page != nil {
		uses = useSet{}
		it.w.uses = uses
	}
	lists, err := it.w.propertyLists(s.Operands[1], it.sc)
	if err != nil {
		return err
	}
	collect := it.page == nil && it.w.texts != nil
	texts := map[parse.Name][]string{}
	for _, d := range lists {
		for _, k := range textKeys {
			v, ok := d[k]
			if !ok {
				continue
			}
			if it.page == nil && !collect {
				return it.shows()
			}
			obj, err := it.w.resolve(v)
			if err != nil {
				return err
			}
			if str, ok := obj.(parse.String); ok {
				texts[k] = append(texts[k], textString(str.Value))
			}
		}
	}
	if it.page == nil {
		if collect {
			for _, k := range textKeys {
				if len(texts[k]) > 0 {
					it.w.texts(it.kind, it.num, texts[k])
				}
			}
		}
		if s.Op == "BDC" {
			it.open = append(it.open, -1)
		}
		return nil
	}
	p := propList{span: it.idx, start: s.Start, end: s.End, op: s.Op, texts: texts,
		firstRun: len(it.page.runs), endRun: len(it.page.runs), uses: uses}
	p.tag, _ = s.Operands[0].(parse.Name)
	switch v := s.Operands[1].(type) {
	case parse.Dict:
		p.inline = v
	case parse.Name:
		p.name = v
	}
	it.page.props = append(it.page.props, p)
	if s.Op == "BDC" {
		it.open = append(it.open, len(it.page.props)-1)
	}
	return nil
}

// show interprets one text-showing operator: str for Tj, ' and ", arr for TJ.
// In a child unit it refuses; on the page it adds a run whose box covers
// every position its glyphs can take with any font of the set (§6.6), and
// moves the pen. Decoding the readings is charged to the operator budget.
func (it *interp) show(s *parse.ContentOpSpan, arr parse.Array, str parse.Object) error {
	// collect: a child unit read for its text (TextScanner), not refused.
	collect := it.page == nil && it.w.texts != nil
	if it.page == nil && !collect {
		return it.shows()
	}
	// textOnly: a TextScanner reads the text and nothing of where it is.
	textOnly := it.w.texts != nil
	gs := &it.gs
	fs := gs.fonts.fonts()
	vertical := fs.vertical()
	if vertical && it.w.texts == nil {
		if err := checkVertical(fs); err != nil {
			return err
		}
	}
	p0X, p0Y := it.penX, it.penY
	size := gs.size
	// Readings: per font, all strings with Text, AltText and ByteText; the
	// extraction reading; and the raw bytes as Latin-1. Index layout: 3 per
	// font, then extraction, then Latin-1.
	nRead := 3*len(fs) + 2
	it.rb.reset(nRead)
	reads, used := it.rb.bufs, it.rb.used
	used[nRead-2], used[nRead-1] = true, true
	extract, latin := nRead-2, nRead-1
	// origins bounds every glyph origin, relative to tm; w0hi is the largest
	// advance upper bound seen (a glyph's ink may reach that far, §6.6).
	var originsX, originsY iv
	w0hi := 0.0
	has := false
	note := func(x, y iv) {
		originsX = originsX.union(x, has)
		originsY = originsY.union(y, has)
		has = true
	}
	advance := func(dx, dy iv, nested bool) {
		if nested || vertical {
			// A reader may skip a nested element or ignore WMode: both
			// moves and no move are possible (I1).
			dx, dy = dx.withZero(), dy.withZero()
		}
		it.penX, it.penY = it.penX.add(dx), it.penY.add(dy)
	}
	// verticalStep is a vertical-writing displacement without /W2 metrics.
	verticalStep := func(space bool) iv {
		d := iv{-verticalAdvanceEm, verticalAdvanceEm}.mulIv(size).shift(gs.tc)
		if space {
			d = d.shift(gs.tw)
		}
		return d
	}
	showString := func(raw []byte, nested bool) {
		for i, f := range fs {
			if tb := f.Tables(); tb != nil {
				// One byte per code: append each byte's readings (the same
				// strings Text, AltText and ByteText return, decoded once
				// per font).
				alt := false
				if tb.Alt != nil {
					for _, c := range raw {
						if tb.AltDiffers[c] {
							alt = true
							break
						}
					}
				}
				// A slot that can never differ from Text for this font (no
				// /Differences reading; ByteText equal to Text) is not built:
				// it would only repeat the Text reading.
				for _, c := range raw {
					reads[3*i] = append(reads[3*i], tb.Text[c]...)
					if i == 0 && !nested {
						reads[extract] = append(reads[extract], tb.Text[c]...)
					}
					if tb.Alt != nil {
						if alt {
							reads[3*i+1] = append(reads[3*i+1], tb.Alt[c]...)
						} else {
							reads[3*i+1] = append(reads[3*i+1], tb.Text[c]...)
						}
					}
					if !tb.ByteIsText {
						reads[3*i+2] = append(reads[3*i+2], tb.Byte[c]...)
					}
				}
				used[3*i] = true
				used[3*i+1] = used[3*i+1] || alt
				used[3*i+2] = used[3*i+2] || !tb.ByteIsText
				continue
			}
			t := f.Text(raw)
			reads[3*i] = append(reads[3*i], t...)
			used[3*i] = true
			if i == 0 && !nested {
				reads[extract] = append(reads[extract], t...)
			}
			if a, ok := f.AltText(raw); ok {
				reads[3*i+1] = append(reads[3*i+1], a...)
				used[3*i+1] = true
			} else {
				reads[3*i+1] = append(reads[3*i+1], t...)
			}
			if b, ok := f.ByteText(raw); ok {
				reads[3*i+2] = append(reads[3*i+2], b...)
				used[3*i+2] = true
			} else {
				reads[3*i+2] = append(reads[3*i+2], t...)
			}
		}
		for _, c := range raw {
			reads[latin] = utf8.AppendRune(reads[latin], rune(c))
		}
		if len(raw) == 0 || textOnly {
			return
		}
		if codes, certain := fs.codes(raw); certain {
			for _, c := range codes {
				note(it.penX, it.penY)
				w := it.w.type3Advance(fs, fs.advance(c))
				w0hi = max(w0hi, w.hi)
				dx := w.mulIv(size).shift(gs.tc)
				if c.SingleByteSpace() {
					dx = dx.shift(gs.tw)
				}
				dy := point(0)
				if vertical {
					dy = verticalStep(c.SingleByteSpace())
				}
				advance(dx.scale(gs.th), dy, nested)
			}
			return
		}
		// Uncertain segmentation: between len/maxCodeLen and len codes, of
		// unknown width; word spacing may apply to each 0x20 byte.
		w := it.w.type3Advance(fs, fs.unknownAdvance())
		w0hi = max(w0hi, w.hi)
		cmax := float64(len(raw))
		cmin := math.Ceil(cmax / float64(fs.maxCodeLen()))
		g := w.mulIv(size).shift(gs.tc)
		tw := spanIv(0, float64(strings.Count(string(raw), " "))*gs.tw)
		total := g.scale(cmin).hull(g.scale(cmax)).add(tw)
		dyTotal := point(0)
		if vertical {
			gv := verticalStep(false)
			dyTotal = gv.scale(cmin).hull(gv.scale(cmax)).add(tw)
		}
		// Every glyph origin lies within pen + (0 … total).
		note(it.penX.add(total.withZero().scale(gs.th)), it.penY.add(dyTotal.withZero()))
		advance(total.scale(gs.th), dyTotal, nested)
	}
	var element func(e parse.Object, depth int)
	element = func(e parse.Object, depth int) {
		nested := depth > 0
		switch v := e.(type) {
		case parse.String:
			showString(v.Value, nested)
		case parse.Integer, parse.Real:
			n := num(v)
			if e := reads[extract]; !nested && n <= -tjSpaceThreshold && len(e) > 0 && e[len(e)-1] != ' ' {
				reads[extract] = append(e, ' ')
			}
			dx := size.scale(-n / 1000 * gs.th)
			dy := point(0)
			if vertical {
				dy = size.scale(-n / 1000)
			}
			advance(dx, dy, nested)
		case parse.Array:
			for _, el := range v {
				element(el, depth+1)
			}
		}
	}
	if arr != nil {
		for _, el := range arr {
			element(el, 0)
		}
	} else if v, ok := str.(parse.String); ok {
		showString(v.Value, false)
	}
	// Charge the readings' size: decoding cost, and what Match will run the
	// pattern over (a /ToUnicode entry may expand one code to a long text).
	charge := int64(1)
	for i := 0; i < nRead; i++ {
		charge += int64(len(reads[i]))
	}
	// The buffers may have grown: keep them for the next run.
	it.rb.bufs = reads
	if err := it.w.visit(charge); err != nil {
		return err
	}
	if collect {
		it.w.texts(it.kind, it.num, it.rb.texts(nRead))
		return nil
	}
	if textOnly {
		if it.w.pageText != nil {
			// The search needs only whether a reading holds a needle: no
			// run is kept.
			for k := 0; k < nRead; k++ {
				if used[k] && it.rb.first(k) == k {
					it.w.pageText(reads[k])
				}
			}
			return nil
		}
		it.page.runs = append(it.page.runs, textRun{span: it.idx, start: s.Start, end: s.End,
			texts: it.rb.texts(nRead), epoch: it.epoch})
		return nil
	}
	if !has {
		note(it.penX, it.penY) // no glyphs: the run is a point at the pen
	}

	run := textRun{span: it.idx, start: s.Start, end: s.End, texts: it.rb.texts(nRead), use: it.textState(),
		epoch: it.epoch}

	// Glyph ink around each origin, in text space (§6.6): x from the
	// leftmost origin plus the ink's left edge to the rightmost origin plus
	// the larger of the ink's right edge and the widest advance; y from the
	// rise plus the ink's vertical extent.
	m := it.tm.mul(gs.ctm)
	place := func(x0, y0, x1, y1 float64) [4]float64 {
		if vertical {
			x0, x1 = min(x0, -x1, -verticalInkEm), max(x1, verticalInkEm)
			y0, y1 = min(y0, -verticalInkEm), max(y1, verticalInkEm)
		}
		bx := originsX.add(iv{min(0, x0), max(x1, w0hi)}.mulIv(size.scale(gs.th)))
		by := originsY.add(iv{y0, y1}.mulIv(size).shift(gs.rise))
		box := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, c := range [4][2]float64{{bx.lo, by.lo}, {bx.hi, by.lo}, {bx.hi, by.hi}, {bx.lo, by.hi}} {
			x, y := m.apply(c[0], c[1])
			box[0], box[1] = min(box[0], x), min(box[1], y)
			box[2], box[3] = max(box[2], x), max(box[3], y)
		}
		return box
	}
	run.box = place(fs.ink())
	run.disp = place(fs.emInk())
	for _, v := range run.box {
		if !isFinite(v) {
			run.nonFinite = true
		}
	}
	if fs.unbounded() || !it.penX.finite() || !it.penY.finite() {
		run.nonFinite = true
	}
	if st := it.settle; st != nil {
		if gone := st.removed(it.idx); gone || run.meetsAny(st.regions) {
			if !gone {
				st.extra = append(st.extra, it.idx)
			}
			// Removed from the output: it moves the pen no further.
			it.penX, it.penY = p0X, p0Y
		}
	}
	it.page.runs = append(it.page.runs, run)
	return nil
}

// meetsAny reports whether the run meets any of the normalized regions.
func (r textRun) meetsAny(regions []Rect) bool {
	for _, rc := range regions {
		if r.intersects(rc) {
			return true
		}
	}
	return false
}

package redact

import (
	"math"
	"strings"

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
// and the glyph ink around the origin. Phase 1 has no /W2 metrics, so both
// directions are allowed.
const (
	verticalAdvanceEm = 1.1
	verticalInkEm     = 1.2
)

// textRun is one text-showing operator: its span, its readings of the shown
// text, and its page-space box.
type textRun struct {
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
}

// propList is one BDC or DP operator with a property list (§14.6.2).
type propList struct {
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
}

// placement is an XObject painted by the page content: the Do span and the
// CTM it is painted with (recorded for D5, step 1.11).
type placement struct {
	start, end int
	ctm        matrix
	image      bool
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
}

// run interprets spans: tiling patterns in the unit's own resources first
// (reachability), then each operator.
func (it *interp) run(spans []parse.ContentOpSpan) error {
	it.gs = gstate{ctm: identity(), th: 1}
	it.tm, it.tlm = identity(), identity()
	if err := it.w.reachPatterns(it.sc, it.kind, it.ownRes, it.depth+1); err != nil {
		return err
	}
	if err := it.w.visit(int64(len(spans))); err != nil {
		return err
	}
	for i := range spans {
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
	return refuse(it.kind.reason(), "shows text through a %s", unitName(it.kind, it.num))
}

// op interprets one operator. The strict scanner has checked every operand
// count and the types of the operands used here (parse.operandsFit).
func (it *interp) op(s *parse.ContentOpSpan) error {
	o := s.Operands
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

	case "BT":
		it.tm, it.tlm = identity(), identity()
		it.penX, it.penY = iv{}, iv{}
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
		for _, c := range cands {
			f, size, ok, err := it.w.extGState(c, it.sc, it.depth+1)
			if err != nil {
				return err
			}
			if ok {
				fonts = append(fonts, it.w.font(f))
				sizes = sizes.union(point(size), set > 0)
				set++
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
		cands, err := it.w.lookup(it.sc, "XObject", o[0].(parse.Name))
		if err != nil {
			return err
		}
		for _, c := range cands {
			image, err := it.w.xobject(c, it.sc, it.depth+1)
			if err != nil {
				return err
			}
			if it.page != nil {
				it.page.placements = append(it.page.placements, placement{s.Start, s.End, it.gs.ctm, image})
			}
		}
	case "scn", "SCN":
		if name, ok := o[len(o)-1].(parse.Name); ok {
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
}

// markedContent records a BDC or DP property list (page) or refuses one that
// carries text (child unit, §6.3).
func (it *interp) markedContent(s *parse.ContentOpSpan) error {
	lists, err := it.w.propertyLists(s.Operands[1], it.sc)
	if err != nil {
		return err
	}
	texts := map[parse.Name][]string{}
	for _, d := range lists {
		for _, k := range textKeys {
			v, ok := d[k]
			if !ok {
				continue
			}
			if it.page == nil {
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
		if s.Op == "BDC" {
			it.open = append(it.open, -1)
		}
		return nil
	}
	p := propList{start: s.Start, end: s.End, op: s.Op, texts: texts,
		firstRun: len(it.page.runs), endRun: len(it.page.runs)}
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
	if it.page == nil {
		return it.shows()
	}
	gs := &it.gs
	fs := gs.fonts.fonts()
	vertical := fs.vertical()
	size := gs.size
	// Readings: per font, all strings with Text, AltText and ByteText; the
	// extraction reading; and the raw bytes as Latin-1. Index layout: 3 per
	// font, then extraction, then Latin-1.
	nRead := 3*len(fs) + 2
	reads := make([]strings.Builder, nRead)
	used := make([]bool, nRead)
	extract, latin := &reads[nRead-2], &reads[nRead-1]
	used[nRead-2], used[nRead-1] = true, true
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
			t := f.Text(raw)
			reads[3*i].WriteString(t)
			used[3*i] = true
			if i == 0 && !nested {
				extract.WriteString(t)
			}
			if a, ok := f.AltText(raw); ok {
				reads[3*i+1].WriteString(a)
				used[3*i+1] = true
			} else {
				reads[3*i+1].WriteString(t)
			}
			if b, ok := f.ByteText(raw); ok {
				reads[3*i+2].WriteString(b)
				used[3*i+2] = true
			} else {
				reads[3*i+2].WriteString(t)
			}
		}
		for _, c := range raw {
			latin.WriteRune(rune(c))
		}
		if len(raw) == 0 {
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
			if !nested && n <= -tjSpaceThreshold && extract.Len() > 0 && !strings.HasSuffix(extract.String(), " ") {
				extract.WriteByte(' ')
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
	for i := range reads {
		charge += int64(reads[i].Len())
	}
	if err := it.w.visit(charge); err != nil {
		return err
	}
	if !has {
		note(it.penX, it.penY) // no glyphs: the run is a point at the pen
	}

	run := textRun{start: s.Start, end: s.End}
	seen := make(map[string]bool, nRead)
	for i := range reads {
		if t := reads[i].String(); used[i] && !seen[t] {
			seen[t] = true
			run.texts = append(run.texts, t)
		}
	}

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
	it.page.runs = append(it.page.runs, run)
	return nil
}

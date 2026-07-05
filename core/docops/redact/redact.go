// Package redact implements safe PDF redaction: it PERMANENTLY removes the
// text-showing operators (Tj/TJ/'/") that fall within a redaction region from a
// page's content stream — not merely painting over them — then draws a solid
// black rectangle for display. This is FluxDocs's reputation-critical guarantee
// (.claude/SECURITY.md §1): after redaction the removed text must not be
// recoverable by extraction or copy-paste.
//
// The removal is byte-exact: the content stream is scanned into operator spans
// (parse.ScanContentSpans), the spans that intersect a region are cut out of the
// original bytes, and every other byte is preserved verbatim. A conservative
// (over-estimated) text bounding box is used so a run that reaches into a region
// is always removed — the engine may over-remove, never under-remove.
//
// Wave-2 scope: text-showing operators. Vector/image content inside a region is
// covered by the black rectangle but not stripped from the stream; glyph-level
// splitting of a partially-covered run is a refinement (a whole intersecting run
// is removed today). This package is pure Go (MIT-safe); it must not import core
// (import cycle) — core translates its own types into redact.Rect.
package redact

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Rect is a redaction region in PDF user space (origin bottom-left, points).
type Rect struct {
	X, Y, Width, Height float64
}

// The redaction fill is solid near-black #18181b (SECURITY.md §1 / design token
// --fd-redact-fill); it is display-only, never the security mechanism.
const (
	fillR = 24.0 / 255.0
	fillG = 24.0 / 255.0
	fillB = 27.0 / 255.0
)

// Conservative glyph-box estimates in em units. bboxCharWidth over-estimates a
// glyph's advance so a run's bounding box never under-covers its ink;
// advanceCharWidth positions the pen for the next run (approximate is fine).
const (
	bboxCharWidth    = 0.6
	advanceCharWidth = 0.5
	ascenderEm       = 0.9
	descenderEm      = -0.25
)

// textRun is one shown text run with its page-space axis-aligned bounding box
// and its byte span in the content stream.
type textRun struct {
	text                   string
	minX, minY, maxX, maxY float64
	start, end             int
}

// RedactContent removes every text-showing operator whose bounding box
// intersects any rect and appends a solid black rectangle over each rect. It
// returns the rewritten content stream, byte-spliced from the original so all
// non-removed bytes are preserved exactly. If rects is empty it returns the
// content unchanged (plus no fills).
func RedactContent(content []byte, rects []Rect) []byte {
	if len(rects) == 0 {
		return content
	}
	runs := scanRuns(content)

	// Collect the byte ranges of runs that intersect a region.
	type span struct{ start, end int }
	var cut []span
	for _, r := range runs {
		if intersectsAny(r, rects) {
			cut = append(cut, span{r.start, r.end})
		}
	}
	sort.Slice(cut, func(i, j int) bool { return cut[i].start < cut[j].start })

	var b strings.Builder
	prev := 0
	for _, c := range cut {
		if c.start < prev { // overlapping/adjacent guard
			continue
		}
		b.Write(content[prev:c.start])
		b.WriteByte(' ') // keep tokens on either side separated
		prev = c.end
	}
	if prev <= len(content) {
		b.Write(content[prev:])
	}

	// Draw the display rectangles on top, after all page content.
	for _, r := range rects {
		writeBlackRect(&b, r)
	}
	return []byte(b.String())
}

// FindTextRects returns the page-space bounding boxes of text runs whose decoded
// text matches pattern. Matching is per-run (a match spanning two runs is not
// detected — a documented Wave-2 limitation).
func FindTextRects(content []byte, pattern *regexp.Regexp) []Rect {
	var out []Rect
	for _, r := range scanRuns(content) {
		if r.text != "" && pattern.MatchString(r.text) {
			out = append(out, Rect{X: r.minX, Y: r.minY, Width: r.maxX - r.minX, Height: r.maxY - r.minY})
		}
	}
	return out
}

func intersectsAny(r textRun, rects []Rect) bool {
	for _, rc := range rects {
		if r.minX < rc.X+rc.Width && r.maxX > rc.X &&
			r.minY < rc.Y+rc.Height && r.maxY > rc.Y {
			return true
		}
	}
	return false
}

func writeBlackRect(b *strings.Builder, r Rect) {
	b.WriteString("\nq ")
	writeNums(b, fillR, fillG, fillB)
	b.WriteString("rg ")
	writeNums(b, r.X, r.Y, r.Width, r.Height)
	b.WriteString("re f Q\n")
}

// --- content-stream interpreter (tracks CTM + text matrix) ---

type gstate struct {
	ctm      matrix
	fontSize float64
	hScale   float64
	leading  float64
	rise     float64
}

// scanRuns walks the content stream and returns every text-showing operator as a
// textRun with its conservative page-space bounding box.
func scanRuns(content []byte) []textRun {
	spans := parse.ScanContentSpans(content)
	var runs []textRun

	gs := gstate{ctm: identity(), hScale: 1}
	var stack []gstate
	tm, tlm := identity(), identity()

	show := func(raw []byte, start, end int) {
		text := latin1(raw)
		wEm := float64(len([]rune(text))) * bboxCharWidth
		trm := mul(mul(matrix{gs.fontSize * gs.hScale, 0, 0, gs.fontSize, 0, gs.rise}, tm), gs.ctm)
		run := boundingRun(trm, wEm, start, end)
		run.text = text
		runs = append(runs, run)
		// Advance the pen for the next run (approximate).
		adv := float64(len([]rune(text))) * advanceCharWidth * gs.fontSize * gs.hScale
		tm = mul(translate(adv, 0), tm)
	}

	for _, s := range spans {
		o := s.Operands
		switch s.Op {
		case "q":
			stack = append(stack, gs)
		case "Q":
			if n := len(stack); n > 0 {
				gs = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if m, ok := mat6(o); ok {
				gs.ctm = mul(m, gs.ctm)
			}
		case "BT":
			tm, tlm = identity(), identity()
		case "Tf":
			gs.fontSize = lastNum(o)
		case "Tz":
			gs.hScale = lastNum(o) / 100
		case "TL":
			gs.leading = lastNum(o)
		case "Ts":
			gs.rise = lastNum(o)
		case "Td":
			if len(o) >= 2 {
				tlm = mul(translate(num(o[len(o)-2]), num(o[len(o)-1])), tlm)
				tm = tlm
			}
		case "TD":
			if len(o) >= 2 {
				ty := num(o[len(o)-1])
				gs.leading = -ty
				tlm = mul(translate(num(o[len(o)-2]), ty), tlm)
				tm = tlm
			}
		case "Tm":
			if m, ok := mat6(o); ok {
				tlm = m
				tm = m
			}
		case "T*":
			tlm = mul(translate(0, -gs.leading), tlm)
			tm = tlm
		case "Tj":
			if raw, ok := lastStr(o); ok {
				show(raw, s.Start, s.End)
			}
		case "'":
			tlm = mul(translate(0, -gs.leading), tlm)
			tm = tlm
			if raw, ok := lastStr(o); ok {
				show(raw, s.Start, s.End)
			}
		case "\"":
			tlm = mul(translate(0, -gs.leading), tlm)
			tm = tlm
			if raw, ok := lastStr(o); ok {
				show(raw, s.Start, s.End)
			}
		case "TJ":
			// Every string in the array uses the whole TJ op's span, so if any
			// element intersects a region the entire TJ operator is removed.
			for _, el := range lastArray(o) {
				switch v := el.(type) {
				case parse.String:
					show(v.Value, s.Start, s.End)
				case parse.Integer, parse.Real:
					adj := -num(v) / 1000 * gs.fontSize * gs.hScale
					tm = mul(translate(adj, 0), tm)
				}
			}
		}
	}
	return runs
}

// lastArray returns the last Array operand (the TJ array), or nil.
func lastArray(o []parse.Object) parse.Array {
	for i := len(o) - 1; i >= 0; i-- {
		if a, ok := o[i].(parse.Array); ok {
			return a
		}
	}
	return nil
}

// boundingRun transforms the run box (0..wEm in x, descender..ascender in y, em
// units) by trm and returns the page-space axis-aligned bounding box.
func boundingRun(trm matrix, wEm float64, start, end int) textRun {
	corners := [4][2]float64{
		{0, descenderEm}, {wEm, descenderEm}, {wEm, ascenderEm}, {0, ascenderEm},
	}
	minX, minY := 1e308, 1e308
	maxX, maxY := -1e308, -1e308
	for _, c := range corners {
		x, y := trm.apply(c[0], c[1])
		minX = min(minX, x)
		minY = min(minY, y)
		maxX = max(maxX, x)
		maxY = max(maxY, y)
	}
	return textRun{minX: minX, minY: minY, maxX: maxX, maxY: maxY, start: start, end: end}
}

// --- 2x3 affine matrix (row-vector convention) ---

type matrix [6]float64

func identity() matrix              { return matrix{1, 0, 0, 1, 0, 0} }
func translate(x, y float64) matrix { return matrix{1, 0, 0, 1, x, y} }

func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func mul(m, n matrix) matrix { return m.mul(n) }

func (m matrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// mat6 builds a matrix from the last six numeric operands.
func mat6(o []parse.Object) (matrix, bool) {
	if len(o) < 6 {
		return matrix{}, false
	}
	b := o[len(o)-6:]
	return matrix{num(b[0]), num(b[1]), num(b[2]), num(b[3]), num(b[4]), num(b[5])}, true
}

// --- operand + encoding helpers ---

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

func lastNum(o []parse.Object) float64 {
	if len(o) == 0 {
		return 0
	}
	return num(o[len(o)-1])
}

func lastStr(o []parse.Object) ([]byte, bool) {
	for i := len(o) - 1; i >= 0; i-- {
		if s, ok := o[i].(parse.String); ok {
			return s.Value, true
		}
	}
	return nil, false
}

// latin1 decodes shown-string bytes as Latin-1 (bytes map 1:1 to runes), which
// is exact for the ASCII range covering the regex-matching use.
func latin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

func writeNums(b *strings.Builder, vals ...float64) {
	for _, v := range vals {
		b.WriteString(formatNum(v))
		b.WriteByte(' ')
	}
}

func formatNum(f float64) string {
	if f != f || f > 1e308 || f < -1e308 {
		return "0"
	}
	s := strconv.FormatFloat(f, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "" || s == "-0" {
		s = "0"
	}
	return s
}

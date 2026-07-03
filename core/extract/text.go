// Package extract implements text (and, later, table) extraction from a parsed
// PDF. It reads a page's decoded content stream, interprets the text-showing and
// text-positioning operators (ISO 32000-1 §9.4), and decodes shown strings to
// Unicode via each font's /ToUnicode CMap, falling back to Latin-1 for simple
// fonts without one.
//
// Scope (Wave 1): the goal is readable text for the common ~90% of real PDFs
// (Word, LaTeX, Chrome print, Adobe). Line breaks and inter-word spacing use
// documented heuristics on the text matrix rather than full glyph-width metrics;
// that refinement is deferred.
package extract

import (
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Options controls text extraction.
type Options struct {
	// PreserveLayout is accepted for API parity with core.ExtractOptions; the
	// Wave 1 extractor always produces reading-order text and ignores it.
	PreserveLayout bool
}

// tjSpaceThreshold is the minimum negative TJ adjustment (in thousandths of an
// em) that is treated as an inter-word gap and rendered as a space. Kerning
// adjustments are typically smaller in magnitude; word gaps are larger.
const tjSpaceThreshold = 100.0

// PageText extracts the text of a single page in reading order. r resolves
// indirect objects; pi is the page whose /Contents and /Resources are read.
//
// Returns the extracted text (possibly empty for a page with no text), or an
// error only if the content stream cannot be resolved or decoded.
func PageText(r *parse.Resolver, pi parse.PageInfo, opts Options) (string, error) {
	content, err := r.PageContent(pi)
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", nil
	}

	fonts := buildFonts(r, pi.Resources)
	st := &textState{fonts: fonts}
	for _, op := range parse.ScanContent(content) {
		st.apply(op)
	}
	return st.out.String(), nil
}

// textState is the running text-object state while interpreting operators.
type textState struct {
	out   strings.Builder
	fonts map[parse.Name]*font

	cur      *font
	fontSize float64
	leading  float64 // set by TL and by TD

	// tlm is the text line matrix [a b c d e f]; only its translation (e, f) is
	// used for line/space heuristics. tm (the text matrix) tracks the same for
	// show operators that begin a run.
	tlm [6]float64

	emitted   bool    // any glyph written yet
	haveLine  bool    // a line position has been established
	lastLineY float64 // baseline Y of the current line
}

// apply interprets one content operator, mutating the text state and appending
// any decoded text.
func (s *textState) apply(op parse.ContentOp) {
	switch op.Op {
	case "BT":
		// A text object resets the text/line matrix to identity (§9.4.1). We
		// keep the last line position so a move in the new object is still
		// compared against the previous one for line-break/space decisions.
		s.tlm = identity()
	case "ET":
		// nothing to flush; text is emitted eagerly

	case "Tf":
		// /Font size Tf
		if len(op.Operands) >= 2 {
			if name, ok := op.Operands[len(op.Operands)-2].(parse.Name); ok {
				s.cur = s.fonts[name]
			}
			s.fontSize = asFloat(op.Operands[len(op.Operands)-1])
		}
	case "TL":
		if len(op.Operands) >= 1 {
			s.leading = asFloat(op.Operands[len(op.Operands)-1])
		}

	case "Td":
		if len(op.Operands) >= 2 {
			s.moveLine(asFloat(op.Operands[len(op.Operands)-2]), asFloat(op.Operands[len(op.Operands)-1]))
		}
	case "TD":
		if len(op.Operands) >= 2 {
			ty := asFloat(op.Operands[len(op.Operands)-1])
			s.leading = -ty
			s.moveLine(asFloat(op.Operands[len(op.Operands)-2]), ty)
		}
	case "Tm":
		// a b c d e f Tm — set the line matrix absolutely.
		if len(op.Operands) >= 6 {
			var m [6]float64
			for i := 0; i < 6; i++ {
				m[i] = asFloat(op.Operands[len(op.Operands)-6+i])
			}
			s.setLine(m)
		}
	case "T*":
		s.moveLine(0, -s.leading)

	case "Tj":
		s.show(lastString(op.Operands))
	case "'":
		// Move to next line, then show.
		s.moveLine(0, -s.leading)
		s.show(lastString(op.Operands))
	case "\"":
		// aw ac string " — set word/char spacing (ignored), next line, show.
		s.moveLine(0, -s.leading)
		s.show(lastString(op.Operands))
	case "TJ":
		s.showArray(op.Operands)
	}
}

// moveLine applies a relative Td-style move to the text line matrix and updates
// the line/space heuristics.
func (s *textState) moveLine(tx, ty float64) {
	s.tlm = mul(translate(tx, ty), s.tlm)
	s.setLine(s.tlm)
}

// setLine sets the text line matrix absolutely and emits a newline when the
// baseline moves vertically far enough to be a new line rather than a
// sub/superscript shift.
//
// Inter-word spacing on the same line is deliberately NOT inferred from the
// horizontal position here: without glyph-width metrics, a document that
// positions every glyph with its own Tm/Td is indistinguishable from one that
// positions words, so a gap-based rule would split every glyph. Instead we rely
// on explicit space characters in the shown strings and on TJ adjustments
// (showArray) — which is what Word, Chrome, LaTeX, and Adobe emit. Recovering
// spacing for glyph-positioned PDFs is a Wave 2 refinement (needs font widths).
func (s *textState) setLine(m [6]float64) {
	y := m[5]
	if s.emitted && s.haveLine {
		if dy := s.lastLineY - y; abs(dy) > s.newlineThreshold() {
			s.out.WriteByte('\n')
		}
	}
	s.tlm = m
	s.lastLineY = y
	s.haveLine = true
}

// newlineThreshold is the vertical movement above which a repositioning is
// treated as a new line rather than a sub/superscript shift.
func (s *textState) newlineThreshold() float64 {
	if s.fontSize > 0 {
		return 0.5 * s.fontSize
	}
	return 2.0
}

// show decodes and appends a single shown string.
func (s *textState) show(raw []byte) {
	if raw == nil {
		return
	}
	text := decode(s.cur, raw)
	if text == "" {
		return
	}
	s.out.WriteString(text)
	s.emitted = true
}

// showArray handles TJ: an array of strings interleaved with numeric position
// adjustments. A sufficiently large negative adjustment is an inter-word gap.
func (s *textState) showArray(operands []parse.Object) {
	arr, ok := lastArray(operands)
	if !ok {
		return
	}
	for _, el := range arr {
		switch v := el.(type) {
		case parse.String:
			s.show(v.Value)
		case parse.Integer:
			if float64(v) <= -tjSpaceThreshold {
				s.maybeSpace()
			}
		case parse.Real:
			if float64(v) <= -tjSpaceThreshold {
				s.maybeSpace()
			}
		}
	}
}

// maybeSpace appends a space unless the output already ends with whitespace.
func (s *textState) maybeSpace() {
	buf := s.out.String()
	if buf == "" {
		return
	}
	switch buf[len(buf)-1] {
	case ' ', '\n', '\t':
		return
	}
	s.out.WriteByte(' ')
}

// --- operand helpers ---

// lastString returns the bytes of the last String operand, or nil.
func lastString(operands []parse.Object) []byte {
	for i := len(operands) - 1; i >= 0; i-- {
		if s, ok := operands[i].(parse.String); ok {
			return s.Value
		}
	}
	return nil
}

// lastArray returns the last Array operand.
func lastArray(operands []parse.Object) (parse.Array, bool) {
	for i := len(operands) - 1; i >= 0; i-- {
		if a, ok := operands[i].(parse.Array); ok {
			return a, true
		}
	}
	return nil, false
}

// asFloat converts an Integer or Real operand to float64 (0 otherwise).
func asFloat(o parse.Object) float64 {
	switch v := o.(type) {
	case parse.Integer:
		return float64(v)
	case parse.Real:
		return float64(v)
	default:
		return 0
	}
}

// --- 2x3 affine matrix helpers ([a b c d e f] = [[a b 0],[c d 0],[e f 1]]) ---

func identity() [6]float64 { return [6]float64{1, 0, 0, 1, 0, 0} }

func translate(tx, ty float64) [6]float64 { return [6]float64{1, 0, 0, 1, tx, ty} }

// mul returns m * n.
func mul(m, n [6]float64) [6]float64 {
	return [6]float64{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

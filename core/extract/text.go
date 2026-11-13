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
	"github.com/thai-pc/fluxdocs/core/internal/textflow"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// Options controls text extraction.
type Options struct {
	// PreserveLayout is accepted for API parity with core.ExtractOptions; the
	// Wave 1 extractor always produces reading-order text and ignores it.
	PreserveLayout bool
}

// tjSpaceThreshold is the TJ word-gap threshold of the text reading
// (textflow.SpaceThreshold); table runs read the same gaps as spaces.
const tjSpaceThreshold = textflow.SpaceThreshold

// PageText extracts the text of a single page in reading order. r resolves
// indirect objects; pi is the page whose /Contents and /Resources are read.
// The reading itself (line breaks, TJ-gap spaces) is textflow.Flow's.
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

	fl := textflow.New(textflow.PageFonts(r, pi.Resources, loadFont(r)))
	for _, op := range parse.ScanContent(content) {
		fl.Apply(op.Op, op.Operands, textflow.NoOwner)
	}
	return fl.String(), nil
}

// --- operand helpers (table.go) ---

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

// Package redact is the redaction engine behind core's RedactText, RedactArea
// and RedactAndFlatten (docs/design/redaction-hardening.md). It PERMANENTLY
// removes content — not merely painting over it — then draws a solid black
// rectangle for display. This is FluxDocs's reputation-critical guarantee
// (.claude/SECURITY.md §1): after redaction the removed text must not be
// recoverable by extraction or copy-paste.
//
// The model (§5): the page content is read with the strict scanner
// (parse.ScanContentStrict), and every route by which the page paints another
// content stream — Form XObjects, soft-mask groups, tiling patterns, Type 3
// glyph procedures — is walked (walk.go). The page's text model
// (textmodel.go) decodes shown strings through the shared font model
// (core/internal/fontmodel), as text extraction does, and gives each run a
// box that over-approximates where its glyphs can be; property lists of
// marked content carry their /ActualText, /Alt and /E text. Matched runs are
// removed by span whatever their box; runs whose box meets a region (closed
// intervals) are removed too, and so is everything else the page paints
// there — images, inline images, text-free forms, painted paths and shadings
// (§6.10, D5) — with the colour, pattern and ExtGState operators only removed
// paint consumed. Every property-list text key on the page is stripped (D7),
// and the page keeps only the resources its surviving content uses (page.go).
// The objects only removed content used are reported (Output.Removed) so that
// core keeps them out of the whole output unless kept content looks them up
// (Output.Used, Painter).
// The removal is byte-exact: removed operators are cut out of the original
// bytes and every other byte is kept. The engine may over-remove, never
// under-remove.
//
// Redaction fails closed on content it cannot read or rewrite: content the
// strict scanner cannot read the way every reader does, a child unit that
// shows text, an unsupported XObject subtype or an exceeded budget is a
// *Refusal, which core reports as a *core.RedactionError. Rendering and text
// extraction stay lenient.
//
// Scope (Phase 1): the page's own content is rewritten, with text-showing
// operators removed whole. The package is internal: only core uses it, and
// its API has no compatibility guarantee. It is pure Go (MIT-safe) and must
// not import core (import cycle) — core translates its own types into
// redact.Rect.
package redact

import (
	"bytes"
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

func writeBlackRect(b *bytes.Buffer, r Rect) {
	b.WriteString("\nq ")
	writeNums(b, fillR, fillG, fillB)
	b.WriteString("rg ")
	writeNums(b, r.X, r.Y, r.Width, r.Height)
	b.WriteString("re f Q\n")
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

func (m matrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// mat6 builds a matrix from six numeric operands.
func mat6(o []parse.Object) matrix {
	return matrix{num(o[0]), num(o[1]), num(o[2]), num(o[3]), num(o[4]), num(o[5])}
}

// --- operand helpers ---

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

func writeNums(b *bytes.Buffer, vals ...float64) {
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

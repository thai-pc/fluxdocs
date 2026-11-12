package render

import (
	"math"
	"strings"
	"sync"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/internal/std14"
	"github.com/thai-pc/fluxdocs/core/internal/std14/faces"
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Substitution of non-embedded simple fonts (docs/design/type1-std14-fonts.md
// §2.4, Phase 3). A simple font whose descriptor holds no program (or that
// has no descriptor at all, as the standard 14 usually do) is drawn from a
// bundled face: the standard face its /BaseFont names or aliases
// (std14.Normalize), else a Latin face chosen by the descriptor's flags.
// Its advances stay the PDF's: /Widths, then for a standard face the Adobe
// AFM widths, then /MissingWidth (glyphFont.advance), so the substitute
// only decides how glyph shapes fill their slots.

// Font descriptor flags (ISO 32000-1 §9.8.2, Table 123): bit position n is
// 1 << (n − 1).
const (
	flagFixedPitch  = 1 << 0
	flagSerif       = 1 << 1
	flagSymbolic    = 1 << 2
	flagNonsymbolic = 1 << 5
	flagItalic      = 1 << 6
	flagForceBold   = 1 << 18
)

// Fit rule (design §2.4, Q6): a substitute glyph whose advance is within
// fitTolerance of the PDF's width w0 is drawn unscaled; otherwise it is
// compressed horizontally by w0 / advance, never below minFit, and never
// widened, so its ink stays inside the redaction model's substitutedBBox.
const (
	fitTolerance = 0.1
	minFit       = 0.6
)

// substituteFaces holds each bundled face, parsed once per process on first
// use. The faces are trusted, compiled-in data, so the parse is not charged
// to any render's budget: charging it to whichever render came first would
// make charges depend on render order. Symbol and ZapfDingbats share one
// face (and one entry: faceSlot). An sfnt.Font is safe for concurrent use
// with distinct Buffers; each substituteProgram has its own.
var substituteFaces [std14.NumFaces]struct {
	once sync.Once
	sf   *sfnt.Font
}

// faceSlot is the substituteFaces entry of face.
func faceSlot(face std14.Face) std14.Face {
	if face == std14.ZapfDingbats {
		return std14.Symbol
	}
	return face
}

// substituteFace returns the parsed bundled face for face, or nil when the
// faces are not compiled in (-tags nostd14) or face is out of range.
func substituteFace(face std14.Face) *sfnt.Font {
	if face >= std14.NumFaces {
		return nil
	}
	e := &substituteFaces[faceSlot(face)]
	e.once.Do(func() {
		if data := faces.Data(face); data != nil {
			if sf, err := sfnt.Parse(data); err == nil {
				e.sf = sf
			}
		}
	})
	return e.sf
}

// substituteChoice is what a non-embedded simple font is drawn with.
type substituteChoice struct {
	face std14.Face
	// standard is set when the /BaseFont names or aliases a standard font:
	// its missing widths come from the AFM, never from the substitute.
	standard bool
	// draw is false for a symbolic font that is neither Symbol nor
	// ZapfDingbats: its codes' meaning is unknown, so nothing is drawn
	// (the text still advances by its widths).
	draw bool
}

// chooseSubstitute picks the face for a non-embedded simple font (design
// §2.4): the standard face its /BaseFont normalizes to; else, by the
// descriptor's flags (fdesc may be nil: flags 0), a symbolic font that is
// not also nonsymbolic is not drawn, and a Latin face is chosen: FixedPitch
// → Courier, Serif → Times, else Helvetica; italic for the Italic flag, a
// non-zero /ItalicAngle or "Italic"/"Oblique" in the name; bold for
// ForceBold or "Bold"/"Black"/"Heavy" in the name ("Semibold" included).
func chooseSubstitute(r *parse.Resolver, fd, fdesc parse.Dict) substituteChoice {
	base := resolvedName(r, fd["BaseFont"])
	if face, ok := std14.Normalize(base); ok {
		return substituteChoice{face: face, standard: true, draw: true}
	}
	var flags int64
	if v, ok := resolvedNumber(r, fdesc["Flags"]); ok && v >= 0 && v <= 1<<32 {
		flags = int64(v)
	}
	if flags&flagSymbolic != 0 && flags&flagNonsymbolic == 0 {
		return substituteChoice{draw: false}
	}
	family := std14.Sans
	switch {
	case flags&flagFixedPitch != 0:
		family = std14.Mono
	case flags&flagSerif != 0:
		family = std14.Serif
	}
	lower := strings.ToLower(base)
	angle, _ := resolvedNumber(r, fdesc["ItalicAngle"])
	italic := flags&flagItalic != 0 || angle != 0 ||
		strings.Contains(lower, "italic") || strings.Contains(lower, "oblique")
	bold := flags&flagForceBold != 0 || strings.Contains(lower, "bold") ||
		strings.Contains(lower, "black") || strings.Contains(lower, "heavy")
	return substituteChoice{face: std14.FaceFor(family, bold, italic), draw: true}
}

// resolvedName returns obj resolved to a name, "" otherwise. A name longer
// than 256 bytes (no font or subtype name comes close) is returned as "" too,
// so no caller scans a huge one.
func resolvedName(r *parse.Resolver, obj parse.Object) string {
	if obj == nil {
		return ""
	}
	v, err := r.Resolve(obj)
	if err != nil {
		return ""
	}
	n, ok := v.(parse.Name)
	if !ok || len(n) > 256 {
		return ""
	}
	return string(n)
}

// resolvedNumber returns obj resolved to a finite number.
func resolvedNumber(r *parse.Resolver, obj parse.Object) (float64, bool) {
	if obj == nil {
		return 0, false
	}
	v, err := r.Resolve(obj)
	if err != nil {
		return 0, false
	}
	switch n := v.(type) {
	case parse.Integer:
		return float64(n), true
	case parse.Real:
		f := float64(n)
		if f != f || f > 1e300 || f < -1e300 {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// afmWidths are a standard font's AFM widths by code, in glyph units
// (noAFMWidth: none), for codes its /Widths does not give.
type afmWidths [256]uint16

const noAFMWidth = 0xFFFF

// loadSubstitute builds a non-embedded simple font's program (design §2.4,
// §4.1 substituteProgram): each code's glyph name (as glyphNames gives it,
// over the face's built-in encoding: StandardEncoding for the Latin faces,
// the fonts' own for Symbol and ZapfDingbats), its Unicode value
// (std14.Rune, then std14.AltRune) and the bundled face's glyph for it. A
// standard font also gets its AFM widths (gf.afm). The fit scale of each
// code is computed from the final advance (gf.advance). The per-code tables
// of the face and base encoding come from a process-wide cache
// (encodingTableFor); only the codes /Differences renames are looked up per
// font. fdesc may be nil.
func loadSubstitute(r *parse.Resolver, fd, fdesc parse.Dict, gf *glyphFont) {
	choice := chooseSubstitute(r, fd, fdesc)
	if !choice.draw {
		gf.prog = nullProgram{}
		return
	}
	p := &substituteProgram{sf: substituteFace(choice.face)}
	t := encodingTableFor(choice.face, baseEncodingOf(gf.model))
	p.gids = t.gids
	adv := t.adv
	var afm afmWidths
	if choice.standard {
		afm = t.afm
	}
	for c := range p.gids {
		if n, ok := gf.model.Difference(byte(c)); ok {
			p.gids[c], afm[c], adv[c] = codeEntry(choice.face, p.sf, &p.buf, string(n))
		}
	}
	if choice.standard {
		gf.std14 = true
		gf.afm = &afm
	}
	gf.prog = p
	for c := range p.fit {
		p.fit[c] = 1
		if p.gids[c] == 0 {
			continue
		}
		w0 := gf.advance(uint32(c), uint32(c), true)
		if a := adv[c]; a > 0 && float64(a-w0) > float64(fitTolerance*w0) {
			p.fit[c] = float32(min(max(w0/a, minFit), 1))
		}
	}
}

// codeEntry returns what a code whose glyph name is name maps to in face:
// the bundled face's glyph (0 = none, or sf nil), the AFM width (noAFMWidth
// = none) and the glyph's advance in the face in em (0 when it has none).
func codeEntry(face std14.Face, sf *sfnt.Font, buf *sfnt.Buffer, name string) (gid uint16, afm uint16, adv float64) {
	afm = noAFMWidth
	if name == "" {
		return 0, afm, 0
	}
	if w, ok := std14.Width(face, name); ok {
		afm = uint16(w)
	}
	if sf == nil {
		return 0, afm, 0
	}
	var g sfnt.GlyphIndex
	if u, ok := std14.Rune(name); ok {
		g, _ = sf.GlyphIndex(buf, u)
	}
	if alt, ok := std14.AltRune(name); ok && g == 0 {
		g, _ = sf.GlyphIndex(buf, alt)
	}
	if g == 0 {
		return 0, afm, 0
	}
	if a, err := sf.GlyphAdvance(buf, g, glyphPPEM, font.HintingNone); err == nil {
		adv = float64(a) / 64.0 / 1000.0
	}
	return uint16(g), afm, adv
}

// Base encodings a substitute's code map starts from (baseEncodingOf).
const (
	baseBuiltin  = iota // the face's built-in encoding (std14.BuiltinEncoding)
	baseWinAnsi         // WinAnsiEncoding
	baseMacRoman        // MacRomanEncoding
	baseStandard        // StandardEncoding named explicitly
	numBases
)

// baseEncodingOf returns the base encoding glyphNames starts from for model:
// the /Encoding or /BaseEncoding name when std14 knows it, else the
// built-in encoding.
func baseEncodingOf(model *fontmodel.Font) int {
	n, ok := model.BaseEncoding()
	if !ok {
		return baseBuiltin
	}
	switch n {
	case "WinAnsiEncoding":
		return baseWinAnsi
	case "MacRomanEncoding":
		return baseMacRoman
	case "StandardEncoding":
		return baseStandard
	}
	return baseBuiltin
}

// encodingTable is a face's code map over one base encoding, before
// /Differences: codeEntry of every code's name.
type encodingTable struct {
	gids [256]uint16
	afm  afmWidths
	adv  [256]float64
}

// encodingTables caches encodingTableFor, process-wide: a table depends only
// on compiled-in data, so it is the same for every document and render.
var encodingTables [std14.NumFaces][numBases]struct {
	once sync.Once
	t    *encodingTable
}

// encodingTableFor returns the cached code map of face over base.
func encodingTableFor(face std14.Face, base int) *encodingTable {
	e := &encodingTables[face][base]
	e.once.Do(func() {
		names := std14.BuiltinEncoding(face)
		switch base {
		case baseWinAnsi:
			names, _ = std14.BaseEncoding("WinAnsiEncoding")
		case baseMacRoman:
			names, _ = std14.BaseEncoding("MacRomanEncoding")
		case baseStandard:
			names = std14.StandardEncoding()
		}
		t := new(encodingTable)
		sf := substituteFace(face)
		var buf sfnt.Buffer
		for c, n := range names {
			t.gids[c], t.afm[c], t.adv[c] = codeEntry(face, sf, &buf, n)
		}
		e.t = t
	})
	return e.t
}

// substituteProgram draws a non-embedded simple font from a bundled face
// (sfnt, shared process-wide). Its "glyph IDs" are the character codes
// themselves: glyph(code) returns code when the face has a glyph for it,
// and subpaths looks up the face's glyph and the code's fit scale, which
// depends on the code's width, not only on the glyph.
type substituteProgram struct {
	sf   *sfnt.Font // nil: faces not compiled in, nothing is drawn
	buf  sfnt.Buffer
	gids [256]uint16  // face glyph per code, 0 = none
	fit  [256]float32 // horizontal scale per code, in [minFit, 1]
}

func (p *substituteProgram) glyph(code uint32) (uint32, bool) {
	if p.sf == nil || code > 255 || p.gids[code] == 0 {
		return 0, false
	}
	return code, true
}

// advanceEm returns the face's own advance of code's glyph in em (used only
// for a non-standard font with neither /Widths nor /MissingWidth), 0.5 when
// it has none.
func (p *substituteProgram) advanceEm(code uint32) float64 {
	if p.sf == nil || code > 255 || p.gids[code] == 0 {
		return 0.5
	}
	adv, err := p.sf.GlyphAdvance(&p.buf, sfnt.GlyphIndex(p.gids[code]), glyphPPEM, font.HintingNone)
	if err != nil {
		return 0.5
	}
	return float64(adv) / 64.0 / 1000.0
}

// subpaths loads code's glyph at glyphPPEM (thousandths of an em, y down),
// scales it to text space by fontSize/1000 and the code's fit, and maps it
// through the text matrix and CTM, every product rounded explicitly
// (mulExact, applyExact) so native and wasm builds draw the same points.
func (p *substituteProgram) subpaths(code uint32, x glyphXform) ([][]point, bool) {
	if p.sf == nil || code > 255 || p.gids[code] == 0 {
		return nil, false
	}
	segs, err := p.sf.LoadGlyph(&p.buf, sfnt.GlyphIndex(p.gids[code]), glyphPPEM, nil)
	if err != nil {
		return nil, false
	}
	sg := float64(x.fontSize / 1000.0)
	sx := float64(float64(sg*x.hScale) * float64(p.fit[code]))
	glyphToText := matrix{sx, 0, 0, -sg, 0, x.rise}
	full := mulExact(mulExact(glyphToText, x.tm), x.ctm)
	return sfntToSubpathsExact(segs, full), true
}

// sfntToSubpathsExact is segmentsToSubpaths with explicit rounding of every
// product (no fused multiply-add on any target) and curves flattened by
// their device size (flattenQuadAdaptive, flattenCubicAdaptive).
func sfntToSubpathsExact(segs []sfnt.Segment, m matrix) [][]point {
	tp := func(q fixed.Point26_6) point {
		x, y := applyExact(m, float64(q.X)/64, float64(q.Y)/64)
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
			c, e := tp(s.Args[0]), tp(s.Args[1])
			cur = flattenQuadAdaptive(cur, last, c, e)
			last = e
		case sfnt.SegmentOpCubeTo:
			c1, c2, e := tp(s.Args[0]), tp(s.Args[1]), tp(s.Args[2])
			cur = flattenCubicAdaptive(cur, last, c1, c2, e)
			last = e
		}
	}
	if len(cur) > 0 {
		subs = append(subs, cur)
	}
	return subs
}

// flattenQuadAdaptive appends a polyline approximation of the quadratic
// Bézier p0 c p1 (p0 already in dst) of n uniform segments, n the smallest
// count, at most quadSegments, that keeps it within flattenTolerance of the
// curve. A quadratic's second derivative is the constant 2·d, d = p0 − 2c +
// p1, and a polyline of n uniform segments strays at most |2d| / (8n²) from
// it, so n = ⌈√(|d| / (4·tol))⌉ = ⌈√(2|d|)⌉ at tol 1/8. A NaN or infinite
// |d| gives quadSegments. Every product is rounded separately (no fused
// multiply-add), so the count and the points are the same on every target.
func flattenQuadAdaptive(dst []point, p0, c, p1 point) []point {
	dx, dy := (p0.X-c.X)-(c.X-p1.X), (p0.Y-c.Y)-(c.Y-p1.Y)
	l := math.Sqrt(float64(dx*dx) + float64(dy*dy))
	n := quadSegments
	if v := math.Ceil(math.Sqrt(float64(l * (0.25 / flattenTolerance)))); v < quadSegments {
		n = max(int(v), 1)
	}
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		a, b, e := float64(u*u), float64(2*u)*t, float64(t*t)
		dst = append(dst, point{
			X: float64(a*p0.X) + float64(b*c.X) + float64(e*p1.X),
			Y: float64(a*p0.Y) + float64(b*c.Y) + float64(e*p1.Y),
		})
	}
	return dst
}

// quadSegments is the most segments flattenQuadAdaptive splits a quadratic
// into: flattenQuad's fixed count.
const quadSegments = 8

// nullProgram draws nothing: the program of a non-embedded symbolic font
// whose codes cannot be interpreted. Its text still advances.
type nullProgram struct{}

func (nullProgram) glyph(uint32) (uint32, bool)                   { return 0, false }
func (nullProgram) advanceEm(uint32) float64                      { return 0.5 }
func (nullProgram) subpaths(uint32, glyphXform) ([][]point, bool) { return nil, false }

// Package fontmodel is the shared model of a PDF font (ISO 32000-1 §9): how
// the bytes of a shown string split into character codes, what Unicode text
// they stand for, and — for layout — how far each code advances the pen and
// how far its glyph may reach. Text extraction (core/extract) and redaction
// (core/internal/redact) both read fonts through it, so RedactText matches the
// text ExtractText returns (docs/design/redaction-hardening.md §5.3, D12).
//
// Version 1 (redaction-hardening step 1.3): code segmentation per font type
// (one byte for simple and Type 3 fonts; codespace ranges for Type 0 fonts,
// from the Identity, UCS2 and UTF16 predefined CMaps or an embedded encoding
// CMap), /ToUnicode mapping, simple-font /Encoding /Differences glyph names,
// and over-approximated metrics (/Widths, /W, /DW, /FontBBox, Type 3
// /FontMatrix). Exact vertical metrics (/W2, /DW2) and standard-14 widths are
// later work.
//
// Clean-room: written from ISO 32000-1:2008 only. Pure Go; Core (MIT).
package fontmodel

import (
	"math"
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Kind is a font's type, from its resolved /Subtype.
type Kind int

const (
	// Simple is a single-byte font: Type1, MMType1, TrueType, or a font whose
	// /Subtype is missing or unknown (§9.6).
	Simple Kind = iota
	// Composite is a Type 0 font: codes of 1–4 bytes select CIDs (§9.7).
	Composite
	// Type3 is a Type 3 font: each glyph is a content stream in /CharProcs
	// (§9.6.5).
	Type3
)

// Options configures Load.
type Options struct {
	// Metrics also reads what layout needs: the resolved type, the codespace
	// of a Type 0 font's encoding, widths, /FontBBox, the Type 3 /FontMatrix
	// and simple-font /Differences. Text extraction does not need them.
	Metrics bool
	// Decode decodes a font stream (/ToUnicode, an embedded encoding CMap).
	// Nil uses parse.DecodeStream. A caller that meters decoded bytes passes
	// its own; a failed decode is treated as an absent stream.
	Decode func(*parse.Stream) ([]byte, error)
}

// Font is the model of one font dictionary. A Font is immutable after Load
// and safe for concurrent use. The nil *Font is a valid font with nothing
// known: Text decodes it as Latin-1, as for a missing font resource.
type Font struct {
	dict    parse.Dict
	toUni   *cmap
	twoByte bool // direct /Subtype /Type0, the test Text has always used

	metrics bool
	kind    Kind
	// space is the Type 0 codespace; spaceOK is false when the encoding's
	// codespace is unknown (a predefined CMap other than Identity, UCS2 or
	// UTF16, or an embedded CMap that could not be read), so segmentation is
	// uncertain.
	space    []spaceRange
	spaceOK  bool
	vertical bool // Type 0 with WMode 1
	identity bool // Type 0 with an Identity encoding: CID = code

	// scale maps glyph space to text space at font size 1: the Type 3
	// /FontMatrix, or 1/1000 for every other font. scaleOK is false for a
	// Type 3 font without a usable matrix.
	scale   [6]float64
	scaleOK bool

	widths       [256]float64 // simple fonts, glyph units
	hasWidth     [256]bool
	missingWidth float64 // simple fonts, glyph units (/MissingWidth, default 0)

	dw      float64             // Type 0 /DW, glyph units (default 1000)
	cidLo   float64             // Type 0: the smallest of /DW and every /W width
	cidHi   float64             // Type 0: the largest of /DW and every /W width
	cidW    map[uint32]float64  // Type 0 /W "c [w1 w2 …]" entries
	cidRngs []cidRange          // Type 0 /W "cfirst clast w" entries
	bbox    [4]float64          // /FontBBox llx lly urx ury, glyph units
	bboxOK  bool                //
	diffs   map[byte]parse.Name // simple fonts: /Encoding /Differences

	wPartial  bool // Type 0: the /W array could not be read to its end
	hasWidths bool // simple fonts: a /Widths array is present
	embedded  bool // simple fonts: /FontDescriptor holds a font program

	// Vertical metrics of a Type 0 font (§9.7.4.3), glyph units: the hulls
	// over every CID of the position vector (vx, vy) and the vertical
	// displacement w1y, from /DW2, /W2 and vx = w0/2 by default. vertOK is
	// false when /DW2 or /W2 cannot be read.
	vx, vy, wy [2]float64
	vertOK     bool

	// substituted is set for a font with no embedded program (not Type 3):
	// a reader paints it with a font of its own, whose glyphs may reach
	// past the em defaults.
	substituted bool
}

// cidRange is one "cfirst clast w" entry of a CIDFont /W array.
type cidRange struct {
	first, last uint32
	w           float64
}

// Bounds on what Load reads from one font, so a hostile font costs bounded
// work: /W items, /Differences items.
const (
	maxWItems    = 1 << 16
	maxDiffItems = 1 << 12
)

// emDefault is the per-glyph bound used when a glyph's advance or ink is not
// known: 1.1 em covers the widest glyphs of the standard fonts (the widest is
// about 1.015 em) with margin (docs/design/redaction-hardening.md §6.6).
const emDefault = 1.1

// Default vertical ink bounds in em, below and above the baseline, used when
// /FontBBox is absent or smaller.
const (
	descenderEm = -0.25
	ascenderEm  = 0.9
)

// Load reads the font dictionary obj (a Reference, a dictionary, or a stream
// whose dictionary is the font). It never fails: whatever cannot be read is
// left unknown. Returns nil when obj does not resolve to a dictionary.
//
// Text keeps extraction's historical test for a composite font: a /Subtype
// written directly as /Type0. An indirect /Subtype (resolved by Kind and the
// metrics) is therefore read as a simple font by Text; this parity quirk keeps
// ExtractText's output unchanged, and redaction adds the per-byte readings
// (ByteText) so it still over-approximates.
func Load(r *parse.Resolver, obj parse.Object, opts Options) *Font {
	if r == nil || obj == nil {
		return nil
	}
	fd, ok, err := r.ResolveDict(obj)
	if err != nil || !ok {
		return nil
	}
	decode := opts.Decode
	if decode == nil {
		decode = parse.DecodeStream
	}
	f := &Font{dict: fd}
	if sub, ok := fd.GetName("Subtype"); ok && sub == "Type0" {
		f.twoByte = true
	}
	if st, ok := resolveStream(r, fd["ToUnicode"]); ok {
		if dec, err := decode(st); err == nil {
			f.toUni = parseCMap(dec)
		}
	}
	if opts.Metrics {
		f.loadMetrics(r, decode)
	}
	return f
}

// resolveStream resolves obj (following a reference) to a *parse.Stream.
func resolveStream(r *parse.Resolver, obj parse.Object) (*parse.Stream, bool) {
	if obj == nil {
		return nil, false
	}
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil, false
	}
	st, ok := resolved.(*parse.Stream)
	return st, ok
}

// Dict returns the resolved font dictionary (nil for the nil Font). Callers
// must not modify it.
func (f *Font) Dict() parse.Dict {
	if f == nil {
		return nil
	}
	return f.dict
}

// Kind returns the font's type (Simple for the nil Font, or when loaded
// without Options.Metrics).
func (f *Font) Kind() Kind {
	if f == nil {
		return Simple
	}
	return f.kind
}

// Text decodes a shown string's bytes to Unicode, exactly as text extraction
// always has.
//
// With a /ToUnicode CMap, codes are read at the CMap's width and mapped. Without
// one, a simple (single-byte) font is decoded as Latin-1, which is exact for the
// ASCII range covering most documents; a composite font without /ToUnicode
// cannot be decoded reliably and yields no text.
func (f *Font) Text(raw []byte) string {
	if f != nil && f.toUni != nil {
		return decodeWithCMap(f.toUni, raw)
	}
	if f != nil && f.twoByte {
		return "" // composite font, no /ToUnicode: not decodable
	}
	// Simple font, no /ToUnicode: bytes map 1:1 to runes (Latin-1).
	var b strings.Builder
	b.Grow(len(raw))
	for _, c := range raw {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// decodeWithCMap reads codes of the CMap's width and maps each to Unicode. An
// unmapped single-byte code falls back to Latin-1; an unmapped wide code is
// dropped.
func decodeWithCMap(c *cmap, raw []byte) string {
	w := c.codeLen
	if w < 1 {
		w = 1
	}
	var b strings.Builder
	for i := 0; i+w <= len(raw); i += w {
		code := bytesToCode(raw[i : i+w])
		if s, ok := c.lookup(code); ok {
			b.WriteString(s)
		} else if w == 1 {
			b.WriteRune(rune(code))
		}
	}
	return b.String()
}

// AltText is a second reading of a shown string for a simple font that has
// /Encoding /Differences and no /ToUnicode: each code renamed by /Differences
// is read through its glyph name (Annex D names, uniXXXX, uXXXX[XX]); other
// codes as Latin-1, as Text reads them. Viewers that map glyph names to
// Unicode show this text where Text shows the raw codes. Returns false when
// there is no such reading or it equals Text. Needs Options.Metrics.
func (f *Font) AltText(raw []byte) (string, bool) {
	if f == nil || f.toUni != nil || f.twoByte || len(f.diffs) == 0 {
		return "", false
	}
	var b strings.Builder
	changed := false
	for _, c := range raw {
		if name, ok := f.diffs[c]; ok {
			if r, ok := glyphRune(name); ok {
				b.WriteString(r)
				changed = changed || r != string(rune(c))
				continue
			}
		}
		b.WriteRune(rune(c))
	}
	if !changed {
		return "", false
	}
	return b.String(), true
}

// ByteText reads a shown string one byte per code, as a reader that treats the
// font as simple does whatever its /ToUnicode codespace says: each byte is
// mapped by /ToUnicode as a one-byte code if it maps it, else through its
// /Differences glyph name, else as Latin-1. Returns false for a Type 0 font
// (whose codes are not single bytes). Redaction matches it in addition to
// Text, so a simple font whose /ToUnicode declares two-byte codes, which Text
// then reads in pairs, is still matched by its bytes.
func (f *Font) ByteText(raw []byte) (string, bool) {
	if f != nil && (f.kind == Composite || f.twoByte) {
		return "", false
	}
	var b strings.Builder
	for _, c := range raw {
		if f != nil && f.toUni != nil {
			if s, ok := f.toUni.lookup(uint32(c)); ok {
				b.WriteString(s)
				continue
			}
		}
		if f != nil {
			if name, ok := f.diffs[c]; ok {
				if r, ok := glyphRune(name); ok {
					b.WriteString(r)
					continue
				}
			}
		}
		b.WriteRune(rune(c))
	}
	return b.String(), true
}

// Code is one character code of a shown string: raw[Start:End], Value its
// bytes big-endian.
type Code struct {
	Start, End int
	Value      uint32
}

// SingleByteSpace reports whether c is the single-byte code 32, the only code
// word spacing (Tw) applies to (§9.3.3).
func (c Code) SingleByteSpace() bool { return c.End-c.Start == 1 && c.Value == 32 }

// MaxCodeLen is the longest code the font's encoding can have: 1 for simple
// and Type 3 fonts, 4 for a Type 0 font.
func (f *Font) MaxCodeLen() int {
	if f != nil && (f.kind == Composite || f.twoByte) {
		return 4
	}
	return 1
}

// Codes splits a shown string into character codes (§9.4.3): one byte per
// code for simple and Type 3 fonts; for a Type 0 font, by the codespace
// ranges of its encoding, shortest match first (§9.7.6.2). Returns false when
// the split is uncertain: the codespace is unknown, or bytes match no range
// (§9.7.6.3 leaves the reader's recovery open). Needs Options.Metrics for
// Type 0 fonts; otherwise their segmentation is unknown.
func (f *Font) Codes(raw []byte) ([]Code, bool) {
	if f == nil || (f.kind != Composite && !f.twoByte) {
		out := make([]Code, len(raw))
		for i, c := range raw {
			out[i] = Code{Start: i, End: i + 1, Value: uint32(c)}
		}
		return out, true
	}
	if !f.metrics || !f.spaceOK {
		return nil, false
	}
	var out []Code
	for i := 0; i < len(raw); {
		n := matchSpace(f.space, raw[i:])
		if n == 0 {
			return nil, false
		}
		out = append(out, Code{Start: i, End: i + n, Value: bytesToCode(raw[i : i+n])})
		i += n
	}
	return out, true
}

// matchSpace returns the length of the shortest codespace range that b starts
// with, or 0.
func matchSpace(space []spaceRange, b []byte) int {
	for n := 1; n <= 4 && n <= len(b); n++ {
		for _, r := range space {
			if r.n != n {
				continue
			}
			ok := true
			for i := 0; i < n; i++ {
				if b[i] < r.lo[i] || b[i] > r.hi[i] {
					ok = false
					break
				}
			}
			if ok {
				return n
			}
		}
	}
	return 0
}

// Vertical reports whether the font writes vertically (a Type 0 font whose
// encoding has WMode 1, §9.7.4.3).
func (f *Font) Vertical() bool { return f != nil && f.vertical }

// Unbounded reports whether the font's glyphs cannot be bounded at all: a
// Type 3 font without a usable /FontMatrix or /FontBBox (§9.6.5), whose glyph
// procedures may paint anywhere; a simple font with an embedded font program
// and no /Widths, whose advances are the program's (§9.6.2.1 makes /Widths
// required except for the standard 14 fonts) and so are not known; or a
// Type 0 font whose /W array could not be read to its end.
func (f *Font) Unbounded() bool {
	if f == nil || !f.metrics {
		return false
	}
	switch f.kind {
	case Type3:
		return !f.scaleOK || !f.bboxOK
	case Simple:
		return f.embedded && !f.hasWidths
	case Composite:
		return f.wPartial
	}
	return false
}

// Advance returns bounds [lo, hi] on the horizontal displacement w0 of code c
// in text space at font size 1 (§9.4.4, before Tc, Tw and Th apply). It is
// exact (lo == hi) when the font gives the width: /Widths for a simple or
// Type 3 font (times /FontMatrix), /W or /DW for a Type 0 font with an
// Identity encoding. Otherwise it over-approximates: [min(0, MissingWidth),
// max(1.1 em, MissingWidth)] for a simple font; for a Type 0 font whose CIDs
// are unknown (any other encoding), the hull of 0, 1.1 em, /DW and every /W
// width; and the /FontBBox extent for a Type 3 font. Needs Options.Metrics.
func (f *Font) Advance(c Code) (lo, hi float64) {
	if f == nil || !f.metrics {
		return 0, emDefault
	}
	switch f.kind {
	case Composite:
		if f.identity && c.End-c.Start == 2 {
			w := f.dw
			if v, ok := f.cidW[c.Value]; ok {
				w = v
			} else {
				for _, r := range f.cidRngs {
					if c.Value >= r.first && c.Value <= r.last {
						w = r.w
						break
					}
				}
			}
			w *= f.scale[0]
			return w, w
		}
		return f.cidBounds()
	case Type3:
		if c.Value < 256 && f.hasWidth[c.Value] && f.scaleOK {
			w := f.widths[c.Value] * f.scale[0]
			return w, w
		}
		x0, _, x1, _ := f.Ink()
		return min(0, x0), max(0, x1)
	default:
		if c.Value < 256 && f.hasWidth[c.Value] {
			w := f.widths[c.Value] * f.scale[0]
			return w, w
		}
		mw := f.missingWidth * f.scale[0]
		return min(0, mw), max(emDefault, mw)
	}
}

// UnknownAdvance is Advance for a code whose value is unknown (the string's
// segmentation is uncertain).
func (f *Font) UnknownAdvance() (lo, hi float64) {
	if f != nil && f.metrics && f.kind == Type3 {
		x0, _, x1, _ := f.Ink()
		return min(0, x0), max(0, x1)
	}
	if f != nil && f.metrics && f.kind == Composite {
		return f.cidBounds()
	}
	return 0, emDefault
}

// cidBounds is the advance of a Type 0 code whose CID is unknown: the hull of
// 0, 1.1 em, /DW and every /W width.
func (f *Font) cidBounds() (lo, hi float64) {
	return min(0, f.cidLo*f.scale[0]), max(emDefault, f.cidHi*f.scale[0])
}

// GlyphToText maps a horizontal glyph-space width to text space at font size
// 1 (times the Type 3 /FontMatrix, or 1/1000 for other fonts).
func (f *Font) GlyphToText(w float64) float64 {
	if f == nil || !f.metrics {
		return w / 1000
	}
	return w * f.scale[0]
}

// Ink returns bounds on where any glyph of the font may paint, relative to its
// origin, in text space at font size 1: the union of /FontBBox (times
// /FontMatrix for Type 3) and, except for Type 3, the em defaults
// [0, 1.1] × [-0.25, 0.9]. A font without an embedded program is painted
// with a reader's substitute, so its box also covers substitutedBBox,
// whatever the /BaseFont. For a Type 3 font with no usable box it returns
// the em defaults; check Unbounded first.
func (f *Font) Ink() (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = 0, descenderEm, emDefault, ascenderEm
	if f == nil || !f.metrics {
		return x0, y0, x1, y1
	}
	if f.substituted && f.kind != Type3 {
		b := substitutedBBox
		x0, y0, x1, y1 = min(x0, b[0]/1000), min(y0, b[1]/1000), max(x1, b[2]/1000), max(y1, b[3]/1000)
	}
	if !f.bboxOK || !f.scaleOK {
		return x0, y0, x1, y1
	}
	bx0, by0, bx1, by1 := f.mapBox(f.bbox)
	if f.kind == Type3 {
		return bx0, by0, bx1, by1
	}
	return min(x0, bx0), min(y0, by0), max(x1, bx1), max(y1, by1)
}

// substitutedBBox bounds the glyphs a reader may draw for a font without an
// embedded program, whatever its /BaseFont (standard 14 names included), in
// glyph units. Readers substitute their own faces, not Adobe's: Ghostscript's
// URW clones reach past the Adobe AFM boxes (WinAnsi Helvetica Å/Ê/Ò/Š/Ž/Î up
// to 0.953 em against the AFM's 0.931; Times Å 0.915 against 0.898), and
// /Differences can name glyphs outside the Adobe standard set
// (Aringacute 1.17 em high, Scommaaccent -0.372 em deep, Bookman-LightItalic
// AE 1.269 em wide). Measured Ghostscript substitutes span about
// [-231 -393 1346 1170]; this box has margin around that.
var substitutedBBox = [4]float64{-300, -450, 1450, 1250}

// mapBox maps a glyph-space box through scale and returns its axis-aligned
// bounds.
func (f *Font) mapBox(b [4]float64) (x0, y0, x1, y1 float64) {
	m := f.scale
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, p := range [4][2]float64{{b[0], b[1]}, {b[2], b[1]}, {b[2], b[3]}, {b[0], b[3]}} {
		x := m[0]*p[0] + m[2]*p[1] + m[4]
		y := m[1]*p[0] + m[3]*p[1] + m[5]
		x0, y0 = min(x0, x), min(y0, y)
		x1, y1 = max(x1, x), max(y1, y)
	}
	return x0, y0, x1, y1
}

// loadMetrics reads the layout part of the model (Options.Metrics).
func (f *Font) loadMetrics(r *parse.Resolver, decode func(*parse.Stream) ([]byte, error)) {
	f.metrics = true
	f.scale = [6]float64{0.001, 0, 0, 0.001, 0, 0}
	f.scaleOK = true
	switch sub, _ := resolveName(r, f.dict["Subtype"]); sub {
	case "Type0":
		f.kind = Composite
	case "Type3":
		f.kind = Type3
	}

	switch f.kind {
	case Composite:
		f.loadEncodingCMap(r, decode)
		desc, ok := resolveArray(r, f.dict["DescendantFonts"])
		if !ok || len(desc) == 0 {
			f.dw = 1000
			return
		}
		f.dw = 1000
		f.cidLo, f.cidHi = f.dw, f.dw
		cid, ok, err := r.ResolveDict(desc[0])
		if err != nil || !ok {
			return
		}
		if v, ok := resolveNumber(r, cid["DW"]); ok {
			f.dw = v
			f.cidLo, f.cidHi = v, v
		}
		f.loadCIDWidths(r, cid["W"])
		f.loadVerticalMetrics(r, cid)
		cdesc := fontDescriptor(r, cid)
		f.loadBBox(r, cdesc["FontBBox"])
		f.substituted = !hasFontFile(cdesc)

	case Type3:
		f.scaleOK = false
		if m, ok := resolveArray(r, f.dict["FontMatrix"]); ok && len(m) == 6 {
			ok := true
			for i, e := range m {
				v, isNum := resolveNumber(r, e)
				if !isNum {
					ok = false
					break
				}
				f.scale[i] = v
			}
			f.scaleOK = ok
		}
		f.loadSimpleWidths(r)
		f.loadBBox(r, f.dict["FontBBox"])
		f.loadDifferences(r)

	default:
		f.loadSimpleWidths(r)
		fdesc := fontDescriptor(r, f.dict)
		f.embedded = hasFontFile(fdesc)
		f.substituted = !f.embedded
		if v, ok := resolveNumber(r, fdesc["MissingWidth"]); ok {
			f.missingWidth = v
		}
		f.loadBBox(r, fdesc["FontBBox"])
		f.loadDifferences(r)
	}
}

// hasFontFile reports whether a font descriptor holds a font program.
func hasFontFile(fdesc parse.Dict) bool {
	for _, k := range []parse.Name{"FontFile", "FontFile2", "FontFile3"} {
		if _, ok := fdesc[k]; ok {
			return true
		}
	}
	return false
}

// loadEncodingCMap reads a Type 0 font's /Encoding (§9.7.5): a predefined
// CMap name or an embedded CMap stream.
func (f *Font) loadEncodingCMap(r *parse.Resolver, decode func(*parse.Stream) ([]byte, error)) {
	enc, err := r.Resolve(f.dict["Encoding"])
	if err != nil {
		return
	}
	switch v := enc.(type) {
	case parse.Name:
		f.space, f.spaceOK, f.identity = predefinedSpace(v)
		f.vertical = strings.HasSuffix(string(v), "-V")
	case *parse.Stream:
		dec, err := decode(v)
		if err != nil {
			return
		}
		c := parseCMap(dec)
		if wm, ok := resolveNumber(r, v.Dict["WMode"]); ok && wm == 1 {
			c.wmode = 1
		}
		f.vertical = c.wmode == 1
		if c.bad {
			return
		}
		space := c.space
		if c.useCMap != "" {
			parent, ok, _ := predefinedSpace(c.useCMap)
			if !ok {
				return
			}
			space = append(append([]spaceRange(nil), space...), parent...)
			f.vertical = f.vertical || strings.HasSuffix(string(c.useCMap), "-V")
		}
		if len(space) > 0 {
			f.space, f.spaceOK = space, true
		}
	}
}

// predefinedSpace returns the codespace of a predefined CMap whose codespace
// is known without its data (§9.7.5.2, Table 118): Identity-H/V (two bytes,
// CID = code), the UCS2 CMaps (two bytes) and the UTF16 CMaps (two bytes, or
// four for a surrogate pair).
func predefinedSpace(name parse.Name) (space []spaceRange, ok, identity bool) {
	two := spaceRange{n: 2, lo: [4]byte{0x00, 0x00}, hi: [4]byte{0xFF, 0xFF}}
	switch n := string(name); {
	case n == "Identity-H" || n == "Identity-V":
		return []spaceRange{two}, true, true
	case strings.Contains(n, "-UCS2-"):
		return []spaceRange{two}, true, false
	case strings.Contains(n, "-UTF16-"):
		return []spaceRange{
			{n: 2, lo: [4]byte{0x00, 0x00}, hi: [4]byte{0xD7, 0xFF}},
			{n: 2, lo: [4]byte{0xE0, 0x00}, hi: [4]byte{0xFF, 0xFF}},
			{n: 4, lo: [4]byte{0xD8, 0x00, 0xDC, 0x00}, hi: [4]byte{0xDB, 0xFF, 0xDF, 0xFF}},
		}, true, false
	}
	return nil, false, false
}

// loadSimpleWidths reads /FirstChar and /Widths (§9.6.2.1). Entries that are
// not finite numbers stay unknown.
func (f *Font) loadSimpleWidths(r *parse.Resolver) {
	first, ok := resolveNumber(r, f.dict["FirstChar"])
	if !ok || first < 0 || first > 255 || first != math.Trunc(first) {
		return
	}
	widths, ok := resolveArray(r, f.dict["Widths"])
	if !ok {
		return
	}
	f.hasWidths = true
	for i, e := range widths {
		code := int(first) + i
		if code > 255 {
			break
		}
		if v, ok := resolveNumber(r, e); ok {
			f.widths[code] = v
			f.hasWidth[code] = true
		}
	}
}

// loadCIDWidths reads a CIDFont /W array (§9.7.4.3): "c [w1 w2 …]" and
// "cfirst clast w" entries, at most maxWItems items. An array it cannot read
// to the end (malformed, or past the bound) leaves the font's widths partial:
// a code not read could have any width, so the font is Unbounded.
func (f *Font) loadCIDWidths(r *parse.Resolver, obj parse.Object) {
	if obj == nil {
		return
	}
	w, ok := resolveArray(r, obj)
	if !ok {
		f.wPartial = true
		return
	}
	f.cidW = map[uint32]float64{}
	f.wPartial = true // until the loop reads the whole array
	items := 0
	for i := 0; i < len(w); {
		if i+1 >= len(w) || items >= maxWItems {
			return
		}
		c, ok := resolveNumber(r, w[i])
		if !ok || c < 0 || c > math.MaxUint32 {
			return
		}
		next, err := r.Resolve(w[i+1])
		if err != nil {
			return
		}
		if arr, isArr := next.(parse.Array); isArr {
			for j, e := range arr {
				if items++; items > maxWItems {
					return
				}
				v, ok := resolveNumber(r, e)
				if !ok || c+float64(j) > math.MaxUint32 {
					return
				}
				f.cidW[uint32(c)+uint32(j)] = v
				f.cidLo, f.cidHi = min(f.cidLo, v), max(f.cidHi, v)
			}
			i += 2
			continue
		}
		if i+2 >= len(w) {
			return
		}
		last, ok1 := resolveNumber(r, next)
		v, ok2 := resolveNumber(r, w[i+2])
		if !ok1 || !ok2 || last < c || last > math.MaxUint32 {
			return
		}
		f.cidRngs = append(f.cidRngs, cidRange{first: uint32(c), last: uint32(last), w: v})
		f.cidLo, f.cidHi = min(f.cidLo, v), max(f.cidHi, v)
		items++
		i += 3
	}
	f.wPartial = false
}

// loadVerticalMetrics reads a CIDFont's /DW2 and /W2 (§9.7.4.3) into the
// hulls vx, vy and wy, within the same item bound as /W. The default /DW2 is
// [880 -1000]; a CID without a /W2 entry has vx = w0/2, w0 any of /DW and
// the /W widths (the hull cidLo…cidHi). Call after loadCIDWidths.
func (f *Font) loadVerticalMetrics(r *parse.Resolver, cid parse.Dict) {
	f.vertOK = false
	vy, wy := 880.0, -1000.0
	if raw, has := cid["DW2"]; has {
		arr, ok := resolveArray(r, raw)
		if !ok || len(arr) != 2 {
			return
		}
		a, ok1 := resolveNumber(r, arr[0])
		b, ok2 := resolveNumber(r, arr[1])
		if !ok1 || !ok2 {
			return
		}
		vy, wy = a, b
	}
	f.vx = [2]float64{f.cidLo / 2, f.cidHi / 2}
	f.vy = [2]float64{vy, vy}
	f.wy = [2]float64{wy, wy}
	add := func(w1y, vx, vy float64) {
		f.wy = [2]float64{min(f.wy[0], w1y), max(f.wy[1], w1y)}
		f.vx = [2]float64{min(f.vx[0], vx), max(f.vx[1], vx)}
		f.vy = [2]float64{min(f.vy[0], vy), max(f.vy[1], vy)}
	}
	raw, has := cid["W2"]
	if !has {
		f.vertOK = true
		return
	}
	w2, ok := resolveArray(r, raw)
	if !ok {
		return
	}
	items := 0
	nums := func(objs []parse.Object) ([]float64, bool) {
		out := make([]float64, len(objs))
		for i, e := range objs {
			v, ok := resolveNumber(r, e)
			if !ok {
				return nil, false
			}
			out[i] = v
		}
		return out, true
	}
	for i := 0; i < len(w2); {
		if i+1 >= len(w2) || items >= maxWItems {
			return
		}
		next, err := r.Resolve(w2[i+1])
		if err != nil {
			return
		}
		if arr, isArr := next.(parse.Array); isArr {
			// c [w1y vx vy w1y vx vy …]
			if len(arr)%3 != 0 || items+len(arr) > maxWItems {
				return
			}
			v, ok := nums(arr)
			if !ok {
				return
			}
			for j := 0; j < len(v); j += 3 {
				add(v[j], v[j+1], v[j+2])
			}
			items += len(arr)
			i += 2
			continue
		}
		// cfirst clast w1y vx vy
		if i+4 >= len(w2) {
			return
		}
		v, ok := nums(w2[i+2 : i+5])
		if !ok {
			return
		}
		add(v[0], v[1], v[2])
		items++
		i += 5
	}
	f.vertOK = true
}

// VerticalMetrics returns, for a Type 0 font, the hulls over every code of
// the position vector components vx and vy and of the vertical displacement
// w1y (§9.7.4.3), in text space at font size 1, and ok false when /DW2 or
// /W2 cannot be read (or the font is not Type 0). Needs Options.Metrics.
func (f *Font) VerticalMetrics() (vx, vy, wy [2]float64, ok bool) {
	if f == nil || !f.metrics || f.kind != Composite || !f.vertOK {
		return vx, vy, wy, false
	}
	s := f.scale[0]
	return [2]float64{f.vx[0] * s, f.vx[1] * s}, [2]float64{f.vy[0] * s, f.vy[1] * s},
		[2]float64{f.wy[0] * s, f.wy[1] * s}, true
}

// loadBBox reads a /FontBBox. An all-zero box means "no assumptions" (§9.8.1)
// and stays unknown.
func (f *Font) loadBBox(r *parse.Resolver, obj parse.Object) {
	arr, ok := resolveArray(r, obj)
	if !ok || len(arr) != 4 {
		return
	}
	var b [4]float64
	for i, e := range arr {
		v, ok := resolveNumber(r, e)
		if !ok {
			return
		}
		b[i] = v
	}
	if b == [4]float64{} {
		return
	}
	f.bbox = [4]float64{min(b[0], b[2]), min(b[1], b[3]), max(b[0], b[2]), max(b[1], b[3])}
	f.bboxOK = true
}

// loadDifferences reads /Encoding /Differences (§9.6.6.1): runs of a code
// followed by glyph names for consecutive codes.
func (f *Font) loadDifferences(r *parse.Resolver) {
	enc, ok, err := r.ResolveDict(f.dict["Encoding"])
	if err != nil || !ok {
		return
	}
	diffs, ok := resolveArray(r, enc["Differences"])
	if !ok {
		return
	}
	code := -1
	for i, e := range diffs {
		if i >= maxDiffItems {
			break
		}
		obj, err := r.Resolve(e)
		if err != nil {
			return
		}
		switch v := obj.(type) {
		case parse.Integer:
			code = int(v)
		case parse.Name:
			if code >= 0 && code <= 255 {
				if f.diffs == nil {
					f.diffs = map[byte]parse.Name{}
				}
				f.diffs[byte(code)] = v
			}
			if code >= 0 {
				code++
			}
		}
	}
}

// fontDescriptor returns the resolved /FontDescriptor of a font dictionary,
// or nil.
func fontDescriptor(r *parse.Resolver, d parse.Dict) parse.Dict {
	fd, ok, err := r.ResolveDict(d["FontDescriptor"])
	if err != nil || !ok {
		return nil
	}
	return fd
}

// resolveName resolves obj to a Name.
func resolveName(r *parse.Resolver, obj parse.Object) (parse.Name, bool) {
	o, err := r.Resolve(obj)
	if err != nil {
		return "", false
	}
	n, ok := o.(parse.Name)
	return n, ok
}

// resolveArray resolves obj to an Array.
func resolveArray(r *parse.Resolver, obj parse.Object) (parse.Array, bool) {
	o, err := r.Resolve(obj)
	if err != nil {
		return nil, false
	}
	a, ok := o.(parse.Array)
	return a, ok
}

// resolveNumber resolves obj to a finite number.
func resolveNumber(r *parse.Resolver, obj parse.Object) (float64, bool) {
	o, err := r.Resolve(obj)
	if err != nil {
		return 0, false
	}
	var v float64
	switch n := o.(type) {
	case parse.Integer:
		v = float64(n)
	case parse.Real:
		v = float64(n)
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

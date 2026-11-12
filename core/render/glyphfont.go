package render

import (
	"bytes"
	"errors"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// glyphPPEM is the pixels-per-em the sfnt rasterizer loads outlines at. Using
// 1000 makes a returned coordinate C mean C/1000 em, so the glyph→text-space
// scale is simply fontSize/1000 regardless of the font's unitsPerEm.
var glyphPPEM = fixed.I(1000)

// glyphFont is a loaded font resource: the embedded program it draws glyphs
// from (glyphProgram: TrueType /FontFile2 or OpenType /FontFile3 through
// x/image/font/sfnt; Type 1 /FontFile and bare CFF /FontFile3 /Type1C or
// /CIDFontType0C through psfont; for a non-embedded simple font, the
// standard 14 included, a bundled substitute face, substitute.go) plus how
// its character codes map to glyphs and how far each advances.
// Non-embedded composite and Type 3 fonts are not drawn
// (docs/design/type1-std14-fonts.md §1.1).
//
// A simple font's advances come from its /Widths (ISO 32000-1 §9.4.4,
// §9.6.2.1), read through fontmodel, then for a non-embedded standard font
// the Adobe AFM widths; see advance.
//
// A glyphFont is not safe for concurrent use (sfnt.Buffer is scratch state); one
// is built per page render, and a single page is interpreted sequentially.
type glyphFont struct {
	prog    glyphProgram
	twoByte bool // composite (Type0/Identity) font: codes are 2-byte CIDs
	// model holds a simple font's /Widths, /MissingWidth and /Encoding, and
	// a composite font's /W and /DW when cidWidths; nil for other composite
	// fonts.
	model *fontmodel.Font
	// cidWidths marks a composite font over a Type 1 or CFF program (any
	// psProgram: a CIDFontType0 with /FontFile3 /CIDFontType0C, or the
	// nonstandard /FontFile Type 1), whose advances come from the CIDFont's
	// /W and /DW (§9.7.4.3) through model. Composite TrueType fonts still
	// advance by the program (hmtx).
	cidWidths bool
	// std14 marks a non-embedded font whose /BaseFont names or aliases one
	// of the standard 14 (std14.Normalize): a width its /Widths does not
	// give comes from afm, then /MissingWidth, else 0.5 em, never from the
	// substitute face (design §2.4).
	std14 bool
	afm   *afmWidths // std14 only: AFM widths by code, glyph units
}

// glyphProgram is the font program a glyphFont draws from.
type glyphProgram interface {
	// glyph maps a character code to a glyph; ok is false when the program
	// has none for it (nothing is drawn).
	glyph(code uint32) (gid uint32, ok bool)
	// advanceEm returns the program's own advance of gid in text space at
	// font size 1, for a font whose dictionary gives no width.
	advanceEm(gid uint32) float64
	// subpaths returns gid's outline as device-space polylines under the
	// text state x, or false when it cannot be built (nothing is drawn).
	subpaths(gid uint32, x glyphXform) ([][]point, bool)
}

// glyphXform is the text state that places a glyph: glyph space reaches
// device space through the program's own scale, then the font size,
// horizontal scale and rise (text space), the text matrix and the CTM.
type glyphXform struct {
	fontSize, hScale, rise float64
	tm, ctm                matrix
}

// errFontStream reports to fontmodel a font stream the budget did not decode
// (a decode error or a trip); fontmodel treats it as absent.
var errFontStream = errors.New("render: font stream not decoded")

// loadMetrics reads a font's layout metrics through fontmodel: a simple
// font's /Widths, /MissingWidth and /Encoding, a composite font's /W and /DW.
// /ToUnicode is not read; any other stream fontmodel decodes goes through b,
// and each CIDFont /W or /W2 item costs cidWidthItemCost raster work.
func loadMetrics(r *parse.Resolver, fd parse.Dict, b *budget) *fontmodel.Font {
	return fontmodel.Load(r, fd, fontmodel.Options{
		Metrics:       true,
		SkipToUnicode: true,
		Decode: func(st *parse.Stream) ([]byte, error) {
			if data, ok := b.decode(st, 0); ok {
				return data, nil
			}
			return nil, errFontStream
		},
		Charge: func(items int) bool { return b.chargePaint(int64(items) * cidWidthItemCost) },
	})
}

// cidMetricsKey identifies what a composite font's metrics are read from:
// the descendant CIDFont (/W, /DW, /W2, its descriptor) and the Type 0
// dictionary's /Encoding, the only other entry fontmodel's metrics read
// (/ToUnicode is skipped). Type 0 dictionaries with equal keys have equal
// metrics.
type cidMetricsKey struct {
	desc parse.Reference
	enc  parse.Object // a parse.Name or parse.Reference
}

// cidMetrics loads a composite font's metrics (loadMetrics), cached by
// cidMetricsKey, so Type 0 dictionaries sharing one CIDFont read and pay for
// its /W once per render; a CIDFont reached directly, or an /Encoding that
// is neither a name nor a reference, is not cached.
func (fl *fontLoader) cidMetrics(fd parse.Dict) *fontmodel.Font {
	key, cacheable := fl.cidKey(fd)
	if cacheable {
		if m, ok := fl.cidModels[key]; ok {
			return m
		}
	}
	m := loadMetrics(fl.r, fd, fl.b)
	if cacheable && !fl.b.stopped() {
		fl.cidModels[key] = m
	}
	return m
}

// cidKey returns fd's cidMetricsKey, and false when it has none.
func (fl *fontLoader) cidKey(fd parse.Dict) (cidMetricsKey, bool) {
	resolved, err := fl.r.Resolve(fd["DescendantFonts"])
	if err != nil {
		return cidMetricsKey{}, false
	}
	arr, ok := resolved.(parse.Array)
	if !ok || len(arr) == 0 {
		return cidMetricsKey{}, false
	}
	desc, ok := arr[0].(parse.Reference)
	if !ok {
		return cidMetricsKey{}, false
	}
	switch enc := fd["Encoding"].(type) {
	case parse.Name, parse.Reference:
		return cidMetricsKey{desc: desc, enc: enc}, true
	}
	return cidMetricsKey{}, false
}

// loadOneFont resolves a font dictionary to the program it is drawn from.
// For a Type0 composite font it follows /DescendantFonts to the CIDFont's
// descriptor and treats codes as 2-byte glyph IDs (the Identity-H common
// case). A simple font (not Type 3) that fontmodel classifies as substituted
// (Font.Substituted: its descriptor holds no program, or it has no
// descriptor) is drawn from a bundled substitute (loadSubstitute), for
// substituteLoadCost raster work on top of the load's fontLoadCost. It
// returns nil when the budget refuses that charge, or when the font cannot
// be drawn from: a Type 3 font, a composite font without an embedded
// program, or an embedded program that cannot be decoded or parsed.
//
// The /Subtype is resolved, as fontmodel resolves it for the redaction
// model's Kind, so a font the model reads as composite (2-byte codes, em
// ink bounds) is never drawn here one byte per glyph from a substitute.
func (fl *fontLoader) loadOneFont(fd parse.Dict) *glyphFont {
	r := fl.r
	subtype := resolvedName(r, fd["Subtype"])
	twoByte := subtype == "Type0"

	var model *fontmodel.Font
	if !twoByte {
		model = loadMetrics(r, fd, fl.b)
	}
	if model.Substituted() { // never for a Type 3 font
		if !fl.b.chargePaint(substituteLoadCost) {
			return nil
		}
		fdesc, _, _ := r.ResolveDict(fd["FontDescriptor"])
		gf := &glyphFont{model: model}
		loadSubstitute(r, fd, fdesc, gf)
		return gf
	}

	descriptorHolder := fd
	if twoByte {
		desc, ok := descendantFont(r, fd)
		if !ok {
			return nil
		}
		descriptorHolder = desc
	}

	fdesc, ok, err := r.ResolveDict(descriptorHolder["FontDescriptor"])
	if err != nil || !ok {
		return nil
	}
	gf := &glyphFont{twoByte: twoByte, model: model}
	if gf.prog = fl.program(fdesc, gf); gf.prog == nil {
		return nil
	}
	if _, ps := gf.prog.(*psProgram); ps && twoByte {
		gf.model, gf.cidWidths = fl.cidMetrics(fd), true
	}
	return gf
}

// descendantFont returns the CIDFont dictionary from a Type0 font's
// /DescendantFonts (an array with a single element per spec).
func descendantFont(r *parse.Resolver, fd parse.Dict) (parse.Dict, bool) {
	resolved, err := r.Resolve(fd["DescendantFonts"])
	if err != nil {
		return nil, false
	}
	arr, ok := resolved.(parse.Array)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	d, ok, err := r.ResolveDict(arr[0])
	if err != nil || !ok {
		return nil, false
	}
	return d, true
}

// programKind is what a decoded font program holds.
type programKind int

const (
	programUnknown programKind = iota
	programSFNT                // TrueType or OpenType (an sfnt wrapper)
	programType1               // a Type 1 program (cleartext + eexec)
	programCFF                 // a bare CFF program (Type1C or CIDFontType0C)
)

// sniffProgram classifies a decoded program by its first bytes
// (docs/design/type1-std14-fonts.md §4.2): mislabeled /FontFile3 subtypes
// and Type 1 data under /FontFile3 occur, so the content decides, not the key.
func sniffProgram(data []byte) programKind {
	switch {
	case len(data) >= 4 && (bytes.Equal(data[:4], []byte{0, 1, 0, 0}) ||
		string(data[:4]) == "true" || string(data[:4]) == "OTTO" || string(data[:4]) == "ttcf"):
		return programSFNT
	case bytes.HasPrefix(data, []byte("%!PS-AdobeFont")) || bytes.HasPrefix(data, []byte("%!FontType1")) ||
		len(data) >= 2 && data[0] == 0x80 && data[1] == 0x01:
		return programType1
	case len(data) >= 4 && data[0] == 1 && data[2] >= 4:
		return programCFF
	}
	return programUnknown
}

// program finds, decodes and parses the font program of a FontDescriptor:
// /FontFile2, /FontFile3 and /FontFile, in that order, the first that
// decodes. The decoded bytes are charged to the render budget's decoded
// bytes (budget.decode), and a program reached through a reference is
// parsed once per render however many font dictionaries share it. Returns
// nil when no program can be drawn from.
func (fl *fontLoader) program(fdesc parse.Dict, gf *glyphFont) glyphProgram {
	for _, key := range []parse.Name{"FontFile2", "FontFile3", "FontFile"} {
		obj := fdesc[key]
		if obj == nil {
			continue
		}
		ref, hasRef := obj.(parse.Reference)
		parsed, cached := fl.progs[ref]
		if !hasRef || !cached {
			parsed = fl.decodeProgram(obj, key)
			if hasRef {
				fl.progs[ref] = parsed
			}
		}
		if _, undecoded := parsed.(undecodedProgram); undecoded {
			continue
		}
		switch p := parsed.(type) {
		case *sfnt.Font:
			return &sfntProgram{sf: p, twoByte: gf.twoByte}
		case *psEntry:
			return newPSProgram(fl, p, gf)
		}
		return nil // a program that decodes but cannot be drawn from
	}
	return nil
}

// undecodedProgram marks a program stream that did not resolve or decode:
// the next key is tried.
type undecodedProgram struct{}

// decodeProgram decodes and parses the program stream obj: an *sfnt.Font, a
// *psEntry, nil (decoded but not drawable), or undecodedProgram.
func (fl *fontLoader) decodeProgram(obj parse.Object, key parse.Name) any {
	st, ok := resolveStream(fl.r, obj)
	if !ok {
		return undecodedProgram{}
	}
	data, ok := fl.b.decode(st, 0)
	if !ok || len(data) == 0 {
		return undecodedProgram{}
	}
	return fl.parseProgram(data, key)
}

// parseProgram parses a decoded program: an *sfnt.Font, a *psEntry, or nil.
// A program the sniff does not recognize is parsed as its key says
// (/FontFile: Type 1; otherwise sfnt, as before the sniff existed): a bare
// CFF always sniffs as one, since ParseCFF needs the same header bytes.
// Parsing a Type 1 or CFF program polls the render's context first.
func (fl *fontLoader) parseProgram(data []byte, key parse.Name) any {
	kind := sniffProgram(data)
	if kind == programUnknown && key == "FontFile" {
		kind = programType1
	}
	switch kind {
	case programType1:
		if !fl.b.poll() {
			return nil
		}
		return parsePSEntry(data, fl.b)
	case programCFF:
		if !fl.b.poll() {
			return nil
		}
		return parseCFFEntry(data, fl.b)
	}
	sf, err := sfnt.Parse(data)
	if err != nil {
		return nil
	}
	return sf
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

// gid maps a character code to a glyph through the font's program; ok is
// false for a missing glyph.
func (gf *glyphFont) gid(code uint32) (uint32, bool) { return gf.prog.glyph(code) }

// advance returns the horizontal displacement w0 of code in text space at font
// size 1 (§9.4.4, design §2.1 step 3): for a composite font over a Type 1 or
// CFF program (cidWidths) with an Identity encoding, the CID's /W entry, else
// /DW (default 1000, §9.7.4.3); for a simple font, the code's /Widths entry;
// else, for a non-embedded standard font (std14), the AFM width of the
// code's glyph name; else, when the font has a /Widths array or is std14,
// the descriptor's /MissingWidth; else a std14 font advances 0.5 em, and
// any other font by its program's advance of the code's glyph, or 0.5 em
// when the code has no glyph. ok and gid are gf.gid(code)'s results.
func (gf *glyphFont) advance(code, gid uint32, ok bool) float64 {
	if gf.cidWidths {
		// fontmodel's advance is exact (lo == hi) for an Identity encoding;
		// for another CMap it is only bounds, and the program decides.
		if lo, hi := gf.model.Advance(fontmodel.Code{End: 2, Value: code}); lo == hi {
			return lo
		}
	}
	if w, has := gf.model.Width(code); has {
		return w
	}
	if gf.afm != nil && code < 256 && gf.afm[code] != noAFMWidth {
		return float64(gf.afm[code]) / 1000
	}
	if gf.model.HasWidths() || gf.std14 {
		if w, has := gf.model.MissingWidth(); has {
			return w
		}
		// Deliberately lenient: Table 122 makes an absent /MissingWidth 0,
		// but the program's advance (or, for a standard font, 0.5 em) is a
		// better guess for a code /Widths does not cover.
	}
	if !ok || gf.std14 {
		return 0.5
	}
	return gf.prog.advanceEm(gid)
}

// forEachCode iterates the character codes in a shown string, reporting whether
// each is a single-byte space (for word spacing). Composite fonts read 2 bytes
// per code; simple fonts read one. The iteration stops early when fn returns
// false (the render budget is spent).
func (gf *glyphFont) forEachCode(raw []byte, fn func(code uint32, isSpace bool) bool) {
	if gf.twoByte {
		for i := 0; i+1 < len(raw); i += 2 {
			if !fn(uint32(raw[i])<<8|uint32(raw[i+1]), false) {
				return
			}
		}
		return
	}
	for _, b := range raw {
		if !fn(uint32(b), b == 32) {
			return
		}
	}
}

// sfntProgram draws from a TrueType or OpenType program through
// x/image/font/sfnt. A simple font's codes are looked up as Latin-1 runes in
// the cmap sfnt picked (the §9.6.6.4 rules are Phase 4); a composite font's
// codes are glyph IDs.
type sfntProgram struct {
	sf      *sfnt.Font // shared by every glyphFont over the same program; safe with distinct Buffers
	buf     sfnt.Buffer
	twoByte bool
}

func (p *sfntProgram) glyph(code uint32) (uint32, bool) {
	if p.twoByte {
		return code, true
	}
	g, err := p.sf.GlyphIndex(&p.buf, rune(code))
	if err != nil || g == 0 {
		return 0, false
	}
	return uint32(g), true
}

// advanceEm returns a glyph's horizontal advance in em units (0..~1).
func (p *sfntProgram) advanceEm(gid uint32) float64 {
	adv, err := p.sf.GlyphAdvance(&p.buf, sfnt.GlyphIndex(gid), glyphPPEM, font.HintingNone)
	if err != nil {
		return 0.5
	}
	return float64(adv) / 64.0 / 1000.0
}

// subpaths loads the glyph at glyphPPEM (so its coordinates are in
// thousandths of an em), scales it to text space by fontSize/1000, then maps
// it through the text matrix and CTM.
func (p *sfntProgram) subpaths(gid uint32, x glyphXform) ([][]point, bool) {
	if gid > 0xFFFF {
		return nil, false
	}
	segs, err := p.sf.LoadGlyph(&p.buf, sfnt.GlyphIndex(gid), glyphPPEM, nil)
	if err != nil {
		return nil, false
	}
	sg := x.fontSize / 1000.0
	// glyph -> text space (fontSize scale + horizontal scale + rise), then text
	// matrix, then CTM. sfnt outlines are y-down and text space is y-up
	// (ISO 32000-1 §9.2.4), so the y scale is negated.
	glyphToText := matrix{sg * x.hScale, 0, 0, -sg, 0, x.rise}
	full := glyphToText.mul(x.tm).mul(x.ctm)
	return segmentsToSubpaths(segs, full), true
}

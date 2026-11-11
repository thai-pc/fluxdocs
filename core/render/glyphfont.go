package render

import (
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

// glyphFont wraps an embedded font program (TrueType /FontFile2 or OpenType-CFF
// /FontFile3) parsed by x/image/font/sfnt, plus how its character codes map to
// glyphs. Type1 (/FontFile) and non-embedded standard-14 fonts are not loaded
// in Wave 1 — their text simply does not render.
//
// A simple font's advances come from its /Widths (ISO 32000-1 §9.4.4,
// §9.6.2.1), read through fontmodel; see advance.
//
// A glyphFont is not safe for concurrent use (sfnt.Buffer is scratch state); one
// is built per page render, and a single page is interpreted sequentially.
type glyphFont struct {
	sf      *sfnt.Font
	buf     sfnt.Buffer
	twoByte bool            // composite (Type0/Identity) font: codes are 2-byte glyph IDs
	model   *fontmodel.Font // simple fonts: /Widths and /MissingWidth; nil for composite fonts
}

// loadFonts builds a glyphFont for each entry of a page's /Font resources.
// Fonts that cannot be loaded (missing/embedded-only-unsupported program, parse
// failure) are omitted; their text is skipped rather than failing the render.
// Font programs are decoded through b (budget.decode); once it stops, no more
// fonts are loaded.
func loadFonts(r *parse.Resolver, resources parse.Dict, b *budget) map[parse.Name]*glyphFont {
	out := map[parse.Name]*glyphFont{}
	if resources == nil {
		return out
	}
	fontDict, ok, err := r.ResolveDict(resources["Font"])
	if err != nil || !ok {
		return out
	}
	for name, ref := range fontDict {
		fd, ok, err := r.ResolveDict(ref)
		if err != nil || !ok {
			continue
		}
		if b.stopped() {
			break
		}
		if gf := loadOneFont(r, fd, b); gf != nil {
			out[name] = gf
		}
	}
	return out
}

// loadOneFont resolves a font dictionary to an embedded program. For a Type0
// composite font it follows /DescendantFonts to the CIDFont's descriptor and
// treats codes as 2-byte glyph IDs (the Identity-H common case).
func loadOneFont(r *parse.Resolver, fd parse.Dict, b *budget) *glyphFont {
	subtype, _ := fd.GetName("Subtype")
	twoByte := subtype == "Type0"

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
	data, ok := fontProgram(r, fdesc, b)
	if !ok {
		return nil
	}
	sf, err := sfnt.Parse(data)
	if err != nil {
		return nil
	}
	gf := &glyphFont{sf: sf, twoByte: twoByte}
	if !twoByte {
		gf.model = loadMetrics(r, fd, b)
	}
	return gf
}

// errFontStream reports to fontmodel a font stream the budget did not decode
// (a decode error or a trip); fontmodel treats it as absent.
var errFontStream = errors.New("render: font stream not decoded")

// loadMetrics reads a simple font's layout metrics (/Widths, /MissingWidth)
// through fontmodel. /ToUnicode is not read; any other stream fontmodel
// decodes goes through b.
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
	})
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

// fontProgram returns the decoded bytes of an embedded sfnt font program from a
// FontDescriptor: /FontFile2 (TrueType) or /FontFile3 (OpenType/CFF). /FontFile
// (Type1) is not supported by sfnt and is skipped. The program is decoded
// through b, which charges every byte as decoded bytes.
func fontProgram(r *parse.Resolver, fdesc parse.Dict, b *budget) ([]byte, bool) {
	for _, key := range []parse.Name{"FontFile2", "FontFile3"} {
		st, ok := resolveStream(r, fdesc[key])
		if !ok {
			continue
		}
		if data, ok := b.decode(st, 0); ok && len(data) > 0 {
			return data, true
		}
	}
	return nil, false
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

// gid maps a character code to a glyph index. For a composite font the code is
// already the glyph ID; for a simple font the code is treated as a Latin-1 rune
// and looked up through the font's cmap. ok is false for a missing glyph.
func (gf *glyphFont) gid(code uint32) (sfnt.GlyphIndex, bool) {
	if gf.twoByte {
		return sfnt.GlyphIndex(code), true
	}
	g, err := gf.sf.GlyphIndex(&gf.buf, rune(code))
	if err != nil || g == 0 {
		return 0, false
	}
	return g, true
}

// advance returns the horizontal displacement w0 of code in text space at font
// size 1 (§9.4.4): for a simple font with a /Widths array, the code's entry,
// else (a code outside /FirstChar–/LastChar or a non-numeric entry) the
// descriptor's /MissingWidth; otherwise (a composite font, a simple font with
// no /Widths at all, or neither entry) the program's advance of the code's
// glyph, or 0.5 em when the code has no glyph. ok and gid are gf.gid(code)'s
// results.
func (gf *glyphFont) advance(code uint32, gid sfnt.GlyphIndex, ok bool) float64 {
	if gf.model.HasWidths() {
		if w, has := gf.model.Width(code); has {
			return w
		}
		if w, has := gf.model.MissingWidth(); has {
			return w
		}
		// Deliberately lenient: Table 122 makes an absent /MissingWidth 0,
		// but the program's advance is a better guess for a code /Widths
		// does not cover.
	}
	if !ok {
		return 0.5
	}
	return gf.advanceEm(gid)
}

// advanceEm returns a glyph's horizontal advance in em units (0..~1).
func (gf *glyphFont) advanceEm(gid sfnt.GlyphIndex) float64 {
	adv, err := gf.sf.GlyphAdvance(&gf.buf, gid, glyphPPEM, font.HintingNone)
	if err != nil {
		return 0.5
	}
	return float64(adv) / 64.0 / 1000.0
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

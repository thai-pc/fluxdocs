package render

import (
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
// A glyphFont is not safe for concurrent use (sfnt.Buffer is scratch state); one
// is built per page render, and a single page is interpreted sequentially.
type glyphFont struct {
	sf      *sfnt.Font
	buf     sfnt.Buffer
	twoByte bool // composite (Type0/Identity) font: codes are 2-byte glyph IDs
}

// loadFonts builds a glyphFont for each entry of a page's /Font resources.
// Fonts that cannot be loaded (missing/embedded-only-unsupported program, parse
// failure) are omitted; their text is skipped rather than failing the render.
func loadFonts(r *parse.Resolver, resources parse.Dict) map[parse.Name]*glyphFont {
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
		if gf := loadOneFont(r, fd); gf != nil {
			out[name] = gf
		}
	}
	return out
}

// loadOneFont resolves a font dictionary to an embedded program. For a Type0
// composite font it follows /DescendantFonts to the CIDFont's descriptor and
// treats codes as 2-byte glyph IDs (the Identity-H common case).
func loadOneFont(r *parse.Resolver, fd parse.Dict) *glyphFont {
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
	data, ok := fontProgram(r, fdesc)
	if !ok {
		return nil
	}
	sf, err := sfnt.Parse(data)
	if err != nil {
		return nil
	}
	return &glyphFont{sf: sf, twoByte: twoByte}
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
// (Type1) is not supported by sfnt and is skipped.
func fontProgram(r *parse.Resolver, fdesc parse.Dict) ([]byte, bool) {
	for _, key := range []parse.Name{"FontFile2", "FontFile3"} {
		st, ok := resolveStream(r, fdesc[key])
		if !ok {
			continue
		}
		if data, err := parse.DecodeStream(st); err == nil && len(data) > 0 {
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
// per code; simple fonts read one.
func (gf *glyphFont) forEachCode(raw []byte, fn func(code uint32, isSpace bool)) {
	if gf.twoByte {
		for i := 0; i+1 < len(raw); i += 2 {
			fn(uint32(raw[i])<<8|uint32(raw[i+1]), false)
		}
		return
	}
	for _, b := range raw {
		fn(uint32(b), b == 32)
	}
}

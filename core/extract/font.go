package extract

import (
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// font holds the minimum a text extractor needs to turn a shown string into
// Unicode: whether codes are one or two bytes, and a /ToUnicode CMap when the
// document provides one.
type font struct {
	toUni   *cmap
	twoByte bool // composite (Type0) font: character codes are multi-byte
}

// buildFonts reads the /Font subdictionary of a page's /Resources and returns a
// font per resource name. Fonts that cannot be resolved are skipped; a page
// with no /Font resource yields an empty (non-nil) map.
func buildFonts(r *parse.Resolver, resources parse.Dict) map[parse.Name]*font {
	fonts := map[parse.Name]*font{}
	if resources == nil {
		return fonts
	}
	fontDict, ok, err := r.ResolveDict(resources["Font"])
	if err != nil || !ok {
		return fonts
	}

	for name, ref := range fontDict {
		fd, ok, err := r.ResolveDict(ref)
		if err != nil || !ok {
			continue
		}
		f := &font{}
		if sub, ok := fd.GetName("Subtype"); ok && sub == "Type0" {
			f.twoByte = true
		}
		if st, ok := resolveStream(r, fd["ToUnicode"]); ok {
			if dec, err := parse.DecodeStream(st); err == nil {
				f.toUni = parseCMap(dec)
			}
		}
		fonts[name] = f
	}
	return fonts
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

// decode turns a shown string's bytes into Unicode text for the given font.
//
// With a /ToUnicode CMap, codes are read at the CMap's width and mapped. Without
// one, a simple (single-byte) font is decoded as Latin-1, which is exact for the
// ASCII range covering most documents; a composite font without /ToUnicode
// cannot be decoded reliably and yields no text (Wave 1 limitation).
func decode(f *font, raw []byte) string {
	if f != nil && f.toUni != nil {
		return decodeWithCMap(f.toUni, raw)
	}
	if f != nil && f.twoByte {
		return "" // composite font, no /ToUnicode: not decodable in Wave 1
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

package extract

import (
	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// font is the shared font model (core/internal/fontmodel), which redaction
// also reads, so RedactText matches the text extraction returns.
type font = fontmodel.Font

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
		if f := fontmodel.Load(r, ref, fontmodel.Options{}); f != nil {
			fonts[name] = f
		}
	}
	return fonts
}

// decode turns a shown string's bytes into Unicode text for the given font (a
// nil font decodes as Latin-1); see fontmodel.Font.Text.
func decode(f *font, raw []byte) string { return f.Text(raw) }

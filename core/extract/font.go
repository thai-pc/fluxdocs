package extract

import (
	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/internal/textflow"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// font is the shared font model (core/internal/fontmodel), which redaction
// also reads, so RedactText matches the text extraction returns.
type font = fontmodel.Font

// buildFonts reads the /Font subdictionary of a page's /Resources (bound as
// textflow.FontDict binds it, the rule the text reading shares with
// redaction) and returns a font per resource name. Fonts that cannot be
// resolved are skipped; a page with no /Font resource yields an empty
// (non-nil) map.
func buildFonts(r *parse.Resolver, resources parse.Dict) map[parse.Name]*font {
	fonts := map[parse.Name]*font{}
	for name, ref := range textflow.FontDict(r, resources) {
		if f := fontmodel.Load(r, ref, fontmodel.Options{}); f != nil {
			fonts[name] = f
		}
	}
	return fonts
}

// loadFont loads a font as extraction reads it: no metrics, streams decoded
// by parse.DecodeStream.
func loadFont(r *parse.Resolver) func(obj parse.Object) *font {
	return func(obj parse.Object) *font { return fontmodel.Load(r, obj, fontmodel.Options{}) }
}

// decode turns a shown string's bytes into Unicode text for the given font (a
// nil font decodes as Latin-1); see fontmodel.Font.Text.
func decode(f *font, raw []byte) string { return f.Text(raw) }

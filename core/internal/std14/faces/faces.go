//go:build !nostd14

// Package faces embeds the substitute faces drawn for non-embedded standard
// 14 and other non-embedded simple fonts (docs/design/type1-std14-fonts.md
// §3, §11 D1–D3): renamed subsets of Liberation Sans, Serif and Mono 2.1.5
// (SIL OFL 1.1, LICENSES/OFL-1.1-Liberation.txt) for the 12 Latin faces, and
// of DejaVu Sans 2.37 (Bitstream Vera and Arev licenses plus public-domain
// DejaVu changes, LICENSES/Bitstream-Vera-DejaVu.txt) for Symbol and
// ZapfDingbats. The files are built by testing/tools/std14fonts; each
// carries its copyright and license in its name table.
//
// Building with -tags nostd14 leaves the faces out (Data returns nil):
// non-embedded standard text then draws nothing, as before Phase 3, but
// advances by the AFM widths, which are new with Phase 3.
package faces

import (
	_ "embed"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

var (
	//go:embed FluxStdMono-Regular.ttf
	monoRegular []byte
	//go:embed FluxStdMono-Bold.ttf
	monoBold []byte
	//go:embed FluxStdMono-Italic.ttf
	monoItalic []byte
	//go:embed FluxStdMono-BoldItalic.ttf
	monoBoldItalic []byte
	//go:embed FluxStdSans-Regular.ttf
	sansRegular []byte
	//go:embed FluxStdSans-Bold.ttf
	sansBold []byte
	//go:embed FluxStdSans-Italic.ttf
	sansItalic []byte
	//go:embed FluxStdSans-BoldItalic.ttf
	sansBoldItalic []byte
	//go:embed FluxStdSerif-Regular.ttf
	serifRegular []byte
	//go:embed FluxStdSerif-Bold.ttf
	serifBold []byte
	//go:embed FluxStdSerif-Italic.ttf
	serifItalic []byte
	//go:embed FluxStdSerif-BoldItalic.ttf
	serifBoldItalic []byte
	//go:embed FluxStdSymbol-Regular.ttf
	symbol []byte
)

// Enabled reports whether the faces are compiled in (false with -tags
// nostd14).
const Enabled = true

// Data returns the TrueType data of the substitute for face: a Latin face's
// FluxStd Mono/Sans/Serif style, or FluxStd Symbol for both Symbol and
// ZapfDingbats. Returns nil for a face out of range. The slice is shared
// and must not be modified.
func Data(face std14.Face) []byte {
	switch face {
	case std14.Courier:
		return monoRegular
	case std14.CourierBold:
		return monoBold
	case std14.CourierOblique:
		return monoItalic
	case std14.CourierBoldOblique:
		return monoBoldItalic
	case std14.Helvetica:
		return sansRegular
	case std14.HelveticaBold:
		return sansBold
	case std14.HelveticaOblique:
		return sansItalic
	case std14.HelveticaBoldOblique:
		return sansBoldItalic
	case std14.TimesRoman:
		return serifRegular
	case std14.TimesBold:
		return serifBold
	case std14.TimesItalic:
		return serifItalic
	case std14.TimesBoldItalic:
		return serifBoldItalic
	case std14.Symbol, std14.ZapfDingbats:
		return symbol
	}
	return nil
}

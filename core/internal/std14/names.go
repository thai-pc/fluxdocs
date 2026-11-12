package std14

import (
	"sort"
	"strings"
)

// Face is one of the 14 standard Type 1 fonts of ISO 32000-1 §9.6.2.2. The
// 12 Latin faces double as the substitutes for other non-embedded simple
// fonts (FaceFor).
type Face uint8

// The standard 14 fonts, in the order of the generated tables.
const (
	Courier Face = iota
	CourierBold
	CourierOblique
	CourierBoldOblique
	Helvetica
	HelveticaBold
	HelveticaOblique
	HelveticaBoldOblique
	TimesRoman
	TimesBold
	TimesItalic
	TimesBoldItalic
	Symbol
	ZapfDingbats

	// NumFaces is the number of faces.
	NumFaces
)

// faceNames are the §9.6.2.2 names, by Face.
var faceNames = [NumFaces]string{
	"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
	"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
	"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
	"Symbol", "ZapfDingbats",
}

// String returns the face's standard PostScript name ("Times-Roman"), or ""
// for a value out of range.
func (f Face) String() string {
	if f >= NumFaces {
		return ""
	}
	return faceNames[f]
}

// Family is a Latin substitute family.
type Family uint8

// The three Latin families.
const (
	Mono Family = iota
	Sans
	Serif
)

// FaceFor returns the Latin face of family with the given style: Courier,
// Helvetica or Times, bold and/or italic (oblique). An unknown family is
// treated as Sans.
func FaceFor(family Family, bold, italic bool) Face {
	base := Helvetica
	switch family {
	case Mono:
		base = Courier
	case Serif:
		base = TimesRoman
	}
	if bold {
		base++
	}
	if italic {
		base += 2
	}
	return base
}

// maxBaseFontLen bounds the /BaseFont names Normalize examines; a longer name
// (none of the standard ones or their aliases come close) is not matched.
const maxBaseFontLen = 128

// familyAliases maps a normalized family part of a /BaseFont (subset tag,
// style suffix, spaces and an "MT"/"PSMT"/"PS" vendor suffix removed) to its
// standard family. The standard names come from §9.6.2.2; the aliases (Arial,
// Times New Roman, Courier New, whose metrics match) are an implementation
// choice ISO does not define.
var familyAliases = map[string]Face{
	"Courier": Courier, "CourierNew": Courier,
	"Helvetica": Helvetica, "Arial": Helvetica,
	"Times": TimesRoman, "TimesNewRoman": TimesRoman,
	"Symbol": Symbol, "ZapfDingbats": ZapfDingbats,
}

// Normalize maps a /BaseFont name to the standard face it names or aliases,
// and false when it is none of them. It strips a subset tag ("ABCDEF+"),
// splits a ",Bold" / ",Italic" / ",BoldItalic" or a PostScript "-Bold",
// "-Italic", "-Oblique", "-BoldOblique", "-BoldItalic", "-Roman" style
// suffix, ignores spaces and a trailing "MT"/"PSMT"/"PS" (the Monotype
// PostScript names: "ArialMT", "TimesNewRomanPS-BoldMT"), then matches the
// family: Courier, Helvetica, Times (as "Times-Roman" etc.), Symbol and
// ZapfDingbats, plus the aliases Arial, TimesNewRoman and CourierNew. Symbol
// and ZapfDingbats have a single face, so their style suffix is ignored. Any
// other style suffix ("Helvetica-Narrow") does not match.
func Normalize(baseFont string) (Face, bool) {
	if len(baseFont) > maxBaseFontLen {
		return 0, false
	}
	name := baseFont
	if len(name) > 7 && name[6] == '+' && isUpperTag(name[:6]) {
		name = name[7:]
	}
	name = strings.ReplaceAll(name, " ", "")
	for f := Face(0); f < NumFaces; f++ {
		if name == faceNames[f] {
			return f, true
		}
	}
	family, style := name, ""
	if i := strings.IndexAny(name, ",-"); i >= 0 {
		family, style = name[:i], name[i+1:]
	}
	for _, suf := range []string{"PSMT", "MT", "PS"} {
		if strings.HasSuffix(family, suf) && len(family) > len(suf) {
			family = family[:len(family)-len(suf)]
			break
		}
	}
	style = strings.TrimSuffix(style, "MT")
	base, ok := familyAliases[family]
	if !ok {
		return 0, false
	}
	if base == Symbol || base == ZapfDingbats {
		return base, true
	}
	var bold, italic bool
	switch style {
	case "", "Roman", "Regular":
	case "Bold":
		bold = true
	case "Italic", "Oblique":
		italic = true
	case "BoldItalic", "BoldOblique":
		bold, italic = true, true
	default:
		return 0, false
	}
	fam := Sans
	switch base {
	case Courier:
		fam = Mono
	case TimesRoman:
		fam = Serif
	}
	return FaceFor(fam, bold, italic), true
}

// isUpperTag reports whether s is six uppercase ASCII letters (a subset tag,
// §9.6.4).
func isUpperTag(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// noWidth marks a glyph a Latin face's AFM does not have.
const noWidth = 0xFFFF

// Width returns the AFM width of glyphName in face, in glyph units (1/1000
// of text space), and false when the face's AFM has no such glyph or face is
// out of range.
func Width(face Face, glyphName string) (float64, bool) {
	switch {
	case face < Symbol:
		i := sort.SearchStrings(afmLatinNames[:], glyphName)
		if i < len(afmLatinNames) && afmLatinNames[i] == glyphName {
			if w := afmLatinWidths[face][i]; w != noWidth {
				return float64(w), true
			}
		}
	case face == Symbol:
		i := sort.SearchStrings(afmSymbolNames[:], glyphName)
		if i < len(afmSymbolNames) && afmSymbolNames[i] == glyphName {
			return float64(afmSymbolWidths[i]), true
		}
	case face == ZapfDingbats:
		i := sort.SearchStrings(afmDingbatsNames[:], glyphName)
		if i < len(afmDingbatsNames) && afmDingbatsNames[i] == glyphName {
			return float64(afmDingbatsWidths[i]), true
		}
	}
	return 0, false
}

// BBox returns the face's AFM FontBBox (llx, lly, urx, ury) in glyph units,
// all zero for a face out of range.
func BBox(face Face) [4]float64 {
	if face >= NumFaces {
		return [4]float64{}
	}
	b := afmBBox[face]
	return [4]float64{float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3])}
}

// BuiltinEncoding returns the built-in encoding of a non-embedded standard
// font (§9.6.6.1, Annex D): StandardEncoding for the 12 Latin faces, the
// fonts' own encodings for Symbol and ZapfDingbats (Annex D.5, D.6, here
// taken from the C values of their AFM files). The result is a copy.
func BuiltinEncoding(face Face) Encoding {
	switch face {
	case Symbol:
		return symbolBuiltin
	case ZapfDingbats:
		return dingbatsBuiltin
	}
	return *standard
}

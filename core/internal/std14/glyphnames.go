package std14

import (
	"sort"
	"strconv"
	"strings"
)

// Rune returns the Unicode character a glyph name stands for, for drawing it
// from a Unicode-keyed substitute face (docs/design/type1-std14-fonts.md
// §2.1 step 2): the Adobe Glyph List entry (agl_gen.go: every name the
// standard 14 fonts use, the ZapfDingbats names "a1"… included, and every
// other AGL name whose character a bundled face has), else a "uniXXXX" name
// (exactly one code unit of four hex digits) or "uXXXX"–"uXXXXXX". A suffix
// from the first period on ("a.sc") is ignored. A ligature name
// ("f_f_i") or a "uni" name of several characters has no single character
// and returns false, as does any other name.
func Rune(name string) (rune, bool) {
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	if name == "" || len(name) > maxGlyphNameLen || strings.IndexByte(name, '_') >= 0 {
		return 0, false
	}
	if i := sort.SearchStrings(aglNames[:], name); i < len(aglNames) && aglNames[i] == name {
		return rune(aglRunes[i]), true
	}
	if len(name) == 7 && strings.HasPrefix(name, "uni") {
		return hexRune(name[3:])
	}
	if len(name) >= 5 && len(name) <= 7 && name[0] == 'u' {
		return hexRune(name[1:])
	}
	return 0, false
}

// maxGlyphNameLen bounds the glyph names Rune examines; PDF names are not
// bounded, and no name it can resolve is longer.
const maxGlyphNameLen = 64

// hexRune parses s as a hexadecimal Unicode scalar value (not a surrogate,
// at most U+10FFFF).
func hexRune(s string) (rune, bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || v > 0x10FFFF || v >= 0xD800 && v <= 0xDFFF {
		return 0, false
	}
	return rune(v), true
}

// AltRune returns a second character to try for a Symbol glyph name whose
// Rune is a private-use character or one the bundled symbol face lacks
// (symbolAlt), and false for any other name.
func AltRune(name string) (rune, bool) {
	r, ok := symbolAlt[name]
	return r, ok
}

// symbolAlt maps Symbol glyph names whose AGL value is a private-use
// character (U+F6D9–F6DB, U+F8E5–F8FE), or a character the bundled symbol
// face lacks (U+2329, U+232A, U+23AF, U+23D0), to a standard character of
// the same appearance, chosen from the Unicode code charts (Latin-1
// Supplement, Letterlike Symbols, Miscellaneous Technical, Box Drawing,
// Miscellaneous Mathematical Symbols-A). An implementation choice; the
// subsetting tool (testing/tools/std14fonts) keeps the same characters.
var symbolAlt = map[string]rune{
	"copyrightsans": 0x00A9, "copyrightserif": 0x00A9,
	"registersans": 0x00AE, "registerserif": 0x00AE,
	"trademarksans": 0x2122, "trademarkserif": 0x2122,
	"arrowvertex": 0x2502, "arrowhorizex": 0x2500,
	"angleleft": 0x27E8, "angleright": 0x27E9,
	"parenlefttp": 0x239B, "parenleftex": 0x239C, "parenleftbt": 0x239D,
	"parenrighttp": 0x239E, "parenrightex": 0x239F, "parenrightbt": 0x23A0,
	"bracketlefttp": 0x23A1, "bracketleftex": 0x23A2, "bracketleftbt": 0x23A3,
	"bracketrighttp": 0x23A4, "bracketrightex": 0x23A5, "bracketrightbt": 0x23A6,
	"bracelefttp": 0x23A7, "braceleftmid": 0x23A8, "braceleftbt": 0x23A9,
	"bracerighttp": 0x23AB, "bracerightmid": 0x23AC, "bracerightbt": 0x23AD,
	"braceex": 0x23AA, "integralex": 0x23AE,
}

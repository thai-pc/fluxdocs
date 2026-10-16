package fontmodel

import (
	"strconv"
	"unicode/utf8"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// glyphNames maps the glyph names of the Latin character set (ISO 32000-1
// Annex D, Table D.2) that are not a single character to their Unicode text.
// The ligatures map to their letters, not to U+FB00–FB04: the reading is for
// matching, and a pattern spells "fi" with two letters (copy/paste in most
// viewers does too).
// Single-letter names (A–Z, a–z) map to themselves; uniXXXX and uXXXX[XX]
// names are read by glyphRune.
var glyphNames = map[parse.Name]string{
	"space": " ", "exclam": "!", "quotedbl": "\"", "numbersign": "#", "dollar": "$",
	"percent": "%", "ampersand": "&", "quotesingle": "'", "quoteright": "’",
	"parenleft": "(", "parenright": ")", "asterisk": "*", "plus": "+", "comma": ",",
	"hyphen": "-", "period": ".", "slash": "/", "zero": "0", "one": "1", "two": "2",
	"three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8",
	"nine": "9", "colon": ":", "semicolon": ";", "less": "<", "equal": "=",
	"greater": ">", "question": "?", "at": "@", "bracketleft": "[", "backslash": "\\",
	"bracketright": "]", "asciicircum": "^", "underscore": "_", "grave": "`",
	"quoteleft": "‘", "braceleft": "{", "bar": "|", "braceright": "}",
	"asciitilde": "~", "endash": "–", "emdash": "—", "bullet": "•",
	"minus": "−", "fi": "fi", "fl": "fl", "ff": "ff", "ffi": "ffi",
	"ffl": "ffl", "quotedblleft": "“", "quotedblright": "”",
	"quotesinglbase": "‚", "quotedblbase": "„", "ellipsis": "…",
	"periodcentered": "·", "nbspace": " ", "sfthyphen": "­",
	"copyright": "©", "registered": "®", "trademark": "™",
	"degree": "°", "section": "§", "paragraph": "¶",
	"dagger": "†", "daggerdbl": "‡", "multiply": "×", "divide": "÷",
	"exclamdown": "¡", "questiondown": "¿", "cent": "¢",
	"sterling": "£", "yen": "¥", "Euro": "€", "florin": "ƒ",
	"guillemotleft": "«", "guillemotright": "»", "germandbls": "ß",
}

// glyphRune returns the Unicode text a glyph name stands for: a name from
// glyphNames, a single-character name, uniXXXX (one or more BMP code units
// of four hex digits each) or uXXXX–uXXXXXX. A suffix after a period
// ("a.sc") is ignored. Returns false for any other name.
func glyphRune(name parse.Name) (string, bool) {
	n := string(name)
	for i := 0; i < len(n); i++ {
		if n[i] == '.' && i > 0 {
			n = n[:i]
			break
		}
	}
	if s, ok := glyphNames[parse.Name(n)]; ok {
		return s, true
	}
	if len(n) == 1 {
		return n, true
	}
	if len(n) >= 7 && n[:3] == "uni" && (len(n)-3)%4 == 0 {
		var out []rune
		for i := 3; i < len(n); i += 4 {
			v, err := strconv.ParseUint(n[i:i+4], 16, 32)
			if err != nil || (v >= 0xD800 && v <= 0xDFFF) {
				return "", false
			}
			out = append(out, rune(v))
		}
		return string(out), true
	}
	if len(n) >= 5 && len(n) <= 7 && n[0] == 'u' {
		v, err := strconv.ParseUint(n[1:], 16, 32)
		if err != nil || !utf8.ValidRune(rune(v)) {
			return "", false
		}
		return string(rune(v)), true
	}
	return "", false
}

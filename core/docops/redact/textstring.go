package redact

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// textString decodes a PDF text string (ISO 32000-1 §7.9.2.2): UTF-16BE after
// a FE FF byte-order mark, otherwise PDFDocEncoding (Annex D, Table D.2). A
// UTF-8 string after EF BB BF (ISO 32000-2) and a UTF-16LE string after
// FF FE are decoded too, since readers that support them would show them.
func textString(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		// Not ISO, but some producers write UTF-16LE with its BOM and some
		// readers decode it; read it too (over-approximation).
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
		return string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF && utf8.Valid(b[3:]):
		return string(b[3:])
	}
	var s strings.Builder
	for _, c := range b {
		if r, ok := pdfDocDiffs[c]; ok {
			s.WriteRune(r)
		} else {
			s.WriteRune(rune(c))
		}
	}
	return s.String()
}

// pdfDocDiffs are the PDFDocEncoding codes that differ from Latin-1 (Annex D,
// Table D.2); 0x9F and 0xAD are undefined and read as U+FFFD.
var pdfDocDiffs = map[byte]rune{
	0x18: '˘', 0x19: 'ˇ', 0x1A: 'ˆ', 0x1B: '˙',
	0x1C: '˝', 0x1D: '˛', 0x1E: '˚', 0x1F: '˜',
	0x80: '•', 0x81: '†', 0x82: '‡', 0x83: '…',
	0x84: '—', 0x85: '–', 0x86: 'ƒ', 0x87: '⁄',
	0x88: '‹', 0x89: '›', 0x8A: '−', 0x8B: '‰',
	0x8C: '„', 0x8D: '“', 0x8E: '”', 0x8F: '‘',
	0x90: '’', 0x91: '‚', 0x92: '™', 0x93: 'ﬁ',
	0x94: 'ﬂ', 0x95: 'Ł', 0x96: 'Œ', 0x97: 'Š',
	0x98: 'Ÿ', 0x99: 'Ž', 0x9A: 'ı', 0x9B: 'ł',
	0x9C: 'œ', 0x9D: 'š', 0x9E: 'ž', 0x9F: '�',
	0xA0: '€', 0xAD: '�',
}

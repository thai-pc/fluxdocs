// Package cffmin builds minimal bare CFF font programs and PDFs that embed
// them, for the hostile-document gates and fuzz seeds under testing/ (design
// docs/design/type1-std14-fonts.md §5.4). It writes the format from Adobe
// Technical Notes #5176 (CFF) and #5177 (Type 2 charstrings).
//
// The fuller builder core/internal/psfont/cffbuild is internal to core and
// so cannot be imported from testing/; this is the small subset the gates
// need. Test support only.
package cffmin

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Op is a Type 2 charstring operator; escaped operators (12 x) are 32 + x.
type Op int

// The operators the gates and seeds use.
const (
	HStem     Op = 1
	HLineTo   Op = 6
	VLineTo   Op = 7
	CallSubr  Op = 10
	Return    Op = 11
	EndChar   Op = 14
	HintMask  Op = 19
	RMoveTo   Op = 21
	CallGSubr Op = 29
	Div       Op = 32 + 12
	Sqrt      Op = 32 + 26
	Random    Op = 32 + 23
	Flex      Op = 32 + 35
)

// CS encodes a charstring of ints (Type 2 numbers, at most ±32767) and Ops.
func CS(items ...any) []byte {
	var b []byte
	for _, it := range items {
		switch v := it.(type) {
		case Op:
			if v >= 32 {
				b = append(b, 12, byte(v-32))
			} else {
				b = append(b, byte(v))
			}
		case int:
			if v >= -107 && v <= 107 {
				b = append(b, byte(v+139))
			} else {
				b = append(b, 28)
				b = binary.BigEndian.AppendUint16(b, uint16(int16(v)))
			}
		case []byte:
			b = append(b, v...)
		}
	}
	return b
}

// Index encodes a CFF INDEX with 4-byte offsets.
func Index(objs [][]byte) []byte {
	if len(objs) == 0 {
		return []byte{0, 0}
	}
	b := binary.BigEndian.AppendUint16(nil, uint16(len(objs)))
	b = append(b, 4)
	off := uint32(1)
	b = binary.BigEndian.AppendUint32(b, off)
	for _, o := range objs {
		off += uint32(len(o))
		b = binary.BigEndian.AppendUint32(b, off)
	}
	for _, o := range objs {
		b = append(b, o...)
	}
	return b
}

// dictInt encodes an integer DICT operand in the fixed 5-byte form.
func dictInt(v int) []byte {
	return binary.BigEndian.AppendUint32([]byte{29}, uint32(int32(v)))
}

// Program builds a non-CID-keyed CFF program: glyph i+1 is glyphs[i], named
// gI (a String INDEX entry) and encoded at code I in its built-in encoding
// (the first 255), after a .notdef; subrs are its local subroutines. pad zero
// bytes follow the program, to make one of a chosen size.
func Program(glyphs [][]byte, subrs [][]byte, pad int) []byte {
	strs := make([][]byte, len(glyphs))
	for i := range glyphs {
		strs[i] = []byte(fmt.Sprintf("g%d", i))
	}
	charset := []byte{0}
	for i := range glyphs {
		charset = binary.BigEndian.AppendUint16(charset, uint16(391+i))
	}
	codes := min(len(glyphs), 255)
	encoding := []byte{0, byte(codes)}
	for i := 0; i < codes; i++ {
		encoding = append(encoding, byte(i))
	}
	css := append([][]byte{CS(EndChar)}, glyphs...)
	private := bytes.Join([][]byte{dictInt(0), {20}, dictInt(0), {21}}, nil)
	if len(subrs) > 0 {
		private = append(private, dictInt(len(private)+6)...)
		private = append(private, 19)
	}

	// Top DICT: charset, Encoding, CharStrings, Private; every offset in
	// the 5-byte form, so its size is fixed before the offsets are known.
	top := func(charsetOff, encOff, csOff, privOff int) []byte {
		return bytes.Join([][]byte{dictInt(charsetOff), {15}, dictInt(encOff), {16}, dictInt(csOff), {17},
			dictInt(len(private)), dictInt(privOff), {18}}, nil)
	}
	head := func(t []byte) []byte {
		return bytes.Join([][]byte{{1, 0, 4, 4}, Index([][]byte{[]byte("CFFMin")}), Index([][]byte{t}), Index(strs), Index(nil)}, nil)
	}
	h := len(head(top(0, 0, 0, 0)))
	charsetOff := h
	encOff := charsetOff + len(charset)
	csOff := encOff + len(encoding)
	csIdx := Index(css)
	privOff := csOff + len(csIdx)
	out := bytes.Join([][]byte{head(top(charsetOff, encOff, csOff, privOff)), charset, encoding, csIdx, private}, nil)
	if len(subrs) > 0 {
		out = append(out, Index(subrs)...)
	}
	return append(out, make([]byte, pad)...)
}

// CIDProgram builds a CID-keyed CFF program of one glyph (CID 1, after
// .notdef) with nFDs Font DICTs. With overlap false every Font DICT points
// at the same Private DICT of privateSize bytes ("0 defaultWidthX"
// repeated); with overlap true Font DICT i points at the same bytes less 2i
// at the front, so the DICTs are distinct but overlap.
func CIDProgram(glyph []byte, nFDs, privateSize int, overlap bool) []byte {
	private := bytes.Repeat([]byte{139, 20}, privateSize/2)
	fdDict := func(privOff, i int) []byte {
		if overlap {
			return bytes.Join([][]byte{dictInt(len(private) - 2*i), dictInt(privOff + 2*i), {18}}, nil)
		}
		return bytes.Join([][]byte{dictInt(len(private)), dictInt(privOff), {18}}, nil)
	}
	fdArray := func(privOff int) []byte {
		objs := make([][]byte, nFDs)
		for i := range objs {
			objs[i] = fdDict(privOff, i)
		}
		return Index(objs)
	}
	top := func(charsetOff, csOff, fdArrOff, fdSelOff int) []byte {
		ros := bytes.Join([][]byte{dictInt(391), dictInt(392), dictInt(0), {12, 30}}, nil)
		return bytes.Join([][]byte{ros, dictInt(charsetOff), {15}, dictInt(csOff), {17},
			dictInt(fdArrOff), {12, 36}, dictInt(fdSelOff), {12, 37}}, nil)
	}
	head := func(t []byte) []byte {
		return bytes.Join([][]byte{{1, 0, 4, 4}, Index([][]byte{[]byte("CFFMinCID")}), Index([][]byte{t}),
			Index([][]byte{[]byte("Adobe"), []byte("Identity")}), Index(nil)}, nil)
	}
	charset := []byte{0, 0, 1}
	fdSelect := []byte{0, 0, byte(nFDs - 1)}
	csIdx := Index([][]byte{CS(EndChar), glyph})
	h := len(head(top(0, 0, 0, 0)))
	charsetOff := h
	fdSelOff := charsetOff + len(charset)
	csOff := fdSelOff + len(fdSelect)
	fdArrOff := csOff + len(csIdx)
	privOff := fdArrOff + len(fdArray(0))
	return bytes.Join([][]byte{head(top(charsetOff, csOff, fdArrOff, fdSelOff)), charset, fdSelect, csIdx, fdArray(privOff), private}, nil)
}

// PDF builds a one-page PDF (200x200) whose /Font dictionary maps names to
// font dictionaries that all share one /FontFile3 /Type1C stream holding
// program (an indirect object, Flate-compressed when flate is non-nil: its
// compressed bytes), with each font's extra entries, and whose content is
// content.
func PDF(program, flate []byte, fonts map[string]string, content string) []byte {
	return pdf(program, flate, fonts, content, false, "", false)
}

// CIDPDF is PDF with Type 0 fonts (Identity-H) over one CIDFontType0
// descendant whose /FontFile3 is /CIDFontType0C.
func CIDPDF(program []byte, fonts map[string]string, content string) []byte {
	return pdf(program, nil, fonts, content, true, "", false)
}

// CIDPDFExtra is CIDPDF with cidExtra (a /W array, say) added to the
// CIDFont dictionary. With perFont each Type 0 font has a CIDFont of its own
// (each with cidExtra, all over the one program) instead of sharing one.
func CIDPDFExtra(program []byte, fonts map[string]string, content, cidExtra string, perFont bool) []byte {
	return pdf(program, nil, fonts, content, true, cidExtra, perFont)
}

func pdf(program, flate []byte, fonts map[string]string, content string, cid bool, cidExtra string, perFont bool) []byte {
	sub := "Type1C"
	if cid {
		sub = "CIDFontType0C"
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"", // page, below
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /FontDescriptor /FontName /CFFMin /Flags 4 /FontBBox [0 0 1000 1000] /FontFile3 6 0 R >>",
	}
	if flate != nil {
		objs = append(objs, fmt.Sprintf("<< /Length %d /Subtype /%s /Filter /FlateDecode >>\nstream\n%s\nendstream", len(flate), sub, flate))
	} else {
		objs = append(objs, fmt.Sprintf("<< /Length %d /Subtype /%s >>\nstream\n%s\nendstream", len(program), sub, program))
	}
	cidFont := "<< /Type /Font /Subtype /CIDFontType0 /BaseFont /CFFMin /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor 5 0 R " + cidExtra + " >>"
	objs = append(objs, cidFont)
	var fontDict bytes.Buffer
	fontDict.WriteString("<<")
	names := make([]string, 0, len(fonts))
	for n := range fonts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if cid {
			desc := 7
			if perFont {
				objs = append(objs, cidFont)
				desc = len(objs)
			}
			objs = append(objs, fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /CFFMin /Encoding /Identity-H /DescendantFonts [%d 0 R] %s >>", desc, fonts[n]))
		} else {
			objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /CFFMin /FontDescriptor 5 0 R "+fonts[n]+" >>")
		}
		fmt.Fprintf(&fontDict, " /%s %d 0 R", n, len(objs))
	}
	fontDict.WriteString(" >>")
	objs[2] = "<< /Type /Page /Parent 2 0 R /Resources << /Font " + fontDict.String() + " >> /Contents 4 0 R >>"

	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := make([]int, len(objs))
	for i, o := range objs {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

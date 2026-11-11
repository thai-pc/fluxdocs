// Package t1min builds minimal Type 1 font programs and PDFs that embed them,
// for the hostile-document gates and fuzz seeds under testing/ (design
// docs/design/type1-std14-fonts.md §5.4). Charstrings are stored unencrypted
// (/lenIV -1); only the eexec layer is encrypted (Type 1 Font Format Ch. 7).
//
// The fuller builder core/internal/psfont/t1build is internal to core and so
// cannot be imported from testing/; this is the small subset the gates need.
// Test support only.
package t1min

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Op is a charstring operator; escaped operators (12 x) are 32 + x.
type Op int

// The operators the gates and seeds use.
const (
	HStem         Op = 1
	RLineTo       Op = 5
	ClosePath     Op = 9
	CallSubr      Op = 10
	Return        Op = 11
	HSBW          Op = 13
	EndChar       Op = 14
	RMoveTo       Op = 21
	Seac          Op = 32 + 6
	Div           Op = 32 + 12
	CallOtherSubr Op = 32 + 16
	Pop           Op = 32 + 17
)

// CS encodes a charstring of ints (Type 1 numbers) and Ops.
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
				b = append(b, 255)
				b = binary.BigEndian.AppendUint32(b, uint32(int32(v)))
			}
		}
	}
	return b
}

// Glyph is a /CharStrings entry.
type Glyph struct {
	Name string
	CS   []byte
}

// Program builds a Type 1 program with the given glyphs (encoded at codes
// 0..len−1 in its built-in encoding, at most 256) and subroutines. pad bytes
// of white space are added to the private part before /CharStrings, to make
// a program of a chosen size.
func Program(glyphs []Glyph, subrs [][]byte, pad int) []byte {
	return ProgramPrivate(glyphs, subrs, string(bytes.Repeat([]byte{' '}, pad)))
}

// ProgramPrivate is Program with private, PostScript text, inserted into the
// private dictionary before /CharStrings: it is eexec-encrypted with the rest
// of the private part (e.g. a hostile "/lenIV 10000 def").
func ProgramPrivate(glyphs []Glyph, subrs [][]byte, private string) []byte {
	var clear bytes.Buffer
	clear.WriteString("%!PS-AdobeFont-1.0: T1Min 001.000\n/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n/Encoding 256 array\n")
	for i, g := range glyphs {
		if i < 256 {
			fmt.Fprintf(&clear, "dup %d /%s put\n", i, g.Name)
		}
	}
	clear.WriteString("readonly def\ncurrentfile eexec\n")

	var priv bytes.Buffer
	priv.WriteString("dup /Private 8 dict dup begin\n/lenIV -1 def\n")
	if len(subrs) > 0 {
		fmt.Fprintf(&priv, "/Subrs %d array\n", len(subrs))
		for i, s := range subrs {
			fmt.Fprintf(&priv, "dup %d %d RD ", i, len(s))
			priv.Write(s)
			priv.WriteString(" NP\n")
		}
		priv.WriteString("ND\n")
	}
	priv.WriteString(private)
	fmt.Fprintf(&priv, "2 index /CharStrings %d dict dup begin\n", len(glyphs))
	for _, g := range glyphs {
		fmt.Fprintf(&priv, "/%s %d RD ", g.Name, len(g.CS))
		priv.Write(g.CS)
		priv.WriteString(" ND\n")
	}
	priv.WriteString("end\nend\nmark currentfile closefile\n")

	plain := append([]byte{0, 0, 0, 0}, priv.Bytes()...)
	enc := make([]byte, len(plain))
	r := uint16(55665)
	for i, p := range plain {
		c := p ^ byte(r>>8)
		enc[i] = c
		r = (uint16(c)+r)*52845 + 22719
	}
	return append(clear.Bytes(), enc...)
}

// PDF builds a one-page PDF (200x200) whose /Font dictionary maps names to
// font dictionaries that all share one /FontFile stream holding program (an
// indirect object, Flate-compressed when flate is non-nil: its compressed
// bytes), with each font's extra entries, and whose content is content.
func PDF(program, flate []byte, fonts map[string]string, content string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"", // page, below
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /FontDescriptor /FontName /T1Min /Flags 4 /FontBBox [0 0 1000 1000] /FontFile 6 0 R >>",
	}
	if flate != nil {
		objs = append(objs, fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(flate), flate))
	} else {
		objs = append(objs, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(program), program))
	}
	var fontDict bytes.Buffer
	fontDict.WriteString("<<")
	names := make([]string, 0, len(fonts))
	for n := range fonts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /T1Min /FontDescriptor 5 0 R "+fonts[n]+" >>")
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

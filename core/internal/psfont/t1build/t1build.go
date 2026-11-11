// Package t1build assembles small Type 1 font programs for tests: a cleartext
// header, an eexec-encrypted private part with /Subrs and /CharStrings, and
// charstrings encrypted with /lenIV fixed bytes. It writes the format from
// Adobe's Type 1 Font Format (Ch. 2, 6, 7) and TN #5040 (PFB) and uses no
// third-party tool or font, so the fonts it builds are ours (MIT).
//
// It is test support for core/internal/psfont and core/render; nothing in a
// production path imports it.
package t1build

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// Op is a Type 1 charstring operator. Escaped operators (12 x) are 32 + x.
type Op int

// Charstring operators (Type 1 Font Format Ch. 6).
const (
	HStem           Op = 1
	VStem           Op = 3
	VMoveTo         Op = 4
	RLineTo         Op = 5
	HLineTo         Op = 6
	VLineTo         Op = 7
	RRCurveTo       Op = 8
	ClosePath       Op = 9
	CallSubr        Op = 10
	Return          Op = 11
	HSBW            Op = 13
	EndChar         Op = 14
	RMoveTo         Op = 21
	HMoveTo         Op = 22
	VHCurveTo       Op = 30
	HVCurveTo       Op = 31
	DotSection      Op = 32 + 0
	VStem3          Op = 32 + 1
	HStem3          Op = 32 + 2
	Seac            Op = 32 + 6
	SBW             Op = 32 + 7
	Div             Op = 32 + 12
	CallOtherSubr   Op = 32 + 16
	Pop             Op = 32 + 17
	SetCurrentPoint Op = 32 + 33
)

// CS encodes a charstring: ints are numbers in the Type 1 encoding (the
// shortest form, 255 + int32 beyond ±1131), Ops are operators.
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
			b = appendNum(b, v)
		default:
			panic(fmt.Sprintf("t1build.CS: unsupported item %T", it))
		}
	}
	return b
}

func appendNum(b []byte, v int) []byte {
	switch {
	case v >= -107 && v <= 107:
		return append(b, byte(v+139))
	case v >= 108 && v <= 1131:
		v -= 108
		return append(b, byte(v/256+247), byte(v%256))
	case v >= -1131 && v <= -108:
		v = -v - 108
		return append(b, byte(v/256+251), byte(v%256))
	}
	b = append(b, 255)
	return binary.BigEndian.AppendUint32(b, uint32(int32(v)))
}

// Encrypt runs the Type 1 cipher forward over prefix+plain with initial key r
// (Ch. 7): c = p ^ (r >> 8); r = (c + r) · 52845 + 22719 mod 2^16.
func Encrypt(plain []byte, r uint16, prefix []byte) []byte {
	in := append(append([]byte(nil), prefix...), plain...)
	out := make([]byte, len(in))
	for i, p := range in {
		c := p ^ byte(r>>8)
		out[i] = c
		r = (uint16(c)+r)*52845 + 22719
	}
	return out
}

// Glyph is one /CharStrings entry, its charstring in plaintext.
type Glyph struct {
	Name string
	CS   []byte
}

// Font describes a Type 1 program to build.
type Font struct {
	Name string
	// Matrix is the /FontMatrix array text; "" writes [0.001 0 0 0.001 0 0].
	Matrix string
	// Encoding maps codes to glyph names for a custom /Encoding array; nil
	// writes "/Encoding StandardEncoding def".
	Encoding map[int]string
	// LenIV is written as /lenIV when LenIVSet; otherwise the default 4
	// applies. -1 stores charstrings unencrypted.
	LenIV    int
	LenIVSet bool
	Subrs    [][]byte
	Glyphs   []Glyph
	// Hex writes the eexec part in hex; PFB wraps the result in PFB segments.
	Hex bool
	PFB bool
	// RD and NP override the RD/NP/ND spellings ("RD", "NP", "ND" by
	// default), to test "-|", "|" and "|-".
	RD, NP, ND string
}

// Bytes builds the program. Its cleartext part ends right after "eexec\r", so
// its length is the /Length1 value; Lengths reports all three.
func (f Font) Bytes() []byte {
	b, _, _, _ := f.build()
	return b
}

// Lengths returns /Length1, /Length2 and /Length3 for the non-PFB form.
func (f Font) Lengths() (l1, l2, l3 int) {
	_, l1, l2, l3 = f.build()
	return l1, l2, l3
}

func (f Font) build() ([]byte, int, int, int) {
	rd, np, nd := or(f.RD, "RD"), or(f.NP, "NP"), or(f.ND, "ND")
	lenIV := 4
	if f.LenIVSet {
		lenIV = f.LenIV
	}
	csEnc := func(plain []byte) []byte {
		if lenIV < 0 {
			return plain
		}
		return Encrypt(plain, 4330, make([]byte, lenIV))
	}

	var clear bytes.Buffer
	fmt.Fprintf(&clear, "%%!PS-AdobeFont-1.0: %s 001.000\n", or(f.Name, "Test"))
	fmt.Fprintf(&clear, "11 dict begin\n/FontInfo 2 dict dup begin\n/Notice (A test font (built by t1build); not eexec) readonly def\nend readonly def\n")
	fmt.Fprintf(&clear, "/FontName /%s def\n/PaintType 0 def\n/FontType 1 def\n", or(f.Name, "Test"))
	fmt.Fprintf(&clear, "/FontMatrix %s readonly def\n", or(f.Matrix, "[0.001 0 0 0.001 0 0]"))
	if f.Encoding == nil {
		clear.WriteString("/Encoding StandardEncoding def\n")
	} else {
		clear.WriteString("/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n")
		codes := make([]int, 0, len(f.Encoding))
		for c := range f.Encoding {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		for _, c := range codes {
			fmt.Fprintf(&clear, "dup %d /%s put\n", c, f.Encoding[c])
		}
		clear.WriteString("readonly def\n")
	}
	clear.WriteString("/FontBBox {0 -250 1000 900} readonly def\ncurrentdict end\ncurrentfile eexec\r")

	var priv bytes.Buffer
	priv.WriteString("dup /Private 8 dict dup begin\n/RD{string currentfile exch readstring pop}executeonly def\n")
	fmt.Fprintf(&priv, "/ND{noaccess def}executeonly def\n/NP{noaccess put}executeonly def\n")
	if f.LenIVSet {
		fmt.Fprintf(&priv, "/lenIV %d def\n", f.LenIV)
	}
	priv.WriteString("/BlueValues [] def\n/MinFeature {16 16} def\n/password 5839 def\n")
	if len(f.Subrs) > 0 {
		fmt.Fprintf(&priv, "/Subrs %d array\n", len(f.Subrs))
		for i, s := range f.Subrs {
			e := csEnc(s)
			fmt.Fprintf(&priv, "dup %d %d %s ", i, len(e), rd)
			priv.Write(e)
			fmt.Fprintf(&priv, " %s\n", np)
		}
		priv.WriteString("ND\n")
	}
	fmt.Fprintf(&priv, "2 index /CharStrings %d dict dup begin\n", len(f.Glyphs))
	for _, g := range f.Glyphs {
		e := csEnc(g.CS)
		fmt.Fprintf(&priv, "/%s %d %s ", g.Name, len(e), rd)
		priv.Write(e)
		fmt.Fprintf(&priv, " %s\n", nd)
	}
	priv.WriteString("end\nend\nreadonly put\nnoaccess put\ndup /FontName get exch definefont pop\nmark currentfile closefile\n")

	enc := Encrypt(priv.Bytes(), 55665, []byte{0x5a, 0x17, 0xc3, 0x01})
	if f.Hex {
		h := hex.EncodeToString(enc)
		var w bytes.Buffer
		for len(h) > 64 {
			w.WriteString(h[:64] + "\n")
			h = h[64:]
		}
		w.WriteString(h + "\n")
		enc = w.Bytes()
	}
	var trailer bytes.Buffer
	for i := 0; i < 8; i++ {
		trailer.WriteString("0000000000000000000000000000000000000000000000000000000000000000\n")
	}
	trailer.WriteString("cleartomark\n")

	if f.PFB {
		var out bytes.Buffer
		seg := func(typ byte, data []byte) {
			out.Write([]byte{0x80, typ})
			out.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(data))))
			out.Write(data)
		}
		seg(1, clear.Bytes())
		seg(2, enc)
		seg(1, trailer.Bytes())
		out.Write([]byte{0x80, 3})
		return out.Bytes(), 0, 0, 0
	}
	all := append(append(append([]byte(nil), clear.Bytes()...), enc...), trailer.Bytes()...)
	return all, clear.Len(), len(enc), trailer.Len()
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

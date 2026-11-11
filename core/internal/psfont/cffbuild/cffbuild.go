// Package cffbuild assembles small bare CFF font programs for tests: a
// header, the Name, Top DICT, String and Global Subr INDEXes, a charset, an
// Encoding, the CharStrings INDEX, Private DICTs with local Subrs and, for a
// CID-keyed font, an FDArray and FDSelect. It writes the format from Adobe
// Technical Notes #5176 (CFF) and #5177 (Type 2 charstrings) and uses no
// third-party tool or font, so the fonts it builds are ours (MIT).
//
// It is test support for core/internal/psfont and core/render; nothing in a
// production path imports it.
package cffbuild

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Op is a Type 2 charstring operator. Escaped operators (12 x) are 32 + x.
type Op int

// Charstring operators (TN #5177 Appendix A).
const (
	HStem      Op = 1
	VStem      Op = 3
	VMoveTo    Op = 4
	RLineTo    Op = 5
	HLineTo    Op = 6
	VLineTo    Op = 7
	RRCurveTo  Op = 8
	CallSubr   Op = 10
	Return     Op = 11
	EndChar    Op = 14
	HStemHM    Op = 18
	HintMask   Op = 19
	CntrMask   Op = 20
	RMoveTo    Op = 21
	HMoveTo    Op = 22
	VStemHM    Op = 23
	RCurveLine Op = 24
	RLineCurve Op = 25
	VVCurveTo  Op = 26
	HHCurveTo  Op = 27
	CallGSubr  Op = 29
	VHCurveTo  Op = 30
	HVCurveTo  Op = 31
	And        Op = 32 + 3
	Or         Op = 32 + 4
	Not        Op = 32 + 5
	Abs        Op = 32 + 9
	Add        Op = 32 + 10
	Sub        Op = 32 + 11
	Div        Op = 32 + 12
	Neg        Op = 32 + 14
	Eq         Op = 32 + 15
	Drop       Op = 32 + 18
	Put        Op = 32 + 20
	Get        Op = 32 + 21
	IfElse     Op = 32 + 22
	Random     Op = 32 + 23
	Mul        Op = 32 + 24
	Sqrt       Op = 32 + 26
	Dup        Op = 32 + 27
	Exch       Op = 32 + 28
	Index      Op = 32 + 29
	Roll       Op = 32 + 30
	HFlex      Op = 32 + 34
	Flex       Op = 32 + 35
	HFlex1     Op = 32 + 36
	Flex1      Op = 32 + 37
)

// Fixed is a charstring number written in the 16.16 fixed-point form (255).
type Fixed float64

// Raw is written to a charstring as is (hintmask bytes, hostile bytes).
type Raw []byte

// CS encodes a charstring: ints are numbers in the shortest Type 2 form (one
// byte, two bytes, or 28 + int16), Fixed values use 255 + 16.16, Ops are
// operators and Raw bytes are copied.
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
			switch {
			case v >= -107 && v <= 107:
				b = append(b, byte(v+139))
			case v >= 108 && v <= 1131:
				v -= 108
				b = append(b, byte(v/256+247), byte(v%256))
			case v >= -1131 && v <= -108:
				v = -v - 108
				b = append(b, byte(v/256+251), byte(v%256))
			case v >= math.MinInt16 && v <= math.MaxInt16:
				b = append(b, 28)
				b = binary.BigEndian.AppendUint16(b, uint16(int16(v)))
			default:
				panic(fmt.Sprintf("cffbuild.CS: %d does not fit a charstring integer; use Fixed", v))
			}
		case Fixed:
			b = append(b, 255)
			b = binary.BigEndian.AppendUint32(b, uint32(int32(math.Round(float64(v)*65536))))
		case Raw:
			b = append(b, v...)
		default:
			panic(fmt.Sprintf("cffbuild.CS: unsupported item %T", it))
		}
	}
	return b
}

// Glyph is a CharStrings entry: its name (non-CID-keyed fonts) or CID
// (CID-keyed fonts) for the charset, and its charstring. Glyph 0 is
// .notdef (CID 0) whatever its Name or CID.
type Glyph struct {
	Name string
	CID  int
	CS   []byte
}

// FD is a CID-keyed font's Font DICT with its Private DICT.
type FD struct {
	Matrix        []float64 // nil: no FontMatrix in the Font DICT
	Subrs         [][]byte
	DefaultWidthX int
	NominalWidthX int
}

// Font describes a program to build. The zero values give a non-CID-keyed
// font with the default FontMatrix, charset format 0 and the Standard
// encoding.
type Font struct {
	Name   string // the Name INDEX entry ("FluxCFFTest" if empty)
	Matrix []float64
	Glyphs []Glyph
	GSubrs [][]byte

	// Non-CID-keyed fonts: the Private DICT.
	Subrs         [][]byte
	DefaultWidthX int
	NominalWidthX int
	// Encoding, when non-nil, is a custom encoding (code → glyph name),
	// written in EncodingFormat 0 or 1; Supplements (code → glyph name,
	// standard or custom string) set the format's high bit.
	Encoding       map[int]string
	EncodingFormat int
	Supplements    map[int]string
	CharsetFormat  int // 0, 1 or 2
	// PredefinedCharset, when 1 (Expert) or 2 (ExpertSubset), is written
	// as the charset offset instead of the custom charset's; likewise
	// PredefinedEncoding 1 (Expert) for the Encoding offset.
	PredefinedCharset  int
	PredefinedEncoding int

	// CID-keyed fonts: ROS is written, FDs become the FDArray, and
	// FDSelect (one FD index per glyph) is written in FDSelectFormat 0 or 3.
	CID            bool
	FDs            []FD
	FDSelect       []int
	FDSelectFormat int

	// CharstringType, when non-zero, is written to the Top DICT.
	CharstringType int
}

// StdStrings are the CFF standard strings the builder knows by SID (TN
// #5176 Appendix A): the subset its tests name. Others go to the String
// INDEX.
var StdStrings = map[string]int{
	".notdef": 0, "space": 1, "A": 34, "B": 35, "C": 36, "D": 37, "a": 66, "b": 67,
	"acute": 125, "grave": 124, "Aacute": 171, "zcaron": 228,
}

// dict is a DICT being encoded: numbers and operators.
type dict struct{ b []byte }

// int writes an integer operand in the 5-byte form (29), so a DICT's size
// does not depend on the offsets it holds.
func (d *dict) int(v int) *dict {
	d.b = append(d.b, 29)
	d.b = binary.BigEndian.AppendUint32(d.b, uint32(int32(v)))
	return d
}

// real writes a real operand (30, nibbles).
func (d *dict) real(v float64) *dict {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	var nib []byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			nib = append(nib, c-'0')
		case c == '.':
			nib = append(nib, 0xa)
		case c == '-':
			nib = append(nib, 0xe)
		case c == 'e':
			if i+1 < len(s) && s[i+1] == '-' {
				nib = append(nib, 0xc)
				i++
			} else {
				nib = append(nib, 0xb)
				if i+1 < len(s) && s[i+1] == '+' {
					i++
				}
			}
		}
	}
	nib = append(nib, 0xf)
	if len(nib)%2 == 1 {
		nib = append(nib, 0xf)
	}
	d.b = append(d.b, 30)
	for i := 0; i < len(nib); i += 2 {
		d.b = append(d.b, nib[i]<<4|nib[i+1])
	}
	return d
}

// op writes an operator (32 + x for 12 x).
func (d *dict) op(o int) *dict {
	if o >= 32 {
		d.b = append(d.b, 12, byte(o-32))
	} else {
		d.b = append(d.b, byte(o))
	}
	return d
}

// EncodeIndex encodes an INDEX of objects with the smallest offSize.
func EncodeIndex(objs [][]byte) []byte {
	if len(objs) == 0 {
		return []byte{0, 0}
	}
	total := 1
	for _, o := range objs {
		total += len(o)
	}
	offSize := 1
	for total >= 1<<(8*offSize) {
		offSize++
	}
	b := binary.BigEndian.AppendUint16(nil, uint16(len(objs)))
	b = append(b, byte(offSize))
	off := 1
	put := func(v int) {
		for i := offSize - 1; i >= 0; i-- {
			b = append(b, byte(v>>(8*i)))
		}
	}
	put(off)
	for _, o := range objs {
		off += len(o)
		put(off)
	}
	for _, o := range objs {
		b = append(b, o...)
	}
	return b
}

// Bytes builds the program.
func (f Font) Bytes() []byte {
	// The layout is computed twice: every offset is a fixed-size DICT
	// integer, so the first pass (offsets 0) gives every position.
	var out []byte
	pos := layout{privates: make([]int, len(f.privates()))}
	for pass := 0; pass < 2; pass++ {
		out, pos = f.build(pos)
	}
	return out
}

// layout holds the offsets one build pass found.
type layout struct {
	charset, encoding, charStrings, fdSelect, fdArray int
	privates                                          []int // per Private DICT (one per FD, or one)
}

func (f Font) build(at layout) ([]byte, layout) {
	name := f.Name
	if name == "" {
		name = "FluxCFFTest"
	}
	var strs []string
	sid := func(s string) int {
		if v, ok := StdStrings[s]; ok {
			return v
		}
		for i, t := range strs {
			if t == s {
				return 391 + i
			}
		}
		strs = append(strs, s)
		return 391 + len(strs) - 1
	}

	privs := f.privates()
	privData := make([][]byte, len(privs))
	for i, p := range privs {
		d := &dict{}
		d.int(p.DefaultWidthX).op(20).int(p.NominalWidthX).op(21)
		if len(p.Subrs) > 0 {
			d.int(0).op(19) // placeholder, rewritten below
		}
		size := len(d.b)
		d = &dict{}
		d.int(p.DefaultWidthX).op(20).int(p.NominalWidthX).op(21)
		if len(p.Subrs) > 0 {
			d.int(size).op(19) // the Subrs INDEX follows the Private DICT
		}
		privData[i] = d.b
	}

	top := &dict{}
	if f.CID {
		top.int(sid("Adobe")).int(sid("Identity")).int(0).op(32 + 30)
	}
	if f.CharstringType != 0 {
		top.int(f.CharstringType).op(32 + 6)
	}
	if f.Matrix != nil {
		for _, v := range f.Matrix {
			top.real(v)
		}
		top.op(32 + 7)
	}
	if f.PredefinedCharset != 0 {
		top.int(f.PredefinedCharset).op(15)
	} else {
		top.int(at.charset).op(15)
	}
	if !f.CID {
		if f.PredefinedEncoding != 0 {
			top.int(f.PredefinedEncoding).op(16)
		} else if f.Encoding != nil {
			top.int(at.encoding).op(16)
		}
		top.int(len(privData[0])).int(at.privates[0]).op(18)
	} else {
		top.int(at.fdArray).op(32 + 36).int(at.fdSelect).op(32 + 37)
	}
	top.int(at.charStrings).op(17)

	var charset []byte
	gids := len(f.Glyphs)
	ids := make([]int, gids)
	for g := 1; g < gids; g++ {
		if f.CID {
			ids[g] = f.Glyphs[g].CID
		} else {
			ids[g] = sid(f.Glyphs[g].Name)
		}
	}
	charset = f.encodeCharset(ids)

	var encoding []byte
	if f.Encoding != nil && !f.CID {
		encoding = f.encodeEncoding(sid)
	}

	var fdArray []byte
	var fdSelect []byte
	if f.CID {
		objs := make([][]byte, len(f.FDs))
		for i, fd := range f.FDs {
			d := &dict{}
			if fd.Matrix != nil {
				for _, v := range fd.Matrix {
					d.real(v)
				}
				d.op(32 + 7)
			}
			d.int(len(privData[i])).int(at.privates[i]).op(18)
			objs[i] = d.b
		}
		fdArray = EncodeIndex(objs)
		fdSelect = f.encodeFDSelect()
	}

	css := make([][]byte, gids)
	for i, g := range f.Glyphs {
		css[i] = g.CS
	}

	strObjs := make([][]byte, len(strs))
	for i, s := range strs {
		strObjs[i] = []byte(s)
	}

	var b bytes.Buffer
	b.Write([]byte{1, 0, 4, 4})
	b.Write(EncodeIndex([][]byte{[]byte(name)}))
	b.Write(EncodeIndex([][]byte{top.b}))
	b.Write(EncodeIndex(strObjs))
	b.Write(EncodeIndex(f.GSubrs))
	var l layout
	l.charset = b.Len()
	b.Write(charset)
	if encoding != nil {
		l.encoding = b.Len()
		b.Write(encoding)
	}
	l.charStrings = b.Len()
	b.Write(EncodeIndex(css))
	if f.CID {
		l.fdSelect = b.Len()
		b.Write(fdSelect)
		l.fdArray = b.Len()
		b.Write(fdArray)
	}
	l.privates = make([]int, len(privs))
	for i, p := range privs {
		l.privates[i] = b.Len()
		b.Write(privData[i])
		if len(p.Subrs) > 0 {
			b.Write(EncodeIndex(p.Subrs))
		}
	}
	return b.Bytes(), l
}

// privates returns the Private DICTs: one per FD, or the font's own.
func (f Font) privates() []FD {
	if f.CID {
		return f.FDs
	}
	return []FD{{Subrs: f.Subrs, DefaultWidthX: f.DefaultWidthX, NominalWidthX: f.NominalWidthX}}
}

// encodeCharset writes ids (GID 1 on) in CharsetFormat.
func (f Font) encodeCharset(ids []int) []byte {
	b := []byte{byte(f.CharsetFormat)}
	if len(ids) == 0 {
		return b
	}
	if f.CharsetFormat == 0 {
		for _, id := range ids[1:] {
			b = binary.BigEndian.AppendUint16(b, uint16(id))
		}
		return b
	}
	for g := 1; g < len(ids); {
		first, left := ids[g], 0
		for g+left+1 < len(ids) && ids[g+left+1] == first+left+1 && (f.CharsetFormat == 2 || left < 255) {
			left++
		}
		b = binary.BigEndian.AppendUint16(b, uint16(first))
		if f.CharsetFormat == 2 {
			b = binary.BigEndian.AppendUint16(b, uint16(left))
		} else {
			b = append(b, byte(left))
		}
		g += left + 1
	}
	return b
}

// encodeEncoding writes the custom encoding: each glyph from GID 1 gets the
// code Encoding gives its name (glyphs without one get code 0 in format 0,
// and end the ranges in format 1).
func (f Font) encodeEncoding(sid func(string) int) []byte {
	codeOf := map[string]int{}
	for c, n := range f.Encoding {
		if old, ok := codeOf[n]; !ok || c < old {
			codeOf[n] = c
		}
	}
	var codes []int
	for _, g := range f.Glyphs[min(1, len(f.Glyphs)):] {
		c, ok := codeOf[g.Name]
		if !ok {
			break
		}
		codes = append(codes, c)
	}
	format := byte(f.EncodingFormat)
	if len(f.Supplements) > 0 {
		format |= 0x80
	}
	b := []byte{format}
	if f.EncodingFormat == 0 {
		b = append(b, byte(len(codes)))
		for _, c := range codes {
			b = append(b, byte(c))
		}
	} else {
		var ranges [][2]int
		for i := 0; i < len(codes); {
			left := 0
			for i+left+1 < len(codes) && codes[i+left+1] == codes[i]+left+1 {
				left++
			}
			ranges = append(ranges, [2]int{codes[i], left})
			i += left + 1
		}
		b = append(b, byte(len(ranges)))
		for _, r := range ranges {
			b = append(b, byte(r[0]), byte(r[1]))
		}
	}
	if len(f.Supplements) > 0 {
		keys := make([]int, 0, len(f.Supplements))
		for c := range f.Supplements {
			keys = append(keys, c)
		}
		sort.Ints(keys)
		b = append(b, byte(len(keys)))
		for _, c := range keys {
			b = append(b, byte(c))
			b = binary.BigEndian.AppendUint16(b, uint16(sid(f.Supplements[c])))
		}
	}
	return b
}

// encodeFDSelect writes FDSelect in FDSelectFormat 0 or 3.
func (f Font) encodeFDSelect() []byte {
	sel := make([]int, len(f.Glyphs))
	copy(sel, f.FDSelect)
	if f.FDSelectFormat == 0 {
		b := []byte{0}
		for _, fd := range sel {
			b = append(b, byte(fd))
		}
		return b
	}
	var ranges [][2]int
	for g, fd := range sel {
		if len(ranges) == 0 || ranges[len(ranges)-1][1] != fd {
			ranges = append(ranges, [2]int{g, fd})
		}
	}
	b := []byte{3}
	b = binary.BigEndian.AppendUint16(b, uint16(len(ranges)))
	for _, r := range ranges {
		b = binary.BigEndian.AppendUint16(b, uint16(r[0]))
		b = append(b, byte(r[1]))
	}
	return binary.BigEndian.AppendUint16(b, uint16(len(sel)))
}

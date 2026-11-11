package fuzz

import (
	"bytes"
	"encoding/binary"

	"github.com/thai-pc/fluxdocs/testing/internal/t1min"
)

// type1SeedBytes are PDF-level seeds embedding hostile Type 1 programs
// (docs/design/type1-std14-fonts.md §5.4): a well-formed square, a
// self-recursive and a mutually recursive subroutine, seac of a seac, a
// callothersubr with more arguments than the stack holds, flex with three
// points and flex never ended, div by zero, a 32-bit number past the
// coordinate bound, an out-of-range /lenIV, PFB lengths of 0xFFFFFFFF and a
// program cut inside its eexec part.
func type1SeedBytes() [][]byte {
	sq := t1min.CS(0, 500, t1min.HSBW, 10, 10, t1min.RMoveTo, 100, 0, t1min.RLineTo, 0, 100, t1min.RLineTo, t1min.ClosePath, t1min.EndChar)
	page := func(prog []byte) []byte {
		return t1min.PDF(prog, nil, map[string]string{"F1": ""}, "BT /F1 50 Tf 10 10 Td <00010203> Tj ET")
	}
	glyphs := func(css ...[]byte) []t1min.Glyph {
		out := make([]t1min.Glyph, len(css))
		for i, cs := range css {
			out[i] = t1min.Glyph{Name: string(rune('a' + i)), CS: cs}
		}
		return out
	}
	hsbw := t1min.CS(0, 500, t1min.HSBW)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	square := t1min.Program(glyphs(sq), nil, 0)
	recursive := t1min.Program(glyphs(cat(hsbw, t1min.CS(0, t1min.CallSubr, t1min.EndChar))),
		[][]byte{t1min.CS(0, t1min.CallSubr, t1min.Return)}, 0)
	mutual := t1min.Program(glyphs(cat(hsbw, t1min.CS(0, t1min.CallSubr, t1min.EndChar))),
		[][]byte{t1min.CS(1, t1min.CallSubr, t1min.Return), t1min.CS(0, t1min.CallSubr, t1min.Return)}, 0)
	seacSeac := t1min.Program([]t1min.Glyph{
		{Name: "a", CS: cat(hsbw, t1min.CS(0, 0, 0, 65, 194, t1min.Seac))},
		{Name: "A", CS: cat(hsbw, t1min.CS(0, 0, 0, 65, 194, t1min.Seac))},
		{Name: "acute", CS: sq},
	}, nil, 0)
	hostile := t1min.Program(glyphs(
		cat(hsbw, t1min.CS(1, 9, 30, t1min.CallOtherSubr, t1min.EndChar)),
		cat(hsbw, t1min.CS(0, 1, t1min.CallOtherSubr, 1, 1, t1min.RMoveTo, 0, 2, t1min.CallOtherSubr, 1, 1, t1min.RMoveTo, 0, 2, t1min.CallOtherSubr,
			1, 1, t1min.RMoveTo, 0, 2, t1min.CallOtherSubr, 50, 3, 3, 3, 0, t1min.CallOtherSubr, t1min.Pop, t1min.Pop, t1min.EndChar)),
		cat(hsbw, t1min.CS(0, 1, t1min.CallOtherSubr, 1, 1, t1min.RMoveTo, t1min.EndChar)),
		cat(hsbw, t1min.CS(1, 0, t1min.Div, 0, t1min.RLineTo, t1min.EndChar)),
		cat(hsbw, t1min.CS(2000000000, 0, t1min.RMoveTo, 1, 1, t1min.RLineTo, t1min.EndChar)),
	), nil, 0)
	// /lenIV belongs to the private dictionary: inside the encrypted part.
	lenIV := t1min.ProgramPrivate(glyphs(sq), nil, "/lenIV 10000 def\n")
	pfb := append([]byte{0x80, 0x01}, binary.LittleEndian.AppendUint32(nil, 0xFFFFFFFF)...)
	pfb = append(pfb, square...)
	cut := square[:bytes.Index(square, []byte("eexec"))+40]
	var out [][]byte
	for _, prog := range [][]byte{square, recursive, mutual, seacSeac, hostile, lenIV, pfb, cut} {
		out = append(out, page(prog))
	}
	return out
}

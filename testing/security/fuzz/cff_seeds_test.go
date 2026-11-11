package fuzz

import (
	"bytes"

	"github.com/thai-pc/fluxdocs/testing/internal/cffmin"
)

// cffSeedBytes are PDF-level seeds embedding hostile CFF programs
// (docs/design/type1-std14-fonts.md §5.4), as Type1C simple fonts and
// CIDFontType0C descendants: a well-formed square, a self-recursive and a
// local/global mutually recursive subroutine, endchar-seac of a seac, a
// hintmask after more stems than the limit, random/sqrt/div by zero, a flex
// short of operands, an INDEX with offSize 5 and with decreasing offsets,
// 256 Font DICTs overlapping one Private DICT, and a program cut short.
func cffSeedBytes() [][]byte {
	sq := cffmin.CS(500, 10, 10, cffmin.RMoveTo, 100, 100, -100, cffmin.HLineTo, cffmin.EndChar)
	page := func(prog []byte) []byte {
		return cffmin.PDF(prog, nil, map[string]string{"F1": ""}, "BT /F1 50 Tf 10 10 Td <00010203> Tj ET")
	}
	cidPage := func(prog []byte) []byte {
		return cffmin.CIDPDF(prog, map[string]string{"F1": ""}, "BT /F1 50 Tf 10 10 Td <00000001> Tj ET")
	}
	square := cffmin.Program([][]byte{sq}, nil, 0)
	recursive := cffmin.Program([][]byte{cffmin.CS(-107, cffmin.CallSubr, cffmin.EndChar)}, [][]byte{cffmin.CS(-107, cffmin.CallSubr, cffmin.Return)}, 0)
	mutual := cffmin.Program([][]byte{cffmin.CS(-107, cffmin.CallSubr, cffmin.EndChar)}, [][]byte{cffmin.CS(-107, cffmin.CallGSubr, cffmin.Return)}, 0)
	hostile := cffmin.Program([][]byte{
		cffmin.CS(0, 0, 65, 194, cffmin.EndChar),
		cffmin.CS(bytes.Repeat(cffmin.CS(1, 1, cffmin.HStem), 100), cffmin.HintMask, cffmin.EndChar),
		cffmin.CS(cffmin.Random, 0, cffmin.Div, -1, cffmin.Sqrt, cffmin.EndChar),
		cffmin.CS(1, 2, 3, cffmin.Flex, cffmin.EndChar),
	}, nil, 0)
	offSize5 := append([]byte(nil), square...)
	offSize5[6] = 5 // the Name INDEX's offSize
	decreasing := append([]byte(nil), square...)
	decreasing[14] = 0 // the Name INDEX's second offset (offSize 4): 0 < 1
	cid := cffmin.CIDProgram(sq, 4, 64, false)
	overlap := cffmin.CIDProgram(sq, 256, 4096, true)
	var out [][]byte
	for _, prog := range [][]byte{square, recursive, mutual, hostile, offSize5, decreasing, square[:len(square)/2]} {
		out = append(out, page(prog))
	}
	return append(out, cidPage(cid), cidPage(overlap), cidPage(cid[:len(cid)-3]))
}

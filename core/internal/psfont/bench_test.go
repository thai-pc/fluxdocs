package psfont

import (
	"bytes"
	"testing"

	tb "github.com/thai-pc/fluxdocs/core/internal/psfont/t1build"
)

// BenchmarkOutline measures charstring operators per nanosecond on a
// glyph that runs into the operator bound through two subroutine levels,
// and on the hand-built test glyphs; ns/op-unit is what charstringOpCost
// in core/render is calibrated against.
func BenchmarkOutline(b *testing.B) {
	glyph := append(tb.CS(0, 500, tb.HSBW), bytes.Repeat(tb.CS(1, tb.CallSubr), 40)...)
	fnt := tb.Font{
		Subrs: [][]byte{tb.CS(tb.Return), append(bytes.Repeat(tb.CS(2, tb.CallSubr), 40), tb.CS(tb.Return)...),
			append(bytes.Repeat(tb.CS(1, 2, tb.RLineTo), 40), tb.CS(tb.Return)...)},
		Glyphs: []tb.Glyph{{Name: "bomb", CS: glyph}, {Name: "square", CS: glyphSquare}, {Name: "flex", CS: glyphFlex}},
	}
	f, err := ParseType1(fnt.Bytes(), Options{})
	if err != nil {
		b.Fatal(err)
	}
	for _, name := range []string{"bomb", "square", "flex"} {
		gid, _ := f.GlyphByName(name)
		b.Run(name, func(b *testing.B) {
			ops := 0
			for i := 0; i < b.N; i++ {
				g, _ := f.Outline(gid)
				ops += g.Ops
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(ops), "ns/charstring-op")
		})
	}
}

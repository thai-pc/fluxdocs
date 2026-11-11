package psfont

import (
	"bytes"
	"encoding/binary"
	"testing"

	tb "github.com/thai-pc/fluxdocs/core/internal/psfont/t1build"
)

// fuzzGlyphCap bounds the glyphs one fuzz input builds, so an input with
// 65,536 charstrings stays fast; the per-glyph limits are what is under test.
const fuzzGlyphCap = 2048

// type1Seeds are FuzzType1's seeds: the hand-built font in every container
// form plus hostile programs (design §5.4).
func type1Seeds() [][]byte {
	base := testFont()
	hex, pfb := base, base
	hex.Hex, pfb.PFB = true, true
	bigIV := base
	bigIV.LenIV, bigIV.LenIVSet = 10000, true
	self := tb.Font{Subrs: [][]byte{tb.CS(0, tb.CallSubr, tb.Return)}, Glyphs: []tb.Glyph{{Name: "g", CS: tb.CS(0, 500, tb.HSBW, 0, tb.CallSubr, tb.EndChar)}}}
	mutual := tb.Font{
		Subrs:  [][]byte{tb.CS(1, tb.CallSubr, tb.Return), tb.CS(0, tb.CallSubr, tb.Return)},
		Glyphs: []tb.Glyph{{Name: "g", CS: tb.CS(0, 500, tb.HSBW, 0, tb.CallSubr, tb.EndChar)}},
	}
	seacSeac := tb.Font{Glyphs: []tb.Glyph{
		{Name: "A", CS: tb.CS(0, 500, tb.HSBW, 0, 0, 0, 65, 65, tb.Seac)},
		{Name: "acute", CS: tb.CS(0, 500, tb.HSBW, tb.EndChar)},
		{Name: "Aacute", CS: tb.CS(0, 500, tb.HSBW, 0, 0, 0, 65, 194, tb.Seac)},
	}}
	othersubr := tb.Font{Glyphs: []tb.Glyph{
		{Name: "n", CS: tb.CS(0, 500, tb.HSBW, 1, 2, 30, 9, tb.CallOtherSubr, tb.Pop, tb.Pop, tb.EndChar)},
		{Name: "flex3", CS: tb.CS(0, 500, tb.HSBW, 0, 1, tb.CallOtherSubr, 1, 1, tb.RMoveTo, 0, 2, tb.CallOtherSubr,
			50, 0, 0, 3, 0, tb.CallOtherSubr, tb.Pop, tb.Pop, tb.SetCurrentPoint, tb.EndChar)},
		{Name: "flexopen", CS: tb.CS(0, 500, tb.HSBW, 0, 1, tb.CallOtherSubr, 1, 1, tb.RMoveTo, tb.EndChar)},
		{Name: "div0", CS: tb.CS(0, 500, tb.HSBW, 1, 0, tb.Div, tb.EndChar)},
	}}
	pfbHuge := pfb.Bytes()
	binary.LittleEndian.PutUint32(pfbHuge[2:6], 0xFFFFFFFF)
	oddHex := hex.Bytes()
	if i := bytes.Index(oddHex, bytes.Repeat([]byte{'0'}, 64)); i > 2 {
		oddHex = append(append(append([]byte(nil), oddHex[:i-2]...), '\n'), oddHex[i:]...)
	}
	seeds := [][]byte{
		base.Bytes(), hex.Bytes(), pfb.Bytes(), bigIV.Bytes(), self.Bytes(), mutual.Bytes(),
		seacSeac.Bytes(), othersubr.Bytes(), pfbHuge, oddHex,
		[]byte("currentfile eexec\n"), {0x80, 0x01, 0xff, 0xff, 0xff, 0xff},
	}
	for _, p := range privateSeeds() {
		seeds = append(seeds, eexecProgram(p))
	}
	return seeds
}

// privateSeeds are plaintext private parts: t1build's (the private part of
// each seed font above) and hostile shapes, an entry length of MaxInt64 (B1)
// and a second /CharStrings whose seac names glyphs of the first (B2).
func privateSeeds() [][]byte {
	base := testFont()
	bigIV := base
	bigIV.LenIV, bigIV.LenIVSet = 10000, true
	counters := base
	counters.Glyphs = append(counters.Glyphs, tb.Glyph{Name: "m", CS: glyphCounters})
	var out [][]byte
	for _, f := range []tb.Font{base, bigIV, counters} {
		out = append(out, privatePart(f))
	}
	return append(out,
		[]byte("/lenIV -1 def /Subrs 1 array dup 0 9223372036854775807 RD x NP /CharStrings 1 dict dup begin /a 1 RD x ND end"),
		[]byte("/lenIV -1 def /CharStrings 1 dict dup begin /a 9223372036854775807 RD x ND end"),
		secondCharStrings(),
		repeatedSubrs(64),
	)
}

// privatePart decrypts the eexec part t1build wrote for f, dropping its four
// random bytes: the plaintext private dictionary.
func privatePart(f tb.Font) []byte {
	data := f.Bytes()
	_, enc, ok := splitEexec(data)
	if !ok || len(enc) < eexecDiscard {
		panic("t1build wrote no eexec part")
	}
	plain := make([]byte, len(enc))
	decrypt(plain, enc, eexecKey)
	return plain[eexecDiscard:]
}

// FuzzType1 parses a program and builds its glyphs: no input may panic,
// hang, or allocate without bound.
func FuzzType1(f *testing.F) {
	for _, s := range type1Seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var charged int64
		font, err := ParseType1(data, Options{Charge: func(n int64) bool {
			charged += n
			return charged <= 4*maxProgramBytes
		}})
		if err != nil {
			return
		}
		_ = font.Matrix()
		_, _ = font.BuiltinEncoding()
		for g := 0; g < font.NumGlyphs() && g < fuzzGlyphCap; g++ {
			gl, err := font.Outline(GID(g))
			if gl.Ops > maxGlyphOps+1 || len(gl.Segs) > maxGlyphSegments {
				t.Fatalf("glyph %d past the limits: %d ops, %d segments", g, gl.Ops, len(gl.Segs))
			}
			if err == nil {
				checkPoints(t, gl)
			}
		}
	})
}

// FuzzType1Private fuzzes the decrypted private part, which FuzzType1 reaches
// only through the cipher: the input is eexec-encrypted behind a fixed
// cleartext prefix (eexecProgram), parsed, and every glyph is built. No input
// may panic, hang, or allocate without bound.
func FuzzType1Private(f *testing.F) {
	for _, s := range privateSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, private []byte) {
		var charged int64
		font, err := ParseType1(eexecProgram(private), Options{Charge: func(n int64) bool {
			charged += n
			return charged <= 4*maxProgramBytes
		}})
		if err != nil {
			return
		}
		for g := 0; g < font.NumGlyphs() && g < fuzzGlyphCap; g++ {
			gl, err := font.Outline(GID(g))
			if gl.Ops > maxGlyphOps+1 || len(gl.Segs) > maxGlyphSegments {
				t.Fatalf("glyph %d past the limits: %d ops, %d segments", g, gl.Ops, len(gl.Segs))
			}
			if err == nil {
				checkPoints(t, gl)
			}
		}
	})
}

// FuzzT1Charstring runs a raw charstring against a synthetic font whose
// subroutine 0 and the seac components (A and acute) are fuzzed too.
func FuzzT1Charstring(f *testing.F) {
	for _, s := range [][2][]byte{
		{glyphSquare, tb.CS(tb.Return)},
		{glyphFlex, tb.CS(tb.Return)},
		{glyphHint, tb.CS(0, 50, tb.HStem, tb.Return)},
		{glyphAacute, glyphA},
		{tb.CS(0, 500, tb.HSBW, 0, tb.CallSubr, tb.EndChar), tb.CS(0, tb.CallSubr, tb.Return)},
		{tb.CS(0, 500, tb.HSBW, 1, 2, 30, 9, tb.CallOtherSubr, tb.EndChar), nil},
		{tb.CS(0, 500, tb.HSBW, 0, 0, 0, 65, 194, tb.Seac), tb.CS(0, 0, 0, 65, 194, tb.Seac)},
		{bytes.Repeat(tb.CS(1, 0, tb.RLineTo), 9000), nil},
		{[]byte{255, 0x7f, 0xff, 0xff, 0xff, 255, 0x7f, 0xff, 0xff, 0xff, 21}, nil},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, cs, sub []byte) {
		font := &Font{
			matrix:      defaultMatrix,
			names:       []string{"g", "A", "acute"},
			charstrings: [][]byte{cs, sub, glyphAcute},
			byName:      map[string]GID{"g": 0, "A": 1, "acute": 2},
			subrs:       [][]byte{sub, cs, nil, tb.CS(tb.Return)},
		}
		g, err := font.Outline(0)
		if g.Ops > maxGlyphOps+1 || len(g.Segs) > maxGlyphSegments {
			t.Fatalf("past the limits: %d ops, %d segments", g.Ops, len(g.Segs))
		}
		if err == nil {
			checkPoints(t, g)
		}
	})
}

// checkPoints fails on a built glyph point outside ±maxCoord or NaN.
func checkPoints(t *testing.T, g Glyph) {
	t.Helper()
	for _, s := range g.Segs {
		for _, p := range s.P {
			if !finiteCoord(p.X) || !finiteCoord(p.Y) {
				t.Fatalf("point %v outside ±2^20", p)
			}
		}
	}
}

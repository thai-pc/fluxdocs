package psfont

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	tb "github.com/thai-pc/fluxdocs/core/internal/psfont/t1build"
	"github.com/thai-pc/fluxdocs/internal/reltime"
)

// TestDecryptKnownAnswer pins the Type 1 cipher (Ch. 7) with vectors computed
// by hand, so an encrypter and a decrypter cannot be wrong symmetrically:
//
//	eexec, r0 = 55665 = 0xD971, cipher bytes 00 00 00 00:
//	  p0 = 00 ^ 0xD9 = 0xD9; r1 = (0 + 55665)·52845 + 22719 mod 65536
//	     = 2941639644 mod 65536 = 56284 = 0xDBDC
//	  p1 = 0xDB; r2 = 56284·52845 + 22719 = 2974350699 mod 65536 = 64875 = 0xFD6B
//	  p2 = 0xFD; r3 = 64875·52845 + 22719 = 3428342094 mod 65536 = 22862 = 0x594E
//	  p3 = 0x59
//	charstring, r0 = 4330 = 0x10EA, cipher bytes 00 00:
//	  p0 = 0x10; r1 = 4330·52845 + 22719 = 228841569 mod 65536 = 55393 = 0xD861
//	  p1 = 0xD8
func TestDecryptKnownAnswer(t *testing.T) {
	for _, tt := range []struct {
		key  uint16
		in   []byte
		want []byte
	}{
		{eexecKey, []byte{0, 0, 0, 0}, []byte{0xD9, 0xDB, 0xFD, 0x59}},
		{charstringKey, []byte{0, 0}, []byte{0x10, 0xD8}},
	} {
		got := make([]byte, len(tt.in))
		decrypt(got, tt.in, tt.key)
		if !bytes.Equal(got, tt.want) {
			t.Errorf("decrypt(%x, %d) = %x, want %x", tt.in, tt.key, got, tt.want)
		}
		// The builder's encrypter must invert it.
		if back := tb.Encrypt(tt.want, tt.key, nil); !bytes.Equal(back, tt.in) {
			t.Errorf("tb.Encrypt(%x, %d) = %x, want %x", tt.want, tt.key, back, tt.in)
		}
	}
}

// Glyphs of the test font, with outlines computable by hand.
var (
	// square: hsbw puts the current point at (50, 0); the square is
	// (100,100)–(600,600).
	glyphSquare = tb.CS(50, 700, tb.HSBW, 50, 100, tb.RMoveTo, 500, 0, tb.RLineTo, 0, 500, tb.RLineTo, -500, 0, tb.RLineTo, tb.ClosePath, tb.EndChar)
	wantSquare  = []Segment{
		{Op: MoveTo, P: [3]Point{{100, 100}}},
		{Op: LineTo, P: [3]Point{{600, 100}}},
		{Op: LineTo, P: [3]Point{{600, 600}}},
		{Op: LineTo, P: [3]Point{{100, 600}}},
	}
	// hole: a 600 square and a reversed 200 square inside it. Type 1
	// closepath leaves the current point at (100, 700), so the inner
	// rmoveto is relative to it.
	glyphHole = tb.CS(0, 800, tb.HSBW, 100, 100, tb.RMoveTo, 600, tb.HLineTo, 600, tb.VLineTo, -600, tb.HLineTo, tb.ClosePath,
		200, -400, tb.RMoveTo, 200, tb.VLineTo, 200, tb.HLineTo, -200, tb.VLineTo, tb.ClosePath, tb.EndChar)
	wantHole = []Segment{
		{Op: MoveTo, P: [3]Point{{100, 100}}}, {Op: LineTo, P: [3]Point{{700, 100}}},
		{Op: LineTo, P: [3]Point{{700, 700}}}, {Op: LineTo, P: [3]Point{{100, 700}}},
		{Op: MoveTo, P: [3]Point{{300, 300}}}, {Op: LineTo, P: [3]Point{{300, 500}}},
		{Op: LineTo, P: [3]Point{{500, 500}}}, {Op: LineTo, P: [3]Point{{500, 300}}},
	}
	// subr: the square's sides through subroutines 3 and 4 (4 calls 3).
	subr3      = tb.CS(500, 0, tb.RLineTo, tb.Return)
	subr4      = tb.CS(0, 500, tb.RLineTo, 3, tb.CallSubr, tb.Return)
	glyphSubrs = tb.CS(50, 700, tb.HSBW, 50, 100, tb.RMoveTo, 3, tb.CallSubr, 0, 500, tb.RLineTo, -500, 0, tb.RLineTo, tb.ClosePath, tb.EndChar)
	glyphNest  = tb.CS(50, 700, tb.HSBW, 50, 100, tb.RMoveTo, 500, 0, tb.RLineTo, 4, tb.CallSubr, tb.ClosePath, tb.EndChar)
	wantNest   = []Segment{
		{Op: MoveTo, P: [3]Point{{100, 100}}}, {Op: LineTo, P: [3]Point{{600, 100}}},
		{Op: LineTo, P: [3]Point{{600, 600}}}, {Op: LineTo, P: [3]Point{{1100, 600}}},
	}
	// flex (Ch. 8): from (0,0) a flex through 7 rmoveto points, then the
	// standard OtherSubr 0 epilogue.
	glyphFlex = tb.CS(0, 1000, tb.HSBW, 0, 0, tb.RMoveTo, 100, 0, tb.RLineTo,
		0, 1, tb.CallOtherSubr, // OtherSubr 1: start flex
		200, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // reference point (300, 0)
		0, 10, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (300, 10)
		100, 10, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (400, 20)
		100, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (500, 20) joint
		100, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (600, 20)
		100, -10, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (700, 10)
		0, -10, tb.RMoveTo, 0, 2, tb.CallOtherSubr, // (700, 0) end
		50, 700, 0, 3, 0, tb.CallOtherSubr, tb.Pop, tb.Pop, tb.SetCurrentPoint,
		0, 100, tb.RLineTo, tb.ClosePath, tb.EndChar)
	wantFlex = []Segment{
		{Op: MoveTo, P: [3]Point{{0, 0}}},
		{Op: LineTo, P: [3]Point{{100, 0}}},
		{Op: CubeTo, P: [3]Point{{300, 10}, {400, 20}, {500, 20}}},
		{Op: CubeTo, P: [3]Point{{600, 20}, {700, 10}, {700, 0}}},
		{Op: LineTo, P: [3]Point{{700, 100}}},
	}
	// hint replacement (OtherSubr 3) then a line; subr 5 holds hints only.
	subr5     = tb.CS(0, 50, tb.HStem, 10, 20, tb.VStem, tb.Return)
	glyphHint = tb.CS(0, 500, tb.HSBW, 5, 1, 3, tb.CallOtherSubr, tb.Pop, tb.CallSubr, 10, 10, tb.RMoveTo, 100, 0, tb.RLineTo, 0, 100, tb.RLineTo, tb.ClosePath, tb.EndChar)
	wantHint  = []Segment{
		{Op: MoveTo, P: [3]Point{{10, 10}}}, {Op: LineTo, P: [3]Point{{110, 10}}}, {Op: LineTo, P: [3]Point{{110, 110}}},
	}
	// div: 1001 / 2 = 500.5 as an rlineto distance.
	glyphDiv = tb.CS(0, 600, tb.HSBW, 0, 0, tb.RMoveTo, 1001, 2, tb.Div, 0, tb.RLineTo, 0, 300, tb.RLineTo, tb.ClosePath, tb.EndChar)
	wantDiv  = []Segment{
		{Op: MoveTo, P: [3]Point{{0, 0}}}, {Op: LineTo, P: [3]Point{{500.5, 0}}}, {Op: LineTo, P: [3]Point{{500.5, 300}}},
	}
	// seac: base A (StandardEncoding 65), accent acute (StandardEncoding
	// 0302 = 194). The composite's hsbw has sbx 20, the accent's asb is 30,
	// adx 150, ady 700: the accent's origin goes to (20 + 150 − 30, 700) =
	// (140, 700), so its sidebearing point (30, 0) lands at (170, 700).
	glyphA      = tb.CS(20, 600, tb.HSBW, 0, 0, tb.RMoveTo, 500, 0, tb.RLineTo, 0, 600, tb.RLineTo, tb.ClosePath, tb.EndChar)
	glyphAcute  = tb.CS(30, 300, tb.HSBW, 0, 0, tb.RMoveTo, 100, 0, tb.RLineTo, 0, 100, tb.RLineTo, tb.ClosePath, tb.EndChar)
	glyphAacute = tb.CS(20, 600, tb.HSBW, 30, 150, 700, 65, 194, tb.Seac)
	wantAacute  = []Segment{
		{Op: MoveTo, P: [3]Point{{20, 0}}}, {Op: LineTo, P: [3]Point{{520, 0}}}, {Op: LineTo, P: [3]Point{{520, 600}}},
		{Op: MoveTo, P: [3]Point{{170, 700}}}, {Op: LineTo, P: [3]Point{{270, 700}}}, {Op: LineTo, P: [3]Point{{270, 800}}},
	}
	// curves: vhcurveto and hvcurveto.
	glyphCurves = tb.CS(0, 500, tb.HSBW, 0, 0, tb.RMoveTo, 100, 50, 100, 100, tb.VHCurveTo, 100, 50, 100, 100, tb.HVCurveTo, tb.EndChar)
	wantCurves  = []Segment{
		{Op: MoveTo, P: [3]Point{{0, 0}}},
		{Op: CubeTo, P: [3]Point{{0, 100}, {50, 200}, {150, 200}}},
		{Op: CubeTo, P: [3]Point{{250, 200}, {300, 300}, {300, 400}}},
	}
)

// testFont is the hand-built font the table tests share.
func testFont() tb.Font {
	return tb.Font{
		Name:     "FluxTest",
		Encoding: map[int]string{0x41: "square", 0x42: "hole", 0x43: "Aacute", 0x61: "A"},
		Subrs:    [][]byte{tb.CS(tb.Return), tb.CS(tb.Return), tb.CS(tb.Return), subr3, subr4, subr5},
		Glyphs: []tb.Glyph{
			{Name: ".notdef", CS: tb.CS(0, 250, tb.HSBW, tb.EndChar)},
			{Name: "square", CS: glyphSquare},
			{Name: "hole", CS: glyphHole},
			{Name: "subrs", CS: glyphSubrs},
			{Name: "nest", CS: glyphNest},
			{Name: "flex", CS: glyphFlex},
			{Name: "hint", CS: glyphHint},
			{Name: "div", CS: glyphDiv},
			{Name: "A", CS: glyphA},
			{Name: "acute", CS: glyphAcute},
			{Name: "Aacute", CS: glyphAacute},
			{Name: "curves", CS: glyphCurves},
		},
	}
}

func mustParse(t *testing.T, data []byte) *Font {
	t.Helper()
	f, err := ParseType1(data, Options{})
	if err != nil {
		t.Fatalf("ParseType1: %v", err)
	}
	return f
}

func outline(t *testing.T, f *Font, name string) Glyph {
	t.Helper()
	gid, ok := f.GlyphByName(name)
	if !ok {
		t.Fatalf("no glyph %q", name)
	}
	g, err := f.Outline(gid)
	if err != nil {
		t.Fatalf("Outline(%q): %v", name, err)
	}
	return g
}

func TestType1Outlines(t *testing.T) {
	f := mustParse(t, testFont().Bytes())
	for _, tt := range []struct {
		name  string
		want  []Segment
		width float64
	}{
		{"square", wantSquare, 700},
		{"hole", wantHole, 800},
		{"subrs", wantSquare, 700},
		{"nest", wantNest, 700},
		{"flex", wantFlex, 1000},
		{"hint", wantHint, 500},
		{"div", wantDiv, 600},
		{"Aacute", wantAacute, 600},
		{"curves", wantCurves, 500},
		{".notdef", nil, 250},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := outline(t, f, tt.name)
			if !reflect.DeepEqual(g.Segs, tt.want) {
				t.Errorf("segments\n got %v\nwant %v", g.Segs, tt.want)
			}
			if g.Width != tt.width {
				t.Errorf("width = %v, want %v", g.Width, tt.width)
			}
			if g.Ops == 0 {
				t.Error("Ops = 0")
			}
		})
	}
}

// TestType1Variants parses the same font in every container form.
func TestType1Variants(t *testing.T) {
	base := testFont()
	hex := base
	hex.Hex = true
	pfb := base
	pfb.PFB = true
	pfbHex := hex
	pfbHex.PFB = true
	spell := base
	spell.RD, spell.NP, spell.ND = "-|", "|", "|-"
	noIV := base
	noIV.LenIV, noIV.LenIVSet = -1, true
	iv0 := base
	iv0.LenIV, iv0.LenIVSet = 0, true
	iv16 := base
	iv16.LenIV, iv16.LenIVSet = 16, true
	for name, fnt := range map[string]tb.Font{
		"binary": base, "hex": hex, "pfb": pfb, "pfb hex": pfbHex, "RD spellings": spell,
		"lenIV -1": noIV, "lenIV 0": iv0, "lenIV 16": iv16,
	} {
		t.Run(name, func(t *testing.T) {
			f := mustParse(t, fnt.Bytes())
			if g := outline(t, f, "square"); !reflect.DeepEqual(g.Segs, wantSquare) {
				t.Errorf("square = %v", g.Segs)
			}
			if f.NumGlyphs() != len(base.Glyphs) {
				t.Errorf("NumGlyphs = %d, want %d", f.NumGlyphs(), len(base.Glyphs))
			}
		})
	}
}

func TestType1BuiltinEncodingAndMatrix(t *testing.T) {
	f := mustParse(t, testFont().Bytes())
	enc, ok := f.BuiltinEncoding()
	if !ok || enc[0x41] != "square" || enc[0x43] != "Aacute" || enc[0x44] != ".notdef" && enc[0x44] != "" {
		t.Errorf("BuiltinEncoding = %v, %v", enc[0x40:0x45], ok)
	}
	if m := f.Matrix(); m != defaultMatrix {
		t.Errorf("Matrix = %v", m)
	}

	std := testFont()
	std.Encoding = nil
	std.Matrix = "[0.00048828125 0 0 0.00048828125 0 0]"
	f = mustParse(t, std.Bytes())
	if enc, ok := f.BuiltinEncoding(); !ok || enc[0101] != "A" || enc[0341] != "AE" {
		t.Errorf("StandardEncoding builtin = %q %q, %v", enc[0101], enc[0341], ok)
	}
	if m := f.Matrix(); m != [6]float64{1.0 / 2048, 0, 0, 1.0 / 2048, 0, 0} {
		t.Errorf("Matrix = %v", m)
	}

	for _, bad := range []string{"[1 0 0 1 0 0 0]", "[2 0 0 2 0 0]", "[0 0 0 0 0 0]", "[0.001 0 0 x 0 0]"} {
		fnt := testFont()
		fnt.Matrix = bad
		if m := mustParse(t, fnt.Bytes()).Matrix(); m != defaultMatrix {
			t.Errorf("FontMatrix %s = %v, want the default", bad, m)
		}
	}
}

// TestType1Limits checks each §5.1 bound at the limit (built) and one past it
// (the glyph or font dropped, never a panic).
func TestType1Limits(t *testing.T) {
	build := func(glyph []byte, subrs ...[]byte) (Glyph, error) {
		fnt := tb.Font{Subrs: subrs, Glyphs: []tb.Glyph{{Name: "g", CS: glyph}}}
		f := mustParse(t, fnt.Bytes())
		return f.Outline(0)
	}
	hsbw := tb.CS(0, 500, tb.HSBW)
	repeat := func(n int, b []byte) []byte { return bytes.Repeat(b, n) }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	// Subroutine nesting: a chain of n subrs, each calling the next.
	chain := func(n int) [][]byte {
		subrs := make([][]byte, n)
		for i := 0; i < n-1; i++ {
			subrs[i] = tb.CS(i+1, tb.CallSubr, tb.Return)
		}
		subrs[n-1] = tb.CS(tb.Return)
		return subrs
	}
	if _, err := build(cat(hsbw, tb.CS(0, tb.CallSubr, tb.EndChar)), chain(maxSubrDepth)...); err != nil {
		t.Errorf("subr nesting %d: %v", maxSubrDepth, err)
	}
	if _, err := build(cat(hsbw, tb.CS(0, tb.CallSubr, tb.EndChar)), chain(maxSubrDepth+1)...); !errors.Is(err, errLimit) {
		t.Errorf("subr nesting %d: err = %v, want errLimit", maxSubrDepth+1, err)
	}
	// A self-recursive subr ends at the nesting limit.
	if _, err := build(cat(hsbw, tb.CS(0, tb.CallSubr, tb.EndChar)), tb.CS(0, tb.CallSubr, tb.Return)); !errors.Is(err, errLimit) {
		t.Errorf("self-recursive subr: err = %v, want errLimit", err)
	}

	// Operand stack.
	if _, err := build(cat(hsbw, repeat(maxStack, tb.CS(1)), tb.CS(tb.HStem, tb.EndChar))); err != nil {
		t.Errorf("stack %d: %v", maxStack, err)
	}
	if _, err := build(cat(hsbw, repeat(maxStack+1, tb.CS(1)), tb.CS(tb.HStem, tb.EndChar))); !errors.Is(err, errLimit) {
		t.Errorf("stack %d: err = %v, want errLimit", maxStack+1, err)
	}

	// Executed operators: hsbw + n hstem + endchar.
	if g, err := build(cat(hsbw, repeat(maxGlyphOps-2, tb.CS(tb.HStem)), tb.CS(tb.EndChar))); err != nil || g.Ops != maxGlyphOps {
		t.Errorf("ops %d: ops %d, %v", maxGlyphOps, g.Ops, err)
	}
	if g, err := build(cat(hsbw, repeat(maxGlyphOps-1, tb.CS(tb.HStem)), tb.CS(tb.EndChar))); !errors.Is(err, errLimit) || g.Ops != maxGlyphOps+1 {
		t.Errorf("ops %d: ops %d, err = %v, want errLimit", maxGlyphOps+1, g.Ops, err)
	}

	// Segments: the implied moveto, then n−1 lines.
	if _, err := build(cat(hsbw, repeat(maxGlyphSegments-1, tb.CS(1, 0, tb.RLineTo)), tb.CS(tb.EndChar))); err != nil {
		t.Errorf("segments %d: %v", maxGlyphSegments, err)
	}
	if _, err := build(cat(hsbw, repeat(maxGlyphSegments, tb.CS(1, 0, tb.RLineTo)), tb.CS(tb.EndChar))); !errors.Is(err, errLimit) {
		t.Errorf("segments %d: err = %v, want errLimit", maxGlyphSegments+1, err)
	}

	// Coordinates.
	if _, err := build(cat(hsbw, tb.CS(maxCoord, 0, tb.RMoveTo, tb.EndChar))); err != nil {
		t.Errorf("coordinate 2^20: %v", err)
	}
	if _, err := build(cat(hsbw, tb.CS(maxCoord+1, 0, tb.RMoveTo, tb.EndChar))); !errors.Is(err, errLimit) {
		t.Errorf("coordinate 2^20+1: err = %v, want errLimit", err)
	}

	// Malformed: div by zero, callothersubr n > stack, stack underflow, pop
	// with nothing to pop, an undefined subr.
	for name, cs := range map[string][]byte{
		"div by zero":         cat(hsbw, tb.CS(1, 0, tb.Div, tb.EndChar)),
		"othersubr n > stack": cat(hsbw, tb.CS(1, 5, 9, tb.CallOtherSubr, tb.EndChar)),
		"underflow":           cat(hsbw, tb.CS(1, tb.RLineTo, tb.EndChar)),
		"pop empty":           cat(hsbw, tb.CS(tb.Pop, tb.EndChar)),
		"undefined subr":      cat(hsbw, tb.CS(7, tb.CallSubr, tb.EndChar)),
		"truncated number":    cat(hsbw, []byte{255, 0, 0}),
	} {
		if _, err := build(cs); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}

	// A flex with 3 points is discarded; the glyph keeps its other segments.
	g, err := build(cat(hsbw, tb.CS(0, 0, tb.RMoveTo, 100, 0, tb.RLineTo, 0, 1, tb.CallOtherSubr,
		10, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 10, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 10, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr,
		50, 130, 0, 3, 0, tb.CallOtherSubr, tb.Pop, tb.Pop, tb.SetCurrentPoint, 0, 100, tb.RLineTo, tb.EndChar)))
	want := []Segment{{Op: MoveTo}, {Op: LineTo, P: [3]Point{{100, 0}}}, {Op: LineTo, P: [3]Point{{130, 100}}}}
	if err != nil || !reflect.DeepEqual(g.Segs, want) {
		t.Errorf("3-point flex: %v, %v; want %v", g.Segs, err, want)
	}
	// A flex never ended draws nothing of itself.
	g, err = build(cat(hsbw, tb.CS(0, 0, tb.RMoveTo, 100, 0, tb.RLineTo, 0, 1, tb.CallOtherSubr, 10, 0, tb.RMoveTo, tb.EndChar)))
	if err != nil || len(g.Segs) != 2 {
		t.Errorf("unterminated flex: %v, %v", g.Segs, err)
	}

}

func TestType1SeacRules(t *testing.T) {
	fnt := tb.Font{Glyphs: []tb.Glyph{
		{Name: "A", CS: glyphA},
		{Name: "acute", CS: tb.CS(0, 300, tb.HSBW, 0, 0, 0, 65, 65, tb.Seac)}, // a seac component that seacs
		{Name: "Aacute", CS: tb.CS(20, 600, tb.HSBW, 0, 0, 0, 65, 194, tb.Seac)},
		{Name: "missing", CS: tb.CS(20, 600, tb.HSBW, 0, 0, 0, 65, 66, tb.Seac)}, // B is absent
		// adx far past the coordinate bound: the accent lands outside ±2^20.
		{Name: "far", CS: tb.CS(20, 600, tb.HSBW, 0, 2000000000, 0, 65, 65, tb.Seac)},
	}}
	f := mustParse(t, fnt.Bytes())
	far, _ := f.GlyphByName("far")
	if _, err := f.Outline(far); !errors.Is(err, errLimit) {
		t.Errorf("far accent: err = %v, want errLimit", err)
	}
	for _, name := range []string{"Aacute", "missing"} {
		gid, _ := f.GlyphByName(name)
		if _, err := f.Outline(gid); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}
}

func TestType1StructuralLimits(t *testing.T) {
	// lenIV out of range rejects the font.
	for _, iv := range []int{17, -2, 10000} {
		fnt := testFont()
		fnt.LenIV, fnt.LenIVSet = iv, true
		if _, err := ParseType1(fnt.Bytes(), Options{}); err == nil {
			t.Errorf("lenIV %d accepted", iv)
		}
	}
	// No eexec, no CharStrings.
	for name, data := range map[string][]byte{
		"empty":    nil,
		"no eexec": []byte("%!PS-AdobeFont-1.0: X\n/FontMatrix [0.001 0 0 0.001 0 0] def\n"),
		"no chars": append([]byte("currentfile eexec\n"), tb.Encrypt([]byte("dup /Private 1 dict"), eexecKey, []byte{1, 2, 3, 4})...),
		"huge":     make([]byte, maxProgramBytes+1),
	} {
		if _, err := ParseType1(data, Options{}); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// A refused charge aborts the parse.
	if _, err := ParseType1(testFont().Bytes(), Options{Charge: func(int64) bool { return false }}); !errors.Is(err, errLimit) {
		t.Errorf("refused charge: err = %v, want errLimit", err)
	}
	// Charges add up to the decoded copies: the eexec part and the charstrings.
	var charged int64
	if _, err := ParseType1(testFont().Bytes(), Options{Charge: func(n int64) bool { charged += n; return true }}); err != nil || charged == 0 {
		t.Errorf("charged %d, %v", charged, err)
	}

	// PFB lengths of 0xFFFFFFFF are clamped to the data.
	pfb := testFont()
	pfb.PFB = true
	data := pfb.Bytes()
	binary.LittleEndian.PutUint32(data[2:6], 0xFFFFFFFF)
	if _, err := ParseType1(data, Options{}); err == nil {
		t.Error("a first segment swallowing the file still parsed (no eexec part should remain)")
	}
	// Hex eexec of odd length: the last nibble is dropped.
	hex := testFont()
	hex.Hex = true
	h := string(hex.Bytes())
	i := strings.Index(h, "0000000000000000000000000000000000000000000000000000000000000000")
	odd := h[:i-1] + "7\n" + h[i:]
	if _, err := ParseType1([]byte(odd), Options{}); err != nil {
		t.Errorf("odd-length hex: %v", err)
	}
}

func TestType1EntryCounts(t *testing.T) {
	// /CharStrings past maxCharStrings: the first maxCharStrings are kept.
	glyphs := make([]tb.Glyph, maxCharStrings+1)
	for i := range glyphs {
		glyphs[i] = tb.Glyph{Name: "g" + strconv.Itoa(i), CS: tb.CS(tb.EndChar)}
	}
	f := mustParse(t, tb.Font{Glyphs: glyphs}.Bytes())
	if f.NumGlyphs() != maxCharStrings {
		t.Errorf("NumGlyphs = %d, want %d", f.NumGlyphs(), maxCharStrings)
	}
	if _, ok := f.GlyphByName("g" + strconv.Itoa(maxCharStrings)); ok {
		t.Error("glyph past the bound is reachable")
	}

	// Subr indices: maxSubrs−1 is kept, maxSubrs dropped.
	s := &scanner{b: []byte("65537 array dup 65535 1 RD x NP dup 65536 1 RD y NP ND")}
	subrs, ok := readSubrs(s)
	if !ok || len(subrs) != maxSubrs || string(subrs[maxSubrs-1]) != "x" {
		t.Errorf("readSubrs: len %d, %v", len(subrs), ok)
	}
	// Indices at or past the declared size are dropped: the table never
	// grows past the declared array.
	for _, tt := range []struct {
		src  string
		want int
	}{
		{"1 array dup 65535 1 RD x NP ND", 0},
		{"2 array dup 1 1 RD x NP dup 2 1 RD y NP ND", 2},
		{"0 array ND", 0},
	} {
		subrs, ok := readSubrs(&scanner{b: []byte(tt.src)})
		if !ok || len(subrs) != tt.want {
			t.Errorf("readSubrs(%q): len %d, %v; want %d, true", tt.src, len(subrs), ok, tt.want)
		}
	}
	if _, ok := readSubrs(&scanner{b: []byte("-1 array")}); ok {
		t.Error("readSubrs accepted a negative array size")
	}

	// Charstring length: maxCharstringLen bytes are kept, one more is stored
	// empty; a length past the buffer ends the scan.
	for _, tt := range []struct {
		n    int
		want int
		ok   bool
	}{{maxCharstringLen, maxCharstringLen, true}, {maxCharstringLen + 1, 0, true}} {
		b := append([]byte(strconv.Itoa(tt.n)+" RD "), make([]byte, tt.n)...)
		got, ok := binaryEntry(&scanner{b: b})
		if ok != tt.ok || len(got) != tt.want {
			t.Errorf("binaryEntry(%d) = %d bytes, %v", tt.n, len(got), ok)
		}
	}
	if _, ok := binaryEntry(&scanner{b: []byte("10 RD abc")}); ok {
		t.Error("binaryEntry past the buffer succeeded")
	}
	// A length whose sum with the position overflows is past the buffer too,
	// not a negative position (B1).
	for _, b := range []string{"9223372036854775807 RD x", "9223372036854775807 RD", "1 RD"} {
		if _, ok := binaryEntry(&scanner{b: []byte(b)}); ok {
			t.Errorf("binaryEntry(%q) succeeded", b)
		}
	}
	s = &scanner{b: []byte("2 array dup 0 1 RD x NP dup 1 9223372036854775807 RD y NP ND")}
	if subrs, _ := readSubrs(s); len(subrs) != 1 || string(subrs[0]) != "x" {
		t.Errorf("readSubrs with a MaxInt64 length: %q", subrs)
	}
	for _, priv := range []string{
		"/lenIV -1 def /Subrs 1 array dup 0 9223372036854775807 RD x NP /CharStrings 1 dict dup begin /a 1 RD x ND end",
		"/lenIV -1 def /CharStrings 1 dict dup begin /a 9223372036854775807 RD x ND end",
	} {
		data := append([]byte("%!PS-AdobeFont-1.0\ncurrentfile eexec\n"), tb.Encrypt([]byte(priv), eexecKey, []byte{1, 2, 3, 4})...)
		if _, err := ParseType1(data, Options{}); err != nil && !errors.Is(err, errMalformed) {
			t.Errorf("ParseType1(%q) = %v, want nil or errMalformed", priv, err)
		}
	}
}

// eexecProgram wraps a plaintext private part in a minimal cleartext prefix
// and the eexec layer (4 fixed bytes), with charstrings stored as given.
func eexecProgram(private []byte) []byte {
	return append([]byte("%!PS-AdobeFont-1.0\ncurrentfile eexec\n"), tb.Encrypt(private, eexecKey, []byte{1, 2, 3, 4})...)
}

// secondCharStrings is a private part with two /CharStrings dicts: three
// glyphs, then one glyph "c" whose seac names A (GID 0) and B (GID 1) of the
// first dict. Reading the second dict into the shared name map once left A
// and B pointing past the one-entry charstrings (B2).
func secondCharStrings() []byte {
	seac := tb.CS(0, 500, tb.HSBW, 0, 0, 0, 65, 66, tb.Seac)
	priv := []byte("/lenIV -1 def /CharStrings 3 dict dup begin /A 1 RD \x0e ND /B 1 RD \x0e ND /C 1 RD \x0e ND end ")
	priv = append(priv, "/CharStrings 1 dict dup begin /c "+strconv.Itoa(len(seac))+" RD "...)
	priv = append(priv, seac...)
	return append(priv, " ND end"...)
}

func TestType1_SecondCharStringsIgnored(t *testing.T) {
	f := mustParse(t, eexecProgram(secondCharStrings()))
	if f.NumGlyphs() != 3 {
		t.Fatalf("NumGlyphs = %d, want 3 (the first /CharStrings)", f.NumGlyphs())
	}
	for name, want := range map[string]GID{"A": 0, "B": 1, "C": 2} {
		if g, ok := f.GlyphByName(name); !ok || g != want {
			t.Errorf("GlyphByName(%q) = %d, %v; want %d", name, g, ok, want)
		}
	}
	if _, ok := f.GlyphByName("c"); ok {
		t.Error("a glyph of the second /CharStrings is reachable")
	}
	for g := 0; g < f.NumGlyphs(); g++ {
		if _, err := f.Outline(GID(g)); err != nil {
			t.Errorf("Outline(%d): %v", g, err)
		}
	}
}

// TestType1_SeacComponentOutOfRange checks seac rejects a component whose
// name maps past the charstrings instead of indexing out of range.
func TestType1_SeacComponentOutOfRange(t *testing.T) {
	f := &Font{
		matrix:      defaultMatrix,
		names:       []string{"g"},
		charstrings: [][]byte{tb.CS(0, 500, tb.HSBW, 0, 0, 0, 65, 66, tb.Seac)},
		byName:      map[string]GID{"g": 0, "A": 5, "B": 1 << 31},
	}
	if _, err := f.Outline(0); !errors.Is(err, errMalformed) {
		t.Errorf("Outline = %v, want errMalformed", err)
	}
}

// glyphCounters draws a square after two counter-control calls (OtherSubrs
// 13 and 12, TN #5015) of 13 arguments each, with no pop after either.
var glyphCounters = func() []byte {
	args := []any{0, 500, tb.HSBW}
	for _, num := range []int{13, 12} {
		for i := 0; i < 13; i++ {
			args = append(args, 10)
		}
		args = append(args, 13, num, tb.CallOtherSubr)
	}
	return tb.CS(append(args, 0, 0, tb.RMoveTo, 100, 0, tb.RLineTo, 0, 100, tb.RLineTo, tb.ClosePath, tb.EndChar)...)
}()

// TestType1_OtherSubrResults checks what OtherSubrs leave for pop (S2, N7):
// counter control returns nothing and unpopped results do not accumulate
// across calls, and an OtherSubr 0 of the wrong arity still ends a flex.
func TestType1_OtherSubrResults(t *testing.T) {
	f := mustParse(t, tb.Font{Glyphs: []tb.Glyph{
		{Name: "counters", CS: glyphCounters},
		{Name: "badflexend", CS: tb.CS(0, 500, tb.HSBW, 0, 0, tb.RMoveTo, 10, 0, tb.RLineTo,
			0, 1, tb.CallOtherSubr, 5, 1, 0, tb.CallOtherSubr, 100, 100, tb.RMoveTo, 50, 0, tb.RLineTo, tb.ClosePath, tb.EndChar)},
		{Name: "counterpop", CS: tb.CS(0, 500, tb.HSBW, 7, 1, 13, tb.CallOtherSubr, tb.Pop, tb.EndChar)},
	}}.Bytes())
	g, err := f.Outline(0)
	if err != nil || len(g.Segs) != 3 {
		t.Errorf("counter-control glyph: %d segments, %v; want 3, nil", len(g.Segs), err)
	}
	g, err = f.Outline(1)
	moves := 0
	for _, s := range g.Segs {
		if s.Op == MoveTo {
			moves++
		}
	}
	if err != nil || moves != 2 {
		t.Errorf("after a 1-argument OtherSubr 0: %d subpaths, %v; want 2 (the rmoveto moves, not a flex point)", moves, err)
	}
	if _, err := f.Outline(2); !errors.Is(err, errMalformed) {
		t.Errorf("pop after counter control = %v, want errMalformed (nothing to pop)", err)
	}
}

// TestScannerDoesNotCopyTokens checks a long run of non-delimiters is scanned
// without being copied (N5): tokens are subslices of the program, and only
// the names a font keeps become strings.
func TestScannerDoesNotCopyTokens(t *testing.T) {
	huge := bytes.Repeat([]byte{'x'}, 8<<20)
	clear := append(append([]byte("/FontMatrix [0.001 0 0 0.001 0 0] /Encoding "), huge...), " def "...)
	private := append(append([]byte("/lenIV -1 def "), huge...), " /CharStrings 1 dict dup begin /a 1 RD \x0e ND end"...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f := &Font{matrix: defaultMatrix}
	f.parseCleartext(clear)
	err := f.parsePrivate(private, Options{})
	runtime.ReadMemStats(&after)
	if err != nil || f.NumGlyphs() != 1 {
		t.Fatalf("parsePrivate: %v, %d glyphs", err, f.NumGlyphs())
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("scanning two 8 MiB tokens allocated %d bytes, want < 1 MiB", n)
	}
}

// TestType1_NotdefArrayIsBuiltinEncoding checks an /Encoding array with no
// entries put is still the program's built-in encoding, every code unassigned
// (N6), not "no encoding" (which would fall back to StandardEncoding).
func TestType1_NotdefArrayIsBuiltinEncoding(t *testing.T) {
	f := &Font{}
	f.parseCleartext([]byte("/Encoding 256 array 0 1 255 {1 index exch /.notdef put} for readonly def"))
	enc, ok := f.BuiltinEncoding()
	if !ok {
		t.Fatal("an all-.notdef /Encoding array is not a built-in encoding")
	}
	if enc != ([256]string{}) {
		t.Errorf("encoding has assigned codes: %q", enc)
	}
	f = &Font{}
	f.parseCleartext([]byte("/FontName /X def"))
	if _, ok := f.BuiltinEncoding(); ok {
		t.Error("a program without /Encoding reports a built-in encoding")
	}
}

// repeatedSubrs is a private part of n "/Subrs 1 array dup 65535 0 RD  NP"
// definitions before one glyph. Each once reallocated a 65,536-entry table,
// uncharged: 493,444 repeats (16 MiB, a few KiB compressed) took minutes.
// With index "00000" (0, inside the declared size) the part is the same
// size and token for token the same shape: the benign reference.
func repeatedSubrs(n int) []byte { return repeatedSubrsAt(n, "65535") }

func repeatedSubrsAt(n int, index string) []byte {
	priv := append([]byte("/lenIV -1 def\n"), bytes.Repeat([]byte("/Subrs 1 array dup "+index+" 0 RD  NP\n"), n)...)
	return append(priv, "/CharStrings 1 dict dup begin /a 1 RD \x0e ND end"...)
}

// TestType1_RepeatedSubrsAreBounded checks only the first /Subrs that parses
// is read and no table outgrows its declared size (B-new): a 16 MiB private
// part of repeated /Subrs parses about as fast as the same part with an index
// inside the declared size (bounded at 3×, measured against it in the same
// run; reltime) and allocates about its own size.
func TestType1_RepeatedSubrsAreBounded(t *testing.T) {
	data := eexecProgram(repeatedSubrs(493444))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f, err := ParseType1(data, Options{})
	runtime.ReadMemStats(&after)
	if err != nil || f.NumGlyphs() != 1 {
		t.Fatalf("ParseType1: %v", err)
	}
	if len(f.subrs) != 0 {
		t.Errorf("%d subrs kept, want 0 (index 65535 is past the declared 1)", len(f.subrs))
	}
	// The program itself is ≈ 16 MiB and the parser makes one decrypted
	// copy of it; a reallocated table per /Subrs would add ≈ 1.5 MiB each.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 3*uint64(len(data))/2 {
		t.Errorf("parse allocated %d MiB for a %d MiB program", alloc>>20, len(data)>>20)
	}
	t.Run("cost against an in-range index", func(t *testing.T) {
		reltime.Skip(t)
		benign := eexecProgram(repeatedSubrsAt(493444, "00000"))
		if len(benign) != len(data) {
			t.Fatalf("reference program is %d bytes, want %d", len(benign), len(data))
		}
		parse := func(b []byte) func() {
			return func() {
				if _, err := ParseType1(b, Options{}); err != nil {
					t.Fatal(err)
				}
			}
		}
		reps := reltime.Reps(reltime.CPU, reltime.CPU.Target(), parse(data))
		r := reltime.Compare(reltime.Runs, reltime.CPU, reltime.Repeat(reps, parse(benign)), reltime.Repeat(reps, parse(data)))
		reltime.Check(t, "repeated out-of-range /Subrs against in-range ones", r, 3)
	})
	f = mustParse(t, eexecProgram([]byte("/lenIV -1 def /Subrs 1 array dup 0 1 RD \x0b NP /Subrs 2 array dup 1 1 RD \x0b NP /CharStrings 1 dict dup begin /a 1 RD \x0e ND end")))
	if len(f.subrs) != 1 {
		t.Errorf("the second /Subrs replaced the first: %d subrs, want 1", len(f.subrs))
	}
}

// TestType1_NonFiniteWidthRejected checks hsbw and sbw reject a width that is
// not finite or past ±maxCoord (a div chain), so it never reaches the
// advance.
func TestType1_NonFiniteWidthRejected(t *testing.T) {
	big := tb.CS(2000000000, 1, tb.Div)                                            // 2e9
	tiny := tb.CS(1, 2000000000, tb.Div)                                           // 5e-10
	huge := append(append(append([]byte(nil), big...), tiny...), tb.CS(tb.Div)...) // 4e18
	f := mustParse(t, tb.Font{Glyphs: []tb.Glyph{
		{Name: "hsbw", CS: append(append(tb.CS(0), huge...), tb.CS(tb.HSBW, tb.EndChar)...)},
		{Name: "sbw", CS: append(append(tb.CS(0, 0), huge...), tb.CS(0, tb.SBW, tb.EndChar)...)},
		{Name: "ok", CS: tb.CS(0, 500, tb.HSBW, tb.EndChar)},
	}}.Bytes())
	for g := GID(0); g < 2; g++ {
		if gl, err := f.Outline(g); !errors.Is(err, errLimit) {
			t.Errorf("glyph %d: width %v, err %v; want errLimit", g, gl.Width, err)
		}
	}
	if gl, err := f.Outline(2); err != nil || gl.Width != 500 {
		t.Errorf("finite width: %v, %v", gl.Width, err)
	}
}

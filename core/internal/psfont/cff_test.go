package psfont

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	cb "github.com/thai-pc/fluxdocs/core/internal/psfont/cffbuild"
	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// The hand-built CFF test font. Its Private DICT has nominalWidthX 100 and
// defaultWidthX 250, so a charstring width operand w gives width 100 + w
// and a glyph without one 250. Coordinates are glyph units.
var (
	// square: "600 100 100 rmoveto" carries the width (700) and starts at
	// (100, 100); the square is (100,100)–(600,600), wantSquare.
	cffSquare = cb.CS(600, 100, 100, cb.RMoveTo, 500, cb.HLineTo, 500, cb.VLineTo, -500, cb.HLineTo, cb.EndChar)
	// hole: hlineto and vlineto alternate; the second rmoveto starts the
	// counter (and closes the first subpath).
	cffHole     = cb.CS(700, 100, 100, cb.RMoveTo, 600, 600, -600, cb.HLineTo, 200, -400, cb.RMoveTo, 200, 200, -200, cb.VLineTo, cb.EndChar)
	wantCFFHole = []Segment{
		{Op: MoveTo, P: [3]Point{{100, 100}}}, {Op: LineTo, P: [3]Point{{700, 100}}},
		{Op: LineTo, P: [3]Point{{700, 700}}}, {Op: LineTo, P: [3]Point{{100, 700}}},
		{Op: MoveTo, P: [3]Point{{300, 300}}}, {Op: LineTo, P: [3]Point{{300, 500}}},
		{Op: LineTo, P: [3]Point{{500, 500}}}, {Op: LineTo, P: [3]Point{{500, 300}}},
	}
	// subrs: the square's edges come from local subrs 0 and 1 (operands
	// −107 and −106 with bias 107) and global subr 0; nest calls local subr
	// 2, which calls local 0 and global 0 in turn.
	cffLocalSubrs = [][]byte{cb.CS(500, cb.HLineTo, cb.Return), cb.CS(500, cb.VLineTo, cb.Return), cb.CS(-107, cb.CallSubr, -106, cb.CallSubr, -107, cb.CallGSubr, cb.Return)}
	cffGSubrs     = [][]byte{cb.CS(-500, cb.HLineTo, cb.Return)}
	cffSubrs      = cb.CS(600, 100, 100, cb.RMoveTo, -107, cb.CallSubr, -106, cb.CallSubr, -107, cb.CallGSubr, cb.EndChar)
	cffNest       = cb.CS(600, 100, 100, cb.RMoveTo, -105, cb.CallSubr, cb.EndChar)

	// curves: every curve operator once from (0, 0), no width operand
	// (width 250). The expected points are summed by hand in wantCFFCurves.
	cffCurves = cb.CS(0, 0, cb.RMoveTo,
		10, 20, 30, 40, 50, 60, cb.RRCurveTo, // (10,20) (40,60) (90,120)
		5, 10, 20, 30, 40, cb.HHCurveTo, // dy1 5: (100,125) (120,155) (160,155)
		6, 10, 20, 30, 40, cb.VVCurveTo, // dx1 6: (166,165) (186,195) (186,235)
		10, 20, 30, 40, cb.HVCurveTo, // (196,235) (216,265) (216,305)
		10, 20, 30, 40, 50, cb.VHCurveTo, // dxf… dyf 50: (216,315) (236,345) (276,395)
		1, 2, 3, 4, 5, 6, 7, 8, cb.HVCurveTo, // (277,395) (279,398) (279,402); (279,407) (285,414) (293,414)
		1, 2, 3, 4, 5, 6, 7, 8, cb.RCurveLine, // (294,416) (297,420) (302,426); line (309,434)
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, cb.RLineCurve, // lines (310,436) (313,440); (318,446) (325,454) (334,464)
		cb.EndChar)
	wantCFFCurves = []Segment{
		{Op: MoveTo, P: [3]Point{{0, 0}}},
		{Op: CubeTo, P: [3]Point{{10, 20}, {40, 60}, {90, 120}}},
		{Op: CubeTo, P: [3]Point{{100, 125}, {120, 155}, {160, 155}}},
		{Op: CubeTo, P: [3]Point{{166, 165}, {186, 195}, {186, 235}}},
		{Op: CubeTo, P: [3]Point{{196, 235}, {216, 265}, {216, 305}}},
		{Op: CubeTo, P: [3]Point{{216, 315}, {236, 345}, {276, 395}}},
		{Op: CubeTo, P: [3]Point{{277, 395}, {279, 398}, {279, 402}}},
		{Op: CubeTo, P: [3]Point{{279, 407}, {285, 414}, {293, 414}}},
		{Op: CubeTo, P: [3]Point{{294, 416}, {297, 420}, {302, 426}}},
		{Op: LineTo, P: [3]Point{{309, 434}}},
		{Op: LineTo, P: [3]Point{{310, 436}}},
		{Op: LineTo, P: [3]Point{{313, 440}}},
		{Op: CubeTo, P: [3]Point{{318, 446}, {325, 454}, {334, 464}}},
	}

	// hint: 5 hstemhm pairs after the width (11 operands, odd), then a
	// hintmask whose 4 pairs are implied vstems: 9 stems, so each mask is 2
	// bytes. The mask bytes are 0x0e (endchar's code): reading one byte too
	// few ends the glyph early, one too many eats the next operator.
	cffHint = cb.CS(600, 0, 10, 20, 10, 40, 10, 60, 10, 80, 10, cb.HStemHM,
		0, 10, 20, 10, 40, 10, 60, 10, cb.HintMask, cb.Raw{0x0e, 0x0e},
		100, 100, cb.RMoveTo, 500, cb.HLineTo, cb.HintMask, cb.Raw{0x0e, 0x0e}, 500, cb.VLineTo,
		cb.CntrMask, cb.Raw{0x0e, 0x0e}, -500, cb.HLineTo, cb.EndChar)

	// The seac components: A is (0,0)–(500,300), acute (0,0)–(200,100);
	// Aacute places the accent's origin at (200, 500), so it covers
	// (200,500)–(400,600). Aacute4 is the same without a width operand.
	cffA          = cb.CS(500, 0, 0, cb.RMoveTo, 500, 300, -500, cb.HLineTo, cb.EndChar)
	cffAcute      = cb.CS(200, 0, 0, cb.RMoveTo, 200, 100, -200, cb.HLineTo, cb.EndChar)
	cffAacute     = cb.CS(500, 200, 500, 65, 194, cb.EndChar)
	cffAacute4    = cb.CS(200, 500, 65, 194, cb.EndChar)
	wantCFFAacute = []Segment{
		{Op: MoveTo, P: [3]Point{{0, 0}}}, {Op: LineTo, P: [3]Point{{500, 0}}},
		{Op: LineTo, P: [3]Point{{500, 300}}}, {Op: LineTo, P: [3]Point{{0, 300}}},
		{Op: MoveTo, P: [3]Point{{200, 500}}}, {Op: LineTo, P: [3]Point{{400, 500}}},
		{Op: LineTo, P: [3]Point{{400, 600}}}, {Op: LineTo, P: [3]Point{{200, 600}}},
	}
)

// cffTestFont is the non-CID-keyed test font.
func cffTestFont() cb.Font {
	return cb.Font{
		DefaultWidthX: 250,
		NominalWidthX: 100,
		Subrs:         cffLocalSubrs,
		GSubrs:        cffGSubrs,
		Encoding:      map[int]string{0x41: "square", 0x42: "hole", 0x43: "Aacute"},
		Glyphs: []cb.Glyph{
			{Name: ".notdef", CS: cb.CS(cb.EndChar)},
			{Name: "square", CS: cffSquare},
			{Name: "hole", CS: cffHole},
			{Name: "Aacute", CS: cffAacute},
			{Name: "subrs", CS: cffSubrs},
			{Name: "nest", CS: cffNest},
			{Name: "curves", CS: cffCurves},
			{Name: "hint", CS: cffHint},
			{Name: "A", CS: cffA},
			{Name: "acute", CS: cffAcute},
			{Name: "Aacute4", CS: cffAacute4},
		},
	}
}

func mustParseCFF(t *testing.T, data []byte) *Font {
	t.Helper()
	f, err := ParseCFF(data, Options{})
	if err != nil {
		t.Fatalf("ParseCFF: %v", err)
	}
	return f
}

func TestCFFStdStrings(t *testing.T) {
	// Anchors from TN #5176 Appendix A.
	for sid, name := range map[int]string{
		0: ".notdef", 1: "space", 34: "A", 59: "Z", 66: "a", 91: "z", 96: "exclamdown",
		109: "fi", 125: "acute", 137: "emdash", 138: "AE", 149: "germandbls", 170: "copyright",
		171: "Aacute", 228: "zcaron", 229: "exclamsmall", 274: "Asmall", 299: "Zsmall",
		300: "colonmonetary", 326: "zerosuperior", 346: "commainferior", 369: "Odieresissmall",
		378: "Ydieresissmall", 379: "001.000", 383: "Black", 390: "Semibold",
	} {
		if cffStdStrings[sid] != name {
			t.Errorf("SID %d = %q, want %q", sid, cffStdStrings[sid], name)
		}
	}
	// StandardEncoding names only ISOAdobe glyphs (SIDs 1–149).
	std := map[string]int{}
	for sid, n := range cffStdStrings[:isoAdobeLast+1] {
		std[n] = sid
	}
	for c := 0; c < 256; c++ {
		if n := std14.StandardName(byte(c)); n != "" {
			if sid, ok := std[n]; !ok || sid > 149 {
				t.Errorf("StandardEncoding code %d (%s): SID %d, %v", c, n, sid, ok)
			}
		}
	}
}

func TestCFFOutlines(t *testing.T) {
	f := mustParseCFF(t, cffTestFont().Bytes())
	for _, tt := range []struct {
		name  string
		want  []Segment
		width float64
	}{
		{"square", wantSquare, 700},
		{"hole", wantCFFHole, 800},
		{"subrs", wantSquare, 700},
		{"nest", wantSquare, 700},
		{"curves", wantCFFCurves, 250},
		{"hint", wantSquare, 700},
		{"Aacute", wantCFFAacute, 600},
		{"Aacute4", wantCFFAacute, 250},
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
			if g.Matrix != defaultMatrix {
				t.Errorf("matrix = %v", g.Matrix)
			}
			if g.Ops == 0 {
				t.Error("Ops = 0")
			}
		})
	}
}

func TestCFFFlex(t *testing.T) {
	mv := cb.CS(0, 0, cb.RMoveTo)
	cub := func(p ...float64) Segment {
		return Segment{Op: CubeTo, P: [3]Point{{p[0], p[1]}, {p[2], p[3]}, {p[4], p[5]}}}
	}
	start := Segment{Op: MoveTo}
	for _, tt := range []struct {
		name string
		cs   []byte
		want []Segment
	}{
		{"flex", cb.CS(10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 50, cb.Flex),
			[]Segment{start, cub(10, 20, 40, 60, 90, 120), cub(160, 200, 250, 300, 360, 420)}},
		// dx1 dx2 dy2 dx3 dx4 dx5 dx6: the second curve mirrors dy2.
		{"hflex", cb.CS(10, 20, 30, 40, 50, 60, 70, cb.HFlex),
			[]Segment{start, cub(10, 0, 30, 30, 70, 30), cub(120, 30, 180, 0, 250, 0)}},
		// dy6 = −(dy1 + dy2 + dy5) = −10 returns to y 0.
		{"hflex1", cb.CS(10, 5, 20, 10, 30, 40, 50, -5, 60, cb.HFlex1),
			[]Segment{start, cub(10, 5, 30, 15, 60, 15), cub(100, 15, 150, 10, 210, 0)}},
		// Σdx 150 > Σdy 0: d6 is dx6 and dy6 = −Σdy.
		{"flex1 horizontal", cb.CS(10, 5, 20, 10, 30, 0, 40, -10, 50, -5, 60, cb.Flex1),
			[]Segment{start, cub(10, 5, 30, 15, 60, 15), cub(100, 5, 150, 0, 210, 0)}},
		// Σdx 0 ≤ Σdy 150: d6 is dy6 and dx6 = −Σdx.
		{"flex1 vertical", cb.CS(5, 10, 10, 20, 0, 30, -10, 40, -5, 50, 60, cb.Flex1),
			[]Segment{start, cub(5, 10, 15, 30, 15, 60), cub(5, 100, 0, 150, 0, 210)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, err := buildT2(t, cb.Font{}, append(append(append([]byte(nil), mv...), tt.cs...), cb.CS(cb.EndChar)...))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g.Segs, tt.want) {
				t.Errorf("segments\n got %v\nwant %v", g.Segs, tt.want)
			}
		})
	}
}

// buildT2 builds glyph 1 (cs) of base with a .notdef added.
func buildT2(t *testing.T, base cb.Font, cs []byte) (Glyph, error) {
	t.Helper()
	base.Glyphs = []cb.Glyph{{Name: ".notdef", CS: cb.CS(cb.EndChar)}, {Name: "g", CS: cs}}
	return mustParseCFF(t, base.Bytes()).Outline(1)
}

func TestCFFArithmetic(t *testing.T) {
	for _, tt := range []struct {
		name string
		expr []any
		want float64
	}{
		{"div", []any{7, 2, cb.Div}, 3.5},
		{"add", []any{3, 4, cb.Add}, 7},
		{"sub", []any{3, 4, cb.Sub}, -1},
		{"mul", []any{3, 4, cb.Mul}, 12},
		{"neg", []any{5, cb.Neg}, -5},
		{"abs", []any{-5, cb.Abs}, 5},
		{"sqrt", []any{16, cb.Sqrt}, 4},
		{"exch", []any{3, 4, cb.Exch, cb.Sub}, 1},
		{"dup", []any{3, cb.Dup, cb.Add}, 6},
		{"index", []any{10, 20, 30, 2, cb.Index, cb.Exch, cb.Drop, cb.Exch, cb.Drop, cb.Add}, 20},
		{"index negative", []any{10, 20, -1, cb.Index, cb.Add, cb.Exch, cb.Drop}, 40},
		{"roll", []any{1, 2, 3, 3, 1, cb.Roll, cb.Drop, cb.Drop}, 3},
		{"roll back", []any{1, 2, 3, 3, -1, cb.Roll, cb.Drop, cb.Drop}, 2},
		{"ifelse true", []any{10, 20, 1, 2, cb.IfElse}, 10},
		{"ifelse false", []any{10, 20, 2, 1, cb.IfElse}, 20},
		{"eq", []any{3, 3, cb.Eq}, 1},
		{"and", []any{1, 0, cb.And}, 0},
		{"or", []any{1, 0, cb.Or}, 1},
		{"not", []any{0, cb.Not}, 1},
		{"random", []any{cb.Random}, t2Random},
		{"put get", []any{42, 31, cb.Put, 31, cb.Get}, 42},
		{"fixed", []any{cb.Fixed(1.5)}, 1.5},
		{"shortint", []any{-2000}, -2000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cs := cb.CS(append(append([]any{0, 0, cb.RMoveTo}, tt.expr...), 0, cb.RLineTo, cb.EndChar)...)
			g, err := buildT2(t, cb.Font{}, cs)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Segs) != 2 || g.Segs[1].P[0] != (Point{tt.want, 0}) {
				t.Errorf("segments %v, want a line to (%v, 0)", g.Segs, tt.want)
			}
		})
	}
}

func TestCFFCharsetsAndEncodings(t *testing.T) {
	for _, cf := range []int{0, 1, 2} {
		for _, ef := range []int{0, 1} {
			f := cffTestFont()
			f.CharsetFormat, f.EncodingFormat = cf, ef
			f.Supplements = map[int]string{0x61: "square", 0x62: "zcaron"}
			fnt := mustParseCFF(t, f.Bytes())
			for g, gl := range f.Glyphs {
				if got, ok := fnt.GlyphByName(gl.Name); !ok || got != GID(g) {
					t.Errorf("charset %d: GlyphByName(%q) = %d, %v; want %d", cf, gl.Name, got, ok, g)
				}
			}
			enc, ok := fnt.BuiltinEncoding()
			if !ok || enc[0x41] != "square" || enc[0x42] != "hole" || enc[0x43] != "Aacute" ||
				enc[0x61] != "square" || enc[0x62] != "zcaron" || enc[0x44] != "" {
				t.Errorf("charset %d encoding %d: built-in A..D %q, a b %q", cf, ef, enc[0x41:0x45], enc[0x61:0x63])
			}
		}
	}
	// No Encoding: the predefined Standard encoding.
	f := cffTestFont()
	f.Encoding = nil
	enc, ok := mustParseCFF(t, f.Bytes()).BuiltinEncoding()
	if !ok || enc != std14.StandardEncoding() {
		t.Errorf("predefined encoding 0: ok %v, A = %q", ok, enc[0x41])
	}
	// The predefined Expert and ExpertSubset charsets and Expert encoding
	// (TN #5176 Appendices B and C): spot checks at both ends of each
	// table, and GIDs past a charset unnamed.
	glyphs := func(n int) []cb.Glyph {
		g := make([]cb.Glyph, n)
		for i := range g {
			g[i] = cb.Glyph{Name: ".notdef", CS: cb.CS(cb.EndChar)}
		}
		return g
	}
	for _, tt := range []struct {
		charset int
		glyphs  int
		names   map[GID]string
	}{
		{1, len(expertCharset) + 2, map[GID]string{1: "space", 2: "exclamsmall", 15: "fraction", 165: "Ydieresissmall"}},
		{2, len(expertSubsetCharset) + 2, map[GID]string{1: "space", 2: "dollaroldstyle", 3: "dollarsuperior", 86: "commainferior"}},
	} {
		fnt := mustParseCFF(t, cb.Font{Glyphs: glyphs(tt.glyphs), PredefinedCharset: tt.charset, PredefinedEncoding: 1}.Bytes())
		for g, name := range tt.names {
			if got, ok := fnt.GlyphByName(name); !ok || got != g {
				t.Errorf("charset %d: GlyphByName(%q) = %d, %v; want %d", tt.charset, name, got, ok, g)
			}
		}
		if g, ok := fnt.GlyphByName(".notdef"); len(fnt.byName) != tt.glyphs-1 || !ok || g != 0 {
			t.Errorf("charset %d: %d names (.notdef %d), want .notdef at 0 and GIDs 1–%d", tt.charset, len(fnt.byName), g, tt.glyphs-2)
		}
		enc, ok := fnt.BuiltinEncoding()
		if !ok || enc[0x20] != "space" || enc[0x21] != "exclamsmall" || enc[0x40] != "" || enc[0x41] != "asuperior" ||
			enc[0x56] != "ff" || enc[0xBC] != "onequarter" || enc[0xFF] != "Ydieresissmall" || enc[0x7F] != "" {
			t.Errorf("predefined encoding 1: ok %v, 20–21 %q, 40–41 %q, 56 %q, BC %q, FF %q", ok, enc[0x20:0x22], enc[0x40:0x42], enc[0x56], enc[0xBC], enc[0xFF])
		}
	}
	// The Expert encoding assigns codes to exactly the Expert charset's
	// glyphs, and the ExpertSubset charset is a subset of the Expert one.
	inExpert := map[uint16]bool{}
	for _, sid := range expertCharset {
		inExpert[sid] = true
	}
	encoded := map[uint16]bool{}
	for _, sid := range expertEncoding {
		if sid != 0 && !inExpert[sid] {
			t.Errorf("Expert encoding SID %d (%s) not in the Expert charset", sid, cffStdStrings[sid])
		}
		encoded[sid] = sid != 0
	}
	for _, sid := range expertCharset {
		if !encoded[sid] {
			t.Errorf("Expert charset SID %d (%s) not encoded", sid, cffStdStrings[sid])
		}
	}
	for _, sid := range expertSubsetCharset {
		if !inExpert[sid] {
			t.Errorf("ExpertSubset SID %d (%s) not in the Expert charset", sid, cffStdStrings[sid])
		}
	}
	// A non-CID-keyed font as a CIDFont uses CID = GID.
	fnt := mustParseCFF(t, cffTestFont().Bytes())
	if g, ok := fnt.GlyphByCID(3); !ok || g != 3 || fnt.CIDKeyed() {
		t.Errorf("GlyphByCID(3) = %d, %v", g, ok)
	}
	if _, ok := fnt.GlyphByCID(uint32(fnt.NumGlyphs())); ok {
		t.Error("GlyphByCID past the glyphs found one")
	}
}

// cidTestFont is a CID-keyed font with two Font DICTs: FD 0 (defaultWidthX
// 300; local subr 0 draws a horizontal 500) and FD 1 (nominalWidthX 1000,
// FontMatrix 1/2000; local subr 0 draws a vertical 500).
func cidTestFont(selFormat int) cb.Font {
	return cb.Font{
		CID: true,
		FDs: []cb.FD{
			{DefaultWidthX: 300, Subrs: [][]byte{cb.CS(500, cb.HLineTo, cb.Return)}},
			{NominalWidthX: 1000, Matrix: []float64{0.0005, 0, 0, 0.0005, 0, 0}, Subrs: [][]byte{cb.CS(500, cb.VLineTo, cb.Return)}},
		},
		FDSelect:       []int{0, 0, 1, 1},
		FDSelectFormat: selFormat,
		Glyphs: []cb.Glyph{
			{CID: 0, CS: cb.CS(cb.EndChar)},
			{CID: 100, CS: cb.CS(100, 100, cb.RMoveTo, -107, cb.CallSubr, cb.EndChar)},
			{CID: 7, CS: cb.CS(-200, 100, 100, cb.RMoveTo, -107, cb.CallSubr, cb.EndChar)},
			{CID: 8, CS: cb.CS(cb.EndChar)},
		},
	}
}

func TestCFFCIDKeyed(t *testing.T) {
	for _, sel := range []int{0, 3} {
		for _, cf := range []int{0, 1, 2} {
			src := cidTestFont(sel)
			src.CharsetFormat = cf
			f := mustParseCFF(t, src.Bytes())
			if !f.CIDKeyed() || f.NumGlyphs() != 4 {
				t.Fatalf("CIDKeyed %v, %d glyphs", f.CIDKeyed(), f.NumGlyphs())
			}
			for cid, want := range map[uint32]int{0: 0, 100: 1, 7: 2, 8: 3, 5: -1, 70000: -1} {
				g, ok := f.GlyphByCID(cid)
				if ok != (want >= 0) || ok && g != GID(want) {
					t.Errorf("FDSelect %d charset %d: GlyphByCID(%d) = %d, %v; want %d", sel, cf, cid, g, ok, want)
				}
			}
			if _, ok := f.GlyphByName(".notdef"); ok {
				t.Error("a CID-keyed font has glyph names")
			}
			g1, err := f.Outline(1)
			want1 := []Segment{{Op: MoveTo, P: [3]Point{{100, 100}}}, {Op: LineTo, P: [3]Point{{600, 100}}}}
			if err != nil || !reflect.DeepEqual(g1.Segs, want1) || g1.Width != 300 || g1.Matrix != defaultMatrix {
				t.Errorf("FD 0 glyph: %v %v width %v matrix %v", err, g1.Segs, g1.Width, g1.Matrix)
			}
			g2, err := f.Outline(2)
			want2 := []Segment{{Op: MoveTo, P: [3]Point{{100, 100}}}, {Op: LineTo, P: [3]Point{{100, 600}}}}
			if err != nil || !reflect.DeepEqual(g2.Segs, want2) || g2.Width != 800 || g2.Matrix != [6]float64{0.0005, 0, 0, 0.0005, 0, 0} {
				t.Errorf("FD 1 glyph: %v %v width %v matrix %v", err, g2.Segs, g2.Width, g2.Matrix)
			}
		}
	}
}

func TestCFFCIDMatrix(t *testing.T) {
	for _, tt := range []struct {
		name     string
		top, fd1 []float64
		want0    [6]float64
		want1    [6]float64
	}{
		{"neither", nil, nil, defaultMatrix, defaultMatrix},
		{"font dict only", nil, []float64{0.0005, 0, 0, 0.0005, 0, 0}, defaultMatrix, [6]float64{0.0005, 0, 0, 0.0005, 0, 0}},
		// A glyph-space Top DICT matrix alone is the whole matrix, not
		// composed with the Font DICTs' default again.
		{"top only", []float64{0.002, 0, 0, 0.002, 0, 0}, nil, [6]float64{0.002, 0, 0, 0.002, 0, 0}, [6]float64{0.002, 0, 0, 0.002, 0, 0}},
		{"top only, 0.001", []float64{0.001, 0, 0, 0.001, 0, 0}, nil, defaultMatrix, defaultMatrix},
		// An identity Top DICT matrix alone scales the Font DICTs' default.
		{"top only, identity", []float64{1, 0, 0, 1, 0, 0}, nil, defaultMatrix, defaultMatrix},
		{"top only, 2× identity", []float64{2, 0, 0, 2, 0, 0}, nil, [6]float64{0.002, 0, 0, 0.002, 0, 0}, [6]float64{0.002, 0, 0, 0.002, 0, 0}},
		// The Font DICT's matrix, then the Top DICT's: 0.5 × 0.002 = 0.001;
		// FD 0 has none, so the default × 0.5.
		{"both", []float64{0.5, 0, 0, 0.5, 0, 0}, []float64{0.002, 0, 0, 0.002, 0, 0}, [6]float64{0.0005, 0, 0, 0.0005, 0, 0}, [6]float64{0.001, 0, 0, 0.001, 0, 0}},
		{"identity top, font dict", []float64{1, 0, 0, 1, 0, 0}, []float64{0.0005, 0, 0, 0.0005, 0, 0}, defaultMatrix, [6]float64{0.0005, 0, 0, 0.0005, 0, 0}},
		// A singular product falls back to the default.
		{"singular", []float64{0.5, 0, 0, 0.5, 0, 0}, []float64{0.002, 0, 0, 0, 0, 0}, [6]float64{0.0005, 0, 0, 0.0005, 0, 0}, defaultMatrix},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := cidTestFont(0)
			src.Matrix, src.FDs[1].Matrix = tt.top, tt.fd1
			f := mustParseCFF(t, src.Bytes())
			g1, _ := f.Outline(1)
			g2, _ := f.Outline(2)
			if g1.Matrix != tt.want0 || g2.Matrix != tt.want1 || f.Matrix() != tt.want0 {
				t.Errorf("matrices %v %v (font %v), want %v %v", g1.Matrix, g2.Matrix, f.Matrix(), tt.want0, tt.want1)
			}
		})
	}
}

func TestCFFSubrBias(t *testing.T) {
	for _, n := range []int{1, 1239, 1240, 33899, 33900} {
		subrs := bytes.Split(bytes.Repeat(cb.CS(cb.Return), n), nil)[:n]
		hit := append([][]byte(nil), subrs...)
		hit[n-1] = cb.CS(500, cb.HLineTo, cb.Return)
		operand := n - 1 - subrBias(n)
		want := []Segment{{Op: MoveTo}, {Op: LineTo, P: [3]Point{{500, 0}}}}
		cs := cb.CS(0, 0, cb.RMoveTo, operand, cb.CallSubr, cb.EndChar)
		g, err := buildT2(t, cb.Font{Subrs: hit}, cs)
		if err != nil || !reflect.DeepEqual(g.Segs, want) {
			t.Errorf("%d local subrs (bias %d): %v %v", n, subrBias(n), err, g.Segs)
		}
		gcs := cb.CS(0, 0, cb.RMoveTo, operand, cb.CallGSubr, cb.EndChar)
		if g, err := buildT2(t, cb.Font{GSubrs: hit}, gcs); err != nil || !reflect.DeepEqual(g.Segs, want) {
			t.Errorf("%d global subrs: %v %v", n, err, g.Segs)
		}
		// One past either end of the INDEX, computed (32768 and −32769 do
		// not fit a charstring number).
		for _, op := range [][]byte{cb.CS(operand, 1, cb.Add), cb.CS(cb.Fixed(-subrBias(n)), 1, cb.Sub)} {
			cs := append(append(cb.CS(0, 0, cb.RMoveTo), op...), cb.CS(cb.CallSubr, cb.EndChar)...)
			if _, err := buildT2(t, cb.Font{Subrs: hit}, cs); !errors.Is(err, errMalformed) {
				t.Errorf("%d subrs, operand %v: err = %v, want errMalformed", n, op, err)
			}
		}
	}
	if subrBias(1239) != 107 || subrBias(1240) != 1131 || subrBias(33899) != 1131 || subrBias(33900) != 32768 {
		t.Error("bias tiers")
	}
}

func TestCFFSeacRules(t *testing.T) {
	f := cffTestFont()
	f.Glyphs = append(f.Glyphs,
		// StandardEncoding 66 is B: the font has no glyph named B.
		cb.Glyph{Name: "missing", CS: cb.CS(0, 0, 65, 66, cb.EndChar)},
		cb.Glyph{Name: "badcode", CS: cb.CS(0, 0, 65, 300, cb.EndChar)},
		// StandardEncoding 193 is grave, here itself a seac: the accent
		// component of "nested" would compose again.
		cb.Glyph{Name: "grave", CS: cffAacute},
		cb.Glyph{Name: "nested", CS: cb.CS(0, 0, 65, 193, cb.EndChar)},
		// An accent offset of 65,534 is fine; 2^20 puts the accent's points
		// past ±2^20.
		cb.Glyph{Name: "far", CS: cb.CS(cb.Fixed(32767), cb.Fixed(32767), cb.Add, 0, 65, 194, cb.EndChar)},
		cb.Glyph{Name: "toofar", CS: cb.CS(1024, 1024, cb.Mul, 0, 65, 194, cb.EndChar)},
	)
	fnt := mustParseCFF(t, f.Bytes())
	for name, want := range map[string]error{
		"missing": errMalformed, "badcode": errMalformed, "nested": errMalformed,
		"far": nil, "toofar": errLimit,
	} {
		gid, ok := fnt.GlyphByName(name)
		if !ok {
			t.Fatalf("no glyph %s", name)
		}
		if _, err := fnt.Outline(gid); !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}
	// A CID-keyed font has no names: endchar-seac aborts the glyph.
	cid := cidTestFont(0)
	cid.Glyphs[3].CS = cb.CS(0, 0, 65, 194, cb.EndChar)
	if _, err := mustParseCFF(t, cid.Bytes()).Outline(3); !errors.Is(err, errMalformed) {
		t.Errorf("CID-keyed seac: err = %v, want errMalformed", err)
	}
}

func TestCFFLimits(t *testing.T) {
	build := func(cs []byte, subrs ...[]byte) (Glyph, error) {
		return buildT2(t, cb.Font{Subrs: subrs}, cs)
	}
	repeat := func(n int, b []byte) []byte { return bytes.Repeat(b, n) }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	mv := cb.CS(0, 0, cb.RMoveTo)
	for _, tt := range []struct {
		name string
		cs   []byte
		subr [][]byte
		want error
	}{
		{"stack 48", cat(mv, repeat(48, cb.CS(1)), cb.CS(cb.RLineTo, cb.EndChar)), nil, nil},
		{"stack 49", cat(mv, repeat(49, cb.CS(1)), cb.CS(cb.RLineTo, cb.EndChar)), nil, errLimit},
		{"stems 96", cat(repeat(4, cat(repeat(48, cb.CS(1)), cb.CS(cb.HStem))), cb.CS(cb.HintMask), make([]byte, 12), cb.CS(cb.EndChar)), nil, nil},
		{"stems 97", cat(repeat(4, cat(repeat(48, cb.CS(1)), cb.CS(cb.HStem))), cb.CS(1, 1, cb.VStem, cb.EndChar)), nil, errLimit},
		{"implied vstems 97", cat(repeat(4, cat(repeat(48, cb.CS(1)), cb.CS(cb.HStemHM))), cb.CS(1, 1, cb.HintMask), make([]byte, 13), cb.CS(cb.EndChar)), nil, errLimit},
		{"mask past the end", cb.CS(1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, cb.HStem, cb.HintMask, cb.Raw{0}), nil, errMalformed},
		{"transient 31", cat(mv, cb.CS(5, 31, cb.Put, 31, cb.Get, 0, cb.RLineTo, cb.EndChar)), nil, nil},
		{"put 32", cat(mv, cb.CS(5, 32, cb.Put, cb.EndChar)), nil, errMalformed},
		{"get 32", cat(mv, cb.CS(32, cb.Get, cb.EndChar)), nil, errMalformed},
		{"get -1", cat(mv, cb.CS(-1, cb.Get, cb.EndChar)), nil, errMalformed},
		{"div 0", cat(mv, cb.CS(1, 0, cb.Div, cb.EndChar)), nil, errMalformed},
		{"sqrt -1", cat(mv, cb.CS(-1, cb.Sqrt, cb.EndChar)), nil, errMalformed},
		{"mul overflow", cat(mv, cb.CS(30000), repeat(8, cb.CS(cb.Dup, cb.Mul)), cb.CS(cb.EndChar)), nil, errLimit},
		{"index past stack", cat(mv, cb.CS(1, 5, cb.Index, cb.EndChar)), nil, errMalformed},
		{"roll past stack", cat(mv, cb.CS(1, 2, 3, 1, cb.Roll, cb.EndChar)), nil, errMalformed},
		{"underflow", cb.CS(1, cb.RLineTo), nil, errMalformed},
		{"coordinate 2^20", cb.CS(1024, 1024, cb.Mul, 0, cb.RMoveTo, 0, 1, cb.RLineTo, cb.EndChar), nil, nil},
		{"coordinate 2^20+1", cb.CS(1024, 1024, cb.Mul, 1, cb.Add, 0, cb.RMoveTo, cb.EndChar), nil, errLimit},
		{"ops 16384", cat(repeat(maxGlyphOps-1, cb.CS(0, cb.HMoveTo)), cb.CS(cb.EndChar)), nil, nil},
		{"ops 16385", cat(repeat(maxGlyphOps, cb.CS(0, cb.HMoveTo)), cb.CS(cb.EndChar)), nil, errLimit},
		{"segments 8192", cat(mv, repeat(maxGlyphSegments-1, cb.CS(1, cb.HLineTo)), cb.CS(cb.EndChar)), nil, nil},
		{"segments 8193", cat(mv, repeat(maxGlyphSegments, cb.CS(1, cb.HLineTo)), cb.CS(cb.EndChar)), nil, errLimit},
		{"charstring 65535", cat(cb.CS(cb.EndChar), make([]byte, maxCharstringLen-1)), nil, nil},
		{"charstring 65536", cat(cb.CS(cb.EndChar), make([]byte, maxCharstringLen)), nil, errLimit},
		{"self-recursive subr", cb.CS(-107, cb.CallSubr, cb.EndChar), [][]byte{cb.CS(-107, cb.CallSubr, cb.Return)}, errLimit},
		{"truncated shortint", []byte{28, 1}, nil, errMalformed},
		{"truncated fixed", []byte{255, 1, 2}, nil, errMalformed},
		{"truncated escape", []byte{12}, nil, errMalformed},
		{"truncated 247", []byte{247}, nil, errMalformed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g, err := build(tt.cs, tt.subr...)
			if !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if g.Ops > maxGlyphOps+1 || len(g.Segs) > maxGlyphSegments {
				t.Errorf("past the limits: %d ops, %d segments", g.Ops, len(g.Segs))
			}
		})
	}
	// Subroutine nesting: a chain of n subrs, each calling the next.
	chain := func(n int) [][]byte {
		subrs := make([][]byte, n)
		for i := 0; i < n-1; i++ {
			subrs[i] = cb.CS(i+1-107, cb.CallSubr, cb.Return)
		}
		subrs[n-1] = cb.CS(cb.Return)
		return subrs
	}
	if _, err := build(cb.CS(-107, cb.CallSubr, cb.EndChar), chain(maxSubrDepth)...); err != nil {
		t.Errorf("subr nesting %d: %v", maxSubrDepth, err)
	}
	if _, err := build(cb.CS(-107, cb.CallSubr, cb.EndChar), chain(maxSubrDepth+1)...); !errors.Is(err, errLimit) {
		t.Errorf("subr nesting %d: err = %v, want errLimit", maxSubrDepth+1, err)
	}
}

func TestReadIndex(t *testing.T) {
	idx := func(count, offSize int, offs ...uint32) []byte {
		b := binary.BigEndian.AppendUint16(nil, uint16(count))
		b = append(b, byte(offSize))
		for _, o := range offs {
			for i := offSize - 1; i >= 0; i-- {
				b = append(b, byte(o>>(8*i)))
			}
		}
		return b
	}
	data := func(b []byte, n int) []byte { return append(b, bytes.Repeat([]byte{'x'}, n)...) }
	for _, tt := range []struct {
		name string
		b    []byte
		ok   bool
		end  int
	}{
		{"empty", []byte{0, 0}, true, 2},
		{"one object", data(idx(1, 1, 1, 4), 3), true, 3 + 2 + 3},
		{"offSize 4", data(idx(2, 4, 1, 2, 3), 2), true, 3 + 12 + 2},
		{"offSize 0", data(idx(1, 0), 3), false, 0},
		{"offSize 5", data(idx(1, 5, 1, 4), 3), false, 0},
		{"first offset 0", data(idx(1, 1, 0, 4), 4), false, 0},
		{"decreasing", data(idx(2, 1, 1, 3, 2), 3), false, 0},
		{"last past the end", data(idx(1, 1, 1, 5), 3), false, 0},
		{"offsets past the end", idx(65535, 4, 1), false, 0},
		{"truncated count", []byte{0}, false, 0},
		{"truncated offSize", []byte{0, 1}, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			x, end, err := readIndex(tt.b, 0)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok %v", err, tt.ok)
			}
			if !tt.ok {
				if end != 0 {
					t.Errorf("end = %d on failure, want the input offset", end)
				}
				return
			}
			if end != tt.end {
				t.Errorf("end = %d, want %d", end, tt.end)
			}
			for i := 0; i < x.count; i++ {
				if _, ok := x.get(i); !ok {
					t.Errorf("get(%d) failed", i)
				}
			}
			if _, ok := x.get(x.count); ok {
				t.Error("get(count) succeeded")
			}
		})
	}
	if _, _, err := readIndex([]byte{0, 0}, -1); err == nil {
		t.Error("negative position accepted")
	}
}

func TestParseDict(t *testing.T) {
	d, err := parseDict([]byte{
		139, 247, 0, 251, 0, 28, 0x80, 0x00, 29, 0x7f, 0xff, 0xff, 0xff, 30, 0x1a, 0x25, 0xc3, 0xff, 30, 0xe2, 0xa5, 0xff, 18, // 0 108 −108 −32768 MaxInt32 1.25E−3 −2.5 Private
		140, 141, 142, 143, 144, 145, 12, 7, // 1–6 FontMatrix (escaped)
		139, 18, // Private again: ignored
		139, 140, 5, // FontBBox: not read, not kept
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0, 108, -108, -32768, 2147483647, 1.25e-3, -2.5}
	if !reflect.DeepEqual(d[dictPrivate], want) {
		t.Errorf("Private operands = %v, want %v (the first occurrence)", d[dictPrivate], want)
	}
	if len(d[dictFontMatrix]) != 6 || len(d) != 2 {
		t.Errorf("FontMatrix = %v, %d operators kept", d[dictFontMatrix], len(d))
	}
	for name, b := range map[string][]byte{
		"49 operands":       append(bytes.Repeat([]byte{139}, maxDictOperands+1), 5),
		"reserved 22":       {22},
		"reserved 255":      {255},
		"truncated 28":      {28, 0},
		"truncated 29":      {29, 0, 0, 0},
		"truncated escape":  {12},
		"truncated real":    {30, 0x12},
		"real 65 nibbles":   append(append([]byte{30}, bytes.Repeat([]byte{0x11}, 33)...), 0xff),
		"real nibble d":     {30, 0xdf},
		"real bad syntax":   {30, 0xaa, 0xff, 5},
		"truncated 247 254": {247},
	} {
		if _, err := parseDict(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, err := parseDict(append(bytes.Repeat([]byte{139}, maxDictOperands), 5)); err != nil {
		t.Errorf("48 operands: %v", err)
	}
	if _, err := parseDict(append(append([]byte{30}, bytes.Repeat([]byte{0x11}, 32)...), 0xff, 5)); err != nil {
		t.Errorf("real of 64 nibbles: %v", err)
	}
	// A DICT of one operator repeated 100,000 times keeps one entry; one of
	// every operator keeps the ones ParseCFF reads.
	rep := bytes.Repeat([]byte{139, 140, 18}, 100000)
	if d, err := parseDict(rep); err != nil || len(d) != 1 || !reflect.DeepEqual(d[dictPrivate], []float64{0, 1}) {
		t.Errorf("repeated Private: %v %v", d, err)
	}
	var every []byte
	for op := 0; op < 22+256; op++ {
		if op == 12 {
			continue // the escape byte
		}
		every = append(every, bytes.Repeat([]byte{139}, maxDictOperands)...)
		switch {
		case op < 22:
			every = append(every, byte(op))
		default:
			every = append(every, 12, byte(op-22))
		}
	}
	if d, err := parseDict(every); err != nil || len(d) != 12 {
		t.Errorf("every operator: %d kept, %v; want the 12 read", len(d), err)
	}
}

func TestCFFStructuralLimits(t *testing.T) {
	good := cffTestFont().Bytes()
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"major version 2", mut(func(b []byte) []byte { b[0] = 2; return b })},
		{"hdrSize 3", mut(func(b []byte) []byte { b[2] = 3; return b })},
		{"hdrSize past the end", mut(func(b []byte) []byte { b[2] = 255; return b[:200] })},
		{"truncated", good[:40]},
		{"empty", nil},
	} {
		if _, err := ParseCFF(tt.data, Options{}); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", tt.name, err)
		}
	}
	for _, tt := range []struct {
		name string
		f    cb.Font
	}{
		{"CharstringType 1", func() cb.Font { f := cffTestFont(); f.CharstringType = 1; return f }()},
		{"no glyphs", cb.Font{}},
		{"FD index past FDArray (format 0)", func() cb.Font { f := cidTestFont(0); f.FDSelect = []int{0, 0, 2, 1}; return f }()},
		{"FD index past FDArray (format 3)", func() cb.Font { f := cidTestFont(3); f.FDSelect = []int{0, 0, 2, 2}; return f }()},
		{"257 FDs", func() cb.Font { f := cidTestFont(0); f.FDs = make([]cb.FD, maxFDs+1); return f }()},
		{"defaultWidthX past maxCoord", cb.Font{DefaultWidthX: maxCoord + 1, Glyphs: cffTestFont().Glyphs}},
		{"nominalWidthX past -maxCoord", cb.Font{NominalWidthX: -maxCoord - 1, Glyphs: cffTestFont().Glyphs}},
		{"FD defaultWidthX past maxCoord", func() cb.Font { f := cidTestFont(0); f.FDs[1].DefaultWidthX = maxCoord + 1; return f }()},
	} {
		if _, err := ParseCFF(tt.f.Bytes(), Options{}); err == nil {
			t.Errorf("%s: parsed", tt.name)
		}
	}
	if _, err := ParseCFF(cb.Font{DefaultWidthX: maxCoord, NominalWidthX: -maxCoord, Glyphs: cffTestFont().Glyphs}.Bytes(), Options{}); err != nil {
		t.Errorf("widths ±maxCoord: %v", err)
	}
	f := cidTestFont(3)
	f.FDs = make([]cb.FD, maxFDs)
	f.FDSelect = []int{0, maxFDs - 1, 3, 3}
	if _, err := ParseCFF(f.Bytes(), Options{}); err != nil {
		t.Errorf("256 FDs: %v", err)
	}
	if _, err := ParseCFF(make([]byte, maxProgramBytes+1), Options{}); !errors.Is(err, errLimit) {
		t.Errorf("program past maxProgramBytes: %v", err)
	}
	refused := Options{Charge: func(int64) bool { return false }}
	if _, err := ParseCFF(good, refused); !errors.Is(err, errLimit) {
		t.Errorf("refused charge: %v", err)
	}
	if _, err := ParseCFF(cidTestFont(0).Bytes(), refused); !errors.Is(err, errLimit) {
		t.Errorf("refused charge (CID): %v", err)
	}
}

// TestCFFTables mutates the charset, Encoding and FDSelect formats: an
// unknown format, a truncated table and an out-of-range entry each reject
// the font, and nothing panics.
func TestCFFTables(t *testing.T) {
	src := cffTestFont()
	src.CharsetFormat = 1
	good := src.Bytes()
	// Find the charset: the byte after the Global Subr INDEX. Parse the
	// layout the same way ParseCFF does.
	_, pos, _ := readIndex(good, 4)
	_, pos, _ = readIndex(good, pos)
	_, pos, _ = readIndex(good, pos)
	_, charset, _ := readIndex(good, pos)
	set := func(off int, v ...byte) []byte {
		b := append([]byte(nil), good...)
		copy(b[off:], v)
		return b
	}
	for name, data := range map[string][]byte{
		"charset format 3":     set(charset, 3),
		"charset SID overflow": set(charset, 1, 0xff, 0xff, 1),
	} {
		if _, err := ParseCFF(data, Options{}); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}
	// A range whose nLeft reaches past SID 0xFFFF but past the glyph count
	// too is clamped to the glyphs, whose SIDs fit.
	if f, err := ParseCFF(set(charset, 1, 0xff, 0xf0, 0xff), Options{}); err != nil || f.NumGlyphs() != len(src.Glyphs) {
		t.Errorf("charset range past 0xFFFF and the glyph count: err = %v", err)
	}
	// FDSelect format 3 whose first range does not start at 0, and an
	// unknown format.
	cid := cidTestFont(3)
	cb3 := cid.Bytes()
	i := bytes.LastIndex(cb3, []byte{3, 0, 2, 0, 0, 0, 0, 2, 1, 0, 4})
	if i < 0 {
		t.Fatal("FDSelect not found")
	}
	for name, data := range map[string][]byte{
		"FDSelect first 1":   append(append(append([]byte(nil), cb3[:i+3]...), 0, 1), cb3[i+5:]...),
		"FDSelect format 1":  append(append(append([]byte(nil), cb3[:i]...), 1), cb3[i+1:]...),
		"FDSelect unordered": append(append(append([]byte(nil), cb3[:i+6]...), 0, 0), cb3[i+8:]...),
	} {
		if _, err := ParseCFF(data, Options{}); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}
}

// TestCFFPrivateSharing: Font DICTs that share one Private DICT read it
// once; distinct Private DICTs that together pass the program's size reject
// the font (errLimit), so overlapping ranges cannot multiply the work.
func TestCFFPrivateSharing(t *testing.T) {
	data := bytes.Repeat([]byte{139, 20}, 500) // 1,000 bytes of "0 defaultWidthX"
	p := &cffParser{data: data, privates: map[[2]int]cffFD{}, subrs: map[int]cffIndex{}}
	same := cffDict{dictPrivate: {1000, 0}}
	for i := 0; i < maxFDs; i++ {
		if _, err := p.readPrivate(same); err != nil {
			t.Fatalf("shared Private, FD %d: %v", i, err)
		}
	}
	if p.privateBytes != 1000 {
		t.Errorf("privateBytes = %d, want 1000", p.privateBytes)
	}
	p = &cffParser{data: data, privates: map[[2]int]cffFD{}, subrs: map[int]cffIndex{}}
	var err error
	for i := 0; i < maxFDs && err == nil; i++ {
		_, err = p.readPrivate(cffDict{dictPrivate: {float64(1000 - 2*i), float64(2 * i)}})
	}
	if !errors.Is(err, errLimit) {
		t.Errorf("overlapping Private DICTs: err = %v, want errLimit", err)
	}
	for name, d := range map[string]cffDict{
		"size past the end":   {dictPrivate: {1001, 0}},
		"offset past the end": {dictPrivate: {0, 1001}},
		"negative":            {dictPrivate: {-1, 0}},
		"fractional":          {dictPrivate: {1.5, 0}},
		"one operand":         {dictPrivate: {10}},
	} {
		p := &cffParser{data: data, privates: map[[2]int]cffFD{}, subrs: map[int]cffIndex{}}
		if _, err := p.readPrivate(d); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}
}

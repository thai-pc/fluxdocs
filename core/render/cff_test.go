package render

import (
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	cb "github.com/thai-pc/fluxdocs/core/internal/psfont/cffbuild"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// Glyphs of the hand-built CFF test font, the Type 2 twins of the Type 1
// render test glyphs (type1_test.go): the same outlines in the same glyph
// units, so the same ink boxes. nominalWidthX is 0, so a width operand is
// the advance.
var (
	cffSquareR = cb.CS(700, 100, 100, cb.RMoveTo, 500, cb.HLineTo, 500, cb.VLineTo, -500, cb.HLineTo, cb.EndChar)
	cffRingR   = cb.CS(800, 100, 100, cb.RMoveTo, 600, 600, -600, cb.HLineTo, 200, -400, cb.RMoveTo, 200, 200, -200, cb.VLineTo, cb.EndChar)
	// The seac components (base (0,0)–(500,300), accent (0,0)–(200,100)) and
	// the composite placing the accent's origin at (200, 500): (200,500)–
	// (400,600), the Type 1 test's accent box.
	cffBaseR      = cb.CS(600, 0, 0, cb.RMoveTo, 500, 300, -500, cb.HLineTo, cb.EndChar)
	cffAccentR    = cb.CS(300, 0, 0, cb.RMoveTo, 200, 100, -200, cb.HLineTo, cb.EndChar)
	cffCompositeR = cb.CS(600, 200, 500, 65, 194, cb.EndChar)
	// cffCurvesR is a disc of four quarter arcs around (400, 400), radius
	// 300 (control offset 166), as two vhcurveto runs of alternating
	// vertical and horizontal starts.
	cffCurvesR = cb.CS(900, 100, 400, cb.RMoveTo,
		166, 134, 134, 166, 166, 134, -134, -166, cb.VHCurveTo,
		-166, -134, -134, -166, -166, -134, 134, 166, cb.VHCurveTo,
		cb.EndChar)
	// cffFlexBarR is a bar (100,100)–(900,400) whose top edge has an hflex
	// bump from x 800 to 200 peaking at (500, 500): box (100,100)–(900,500).
	cffFlexBarR = cb.CS(1000, 100, 100, cb.RMoveTo, 800, 300, -100, cb.HLineTo,
		-100, -100, 100, -100, -100, -100, -100, cb.HFlex, -100, cb.HLineTo, cb.EndChar)
)

// cffTestFontR is the font the render tests embed: "square" at code 0x41 in
// its built-in encoding, "ring" at 0x42, "Aacute" at 0x43, "renamed" (an
// empty glyph) at 0x44, "curves" at 0x45, "flexbar" at 0x46, and the seac
// components under
// their StandardEncoding names.
func cffTestFontR() cb.Font {
	return cb.Font{
		Encoding: map[int]string{0x41: "square", 0x42: "ring", 0x43: "Aacute", 0x44: "renamed", 0x45: "curves", 0x46: "flexbar"},
		Glyphs: []cb.Glyph{
			{Name: ".notdef", CS: cb.CS(500, cb.EndChar)},
			{Name: "square", CS: cffSquareR},
			{Name: "ring", CS: cffRingR},
			{Name: "Aacute", CS: cffCompositeR},
			{Name: "renamed", CS: cb.CS(700, cb.EndChar)},
			{Name: "curves", CS: cffCurvesR},
			{Name: "flexbar", CS: cffFlexBarR},
			{Name: "A", CS: cffBaseR},
			{Name: "acute", CS: cffAccentR},
		},
	}
}

// cffPDF builds a 200x200 page that shows content with a simple font /F1
// over an embedded /FontFile3 of subtype /Type1C holding font, the font
// dictionary carrying fontExtra.
func cffPDF(font []byte, fontExtra, content string) []byte {
	return buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /FluxCFFTest /FontDescriptor 6 0 R " + fontExtra + " >>",
		5: streamObj(content),
		6: "<< /Type /FontDescriptor /FontName /FluxCFFTest /Flags 4 /FontBBox [0 -250 1000 900] /FontFile3 7 0 R >>",
		7: fmt.Sprintf("<< /Length %d /Subtype /Type1C >>\nstream\n%s\nendstream", len(font), font),
	}, "<< /Size 8 /Root 1 0 R >>")
}

// cidPDF builds a 200x200 page that shows content with a Type 0 font /F1
// (Identity-H) whose CIDFontType0 descendant embeds font as /FontFile3 of
// subtype /CIDFontType0C.
func cidPDF(font []byte, content string) []byte { return cidPDFWith(font, "", content) }

// cidPDFWith is cidPDF with cidExtra (e.g. /W, /DW) in the CIDFont
// dictionary.
func cidPDFWith(font []byte, cidExtra, content string) []byte {
	return buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type0 /BaseFont /FluxCIDTest /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
		5: streamObj(content),
		6: "<< /Type /Font /Subtype /CIDFontType0 /BaseFont /FluxCIDTest /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor 7 0 R " + cidExtra + " >>",
		7: "<< /Type /FontDescriptor /FontName /FluxCIDTest /Flags 4 /FontBBox [0 -250 1000 900] /FontFile3 8 0 R >>",
		8: fmt.Sprintf("<< /Length %d /Subtype /CIDFontType0C >>\nstream\n%s\nendstream", len(font), font),
	}, "<< /Size 9 /Root 1 0 R >>")
}

// cidTestFontR is a CID-keyed font: CID 500 is the square through FD 0 and
// CID 7 the square in 2,000-unit coordinates through FD 1 (FontMatrix
// 1/2000), drawn the same size.
func cidTestFontR() cb.Font {
	d := func(v int) int { return 2 * v }
	return cb.Font{
		CID: true,
		FDs: []cb.FD{{}, {Matrix: []float64{0.0005, 0, 0, 0.0005, 0, 0}}},
		Glyphs: []cb.Glyph{
			{CID: 0, CS: cb.CS(500, cb.EndChar)},
			{CID: 500, CS: cffSquareR},
			{CID: 7, CS: cb.CS(d(700), d(100), d(100), cb.RMoveTo, d(500), cb.HLineTo, d(500), cb.VLineTo, -d(500), cb.HLineTo, cb.EndChar)},
		},
		FDSelect:       []int{0, 0, 1},
		FDSelectFormat: 3,
	}
}

func renderCFF(t *testing.T, fontExtra, content string) image.Image {
	t.Helper()
	return renderPDF(t, cffPDF(cffTestFontR().Bytes(), fontExtra, content))
}

func TestCFF_SquareInkBox(t *testing.T) {
	want := image.Rect(20+10, 200-30-60, 20+60, 200-30-10) // origin (20, 30), square 10..60 px
	for _, tt := range []struct {
		name string
		pdf  []byte
	}{
		{"Type1C simple font", cffPDF(cffTestFontR().Bytes(), "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"CIDFontType0C, FD 0", cidPDF(cidTestFontR().Bytes(), "BT /F1 100 Tf 20 30 Td <01F4> Tj ET")},
		{"CIDFontType0C, FD 1 matrix 1/2000", cidPDF(cidTestFontR().Bytes(), "BT /F1 100 Tf 20 30 Td <0007> Tj ET")},
		// A non-CID-keyed CFF as a CIDFont: CID = GID (§9.7.4.2).
		{"Type1C as CIDFontType0", cidPDF(cffTestFontR().Bytes(), "BT /F1 100 Tf 20 30 Td <0001> Tj ET")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := renderPDF(t, tt.pdf)
			box, ok := inkBox(img, img.Bounds())
			if !ok || !near(box, want) {
				t.Errorf("ink box = %v (found %v), want %v ±1", box, ok, want)
			}
		})
	}
}

func TestCFF_CIDSelection(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pdf     []byte
		ink     bool
		advance int
	}{
		// CID 8 is not in the charset: nothing drawn.
		{"CID missing", cidPDF(cidTestFontR().Bytes(), "BT /F1 100 Tf 20 30 Td <0008> Tj ET"), false, 0},
		// A CID-keyed font used as a simple font has no glyph names.
		{"CID-keyed as simple font", cffPDF(cidTestFontR().Bytes(), "", "BT /F1 100 Tf 20 30 Td (A) Tj ET"), false, 0},
		// Two CIDs advance by the CIDFont's widths (§9.7.4.3), not the
		// program's 700/1000 em: /DW (default 1000) gives 100 px at 100 pt,
		// a /W entry of 500 50 px, /DW 800 80 px.
		{"advance /DW default", cidPDF(cidTestFontR().Bytes(), "BT /F1 100 Tf 0 30 Td <01F40007> Tj ET"), true, 100},
		{"advance /W", cidPDFWith(cidTestFontR().Bytes(), "/W [500 [600] 7 7 300]", "BT /F1 100 Tf 0 30 Td <01F40007> Tj ET"), true, 60},
		{"advance /DW", cidPDFWith(cidTestFontR().Bytes(), "/DW 800", "BT /F1 100 Tf 0 30 Td <01F40007> Tj ET"), true, 80},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := renderPDF(t, tt.pdf)
			if _, ink := inkBox(img, img.Bounds()); ink != tt.ink {
				t.Fatalf("ink = %v, want %v", ink, tt.ink)
			}
			if tt.advance > 0 {
				runs := inkRuns(img)
				if len(runs) != 2 || runs[1][0]-runs[0][0] < tt.advance-1 || runs[1][0]-runs[0][0] > tt.advance+1 {
					t.Errorf("ink runs %v, want two %d px apart", runs, tt.advance)
				}
			}
		})
	}
}

func TestCFF_CounterIsHollow(t *testing.T) {
	img := renderCFF(t, "", "BT /F1 100 Tf 20 30 Td (B) Tj ET")
	outer, ok := inkBox(img, img.Bounds())
	if want := image.Rect(30, 100, 90, 160); !ok || !near(outer, want) {
		t.Fatalf("ring ink box = %v, want %v ±1", outer, want)
	}
	if hole, dark := inkBox(img, image.Rect(52, 122, 68, 138)); dark {
		t.Errorf("ink inside the counter at %v", hole)
	}
}

func TestCFF_SeacAccentAboveBase(t *testing.T) {
	img := renderCFF(t, "", "BT /F1 100 Tf 20 30 Td (C) Tj ET")
	if base, ok := inkBox(img, image.Rect(0, 135, 200, 200)); !ok || !near(base, image.Rect(20, 140, 70, 170)) {
		t.Errorf("base ink box = %v, want (20,140)-(70,170)", base)
	}
	if acc, ok := inkBox(img, image.Rect(0, 0, 200, 135)); !ok || !near(acc, image.Rect(40, 110, 60, 120)) {
		t.Errorf("accent ink box = %v, want (40,110)-(60,120)", acc)
	}
}

func TestCFF_CurvesInkBox(t *testing.T) {
	// Four quarter arcs through (100,400) (400,700) (700,400) (400,100):
	// glyph box (100,100)–(700,700), device (30,100)–(90,160) at 100 pt.
	img := renderCFF(t, "", "BT /F1 100 Tf 20 30 Td (E) Tj ET")
	if box, ok := inkBox(img, img.Bounds()); !ok || !near(box, image.Rect(30, 100, 90, 160)) {
		t.Errorf("curves ink box = %v, want (30,100)-(90,160) ±1", box)
	}
}

func TestCFF_FlexInkBox(t *testing.T) {
	// Glyph box (100,100)–(900,500): device (30,120)–(110,160) at 100 pt.
	img := renderCFF(t, "", "BT /F1 100 Tf 20 30 Td (F) Tj ET")
	if box, ok := inkBox(img, img.Bounds()); !ok || !near(box, image.Rect(30, 120, 110, 160)) {
		t.Errorf("flex ink box = %v, want (30,120)-(110,160) ±1", box)
	}
}

func TestCFF_WidthsOverrideProgramAdvance(t *testing.T) {
	for _, tt := range []struct {
		name, extra string
		want        int
	}{
		{"widths", "/FirstChar 65 /LastChar 65 /Widths [1000]", 100},
		{"program advance", "", 70},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runs := inkRuns(renderCFF(t, tt.extra, "BT /F1 100 Tf 0 30 Td (AA) Tj ET"))
			if len(runs) != 2 {
				t.Fatalf("ink runs = %v, want 2 glyphs", runs)
			}
			if d := runs[1][0] - runs[0][0]; d < tt.want-1 || d > tt.want+1 {
				t.Errorf("glyph spacing = %d px, want %d ±1", d, tt.want)
			}
		})
	}
}

func TestCFF_EncodingSelectsGlyph(t *testing.T) {
	for _, tt := range []struct {
		name, extra, content string
		ink                  bool
	}{
		{"differences", "/Encoding << /Differences [65 /renamed] >>", "(A)", false},
		{"differences add", "/Encoding << /Differences [97 /square] >>", "(a)", true},
		{"base encoding", "/Encoding /WinAnsiEncoding", "(A)", true},
		{"unassigned", "", "(Z)", false},
		{"invisible", "", "3 Tr (ABC)", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := renderCFF(t, tt.extra, "BT /F1 100 Tf 20 30 Td "+tt.content+" Tj ET")
			if _, ink := inkBox(img, img.Bounds()); ink != tt.ink {
				t.Errorf("ink = %v, want %v", ink, tt.ink)
			}
		})
	}
	img := renderCFF(t, "/Encoding /WinAnsiEncoding", "BT /F1 100 Tf 20 30 Td (A) Tj ET")
	if box, _ := inkBox(img, img.Bounds()); !near(box, image.Rect(20, 140, 70, 170)) {
		t.Errorf("WinAnsi A ink box = %v, want the base glyph (20,140)-(70,170)", box)
	}
}

func TestCFF_FontMatrix2048(t *testing.T) {
	s := func(v int) int { return v * 2048 / 1000 }
	f := cb.Font{
		Matrix:   []float64{0.00048828125, 0, 0, 0.00048828125, 0, 0},
		Encoding: map[int]string{0x41: "square"},
		Glyphs: []cb.Glyph{{Name: ".notdef", CS: cb.CS(cb.EndChar)},
			{Name: "square", CS: cb.CS(s(700), s(100), s(100), cb.RMoveTo, s(500), cb.HLineTo, s(500), cb.VLineTo, -s(500), cb.HLineTo, cb.EndChar)}},
	}
	img := renderPDF(t, cffPDF(f.Bytes(), "", "BT /F1 100 Tf 20 30 Td (A) Tj ET"))
	if box, ok := inkBox(img, img.Bounds()); !ok || !near(box, image.Rect(30, 110, 80, 160)) {
		t.Errorf("ink box = %v, want (30,110)-(80,160) ±1", box)
	}
}

func TestCFF_BrokenProgramDrawsNothing(t *testing.T) {
	good := cffTestFontR().Bytes()
	t1 := cffTestFontR()
	t1.CharstringType = 1
	for name, prog := range map[string][]byte{
		"truncated":        good[:len(good)/2],
		"header only":      good[:4],
		"CharstringType 1": t1.Bytes(),
		"bad INDEX":        append([]byte{1, 0, 4, 4, 0, 1, 9}, good[7:]...),
		"garbage":          append([]byte{1, 0, 4, 4}, []byte(strings.Repeat("\xff\x00", 64))...),
	} {
		t.Run(name, func(t *testing.T) {
			img := renderPDF(t, cffPDF(prog, "", "BT /F1 100 Tf 20 30 Td (ABC) Tj 0 0 m 10 0 l 10 10 l f ET"))
			if box, ink := inkBox(img, image.Rect(15, 0, 200, 200)); ink {
				t.Errorf("a broken program drew ink at %v", box)
			}
			if _, ok := inkBox(img, image.Rect(0, 190, 10, 200)); !ok {
				t.Error("the path after the text did not render")
			}
		})
	}
}

func TestCFF_LazyLoadAndReferenceCache(t *testing.T) {
	prog := cidTestFontR().Bytes()
	fontFile := fmt.Sprintf("<< /Length %d /Subtype /CIDFontType0C >>\nstream\n%s\nendstream", len(prog), prog)
	desc := "<< /Type /FontDescriptor /FontFile3 6 0 R >>"
	cid := "<< /Type /Font /Subtype /CIDFontType0 /FontDescriptor 7 0 R >>"
	font := "<< /Type /Font /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [8 0 R] >>"
	font2 := "<< /Type /Font /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [8 0 R] /BaseFont /Other >>"

	once := decodedBy(t, fontsPDF("<< /F1 9 0 R >>", "BT /F1 10 Tf <01F4> Tj ET", fontFile, desc, cid, font))
	// An unfiltered program is not copied; the charset and CID map are
	// charged.
	if once <= 0 {
		t.Fatalf("one load charged %d decoded bytes, want > 0", once)
	}
	for _, tt := range []struct {
		name, fonts, content string
		want                 int64
	}{
		{"unused fonts are not loaded", "<< /F1 9 0 R /F2 10 0 R >>", "BT ET", 0},
		{"one reference under two names", "<< /F1 9 0 R /F2 9 0 R >>", "BT /F1 10 Tf <01F4> Tj /F2 10 Tf <01F4> Tj ET", once},
		{"two fonts share one program", "<< /F1 9 0 R /F2 10 0 R >>", "BT /F1 10 Tf <01F4> Tj /F2 10 Tf <01F4> Tj ET", once},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := decodedBy(t, fontsPDF(tt.fonts, tt.content, fontFile, desc, cid, font, font2))
			if got != tt.want {
				t.Errorf("decoded bytes = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCFF_GlyphCacheChargesOnce checks a glyph's charstring operators are
// charged once per render however often it is drawn.
func TestCFF_GlyphCacheChargesOnce(t *testing.T) {
	paint := func(content string) int64 {
		r, err := parse.NewResolver(cffPDF(cffTestFontR().Bytes(), "", content))
		if err != nil {
			t.Fatal(err)
		}
		pages, _ := r.Pages()
		data, _ := r.PageContent(pages[0])
		rs := newRaster(200, 200)
		run(rs, identity(), parse.ScanContent(data), r, pages[0].Resources)
		return rs.b.paint
	}
	one := paint("BT /F1 10 Tf 3 Tr (A) Tj ET")
	many := paint("BT /F1 10 Tf 3 Tr (" + strings.Repeat("A", 100) + ") Tj ET")
	if one == 0 || many != one {
		t.Errorf("paint for 1 glyph = %d, for 100 of the same glyph = %d; want equal and > 0", one, many)
	}
}

func TestFlattenCubicAdaptive(t *testing.T) {
	p := func(x, y float64) point { return point{x, y} }
	// A large curve uses cubicSegments and exactly flattenCubic's points.
	big := []point{p(0, 0), p(0, 300), p(300, 300), p(300, 0)}
	got := flattenCubicAdaptive(nil, big[0], big[1], big[2], big[3])
	want := flattenCubic(nil, big[0], big[1], big[2], big[3])
	if len(got) != cubicSegments || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("large curve: %d points, want flattenCubic's %d", len(got), len(want))
	}
	// A straight "curve" is one segment; a NaN control point the maximum.
	if n := len(flattenCubicAdaptive(nil, p(0, 0), p(1, 1), p(2, 2), p(3, 3))); n != 1 {
		t.Errorf("straight curve: %d segments, want 1", n)
	}
	if n := len(flattenCubicAdaptive(nil, p(0, 0), p(math.NaN(), 1), p(2, 2), p(3, 3))); n != cubicSegments {
		t.Errorf("NaN control point: %d segments, want %d", n, cubicSegments)
	}
	// Small curves: fewer segments, every curve point within the tolerance
	// of the polyline.
	for _, c := range [][4]point{
		{p(0, 0), p(0, 3), p(3, 3), p(3, 0)},
		{p(0, 0), p(10, 0), p(10, 10), p(0, 10)},
		{p(0, 0), p(2, 7), p(-5, 4), p(6, 1)},
		{p(0, 0), p(40, 0), p(40, 40), p(0, 40)},
	} {
		poly := append([]point{c[0]}, flattenCubicAdaptive(nil, c[0], c[1], c[2], c[3])...)
		if len(poly)-1 >= cubicSegments && c[1].X < 20 {
			t.Errorf("curve %v: %d segments, want fewer than %d", c, len(poly)-1, cubicSegments)
		}
		for i := 0; i <= 1000; i++ {
			tt := float64(i) / 1000
			u := 1 - tt
			q := p(u*u*u*c[0].X+3*u*u*tt*c[1].X+3*u*tt*tt*c[2].X+tt*tt*tt*c[3].X,
				u*u*u*c[0].Y+3*u*u*tt*c[1].Y+3*u*tt*tt*c[2].Y+tt*tt*tt*c[3].Y)
			if d := distToPolyline(q, poly); d > flattenTolerance+1e-9 {
				t.Fatalf("curve %v (%d segments): point at t=%v is %v px from the polyline", c, len(poly)-1, tt, d)
			}
		}
	}
}

func distToPolyline(q point, poly []point) float64 {
	best := math.Inf(1)
	for i := 0; i+1 < len(poly); i++ {
		a, b := poly[i], poly[i+1]
		dx, dy := b.X-a.X, b.Y-a.Y
		l := dx*dx + dy*dy
		s := 0.0
		if l > 0 {
			s = math.Max(0, math.Min(1, ((q.X-a.X)*dx+(q.Y-a.Y)*dy)/l))
		}
		best = math.Min(best, math.Hypot(q.X-(a.X+s*dx), q.Y-(a.Y+s*dy)))
	}
	return best
}

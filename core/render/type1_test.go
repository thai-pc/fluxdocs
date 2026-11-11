package render

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	tb "github.com/thai-pc/fluxdocs/core/internal/psfont/t1build"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// Glyphs of the hand-built Type 1 test font. Coordinates are glyph units
// (1/1000 em with the default FontMatrix).
var (
	// t1Square fills (100,100)–(600,600).
	t1Square = tb.CS(0, 700, tb.HSBW, 100, 100, tb.RMoveTo, 500, tb.HLineTo, 500, tb.VLineTo, -500, tb.HLineTo, tb.ClosePath, tb.EndChar)
	// t1Ring is (100,100)–(700,700) with a (300,300)–(500,500) hole.
	t1Ring = tb.CS(0, 800, tb.HSBW, 100, 100, tb.RMoveTo, 600, tb.HLineTo, 600, tb.VLineTo, -600, tb.HLineTo, tb.ClosePath,
		200, -400, tb.RMoveTo, 200, tb.VLineTo, 200, tb.HLineTo, -200, tb.VLineTo, tb.ClosePath, tb.EndChar)
	// t1Base (StandardEncoding "A") is (0,0)–(500,300); t1Accent ("acute")
	// starts at its sidebearing point (50, 0) and covers (50,0)–(250,100) in
	// its own glyph space; t1Composite places it with sbx 0, asb 50, adx 200,
	// ady 500: the accent's origin goes to (0 + 200 − 50, 500), its
	// sidebearing point lands at (200, 500), and it covers (200,500)–(400,600)
	// (Ghostscript agrees: TestType1_GhostscriptOracle/seac).
	t1Base      = tb.CS(0, 600, tb.HSBW, 0, 0, tb.RMoveTo, 500, tb.HLineTo, 300, tb.VLineTo, -500, tb.HLineTo, tb.ClosePath, tb.EndChar)
	t1Accent    = tb.CS(50, 300, tb.HSBW, 0, 0, tb.RMoveTo, 200, tb.HLineTo, 100, tb.VLineTo, -200, tb.HLineTo, tb.ClosePath, tb.EndChar)
	t1Composite = tb.CS(0, 600, tb.HSBW, 50, 200, 500, 65, 194, tb.Seac)
)

// t1TestFont is the font the render tests embed: "square" at code 0x41 (A)
// in its built-in encoding, "ring" at 0x42, "Aacute" at 0x43, and the seac
// components under their StandardEncoding names.
func t1TestFont() tb.Font {
	return tb.Font{
		Name:     "FluxRenderTest",
		Encoding: map[int]string{0x41: "square", 0x42: "ring", 0x43: "Aacute", 0x44: "renamed"},
		Glyphs: []tb.Glyph{
			{Name: ".notdef", CS: tb.CS(0, 500, tb.HSBW, tb.EndChar)},
			{Name: "square", CS: t1Square},
			{Name: "ring", CS: t1Ring},
			{Name: "A", CS: t1Base},
			{Name: "acute", CS: t1Accent},
			{Name: "Aacute", CS: t1Composite},
			{Name: "renamed", CS: tb.CS(0, 700, tb.HSBW, tb.EndChar)}, // empty: /Differences test
		},
	}
}

// t1PDF builds a 200x200 page that shows content with font as /F1, an
// embedded /FontFile whose font dictionary carries fontExtra.
func t1PDF(font []byte, l1, l2, l3 int, fontExtra, content string) []byte {
	return buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /FluxRenderTest /FontDescriptor 6 0 R " + fontExtra + " >>",
		5: streamObj(content),
		6: "<< /Type /FontDescriptor /FontName /FluxRenderTest /Flags 4 /FontBBox [0 -250 1000 900] /FontFile 7 0 R >>",
		7: fmt.Sprintf("<< /Length %d /Length1 %d /Length2 %d /Length3 %d >>\nstream\n%s\nendstream", len(font), l1, l2, l3, font),
	}, "<< /Size 8 /Root 1 0 R >>")
}

func renderPDF(t *testing.T, pdf []byte) image.Image {
	t.Helper()
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	data, err := Page(r, pages[0], Options{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// renderT1 renders content with t1TestFont as /F1.
func renderT1(t *testing.T, fontExtra, content string) image.Image {
	t.Helper()
	f := t1TestFont()
	l1, l2, l3 := f.Lengths()
	return renderPDF(t, t1PDF(f.Bytes(), l1, l2, l3, fontExtra, content))
}

// inkBox returns the bounding box of the dark pixels in r, and false when
// there are none.
func inkBox(img image.Image, r image.Rectangle) (image.Rectangle, bool) {
	box := image.Rectangle{}
	found := false
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 < 128 && cg>>8 < 128 && cb>>8 < 128 {
				p := image.Rect(x, y, x+1, y+1)
				if !found {
					box, found = p, true
				} else {
					box = box.Union(p)
				}
			}
		}
	}
	return box, found
}

// near reports whether two rectangles agree within ±1 px on every edge.
func near(a, b image.Rectangle) bool {
	d := func(x, y int) bool { return x-y >= -1 && x-y <= 1 }
	return d(a.Min.X, b.Min.X) && d(a.Min.Y, b.Min.Y) && d(a.Max.X, b.Max.X) && d(a.Max.Y, b.Max.Y)
}

// At 72 DPI on a 200 pt page, user (x, y) is device (x, 200 − y). A glyph at
// 100 pt maps glyph units g to g/10 px.

func TestType1_SquareInkBox(t *testing.T) {
	f := t1TestFont()
	l1, l2, l3 := f.Lengths()
	hex, pfb, badLen := f, f, f
	hex.Hex, pfb.PFB = true, true
	want := image.Rect(20+10, 200-30-60, 20+60, 200-30-10) // origin (20, 30), square 10..60 px
	for _, tt := range []struct {
		name string
		pdf  []byte
	}{
		{"binary", t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"hex", t1PDF(hex.Bytes(), 0, 0, 0, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"pfb", t1PDF(pfb.Bytes(), 0, 0, 0, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"wrong Length1", t1PDF(badLen.Bytes(), l1-40, l2+40, 0, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
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

func TestType1_CounterIsHollow(t *testing.T) {
	img := renderT1(t, "", "BT /F1 100 Tf 20 30 Td (B) Tj ET")
	outer, ok := inkBox(img, img.Bounds())
	if want := image.Rect(30, 100, 90, 160); !ok || !near(outer, want) {
		t.Fatalf("ring ink box = %v, want %v ±1", outer, want)
	}
	// The hole is (300,300)–(500,500) glyph units: device (50..70, 120..140).
	if hole, dark := inkBox(img, image.Rect(52, 122, 68, 138)); dark {
		t.Errorf("ink inside the counter at %v", hole)
	}
}

func TestType1_SeacAccentAboveBase(t *testing.T) {
	img := renderT1(t, "", "BT /F1 100 Tf 20 30 Td (C) Tj ET")
	// Base (0,0)–(500,300): device x 20..70, y 200−30−30..200−30 = 140..170.
	if base, ok := inkBox(img, image.Rect(0, 135, 200, 200)); !ok || !near(base, image.Rect(20, 140, 70, 170)) {
		t.Errorf("base ink box = %v, want (20,140)-(70,170)", base)
	}
	// Accent (200,500)–(400,600): device x 40..60, y 110..120.
	if acc, ok := inkBox(img, image.Rect(0, 0, 200, 135)); !ok || !near(acc, image.Rect(40, 110, 60, 120)) {
		t.Errorf("accent ink box = %v, want (40,110)-(60,120)", acc)
	}
}

func TestType1_WidthsOverrideProgramAdvance(t *testing.T) {
	// The square advances 700 in the program; /Widths says 1000: the second
	// square starts 100 px (not 70) to the right at 100 pt.
	for _, tt := range []struct {
		name, extra string
		want        int
	}{
		{"widths", "/FirstChar 65 /LastChar 65 /Widths [1000]", 100},
		{"program advance", "", 70},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runs := inkRuns(renderT1(t, tt.extra, "BT /F1 100 Tf 0 30 Td (AA) Tj ET"))
			if len(runs) != 2 {
				t.Fatalf("ink runs = %v, want 2 glyphs", runs)
			}
			if d := runs[1][0] - runs[0][0]; d < tt.want-1 || d > tt.want+1 {
				t.Errorf("glyph spacing = %d px, want %d ±1", d, tt.want)
			}
		})
	}
}

func TestType1_EncodingSelectsGlyph(t *testing.T) {
	for _, tt := range []struct {
		name, extra, content string
		ink                  bool
	}{
		// /Differences renames code 0x41 to the empty glyph.
		{"differences", "/Encoding << /Differences [65 /renamed] >>", "(A)", false},
		// /Differences maps code 0x61 to the square.
		{"differences add", "/Encoding << /Differences [97 /square] >>", "(a)", true},
		// WinAnsiEncoding: code 0x41 is "A", the seac base glyph.
		{"base encoding", "/Encoding /WinAnsiEncoding", "(A)", true},
		// The built-in encoding has nothing at 0x5A: .notdef (empty).
		{"unassigned", "", "(Z)", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := renderT1(t, tt.extra, "BT /F1 100 Tf 20 30 Td "+tt.content+" Tj ET")
			if _, ink := inkBox(img, img.Bounds()); ink != tt.ink {
				t.Errorf("ink = %v, want %v", ink, tt.ink)
			}
		})
	}
	// WinAnsiEncoding's A is the base glyph, not the square.
	img := renderT1(t, "/Encoding /WinAnsiEncoding", "BT /F1 100 Tf 20 30 Td (A) Tj ET")
	if box, _ := inkBox(img, img.Bounds()); !near(box, image.Rect(20, 140, 70, 170)) {
		t.Errorf("WinAnsi A ink box = %v, want the base glyph (20,140)-(70,170)", box)
	}
}

func TestType1_InvisibleDrawsNothing(t *testing.T) {
	img := renderT1(t, "", "BT /F1 100 Tf 3 Tr 20 30 Td (ABC) Tj ET")
	if box, ink := inkBox(img, img.Bounds()); ink {
		t.Errorf("invisible text drew ink at %v", box)
	}
}

func TestType1_FontMatrix2048(t *testing.T) {
	// The square in 2048 units per em with /FontMatrix 1/2048 renders the
	// same size as the 1000-unit square.
	s := func(v int) int { return v * 2048 / 1000 }
	f := tb.Font{
		Matrix:   "[0.00048828125 0 0 0.00048828125 0 0]",
		Encoding: map[int]string{0x41: "square"},
		Glyphs: []tb.Glyph{{Name: "square", CS: tb.CS(0, s(700), tb.HSBW, s(100), s(100), tb.RMoveTo, s(500), tb.HLineTo,
			s(500), tb.VLineTo, -s(500), tb.HLineTo, tb.ClosePath, tb.EndChar)}},
	}
	l1, l2, l3 := f.Lengths()
	img := renderPDF(t, t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET"))
	if box, ok := inkBox(img, img.Bounds()); !ok || !near(box, image.Rect(30, 110, 80, 160)) {
		t.Errorf("ink box = %v, want (30,110)-(80,160) ±1", box)
	}
}

func TestType1_BrokenProgramDrawsNothing(t *testing.T) {
	f := t1TestFont()
	data := f.Bytes()
	i := bytes.Index(data, []byte("eexec"))
	// An entry length of MaxInt64 once overflowed the parser's bounds check
	// into a panic (ErrInternal for the page); the font is now dropped.
	hugeLen := append([]byte("%!PS-AdobeFont-1.0\ncurrentfile eexec\n"),
		tb.Encrypt([]byte("/lenIV -1 def /CharStrings 1 dict dup begin /A 9223372036854775807 RD x ND end"), 55665, []byte{1, 2, 3, 4})...)
	for name, prog := range map[string][]byte{
		"truncated":             data[:i+20],
		"no eexec":              data[:i],
		"garbage":               []byte(strings.Repeat("\x80\x01\xff\xff", 64)),
		"not a font":            []byte("%!PS-AdobeFont-1.0\nhello"),
		"cff sniffed":           {1, 0, 4, 1, 0, 0, 0, 0},
		"MaxInt64 entry length": hugeLen,
	} {
		t.Run(name, func(t *testing.T) {
			img := renderPDF(t, t1PDF(prog, len(prog), 0, 0, "", "BT /F1 100 Tf 20 30 Td (ABC) Tj 0 0 m 10 0 l 10 10 l f ET"))
			box, ink := inkBox(img, image.Rect(15, 0, 200, 200))
			if ink {
				t.Errorf("a broken program drew ink at %v", box)
			}
			// The rest of the page still renders.
			if _, ok := inkBox(img, image.Rect(0, 190, 10, 200)); !ok {
				t.Error("the path after the text did not render")
			}
		})
	}
}

// fontsPDF builds a page whose /Font dictionary is fontDict and whose
// remaining objects (from 6 on) are extra, showing content.
func fontsPDF(fontDict string, content string, extra ...string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font " + fontDict + " >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: "<< >>",
	}
	for i, e := range extra {
		objs[6+i] = e
	}
	return buildRenderPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1))
}

// decodedBy renders pdf's page and returns the bytes charged to decoded
// bytes.
func decodedBy(t *testing.T, pdf []byte) int64 {
	t.Helper()
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	content, err := r.PageContent(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	rs := newRaster(200, 200)
	run(rs, identity(), parse.ScanContent(content), r, pages[0].Resources)
	if rs.b.err != nil {
		t.Fatal(rs.b.err)
	}
	return rs.b.decoded
}

func TestType1_LazyLoadAndReferenceCache(t *testing.T) {
	f := t1TestFont()
	prog := f.Bytes()
	fontFile := fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(prog), prog)
	desc := "<< /Type /FontDescriptor /FontFile 6 0 R >>"
	font := "<< /Type /Font /Subtype /Type1 /FontDescriptor 7 0 R >>"
	font2 := "<< /Type /Font /Subtype /Type1 /FontDescriptor 7 0 R /Encoding /WinAnsiEncoding >>"

	once := decodedBy(t, fontsPDF("<< /F1 8 0 R >>", "BT /F1 10 Tf (A) Tj ET", fontFile, desc, font))
	// An unfiltered stream is free; the eexec-decrypted and charstring-decrypted
	// copies are charged.
	if once <= 0 || once > 2*int64(len(prog)) {
		t.Fatalf("one load charged %d decoded bytes, want (0, %d]", once, 2*len(prog))
	}
	for _, tt := range []struct {
		name, fonts, content string
		want                 int64
	}{
		{"unused fonts are not loaded", "<< /F1 8 0 R /F2 8 0 R /F3 9 0 R >>", "BT ET", 0},
		{"one reference under two names", "<< /F1 8 0 R /F2 8 0 R >>", "BT /F1 10 Tf (A) Tj /F2 10 Tf (A) Tj ET", once},
		{"two fonts share one program", "<< /F1 8 0 R /F2 9 0 R >>", "BT /F1 10 Tf (A) Tj /F2 10 Tf (A) Tj ET", once},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := decodedBy(t, fontsPDF(tt.fonts, tt.content, fontFile, desc, font, font2))
			if got != tt.want {
				t.Errorf("decoded bytes = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestType1_GlyphCacheChargesOnce checks a glyph's charstring operators are
// charged once per render however often it is drawn.
func TestType1_GlyphCacheChargesOnce(t *testing.T) {
	paint := func(content string) int64 {
		f := t1TestFont()
		l1, l2, l3 := f.Lengths()
		r, err := parse.NewResolver(t1PDF(f.Bytes(), l1, l2, l3, "", content))
		if err != nil {
			t.Fatal(err)
		}
		pages, _ := r.Pages()
		data, _ := r.PageContent(pages[0])
		rs := newRaster(200, 200)
		run(rs, identity(), parse.ScanContent(data), r, pages[0].Resources)
		return rs.b.paint
	}
	// Invisible text with /Widths absent: the advance needs the glyph
	// (hsbw), so the charstring runs, but nothing is painted.
	one := paint("BT /F1 10 Tf 3 Tr (A) Tj ET")
	many := paint("BT /F1 10 Tf 3 Tr (" + strings.Repeat("A", 100) + ") Tj ET")
	if one == 0 || many != one {
		t.Errorf("paint for 1 glyph = %d, for 100 of the same glyph = %d; want equal and > 0", one, many)
	}
}

// manyFontsPage builds a page whose /Font resources list n font dictionaries
// that all share one embedded Type 1 program (t1TestFont), and returns their
// resource names.
func manyFontsPage(tb testing.TB, n int) (*parse.Resolver, parse.PageInfo, []parse.Name) {
	tb.Helper()
	prog := t1TestFont().Bytes()
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		4: streamObj("BT ET"),
		5: "<< /Type /FontDescriptor /FontFile 6 0 R >>",
		6: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(prog), prog),
	}
	var fonts strings.Builder
	names := make([]parse.Name, n)
	for i := range names {
		names[i] = parse.Name(fmt.Sprintf("F%d", i))
		objs[7+i] = "<< /Type /Font /Subtype /Type1 /FontDescriptor 5 0 R >>"
		fmt.Fprintf(&fonts, "/F%d %d 0 R ", i, 7+i)
	}
	objs[3] = "<< /Type /Page /Parent 2 0 R /Resources << /Font << " + fonts.String() + ">> >> /Contents 4 0 R >>"
	r, err := parse.NewResolver(buildRenderPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", 7+n)))
	if err != nil {
		tb.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		tb.Fatal(err)
	}
	return r, pages[0], names
}

// TestType1_FontLoadsChargedAndCapped checks every font dictionary loaded is
// charged fontLoadCost, even when its program is shared and already parsed,
// that at most maxFontLoads are loaded per render (S1), and that a load the
// budget refuses selects no font.
func TestType1_FontLoadsChargedAndCapped(t *testing.T) {
	r, page, names := manyFontsPage(t, maxFontLoads+10)
	rs := newRaster(200, 200)
	sc := newFontLoader(r, rs.b).scope(page.Resources)
	for i, n := range names {
		gf := sc.font(n)
		if (gf != nil) != (i < maxFontLoads) {
			t.Fatalf("font %d of %d loaded = %v, want %v (cap %d)", i, len(names), gf != nil, i < maxFontLoads, maxFontLoads)
		}
	}
	if want := int64(maxFontLoads) * fontLoadCost; rs.b.paint != want {
		t.Errorf("paint charged %d, want %d (maxFontLoads × fontLoadCost)", rs.b.paint, want)
	}
	if rs.b.stopped() {
		t.Errorf("the cap tripped the budget: %v", rs.b.err)
	}

	b := newBudget(nil, budgetLimits{paint: 10 * fontLoadCost, steps: unlimited, samples: unlimited, content: unlimited, decoded: unlimited, pathPoints: unlimited})
	sc = newFontLoader(r, b).scope(page.Resources)
	loaded := 0
	for _, n := range names[:20] {
		if sc.font(n) != nil {
			loaded++
		}
	}
	if loaded != 10 || !b.stopped() {
		t.Errorf("under a budget of 10 loads: %d loaded, stopped %v; want 10, true", loaded, b.stopped())
	}
}

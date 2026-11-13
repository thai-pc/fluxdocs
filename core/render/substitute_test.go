package render

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/internal/std14"
	"github.com/thai-pc/fluxdocs/core/internal/std14/faces"
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/sfnt"
)

// substitutePDF builds a 300x100 page whose /F1 is the font dictionary
// fontDict (object 4); desc, when not empty, is object 6, which fontDict
// may reference as its /FontDescriptor.
func substitutePDF(fontDict, desc, content string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: fontDict,
		5: streamObj(content),
	}
	trailer := "<< /Size 6 /Root 1 0 R >>"
	if desc != "" {
		objs[6] = desc
		trailer = "<< /Size 7 /Root 1 0 R >>"
	}
	return buildRenderPDF(objs, trailer)
}

// loadF1 loads /F1 of a substitutePDF page through the font loader.
func loadF1(t *testing.T, fontDict, desc string) *glyphFont {
	t.Helper()
	r, err := parse.NewResolver(substitutePDF(fontDict, desc, ""))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	return newFontLoader(r, unlimitedBudget()).scope(pages[0].Resources).font("F1")
}

// renderSubstitute renders a substitutePDF page at 72 DPI.
func renderSubstitute(t *testing.T, fontDict, desc, content string) image.Image {
	t.Helper()
	r, err := parse.NewResolver(substitutePDF(fontDict, desc, content))
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

// TestSubstitute_Standard14DrawInk shows text in each of the 14 standard
// fonts, not embedded and without /Widths: each draws ink (with the faces
// compiled in), and with -tags nostd14 none does.
func TestSubstitute_Standard14DrawInk(t *testing.T) {
	for f := std14.Face(0); f < std14.NumFaces; f++ {
		t.Run(f.String(), func(t *testing.T) {
			img := renderSubstitute(t, "<< /Type /Font /Subtype /Type1 /BaseFont /"+f.String()+" >>", "",
				"BT /F1 40 Tf 10 30 Td (Abg!) Tj ET")
			got := darkPixels(img)
			if faces.Enabled && got < 100 {
				t.Errorf("dark pixels = %d, want >= 100", got)
			}
			if !faces.Enabled && got != 0 {
				t.Errorf("nostd14: dark pixels = %d, want 0", got)
			}
		})
	}
}

// TestSubstitute_AFMWidths checks the advance order of design §2.1 step 3 for
// a non-embedded standard font: /Widths, then the AFM width of the code's
// glyph name, then /MissingWidth, then 0.5 em, never the substitute face's
// own advance; identical with and without the faces compiled in.
func TestSubstitute_AFMWidths(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	gf := loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "")
	if gf == nil || !gf.std14 {
		t.Fatalf("Helvetica: font %v, want a std14 substitute", gf)
	}
	for _, c := range []struct {
		code byte
		want float64
	}{
		{'A', 0.667}, {'i', 0.222}, {' ', 0.278}, {'W', 0.944},
		{0xA4, 0.167}, // StandardEncoding fraction
		{0x80, 0.5},   // unassigned in StandardEncoding: no AFM width, no /MissingWidth
	} {
		gid, ok := gf.gid(uint32(c.code))
		if got := gf.advance(uint32(c.code), gid, ok); !near(got, c.want) {
			t.Errorf("Helvetica %#x: advance %v, want %v", c.code, got, c.want)
		}
	}

	// /Widths wins where given; the AFM fills codes outside FirstChar–LastChar.
	gf = loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Times-Bold /FirstChar 65 /LastChar 65 /Widths [500] /Encoding /WinAnsiEncoding >>", "")
	for _, c := range []struct {
		code byte
		want float64
	}{{'A', 0.5}, {'B', 0.667}, {0x80, 0.5 /* Euro */}, {0xE9, 0.444}} {
		gid, ok := gf.gid(uint32(c.code))
		if got := gf.advance(uint32(c.code), gid, ok); !near(got, c.want) {
			t.Errorf("Times-Bold %#x: advance %v, want %v", c.code, got, c.want)
		}
	}

	// /MissingWidth covers a name the AFM lacks.
	gf = loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /FontDescriptor 6 0 R /Encoding << /Differences [65 /nosuchglyph] >> >>",
		"<< /Type /FontDescriptor /FontName /Courier /Flags 33 /MissingWidth 250 >>")
	gid, ok := gf.gid('A')
	if got := gf.advance('A', gid, ok); !near(got, 0.25) {
		t.Errorf("Courier /nosuchglyph: advance %v, want 0.25 (MissingWidth)", got)
	}
	gid, ok = gf.gid('B')
	if got := gf.advance('B', gid, ok); !near(got, 0.6) {
		t.Errorf("Courier B: advance %v, want 0.6", got)
	}

	// Symbol and ZapfDingbats use their built-in encodings and AFMs.
	gf = loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Symbol >>", "")
	if gid, ok := gf.gid('a'); !near(gf.advance('a', gid, ok), 0.631) {
		t.Errorf("Symbol a (alpha): advance %v, want 0.631", gf.advance('a', gid, ok))
	}
	gf = loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >>", "")
	if gid, ok := gf.gid('!'); !near(gf.advance('!', gid, ok), 0.974) {
		t.Errorf("ZapfDingbats ! (a1): advance %v, want 0.974", gf.advance('!', gid, ok))
	}
}

// TestSubstitute_AdvanceMovesPen renders "AA" in non-embedded Helvetica at
// 100 pt: the second A starts 66.7 px (AFM width 667) after the first.
func TestSubstitute_AdvanceMovesPen(t *testing.T) {
	if !faces.Enabled {
		t.Skip("faces not compiled in (nostd14)")
	}
	img := renderSubstitute(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "",
		"BT /F1 100 Tf 10 20 Td (AA) Tj ET")
	runs := inkRuns(img)
	if len(runs) != 2 {
		t.Fatalf("ink runs %v, want 2", runs)
	}
	if d := runs[1][0] - runs[0][0]; d < 66 || d > 68 {
		t.Errorf("second A starts %d px after the first, want ≈ 66.7", d)
	}
}

// TestSubstitute_Choice checks the face selection of design §2.4.
func TestSubstitute_Choice(t *testing.T) {
	for _, c := range []struct {
		base, desc string
		face       std14.Face
		standard   bool
		draw       bool
	}{
		{"Helvetica", "", std14.Helvetica, true, true},
		{"ABCDEF+Arial,BoldItalic", "", std14.HelveticaBoldOblique, true, true},
		{"TimesNewRomanPS-BoldMT", "", std14.TimesBold, true, true},
		{"Verdana", "<< /Flags 32 >>", std14.Helvetica, false, true},
		{"Georgia", "<< /Flags 34 >>", std14.TimesRoman, false, true},
		{"Consolas", "<< /Flags 33 >>", std14.Courier, false, true},
		{"Georgia-Italic", "<< /Flags 34 >>", std14.TimesItalic, false, true},
		{"Garamond", "<< /Flags 34 /ItalicAngle -12 >>", std14.TimesItalic, false, true},
		{"Garamond", "<< /Flags 98 >>", std14.TimesItalic, false, true},
		{"Garamond", "<< /Flags 262178 >>", std14.TimesBold, false, true},
		{"Tahoma-Semibold", "<< /Flags 32 >>", std14.HelveticaBold, false, true},
		{"Arial-Black", "<< /Flags 32 >>", std14.HelveticaBold, false, true},
		{"Wingdings", "<< /Flags 4 >>", 0, false, false},
		{"Wingdings", "<< /Flags 36 >>", std14.Helvetica, false, true}, // nonsymbolic too
		{"Symbol", "<< /Flags 4 >>", std14.Symbol, true, true},         // by name
		{"NoDescriptor", "", std14.Helvetica, false, true},
	} {
		r, err := parse.NewResolver(substitutePDF("<< /Type /Font /Subtype /Type1 >>", "", ""))
		if err != nil {
			t.Fatal(err)
		}
		fd := parse.Dict{"BaseFont": parse.Name(c.base)}
		var fdesc parse.Dict
		if c.desc != "" {
			obj, err := parse.NewResolver(substitutePDF(c.desc, "", ""))
			if err != nil {
				t.Fatal(err)
			}
			fdesc, _, _ = obj.ResolveDict(parse.Reference{Number: 4})
		}
		got := chooseSubstitute(r, fd, fdesc)
		want := substituteChoice{face: c.face, standard: c.standard, draw: c.draw}
		if !c.draw {
			want.face = 0
		}
		if got != want {
			t.Errorf("%s %s: %+v, want %+v", c.base, c.desc, got, want)
		}
	}
}

// TestSubstitute_SymbolicUnknownNotDrawn: a non-embedded symbolic font that
// is not Symbol or ZapfDingbats draws nothing, but its text advances by its
// /Widths (and other fonts' text after it lands where it should).
func TestSubstitute_SymbolicUnknownNotDrawn(t *testing.T) {
	img := renderSubstitute(t, "<< /Type /Font /Subtype /TrueType /BaseFont /Wingdings /FontDescriptor 6 0 R /FirstChar 65 /LastChar 65 /Widths [800] >>",
		"<< /Type /FontDescriptor /FontName /Wingdings /Flags 4 >>", "BT /F1 40 Tf 10 30 Td (AAA) Tj ET")
	if got := darkPixels(img); got != 0 {
		t.Errorf("dark pixels = %d, want 0", got)
	}
	gf := loadF1(t, "<< /Type /Font /Subtype /TrueType /BaseFont /Wingdings /FontDescriptor 6 0 R /FirstChar 65 /LastChar 65 /Widths [800] >>",
		"<< /Type /FontDescriptor /FontName /Wingdings /Flags 4 >>")
	if gf == nil {
		t.Fatal("no font: the text would not advance")
	}
	gid, ok := gf.gid('A')
	if ok {
		t.Error("a glyph for A, want none")
	}
	if got := gf.advance('A', gid, ok); got != 0.8 {
		t.Errorf("advance %v, want 0.8", got)
	}
}

// TestSubstitute_Fit checks the fit rule (design §2.4): a glyph whose
// substitute advance exceeds the PDF width by more than 10% is compressed to
// w0 / advance, not below 0.6; a narrower width never widens it.
func TestSubstitute_Fit(t *testing.T) {
	if !faces.Enabled {
		t.Skip("faces not compiled in (nostd14)")
	}
	// Helvetica M is 833 in the AFM; the substitute's M is about as wide.
	gf := loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 77 /LastChar 79 /Widths [600 300 2000] >>", "")
	p := gf.prog.(*substituteProgram)
	adv := p.advanceEm('M')
	if got, want := float64(p.fit['M']), 0.6/adv; math.Abs(got-want) > 1e-6 {
		t.Errorf("M at 600: fit %v, want %v", got, want)
	}
	if got := p.fit['N']; got != minFit {
		t.Errorf("N at 300: fit %v, want %v (clamped)", got, minFit)
	}
	if got := p.fit['O']; got != 1 {
		t.Errorf("O at 2000: fit %v, want 1 (never widened)", got)
	}
	gf = loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "")
	p = gf.prog.(*substituteProgram)
	// Letters and digits are metric-compatible (Liberation Sans matches
	// Arial, whose widths match Helvetica's); some spacing accents are wider
	// in the substitute (macron) and are compressed.
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789" {
		if p.gids[c] == 0 || p.fit[c] != 1 {
			t.Errorf("Helvetica %q with AFM widths: glyph %d fit %v, want a glyph at fit 1", c, p.gids[c], p.fit[c])
		}
	}
	if p.fit[0305] >= 1 {
		t.Errorf("Helvetica macron: fit %v, want < 1 (the substitute's macron is wider than the AFM's 333)", p.fit[0305])
	}
}

// TestSubstitute_GlyphsInsideRedactionBox is the substitute-box gate of
// design §4.6: every glyph of every bundled face, unscaled (the largest fit
// scale), lies inside the box the redaction model allows a substituted font
// (fontmodel's Ink for a non-embedded font, which covers substitutedBBox).
func TestSubstitute_GlyphsInsideRedactionBox(t *testing.T) {
	if !faces.Enabled {
		t.Skip("faces not compiled in (nostd14)")
	}
	r, err := parse.NewResolver(substitutePDF("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	m := fontmodel.Load(r, parse.Reference{Number: 4}, fontmodel.Options{Metrics: true})
	x0, y0, x1, y1 := m.Ink()
	if x0 > -0.3 || y0 > -0.45 || x1 < 1.45 || y1 < 1.25 {
		t.Fatalf("fontmodel Ink for a substituted font = [%v %v %v %v], smaller than this gate expects", x0, y0, x1, y1)
	}
	var buf sfnt.Buffer
	for f := std14.Face(0); f < std14.NumFaces; f++ {
		sf := substituteFace(f)
		if sf == nil {
			t.Fatalf("%v: face did not parse", f)
		}
		for g := 0; g < sf.NumGlyphs(); g++ {
			segs, err := sf.LoadGlyph(&buf, sfnt.GlyphIndex(g), glyphPPEM, nil)
			if err != nil {
				t.Fatalf("%v glyph %d: %v", f, g, err)
			}
			for _, s := range segs {
				for _, a := range s.Args[:segArgs(s.Op)] {
					// LoadGlyph's y axis points down; text space's up.
					x, y := float64(a.X)/64/1000, -float64(a.Y)/64/1000
					if x < x0 || x > x1 || y < y0 || y > y1 {
						t.Errorf("%v glyph %d: point (%.3f, %.3f) em outside [%v %v %v %v]", f, g, x, y, x0, y0, x1, y1)
					}
				}
			}
		}
	}
}

// segArgs is the number of points a segment of op uses.
func segArgs(op sfnt.SegmentOp) int {
	switch op {
	case sfnt.SegmentOpQuadTo:
		return 2
	case sfnt.SegmentOpCubeTo:
		return 3
	}
	return 1
}

// TestSubstitute_EmbeddedFontsUnchanged: a font with an embedded program that
// cannot be parsed, a Type 3 font and a non-embedded composite font (its
// /Subtype direct or indirect) are still not drawn (nil), as before Phase 3.
func TestSubstitute_EmbeddedFontsUnchanged(t *testing.T) {
	if gf := loadF1(t, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Helvetica /Flags 32 /FontFile 7 0 R >>"); gf != nil {
		t.Error("broken embedded program: font loaded, want nil")
	}
	if gf := loadF1(t, "<< /Type /Font /Subtype /Type3 /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << >> /FirstChar 65 /LastChar 65 /Widths [500] >>", ""); gf != nil {
		t.Error("Type 3: font loaded, want nil")
	}
	if gf := loadF1(t, "<< /Type /Font /Subtype /Type0 /BaseFont /MSMincho /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /MSMincho >>"); gf != nil {
		t.Error("non-embedded Type 0: font loaded, want nil")
	}

	// An indirect /Subtype is resolved, as fontmodel resolves it: a Type 0
	// font the redaction model reads as composite (2-byte codes, em ink
	// bounds) is not drawn one Helvetica glyph per byte (paint-over leak).
	indirectType0 := "<< /Type /Font /Subtype 6 0 R /BaseFont /Helvetica /Encoding /Identity-H >>"
	if gf := loadF1(t, indirectType0, "/Type0"); gf != nil {
		t.Errorf("indirect /Subtype Type0: font loaded (program %T), want nil", gf.prog)
	}
	if got := darkPixels(renderSubstitute(t, indirectType0, "/Type0", "BT /F1 40 Tf 10 30 Td (Abg!) Tj ET")); got != 0 {
		t.Errorf("indirect /Subtype Type0: dark pixels = %d, want 0", got)
	}
	// An indirect /Subtype Type1 is still a simple, substituted font.
	if gf := loadF1(t, "<< /Type /Font /Subtype 6 0 R /BaseFont /Helvetica >>", "/Type1"); gf == nil || !gf.std14 {
		t.Errorf("indirect /Subtype Type1: font %v, want a std14 substitute", gf)
	}
}

// TestSubstitute_MatchesModel: the renderer draws a font from a substitute
// exactly when the redaction model classifies it as substituted
// (fontmodel.Font.Substituted), over the descriptor shapes.
func TestSubstitute_MatchesModel(t *testing.T) {
	for _, tc := range []struct{ name, font, desc string }{
		{"no descriptor", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", ""},
		{"direct descriptor, no program", "<< /Type /Font /Subtype /Type1 /BaseFont /Arial /FontDescriptor << /Flags 32 >> >>", ""},
		{"indirect descriptor, no program", "<< /Type /Font /Subtype /TrueType /BaseFont /Arial /FontDescriptor 6 0 R >>", "<< /Flags 32 >>"},
		{"descriptor not a dict", "<< /Type /Font /Subtype /Type1 /BaseFont /Arial /FontDescriptor 6 0 R >>", "42"},
		{"direct FontFile", "<< /Type /Font /Subtype /Type1 /BaseFont /Arial /FontDescriptor << /FontFile 6 0 R >> >>", streamObj("x")},
		{"FontFile", "<< /Type /Font /Subtype /Type1 /BaseFont /Arial /FontDescriptor 6 0 R >>", "<< /FontFile 7 >>"},
		{"FontFile2", "<< /Type /Font /Subtype /TrueType /BaseFont /Arial /FontDescriptor 6 0 R >>", "<< /FontFile2 7 >>"},
		{"FontFile3", "<< /Type /Font /Subtype /Type1 /BaseFont /Arial /FontDescriptor 6 0 R >>", "<< /FontFile3 7 >>"},
		{"indirect /Subtype Type0", "<< /Type /Font /Subtype 6 0 R /BaseFont /Helvetica /Encoding /Identity-H >>", "/Type0"},
		{"indirect /Subtype Type1", "<< /Type /Font /Subtype 6 0 R /BaseFont /Helvetica >>", "/Type1"},
	} {
		r, err := parse.NewResolver(substitutePDF(tc.font, tc.desc, ""))
		if err != nil {
			t.Fatal(err)
		}
		model := fontmodel.Load(r, parse.Reference{Number: 4}, fontmodel.Options{Metrics: true})
		gf := loadF1(t, tc.font, tc.desc)
		drawn := false
		if gf != nil {
			_, sub := gf.prog.(*substituteProgram)
			_, null := gf.prog.(nullProgram)
			drawn = sub || null
		}
		if drawn != model.Substituted() {
			t.Errorf("%s: substitute %v, model Substituted %v", tc.name, drawn, model.Substituted())
		}
	}
}

// manySubstituteFontsPage builds a page declaring n distinct non-embedded
// font dictionaries, cycling through standard names (with /Widths) and a
// flag-chosen face, all sharing one descriptor and /Differences.
func manySubstituteFontsPage(tb testing.TB, n int) (*parse.Resolver, parse.PageInfo, []parse.Name) {
	tb.Helper()
	var widths, diffs strings.Builder
	for i := 0; i < 224; i++ {
		fmt.Fprintf(&widths, "%d ", 300+i)
	}
	diffs.WriteString("<< /BaseEncoding /WinAnsiEncoding /Differences [128 /alpha /Lslash /uni20AC /fi /a1] >>")
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		4: streamObj("BT ET"),
		5: "<< /Type /FontDescriptor /FontName /Verdana /Flags 32 >>",
		6: diffs.String(),
	}
	bases := []string{"Helvetica", "Times-Roman,Bold", "Arial-ItalicMT", "Courier", "Verdana"}
	var fonts strings.Builder
	names := make([]parse.Name, n)
	for i := range names {
		names[i] = parse.Name(fmt.Sprintf("F%d", i))
		objs[7+i] = fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /FirstChar 32 /LastChar 255 /Widths [%s] /Encoding 6 0 R /FontDescriptor 5 0 R >>",
			bases[i%len(bases)], widths.String())
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

// TestFlattenQuadAdaptive: a large quadratic gets quadSegments segments,
// equal to flattenQuad's; a nearly straight one gets one; and every point of
// a medium one lies on the curve, with the polyline within flattenTolerance
// of the curve at segment midpoints.
func TestFlattenQuadAdaptive(t *testing.T) {
	p0, c, p1 := point{0, 0}, point{500, 1000}, point{1000, 0}
	got := flattenQuadAdaptive(nil, p0, c, p1)
	want := flattenQuad(nil, p0, c, p1)
	if len(got) != quadSegments || len(want) != quadSegments {
		t.Fatalf("large curve: %d points, want %d", len(got), quadSegments)
	}
	for i := range got {
		if math.Abs(got[i].X-want[i].X) > 1e-9 || math.Abs(got[i].Y-want[i].Y) > 1e-9 {
			t.Errorf("point %d: %v, want %v", i, got[i], want[i])
		}
	}
	if got := flattenQuadAdaptive(nil, point{0, 0}, point{5, 0.01}, point{10, 0}); len(got) != 1 || got[0] != (point{10, 0}) {
		t.Errorf("flat curve: %v, want one segment to the end point", got)
	}
	bez := func(t float64) point {
		u := 1 - t
		return point{u*u*p0.X + 2*u*t*c.X + t*t*p1.X, u*u*p0.Y + 2*u*t*c.Y + t*t*p1.Y}
	}
	p0, c, p1 = point{0, 0}, point{3, 4}, point{6, 0}
	pts := append([]point{p0}, flattenQuadAdaptive(nil, p0, c, p1)...)
	n := len(pts) - 1
	if n < 2 || n >= quadSegments {
		t.Fatalf("medium curve: %d segments", n)
	}
	for i := 0; i < n; i++ {
		mid := point{(pts[i].X + pts[i+1].X) / 2, (pts[i].Y + pts[i+1].Y) / 2}
		on := bez((float64(i) + 0.5) / float64(n))
		if d := math.Hypot(mid.X-on.X, mid.Y-on.Y); d > flattenTolerance {
			t.Errorf("segment %d strays %v > %v", i, d, flattenTolerance)
		}
	}
	nan := flattenQuadAdaptive(nil, point{0, 0}, point{math.NaN(), 0}, point{1, 1})
	if len(nan) != quadSegments {
		t.Errorf("NaN control point: %d segments, want %d", len(nan), quadSegments)
	}
}

// TestSubstitute_LoadCharged: a non-embedded font dictionary costs
// fontLoadCost + substituteLoadCost raster work, once per render; a budget
// that cannot pay it loads no font.
func TestSubstitute_LoadCharged(t *testing.T) {
	r, page, names := manySubstituteFontsPage(t, 3)
	rs := newRaster(1, 1)
	sc := newFontLoader(r, rs.b).scope(page.Resources)
	for _, n := range append(names, names...) {
		if sc.font(n) == nil {
			t.Fatalf("%s not loaded", n)
		}
	}
	if want := int64(3) * (fontLoadCost + substituteLoadCost); rs.b.paint != want {
		t.Errorf("paint charged %d, want %d", rs.b.paint, want)
	}
	b := newBudget(nil, budgetLimits{paint: fontLoadCost + substituteLoadCost/2, steps: unlimited, samples: unlimited, content: unlimited, decoded: unlimited, pathPoints: unlimited, clipMask: unlimited})
	if gf := newFontLoader(r, b).scope(page.Resources).font(names[0]); gf != nil || !b.stopped() {
		t.Errorf("under a budget short of the substitute charge: font %v, stopped %v; want nil, true", gf, b.stopped())
	}
}

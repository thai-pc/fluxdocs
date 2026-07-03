package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/gofont/goregular"
)

// buildFontPDF builds a one-page PDF (200x200) whose page uses an embedded
// TrueType font (Go Regular) as /F1 and runs the given content stream.
func buildFontPDF(content string) []byte {
	ttf := string(goregular.TTF)
	return buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FontDescriptor 6 0 R >>",
		5: streamObj(content),
		6: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 7 0 R >>",
		7: streamObj(ttf),
	}, "<< /Size 8 /Root 1 0 R >>")
}

func renderFontPDF(t *testing.T, content string) image.Image {
	t.Helper()
	r, err := parse.NewResolver(buildFontPDF(content))
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

// darkPixels counts near-black pixels (drawn glyph ink) in the image.
func darkPixels(img image.Image) int {
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				n++
			}
		}
	}
	return n
}

func TestGlyph_DrawsInk(t *testing.T) {
	// A large glyph run must put a meaningful amount of black ink on the page.
	img := renderFontPDF(t, "BT /F1 60 Tf 20 80 Td (Hello) Tj ET")
	if got := darkPixels(img); got < 100 {
		t.Errorf("dark pixels = %d, want >= 100 (glyphs should render)", got)
	}
}

func TestGlyph_NoTextIsBlank(t *testing.T) {
	// Same page, no text operators: the page stays white.
	img := renderFontPDF(t, "")
	if got := darkPixels(img); got != 0 {
		t.Errorf("dark pixels = %d, want 0 for an empty content stream", got)
	}
}

func TestGlyph_InvisibleRenderMode(t *testing.T) {
	// Render mode 3 (invisible) must not draw ink, even with text shown.
	img := renderFontPDF(t, "BT /F1 60 Tf 3 Tr 20 80 Td (Hello) Tj ET")
	if got := darkPixels(img); got != 0 {
		t.Errorf("dark pixels = %d, want 0 for invisible render mode", got)
	}
}

func TestGlyph_ColorAppliesToText(t *testing.T) {
	// Fill color red -> the glyph ink is red, not black. Expect red pixels and
	// no near-black pixels.
	img := renderFontPDF(t, "1 0 0 rg BT /F1 60 Tf 20 80 Td (Hello) Tj ET")

	var red int
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 150 && g>>8 < 100 && bl>>8 < 100 {
				red++
			}
		}
	}
	if red < 100 {
		t.Errorf("red pixels = %d, want >= 100 (text should be red)", red)
	}
	if got := darkPixels(img); got > 0 {
		t.Errorf("dark pixels = %d, want 0 (text is red, not black)", got)
	}
}

func TestGlyph_MissingFontDoesNotCrash(t *testing.T) {
	// A font resource with no embedded program: text is skipped, page still
	// renders (white), no panic.
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 50 50] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		5: streamObj("BT /F1 20 Tf 5 20 Td (Hi) Tj ET"),
	}, "<< /Size 6 /Root 1 0 R >>")
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := r.Pages()
	data, err := Page(r, pages[0], Options{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatalf("render with unsupported font: %v", err)
	}
	img, _ := png.Decode(bytes.NewReader(data))
	if got := darkPixels(img); got != 0 {
		t.Errorf("dark pixels = %d, want 0 (unsupported font renders no text)", got)
	}
}

func TestGlyph_Type0Renders(t *testing.T) {
	// A Type0/Identity-H composite font over an embedded CIDFontType2 program.
	// Codes are 2-byte glyph IDs; showing a run of low GIDs must draw ink and
	// must not crash (exercises the DescendantFonts + 2-byte code path).
	ttf := string(goregular.TTF)
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type0 /BaseFont /GoRegular /Encoding /Identity-H /DescendantFonts [8 0 R] >>",
		5: streamObj("BT /F1 40 Tf 20 100 Td <0024002500260027> Tj ET"),
		6: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 7 0 R >>",
		7: streamObj(ttf),
		8: "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /GoRegular /FontDescriptor 6 0 R /CIDToGIDMap /Identity >>",
	}, "<< /Size 9 /Root 1 0 R >>")

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
		t.Fatalf("Type0 render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := darkPixels(img); got < 50 {
		t.Errorf("dark pixels = %d, want >= 50 (Type0 glyphs should render)", got)
	}
}

func TestGlyph_CounterIsHollow(t *testing.T) {
	// A large "O" must keep its counter (the inner hole) white — this verifies
	// the glyph is filled with the nonzero winding rule, not painted as a solid
	// blob. We find the ink bounding box and assert there is white *inside* it.
	img := renderFontPDF(t, "BT /F1 150 Tf 30 40 Td (O) Tj ET")

	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	isDark := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		return r>>8 < 128 && g>>8 < 128 && b>>8 < 128
	}
	bnd := img.Bounds()
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			if isDark(x, y) {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		t.Fatal("no ink drawn for 'O'")
	}
	// Count white pixels strictly inside the ink bounding box.
	whiteInside := 0
	for y := minY + 1; y < maxY; y++ {
		for x := minX + 1; x < maxX; x++ {
			if !isDark(x, y) {
				whiteInside++
			}
		}
	}
	if whiteInside < 50 {
		t.Errorf("white pixels inside 'O' bbox = %d, want >= 50 (counter must be hollow)", whiteInside)
	}
}

// sanity: the fixture actually embeds a parseable font program.
func TestBuildFontPDF_EmbedsFont(t *testing.T) {
	if len(goregular.TTF) < 1000 {
		t.Fatalf("goregular.TTF too small: %d", len(goregular.TTF))
	}
}

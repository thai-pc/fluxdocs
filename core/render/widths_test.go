package render

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

// renderWidthsPDF renders, at 72 DPI, a 300x100 page that shows content with
// the embedded Go Regular TrueType font as /F1, whose font dictionary carries
// extra (e.g. /FirstChar and /Widths) and whose descriptor carries descExtra.
func renderWidthsPDF(t *testing.T, extra, descExtra, content string) image.Image {
	t.Helper()
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FontDescriptor 6 0 R " + extra + " >>",
		5: streamObj(content),
		6: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 7 0 R " + descExtra + " >>",
		7: streamObj(string(goregular.TTF)),
	}, "<< /Size 8 /Root 1 0 R >>")
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

// inkRuns returns the [start, end) x ranges of the image's columns holding
// dark ink, left to right.
func inkRuns(img image.Image) [][2]int {
	b := img.Bounds()
	var runs [][2]int
	in := false
	for x := b.Min.X; x < b.Max.X; x++ {
		dark := false
		for y := b.Min.Y; y < b.Max.Y && !dark; y++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			dark = r>>8 < 128 && g>>8 < 128 && bl>>8 < 128
		}
		switch {
		case dark && !in:
			runs = append(runs, [2]int{x, x + 1})
			in = true
		case dark:
			runs[len(runs)-1][1] = x + 1
		default:
			in = false
		}
	}
	return runs
}

// TestGlyph_WidthsOverrideProgramAdvance pins §9.4.4: a simple font's glyph
// displacement is its /Widths entry, not the program's advance. Two "I"s at
// 50 pt start w0 × 50 / 1000 px apart at 72 DPI.
func TestGlyph_WidthsOverrideProgramAdvance(t *testing.T) {
	for _, tt := range []struct {
		name  string
		extra string
		want  int // px between the two glyphs' left ink edges
	}{
		{"widths 1000", "/FirstChar 73 /LastChar 73 /Widths [1000]", 50},
		{"widths 2000", "/FirstChar 73 /LastChar 73 /Widths [2000]", 100},
		{"missing width", "/FirstChar 65 /LastChar 65 /Widths [100]", 75},
		{"null entry, missing width", "/FirstChar 73 /LastChar 73 /Widths [null]", 75},
	} {
		t.Run(tt.name, func(t *testing.T) {
			img := renderWidthsPDF(t, tt.extra, "/MissingWidth 1500", "BT /F1 50 Tf 20 30 Td (II) Tj ET")
			runs := inkRuns(img)
			if len(runs) != 2 {
				t.Fatalf("ink runs = %v, want 2 glyphs", runs)
			}
			if d := runs[1][0] - runs[0][0]; d < tt.want-1 || d > tt.want+1 {
				t.Errorf("glyph spacing = %d px, want %d ±1", d, tt.want)
			}
		})
	}
}

// TestGlyph_NoWidthsUsesProgramAdvance keeps the program's advance for a font
// without /Widths (Go Regular's hmtx advance of "I", read here through sfnt),
// whether or not its descriptor has a /MissingWidth: /MissingWidth stands in
// for codes a /Widths array does not cover, not for the array itself, and
// the program knows its glyphs' widths.
func TestGlyph_NoWidthsUsesProgramAdvance(t *testing.T) {
	sf, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	gf := &glyphFont{sf: sf}
	gid, ok := gf.gid('I')
	if !ok {
		t.Fatal("no glyph for I")
	}
	program := int(math.Round(gf.advanceEm(gid) * 50))
	for _, tt := range []struct {
		name, descExtra string
		want            int
	}{
		{"program advance", "", program},
		{"missing width ignored", "/MissingWidth 1500", program},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runs := inkRuns(renderWidthsPDF(t, "", tt.descExtra, "BT /F1 50 Tf 20 30 Td (II) Tj ET"))
			if len(runs) != 2 {
				t.Fatalf("ink runs = %v, want 2 glyphs", runs)
			}
			if d := runs[1][0] - runs[0][0]; d < tt.want-1 || d > tt.want+1 {
				t.Errorf("glyph spacing = %d px, want %d ±1", d, tt.want)
			}
		})
	}
}

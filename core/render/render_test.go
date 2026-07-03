package render

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

func TestDeviceMatrix_Rotate0Corners(t *testing.T) {
	mb := parse.Rectangle{LLX: 0, LLY: 0, URX: 100, URY: 200}
	m, pxW, pxH, err := deviceMatrix(mb, 0, 2.0) // 144 DPI
	if err != nil {
		t.Fatal(err)
	}
	if pxW != 200 || pxH != 400 {
		t.Errorf("canvas = %dx%d, want 200x400", pxW, pxH)
	}
	// Bottom-left PDF -> bottom-left device; top-right PDF -> top-right device.
	check := func(x, y, wantPx, wantPy float64) {
		px, py := m.apply(x, y)
		if math.Abs(px-wantPx) > 1e-9 || math.Abs(py-wantPy) > 1e-9 {
			t.Errorf("apply(%g,%g) = (%g,%g), want (%g,%g)", x, y, px, py, wantPx, wantPy)
		}
	}
	check(0, 0, 0, 400)     // bottom-left -> bottom (py max)
	check(100, 200, 200, 0) // top-right -> top (py 0)
	check(0, 200, 0, 0)     // top-left -> origin
}

func TestDeviceMatrix_RotationSwapsDimensions(t *testing.T) {
	mb := parse.Rectangle{LLX: 0, LLY: 0, URX: 100, URY: 200}
	for _, rot := range []int{90, 270} {
		_, pxW, pxH, err := deviceMatrix(mb, rot, 1.0)
		if err != nil {
			t.Fatal(err)
		}
		if pxW != 200 || pxH != 100 {
			t.Errorf("rot %d: canvas = %dx%d, want 200x100 (swapped)", rot, pxW, pxH)
		}
	}
}

func TestDeviceMatrix_DegenerateAndOversize(t *testing.T) {
	if _, _, _, err := deviceMatrix(parse.Rectangle{}, 0, 1); err == nil {
		t.Error("degenerate MediaBox should error")
	}
	huge := parse.Rectangle{URX: 100000, URY: 100000}
	if _, _, _, err := deviceMatrix(huge, 0, 10); err == nil {
		t.Error("oversize canvas should error")
	}
}

// renderRect builds a one-page PDF that fills a rectangle in the given color
// operator, renders it, and returns the decoded image.
func renderRect(t *testing.T, content string, opts Options) image.Image {
	t.Helper()
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: streamObj(content),
	}, "<< /Size 5 /Root 1 0 R >>")

	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	data, err := Page(r, pages[0], opts)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	return img
}

func nearlyEqual(a, b color.Color, tol int) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		v := int(x>>8) - int(y>>8)
		if v < 0 {
			return -v
		}
		return v
	}
	return d(ar, br) <= tol && d(ag, bg) <= tol && d(ab, bb) <= tol
}

func TestPage_FillsRedRectangle(t *testing.T) {
	// Fill the middle 50x50 region of a 100x100 page with red (DPI 72 -> 1px/pt).
	img := renderRect(t, "1 0 0 rg 25 25 50 50 re f", Options{DPI: 72, Format: "png"})

	b := img.Bounds()
	if b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("image = %dx%d, want 100x100", b.Dx(), b.Dy())
	}
	// Center is inside the rectangle -> red.
	if got := img.At(50, 50); !nearlyEqual(got, color.RGBA{255, 0, 0, 255}, 4) {
		t.Errorf("center pixel = %v, want red", got)
	}
	// A corner is outside -> white background.
	if got := img.At(5, 5); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 4) {
		t.Errorf("corner pixel = %v, want white", got)
	}
}

func TestPage_YAxisIsFlipped(t *testing.T) {
	// Fill only the bottom strip in PDF space (y 0..20). In the image that must
	// appear at the BOTTOM (large py), not the top.
	img := renderRect(t, "0 0 0 rg 0 0 100 20 re f", Options{DPI: 72, Format: "png"})

	if got := img.At(50, 90); !nearlyEqual(got, color.RGBA{0, 0, 0, 255}, 4) {
		t.Errorf("bottom pixel (0,90) = %v, want black (PDF bottom strip)", got)
	}
	if got := img.At(50, 10); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 4) {
		t.Errorf("top pixel (50,10) = %v, want white", got)
	}
}

func TestPage_CMYKFill(t *testing.T) {
	// CMYK (0 1 1 0) = red.
	img := renderRect(t, "0 1 1 0 k 0 0 100 100 re f", Options{DPI: 72, Format: "png"})
	if got := img.At(50, 50); !nearlyEqual(got, color.RGBA{255, 0, 0, 255}, 4) {
		t.Errorf("center = %v, want red from CMYK", got)
	}
}

func TestPage_JPEGAndDefaultFormat(t *testing.T) {
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 10 10] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: streamObj("0 0 10 10 re f"),
	}, "<< /Size 5 /Root 1 0 R >>")
	r, _ := parse.NewResolver(pdf)
	pages, _ := r.Pages()

	jpg, err := Page(r, pages[0], Options{Format: "jpeg", DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(jpg, []byte{0xFF, 0xD8, 0xFF}) {
		t.Errorf("output is not a JPEG (bad magic)")
	}

	// Empty format defaults to PNG.
	pngOut, err := Page(r, pages[0], Options{DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pngOut, []byte("\x89PNG\r\n")) {
		t.Errorf("default output is not a PNG")
	}
}

func TestPage_UnsupportedFormat(t *testing.T) {
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 10 10] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R >>")
	r, _ := parse.NewResolver(pdf)
	pages, _ := r.Pages()

	if _, err := Page(r, pages[0], Options{Format: "svg"}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

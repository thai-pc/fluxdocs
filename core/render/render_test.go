package render

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
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

// TestDeviceMatrix_HostileSizesRejected is the regression for an int64 area
// overflow: a 1e18 pt MediaBox at DPI 1 made int64(pxW)*int64(pxH) wrap past
// the cap, and newRaster panicked (under js/wasm the float→int conversion
// saturates and the product wraps the same way). Each case must return an
// error, never a canvas (SECURITY.md §2).
func TestDeviceMatrix_HostileSizesRejected(t *testing.T) {
	tests := []struct {
		name  string
		mb    parse.Rectangle
		rot   int
		scale float64
	}{
		{"1e18 square at DPI 1", parse.Rectangle{URX: 1e18, URY: 1e18}, 0, 1.0 / 72},
		{"1e18 square rotated", parse.Rectangle{URX: 1e18, URY: 1e18}, 90, 1.0 / 72},
		{"one side 1e18", parse.Rectangle{URX: 1e18, URY: 1}, 0, 1.0 / 72},
		{"product overflows int64", parse.Rectangle{URX: 5e9, URY: 5e9}, 0, 1},
		{"max float", parse.Rectangle{URX: math.MaxFloat64, URY: math.MaxFloat64}, 0, 1},
		{"infinite", parse.Rectangle{URX: math.Inf(1), URY: 10}, 0, 1},
		{"NaN width", parse.Rectangle{URX: math.NaN(), URY: 10}, 0, 1},
		{"side over cap, other underflows", parse.Rectangle{URX: 1e8, URY: 0.001}, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, pxW, pxH, err := deviceMatrix(tc.mb, tc.rot, tc.scale)
			if err == nil {
				t.Fatalf("deviceMatrix accepted a %dx%d canvas, want error", pxW, pxH)
			}
			if !errors.Is(err, ErrCanvasTooLarge) {
				t.Errorf("err = %v, want ErrCanvasTooLarge", err)
			}
		})
	}
}

// TestPage_HugeMediaBoxReturnsError drives the same overflow end to end through
// Page: it must return an error, not panic in newRaster.
func TestPage_HugeMediaBoxReturnsError(t *testing.T) {
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 1000000000000000000 1000000000000000000] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R >>")
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Page(r, pages[0], Options{DPI: 1, Format: "png"}); !errors.Is(err, ErrCanvasTooLarge) {
		t.Fatalf("Page on a 1e18 pt MediaBox: err = %v, want ErrCanvasTooLarge", err)
	}
}

// TestDeviceMatrix_CanvasCapBoundary pins the exact edges of the canvas cap: an
// area of exactly maxCanvasPixels (and a single side of exactly maxCanvasPixels
// with the other side 1px) is accepted, one pixel more on either axis — or a
// fractional overshoot that Ceil rounds up — is rejected. Rotation must not let
// a page slip past the cap by swapping its sides.
func TestDeviceMatrix_CanvasCapBoundary(t *testing.T) {
	const side = 8192 // side*side == maxCanvasPixels
	if side*side != maxCanvasPixels {
		t.Fatalf("test assumes maxCanvasPixels == %d, got %d", side*side, maxCanvasPixels)
	}
	tests := []struct {
		name         string
		mb           parse.Rectangle
		rot          int
		wantW, wantH int // 0 = want an error
	}{
		{"area exactly cap", parse.Rectangle{URX: side, URY: side}, 0, side, side},
		{"area exactly cap rotated 90", parse.Rectangle{URX: side, URY: side}, 90, side, side},
		{"one column over cap", parse.Rectangle{URX: side + 1, URY: side}, 0, 0, 0},
		{"one row over cap", parse.Rectangle{URX: side, URY: side + 1}, 0, 0, 0},
		{"fraction over cap rounds up", parse.Rectangle{URX: side + 0.25, URY: side}, 0, 0, 0},
		{"side exactly cap, other 1px", parse.Rectangle{URX: maxCanvasPixels, URY: 1}, 0, maxCanvasPixels, 1},
		{"side exactly cap rotated 90", parse.Rectangle{URX: maxCanvasPixels, URY: 1}, 90, 1, maxCanvasPixels},
		{"side exactly cap rotated 270", parse.Rectangle{URX: 1, URY: maxCanvasPixels}, 270, maxCanvasPixels, 1},
		{"side cap+1, other 1px", parse.Rectangle{URX: maxCanvasPixels + 1, URY: 1}, 0, 0, 0},
		{"side cap+1 rotated 90", parse.Rectangle{URX: maxCanvasPixels + 1, URY: 1}, 90, 0, 0},
		{"side exactly cap, other 2px", parse.Rectangle{URX: maxCanvasPixels, URY: 2}, 0, 0, 0},
		{"side exactly cap, other underflows", parse.Rectangle{URX: maxCanvasPixels, URY: 1e-300}, 0, maxCanvasPixels, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, pxW, pxH, err := deviceMatrix(tc.mb, tc.rot, 1)
			if tc.wantW == 0 {
				if err == nil {
					t.Fatalf("deviceMatrix accepted a %dx%d canvas, want canvas-too-large error", pxW, pxH)
				}
				if !strings.Contains(err.Error(), "exceeds") {
					t.Fatalf("err = %v, want a canvas-too-large (\"exceeds\") error", err)
				}
				if !errors.Is(err, ErrCanvasTooLarge) {
					t.Errorf("err = %v, want ErrCanvasTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("deviceMatrix: %v, want %dx%d canvas", err, tc.wantW, tc.wantH)
			}
			if pxW != tc.wantW || pxH != tc.wantH {
				t.Errorf("canvas = %dx%d, want %dx%d", pxW, pxH, tc.wantW, tc.wantH)
			}
		})
	}
}

// TestDeviceMatrix_HostileBoxShapes covers MediaBoxes that are hostile through
// their corner order or sign rather than a plain huge URX/URY: Width/Height take
// the absolute extent, so an inverted or fully negative huge box must hit the
// same cap. A tiny page that underflows to 0px keeps the 1px minimum.
func TestDeviceMatrix_HostileBoxShapes(t *testing.T) {
	rejected := []struct {
		name  string
		mb    parse.Rectangle
		rot   int
		scale float64
	}{
		{"inverted 1e18 box", parse.Rectangle{LLX: 1e18, LLY: 1e18}, 0, 1.0 / 72},
		{"negative 1e18 box", parse.Rectangle{LLX: -1e18, LLY: -1e18}, 0, 1.0 / 72},
		{"negative 1e18 box rotated 180", parse.Rectangle{LLX: -1e18, LLY: -1e18}, 180, 1.0 / 72},
		{"box spanning -1e18..1e18", parse.Rectangle{LLX: -1e18, LLY: -1, URX: 1e18, URY: 1}, 0, 1},
		{"tall sliver rotated 270", parse.Rectangle{URX: 1, URY: 1e9}, 270, 1},
		{"-Inf corner", parse.Rectangle{LLX: math.Inf(-1), URX: 10, URY: 10}, 0, 1},
		{"Inf-Inf width is NaN", parse.Rectangle{LLX: math.Inf(1), URX: math.Inf(1), URY: 10}, 0, 1},
		{"1pt page at max int DPI", parse.Rectangle{URX: 1, URY: 1}, 0, float64(math.MaxInt) / 72},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, pxW, pxH, err := deviceMatrix(tc.mb, tc.rot, tc.scale)
			if err == nil {
				t.Fatalf("deviceMatrix accepted a %dx%d canvas, want error", pxW, pxH)
			}
			if !errors.Is(err, ErrCanvasTooLarge) {
				t.Errorf("err = %v, want ErrCanvasTooLarge", err)
			}
		})
	}

	accepted := []struct {
		name         string
		mb           parse.Rectangle
		wantW, wantH int
	}{
		{"negative-origin box", parse.Rectangle{LLX: -100, LLY: -50, URX: 0, URY: 50}, 100, 100},
		{"sub-pixel page clamps to 1px", parse.Rectangle{URX: 1e-300, URY: 1e-300}, 1, 1},
		{"far-offset normal-size box", parse.Rectangle{LLX: 1e6, LLY: 1e6, URX: 1e6 + 10, URY: 1e6 + 20}, 10, 20},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			_, pxW, pxH, err := deviceMatrix(tc.mb, 0, 1)
			if err != nil {
				t.Fatalf("deviceMatrix: %v, want %dx%d", err, tc.wantW, tc.wantH)
			}
			if pxW != tc.wantW || pxH != tc.wantH {
				t.Errorf("canvas = %dx%d, want %dx%d", pxW, pxH, tc.wantW, tc.wantH)
			}
		})
	}
}

// TestPage_DPIDefaultAndExtremesHitCanvasCap checks the DPI handling in front of
// deviceMatrix: DPI <= 0 means 150 (so a 4000pt page that is tiny at DPI 1 must
// exceed the cap at the default), and an absurd DPI is rejected rather than
// overflowing the scale.
func TestPage_DPIDefaultAndExtremesHitCanvasCap(t *testing.T) {
	pdf := buildRenderPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 4000 4000] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R >>")
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}

	// DPI 1: 4000/72 = 55.6 -> 56px square, well under the cap.
	out, err := Page(r, pages[0], Options{DPI: 1, Format: "png"})
	if err != nil {
		t.Fatalf("DPI 1: %v, want a 56x56 render", err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 56 || b.Dy() != 56 {
		t.Errorf("DPI 1: image = %dx%d, want 56x56", b.Dx(), b.Dy())
	}

	// At 150 DPI the page is 8334x8334 = 69.4M px > 64Mi px.
	for _, dpi := range []int{0, -1, math.MinInt, math.MaxInt} {
		_, err := Page(r, pages[0], Options{DPI: dpi, Format: "png"})
		if err == nil || !strings.Contains(err.Error(), "exceeds") || !errors.Is(err, ErrCanvasTooLarge) {
			t.Errorf("DPI %d: err = %v, want canvas-too-large error", dpi, err)
		}
	}
}

// TestQ_SaveDepthIsCapped: a flood of q (8 Mi, well within MaxOperators) keeps
// at most maxSaveDepth saved states; the excess is counted instead of saved.
// Before the cap each q copied the graphics state onto the stack (≈ 3 GB for
// 8 Mi q natively, fatal under js/wasm).
func TestQ_SaveDepthIsCapped(t *testing.T) {
	rs := newRaster(10, 10)
	in := &interp{rs: rs, b: rs.b, gs: gstate{ctm: identity(), clip: fullClip(10, 10)}}
	q := parse.ContentOp{Op: "q"}
	const n = 8 << 20
	for i := 0; i < n; i++ {
		in.apply(q)
	}
	if len(in.stack) != maxSaveDepth || cap(in.stack) > 2*maxSaveDepth {
		t.Errorf("stack len %d cap %d, want len %d and a bounded cap", len(in.stack), cap(in.stack), maxSaveDepth)
	}
	if in.skippedQ != n-maxSaveDepth {
		t.Errorf("skippedQ = %d, want %d", in.skippedQ, n-maxSaveDepth)
	}
}

// TestQ_IgnoredQSwallowsItsQ pins the lenient pairing past maxSaveDepth: the Q
// matching an ignored q is swallowed (it restores nothing, so state set inside
// stays), and the next Q pops the deepest saved state as usual.
func TestQ_IgnoredQSwallowsItsQ(t *testing.T) {
	const extra = 6
	content := "1 0 0 rg " + strings.Repeat("q ", maxSaveDepth+extra) + "0 0 1 rg " +
		strings.Repeat("Q ", extra) + "0 0 50 100 re f " + // blue: the swallowed Qs restore nothing
		"Q 50 0 50 100 re f" // red: this Q pops the state saved by the last real q
	img := renderRect(t, content, Options{DPI: 72})
	blue, red := color.RGBA{0, 0, 255, 255}, color.RGBA{255, 0, 0, 255}
	if c := img.At(25, 50); !nearlyEqual(c, blue, 2) {
		t.Errorf("left half = %v, want blue", c)
	}
	if c := img.At(75, 50); !nearlyEqual(c, red, 2) {
		t.Errorf("right half = %v, want red", c)
	}
}

// saveDepthPage is content that fills the graphics-state stack to exactly
// maxSaveDepth with distinct colours in the top two frames: green below,
// yellow saved by the next-to-last q, cyan by the last. Any Q after it that is
// not swallowed restores cyan, then yellow, then green.
func saveDepthPage() string {
	return "0 1 0 rg " + strings.Repeat("q ", maxSaveDepth-2) + "1 1 0 rg q 0 1 1 rg q "
}

// TestQ_FormSkippedQIsScoped pins how ignored q operators (past maxSaveDepth)
// interact with a Form XObject: the form starts with none of the caller's
// (form.go resets skippedQ), so its own Q pops its implicit frame rather than
// consuming the caller's ignored q; and the caller's count is restored on exit,
// in both directions — the caller's ignored q still pair with its Qs after the
// Do, and an ignored q the form leaves open does not swallow a caller Q.
func TestQ_FormSkippedQIsScoped(t *testing.T) {
	cyan, red := color.RGBA{0, 255, 255, 255}, color.RGBA{255, 0, 0, 255}
	form := func(content string) map[int]string {
		return map[int]string{5: formStreamObj("/BBox [0 0 50 100]", content)}
	}
	t.Run("caller's ignored q survive a Do", func(t *testing.T) {
		// Two ignored q (skippedQ 2), blue fill, Do, then Q Q (swallowed: they
		// pair with the ignored q) and Q (pops the cyan frame).
		content := saveDepthPage() + "q q 0 0 1 rg /Fm Do Q Q Q 0 0 10 10 re f"
		img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", content,
			form("Q Q 1 0 0 rg 0 0 100 100 re f"))
		if c := img.At(5, 95); !nearlyEqual(c, cyan, 2) {
			t.Errorf("caller fill after the Do = %v, want cyan (the caller's ignored q paired with its Qs)", c)
		}
		// The form's first Q popped the form's own implicit frame (dropping its
		// /BBox clip) instead of consuming one of the caller's ignored q, so
		// its red fill reaches the right half.
		if c := img.At(75, 50); !nearlyEqual(c, red, 2) {
			t.Errorf("form fill outside its /BBox = %v, want red (the form's Q must not consume the caller's ignored q)", c)
		}
	})
	t.Run("form's ignored q does not leak", func(t *testing.T) {
		// The stack is full on entry, so the form's q is ignored. After the Do
		// the caller's Q must pop the cyan frame, not be swallowed.
		content := saveDepthPage() + "0 0 1 rg /Fm Do Q 0 0 10 10 re f"
		img := renderFormPDF(t, "<< /XObject << /Fm 5 0 R >> >>", content, form("q 1 0 0 rg"))
		if c := img.At(5, 95); !nearlyEqual(c, cyan, 2) {
			t.Errorf("caller fill after the Do = %v, want cyan (the form's ignored q leaked)", c)
		}
	})
}

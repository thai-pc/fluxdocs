package render

import (
	"image"
	"image/color"
	"testing"
)

// renderAnnots renders a blank 100x100 page (DPI 72 → 1px/pt) with the given
// annotation overlay and returns the decoded image.
func renderAnnots(t *testing.T, annots []Annotation) image.Image {
	t.Helper()
	return renderRect(t, "", Options{DPI: 72, Format: "png", Annotations: annots})
}

func TestAnnotation_HighlightTintsRegion(t *testing.T) {
	// Red highlight at 50% opacity over a rect; the covered region is tinted,
	// outside stays white.
	img := renderAnnots(t, []Annotation{{
		Type: "highlight", X: 25, Y: 25, Width: 50, Height: 50,
		Color: "#ff0000", Opacity: 0.5,
	}})
	// 50% red over white ≈ (255,128,128).
	if got := img.At(50, 50); !nearlyEqual(got, color.RGBA{255, 128, 128, 255}, 12) {
		t.Errorf("center = %v, want ~(255,128,128) tinted", got)
	}
	if got := img.At(5, 5); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 4) {
		t.Errorf("corner = %v, want white (outside highlight)", got)
	}
}

func TestAnnotation_ShapeIsHollowOutline(t *testing.T) {
	// A stroked rectangle: the border is colored, the interior stays white.
	img := renderAnnots(t, []Annotation{{
		Type: "shape", X: 25, Y: 25, Width: 50, Height: 50,
		Color: "#0000ff", Opacity: 1,
	}})
	// Left border at device (25,50) should be blue-ish.
	if got := img.At(25, 50); nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 10) {
		t.Errorf("border pixel (25,50) = %v, want colored (not white)", got)
	}
	// Interior center stays white (hollow outline).
	if got := img.At(50, 50); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 6) {
		t.Errorf("interior (50,50) = %v, want white (hollow)", got)
	}
}

func TestAnnotation_DrawStrokesPolyline(t *testing.T) {
	// A freehand line from (20,50) to (80,50) in PDF space (device y = 50).
	img := renderAnnots(t, []Annotation{{
		Type:  "draw",
		Color: "#000000", Opacity: 1,
		Points: []AnnPoint{{X: 20, Y: 50}, {X: 80, Y: 50}},
	}})
	dark := darkPixels(img)
	if dark < 30 {
		t.Errorf("dark pixels = %d, want >= 30 along the drawn line", dark)
	}
	// A point clearly off the line stays white.
	if got := img.At(50, 10); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 4) {
		t.Errorf("off-line pixel = %v, want white", got)
	}
}

func TestAnnotation_DrawSinglePointIsNoOp(t *testing.T) {
	img := renderAnnots(t, []Annotation{{
		Type: "draw", Color: "#000000", Opacity: 1,
		Points: []AnnPoint{{X: 50, Y: 50}},
	}})
	if got := nonWhitePixels(img); got != 0 {
		t.Errorf("non-white pixels = %d, want 0 for a single-point draw", got)
	}
}

func TestAnnotation_NoteMarker(t *testing.T) {
	// A note at the top-left; its marker is a small solid block.
	img := renderAnnots(t, []Annotation{{
		Type: "note", X: 10, Y: 80, Width: 5, Height: 5,
		Color: "#f59e0b", Opacity: 1,
	}})
	// Marker anchored at PDF (10, 85) → device (10, 15), a 14px square.
	if got := img.At(15, 20); nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 10) {
		t.Errorf("marker pixel = %v, want the amber marker (not white)", got)
	}
}

func TestAnnotation_UnknownTypeIsNoOp(t *testing.T) {
	img := renderAnnots(t, []Annotation{{
		Type: "mystery", X: 10, Y: 10, Width: 50, Height: 50, Color: "#ff0000", Opacity: 1,
	}})
	if got := nonWhitePixels(img); got != 0 {
		t.Errorf("non-white pixels = %d, want 0 for an unknown annotation type", got)
	}
}

func TestAnnotation_YAxisFlip(t *testing.T) {
	// A highlight over the PDF bottom strip (y 0..20) must appear at the BOTTOM
	// of the image (device y large), not the top.
	img := renderAnnots(t, []Annotation{{
		Type: "highlight", X: 0, Y: 0, Width: 100, Height: 20,
		Color: "#000000", Opacity: 1,
	}})
	if got := img.At(50, 90); !nearlyEqual(got, color.RGBA{0, 0, 0, 255}, 6) {
		t.Errorf("bottom pixel (50,90) = %v, want black (PDF bottom strip)", got)
	}
	if got := img.At(50, 10); !nearlyEqual(got, color.RGBA{255, 255, 255, 255}, 6) {
		t.Errorf("top pixel (50,10) = %v, want white", got)
	}
}

func TestAnnotation_EmptyOverlayUnchanged(t *testing.T) {
	// No annotations → blank page stays fully white (the overlay is a no-op).
	img := renderAnnots(t, nil)
	if got := nonWhitePixels(img); got != 0 {
		t.Errorf("non-white pixels = %d, want 0 for an empty overlay", got)
	}
}

func TestDecodeHexRGB(t *testing.T) {
	tests := []struct {
		in      string
		r, g, b uint8
		ok      bool
	}{
		{"#ff0000", 255, 0, 0, true},
		{"#00ff00", 0, 255, 0, true},
		{"#f59e0b", 0xf5, 0x9e, 0x0b, true},
		{"#FFF", 255, 255, 255, true}, // #rgb doubled
		{"#abc", 0xaa, 0xbb, 0xcc, true},
		{"", 0, 0, 0, false},
		{"ff0000", 0, 0, 0, false}, // no '#'
		{"#12345", 0, 0, 0, false}, // wrong length
		{"#gggggg", 0, 0, 0, false},
	}
	for _, tt := range tests {
		r, g, b, ok := decodeHexRGB(tt.in)
		if ok != tt.ok || (ok && (r != tt.r || g != tt.g || b != tt.b)) {
			t.Errorf("decodeHexRGB(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				tt.in, r, g, b, ok, tt.r, tt.g, tt.b, tt.ok)
		}
	}
}

func TestParseHexColor_FallbackAndOpacity(t *testing.T) {
	// Invalid hex → default amber.
	amber := parseHexColor("not-a-color", 1)
	if amber.R != 0xf5 || amber.G != 0x9e || amber.B != 0x0b {
		t.Errorf("fallback = %v, want amber #f59e0b", amber)
	}
	// Opacity maps to alpha; NaN/negative → 0; >1 → 255.
	if a := parseHexColor("#000000", 0.5); a.A < 122 || a.A > 132 {
		t.Errorf("alpha for 0.5 = %d, want ~127", a.A)
	}
	if a := parseHexColor("#000000", -1); a.A != 0 {
		t.Errorf("alpha for -1 = %d, want 0", a.A)
	}
	if a := parseHexColor("#000000", 5); a.A != 255 {
		t.Errorf("alpha for 5 = %d, want 255", a.A)
	}
}

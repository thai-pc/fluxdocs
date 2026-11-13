package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/image/font/gofont/goregular"
)

// This file holds the clipping-path tests of docs/design/clipping-paths.md
// §10.1. They assert ink at pixels and exact pixel identities, not binary
// goldens. Run them under both targets (§7):
//
//	go test ./core/render/
//	GOOS=js GOARCH=wasm go test -exec "$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./core/render/

// clipPage builds a one-page PDF with a w×h pt MediaBox, page resources res
// ("" for none), content as object 4 and extra objects numbered from 5.
func clipPage(w, h int, res, content string, extra ...string) []byte {
	if res != "" {
		res = " /Resources " + res
	}
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 %d %d] >>", w, h),
		3: "<< /Type /Page /Parent 2 0 R" + res + " /Contents 4 0 R >>",
		4: streamObj(content),
	}
	for i, e := range extra {
		objs[5+i] = e
	}
	return buildRenderPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1))
}

// clipRender renders a clipPage at 72 DPI (1 px per pt).
func clipRender(t *testing.T, w, h int, res, content string, extra ...string) image.Image {
	t.Helper()
	return renderPDF(t, clipPage(w, h, res, content, extra...))
}

// black is opaque black (white is form_test.go's).
var black = color.RGBA{0, 0, 0, 0xff}

// rgbaAt returns the pixel at (x, y) as 8-bit RGBA.
func rgbaAt(img image.Image, x, y int) color.RGBA {
	return color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
}

// inkCount counts the pixels that are not white.
func inkCount(img image.Image) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if rgbaAt(img, x, y) != white {
				n++
			}
		}
	}
	return n
}

// imgHash hashes an image by its size and 8-bit RGBA rows, like the corpus
// manifest, so it does not depend on the PNG encoder.
func imgHash(img image.Image) string {
	b := img.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d\n", b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := rgbaAt(img, x, y)
			h.Write([]byte{c.R, c.G, c.B, c.A})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Resources and objects of the clip fixtures: an RGB image /Im (4×4), an
// image mask /Mk (8×8 checkerboard), an RGB image with a gray /SMask /Sm, and
// a form /Fm with its own q/Q, fill and stroke.
const clipRes = "<< /XObject << /Im 5 0 R /Mk 6 0 R /Sm 7 0 R /Fm 9 0 R >> >>"

func clipObjects() []string {
	rgb := make([]byte, 4*4*3)
	for i := range rgb {
		rgb[i] = byte(i * 37)
	}
	return []string{
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 4 /Height 4 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(rgb), rgb),
		"<< /Type /XObject /Subtype /Image /Width 8 /Height 8 /ImageMask true /BitsPerComponent 1 /Length 8 >>\nstream\n\x55\xaa\x55\xaa\x55\xaa\x55\xaa\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 8 0 R /Length 12 >>\nstream\n\x10\x20\x30\x40\x50\x60\x70\x80\x90\xa0\xb0\xc0\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 4 >>\nstream\n\xff\x80\x40\xff\nendstream",
		streamObjDict("/Type /XObject /Subtype /Form /BBox [0 0 100 100]", "q 1 0 1 rg 0 0 100 50 re f Q 0 0 0 RG 3 w 0 0 m 100 100 l S"),
	}
}

// streamObjDict is streamObj with more dictionary entries.
func streamObjDict(dict, content string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(content), content)
}

// Clip paths of the fixtures, in a 100×100 user space.
var clipPaths = []struct {
	name, path string
	evenOdd    bool
}{
	{"triangle", "10 5 m 95 50.5 l 20.25 95 l h", false},
	{"ring-evenodd", "5 5 90 90 re 30.5 30.5 40 40 re", true},
	{"ring-nonzero", "5 5 90 90 re 30.5 30.5 40 40 re", false},
	{"curve", "50 10 m 90 10 90 90 50 90 c 10 90 10 10 50 10 c h", false},
	{"rect", "12.3 7.6 60.2 70.7 re", false},
	{"open-subpath", "10 10 m 90 20 l 30 90 l", false}, // implicitly closed
}

// assertClipped checks the pixel rule of §2.1 end to end: content rendered
// under the clip path equals content rendered without it where a fill of the
// path paints, and is untouched (white) everywhere else.
func assertClipped(t *testing.T, w, h int, res, clip string, evenOdd bool, content string, extra ...string) {
	t.Helper()
	wop, fop := "W", "f"
	if evenOdd {
		wop, fop = "W*", "f*"
	}
	plain := clipRender(t, w, h, res, content, extra...)
	clipped := clipRender(t, w, h, res, clip+" "+wop+" n "+content, extra...)
	region := clipRender(t, w, h, res, "0 g "+clip+" "+fop, extra...)
	in, out, inkOut := 0, 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			got := rgbaAt(clipped, x, y)
			want := white
			if rgbaAt(region, x, y) == black {
				want = rgbaAt(plain, x, y)
				in++
			} else {
				out++
				if rgbaAt(plain, x, y) != white {
					inkOut++
				}
			}
			if got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
	if in == 0 || out == 0 || inkOut == 0 {
		t.Fatalf("fixture proves nothing: %d px inside, %d outside, %d inked outside", in, out, inkOut)
	}
}

// TestClip_PaintersHonourClip covers §10.1 items 1, 8 and 9: fills, strokes,
// images, image masks, soft-masked images, forms and glyphs, under
// rectangular and general clip paths and both winding rules.
func TestClip_PaintersHonourClip(t *testing.T) {
	painters := []struct{ name, content string }{
		{"fill", "0 0 1 rg 0 0 100 100 re f"},
		{"translucent-overlap", "0 0 1 rg 0 0 100 100 re f 1 0 0 rg 0 0 60 60 re f"},
		{"stroke", "1 0 0 RG 7 w 5 5 m 95 95 l 95 5 l S"},
		{"image", "100 0 0 100 0 0 cm /Im Do"},
		{"image-mask", "0 0.5 0 rg 100 0 0 100 0 0 cm /Mk Do"},
		{"smask-image", "100 0 0 100 0 0 cm /Sm Do"},
		{"form", "/Fm Do"},
	}
	for _, cp := range clipPaths {
		for _, p := range painters {
			t.Run(cp.name+"/"+p.name, func(t *testing.T) {
				assertClipped(t, 100, 100, clipRes, cp.path, cp.evenOdd, p.content, clipObjects()...)
			})
		}
	}
}

// TestClip_Glyphs is §10.1 item 9: embedded TrueType and Type 1 glyphs under
// a general clip.
func TestClip_Glyphs(t *testing.T) {
	clip := "0 0 m 45 0 l 60 200 l 0 200 l h"
	t.Run("truetype", func(t *testing.T) {
		pdf := func(c string) []byte { return buildFontPDF(c) }
		assertClippedPDF(t, 200, 200, pdf, clip, "BT /F1 150 Tf 10 30 Td (H) Tj ET")
	})
	t.Run("type1", func(t *testing.T) {
		f := t1TestFont()
		l1, l2, l3 := f.Lengths()
		pdf := func(c string) []byte { return t1PDF(f.Bytes(), l1, l2, l3, "", c) }
		assertClippedPDF(t, 200, 200, pdf, clip, "BT /F1 100 Tf 20 30 Td (A) Tj ET")
	})
}

// assertClippedPDF is assertClipped for a fixture built by mk.
func assertClippedPDF(t *testing.T, w, h int, mk func(string) []byte, clip, content string) {
	t.Helper()
	plain := renderPDF(t, mk(content))
	clipped := renderPDF(t, mk(clip+" W n "+content))
	region := renderPDF(t, mk("0 g "+clip+" f"))
	inkIn, inkOut := 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := white
			if rgbaAt(region, x, y) == black {
				want = rgbaAt(plain, x, y)
				if want != white {
					inkIn++
				}
			} else if rgbaAt(plain, x, y) != white {
				inkOut++
			}
			if got := rgbaAt(clipped, x, y); got != want {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
	if inkIn == 0 || inkOut == 0 {
		t.Fatalf("fixture proves nothing: %d inked inside, %d outside", inkIn, inkOut)
	}
}

// TestClip_Semantics covers §10.1 items 1–7 and 10 with ink counts and probe
// pixels on a 100×100 px page (device row = 99 − user y, roughly).
func TestClip_Semantics(t *testing.T) {
	const full = " 0 g 0 0 100 100 re f"
	tests := []struct {
		name    string
		content string
		ink     int        // exact non-white pixel count; -1 = do not check
		dark    [][2]int   // device pixels that must be black
		light   [][2]int   // device pixels that must be white
		colors  []pixColor // device pixels of an exact color
	}{
		{name: "re W n clips a full fill", content: "20 30 40 50 re W n" + full, ink: 40 * 50,
			dark: [][2]int{{20, 20}, {59, 69}}, light: [][2]int{{19, 50}, {60, 50}, {30, 19}, {30, 70}}},
		{name: "W f paints under the old clip, then clips", content: "q 0 0 100 50 re W n 0 0 1 rg 0 0 50 100 re W f 1 0 0 rg 0 0 100 100 re f Q",
			ink: 50 * 50, colors: []pixColor{{25, 75, color.RGBA{0xff, 0, 0, 0xff}}, {75, 75, white}, {25, 25, white}}},
		{name: "W S clips to the fill region, after stroking", content: "0 g 10 w 20 20 60 60 re W S 1 0 0 rg 0 0 100 100 re f",
			colors: []pixColor{{17, 50, black}, {50, 50, color.RGBA{0xff, 0, 0, 0xff}}, {5, 50, white}, {90, 50, white}}, ink: -1},
		{name: "nonzero ring keeps the hole", content: "10 10 80 80 re 30 30 40 40 re W n" + full, ink: 80 * 80,
			dark: [][2]int{{50, 50}, {15, 15}}},
		{name: "even-odd ring empties the hole", content: "10 10 80 80 re 30 30 40 40 re W* n" + full, ink: 80*80 - 40*40,
			dark: [][2]int{{15, 15}}, light: [][2]int{{50, 50}}},
		{name: "a larger second clip does not widen", content: "20 20 20 20 re W n 0 0 100 100 re W n" + full, ink: 400},
		{name: "a general second clip does not widen", content: "20 20 20 20 re W n -50 -50 m 300 -50 l -50 300 l h W n" + full, ink: 400},
		{name: "intersection of two general clips", content: "0 0 m 100 0 l 0 100 l h W n 100 100 m 0 100 l 100 0 l h W n" + full, ink: -1,
			light: [][2]int{{10, 10}, {90, 90}, {10, 90}, {90, 10}}},
		{name: "Q restores the clip", content: "q 20 20 20 20 re W n Q" + full, ink: 100 * 100},
		{name: "Q restores a general clip", content: "q 0 0 m 100 0 l 0 100 l h W n Q" + full, ink: 100 * 100},
		{name: "W n with no path does not narrow", content: "W n" + full, ink: 100 * 100},
		{name: "h W n does not narrow", content: "h W n" + full, ink: 100 * 100},
		{name: "zero-area path clips everything", content: "10 10 0 50 re W n" + full, ink: 0},
		{name: "one-point path clips everything", content: "10 10 m W n" + full, ink: 0},
		{name: "line path clips everything", content: "10 10 m 90 90 l W n" + full, ink: 0},
		{name: "mark survives Q", content: "q 20 20 20 20 re W Q n" + full, ink: 400},
		{name: "mark survives an image Do", content: "20 20 20 20 re W q 1 0 0 1 25 25 cm /Im Do Q n" + full, ink: 400},
		{name: "last of W and W* sets the rule", content: "10 10 80 80 re 30 30 40 40 re W W* n" + full, ink: 80*80 - 40*40},
		{name: "path after W extends the clip path (Q3 a)", content: "20 20 20 20 re W 60 60 20 20 re n" + full, ink: 800},
		{name: "W inside BT applies", content: "BT 20 20 20 20 re W n ET" + full, ink: 400},
		{name: "W without a painting operator never clips", content: "20 20 20 20 re W" + full, ink: 100 * 100},
		{name: "NaN path clips everything", content: "q " + spelled("1", 200) + " 0 0 " + spelled("1", 200) + " 0 0 cm " +
			spelled("1", 200) + " 0 0 " + spelled("1", 200) + " 0 0 cm 0 0 1 1 re Q W n" + full, ink: 0},
		{name: "huge rect clip covers the page", content: "-" + spelled("1", 300) + " -" + spelled("1", 300) + " " + spelled("2", 300) + " " + spelled("2", 300) + " re W n" + full, ink: 100 * 100},
		{name: "fill after an empty clip paints nothing", content: "0 0 0 0 re W n 1 w 0 0 m 100 100 l S" + full + " 100 0 0 100 0 0 cm /Im Do", ink: 0},
		{name: "rotated rect is a general clip", content: "q 0.8 0.6 -0.6 0.8 50 0 cm 0 0 40 40 re W n" + full + " Q", ink: -1,
			dark: [][2]int{{48, 90}}, light: [][2]int{{5, 5}, {95, 95}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := clipRender(t, 100, 100, clipRes, tt.content, clipObjects()...)
			if tt.ink >= 0 {
				if n := inkCount(img); n != tt.ink {
					t.Errorf("ink = %d px, want %d", n, tt.ink)
				}
			}
			for _, p := range tt.dark {
				if c := rgbaAt(img, p[0], p[1]); c != black {
					t.Errorf("pixel %v = %v, want black", p, c)
				}
			}
			for _, p := range tt.light {
				if c := rgbaAt(img, p[0], p[1]); c != white {
					t.Errorf("pixel %v = %v, want white", p, c)
				}
			}
			for _, p := range tt.colors {
				if c := rgbaAt(img, p.x, p.y); c != p.c {
					t.Errorf("pixel (%d,%d) = %v, want %v", p.x, p.y, c, p.c)
				}
			}
		})
	}
}

type pixColor struct {
	x, y int
	c    color.RGBA
}

// TestClip_Forms is §10.1 item 7: a W inside a form is undone at exit, an
// outer W clips the form's content, and the /BBox intersects the clip.
func TestClip_Forms(t *testing.T) {
	form := func(bbox, content string) string {
		return streamObjDict("/Type /XObject /Subtype /Form /BBox ["+bbox+"]", content)
	}
	res := "<< /XObject << /Fm 5 0 R >> >>"
	const full = "0 g 0 0 100 100 re f"
	tests := []struct {
		name, page, form, bbox string
		ink                    int
	}{
		{"W inside a form is undone at exit", "/Fm Do " + full, "0 0 10 10 re W n", "0 0 100 100", 100 * 100},
		{"unbalanced q W n inside a form is undone", "/Fm Do " + full, "q q 0 0 m 10 0 l 0 10 l h W n", "0 0 100 100", 100 * 100},
		{"a stray Q in a form cannot leak its clip", "/Fm Do " + full, "Q Q 0 0 m 10 0 l 0 10 l h W n", "0 0 100 100", 100 * 100},
		{"outer W clips the form", "20 20 20 20 re W n /Fm Do", full, "0 0 100 100", 400},
		{"outer general clip clips the form", "0 0 m 100 0 l 0 100 l h W n /Fm Do " + "1 g 0 0 100 100 re f", full, "0 0 100 100", 0},
		{"BBox intersects W", "20 20 60 60 re W n /Fm Do", full, "0 0 30 30", 100},
		{"form W intersects BBox and outer W", "20 20 60 60 re W n /Fm Do", "25 25 50 50 re W n " + full, "0 0 30 30", 25},
		{"pending W at Do applies after the form", "20 20 20 20 re W /Fm Do n " + full, "", "0 0 100 100", 400},
		{"pending W in a form does not leak to the caller", "/Fm Do n " + full, "20 20 20 20 re W", "0 0 100 100", 100 * 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := clipRender(t, 100, 100, res, tt.page, form(tt.bbox, tt.form))
			if n := inkCount(img); n != tt.ink {
				t.Errorf("ink = %d px, want %d", n, tt.ink)
			}
		})
	}
}

// TestClip_OverlayIsNotClipped is §10.1 item 11 (mandatory, §4): page content
// that ends inside an unbalanced clip must not clip the annotation overlay.
func TestClip_OverlayIsNotClipped(t *testing.T) {
	for _, content := range []string{
		"0 0 1 1 re W n",
		"q q 0 0 m 1 0 l 0 1 l h W n",
		"0 0 0 0 re W n", // empty clip
	} {
		t.Run(content, func(t *testing.T) {
			pdf := clipPage(100, 100, "", content)
			r, err := parse.NewResolver(pdf)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := r.Pages()
			if err != nil {
				t.Fatal(err)
			}
			data, err := Page(r, pages[0], Options{DPI: 72, Annotations: []Annotation{
				{Type: "highlight", X: 10, Y: 10, Width: 80, Height: 80, Color: "#000000", Opacity: 1},
			}})
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if n := inkCount(img); n != 80*80 {
				t.Errorf("overlay ink = %d px, want %d (the overlay was clipped)", n, 80*80)
			}
		})
	}
}

// pixelsOf returns the pixels of a w×h canvas for which in reports true.
func pixelsOf(w, h int, in func(x, y int) bool) []bool {
	out := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out[y*w+x] = in(x, y)
		}
	}
	return out
}

// filled returns the pixels fillPath paints for the path.
func filled(w, h int, path [][]point, evenOdd bool) []bool {
	rs := newRaster(w, h)
	rs.fillPath(path, black, evenOdd)
	return pixelsOf(w, h, func(x, y int) bool { return rs.img.RGBAAt(x, y) == black })
}

// clipped returns the pixels inside a clip state.
func clipped(w, h int, c clipState) []bool {
	return pixelsOf(w, h, func(x, y int) bool {
		return c.rect.contains(x, y) && (c.mask == nil || c.mask.bit(x, y))
	})
}

func diffAt(a, b []bool, w int) (int, int, bool) {
	for i := range a {
		if a[i] != b[i] {
			return i % w, i / w, true
		}
	}
	return 0, 0, false
}

// TestClip_RectFastPathMatchesMaskAndFill is §10.1 item 12 and the §2.1 rule
// for rectangles: random rectangles (fractional, negative width and height,
// off-canvas, on pixel boundaries and centers) through all four /Rotate
// matrices give the same pixels by the fast path, by a mask and by a fill.
func TestClip_RectFastPathMatchesMaskAndFill(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	coord := func(limit float64) float64 {
		switch rng.IntN(8) {
		case 0:
			return math.Round(rng.Float64()*limit) + 0.5
		case 1:
			return math.Round(rng.Float64() * limit)
		case 2:
			return (rng.Float64()*3 - 1) * limit * 10
		default:
			return (rng.Float64()*1.4 - 0.2) * limit
		}
	}
	mb := parse.Rectangle{LLX: 3, LLY: -7, URX: 63, URY: 44}
	for iter := 0; iter < 2000; iter++ {
		rot := []int{0, 90, 180, 270}[iter%4]
		s := []float64{1, 0.75, 1.37, 2}[rng.IntN(4)]
		base, w, h, err := deviceMatrix(mb, rot, s)
		if err != nil {
			t.Fatal(err)
		}
		x, y := coord(70), coord(60)
		rw, rh := coord(70)-x, coord(60)-y
		var pts []point
		for _, c := range [4][2]float64{{x, y}, {x + rw, y}, {x + rw, y + rh}, {x, y + rh}} {
			dx, dy := base.apply(c[0], c[1])
			pts = append(pts, point{dx, dy})
		}
		path := [][]point{pts}
		minX, minY, maxX, maxY, ok := rectPath(path)
		if !ok {
			t.Fatalf("iter %d: rect %v not detected as a rectangle", iter, pts)
		}
		fast := clipped(w, h, clipState{rect: rectPixels(minX, minY, maxX, maxY, w, h)})
		for _, evenOdd := range []bool{false, true} {
			fill := filled(w, h, path, evenOdd)
			rs := newRaster(w, h)
			mask := clipped(w, h, rs.buildClip(fullClipState(w, h), path, evenOdd, 0))
			if x, y, d := diffAt(fill, fast, w); d {
				t.Fatalf("iter %d rot %d: fast path differs from fill at (%d,%d) for %v", iter, rot, x, y, pts)
			}
			if x, y, d := diffAt(fill, mask, w); d {
				t.Fatalf("iter %d rot %d: mask differs from fill at (%d,%d) for %v", iter, rot, x, y, pts)
			}
		}
	}
}

// TestClip_MaskMatchesFill is the §2.1 rule for general paths, and the
// intersection with an enclosing clip: random paths (self-intersecting,
// off-canvas, infinite and NaN points, both rules) inside a random enclosing
// clip (a rectangle and a mask) give exactly fill ∩ enclosing.
func TestClip_MaskMatchesFill(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	const w, h = 150, 70 // three word columns
	coord := func(limit float64) float64 {
		switch rng.IntN(25) {
		case 0:
			return math.Round(rng.Float64()*limit) + 0.5
		case 1:
			return (rng.Float64()*3 - 1) * limit * 10
		case 2:
			return math.Inf(1 - 2*rng.IntN(2))
		case 3:
			if rng.IntN(4) == 0 {
				return math.NaN()
			}
			fallthrough
		default:
			return (rng.Float64()*1.4 - 0.2) * limit
		}
	}
	randPath := func() [][]point {
		var path [][]point
		for s := rng.IntN(3) + 1; s > 0; s-- {
			var sp []point
			for n := rng.IntN(9); n > 0; n-- {
				sp = append(sp, point{coord(w), coord(h)})
			}
			path = append(path, sp)
		}
		return path
	}
	for iter := 0; iter < 1500; iter++ {
		rs := newRaster(w, h)
		outer := fullClipState(w, h)
		var outerPx []bool
		switch iter % 3 {
		case 0:
			outerPx = clipped(w, h, outer)
		case 1:
			outer.rect = clipRect{x0: rng.IntN(w), y0: rng.IntN(h), x1: rng.IntN(w + 1), y1: rng.IntN(h + 1)}
			outerPx = clipped(w, h, outer)
		case 2:
			p := randPath()
			eo := rng.IntN(2) == 0
			outer = rs.buildClip(outer, p, eo, 0)
			outerPx = filled(w, h, p, eo)
			if x, y, d := diffAt(outerPx, clipped(w, h, outer), w); d {
				t.Fatalf("iter %d: outer mask differs from fill at (%d,%d) for %v", iter, x, y, p)
			}
		}
		path := randPath()
		for _, evenOdd := range []bool{false, true} {
			fill := filled(w, h, path, evenOdd)
			for i := range fill {
				fill[i] = fill[i] && outerPx[i]
			}
			got := rs.buildClip(outer, path, evenOdd, 0)
			if x, y, d := diffAt(fill, clipped(w, h, got), w); d {
				t.Fatalf("iter %d evenOdd=%v: clip differs from fill ∩ outer at (%d,%d) for %v", iter, evenOdd, x, y, path)
			}
			if got.mask != nil {
				checkTight(t, got)
			}
		}
	}
}

// checkTight verifies a built clip's rect is the tight box of its set bits and
// each row's extent bounds the row's bits.
func checkTight(t *testing.T, c clipState) {
	t.Helper()
	m := c.mask
	tight := clipRect{x0: math.MaxInt, y0: math.MaxInt, x1: math.MinInt, y1: math.MinInt}
	for y := m.y0; y < m.y1; y++ {
		lo, hi := m.rowRange(y)
		for x := m.x0w * 64; x < m.x1w*64; x++ {
			if m.bit(x, y) {
				if x < lo || x >= hi {
					t.Fatalf("bit (%d,%d) outside its row range [%d,%d)", x, y, lo, hi)
				}
				tight.x0, tight.x1 = min(tight.x0, x), max(tight.x1, x+1)
				tight.y0, tight.y1 = min(tight.y0, y), max(tight.y1, y+1)
			}
		}
	}
	if tight != c.rect {
		t.Fatalf("rect %v, tight box of the bits %v", c.rect, tight)
	}
}

// liveMaskBytes is the liveness oracle: the bytes of the distinct masks the
// current state, the saved states, the active forms' saved states and the
// open text object's clip accumulation (textclip.go) hold.
func liveMaskBytes(in *interp) int64 {
	seen := map[*clipMask]bool{}
	add := func(m *clipMask) {
		if m != nil {
			seen[m] = true
		}
	}
	add(in.gs.clip.mask)
	for _, s := range in.stack {
		add(s.clip.mask)
	}
	for _, m := range in.formClips {
		add(m)
	}
	if in.textClip != nil {
		add(in.textClip.mask)
	}
	var n int64
	for m := range seen {
		n += m.bytes
	}
	return n
}

// TestClip_Liveness is §10.1 item 14: after every operator of scripted and
// random q/W/Q/form sequences, including maxSaveDepth overflow, forms with
// stray Q and clip-mode text objects (complete, left open, with q or Q
// inside, around a Do), the live clip-mask counter equals the bytes of the
// masks something still holds.
func TestClip_Liveness(t *testing.T) {
	forms := map[string]string{
		"F1": "q 0 0 m 50 0 l 0 50 l h W n Q Q 10 10 m 90 10 l 10 90 l h W n",
		"F2": "q q 5 5 m 95 5 l 5 95 l h W n /F3 Do",
		"F3": "0 0 m 100 0 l 0 100 l h W n Q 1 1 m 99 1 l 1 99 l h W* n",
		"F4": "Q 20 20 m 80 20 l 20 80 l h W n q 30 30 m 70 30 l 30 70 l h W n",
		"F5": "BT 7 Tr /F1 40 Tf 5 30 Td (Hx) Tj ET q BT 4 Tr /F1 30 Tf (I) Tj",
		"F6": "Q BT 7 Tr /F1 40 Tf 5 30 Td (H) Tj /F5 Do ET",
	}
	objs := map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3:  "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /F1 5 0 R /F2 6 0 R /F3 7 0 R /F4 8 0 R /F5 9 0 R /F6 10 0 R >> /Font << /F1 11 0 R >> >> /Contents 4 0 R >>",
		4:  streamObj(""),
		11: "<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FontDescriptor 12 0 R >>",
		12: "<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontFile2 13 0 R >>",
		13: streamObj(string(goregular.TTF)),
	}
	for i, n := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
		objs[5+i] = streamObjDict("/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /XObject << /F3 7 0 R /F5 9 0 R >> /Font << /F1 11 0 R >> >>", forms[n])
	}
	r, err := parse.NewResolver(buildRenderPDF(objs, "<< /Size 14 /Root 1 0 R >>"))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	// run reports how many text accumulations held a mask after a glyph.
	run := func(t *testing.T, src string) (accumulated int) {
		t.Helper()
		rs := newRaster(100, 100)
		in := newInterp(rs, identity(), r, pages[0].Resources)
		for i, op := range parse.ScanContent([]byte(src)) {
			in.apply(op)
			if got, want := rs.b.clipMask, liveMaskBytes(in); got != want {
				t.Fatalf("after op %d (%s): live clip-mask bytes %d, held %d", i, op.Op, got, want)
			}
			if op.Op == "Tj" && in.textClip != nil && in.textClip.mask != nil {
				accumulated++
			}
		}
		return accumulated
	}

	tri := func(k int) string { return fmt.Sprintf("%d 0 m 100 %d l 0 100 l h", k, k) }
	t.Run("scripted", func(t *testing.T) {
		run(t, "q "+tri(1)+" W n q "+tri(2)+" W n "+tri(3)+" W n Q "+tri(4)+" W* n Q "+tri(5)+" W n "+
			"q q /F1 Do Q /F2 Do /F4 Do Q "+tri(6)+" W n 10 10 50 50 re W n Q Q Q")
	})
	t.Run("text", func(t *testing.T) {
		if n := run(t, "q "+tri(3)+" W n BT 7 Tr /F1 40 Tf 5 30 Td (Hx) Tj ET q BT 4 Tr (I) Tj Q ET "+
			"BT 7 Tr (H) Tj q (x) Tj ET Q BT 7 Tr (H) Tj BT 7 Tr ( ) Tj ET /F5 Do /F6 Do "+
			"q BT 7 Tr (H) Tj /F5 Do ET Q Q BT 7 Tr (Hx) Tj"); n < 5 {
			t.Fatalf("%d text accumulations, want ≥ 5", n)
		}
	})
	t.Run("past maxSaveDepth", func(t *testing.T) {
		run(t, tri(1)+" W n "+strings.Repeat("q ", maxSaveDepth+5)+tri(2)+" W n "+tri(3)+" W n /F1 Do "+
			strings.Repeat("Q ", 7)+tri(4)+" W n "+strings.Repeat("Q ", maxSaveDepth+5))
	})
	t.Run("random", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(31, 32))
		texts := 0
		for iter := 0; iter < 300; iter++ {
			var b strings.Builder
			for n := rng.IntN(60); n > 0; n-- {
				switch rng.IntN(14) {
				case 0, 1:
					b.WriteString("q ")
				case 2, 3:
					b.WriteString("Q ")
				case 4:
					fmt.Fprintf(&b, "%s W n ", tri(rng.IntN(20)))
				case 5:
					fmt.Fprintf(&b, "%s W* f ", tri(rng.IntN(20)))
				case 6:
					fmt.Fprintf(&b, "%d %d 50 50 re W n ", rng.IntN(40), rng.IntN(40))
				case 7:
					fmt.Fprintf(&b, "/F%d Do ", 1+rng.IntN(4))
				case 8:
					b.WriteString("0 0 0 0 re W n ")
				case 9:
					fmt.Fprintf(&b, "BT %d Tr /F1 %d Tf %d 20 Td (Hx) Tj ", 4+rng.IntN(4), 10+rng.IntN(60), rng.IntN(60))
				case 10:
					b.WriteString("ET ")
				case 11:
					b.WriteString("BT ")
				case 12:
					b.WriteString("( ) Tj ")
				case 13:
					fmt.Fprintf(&b, "/F%d Do ", 5+rng.IntN(2))
				}
			}
			texts += run(t, b.String())
		}
		if texts < 100 {
			t.Fatalf("%d text accumulations in 300 sequences, want ≥ 100", texts)
		}
	})
}

// TestClip_LiveCapTrips is Q2 (a): nested non-rectangular clips past the live
// clip-mask cap stop the render with ErrBudgetExceeded naming "clip masks",
// while a sequence that frees its masks never trips.
func TestClip_LiveCapTrips(t *testing.T) {
	tri := func(k int) string { return fmt.Sprintf("%d 0 m 100 %d l 0 100 l h W n ", k, k) }
	var nested, freed strings.Builder
	for i := 0; i < 40; i++ {
		nested.WriteString("q " + tri(i%10))
		freed.WriteString("q " + tri(i%10) + "Q ")
	}
	lim := withLimitT(func(l *budgetLimits) { l.clipMask = 16 << 10 })
	r, pi := contentPage(t, nested.String()+" 0 0 100 100 re f")
	_, err := Page(r, pi, Options{DPI: 72, testLimits: lim})
	if !errors.Is(err, ErrBudgetExceeded) || !strings.Contains(err.Error(), "clip masks") {
		t.Fatalf("nested clips: err = %v, want a clip masks trip", err)
	}
	r, pi = contentPage(t, freed.String()+" 0 0 100 100 re f")
	if _, err := Page(r, pi, Options{DPI: 72, testLimits: lim}); err != nil {
		t.Fatalf("freed clips: %v", err)
	}
	if got := resolveLimits(0, 0, 100).clipMask; got != maxClipMaskBytes {
		t.Errorf("default clip-mask limit %d, want %d", got, maxClipMaskBytes)
	}
}

// TestClip_RectClipAllocatesNothing: a rectangular clip adds no allocation
// to its operators (§3.4): re W n allocates exactly what re n does.
func TestClip_RectClipAllocatesNothing(t *testing.T) {
	measure := func(src string) float64 {
		rs := newRaster(100, 100)
		in := newInterp(rs, identity(), nil, nil)
		ops := parse.ScanContent([]byte(src))
		return testing.AllocsPerRun(200, func() {
			for _, op := range ops {
				in.apply(op)
			}
		})
	}
	withClip := measure("q 0 0 100 100 re W n Q q 10.5 10 50 50 re W* n Q")
	without := measure("q 0 0 100 100 re n Q q 10.5 10 50 50 re n Q")
	if withClip != without {
		t.Errorf("allocations per run: %v with rectangle clips, %v without", withClip, without)
	}
}

// TestClip_Charges pins the raster-work charge of a mask build: the scan
// charges what a fill's scan charges, plus clipRowCost and clipWords per row
// and pass up front (one pass, two with an enclosing mask) and clipWords per
// span; a rectangle clip charges nothing.
func TestClip_Charges(t *testing.T) {
	path := [][]point{{{0, 0}, {100, 0}, {0, 100}}}
	rs := newRaster(100, 100)
	rs.buildClip(fullClipState(100, 100), path, false, 0)
	clipCharge := rs.b.paint

	scan := newRaster(100, 100)
	spans := int64(0)
	scan.scanPath(path, false, 0, 100, func(x0, x1 float64, y int) {
		if a, b, ok := spanPixels(x0, x1, 0, 100); ok {
			spans += clipWords((b-1)>>6 - a>>6 + 1)
		}
	})
	want := scan.b.paint + 100*(clipRowCost+clipWords(2)) + spans
	if clipCharge != want {
		t.Errorf("mask build charged %d, want %d", clipCharge, want)
	}

	in := newInterp(newRaster(100, 100), identity(), nil, nil)
	for _, op := range parse.ScanContent([]byte("10 10 50 50 re W n")) {
		in.apply(op)
	}
	if in.b.paint != 0 || in.b.clipMask != 0 {
		t.Errorf("rect clip charged %d paint, %d mask bytes; want 0", in.b.paint, in.b.clipMask)
	}
}

// clipParityFixture is the content of the fixture whose pixel hash is pinned
// here and in wasm/smoke_test.mjs, so native amd64 and js/wasm renders are
// checked byte-identical (§7). arm64 is not checked in CI: Go may fuse
// multiply-adds there, and code outside clip.go (matrix.apply, raster.line,
// blend, drawImage, segmentsToSubpaths) has not been written to prevent it.
// The fixture holds nested general clips of
// both rules, a rectangle clip, a clipped image, a clipped form and a
// clipped stroke.
const clipParityFixture = "q 50 3 m 97 40.5 l 80.25 97 l 12 85 l 4.5 30 l h 30 30 40 40 re W* n " +
	"0.2 0.4 0.8 rg 0 0 100 100 re f q 10.3 10.7 79 79 re W n 100 0 0 100 0 0 cm /Im Do Q " +
	"50 10 m 90 10 90 90 50 90 c 10 90 10 10 50 10 c h W n /Fm Do 1 0 0 RG 4 w 0 50 m 100 60 l S Q"

// clipParityHash is the pixel hash of clipParityFixture at 72 DPI (imgHash).
const clipParityHash = "3192d00f3cd466d6f10b7cc19d762a5a3159da0ba1a21bbe92a7b05b71d3fb2f"

// TestClip_ParityHash pins one clip fixture's pixels, so native amd64 and
// js/wasm renders are byte-identical (§7; arm64 is not checked, see
// clipParityFixture).
func TestClip_ParityHash(t *testing.T) {
	img := clipRender(t, 100, 100, clipRes, clipParityFixture, clipObjects()...)
	if got := imgHash(img); got != clipParityHash {
		t.Errorf("clip parity fixture hash = %s, want %s", got, clipParityHash)
	}
}

// clipParityPDF is wasm/testdata/clip.pdf, the parity fixture as a file:
// wasm/smoke_test.mjs renders it inside the js/wasm module and compares its
// pixel hash with clipParityHash.
const clipParityPDF = "../../wasm/testdata/clip.pdf"

// TestClip_ParityFixtureFile keeps wasm/testdata/clip.pdf in step with
// clipParityFixture (FLUXDOCS_WRITE_CLIP_FIXTURE=1 rewrites it), so the smoke
// test's hash and this package's pin describe the same page.
func TestClip_ParityFixtureFile(t *testing.T) {
	want := clipPage(100, 100, clipRes, clipParityFixture, clipObjects()...)
	if os.Getenv("FLUXDOCS_WRITE_CLIP_FIXTURE") == "1" {
		if err := os.WriteFile(clipParityPDF, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(clipParityPDF)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: rerun with FLUXDOCS_WRITE_CLIP_FIXTURE=1", clipParityPDF)
	}
	if h := imgHash(renderPDF(t, got)); h != clipParityHash {
		t.Errorf("%s hash = %s, want %s", clipParityPDF, h, clipParityHash)
	}
}

// TestClip_SmokeHashInStep keeps wasm/smoke_test.mjs's copy of
// clipParityHash from drifting: the smoke test compares the js/wasm render
// with the hash it holds, not with this package's pin.
func TestClip_SmokeHashInStep(t *testing.T) {
	const smoke = "../../wasm/smoke_test.mjs"
	src, err := os.ReadFile(smoke)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(`const clipHash = "`+clipParityHash+`";`)) {
		t.Errorf("%s does not hold clipHash %q (clipParityHash)", smoke, clipParityHash)
	}
}

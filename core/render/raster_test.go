package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// renderWithin renders content on the 100x100pt test page at DPI 72 (1px/pt) in
// a goroutine and fails the test if it has not finished after d. The deadline
// turns an unbounded rasterizer loop into a test failure instead of a hang (the
// goroutine is abandoned on timeout). Under js/wasm there is no async
// preemption, so a regression there hangs until go test -timeout instead;
// keep a modest -timeout on wasm test runs.
func renderWithin(t *testing.T, content string, d time.Duration) image.Image {
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
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := Page(r, pages[0], Options{DPI: 72, Format: "png"})
		done <- result{data, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Page: %v", res.err)
		}
		img, err := png.Decode(bytes.NewReader(res.data))
		if err != nil {
			t.Fatalf("decode PNG: %v", err)
		}
		return img
	case <-time.After(d):
		t.Fatalf("render still running after %v (unbounded stroke work)", d)
		return nil
	}
}

// isDark reports whether the pixel at (x, y) is near-black stroke ink.
func isDark(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 < 128 && g>>8 < 128 && b>>8 < 128
}

// TestStroke_HostileInputIsBounded pins SECURITY.md §2 for the stroke
// rasterizer: segment length, coordinate magnitude and line width come from an
// untrusted content stream, so the work must be bounded by the canvas. Each case
// must finish well inside the deadline, and where the stroke crosses the page
// the visible ink must still be painted (clipping must not drop it).
func TestStroke_HostileInputIsBounded(t *testing.T) {
	// The lexer has no exponent syntax, so large magnitudes are spelled out.
	big := "1" + strings.Repeat("0", 30) + ".0"   // 1e30
	big2 := "2" + strings.Repeat("0", 30) + ".0"  // 2e30
	huge := "1" + strings.Repeat("0", 308) + ".0" // ~1e308: finite, but cm products overflow to Inf/NaN

	allDark := func(t *testing.T, img image.Image) {
		if got := darkPixels(img); got != 100*100 {
			t.Errorf("dark pixels = %d, want 10000 (stroke wider than the page covers it)", got)
		}
	}
	blank := func(t *testing.T, img image.Image) {
		if got := nonWhitePixels(img); got != 0 {
			t.Errorf("non-white pixels = %d, want 0 (stroke lies entirely off the page)", got)
		}
	}
	// inkAt asserts the listed device pixels are dark and the off pixels white.
	inkAt := func(on, off [][2]int) func(*testing.T, image.Image) {
		return func(t *testing.T, img image.Image) {
			for _, p := range on {
				if !isDark(img, p[0], p[1]) {
					t.Errorf("pixel %v = %v, want stroke ink", p, img.At(p[0], p[1]))
				}
			}
			for _, p := range off {
				if isDark(img, p[0], p[1]) {
					t.Errorf("pixel %v = %v, want white (off the stroke)", p, img.At(p[0], p[1]))
				}
			}
		}
	}
	row50 := inkAt([][2]int{{0, 50}, {50, 50}, {99, 50}}, [][2]int{{50, 10}, {50, 90}})
	noCheck := func(*testing.T, image.Image) {}

	cases := []struct {
		name    string
		content string
		check   func(*testing.T, image.Image)
	}{
		// The three inputs from the go-tester report.
		{"length 1e9", "0 0 m 1000000000 0 l S", inkAt([][2]int{{0, 99}, {99, 99}}, [][2]int{{50, 50}})},
		{"width 1e5", "100000 w 50 50 m 51 50 l S", allDark},
		// Literally "1e30" lexes as 1 followed by an e30 operator; kept as reported.
		{"reported 1e30 spelling", "0 0 m 1e30 0 l S", noCheck},

		{"long horizontal through page", "0 50 m 1000000000 50 l S", row50},
		{"endpoint at 1e30", "0 50 m " + big + " 50 l S", row50},
		{"width 1e30", big + " w 50 50 m 51 50 l S", allDark},
		{"width ~1e308", huge + " w 50 50 m 51 50 l S", allDark},
		{"negative width is 1px", "-100000 w 10 50 m 90 50 l S", inkAt([][2]int{{50, 50}}, [][2]int{{50, 10}, {5, 50}})},
		{"far outside the page", big + " " + big + " m " + big2 + " " + big + " l S", blank},
		{"far outside, very wide", "1000 w " + big + " " + big + " m " + big2 + " " + big + " l S", blank},
		{"crossing horizontally from +-1e30", "-" + big + " 50 m " + big + " 50 l S", row50},
		{"crossing vertically from +-1e30", "50 -" + big + " m 50 " + big + " l S",
			inkAt([][2]int{{50, 0}, {50, 50}, {50, 99}}, [][2]int{{10, 50}, {90, 50}})},
		// User y = x maps to device y = 100 - x on this page.
		{"crossing diagonally from +-1e9", "-1000000000 -1000000000 m 1000000000 1000000000 l S",
			inkAt([][2]int{{10, 90}, {50, 50}, {90, 10}}, [][2]int{{10, 10}, {90, 90}})},
		// At 1e30 the page offset is below float64 resolution; only boundedness is
		// asserted for an oblique segment.
		{"crossing diagonally from +-1e30", "-" + big + " " + big + " m " + big + " -" + big + " l S", noCheck},
		{"many long segments", strings.Repeat("0 50 m 1000000000 50 l ", 50) + "S", row50},
		// cm overflow: Inf coordinates/width, and NaN after a second Inf product.
		{"Inf from cm", huge + " 0 0 " + huge + " 0 0 cm 1 1 m 2 2 l S", noCheck},
		{"NaN from cm", huge + " 0 0 " + huge + " 0 0 cm " + huge + " 0 0 " + huge + " 0 0 cm 0 0 m 1 1 l S", noCheck},
		// The page sits deep inside this rectangle, beyond the reach of its edges.
		{"wide stroke of a huge rect", "100000 w -1000000000 -1000000000 2000000000 2000000000 re S", blank},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderWithin(t, tc.content, 2*time.Second)
			tc.check(t, img)
		})
	}
}

// lineBySamples is the pre-fix stroke rasterizer (one width×width stamp per
// sample), kept as an oracle for pixel compatibility. It is only safe on small,
// finite inputs.
func lineBySamples(r *raster, a, b point, col color.RGBA, width float64) {
	dx := b.X - a.X
	dy := b.Y - a.Y
	length := math.Hypot(dx, dy)
	half := width / 2
	steps := int(math.Ceil(length)) + 1
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		cx := a.X + t*dx
		cy := a.Y + t*dy
		for oy := int(math.Floor(cy - half)); oy <= int(math.Ceil(cy+half)); oy++ {
			for ox := int(math.Floor(cx - half)); ox <= int(math.Ceil(cx+half)); ox++ {
				r.blend(ox, oy, col)
			}
		}
	}
}

// TestStroke_MatchesSampledStamps checks that the bounded row-span stroke paints
// exactly the pixels of the original sample-and-stamp rasterizer for ordinary
// opaque strokes whose endpoints lie on the paintable area, with and without a
// narrowed clip.
func TestStroke_MatchesSampledStamps(t *testing.T) {
	const w, h = 41, 33
	rng := rand.New(rand.NewPCG(1, 2))
	clips := []clipRect{fullClip(w, h), {5, 4, 30, 25}}
	for i := 0; i < 4000; i++ {
		clip := clips[i%len(clips)]
		pt := func() point {
			return point{
				X: float64(clip.x0) + rng.Float64()*float64(clip.x1-clip.x0),
				Y: float64(clip.y0) + rng.Float64()*float64(clip.y1-clip.y0),
			}
		}
		a, b := pt(), pt()
		switch i % 5 { // include axis-aligned, degenerate and integer-aligned segments
		case 1:
			b.Y = a.Y
		case 2:
			b.X = a.X
		case 3:
			b = a
		case 4:
			a = point{math.Round(a.X), math.Round(a.Y)}
			b = point{math.Round(b.X), math.Round(b.Y)}
		}
		width := 1 + rng.Float64()*7
		if i%3 == 0 {
			width = math.Round(width)
		}
		col := color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 0xff}

		want := newRaster(w, h)
		want.clip = clip
		lineBySamples(want, a, b, col, width)
		got := newRaster(w, h)
		got.clip = clip
		got.line(a, b, col, width)
		if !bytes.Equal(want.img.Pix, got.img.Pix) {
			t.Fatalf("case %d: a=%v b=%v width=%v clip=%v: pixels differ from the sampled-stamp reference", i, a, b, width, clip)
		}
	}
}

//go:build !js

package render

import (
	"image"
	"os"
	"os/exec"
	"testing"
)

// TestClip_GhostscriptOracle compares the clipping decisions of
// docs/design/clipping-paths.md §2.2 (and Q3) and a few clip shapes with
// Ghostscript, an external black-box oracle (AGPL) run as a separate process
// in tests only: nothing is linked or vendored. Each fixture clips a
// full-page black fill on a 100×100 pt page at 72 DPI. It skips without gs on
// PATH unless FLUXDOCS_REQUIRE_GS=1.
//
// Ghostscript fills by a different pixel rule (a pixel any part of which the
// shape touches), so the ink boxes are compared within ±1 px and the ink
// counts within tol. One recorded disagreement follows from that rule: a
// zero-area clip path that is not a point (a line) keeps a one-pixel line in
// Ghostscript and clips everything here, because a clip covers exactly the
// pixels a fill of its path would (§2.1), and a fill of a line paints none.
func TestClip_GhostscriptOracle(t *testing.T) {
	gs, _ := exec.LookPath("gs")
	if gs == "" {
		if os.Getenv("FLUXDOCS_REQUIRE_GS") == "1" {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not installed")
	}
	const full = " 0 g 0 0 100 100 re f"
	tests := []struct {
		name, clip string
		tol        float64 // allowed |ours − gs| / gs in ink count
		gsLine     bool    // the recorded disagreement: gs keeps a line, we keep nothing
	}{
		{name: "W n with no path does not narrow", clip: "W n"},
		{name: "h W n does not narrow", clip: "h W n"},
		{name: "zero-area rectangle clips everything", clip: "10 10 0 50 re W n"},
		{name: "one-point path clips everything", clip: "10 10 m W n"},
		{name: "line path", clip: "10 10 m 90 90 l W n", gsLine: true},
		{name: "mark survives Q", clip: "q 20 20 20 20 re W Q n"},
		{name: "last of W and W* sets the rule", clip: "10 10 80 80 re 30 30 40 40 re W W* n"},
		{name: "Q3: path after W is part of the clip", clip: "20 20 20 20 re W 60 60 20 20 re n"},
		{name: "W inside BT", clip: "BT 20 20 20 20 re W n ET"},
		{name: "W never painted does not clip", clip: "20 20 20 20 re W"},
		{name: "fractional rectangle", clip: "20.3 20.6 20.4 20.2 re W n", tol: 0.06},
		{name: "triangle", clip: "10 5 m 95 50.5 l 20.25 95 l h W n", tol: 0.06},
		{name: "even-odd ring", clip: "5 5 90 90 re 30.5 30.5 40 40 re W* n", tol: 0.06},
		{name: "nested general clips", clip: "0 0 m 100 0 l 0 100 l h W n 100 100 m -10 100 l 100 -10 l h W n", tol: 0.25},
	}
	page := image.Rect(0, 0, 100, 100)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pdf := clipPage(100, 100, "", tt.clip+full)
			ours, theirs := renderPDF(t, pdf), gsPNG(t, gs, pdf)
			a, okA := inkBox(ours, page)
			b, okB := inkBox(theirs, page)
			if tt.gsLine {
				if okA || !okB || b.Dx() < 70 || b.Dy() < 70 {
					t.Errorf("recorded disagreement changed: ours %v (%v), Ghostscript %v (%v)", a, okA, b, okB)
				}
				return
			}
			if okA != okB || okA && !near(a, b) {
				t.Fatalf("ink box = %v (%v), Ghostscript %v (%v)", a, okA, b, okB)
			}
			na, nb := darkCount(ours), darkCount(theirs)
			if d := float64(na - nb); d > tt.tol*float64(nb) || -d > tt.tol*float64(nb) {
				t.Errorf("ink = %d px, Ghostscript %d (tolerance %.0f%%)", na, nb, 100*tt.tol)
			}
		})
	}
}

// darkCount counts the pixels inkBox counts as ink.
func darkCount(img image.Image) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if isDark(img, x, y) {
				n++
			}
		}
	}
	return n
}

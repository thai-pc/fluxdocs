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

// TestTextClip_GhostscriptOracle compares text clipping (Tr 4–7,
// docs/design/clipping-paths.md §5) with Ghostscript on 200×200 pt pages with
// an embedded TrueType font, an embedded Type 1 font and a standard 14 font
// (each reader draws its own substitute for it). Each case is a clip and a
// reference: a full-page fill under the clip must paint what the reference
// paints, in each renderer (ours exactly, TestTextClip_Modes; Ghostscript's
// ink box within ±1 px and its ink count within tol). With the embedded fonts
// the two renderers' ink boxes must also agree within ±1 px.
//
// Recorded disagreements, where Ghostscript keeps an accumulation in the
// graphics state and decides at ET by the mode then in effect: a text object
// that ends in a clip mode without showing a glyph clips everything there
// and nothing here (no clip-mode glyph, no narrowing); a show with no font
// (no Tf) clips everything there, and here the unavailable outline discards
// the accumulation, so ET narrows nothing; a switch from a clip mode to a
// painting mode applies the clip at once there, so later glyphs of the
// object are clipped, and here they paint under the old clip; Q inside the
// object drops the glyphs shown since its q there and discards the whole
// accumulation here. Each is fail-visible here (§5). One Ghostscript
// artifact is avoided rather than recorded: text painted in mode 0 right
// after a clip-mode text object shows nothing there, even inside the clip,
// unless some other paint comes between, so no reference here paints text
// after a text clip.
func TestTextClip_GhostscriptOracle(t *testing.T) {
	gs, _ := exec.LookPath("gs")
	if gs == "" {
		if os.Getenv("FLUXDOCS_REQUIRE_GS") == "1" {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not installed")
	}
	const fill = " 0 g 0 0 200 200 re f"
	page := image.Rect(0, 0, 200, 200)
	for _, f := range fontFixtures() {
		show := func(s string) string { return "/F1 60 Tf 10 40 Td (" + s + ") Tj" }
		ink := show(f.ink)
		tests := []struct {
			name, clip, ref  string // ref "": no ink; "full": the whole page
			tol              float64
			oursFull, gsNone bool // a recorded disagreement
			noCross          bool // strokes, which only Ghostscript paints
		}{
			{name: "mode 7", clip: "BT 7 Tr " + ink + " ET" + fill, ref: "BT " + ink + " ET"},
			{name: "mode 4", clip: "BT 4 Tr " + ink + " ET" + fill, ref: "BT " + ink + " ET"},
			{name: "mode 5", clip: "BT 5 Tr " + ink + " ET" + fill, ref: "BT 2 Tr " + ink + " ET", noCross: true},
			{name: "mode 6", clip: "BT 6 Tr " + ink + " ET" + fill, ref: "BT 2 Tr " + ink + " ET", noCross: true},
			{name: "7.0 Tr", clip: "BT 7.0 Tr " + ink + " ET" + fill, ref: "BT " + ink + " ET"},
			{name: "Tj TJ ' and \"", clip: "BT 7 Tr /F1 40 Tf 60 TL 10 120 Td (" + f.ink + ") Tj [(" + f.ink + ") -900 (" + f.ink + ")] TJ (" + f.ink + ") ' 2 1 (" + f.ink + ") \" ET" + fill,
				ref: "BT /F1 40 Tf 60 TL 10 120 Td (" + f.ink + ") Tj [(" + f.ink + ") -900 (" + f.ink + ")] TJ (" + f.ink + ") ' 2 1 (" + f.ink + ") \" ET"},
			{name: "Tc Tz Ts rotated", clip: "BT 7 Tr /F1 45 Tf 6 Tc 140 Tz 8 Ts 0.866 0.5 -0.5 0.866 50 20 Tm (" + f.gap + f.ink + ") Tj ET" + fill,
				ref: "BT /F1 45 Tf 6 Tc 140 Tz 8 Ts 0.866 0.5 -0.5 0.866 50 20 Tm (" + f.gap + f.ink + ") Tj ET"},
			{name: "inside a general W", clip: "0 0 m 200 0 l 0 200 l h W n BT 7 Tr " + ink + " ET" + fill, ref: "0 0 m 200 0 l 0 200 l h W n BT " + ink + " ET", tol: 0.06},
			{name: "two objects intersect", clip: "BT 7 Tr " + ink + " ET BT 7 Tr /F1 60 Tf 25 40 Td (" + f.ink + ") Tj ET" + fill, ref: "cross", tol: 0.15},
			{name: "Q4: spaces clip everything", clip: "BT 7 Tr " + show("   ") + " ET" + fill},
			{name: "no ET", clip: "BT 7 Tr " + ink + fill, ref: "full"},
			{name: "4.5 Tr fills, no clip", clip: "BT 4.5 Tr " + ink + " ET" + fill, ref: "full"},
			{name: "Q after ET restores", clip: "q BT 7 Tr " + ink + " ET Q" + fill, ref: "full"},
			{name: "no clip-mode glyph", clip: "BT 7 Tr ET" + fill, oursFull: true, gsNone: true},
			{name: "Q inside, q outside", clip: "q BT 7 Tr " + ink + " Q ET" + fill, ref: "full"},
			{name: "q and Q inside", clip: "BT 7 Tr /F1 60 Tf 10 40 Td q (" + f.ink + ") Tj Q ET" + fill, oursFull: true, gsNone: true},
			{name: "no font", clip: "BT 7 Tr 10 40 Td (HI) Tj ET" + fill, oursFull: true, gsNone: true},
		}
		for _, tt := range tests {
			t.Run(f.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel() // each case runs gs up to three times
				pdf := f.mk(tt.clip)
				ours, theirs := renderPDF(t, pdf), gsPNG(t, gs, pdf)
				if tt.oursFull {
					if n := darkCount(ours); n != 200*200 {
						t.Errorf("ours: %d dark px, want the full page", n)
					}
					if n := darkCount(theirs); tt.gsNone && n != 0 {
						t.Errorf("recorded disagreement changed: Ghostscript %d dark px, want 0", n)
					}
					return
				}
				var refOurs, refGs image.Image
				switch tt.ref {
				case "cross":
					// Ghostscript paints no text right after a text clip
					// (see above), so the reference is the other renderer.
					if f.name == "std14" {
						return // each reader draws its own substitute
					}
					a, okA := inkBox(ours, page)
					b, okB := inkBox(theirs, page)
					na, nb := darkCount(ours), darkCount(theirs)
					if !okA || !okB || !near(a, b) || float64(na-nb) > tt.tol*float64(nb) || float64(nb-na) > tt.tol*float64(nb) {
						t.Errorf("ink %v (%d px), Ghostscript %v (%d px)", a, na, b, nb)
					}
					return
				case "":
				case "full":
					refOurs, refGs = renderPDF(t, f.mk(fill)), gsPNG(t, gs, f.mk(fill))
				default:
					refOurs, refGs = renderPDF(t, f.mk(tt.ref)), gsPNG(t, gs, f.mk(tt.ref))
				}
				check := func(who string, got, want image.Image, tol float64) {
					t.Helper()
					a, okA := inkBox(got, page)
					if want == nil {
						if okA {
							t.Errorf("%s: ink %v, want none", who, a)
						}
						return
					}
					b, okB := inkBox(want, page)
					if okA != okB || okA && !near(a, b) {
						t.Errorf("%s: ink box %v (%v), reference %v (%v)", who, a, okA, b, okB)
					}
					na, nb := darkCount(got), darkCount(want)
					if d := float64(na - nb); d > tol*float64(nb) || -d > tol*float64(nb) {
						t.Errorf("%s: ink %d px, reference %d (tolerance %.0f%%)", who, na, nb, 100*tol)
					}
				}
				check("ours", ours, refOurs, 0)
				check("Ghostscript", theirs, refGs, tt.tol+0.03)
				if f.name != "std14" && refOurs != nil && !tt.noCross {
					a, _ := inkBox(ours, page)
					b, _ := inkBox(theirs, page)
					if !near(a, b) {
						t.Errorf("ink box %v, Ghostscript %v", a, b)
					}
				}
			})
		}
	}
}

package dos

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// This file holds the DoS gates of docs/design/clipping-paths.md §10.2:
// clipping paths (W, W*) build masks, and a hostile stream of clips must stay
// bounded in time (raster work) and memory (the live clip-mask cap). Every
// bound is relative to a reference render measured in the same test on the
// same machine: CI runs on small shared runners where absolute bounds flake.

// letterClipOpts is the render the clip gates use: Letter at 150 DPI, the
// default budget.
var letterClipOpts = core.RenderOptions{DPI: 150}

// floodReference is the CPU time of TestDoS_DefaultBudgetBoundsPage's flood
// (100k full-page fills, stopped by the default budget), the unit the clip
// gates' time limits are expressed in.
func floodReference(t *testing.T) time.Duration {
	t.Helper()
	err, flood, _ := timedRender(floodDoc(t, 1, "[0 0 612 792]", 100_000), letterClipOpts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("flood reference: err = %v, want ErrRenderBudgetExceeded", err)
	}
	return flood
}

// TestDoS_ClipMaskFloodIsBounded: q <triangle> W n Q builds and frees one
// full-page mask per repetition. Each build is charged as raster work, so the
// default budget stops the page after ≈ 2,000 of them (design §6), about as
// fast as the plain fill flood; one mask is alive at a time. 20,000
// repetitions are ten times what the budget admits (the design's 10^6 only
// adds content-scan time, which the budget does not meter: render-budget.md
// §9 R2).
func TestDoS_ClipMaskFloodIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	flood := floodReference(t)
	const n = 20_000
	clip := contentDoc(t, "[0 0 612 792]", strings.Repeat("q 3 1 m 610 40 l 590 791 l 1 700 l h W n Q\n", n))
	err, elapsed, alloc := timedRender(clip, letterClipOpts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) || !strings.Contains(err.Error(), "paint work") {
		t.Fatalf("err = %v, want a paint work trip", err)
	}
	// The same operators without W: no mask, the reference for the scan,
	// the path and the canvas.
	plain := contentDoc(t, "[0 0 612 792]", strings.Repeat("q 3 1 m 610 40 l 590 791 l 1 700 l h n Q\n", n))
	errRef, _, refAlloc := timedRender(plain, letterClipOpts)
	if errRef != nil {
		t.Fatalf("reference: %v", errRef)
	}
	if limit := 2*flood + time.Second; elapsed > limit {
		t.Errorf("render took %v CPU, want ≤ %v (2× the %v flood reference + 1 s)", elapsed, limit, flood)
	}
	// ≈ 2,000 short-lived masks of ≈ 264 KB each, one alive at a time.
	if extra := int64(alloc) - int64(refAlloc); extra > 1<<30 {
		t.Errorf("render allocated %d MB beyond the reference's %d MB, want ≤ 1024 MB", extra>>20, refAlloc>>20)
	}
	t.Logf("%v CPU (flood %v), %d MB allocated (reference %d MB)", elapsed, flood, alloc>>20, refAlloc>>20)
}

// TestDoS_NestedClipMasksAreCapped: 1024 nested non-rectangular clips on a
// page at the canvas cap (8192×8192 px) keep every level's mask alive; each is
// ≈ 8 MiB, so 1024 would hold 8 GiB (fatal under js/wasm). The live clip-mask
// cap (128 MiB) stops the render after ≈ 16 levels with a "clip masks" trip.
func TestDoS_NestedClipMasksAreCapped(t *testing.T) {
	skipUnlessTimed(t)
	flood := floodReference(t)
	var nested, plain strings.Builder
	for i := 0; i < 1024; i++ {
		fmt.Fprintf(&nested, "q %d 0 m 8192 %d l 0 8192 l h W n\n", i%7, i%5)
		fmt.Fprintf(&plain, "q %d 0 m 8192 %d l 0 8192 l h n\n", i%7, i%5)
	}
	opts := core.RenderOptions{DPI: 72}
	err, elapsed, alloc := timedRender(contentDoc(t, "[0 0 8192 8192]", nested.String()), opts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) || !strings.Contains(err.Error(), "clip masks") {
		t.Fatalf("err = %v, want a clip masks trip", err)
	}
	errRef, refTime, refAlloc := timedRender(contentDoc(t, "[0 0 8192 8192]", plain.String()), opts)
	if errRef != nil {
		t.Fatalf("reference: %v", errRef)
	}
	if limit := 2*(flood+refTime) + time.Second; elapsed > limit {
		t.Errorf("render took %v CPU, want ≤ %v (2× the flood %v and canvas %v references + 1 s)", elapsed, limit, flood, refTime)
	}
	// At most the 128 MiB of live masks plus the one that trips (never
	// allocated: the counter is checked before make) and the scan's scratch.
	if extra := int64(alloc) - int64(refAlloc); extra > 160<<20 {
		t.Errorf("render allocated %d MB beyond the reference's %d MB, want ≤ 160 MB", extra>>20, refAlloc>>20)
	}
	t.Logf("%v CPU, %d MB beyond the reference (%v, %d MB)", elapsed, (int64(alloc)-int64(refAlloc))>>20, refTime, refAlloc>>20)
}

// TestDoS_RectClipFloodIsCheap: re W n repeated is a rectangle clip, which
// allocates nothing and costs no raster work (design §3.4, §6): it renders
// without a trip, in about the time and allocation of the same operators
// without W. (core/render's TestClip_RectClipAllocatesNothing checks the
// per-operator allocations exactly.)
func TestDoS_RectClipFloodIsCheap(t *testing.T) {
	skipUnlessTimed(t)
	const n = 200_000
	clip := contentDoc(t, "[0 0 612 792]", strings.Repeat("0 0 612 792 re W n\n", n)+"0 0 612 792 re f")
	// The reference has the same operator count (a w with no operand is
	// ignored), so the content scan, which allocates per operator, matches.
	plain := contentDoc(t, "[0 0 612 792]", strings.Repeat("0 0 612 792 re w n\n", n)+"0 0 612 792 re f")
	errRef, refTime, refAlloc := timedRender(plain, letterClipOpts)
	err, elapsed, alloc := timedRender(clip, letterClipOpts)
	if err != nil || errRef != nil {
		t.Fatalf("err = %v, reference %v; want both to render", err, errRef)
	}
	if limit := 2*refTime + time.Second/2; elapsed > limit {
		t.Errorf("render took %v CPU, want ≤ %v (2× the %v reference + 0.5 s)", elapsed, limit, refTime)
	}
	if limit := refAlloc + refAlloc/10 + 1<<20; alloc > limit {
		t.Errorf("render allocated %d MB, want ≤ %d MB (the reference's %d MB + 10%%)", alloc>>20, limit>>20, refAlloc>>20)
	}
	t.Logf("%v CPU, %d MB (reference %v, %d MB)", elapsed, alloc>>20, refTime, refAlloc>>20)
}

// TestDoS_ClipAtPathCapIsBoundedLikeFill: a clip path at the maxPathPoints
// cap (262,140 c, every edge spanning the page) is scanned exactly like the
// same path filled, so it costs about the same time and allocation (the fill
// scratch plus one mask) and trips the same budget.
func TestDoS_ClipAtPathCapIsBoundedLikeFill(t *testing.T) {
	skipUnlessTimed(t)
	path := zigzagContent(262_140, 612, 792, true)
	path = strings.TrimSuffix(path, "f\n")
	errFill, fillTime, fillAlloc := timedRender(contentDoc(t, "[0 0 612 792]", path+"f\n"), letterClipOpts)
	err, elapsed, alloc := timedRender(contentDoc(t, "[0 0 612 792]", path+"W n\n0 0 612 792 re f\n"), letterClipOpts)
	if !errors.Is(errFill, core.ErrRenderBudgetExceeded) || !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("err = %v, fill %v; want both to trip the budget", err, errFill)
	}
	if limit := 2*fillTime + time.Second; elapsed > limit {
		t.Errorf("clip took %v CPU, want ≤ %v (2× the %v fill + 1 s)", elapsed, limit, fillTime)
	}
	if limit := fillAlloc + fillAlloc/4 + 8<<20; alloc > limit {
		t.Errorf("clip allocated %d MB, want ≤ %d MB (the fill's %d MB + 25%% + one mask)", alloc>>20, limit>>20, fillAlloc>>20)
	}
	t.Logf("clip %v CPU, %d MB; fill %v CPU, %d MB", elapsed, alloc>>20, fillTime, fillAlloc>>20)
}

// fontDoc is contentDoc for one Letter page whose /F1 is Helvetica (drawn
// with its bundled substitute face).
func fontDoc(t *testing.T, content string) *core.Document {
	t.Helper()
	doc, err := core.OpenBytes(classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// TestDoS_TextClipHugeStringIsBounded is §10.2's Phase B gate: one Tj of a
// huge string in 7 Tr (text clipping, design §5). Every glyph is one
// interpreter step and, unlike mode 3, builds its outline and ORs its fill
// spans into the text object's clip mask, all charged as raster work; the
// mask is built once. Each case stops about as fast as the same string
// shown in mode 0, with one mask's allocation beyond it: large and tiny
// glyphs at the paint budget, spaces (empty outlines, loaded but charged
// nothing beyond their step, as in mode 0) at the operator budget.
func TestDoS_TextClipHugeStringIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	for _, tt := range []struct {
		name, size, char, ref string
		n                     int
		resource              string
	}{
		{"large glyphs", "300", "W", "0", 1 << 20, "paint work"},
		{"tiny glyphs", "0.5", "W", "0", 9 << 20, "paint work"},
		{"spaces", "12", " ", "0", 9 << 20, "operators"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := " Tr BT /F1 " + tt.size + " Tf 10 300 Td (" + strings.Repeat(tt.char, tt.n) + ") Tj ET 0 0 612 792 re f"
			errRef, refTime, refAlloc := timedRender(fontDoc(t, tt.ref+text), letterClipOpts)
			err, elapsed, alloc := timedRender(fontDoc(t, "7"+text), letterClipOpts)
			for _, e := range []error{err, errRef} {
				if !errors.Is(e, core.ErrRenderBudgetExceeded) || !strings.Contains(e.Error(), tt.resource) {
					t.Fatalf("err = %v (mode %s: %v), want a %s trip", err, tt.ref, errRef, tt.resource)
				}
			}
			if limit := 2*refTime + time.Second; elapsed > limit {
				t.Errorf("render took %v CPU, want ≤ %v (2× the %v mode %s reference + 1 s)", elapsed, limit, refTime, tt.ref)
			}
			if limit := refAlloc + refAlloc/4 + 8<<20; alloc > limit {
				t.Errorf("render allocated %d MB, want ≤ %d MB (the reference's %d MB + 25%% + one mask)", alloc>>20, limit>>20, refAlloc>>20)
			}
			t.Logf("%v CPU, %d MB (mode %s reference %v, %d MB)", elapsed, alloc>>20, tt.ref, refTime, refAlloc>>20)
		})
	}
}

// TestDoS_TextClipObjectFloodIsBounded: q BT 7 Tr (W) Tj ET Q repeated
// builds one full-page accumulation mask per text object (each over the
// clip's box at its first glyph), frees it at ET for a copy over the glyph's
// box and frees that at Q. Each build is charged as raster work (newMask:
// its rows, the zeroing and extent pass; the copy's rows and one pass), so
// the default budget stops the page about as fast as the plain fill flood,
// with one accumulation alive at a time. The reference for allocation is
// the W clip flood of TestDoS_ClipMaskFloodIsBounded: a text mask is
// charged less per build (no path scan over every row), so the budget
// admits ≈ 1.5× as many.
func TestDoS_TextClipObjectFloodIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	flood := floodReference(t)
	const n = 50_000
	err, elapsed, alloc := timedRender(fontDoc(t, strings.Repeat("q BT 7 Tr /F1 12 Tf 300 400 Td (W) Tj ET Q\n", n)), letterClipOpts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) || !strings.Contains(err.Error(), "paint work") {
		t.Fatalf("err = %v, want a paint work trip", err)
	}
	errRef, refTime, refAlloc := timedRender(fontDoc(t, strings.Repeat("q BT 3 Tr /F1 12 Tf 300 400 Td (W) Tj ET Q\n", n)), letterClipOpts)
	if errRef != nil {
		t.Fatalf("reference: %v", errRef)
	}
	errW, wTime, wAlloc := timedRender(contentDoc(t, "[0 0 612 792]", strings.Repeat("q 3 1 m 610 40 l 590 791 l 1 700 l h W n Q\n", n)), letterClipOpts)
	if !errors.Is(errW, core.ErrRenderBudgetExceeded) {
		t.Fatalf("W flood: %v", errW)
	}
	if limit := 2*flood + time.Second; elapsed > limit {
		t.Errorf("render took %v CPU, want ≤ %v (2× the %v flood reference + 1 s)", elapsed, limit, flood)
	}
	if extra := int64(alloc) - int64(refAlloc); extra > 4*int64(wAlloc) {
		t.Errorf("render allocated %d MB beyond the reference's %d MB, want ≤ %d MB (4× the W clip flood)", extra>>20, refAlloc>>20, 4*wAlloc>>20)
	}
	t.Logf("%v CPU (flood %v), %d MB allocated (mode 3 reference %v, %d MB; W flood %v, %d MB)", elapsed, flood, alloc>>20, refTime, refAlloc>>20, wTime, wAlloc>>20)
}

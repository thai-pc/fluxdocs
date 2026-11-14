package dos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// Gates for the cost of a page's canvas (render-budget.md §3, canvas caps): an
// empty page pays for clearing its canvas and encoding it, work the paint
// budget does not meter. A fuzzed page with no content and a 1 × 10^7 pt
// MediaBox (a 1 × 5,000,000 px canvas at 36 DPI) ran past the fuzz harness's
// deadline under load: under the area cap alone a canvas could be a 1-pixel
// sliver, whose PNG encode costs ≈ 5× a square canvas of the same area, and
// neither the clear nor the encode polled the deadline.

// canvasPDF returns a one-page document with the given MediaBox, page
// resources and content (empty: no /Contents).
func canvasPDF(mediaBox, resources, content string) []byte {
	page := "<< /Type /Page /Parent 2 0 R /MediaBox " + mediaBox + " /Resources " + resources
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"}
	if content == "" {
		objs = append(objs, page+" >>")
	} else {
		objs = append(objs, page+" /Contents 4 0 R >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := make([]int, len(objs))
	for i, o := range objs {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// normalPageContent is a typical text-and-graphics Letter page: 50 lines of
// text, a table grid of strokes and a few filled boxes.
func normalPageContent() string {
	var sb strings.Builder
	sb.WriteString("0.9 0.9 0.95 rg 50 650 512 100 re f 0 0 0 rg\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, "50 %d m 562 %d l S\n", 100+25*i, 100+25*i)
	}
	sb.WriteString("BT /F1 10 Tf 12 TL 60 740 Td\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "(Line %d of an ordinary page: invoices, contracts, the usual text.) '\n", i)
	}
	sb.WriteString("ET\n")
	return sb.String()
}

// openPDF opens data or fails the test.
func openPDF(t *testing.T, data []byte) *core.Document {
	t.Helper()
	doc, err := core.OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// renderCPU renders page 0 of doc reps times and returns the process CPU time
// that took (cpuTime) and the last error.
func renderCPU(doc *core.Document, opts core.RenderOptions, reps int) (time.Duration, error) {
	runtime.GC()
	var err error
	start := cpuTime()
	for i := 0; i < reps; i++ {
		_, err = doc.RenderPage(0, opts)
	}
	return cpuTime() - start, err
}

// TestDoS_EmptyPageCanvasIsBounded: an empty page at the canvas caps costs,
// per output pixel, a small multiple of a normal page's cost per pixel, in
// every shape the caps admit; the slivers past the side cap — the fuzz input
// among them — are refused before any canvas exists. The reference is a
// normal Letter page at 150 DPI, rendered enough times to take about as long
// as the empty page, in runs alternated with it, and each bound is on the
// median ratio of five such runs (process CPU time: a loaded host delays both
// alike). The admitted shapes measure ≈ 0.4× the normal page per pixel;
// before the side cap, slivers measured 1.5× (2 × 32 Mi px), 1.8×
// (1 × 64 Mi px) and 2.2× (1 × 5 M px, the fuzz input's canvas at 72 DPI).
func TestDoS_EmptyPageCanvasIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	normal := openPDF(t, canvasPDF("[0 0 612 792]", "<< /Font << /F1 4 0 R >> >>", normalPageContent()))
	opts := core.RenderOptions{DPI: 150, Format: "png"}
	if _, err := normal.RenderPage(0, opts); err != nil {
		t.Fatalf("normal page: %v", err)
	}
	const normalPx = 1275 * 1650
	one, _ := renderCPU(normal, opts, 1)
	reps := max(int(time.Second/max(one, time.Millisecond)), 5) // ≥ 1 s of reference work per run

	const bound = 1.0 // per-pixel cost over the normal page's
	for _, tc := range []struct {
		name     string
		mediaBox string // at 72 DPI: points are pixels
		px       float64
	}{
		{"square at the area cap", "[0 0 8192 8192]", 8192 * 8192},
		{"tall at the side cap", "[0 0 1024 65535]", 1024 * 65535},
		{"wide at the side cap", "[0 0 65535 1024]", 65535 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := openPDF(t, canvasPDF(tc.mediaBox, "<< >>", ""))
			empty := core.RenderOptions{DPI: 72, Format: "png"}
			var ratios []float64
			for run := 0; run < 5; run++ {
				ref, err := renderCPU(normal, opts, reps)
				if err != nil {
					t.Fatalf("normal page: %v", err)
				}
				got, err := renderCPU(doc, empty, 1)
				if err != nil {
					t.Fatalf("empty page: %v", err)
				}
				ratios = append(ratios, (float64(got)/tc.px)/(float64(ref)/float64(reps)/normalPx))
			}
			slices.Sort(ratios)
			t.Logf("per-pixel cost over the normal page's: median %.2f (runs %.2f), bound %.1f", ratios[2], ratios, bound)
			if ratios[2] > bound {
				t.Errorf("an empty %s page costs %.2f× a normal page per pixel, want ≤ %.1f×", tc.mediaBox, ratios[2], bound)
			}
		})
	}

	// Past the side cap: refused before any canvas is allocated, so at a
	// small fraction of one normal page and with no allocation to speak of.
	for _, tc := range []struct {
		name string
		data []byte
		dpi  int
	}{
		{"the fuzz input: 1 × 5,000,000 px at 36 DPI", fuzzTallPage, 36},
		{"1 × 64 Mi px", canvasPDF("[0 0 1 67108864]", "<< >>", ""), 72},
		{"64 Mi × 1 px", canvasPDF("[0 0 67108864 1]", "<< >>", ""), 72},
		{"one pixel past the side cap", canvasPDF("[0 0 1 65536]", "<< >>", ""), 72},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			doc := openPDF(t, tc.data)
			err, took, alloc := timedRender(doc, core.RenderOptions{DPI: tc.dpi, Format: "png"})
			if !errors.Is(err, core.ErrRenderBudgetExceeded) {
				t.Fatalf("err = %v, want ErrRenderBudgetExceeded (canvas too large)", err)
			}
			if limit := one / 4; took > limit || alloc > 1<<20 {
				t.Errorf("refusal took %v CPU and %d bytes, want ≤ %v (a quarter of a normal page) and ≤ 1 MiB", took, alloc, limit)
			}
		})
	}
}

// fuzzTallPage is the fuzz input (FuzzOpenBytes): a page with no content
// whose inherited MediaBox, [0 0 1 10000000 0] as lexed, is 1 × 10^7 pt.
var fuzzTallPage = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 1 10000000\r00000000000] >>\nendobj\n3 0 obj\n<< /Type /Page /Parent 2 0 R >>\nendobj\nxref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000153 00000 n \ntrailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n200\n%%EOF\n")

// TestDoS_CanvasHonoursDeadline: the canvas clear and the encode poll the
// render's deadline, so an empty page at the caps whose deadline passes
// half-way through its render returns soon after. The reference is the same
// render without a deadline (wall time, median of three); the deadline is
// half of it, after the clear (≈ a quarter), so only the encode's polls can
// stop it. Before they did, the render ran to its end (≈ 1.0× the
// reference); with them it returns at ≈ 0.5–0.6×.
func TestDoS_CanvasHonoursDeadline(t *testing.T) {
	skipUnlessTimed(t)
	const bound = 0.8 // of the reference
	for _, tc := range []struct{ name, mediaBox string }{
		{"square at the area cap", "[0 0 8192 8192]"},
		{"wide at the side cap", "[0 0 65535 1024]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := openPDF(t, canvasPDF(tc.mediaBox, "<< >>", ""))
			opts := core.RenderOptions{DPI: 72, Format: "png"}
			wall := func(ctx context.Context) (time.Duration, error) {
				runtime.GC()
				start := time.Now()
				_, err := doc.RenderPageContext(ctx, 0, opts)
				return time.Since(start), err
			}
			var refs, got []time.Duration
			for run := 0; run < 3; run++ {
				ref, err := wall(context.Background())
				if err != nil {
					t.Fatalf("render: %v", err)
				}
				refs = append(refs, ref)
			}
			slices.Sort(refs)
			ref := refs[1]
			for run := 0; run < 3; run++ {
				ctx, cancel := context.WithTimeout(context.Background(), ref/2)
				took, err := wall(ctx)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("render with a deadline of %v: err = %v after %v, want DeadlineExceeded", ref/2, err, took)
				}
				got = append(got, took)
			}
			slices.Sort(got)
			ratio := float64(got[1]) / float64(ref)
			t.Logf("reference %v, deadline %v, returned after %v (runs %v): %.2f× the reference, bound %.1f", ref, ref/2, got[1], got, ratio, bound)
			if ratio > bound {
				t.Errorf("returned after %.2f× the full render, want ≤ %.1f×", ratio, bound)
			}
		})
	}
}

package fuzz

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// Render-budget seeds (SECURITY.md §2; design: docs/design/render-budget.md
// §8). Each paint operator is bounded by the canvas, so the DoS is the number of
// them: these inputs repeat canvas-sized work far past the per-page budget. The
// renderer must stop them with ErrRenderBudgetExceeded, or at the per-input
// deadline exercise sets, never run for minutes.

// renderDeadline bounds each render in exercise; renderSlack absorbs -race and
// a loaded CI host on top of it. A render past both is a hang.
const (
	renderDeadline = 2 * time.Second
	renderSlack    = 10 * time.Second
)

// budgetSeed is a named budget seed and whether, at exercise's DPI, it must trip
// the budget (rather than possibly hitting the deadline first under -race).
type budgetSeed struct {
	name     string
	data     []byte
	mustTrip bool
}

func budgetSeeds() []budgetSeed {
	// Wide form fan-out, three levels deep: the page Do's /F1 64 times, F1 Do's
	// F2 64 times, F2 Do's F3 64 times, F3 fills the page 10 times. The form
	// budget caps executions at 4096; the paint budget stops it first.
	fanOut := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /F1 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(strings.Repeat("/F1 Do\n", 64)),
		formBody("<< /XObject << /F2 6 0 R >> >>", strings.Repeat("/F2 Do\n", 64)),
		formBody("<< /XObject << /F3 7 0 R >> >>", strings.Repeat("/F3 Do\n", 64)),
		formBody("<< >>", strings.Repeat("0 0 100 100 re f\n", 10)),
	})

	// A 1024x1024 image placed at a fraction of a pixel and painted 400 times:
	// the decode (1 M samples per Do) is the cost, not the drawing.
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, 1024*1024))
	zw.Close()
	tinyImage := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(strings.Repeat("q 0.01 0 0 0.01 1 1 cm /Im Do Q\n", 400)),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1024 /Height 1024 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes()),
	})

	// A zigzag path of 20k edges crossing every row, filled three times.
	var zig strings.Builder
	zig.WriteString("0 0 m ")
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&zig, "100 %d.25 l 0 %d.75 l ", i%100, i%100)
	}
	zig.WriteString("f\n")
	manyEdges := contentPDF(strings.Repeat(zig.String(), 3))

	// One small stream listed 10000 times in /Contents (render-budget.md §9 R1).
	refs := strings.TrimSpace(strings.Repeat("4 0 R ", 10000))
	repeatedContents := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Contents [" + refs + "] >>",
		streamBody("0 0 100 100 re f\n"),
	})

	// A 1×1 image whose Flate body inflates to 16 MiB, Do'd 1000 times: one
	// sample each, so only the decoded-bytes budget stops the re-inflation; and
	// the same body as the image's /SMask.
	var bomb bytes.Buffer
	bw, _ := zlib.NewWriterLevel(&bomb, zlib.BestCompression)
	bw.Write(make([]byte, 16<<20))
	bw.Close()
	gray1x1 := "/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8"
	bombImage := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(strings.Repeat("/Im Do\n", 1000)),
		fmt.Sprintf("<< %s /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", gray1x1, bomb.Len(), bomb.Bytes()),
	})
	bombSMask := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(strings.Repeat("/Im Do\n", 1000)),
		"<< " + gray1x1 + " /SMask 6 0 R /Length 1 >>\nstream\n\x80\nendstream",
		fmt.Sprintf("<< %s /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", gray1x1, bomb.Len(), bomb.Bytes()),
	})
	// An unfiltered 1×1 image Do'd 200k times: the per-Do cost (doCost).
	tinyDos := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(strings.Repeat("/Im Do\n", 200_000)),
		"<< " + gray1x1 + " /Length 1 >>\nstream\n\x80\nendstream",
	})

	return []budgetSeed{
		{"image in a broken 64 MiB ObjStm Do'd repeatedly", brokenObjStmPDF(200), false},
		{"1x1 image Flate bomb Do'd repeatedly", bombImage, false},
		{"1x1 SMask Flate bomb Do'd repeatedly", bombSMask, false},
		{"1x1 image Do flood", tinyDos, true},
		{"full-canvas fills", contentPDF(strings.Repeat("0 0 100 100 re f\n", 20000)), true},
		{"full-canvas wide strokes", contentPDF(strings.Repeat("1000 w 0 50 m 100 50 l S\n", 20000)), true},
		{"full-canvas fills and strokes in q/Q", contentPDF(strings.Repeat("q 1 0 0 rg 0 0 100 100 re B Q\n", 20000)), true},
		{"form fan-out", fanOut, true},
		{"huge image tiny-placed and repeated", tinyImage, false},
		{"many-edge path", manyEdges, false},
		{"repeated /Contents stream", repeatedContents, true},
	}
}

// brokenObjStmPDF is a page that Do's /Im0 reps times, where /Im0 (7 0 R) lives
// in ObjStm 5 whose Flate body inflates to 64 MiB of spaces but whose /First
// is past the end, so the decode succeeds and loading the ObjStm then fails
// (≈ 64 KB; xref stream object 6). The resolver must cache that failure rather
// than re-inflate the ObjStm on every Do.
func brokenObjStmPDF(reps int) []byte {
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(bytes.Repeat([]byte(" "), 64<<20))
	zw.Close()
	body := strings.Repeat("/Im0 Do\n", reps)
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im0 7 0 R >> >> /Contents 4 0 R >>",
		streamBody(body),
		fmt.Sprintf("<< /Type /ObjStm /N 1 /First 999999999999 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.String())}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := make([]int, len(objs)+1)
	for i, o := range objs {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	off[5] = b.Len()
	var xs bytes.Buffer
	ent := func(typ byte, f2 uint32, f3 uint16) {
		xs.WriteByte(typ)
		binary.Write(&xs, binary.BigEndian, f2)
		binary.Write(&xs, binary.BigEndian, f3)
	}
	ent(0, 0, 65535)
	for i := 0; i < 6; i++ {
		ent(1, uint32(off[i]), 0)
	}
	ent(2, 5, 0) // object 7: index 0 of ObjStm 5
	fmt.Fprintf(&b, "6 0 obj\n<< /Type /XRef /Size 8 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n%s\nendstream\nendobj\n", xs.Len(), xs.String())
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[5])
	return b.Bytes()
}

// streamBody is an uncompressed stream object body.
func streamBody(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

// formBody is a Form XObject over the 100x100 page with the given /Resources.
func formBody(resources, content string) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources %s /Length %d >>\nstream\n%s\nendstream",
		resources, len(content), content)
}

// budgetSeedBytes returns the budget seeds' inputs for the fuzz corpus.
func budgetSeedBytes() [][]byte {
	var out [][]byte
	for _, s := range budgetSeeds() {
		out = append(out, s.data)
	}
	return out
}

// TestBudgetSeedsBounded pins the seeds' contract beyond "no panic". With the
// default budget under renderDeadline, each returns within the deadline plus
// slack, with no image unless it succeeded and no recovered panic. Which of the
// budget or the deadline stops it first depends on host speed (-race is ~10×
// slower), so the deterministic check is separate: with a tight explicit
// budget and no deadline, the seeds marked mustTrip fail with
// ErrRenderBudgetExceeded.
func TestBudgetSeedsBounded(t *testing.T) {
	for _, s := range budgetSeeds() {
		t.Run(s.name, func(t *testing.T) {
			doc, err := core.OpenBytes(s.data)
			if err != nil {
				t.Fatalf("OpenBytes: %v (seed must reach the renderer)", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), renderDeadline)
			defer cancel()
			start := time.Now()
			img, err := doc.RenderPageContext(ctx, 0, core.RenderOptions{DPI: 36, Format: "png"})
			elapsed := time.Since(start)
			failOnInternal(t, err)
			if elapsed > renderDeadline+renderSlack {
				t.Errorf("render took %v, want ≤ %v", elapsed, renderDeadline+renderSlack)
			}
			if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want nil, ErrRenderBudgetExceeded or DeadlineExceeded", err)
			}
			if err != nil && img != nil {
				t.Errorf("got %d bytes with an error, want nil", len(img))
			}

			if !s.mustTrip {
				return
			}
			img, err = doc.RenderPage(0, core.RenderOptions{DPI: 36, Format: "png", MaxPaintPixels: 1 << 20})
			failOnInternal(t, err)
			if !errors.Is(err, core.ErrRenderBudgetExceeded) || img != nil {
				t.Errorf("tight budget: err = %v (%d bytes), want ErrRenderBudgetExceeded and no image", err, len(img))
			}
		})
	}
}

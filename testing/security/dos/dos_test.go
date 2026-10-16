// Package dos holds the slow hostile-document gates for the per-page render
// budget (SECURITY.md §2; design: docs/design/render-budget.md §8). Each paint
// operator is bounded by the canvas; these documents repeat canvas-sized work
// far past the budget, and the gates check that the default budget turns that
// into a bounded, prompt ErrRenderBudgetExceeded — per page, and per batch,
// where the first failure cancels the rest. They take seconds, so -short skips
// them, and they measure time (process CPU time when comparing renders, wall
// time for deadlines), so the race detector skips them too (CI runs them in
// their own step without -race).
package dos

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// skipUnlessTimed skips a wall-clock gate under -short or the race detector.
func skipUnlessTimed(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow hostile-document gate")
	}
	if raceEnabled {
		t.Skip("wall-clock gate: run without -race")
	}
}

// floodDoc builds a document of pages pages, each mediaBox-sized and filling the
// whole page fills times.
func floodDoc(t *testing.T, pages int, mediaBox string, fills int) *core.Document {
	t.Helper()
	content := strings.Repeat("0 0 100000 100000 re f\n", fills)
	contents := make([]string, pages)
	for i := range contents {
		contents[i] = content
	}
	return contentDoc(t, mediaBox, contents...)
}

// contentDoc builds a document with one mediaBox-sized page per content stream.
func contentDoc(t *testing.T, mediaBox string, contents ...string) *core.Document {
	t.Helper()
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	kids := ""
	for _, content := range contents {
		page := len(objs) + 1
		kids += fmt.Sprintf("%d 0 R ", page)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox %s /Contents %d 0 R >>", mediaBox, page+1),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(contents))

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
	doc, err := core.OpenBytes(b.Bytes())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// TestDoS_DefaultBudgetBoundsPage: 100k full-page fills on a Letter page would
// be ≈ 2·10^11 pixel visits (minutes). At 150 DPI the default budget (≈ 67 M
// visits, ≈ 0.3–0.8 s) stops it.
func TestDoS_DefaultBudgetBoundsPage(t *testing.T) {
	skipUnlessTimed(t)
	doc := floodDoc(t, 1, "[0 0 612 792]", 100_000)
	start := time.Now()
	img, err := doc.RenderPage(0, core.RenderOptions{DPI: 150})
	elapsed := time.Since(start)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) || img != nil {
		t.Fatalf("err = %v (%d bytes), want ErrRenderBudgetExceeded and no image", err, len(img))
	}
	if elapsed > 20*time.Second { // ≈ 1 s expected; slack for -race and loaded CI
		t.Errorf("render took %v, want the budget to stop it within seconds", elapsed)
	}
	t.Logf("default budget at 150 DPI stopped the page after %v", elapsed)
}

// TestDoS_HostileBatchIsCanceled: a 64-page document where every page exceeds
// the budget costs about one wave of NumCPU pages, not 64 pages, because the
// first failure cancels the batch. It compares against a batch of exactly one
// wave, so parallel contention on the host affects both sides alike.
func TestDoS_HostileBatchIsCanceled(t *testing.T) {
	skipUnlessTimed(t)
	const pages = 64
	wave := runtime.NumCPU()
	if wave*2 > pages {
		t.Skipf("NumCPU %d: a %d-page batch is under two waves, nothing to compare", wave, pages)
	}
	opts := core.RenderOptions{DPI: 72}
	batch := func(n int) time.Duration {
		doc := floodDoc(t, n, "[0 0 612 792]", 100_000)
		var fired int
		doc.OnPageRendered(func(int, []byte) { fired++ })
		start := time.Now()
		imgs, err := doc.RenderAllPagesContext(context.Background(), opts)
		elapsed := time.Since(start)
		if !errors.Is(err, core.ErrRenderBudgetExceeded) || errors.Is(err, context.Canceled) || imgs != nil {
			t.Fatalf("%d pages: err = %v (%d images), want the budget error and no images", n, err, len(imgs))
		}
		if fired != 0 {
			t.Errorf("%d pages: OnPageRendered fired %d times for failed pages", n, fired)
		}
		return elapsed
	}
	oneWave := batch(wave)
	all := batch(pages)
	// Uncanceled, 64 pages cost pages/wave waves; canceled, about one.
	waves := float64(pages) / float64(wave)
	if limit := time.Duration(float64(oneWave) * max(waves/2, 1.5)); all > limit+2*time.Second {
		t.Errorf("%d-page batch took %v vs %v for one wave of %d (%.1f waves uncanceled), want early cancellation",
			pages, all, oneWave, wave, waves)
	}
	t.Logf("one wave (%d pages) %v, %d-page hostile batch %v", wave, oneWave, pages, all)
}

// zigzagContent is one path of n vertices alternating between the bottom and
// the top of a w×h pt page at random x, filled: every edge spans every row, in
// random x order, so each row sorts n crossings. With curves, each vertex is
// reached by a `c` whose 16 flattened points climb monotonically, so every row
// still crosses one edge per curve.
func zigzagContent(n int, w, h float64, curves bool) string {
	rng := rand.New(rand.NewPCG(1, 2))
	var sb strings.Builder
	sb.WriteString("0 0 m\n")
	for i := 0; i < n; i++ {
		x := rng.Float64() * w
		y, prev := 0.0, h
		if i%2 == 0 {
			y, prev = h, 0
		}
		if curves {
			fmt.Fprintf(&sb, "%.2f %.0f %.2f %.0f %.2f %.0f c\n", x, prev, x, y, x, y)
		} else {
			fmt.Fprintf(&sb, "%.2f %.0f l\n", x, y)
		}
	}
	sb.WriteString("f\n")
	return sb.String()
}

// timedRender renders page 0 and returns the error, the process CPU time it
// took (cpuTime: a loaded host delays it but does not inflate it, so ratios
// between renders hold), and the bytes allocated during the render.
func timedRender(doc *core.Document, opts core.RenderOptions) (error, time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := cpuTime()
	_, err := doc.RenderPage(0, opts)
	elapsed := cpuTime() - start
	runtime.ReadMemStats(&after)
	return err, elapsed, after.TotalAlloc - before.TotalAlloc
}

// TestDoS_ZigzagFillIsBounded: a path whose edges all span the page costs
// k log k per row to sort its k crossings. The budget must charge that, so a
// zigzag stops about as fast as the plain fill flood of
// TestDoS_DefaultBudgetBoundsPage — before the charge counted k per row, the
// 65,536-vertex zigzag ran ≈ 9 s against ≈ 0.3 s for the flood. The curves
// variant is a path at the maxPathPoints cap (262,140 `c` = 4 Mi points); its
// allocation is also bounded: ≈ 44 bytes per point of fill scratch on top of
// the path and the parsed content, with no doubling garbage (it allocated
// > 1 GB before).
func TestDoS_ZigzagFillIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	const letter = "[0 0 612 792]"
	opts := core.RenderOptions{DPI: 150}

	err, flood, _ := timedRender(floodDoc(t, 1, letter, 100_000), opts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("flood: err = %v, want ErrRenderBudgetExceeded", err)
	}
	limit := 2*flood + time.Second
	t.Logf("flood reference %v CPU; limit %v", flood, limit)

	tests := []struct {
		name     string
		vertices int
		curves   bool
		maxAlloc uint64 // bytes allocated by the render (content scan, path, fill scratch)
	}{
		{"lines 65,536 vertices", 65_536, false, 64 << 20},
		{"lines 262,144 vertices", 262_144, false, 192 << 20},
		{"curves 262,140 c (4 Mi points)", 262_140, true, 768 << 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := contentDoc(t, letter, zigzagContent(tc.vertices, 612, 792, tc.curves))
			err, elapsed, alloc := timedRender(doc, opts)
			if !errors.Is(err, core.ErrRenderBudgetExceeded) {
				t.Fatalf("err = %v, want ErrRenderBudgetExceeded", err)
			}
			if elapsed > limit {
				t.Errorf("render took %v CPU, want ≤ %v (2× the flood gate + 1 s)", elapsed, limit)
			}
			if alloc > tc.maxAlloc {
				t.Errorf("render allocated %d MB, want ≤ %d MB", alloc>>20, tc.maxAlloc>>20)
			}
			t.Logf("%v CPU, %d MB allocated", elapsed, alloc>>20)
		})
	}
}

// hairlineContent is a CAD-like drawing: n short random hairline segments
// (≤ 15 pt each way, line width 0.24 pt) on a Letter page, stroked as one path.
func hairlineContent(n int) string {
	rng := rand.New(rand.NewPCG(12345, 1))
	var sb strings.Builder
	sb.WriteString("0.24 w\n")
	for i := 0; i < n; i++ {
		x, y := 20+rng.Float64()*572, 20+rng.Float64()*752
		fmt.Fprintf(&sb, "%.2f %.2f m %.2f %.2f l\n", x, y, x+(rng.Float64()-0.5)*30, y+(rng.Float64()-0.5)*30)
	}
	sb.WriteString("S\n")
	return sb.String()
}

// TestDoS_DenseHairlinePageRenders is the false-positive side of the budget: a
// dense vector page (100k short hairline segments, a real CAD/GIS density) must
// render under the default budget at 150 DPI. Before line's row search resumed
// from the previous row it cost lineRowCost = 48 per row and tripped. The 1M
// segment page is logged, not asserted: it costs ≈ 300 M units against the 67 M
// default, so such pages need a higher RenderOptions.MaxPaintPixels.
func TestDoS_DenseHairlinePageRenders(t *testing.T) {
	skipUnlessTimed(t)
	opts := core.RenderOptions{DPI: 150}
	doc := contentDoc(t, "[0 0 612 792]", hairlineContent(100_000))
	err, elapsed, _ := timedRender(doc, opts)
	if err != nil {
		t.Fatalf("100k hairlines: %v, want a rendered page", err)
	}
	t.Logf("100k hairlines rendered in %v", elapsed)

	doc = contentDoc(t, "[0 0 612 792]", hairlineContent(1_000_000))
	err, elapsed, _ = timedRender(doc, opts)
	if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("1M hairlines: %v, want success or ErrRenderBudgetExceeded", err)
	}
	t.Logf("1M hairlines: err %v after %v", err, elapsed)
}

// imageDoc is a Letter page running content, whose /Im is a 1×1 DeviceGray
// image. With bomb > 0 a Flate body inflating to bomb bytes goes in the role
// named by kind: "image" (the samples), "mask" (an /ImageMask's bits) or
// "smask" (the image's /SMask).
func imageDoc(t *testing.T, kind string, bomb int, content string) *core.Document {
	t.Helper()
	gray := "/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8"
	plain := "<< " + gray + " /Length 1 >>\nstream\n\x80\nendstream"
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(make([]byte, bomb))
	zw.Close()
	flate := func(dict string) string {
		return fmt.Sprintf("<< %s /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", dict, z.Len(), z.Bytes())
	}
	img, smask := plain, plain
	switch kind {
	case "image":
		img = flate(gray)
	case "mask":
		img = flate("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ImageMask true")
	case "smask":
		img = "<< " + gray + " /SMask 6 0 R /Length 1 >>\nstream\n\x80\nendstream"
		smask = flate(gray)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		img,
		smask,
	}
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
	doc, err := core.OpenBytes(b.Bytes())
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// TestDoS_ImageDecodeIsBounded: an image costs its decode on every Do (there is
// no decode cache). A 1×1 image whose Flate body inflates to 64 MiB (a 64 KB
// object) was charged one sample per Do, so 1000 Do inflated 64 GiB; its mask
// and SMask variants likewise. The decoded-bytes budget (512 MiB) must stop
// each within the flood bound of TestDoS_ZigzagFillIsBounded. The Do flood
// (an unfiltered 1×1 image, 1 M Do) checks the per-Do charge, doCost.
func TestDoS_ImageDecodeIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	opts := core.RenderOptions{DPI: 150}
	err, flood, _ := timedRender(floodDoc(t, 1, "[0 0 612 792]", 100_000), opts)
	if !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("flood: err = %v, want ErrRenderBudgetExceeded", err)
	}
	limit := 2*flood + time.Second
	t.Logf("flood reference %v CPU; limit %v", flood, limit)

	bombDos := strings.Repeat("/Im Do\n", 1000)
	tests := []struct {
		name     string
		doc      func(*testing.T) *core.Document
		resource string
	}{
		{"image 64 MiB x 1000 Do", func(t *testing.T) *core.Document { return imageDoc(t, "image", 64<<20, bombDos) }, "decoded bytes"},
		{"mask 64 MiB x 1000 Do", func(t *testing.T) *core.Document { return imageDoc(t, "mask", 64<<20, bombDos) }, "decoded bytes"},
		{"SMask 64 MiB x 1000 Do", func(t *testing.T) *core.Document { return imageDoc(t, "smask", 64<<20, bombDos) }, "decoded bytes"},
		{"1x1 image x 1M Do", func(t *testing.T) *core.Document {
			return imageDoc(t, "", 0, strings.Repeat("/Im Do\n", 1<<20))
		}, "paint work"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err, elapsed, _ := timedRender(tc.doc(t), opts)
			if !errors.Is(err, core.ErrRenderBudgetExceeded) || !strings.Contains(err.Error(), tc.resource) {
				t.Fatalf("err = %v, want ErrRenderBudgetExceeded naming %q", err, tc.resource)
			}
			if elapsed > limit {
				t.Errorf("render took %v CPU, want ≤ %v (2× the flood gate + 1 s)", elapsed, limit)
			}
			t.Logf("%v CPU", elapsed)
		})
	}
}

// TestDoS_SaveStackIsBounded: 8 Mi q, within the default MaxOperators, saved a
// graphics state each (≈ 3 GB natively, fatal under js/wasm's 4 GiB). Past
// maxSaveDepth they are counted, not saved, so the page renders and allocates
// what scanning its content costs (ScanContent materializes every operator up
// front, render-budget.md §9 R2, not yet bounded) plus a bounded remainder.
func TestDoS_SaveStackIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	content := strings.Repeat("q\n", 8<<20)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	parse.ScanContent([]byte(content))
	runtime.ReadMemStats(&after)
	scan := after.TotalAlloc - before.TotalAlloc

	doc := contentDoc(t, "[0 0 612 792]", content)
	err, elapsed, alloc := timedRender(doc, core.RenderOptions{DPI: 72})
	if err != nil {
		t.Fatalf("err = %v, want the page to render", err)
	}
	if alloc > scan+128<<20 {
		t.Errorf("render allocated %d MB, want ≤ the content scan's %d MB + 128 MB", alloc>>20, scan>>20)
	}
	t.Logf("%v CPU, %d MB allocated (content scan alone: %d MB)", elapsed, alloc>>20, scan>>20)
}

// brokenObjStmPDF is a page that Do's /Im0 reps times, where /Im0 (7 0 R) lives
// in ObjStm 5 whose Flate body inflates to 64 MiB of spaces but whose /First
// is past the end: the decode succeeds, then loading the ObjStm fails. The
// file is ≈ 64 KB; the xref is a stream (object 6).
func brokenObjStmPDF(reps int) []byte {
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(bytes.Repeat([]byte(" "), 64<<20))
	zw.Close()
	body := strings.Repeat("/Im0 Do\n", reps)
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im0 7 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
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

// TestDoS_BrokenObjStmIsDecodedOnce: an ObjStm that inflates and then fails
// to load was not cached, so every Do of an object inside it re-inflated
// 64 MiB in the parser, outside the render budget and between its context
// polls (200 Do: ≈ 12 s, err nil; a 1 s deadline returned after ≈ 11 s). The
// resolver now caches the failure: the ObjStm is inflated once.
func TestDoS_BrokenObjStmIsDecodedOnce(t *testing.T) {
	skipUnlessTimed(t)
	err, flood, _ := timedRender(floodDoc(t, 1, "[0 0 612 792]", 100_000), core.RenderOptions{DPI: 150})
	if !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Fatalf("flood: err = %v, want ErrRenderBudgetExceeded", err)
	}
	limit := 2*flood + time.Second

	open := func() *core.Document {
		doc, err := core.OpenBytes(brokenObjStmPDF(200))
		if err != nil {
			t.Fatalf("OpenBytes: %v", err)
		}
		return doc
	}
	err, elapsed, _ := timedRender(open(), core.RenderOptions{DPI: 72})
	if err != nil {
		t.Fatalf("err = %v, want the page to render without the broken image", err)
	}
	if elapsed > limit {
		t.Errorf("render took %v CPU, want ≤ %v (2× the flood gate + 1 s)", elapsed, limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err = open().RenderPageContext(ctx, 0, core.RenderOptions{DPI: 72})
	wall := time.Since(start)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline render: err = %v", err)
	}
	if wall > 3*time.Second { // 1 s deadline + slack for a loaded host
		t.Errorf("render under a 1 s deadline returned after %v", wall)
	}
	t.Logf("%v CPU (limit %v); under a 1 s deadline: %v wall, err %v", elapsed, limit, wall, err)
}

// repeatedBombContentsDoc is a page whose /Contents array lists one Flate
// stream reps times; the stream inflates to 64 MiB of spaces (a 64 KB file).
func repeatedBombContentsDoc(t *testing.T, reps int) *core.Document {
	t.Helper()
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(bytes.Repeat([]byte(" "), 64<<20))
	zw.Close()
	pdf := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents [" + strings.Repeat("4 0 R ", reps) + "] >>",
		fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.String()),
	})
	doc, err := core.OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// classicPDF serializes objs as objects 1..n with a classic xref table and a
// trailer whose /Root is object 1.
func classicPDF(objs []string) []byte {
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

// TestDoS_RepeatedContentsBombIsCapped: a /Contents array repeating one 64 MiB
// Flate bomb was concatenated whole before the render budget or ExtractText
// got control (render-budget.md §9 R1): 4 refs ≈ 1.36 GB RSS, 24 refs ≈ 5 GB
// (fatal under js/wasm's 4 GiB), ≈ 10 s. The page's content is now capped at
// parse.MaxPageContent (256 MiB) and a repeated stream is decoded once, so
// each shape fails after one 64 MiB inflate: the render with
// ErrRenderBudgetExceeded naming content bytes, extraction with
// parse.ErrDecodeLimit.
func TestDoS_RepeatedContentsBombIsCapped(t *testing.T) {
	skipUnlessTimed(t)
	for _, reps := range []int{4, 12, 24} {
		t.Run(fmt.Sprintf("%d refs", reps), func(t *testing.T) {
			doc := repeatedBombContentsDoc(t, reps)
			err, elapsed, alloc := timedRender(doc, core.RenderOptions{DPI: 72})
			if !errors.Is(err, core.ErrRenderBudgetExceeded) || !strings.Contains(err.Error(), "content bytes") {
				t.Fatalf("render err = %v, want ErrRenderBudgetExceeded naming content bytes", err)
			}
			if alloc > 600<<20 {
				t.Errorf("render allocated %d MB, want ≤ 600 MB", alloc>>20)
			}
			if elapsed > 3*time.Second { // one 64 MiB inflate: ≈ 0.1–0.3 s
				t.Errorf("render took %v CPU, want ≤ 3 s", elapsed)
			}

			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := cpuTime()
			_, xerr := doc.ExtractText(core.ExtractOptions{})
			xelapsed := cpuTime() - start
			runtime.ReadMemStats(&after)
			if !errors.Is(xerr, parse.ErrDecodeLimit) {
				t.Fatalf("ExtractText err = %v, want parse.ErrDecodeLimit", xerr)
			}
			if xalloc := after.TotalAlloc - before.TotalAlloc; xalloc > 600<<20 {
				t.Errorf("ExtractText allocated %d MB, want ≤ 600 MB", xalloc>>20)
			}
			if xelapsed > 3*time.Second {
				t.Errorf("ExtractText took %v CPU, want ≤ 3 s", xelapsed)
			}
			t.Logf("render %v CPU, %d MB allocated; ExtractText %v CPU", elapsed, alloc>>20, xelapsed)
		})
	}
}

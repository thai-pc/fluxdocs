package render

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

var blackRGBA = color.RGBA{0, 0, 0, 0xff}

// budgetPage parses a one-page PDF built from objs (object 1 = catalog, 2 =
// pages; the page tree's /MediaBox is mediaBox) and returns its resolver and
// page.
func budgetPage(t *testing.T, objs map[int]string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	pdf := buildRenderPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1))
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	return r, pages[0]
}

// contentPage is a 100x100pt page (100x100 px at DPI 72) running content.
func contentPage(t *testing.T, content string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	return budgetPage(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: streamObj(content),
	})
}

// imagePage is a 100x100pt page that paints the 100x100 DeviceGray image /Im
// once per placement in content.
func imagePage(t *testing.T, content string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	samples := strings.Repeat("\x80", 100*100)
	return budgetPage(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 100 /Height 100 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(samples), samples),
	})
}

// formPage is a 100x100pt page that Do's the Form XObject /Fm, whose content is
// formContent, as directed by content.
func formPage(t *testing.T, content, formContent string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	return budgetPage(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Length %d >>\nstream\n%s\nendstream", len(formContent), formContent),
	})
}

// fontPage is buildFontPDF's 200x200pt page with an embedded TrueType /F1.
func fontPage(t *testing.T, content string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	r, err := parse.NewResolver(buildFontPDF(content))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	return r, pages[0]
}

// flateBomb is a zlib stream that inflates to n zero bytes.
func flateBomb(n int) string {
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(make([]byte, n))
	zw.Close()
	return z.String()
}

// repeatedContentsPage is a 100x100pt page whose /Contents array lists one
// Flate stream reps times; the stream inflates to content.
func repeatedContentsPage(t *testing.T, content []byte, reps int) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(content)
	zw.Close()
	return budgetPage(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents [" + strings.Repeat("4 0 R ", reps) + "] >>",
		4: fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.String()),
	})
}

// bombPage is a 100x100pt page running content, whose resources hold a stream
// that inflates to n bytes in the role named by kind: "image" (a 1×1 DeviceGray
// /Im), "mask" (a 1×1 /ImageMask /Im), "smask" (the /SMask of a 1×1 /Im),
// "form" (/Fm, whose content is n bytes of spaces) or "font" (the /FontFile2 of
// /F1, loaded with the page).
func bombPage(t *testing.T, kind string, n int, content string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	body := flateBomb(n)
	if kind == "form" {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(bytes.Repeat([]byte(" "), n))
		zw.Close()
		body = z.String()
	}
	flate := func(dict string) string {
		return fmt.Sprintf("<< %s /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", dict, len(body), body)
	}
	gray := "/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8"
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R /Fm 5 0 R >> /Font << /F1 6 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		6: "<< /Type /Font /Subtype /TrueType /FontDescriptor 7 0 R >>",
		7: "<< /Type /FontDescriptor /FontFile2 8 0 R >>",
		8: streamObj("x"),
		9: streamObj("x"),
	}
	switch kind {
	case "image":
		objs[5] = flate(gray)
	case "mask":
		objs[5] = flate("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ImageMask true")
	case "smask":
		objs[5] = "<< " + gray + " /SMask 9 0 R /Length 1 >>\nstream\n\x80\nendstream"
		objs[9] = flate(gray)
	case "form":
		objs[5] = flate("/Type /XObject /Subtype /Form /BBox [0 0 100 100]")
	case "font":
		objs[5] = streamObj("x")
		objs[8] = flate("")
	}
	return budgetPage(t, objs)
}

// paddedJPEGPage is a 100x100pt page running content, whose /Im is a 1×1
// grayscale baseline JPEG padded with a 64 KiB comment segment: its compressed
// data far exceeds its frame's raw size (1 byte), which is what decodeJPEG
// charges as decoded bytes.
func paddedJPEGPage(t *testing.T, content string) (*parse.Resolver, parse.PageInfo) {
	t.Helper()
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, image.NewGray(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	enc := jb.Bytes()
	const comLen = 65535 // the segment length field counts itself, ≤ 0xFFFF
	com := append([]byte{0xFF, 0xFE, comLen >> 8, comLen & 0xFF}, make([]byte, comLen-2)...)
	raw := append(append(append([]byte(nil), enc[:2]...), com...), enc[2:]...) // after SOI
	return budgetPage(t, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n%s\nendstream", len(raw), raw),
	})
}

// defaultLimits returns the limits render.Page would resolve for a w×h px
// canvas with default options, for tests that tighten one counter.
func defaultLimits(w, h int64) *budgetLimits {
	lim := resolveLimits(0, 0, w*h)
	return &lim
}

// TestBudget_Trips drives each counter past its limit with a small input. Each
// case must fail with ErrBudgetExceeded naming the resource, with no image.
func TestBudget_Trips(t *testing.T) {
	fullFills := strings.Repeat("0 0 100 100 re f\n", 50)
	wideStrokes := strings.Repeat("200 w 0 50 m 100 50 l S\n", 50)
	// 500 zero-width teeth spanning every row: no pixel is painted, but every
	// row crosses 1000 edges, so only the edge work can trip the budget.
	var comb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&comb, "%d.1 0 0 100 re ", i%100)
	}
	comb.WriteString("f\n")
	// A 100x100 image squeezed into a 1x1 px box: the decode is the cost.
	tinyImages := strings.Repeat("q 1 0 0 1 10 10 cm /Im Do Q\n", 20)
	qFlood := strings.Repeat("q Q ", 1000)
	invisibleText := "BT /F1 12 Tf 3 Tr (" + strings.Repeat("A", 5000) + ") Tj ET"
	whitespaceForm := strings.Repeat(" ", 64<<10)
	manyDo := strings.Repeat("/Fm Do\n", 40)
	longPath := "0 0 m " + strings.Repeat("1 1 l ", 1000) + "f"
	// A 1×1 image whose Flate body inflates to 1 MiB, Do'd 20 times: one sample
	// each, so only the inflated bytes can trip (decoded bytes, 4 MiB here).
	bombDos := strings.Repeat("/Im Do\n", 20)

	var draw []AnnPoint
	for i := 0; i < 2000; i++ {
		draw = append(draw, AnnPoint{X: float64(i % 2 * 100), Y: float64(i % 100)})
	}

	withLimit := withLimitT

	tests := []struct {
		name     string
		page     func(*testing.T) (*parse.Resolver, parse.PageInfo)
		opts     Options
		resource string
	}{
		{
			name:     "full-canvas fills",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, fullFills) },
			opts:     Options{MaxPaintPixels: 200_000},
			resource: "paint work",
		},
		{
			name:     "wide full-canvas strokes",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, wideStrokes) },
			opts:     Options{MaxPaintPixels: 200_000},
			resource: "paint work",
		},
		{
			name:     "many-edge fill charges edge work",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, comb.String()) },
			opts:     Options{MaxPaintPixels: 50_000},
			resource: "paint work",
		},
		{
			name:     "repeated tiny-placed image decode",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return imagePage(t, tinyImages) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.samples = 100_000 })},
			resource: "image samples",
		},
		{
			name:     "q Q flood",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, qFlood) },
			opts:     Options{MaxOperators: 1000},
			resource: "operators",
		},
		{
			name:     "huge invisible Tj counts glyphs",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return fontPage(t, invisibleText) },
			opts:     Options{MaxOperators: 1000},
			resource: "operators",
		},
		{
			name: "whitespace-only form Do'd repeatedly",
			page: func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
				return formPage(t, manyDo, whitespaceForm)
			},
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.content = 1 << 20 })},
			resource: "content bytes",
		},
		{
			name:     "1x1 image with a Flate bomb Do'd repeatedly",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return bombPage(t, "image", 1<<20, bombDos) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.decoded = 4 << 20 })},
			resource: "decoded bytes",
		},
		{
			name:     "1x1 image mask with a Flate bomb Do'd repeatedly",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return bombPage(t, "mask", 1<<20, bombDos) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.decoded = 4 << 20 })},
			resource: "decoded bytes",
		},
		{
			name:     "1x1 SMask with a Flate bomb Do'd repeatedly",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return bombPage(t, "smask", 1<<20, bombDos) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.decoded = 4 << 20 })},
			resource: "decoded bytes",
		},
		{
			name:     "font program Flate bomb",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return bombPage(t, "font", 1<<20, "q Q") },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.decoded = 512 << 10 })},
			resource: "decoded bytes",
		},
		{
			name:     "1x1 JPEG padded to 64 KiB Do'd repeatedly",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return paddedJPEGPage(t, bombDos) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.decoded = 256 << 10 })},
			resource: "decoded bytes",
		},
		{
			name: "Flate form of whitespace Do'd repeatedly",
			page: func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
				return bombPage(t, "form", 1<<20, strings.Repeat("/Fm Do\n", 20))
			},
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.content = 4 << 20 })},
			resource: "content bytes",
		},
		{
			name: "tiny image Do flood charges each Do",
			page: func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
				return imagePage(t, strings.Repeat("q 0.01 0 0 0.01 0 0 cm /Im Do Q\n", 1000))
			},
			opts:     Options{MaxPaintPixels: 1000 * doCost / 2},
			resource: "paint work",
		},
		{
			name: "Flate content stream repeated in /Contents",
			page: func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
				return repeatedContentsPage(t, bytes.Repeat([]byte(" "), 1<<20), 20)
			},
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.content = 4 << 20 })},
			resource: "content bytes",
		},
		{
			name:     "long current path",
			page:     func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, longPath) },
			opts:     Options{testLimits: withLimit(func(l *budgetLimits) { l.pathPoints = 500 })},
			resource: "path points",
		},
		{
			name: "freehand annotation overlay shares the budget",
			page: func(t *testing.T) (*parse.Resolver, parse.PageInfo) { return contentPage(t, "") },
			opts: Options{
				MaxPaintPixels: 100_000,
				Annotations:    []Annotation{{Type: "draw", Color: "#000000", Opacity: 1, Points: draw}},
			},
			resource: "paint work",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, pi := tc.page(t)
			opts := tc.opts
			opts.DPI = 72
			data, err := Page(r, pi, opts)
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("err = %v, want ErrBudgetExceeded", err)
			}
			if data != nil {
				t.Errorf("got %d image bytes with the error, want nil", len(data))
			}
			if !strings.Contains(err.Error(), tc.resource) {
				t.Errorf("err = %q, want it to name %q", err, tc.resource)
			}

			// The same page renders when the tripped counter is off: the trip
			// came from the budget, not from a broken input.
			opts.MaxPaintPixels, opts.MaxOperators = -1, -1
			opts.testLimits = &budgetLimits{paint: unlimited, steps: unlimited, samples: unlimited, content: unlimited, decoded: unlimited, pathPoints: unlimited}
			if _, err := Page(r, pi, opts); err != nil {
				t.Errorf("unlimited render: %v", err)
			}
		})
	}
}

// TestBudget_DecodeIsCapped checks one decode cannot inflate past what the
// budget has left: a single 1×1 image whose body inflates to 16 MiB stops at
// the 1 MiB decoded-bytes limit plus one byte (its one free sample byte aside),
// not after inflating all of it.
func TestBudget_DecodeIsCapped(t *testing.T) {
	r, pi := bombPage(t, "image", 16<<20, "/Im Do")
	_, err := Page(r, pi, Options{DPI: 72, testLimits: withLimitT(func(l *budgetLimits) { l.decoded = 1 << 20 })})
	if want := fmt.Sprintf("decoded bytes %d > %d", 1<<20+1, 1<<20); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want ErrBudgetExceeded with %q", err, want)
	}
}

// withLimitT returns the default limits for a 100x100 px canvas changed by set.
func withLimitT(set func(*budgetLimits)) *budgetLimits {
	lim := defaultLimits(100, 100)
	set(lim)
	return lim
}

// TestBudget_ExactLimit pins the accounting so counting stays deterministic: a
// render costing exactly the limit passes and one unit less fails.
//
// One `0 0 100 100 re f` on a 100x100 px canvas costs 4 points × fillPointCost
// (setup) + 100 rows × fillRowCost(2 active edges) + 100 rows × 100 px = 16 +
// 900 + 10000 = 10916 paint units and 2 operators (re, f).
func TestBudget_ExactLimit(t *testing.T) {
	r, pi := contentPage(t, "0 0 100 100 re f")
	tests := []struct {
		name     string
		opts     Options
		wantFail bool
	}{
		{"paint at limit", Options{MaxPaintPixels: 10916}, false},
		{"paint one under", Options{MaxPaintPixels: 10915}, true},
		{"operators at limit", Options{MaxOperators: 2}, false},
		{"operators one under", Options{MaxOperators: 1}, true},
	}
	if got := 4*fillPointCost + 100*fillRowCost(2) + 100*100; got != 10916 {
		t.Fatalf("expected cost = %d, want 10916: update this test with the constants", got)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.DPI = 72
			_, err := Page(r, pi, opts)
			if tc.wantFail != errors.Is(err, ErrBudgetExceeded) {
				t.Errorf("err = %v, want budget failure = %v", err, tc.wantFail)
			}
			if !tc.wantFail && err != nil {
				t.Errorf("err = %v, want success", err)
			}
		})
	}
}

// TestBudget_PageContentWithinBudget pins how the page's own content meets the
// content-bytes counter (render-budget.md §9 R1). It is decoded within what is
// left of the counter, charged once (a page at exactly the limit renders, one
// byte under trips), and a /Contents array repeating a stream fails at the
// limit without materializing every copy: 200 repeats of a 1 MiB stream used
// to concatenate 200 MiB before the counter was consulted.
func TestBudget_PageContentWithinBudget(t *testing.T) {
	// "q Q" twice plus two separators: 8 content bytes.
	r, pi := repeatedContentsPage(t, []byte("q Q"), 2)
	for _, tc := range []struct {
		limit    int64
		wantFail bool
	}{{8, false}, {7, true}} {
		_, err := Page(r, pi, Options{DPI: 72, testLimits: withLimitT(func(l *budgetLimits) { l.content = tc.limit })})
		if tc.wantFail != errors.Is(err, ErrBudgetExceeded) || (!tc.wantFail && err != nil) {
			t.Errorf("content limit %d: err = %v, want budget failure = %v", tc.limit, err, tc.wantFail)
		}
	}

	r, pi = repeatedContentsPage(t, bytes.Repeat([]byte(" "), 1<<20), 200)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := Page(r, pi, Options{DPI: 72, testLimits: withLimitT(func(l *budgetLimits) { l.content = 4 << 20 })})
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrBudgetExceeded) || !strings.Contains(err.Error(), "content bytes") {
		t.Fatalf("err = %v, want ErrBudgetExceeded naming content bytes", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Errorf("render allocated %d MB, want ≤ 32 MB (one 1 MiB decode, not 200)", alloc>>20)
	}
}

// TestResolveLimits pins the defaults (render-budget.md §3): 32 × canvas pixels
// clamped to [16 Mi, 1 Gi] for paint, defaultMaxOperators for steps; negative
// disables; positive is absolute and not scaled by DPI.
func TestResolveLimits(t *testing.T) {
	letter := func(dpi int64) int64 { // a Letter page's canvas at dpi
		w := (612*dpi + 71) / 72
		h := (792*dpi + 71) / 72
		return w * h
	}
	tests := []struct {
		name               string
		maxPaint, maxOps   int64
		canvasPx           int64
		wantPaint, wantOps int64
	}{
		{"Letter 72 DPI hits the floor", 0, 0, letter(72), 16 << 20, defaultMaxOperators},
		{"Letter 150 DPI", 0, 0, letter(150), 32 * 1275 * 1650, defaultMaxOperators},
		{"Letter 300 DPI", 0, 0, letter(300), 32 * 2550 * 3300, defaultMaxOperators},
		{"max canvas hits the ceiling", 0, 0, maxCanvasPixels, 1 << 30, defaultMaxOperators},
		{"ceiling boundary", 0, 0, (1 << 30) / 32, 1 << 30, defaultMaxOperators},
		{"negative disables", -1, -5, letter(150), unlimited, unlimited},
		{"positive is absolute", 1234, 56, letter(300), 1234, 56},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lim := resolveLimits(tc.maxPaint, tc.maxOps, tc.canvasPx)
			if lim.paint != tc.wantPaint || lim.steps != tc.wantOps {
				t.Errorf("limits = paint %d, steps %d; want %d, %d", lim.paint, lim.steps, tc.wantPaint, tc.wantOps)
			}
			if lim.samples != maxImageSamples || lim.content != maxContentBytes || lim.decoded != maxDecodedBytes || lim.pathPoints != maxPathPoints {
				t.Errorf("internal caps = %+v, want the constants", lim)
			}
		})
	}
	if got := letter(150); got != 1275*1650 {
		t.Fatalf("letter(150) = %d, want %d", got, 1275*1650)
	}
}

// TestBudget_DefaultTripsUnlimitedRenders checks the zero-value options enforce
// the budget (on by default) and a negative value disables it.
func TestBudget_DefaultTripsUnlimitedRenders(t *testing.T) {
	// 100x100 px canvas: the default is the 16 Mi floor, ≈ 1600 full fills.
	r, pi := contentPage(t, strings.Repeat("0 0 100 100 re f\n", 2000))
	if _, err := Page(r, pi, Options{DPI: 72}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("default budget: err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := Page(r, pi, Options{DPI: 72, MaxPaintPixels: -1}); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
}

// TestBudget_OutputUnchanged checks that metering never changes output: pages
// that stay within the budget render byte-identically with the default budget
// and with it disabled.
func TestBudget_OutputUnchanged(t *testing.T) {
	pages := map[string]func(*testing.T) (*parse.Resolver, parse.PageInfo){
		"fills and strokes": func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
			return contentPage(t, "1 0 0 rg 10 10 80 80 re f 0 0 1 RG 5 w 0 0 m 100 100 l 100 0 l h S")
		},
		"text": func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
			return fontPage(t, "BT /F1 24 Tf 10 100 Td (Budget) Tj [(A) -200 (B)] TJ ET")
		},
		"image": func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
			return imagePage(t, "q 80 0 0 80 10 10 cm /Im Do Q")
		},
		"form": func(t *testing.T) (*parse.Resolver, parse.PageInfo) {
			return formPage(t, "/Fm Do", "0 1 0 rg 0 0 50 50 re f")
		},
	}
	ann := []Annotation{{Type: "highlight", X: 10, Y: 10, Width: 30, Height: 20, Opacity: 0.4}}
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			r, pi := page(t)
			on, err := Page(r, pi, Options{DPI: 150, Annotations: ann})
			if err != nil {
				t.Fatal(err)
			}
			off, err := Page(r, pi, Options{DPI: 150, Annotations: ann, MaxPaintPixels: -1, MaxOperators: -1})
			if err != nil {
				t.Fatal(err)
			}
			if string(on) != string(off) {
				t.Error("budgeted render differs from the unlimited render")
			}
		})
	}
}

// heavyContent is n full-canvas fills: with an unlimited budget it runs long
// enough for a deadline to fire mid-render.
func heavyContent(n int) string { return strings.Repeat("0 0 100 100 re f\n", n) }

func TestBudget_ContextAlreadyCanceled(t *testing.T) {
	r, pi := contentPage(t, "0 0 100 100 re f")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := Page(r, pi, Options{DPI: 72, Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("err = %v also matches ErrBudgetExceeded", err)
	}
	if data != nil {
		t.Errorf("got %d bytes, want nil", len(data))
	}
}

// TestBudget_CancelCauseIsWrapped checks a canceler's cause stays visible next
// to context.Canceled (core's batch cancellation relies on it).
func TestBudget_CancelCauseIsWrapped(t *testing.T) {
	r, pi := contentPage(t, "0 0 100 100 re f")
	cause := errors.New("stop now")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	_, err := Page(r, pi, Options{DPI: 72, Context: ctx})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want both context.Canceled and the cause", err)
	}
}

func TestBudget_NilContext(t *testing.T) {
	r, pi := contentPage(t, "0 0 100 100 re f")
	if _, err := Page(r, pi, Options{DPI: 72, Context: nil}); err != nil {
		t.Fatalf("nil Context: %v", err)
	}
}

// TestBudget_DeadlineMidRender runs an unbounded-budget heavy page under a short
// deadline: it must stop with DeadlineExceeded (not a budget error) shortly
// after the deadline.
func TestBudget_DeadlineMidRender(t *testing.T) {
	// 300 DPI: 417x417 px per fill; 20000 fills ≈ 3.5 G pixel visits.
	r, pi := contentPage(t, heavyContent(20000))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	data, err := Page(r, pi, Options{DPI: 300, MaxPaintPixels: -1, MaxOperators: -1, Context: ctx})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("a deadline must not be a budget error: %v", err)
	}
	if data != nil {
		t.Errorf("got %d bytes, want nil", len(data))
	}
	if elapsed > 5*time.Second { // generous slack for -race and loaded CI
		t.Errorf("render returned after %v, want shortly after the 50ms deadline", elapsed)
	}
}

// clockOnlyCtx has a deadline but never reports itself done, like a
// context.WithTimeout under js/wasm whose timer goroutine cannot run while a
// synchronous export is busy. Only the budget's explicit clock check can stop
// a render under it.
type clockOnlyCtx struct {
	context.Context
	deadline time.Time
}

func (c clockOnlyCtx) Deadline() (time.Time, bool) { return c.deadline, true }

func TestBudget_DeadlineByClock(t *testing.T) {
	r, pi := contentPage(t, heavyContent(20000))
	ctx := clockOnlyCtx{Context: context.Background(), deadline: time.Now().Add(30 * time.Millisecond)}
	start := time.Now()
	_, err := Page(r, pi, Options{DPI: 300, MaxPaintPixels: -1, MaxOperators: -1, Context: ctx})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded from the clock check", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("render returned after %v, want shortly after the 30ms deadline", elapsed)
	}

	// A deadline already past by the clock stops the render before any work.
	r, pi = contentPage(t, "0 0 100 100 re f")
	past := clockOnlyCtx{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if _, err := Page(r, pi, Options{DPI: 72, Context: past}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("past deadline: err = %v, want context.DeadlineExceeded", err)
	}
}

// TestBudget_PollQuantum pins when the context is polled: after
// pollStepQuantum steps or pollPaintQuantum paint units, never in between, so
// the hot path stays one add and one compare.
func TestBudget_PollQuantum(t *testing.T) {
	past := clockOnlyCtx{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	open := budgetLimits{paint: unlimited, steps: unlimited, samples: unlimited, content: unlimited, decoded: unlimited, pathPoints: unlimited}

	b := newBudget(past, open)
	for i := 1; i < pollStepQuantum; i++ {
		if !b.step() {
			t.Fatalf("step %d polled early", i)
		}
	}
	if b.step() {
		t.Fatalf("step %d did not poll the expired deadline", pollStepQuantum)
	}
	if !errors.Is(b.err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", b.err)
	}

	b = newBudget(past, open)
	if !b.chargePaint(pollPaintQuantum - 1) {
		t.Fatal("paint polled early")
	}
	if b.chargePaint(1) {
		t.Fatal("paint quantum did not poll the expired deadline")
	}
}

// TestBudget_StickyStop checks that after a trip every hot-path charge fails,
// so no routine paints past the first trip.
func TestBudget_StickyStop(t *testing.T) {
	b := newBudget(context.Background(), budgetLimits{paint: 10, steps: 10, samples: 10, content: 10, decoded: 10, pathPoints: 10})
	if !b.chargePaint(10) {
		t.Fatal("charge at the limit failed")
	}
	if b.chargePaint(1) {
		t.Fatal("charge past the limit succeeded")
	}
	if b.step() || b.chargePaint(0) || b.chargeSamples(0) || b.chargeContent(0) || b.chargeDecoded(0) || b.checkPathPoints(0) {
		t.Error("a charge succeeded after the budget stopped")
	}
	if !errors.Is(b.err, ErrBudgetExceeded) || !strings.Contains(b.err.Error(), "paint work 11 > 10") {
		t.Errorf("err = %v, want the first trip (paint work 11 > 10)", b.err)
	}
}

// TestBudget_FillChargesSort pins fillPath's per-row charge on a zigzag whose
// 1024 edges all cross every row: each row pays fillRowCost(1024) =
// 1 + 1024·(fillCrossingCost + 11), the k log k sort included, not k + 1 (the
// charge that let such paths run 10–50× past the budget).
func TestBudget_FillChargesSort(t *testing.T) {
	const k, rows = 1024, 64
	poly := make([]point, k)
	for i := range poly {
		poly[i] = point{float64((i * 7919) % 500), float64(i%2) * rows}
	}
	rs := newRaster(500, rows)
	rs.fillPath([][]point{poly}, color.RGBA{0, 0, 0, 0}, false) // transparent: spans still charge
	rowCharge := int64(rows) * fillRowCost(k)
	if want := int64(1 + k*(fillCrossingCost+11)); fillRowCost(k) != want {
		t.Fatalf("fillRowCost(%d) = %d, want %d", k, fillRowCost(k), want)
	}
	if setup := int64(k * fillPointCost); rs.b.paint < setup+rowCharge {
		t.Errorf("charged %d, want ≥ %d (setup %d + %d rows × fillRowCost)", rs.b.paint, setup+rowCharge, setup, rows)
	}
}

// TestBudget_UnlimitedRasterNeverTrips checks newRaster's default budget, used by
// tests that drive the rasterizer directly.
func TestBudget_UnlimitedRasterNeverTrips(t *testing.T) {
	rs := newRaster(10, 10)
	for i := 0; i < 1000; i++ {
		rs.fillPath([][]point{{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}, blackRGBA, false)
	}
	if rs.b.stopped() {
		t.Fatalf("unlimited raster budget stopped: %v", rs.b.err)
	}
}

// calibrate runs work b.N times, each run followed by a wide-span pixel-visit
// reference, and reports work's time per paint unit it charged ("ns/unit") and
// that time over the reference's time per pixel visit ("px/unit"; the cost
// constants are right when it is ≈ 1). Both sides are timed in process CPU time
// (cpuTime) and interleaved, so host load affects them alike. work returns the
// units it charged.
func calibrate(b *testing.B, work func() int64) {
	const refSpans = 256 // ≈ 1 Mi pixel visits per iteration
	ref := newRaster(4096, 64)
	var workNs, refNs, units int64
	for i := 0; i < b.N; i++ {
		t := cpuTime()
		units += work()
		workNs += int64(cpuTime() - t)
		t = cpuTime()
		for j := 0; j < refSpans; j++ {
			ref.span(0, 4096, 32, blackRGBA)
		}
		refNs += int64(cpuTime() - t)
	}
	perUnit := float64(workNs) / float64(units)
	b.ReportMetric(perUnit, "ns/unit")
	b.ReportMetric(perUnit/(float64(refNs)/float64(b.N*refSpans*4096)), "px/unit")
}

// charged returns a work function for calibrate that runs paint on rs and
// returns the paint units it charged.
func charged(rs *raster, paint func()) func() int64 {
	return func() int64 {
		before := rs.b.paint
		paint()
		return rs.b.paint - before
	}
}

// BenchmarkCalibration measures the costs behind the budget's constants
// (render-budget.md §3, §8). Absolute times depend on the host; what sets the
// constants is each paint benchmark's px/unit, its time per charged unit over a
// pixel visit timed alongside (calibrate), which should be ≈ 1:
//   - pixel-visit: one composited pixel of a wide span, the unit of
//     MaxPaintPixels;
//   - line-row: rows of a thin, tall stroke, mostly range-search overhead; sets
//     lineRowCost;
//   - line-dots: zero-length hairlines, where the per-call setup dominates;
//     sets lineCallCost;
//   - line-hairlines: many short random hairlines (a CAD drawing), calls, rows
//     and pixels together: a check on both;
//   - fill-setup: fillPath on a path that crosses no row center, so only the
//     per-point setup runs; sets fillPointCost;
//   - fill-dense-row: rows of 64 Ki random crossings (a zigzag whose edges all
//     span the canvas), sort included, charged by fillRowCost; sets
//     fillCrossingCost;
//   - image-pixel: pixels of a scaled image placement; sets imagePixelCost;
//   - do-tiny-image: a 1×1 image Do'd repeatedly, per unit of steps + paint;
//     sets doCost;
//   - glyph-tiny-visible: visible glyphs too small to cover a row (outline
//     load, flatten, fill setup), per unit of steps + paint; sets
//     glyphPointCost;
//   - step-*: one interpreter step with no painting (a q/Q operator, a glyph
//     advanced in an invisible render mode); the slowest sets
//     defaultMaxOperators so a steps-only page stays ≲ 1 s native;
//   - scan-op: ScanContent per operator, which runs before the budget is
//     consulted (render-budget.md §9 R2), for reference.
func BenchmarkCalibration(b *testing.B) {
	perUnit := func(b *testing.B, units int, name string) {
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(units), name)
	}
	b.Run("pixel-visit", func(b *testing.B) {
		rs := newRaster(4096, 64)
		for i := 0; i < b.N; i++ {
			rs.span(0, 4096, 32, blackRGBA)
		}
		perUnit(b, 4096, "ns/px")
	})
	b.Run("line-row", func(b *testing.B) {
		const h = 8192 // log2(steps) ≈ 13, a large page's diagonal
		rs := newRaster(64, h)
		calibrate(b, charged(rs, func() { rs.line(point{32.3, 0}, point{32.7, h}, blackRGBA, 1) }))
	})
	b.Run("line-dots", func(b *testing.B) {
		rng := rand.New(rand.NewPCG(9, 10))
		dots := make([]point, 1<<14)
		for i := range dots { // zero-length hairlines: the per-call setup dominates
			dots[i] = point{20 + rng.Float64()*1235, 20 + rng.Float64()*1610}
		}
		rs := newRaster(1275, 1650)
		calibrate(b, charged(rs, func() {
			for _, p := range dots {
				rs.line(p, p, blackRGBA, 1)
			}
		}))
	})
	b.Run("line-hairlines", func(b *testing.B) {
		rng := rand.New(rand.NewPCG(3, 4))
		segs := make([][2]point, 1<<14)
		for i := range segs { // ≤ 31 px each way: 15 pt at 150 DPI
			x, y := 20+rng.Float64()*1235, 20+rng.Float64()*1610
			segs[i] = [2]point{{x, y}, {x + (rng.Float64()-0.5)*62, y + (rng.Float64()-0.5)*62}}
		}
		rs := newRaster(1275, 1650)
		calibrate(b, charged(rs, func() {
			for _, sg := range segs {
				rs.line(sg[0], sg[1], blackRGBA, 1)
			}
		}))
	})
	b.Run("fill-setup", func(b *testing.B) {
		rng := rand.New(rand.NewPCG(5, 6))
		poly := make([]point, 1<<16)
		for i := range poly { // y in (0.55, 0.95): no row center, so only setup runs
			poly[i] = point{rng.Float64() * 4096, 0.55 + rng.Float64()*0.4}
		}
		rs := newRaster(4096, 4)
		calibrate(b, charged(rs, func() { rs.fillPath([][]point{poly}, blackRGBA, false) }))
	})
	b.Run("fill-dense-row", func(b *testing.B) {
		const rows = 16
		rng := rand.New(rand.NewPCG(7, 8))
		poly := make([]point, 1<<16)
		for i := range poly { // every edge spans every row, in random x order
			poly[i] = point{rng.Float64() * 4096, float64(i%2) * rows}
		}
		rs := newRaster(4096, rows)
		calibrate(b, charged(rs, func() { rs.fillPath([][]point{poly}, blackRGBA, false) }))
	})
	b.Run("image-pixel", func(b *testing.B) {
		const side = 512
		img := &decodedImage{w: 64, h: 64, pix: make([]color.RGBA, 64*64)}
		for i := range img.pix {
			img.pix[i] = color.RGBA{uint8(i), uint8(i >> 4), 0x80, 0xff}
		}
		rs := newRaster(side, side)
		calibrate(b, charged(rs, func() { drawImage(rs, matrix{side, 0, 0, side, 0, 0}, img) }))
	})

	const glyphs = 1 << 14
	// Visible glyphs too small to cover a row: LoadGlyph and the fill setup
	// are the cost, charged as one step plus paint units per outline point.
	r2, pi2 := fontPageB(b, "BT /F1 0.01 Tf ("+strings.Repeat("Ag", glyphs/2)+") Tj ET")
	content2, err := r2.PageContent(pi2)
	if err != nil {
		b.Fatal(err)
	}
	visible := parse.ScanContent(content2)
	fonts2 := loadFonts(r2, pi2.Resources, unlimitedBudget())
	b.Run("glyph-tiny-visible", func(b *testing.B) {
		calibrate(b, func() int64 {
			rs := newRaster(1, 1)
			run(rs, identity(), visible, fonts2, r2, pi2.Resources)
			return rs.b.paint + rs.b.steps
		})
	})

	// A 1×1 unfiltered image Do'd repeatedly at 1 px: the per-Do fixed cost
	// (resource lookup, dictionary checks, decode, allocation) dominates.
	const dos = 1 << 14
	r3, pi3 := budgetPageB(b, map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj("x"),
		5: "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x80\nendstream",
	})
	tinyDos := parse.ScanContent([]byte(strings.Repeat("/Im Do\n", dos)))
	b.Run("do-tiny-image", func(b *testing.B) {
		calibrate(b, func() int64 {
			rs := newRaster(100, 100)
			run(rs, identity(), tinyDos, nil, r3, pi3.Resources)
			return rs.b.paint + rs.b.steps
		})
	})

	const pairs = 1 << 17
	qq := parse.ScanContent([]byte(strings.Repeat("q Q ", pairs)))
	b.Run("step-qQ", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rs := newRaster(1, 1)
			run(rs, identity(), qq, nil, nil, nil)
		}
		perUnit(b, 2*pairs, "ns/step")
	})

	r, pi := fontPageB(b, "BT /F1 12 Tf 3 Tr ("+strings.Repeat("Ag", glyphs/2)+") Tj ET")
	content, err := r.PageContent(pi)
	if err != nil {
		b.Fatal(err)
	}
	invisible := parse.ScanContent(content)
	fonts := loadFonts(r, pi.Resources, unlimitedBudget())
	b.Run("step-invisible-glyph", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rs := newRaster(1, 1)
			run(rs, identity(), invisible, fonts, r, pi.Resources)
		}
		perUnit(b, glyphs, "ns/step")
	})

	scanSrc := []byte(strings.Repeat("q Q ", pairs))
	b.Run("scan-op", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parse.ScanContent(scanSrc)
		}
		perUnit(b, 2*pairs, "ns/scanned-op")
	})
}

// budgetPageB is budgetPage for benchmarks.
func budgetPageB(b *testing.B, objs map[int]string) (*parse.Resolver, parse.PageInfo) {
	b.Helper()
	r, err := parse.NewResolver(buildRenderPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1)))
	if err != nil {
		b.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		b.Fatal(err)
	}
	return r, pages[0]
}

// fontPageB is fontPage for benchmarks.
func fontPageB(b *testing.B, content string) (*parse.Resolver, parse.PageInfo) {
	b.Helper()
	r, err := parse.NewResolver(buildFontPDF(content))
	if err != nil {
		b.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		b.Fatal(err)
	}
	return r, pages[0]
}

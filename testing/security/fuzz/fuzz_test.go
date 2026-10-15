// Package fuzz holds the parser/pipeline fuzz targets. The core promise is that
// an untrusted PDF can never crash the library: every input must be parsed or
// rejected, never panic or hang (the DoS guards bound the work — SECURITY.md
// §2–3).
//
// The seed corpus (f.Add below) runs as an ordinary test under `go test`, so CI
// exercises these paths on every run. To actually fuzz:
//
//	go test ./testing/security/fuzz -run=xxx -fuzz=FuzzOpenBytes    -fuzztime=60s
//	go test ./testing/security/fuzz -run=xxx -fuzz=FuzzOpenSaveReopen -fuzztime=60s
package fuzz

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// minimalPDF is a tiny valid one-page PDF used as a fuzz seed.
const minimalPDF = "%PDF-1.7\n" +
	"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
	"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>\nendobj\n" +
	"3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n" +
	"4 0 obj\n<< /Length 40 >>\nstream\nBT /F1 12 Tf 20 100 Td (Seed) Tj ET\nendstream\nendobj\n" +
	"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n" +
	"trailer\n<< /Size 6 /Root 1 0 R >>\n"

// mediaBoxPDF builds a well-formed one-page PDF (classic xref, so it opens and
// reaches the rasterizer) whose page tree carries the given /MediaBox array.
func mediaBoxPDF(mediaBox string) []byte {
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox " + mediaBox + " >>",
		"<< /Type /Page /Parent 2 0 R >>",
	})
}

// contentPDF builds a well-formed one-page PDF (100x100pt) whose page content
// stream is content, so the seed reaches the content-stream interpreter and the
// rasterizer.
func contentPDF(content string) []byte {
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	})
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

// seeds returns a spread of inputs: valid, empty, header-only, and various
// truncations/corruptions that should exercise the lenient recovery paths.
func seeds() [][]byte {
	full := []byte(minimalPDF)
	return [][]byte{
		full,
		nil,
		[]byte(""),
		[]byte("%PDF-1.7\n"),
		[]byte("%PDF-1.7\ntrailer\n<< /Root 1 0 R >>\n"),
		full[:len(full)/2], // truncated mid-file
		bytes.ReplaceAll(full, []byte("obj"), []byte("")),                  // remove object markers
		bytes.Repeat([]byte("<< /Kids [2 0 R] >>"), 64),                    // nested-ish junk
		[]byte("2 0 obj << /Type /Pages /Kids [2 0 R] /Count 1 >> endobj"), // cyclic fragment
		// Hostile MediaBoxes: at the DPI 36 used by exercise, 2^33 pt is a
		// 2^32 x 2^32 canvas whose int64 area wraps to 0 and slipped past the
		// cap, so the rasterizer panicked; 1e18 pt overflows the float→int
		// conversion. Both must return the canvas-too-large error.
		mediaBoxPDF("[0 0 8589934592 8589934592]"),
		mediaBoxPDF("[0 0 1000000000000000000 1000000000000000000]"),
		// Same overflow reached through corner order/sign (Width/Height take
		// the absolute extent), a one-sided sliver, and lexer edge cases: an
		// int64-overflowing integer (lexed leniently as 0) and an over-long
		// real.
		mediaBoxPDF("[8589934592 8589934592 0 0]"),
		mediaBoxPDF("[-8589934592 -8589934592 0 0]"),
		mediaBoxPDF("[0 0 1 1000000000000000000]"),
		mediaBoxPDF("[0 0 99999999999999999999 10]"),
		mediaBoxPDF("[0 0 " + strings.Repeat("9", 400) + ".0 10]"),
		// Hostile strokes: segment length, coordinate magnitude and line width
		// are unbounded in the content stream, and the stroke rasterizer once
		// did work proportional to length × width² (a hang). The lexer has no
		// exponent syntax, so big magnitudes are spelled out; the cm cases
		// overflow the CTM to Inf and then NaN.
		contentPDF("0 0 m 1000000000 0 l S"),
		contentPDF("100000 w 50 50 m 51 50 l S"),
		contentPDF("0 50 m " + bigReal + " 50 l S"),
		contentPDF(bigReal + " w 50 50 m 51 50 l S"),
		contentPDF("-" + bigReal + " 50 m " + bigReal + " 50 l S"),
		contentPDF("-" + bigReal + " " + bigReal + " m " + bigReal + " -" + bigReal + " l S"),
		contentPDF("100000 w -1000000000 -1000000000 2000000000 2000000000 re S"),
		contentPDF(hugeReal + " 0 0 " + hugeReal + " 0 0 cm " + hugeReal + " 0 0 " + hugeReal + " 0 0 cm 0 0 m 1 1 l S"),
	}
}

// bigReal (1e30) and hugeReal (~1e308, still finite) are spelled-out reals for
// the hostile-stroke seeds.
var (
	bigReal  = "1" + strings.Repeat("0", 30) + ".0"
	hugeReal = "1" + strings.Repeat("0", 308) + ".0"
)

// hostileMediaBoxes are the seeds above that open as one-page documents whose
// canvas must be rejected by the renderer's cap at the DPI exercise uses.
var hostileMediaBoxes = []string{
	"[0 0 8589934592 8589934592]",
	"[0 0 1000000000000000000 1000000000000000000]",
	"[8589934592 8589934592 0 0]",
	"[-8589934592 -8589934592 0 0]",
	"[0 0 1 1000000000000000000]",
}

// TestHostileMediaBoxSeedsReturnError makes the seeds' contract explicit:
// exercise only proves "no panic", but each hostile MediaBox must also come back
// as an error from RenderPage, never as an image.
func TestHostileMediaBoxSeedsReturnError(t *testing.T) {
	for _, mb := range hostileMediaBoxes {
		t.Run(mb, func(t *testing.T) {
			doc, err := core.OpenBytes(mediaBoxPDF(mb))
			if err != nil {
				t.Fatalf("OpenBytes: %v (seed must reach the renderer)", err)
			}
			img, err := doc.RenderPage(0, core.RenderOptions{DPI: 36, Format: "png"})
			if err == nil {
				t.Fatalf("RenderPage returned %d bytes, want canvas-too-large error", len(img))
			}
		})
	}
}

// exercise runs the read-side pipeline on one input. It must never panic.
func exercise(data []byte) {
	doc, err := core.OpenBytes(data)
	if err != nil {
		return
	}
	_ = doc.GetPageCount()
	if _, err := doc.GetPage(0); err == nil {
		doc.ExtractTextByPage(0, core.ExtractOptions{})
		doc.RenderPage(0, core.RenderOptions{DPI: 36, Format: "png"})
	}
}

// FuzzOpenBytes fuzzes the parser + read-side pipeline (page tree, content-stream
// interpreter, extractor, rasterizer) on arbitrary bytes.
func FuzzOpenBytes(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		exercise(data)
	})
}

// FuzzOpenSaveReopen fuzzes the write path: any input that opens must serialize
// and re-open without panicking (the copy/writer guards bound hostile graphs).
func FuzzOpenSaveReopen(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := core.OpenBytes(data)
		if err != nil {
			return
		}
		var buf bytes.Buffer
		if err := doc.SaveTo(&buf); err != nil {
			return
		}
		// The re-opened document must also be safe to handle.
		exercise(buf.Bytes())
	})
}

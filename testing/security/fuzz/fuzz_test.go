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
	"compress/zlib"
	"errors"
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

// imagePDF builds a well-formed one-page PDF (100x100pt) whose page paints the
// image XObject /Im (object 5, dictionary entries imageDict and sample bytes
// data) over the whole page, so the seed reaches the image decoders. extra
// objects follow as 6, 7, … (e.g. an /SMask referenced as 6 0 R).
func imagePDF(imageDict string, data []byte, extra ...string) []byte {
	const content = "100 0 0 100 0 0 cm /Im Do"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image %s /Length %d >>\nstream\n%s\nendstream", imageDict, len(data), data),
	}
	return classicPDF(append(objs, extra...))
}

// flatePDF builds a one-page PDF whose content stream is Flate-encoded empty
// data with the given /DecodeParms, so the seed reaches the predictor.
func flatePDF(decodeParms string) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Close()
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		fmt.Sprintf("<< /Filter /FlateDecode /DecodeParms %s /Length %d >>\nstream\n%s\nendstream", decodeParms, z.Len(), z.Bytes()),
	})
}

// objStmPDF builds a PDF 1.5 file whose page (object 3) is compressed in an
// object stream (object 4) whose dictionary declares /N n, indexed by an xref
// stream (object 5) declaring /W w. The packed rows always use widths [1 4 2],
// so a hostile n or w reaches the ObjStm / xref-stream header checks on open.
func objStmPDF(n, w string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	off := make([]int, 6)
	for i, o := range []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
	} {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	const header = "3 0 "
	body := header + "<< /Type /Page /Parent 2 0 R >>"
	off[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Type /ObjStm /N %s /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		n, len(header), len(body), body)

	off[5] = b.Len()
	var rows []byte
	for i, typ := range []byte{0, 1, 1, 2, 1, 1} {
		f2, f3 := uint32(off[i]), uint16(0)
		switch {
		case i == 0:
			f3 = 65535
		case typ == 2:
			f2 = 4 // obj 3 lives in ObjStm 4 at index 0
		}
		rows = append(rows, typ, byte(f2>>24), byte(f2>>16), byte(f2>>8), byte(f2), byte(f3>>8), byte(f3))
	}
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XRef /Size 6 /Root 1 0 R /W %s /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		w, len(rows), rows)
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off[5])
	return b.Bytes()
}

// classicPDF serializes objs as objects 1..n with a classic xref table and a
// trailer whose /Root is object 1.
func classicPDF(objs []string) []byte {
	return classicPDFTrailer(objs, "")
}

// classicPDFTrailer is classicPDF with extra trailer entries (e.g. /Encrypt and
// /ID) appended inside the trailer dictionary.
func classicPDFTrailer(objs []string, trailerExtra string) []byte {
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, trailerExtra, x)
	return b.Bytes()
}

// seeds returns a spread of inputs: valid, empty, header-only, and various
// truncations/corruptions that should exercise the lenient recovery paths.
func seeds() [][]byte {
	full := []byte(minimalPDF)
	return append([][]byte{
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
		// Hostile image sizes: 2^32 x 2^32 wrapped the int64 area check to 0
		// and the decoder indexed an empty sample buffer (a panic on every
		// target); MaxInt64 overflowed the mask stride; a real 1e30 width
		// saturated to MaxInt64 under js/wasm. Also via an /SMask and an
		// inline image.
		imagePDF("/Width 4294967296 /Height 4294967296 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0, 0, 0, 0}),
		imagePDF("/Width 4294967296 /Height 4294967296 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{0, 0, 0, 0}),
		imagePDF("/Width 9223372036854775807 /Height 2 /ImageMask true", []byte{0, 0, 0, 0}),
		imagePDF("/Width "+bigReal+" /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8", []byte{0, 0}),
		imagePDF("/Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /SMask 6 0 R", []byte{0},
			"<< /Type /XObject /Subtype /Image /Width 4294967296 /Height 4294967296 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 4 >>\nstream\n\x00\x00\x00\x00\nendstream"),
		contentPDF("q 100 0 0 100 0 0 cm BI /W 4294967296 /H 4294967296 /CS /G /BPC 8 ID \x00\x00\x00\x00 EI Q"),
		// Hostile predictor parameters on an empty Flate stream: the row-length
		// product wrapped or drove a makeslice panic before any data was read.
		flatePDF("<< /Predictor 12 /Columns 4503599627370496 >>"),
		flatePDF("<< /Predictor 12 /Colors 3 /BitsPerComponent 4611686018427387904 /Columns 3 >>"),
		// /Rotate beyond the int range: amd64 and js/wasm used to disagree.
		classicPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R /Rotate " + bigReal + " >>",
		}),
		// Hostile header integers that sized an allocation or a slice on open
		// (a native crash from OpenBytes): ObjStm /N 2^60 reached makeslice;
		// xref-stream /W widths of 2^62 wrapped the row length negative.
		objStmPDF("1", "[1 4 2]"), // valid control: the page resolves via the ObjStm
		objStmPDF("1152921504606846976", "[1 4 2]"),
		objStmPDF("9223372036854775807", "[1 4 2]"),
		objStmPDF("1", "[4611686018427387904 4611686018427387904 4611686018427387904]"),
		objStmPDF("1", "[1 9223372036854775807 1]"),
		// A direct stream /Length of MaxInt64 wrapped start+length negative.
		classicPDF([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
			"<< /Length 9223372036854775807 >>\nstream\n0 0 m 10 10 l S\nendstream",
		}),
		// An /Encrypt key /Length of 256 bits sliced the 16-byte MD5 sum to [:32].
		classicPDFTrailer([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R >>",
			"<< /Filter /Standard /V 2 /R 3 /Length 256 /P -44 /O <" + strings.Repeat("AB", 32) + "> /U <" + strings.Repeat("CD", 32) + "> >>",
		}, "/Encrypt 4 0 R /ID [<00112233445566778899AABBCCDDEEFF> <00112233445566778899AABBCCDDEEFF>] "),
	}, deepNestingSeeds()...)
}

// deepNestingDepth is deep enough to cross the parser's nesting bound many
// times over while keeping the seeds small (the 3,000,000-level stack overflow
// they guard against is not something the fuzzer needs to rebuild each time).
const deepNestingDepth = 10_000

// deepNestingSeeds nest arrays and dictionaries far past the parser's bound in
// an object (reached on open), in a page content stream (the scanner calls the
// container parsers directly), and in an inline-image dictionary. Unbounded,
// the recursion overflowed the stack: a fatal error, not a recoverable panic.
func deepNestingSeeds() [][]byte {
	arrays := strings.Repeat("[", deepNestingDepth)
	dicts := strings.Repeat("<< /A ", deepNestingDepth)
	closed := arrays + strings.Repeat("]", deepNestingDepth)
	return [][]byte{
		deepCatalogPDF(arrays),
		deepCatalogPDF(dicts),
		contentPDF(closed + " 1 0 0 1 0 0 cm"),
		contentPDF(dicts + " 0 0 m 10 10 l S"),
		contentPDF("BT /F1 12 Tf " + closed + " TJ ET"),
		contentPDF("BI /W 1 /H 1 /D " + closed + " ID \x00 EI"),
	}
}

// deepCatalogPDF is a one-page PDF whose catalog carries /Deep deep, so the
// nesting is parsed while OpenBytes resolves the catalog.
func deepCatalogPDF(deep string) []byte {
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R /Deep " + deep + " >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	})
}

// TestDeepNestingSeedsHandled pins the deep-nesting seeds through the public
// API: a deep catalog is rejected as ErrInvalidPDF, and deep content-stream
// operands leave the page openable, extractable and renderable — never a
// stack overflow or a recovered panic.
func TestDeepNestingSeedsHandled(t *testing.T) {
	for _, deep := range []string{strings.Repeat("[", deepNestingDepth), strings.Repeat("<< /A ", deepNestingDepth)} {
		if _, err := core.OpenBytes(deepCatalogPDF(deep)); !errors.Is(err, core.ErrInvalidPDF) {
			t.Errorf("deep catalog: OpenBytes err = %v, want ErrInvalidPDF", err)
		}
	}
	for i, seed := range deepNestingSeeds()[2:] {
		doc, err := core.OpenBytes(seed)
		if err != nil {
			t.Fatalf("content seed %d: OpenBytes: %v", i, err)
		}
		_, err = doc.ExtractTextByPage(0, core.ExtractOptions{})
		failOnInternal(t, err)
		if _, err := doc.RenderPage(0, core.RenderOptions{DPI: 36, Format: "png"}); err != nil {
			t.Errorf("content seed %d: RenderPage: %v", i, err)
		}
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
			failOnInternal(t, err)
			if err == nil {
				t.Fatalf("RenderPage returned %d bytes, want canvas-too-large error", len(img))
			}
		})
	}
}

// TestHostileHeaderSeedsReturnError pins the header-integer seeds: the valid
// ObjStm control must open (so the builder really reaches the ObjStm path), and
// each hostile variant must come back from OpenBytes as ErrInvalidPDF (or
// ErrEncryptedDocument for the /Encrypt key length), never as a crash.
func TestHostileHeaderSeedsReturnError(t *testing.T) {
	doc, err := core.OpenBytes(objStmPDF("1", "[1 4 2]"))
	if err != nil {
		t.Fatalf("valid ObjStm control: OpenBytes: %v", err)
	}
	if doc.GetPageCount() != 1 {
		t.Fatalf("valid ObjStm control: %d pages, want 1", doc.GetPageCount())
	}

	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"ObjStm /N 2^60", objStmPDF("1152921504606846976", "[1 4 2]"), core.ErrInvalidPDF},
		{"ObjStm /N MaxInt64", objStmPDF("9223372036854775807", "[1 4 2]"), core.ErrInvalidPDF},
		{"xref /W 3x2^62", objStmPDF("1", "[4611686018427387904 4611686018427387904 4611686018427387904]"), core.ErrInvalidPDF},
		{"xref /W MaxInt64 middle", objStmPDF("1", "[1 9223372036854775807 1]"), core.ErrInvalidPDF},
		{"Encrypt /Length 256", classicPDFTrailer([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R >>",
			"<< /Filter /Standard /V 2 /R 3 /Length 256 /P -44 /O <" + strings.Repeat("AB", 32) + "> /U <" + strings.Repeat("CD", 32) + "> >>",
		}, "/Encrypt 4 0 R /ID [<00112233445566778899AABBCCDDEEFF> <00112233445566778899AABBCCDDEEFF>] "),
			core.ErrEncryptedDocument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := core.OpenBytes(tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenBytes err = %v, want %v", err, tc.want)
			}
		})
	}
}

// failOnInternal fails the test, with the recovered stack, when err is
// ErrInternal. The public API turns a panic into ErrInternal instead of
// crashing, so to the harness that error IS the crash: recover-to-error must
// never hide a parser/renderer panic from the fuzzer.
func failOnInternal(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, core.ErrInternal) {
		var stack []byte
		var ie *core.InternalError
		if errors.As(err, &ie) {
			stack = ie.Stack
		}
		t.Fatalf("recovered panic (a bug, not an input error): %v\n%s", err, stack)
	}
}

// exercise runs the read-side pipeline on one input. Each step may reject the
// input with an error, but must never panic — including a panic recovered into
// ErrInternal.
func exercise(t *testing.T, data []byte) {
	t.Helper()
	doc, err := core.OpenBytes(data)
	failOnInternal(t, err)
	if err != nil {
		return
	}
	_ = doc.GetPageCount()
	if _, err := doc.GetPage(0); err == nil {
		_, err = doc.ExtractTextByPage(0, core.ExtractOptions{})
		failOnInternal(t, err)
		_, err = doc.RenderPage(0, core.RenderOptions{DPI: 36, Format: "png"})
		failOnInternal(t, err)
	}
}

// FuzzOpenBytes fuzzes the parser + read-side pipeline (page tree, content-stream
// interpreter, extractor, rasterizer) on arbitrary bytes.
func FuzzOpenBytes(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		exercise(t, data)
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
		failOnInternal(t, err)
		if err != nil {
			return
		}
		var buf bytes.Buffer
		if err := doc.SaveTo(&buf); err != nil {
			failOnInternal(t, err)
			return
		}
		// The re-opened document must also be safe to handle.
		exercise(t, buf.Bytes())
	})
}

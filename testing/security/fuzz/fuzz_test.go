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

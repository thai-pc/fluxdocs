// Package corpus runs the whole read→render→save→reopen pipeline over the
// synthetic PDF corpus (generate.go). Clean files must round-trip; malformed
// files must be handled leniently — parsed or rejected, but never a panic or a
// hang (the parser/render DoS guards bound the work). Regenerate the corpus with
// `go run testing/corpus/generate.go`.
package corpus

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

func pdfFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".pdf" {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatalf("no PDFs in %s (run: go run testing/corpus/generate.go)", dir)
	}
	return out
}

// TestCorpusClean opens every clean PDF, extracts text, renders page 0, then
// saves and re-opens it, asserting the page count is preserved through the
// writer round-trip.
func TestCorpusClean(t *testing.T) {
	for _, path := range pdfFiles(t, "clean") {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := core.OpenBytes(data)
			if err != nil {
				t.Fatalf("OpenBytes: %v", err)
			}
			if doc.PageCount < 1 {
				t.Fatalf("PageCount = %d, want >= 1", doc.PageCount)
			}
			if _, err := doc.ExtractText(core.ExtractOptions{}); err != nil {
				t.Errorf("ExtractText: %v", err)
			}
			if _, err := doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"}); err != nil {
				t.Errorf("RenderPage: %v", err)
			}

			// Writer round-trip: save → reopen → page count stable.
			var buf bytes.Buffer
			if err := doc.SaveTo(&buf); err != nil {
				t.Fatalf("SaveTo: %v", err)
			}
			rt, err := core.OpenBytes(buf.Bytes())
			if err != nil {
				t.Fatalf("reopen saved bytes: %v", err)
			}
			if rt.PageCount != doc.PageCount {
				t.Errorf("round-trip PageCount = %d, want %d", rt.PageCount, doc.PageCount)
			}
			// Text should survive the round-trip (the writer copies content verbatim).
			before, _ := doc.ExtractText(core.ExtractOptions{})
			after, _ := rt.ExtractText(core.ExtractOptions{})
			if before != after {
				t.Errorf("round-trip text changed:\n before=%q\n after =%q", before, after)
			}
		})
	}
}

// TestCorpusMalformed feeds each malformed PDF through the full pipeline. The
// only requirement is robustness: OpenBytes may return an error or a document,
// but nothing may panic or hang. If a document opens, every accessor is
// exercised to reach the lenient-parsing code paths. The test completing is the
// pass signal (a panic fails it; a hang trips the go-test timeout).
func TestCorpusMalformed(t *testing.T) {
	for _, path := range pdfFiles(t, "malformed") {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// The API recovers a panic into ErrInternal; that error is the panic
			// this test exists to catch, never a clean rejection.
			noPanic := func(err error) {
				t.Helper()
				var ie *core.InternalError
				if errors.As(err, &ie) {
					t.Fatalf("recovered panic: %v\n%s", err, ie.Stack)
				}
			}
			doc, err := core.OpenBytes(data)
			noPanic(err)
			if err != nil {
				return // rejected cleanly — acceptable
			}
			// Opened leniently: exercise the accessors; none may panic.
			n := doc.GetPageCount()
			for i := 0; i < n && i < 8; i++ {
				doc.GetPage(i)
				_, err = doc.ExtractTextByPage(i, core.ExtractOptions{})
				noPanic(err)
				_, err = doc.RenderPage(i, core.RenderOptions{DPI: 36, Format: "png"})
				noPanic(err)
			}
			_, err = doc.ExtractText(core.ExtractOptions{})
			noPanic(err)
		})
	}
}

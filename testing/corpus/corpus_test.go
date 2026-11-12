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
	"strings"
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

// malformedOutcome is what opening one malformed corpus file must give
// (docs/design/xref-reconstruction.md §1.1): whether it opens, its page count,
// how it was repaired ("" for a strict open), whether its text holds the
// corpus sample, and whether a repaired one saves.
type malformedOutcome struct {
	opens  bool
	pages  int
	method core.RepairMethod
	text   bool
	saves  bool
}

// malformedOutcomes pins every file in malformed/. truncated.pdf keeps its
// catalog, page tree and page, but its content stream runs past the end of the
// file, so its text does not load and SaveTo, which copies it, fails (as for
// any such stream, repaired or not).
var malformedOutcomes = map[string]malformedOutcome{
	"bad_xref.pdf":       {true, 1, core.RepairObjectScan, true, true},
	"cyclic_pages.pdf":   {true, 0, "", false, true},
	"junk_prefix.pdf":    {true, 1, core.RepairHeaderOffset, true, true},
	"missing_endobj.pdf": {true, 1, core.RepairObjectScan, true, true},
	"no_trailer.pdf":     {true, 1, core.RepairObjectScan, true, true},
	"truncated.pdf":      {true, 1, core.RepairObjectScan, false, false},
}

// TestCorpusMalformed feeds each malformed PDF through the full pipeline.
// Each must give its malformedOutcomes row — accepting any clean rejection
// hid that five of six files did not open — and nothing may panic or hang.
// If a document opens, every accessor is exercised to reach the
// lenient-parsing code paths, and a repaired one that saves must save to a
// file that opens without repair.
func TestCorpusMalformed(t *testing.T) {
	files := pdfFiles(t, "malformed")
	if len(files) != len(malformedOutcomes) {
		t.Errorf("%d malformed files, %d expected outcomes: pin every file", len(files), len(malformedOutcomes))
	}
	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			want, known := malformedOutcomes[filepath.Base(path)]
			if !known {
				t.Fatalf("no expected outcome for %s", path)
			}
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
			if (err == nil) != want.opens {
				t.Fatalf("OpenBytes err = %v, want opens=%v", err, want.opens)
			}
			if err != nil {
				return // rejected cleanly, as expected
			}
			rep, _ := doc.RepairReport()
			if doc.PageCount != want.pages || rep.Method != want.method {
				t.Errorf("%d pages, repair %q; want %d pages, repair %q", doc.PageCount, rep.Method, want.pages, want.method)
			}
			text, _ := doc.ExtractText(core.ExtractOptions{})
			if strings.Contains(text, "Corpus text sample") != want.text {
				t.Errorf("text %q: contains the corpus sample = %v, want %v", text, !want.text, want.text)
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
			if want.method != "" {
				var buf bytes.Buffer
				err := doc.SaveTo(&buf)
				noPanic(err)
				if (err == nil) != want.saves {
					t.Fatalf("SaveTo err = %v, want saves=%v", err, want.saves)
				}
				if err != nil {
					return
				}
				out, err := core.OpenBytes(buf.Bytes())
				if err != nil {
					t.Fatalf("re-open the saved repair: %v", err)
				}
				if _, again := out.RepairReport(); again || out.PageCount != doc.PageCount {
					t.Errorf("saved repair: repaired %v, %d pages; want a strict open of %d", again, out.PageCount, doc.PageCount)
				}
			}
		})
	}
}

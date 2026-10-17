// Command cli-batch-redact-demo redacts a regex pattern from every PDF in an
// input directory and writes the redacted copies to an output directory. It
// shows how to drive the FluxDocs core for a batch job: open, mark by pattern,
// permanently remove, and save — entirely locally, no network.
//
//	go run ./examples/cli-batch-redact-demo -in ./pdfs -out ./redacted -pattern '\d{3}-\d{2}-\d{4}'
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thai-pc/fluxdocs/core"
)

func main() {
	in := flag.String("in", ".", "input directory of PDFs")
	out := flag.String("out", "./redacted", "output directory")
	pattern := flag.String("pattern", "", "regex to redact (e.g. an SSN or card number)")
	flag.Parse()

	if *pattern == "" {
		fmt.Fprintln(os.Stderr, "error: -pattern is required")
		os.Exit(2)
	}
	results, err := BatchRedact(*in, *out, *pattern)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	for _, r := range results {
		var refused *core.RedactionError
		if errors.As(r.Err, &refused) {
			// Redaction fails closed on content it cannot remove safely; the
			// reason code says why (no document text is in the error).
			where := fmt.Sprintf("on page %d", refused.Page)
			if refused.Page < 0 {
				where = "for the document"
			}
			fmt.Printf("%s: REFUSED %s (%s)\n", r.Name, where, refused.Reason)
			continue
		}
		if r.Err != nil {
			fmt.Printf("%s: FAILED (%v)\n", r.Name, r.Err)
			continue
		}
		fmt.Printf("%s: redacted %d region(s) -> %s\n", r.Name, r.Regions, r.Out)
	}
}

// Result reports the outcome of redacting one file.
type Result struct {
	Name    string
	Out     string
	Regions int
	Err     error
}

// BatchRedact redacts `pattern` from every *.pdf in inDir, writing the results
// to outDir (created if needed). It returns one Result per input file; a
// per-file failure does not stop the batch.
func BatchRedact(inDir, outDir, pattern string) ([]Result, error) {
	entries, err := os.ReadDir(inDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	var results []Result
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pdf") {
			continue
		}
		res := Result{Name: e.Name()}
		res.Regions, res.Out, res.Err = redactFile(filepath.Join(inDir, e.Name()), outDir, pattern)
		results = append(results, res)
	}
	return results, nil
}

// redactFile redacts pattern from one PDF and writes it under outDir.
func redactFile(path, outDir, pattern string) (regions int, outPath string, err error) {
	doc, err := core.OpenDocument(path)
	if err != nil {
		return 0, "", err
	}
	defer doc.Close()

	for p := 0; p < doc.PageCount; p++ {
		rects, err := doc.RedactText(p, pattern)
		if err != nil {
			return 0, "", err
		}
		regions += len(rects)
	}
	redacted, err := doc.RedactAndFlatten()
	if err != nil {
		return 0, "", err
	}
	outPath = filepath.Join(outDir, filepath.Base(path))
	if err := redacted.Save(outPath); err != nil {
		return 0, "", err
	}
	return regions, outPath, nil
}

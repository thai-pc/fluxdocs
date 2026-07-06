package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// secretPDF builds a one-page PDF containing a secret and some public text.
func secretPDF() []byte {
	var b bytes.Buffer
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
	}
	content := "BT /F1 12 Tf 100 700 Td (SSN 123-45-6789) Tj 0 -100 Td (public notice) Tj ET"
	objs[4] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	objs[5] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

	b.WriteString("%PDF-1.7\n")
	off := map[int]int{}
	for i := 1; i <= 5; i++ {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

func TestBatchRedact(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	// Two PDFs to redact + one non-PDF to skip.
	os.WriteFile(filepath.Join(in, "a.pdf"), secretPDF(), 0o644)
	os.WriteFile(filepath.Join(in, "b.pdf"), secretPDF(), 0o644)
	os.WriteFile(filepath.Join(in, "notes.txt"), []byte("ignore me"), 0o644)

	results, err := BatchRedact(in, out, `\d{3}-\d{2}-\d{4}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("processed %d files, want 2 (the .txt should be skipped)", len(results))
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s failed: %v", r.Name, r.Err)
			continue
		}
		if r.Regions == 0 {
			t.Errorf("%s: no regions redacted, expected the SSN to match", r.Name)
		}
		// The output must not leak the SSN.
		doc, err := core.OpenDocument(r.Out)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := doc.ExtractText(core.ExtractOptions{})
		if strings.Contains(text, "123-45-6789") {
			t.Errorf("LEAK: %s still contains the SSN: %q", r.Name, text)
		}
		if !strings.Contains(text, "public notice") {
			t.Errorf("%s: public text was over-redacted: %q", r.Name, text)
		}
	}
}

func TestBatchRedact_MissingInputDir(t *testing.T) {
	if _, err := BatchRedact("/no/such/dir", t.TempDir(), "x"); err == nil {
		t.Errorf("expected an error for a missing input directory")
	}
}

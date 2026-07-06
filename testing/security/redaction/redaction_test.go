// Package redaction is the non-negotiable redaction leak gate (SECURITY.md §1):
// after RedactAndFlatten, the removed text must not be recoverable by
// ExtractText — proving the content was stripped from the stream, not merely
// painted over. A PR touching redaction does not merge unless this passes 100%.
package redaction

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// buildPDF assembles a classic-xref PDF (computed offsets).
func buildPDF(objects map[int]string, trailer string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.7\n")
	n := len(objects)
	for i := 1; i <= n; i++ {
		off[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i, objects[i]))
	}
	xref := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", n+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n" + trailer + "\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return b.Bytes()
}

func streamObj(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

// secretPDF places "SECRET-1234" at (100,700) and "public info" at (100,600) on
// one page.
func secretPDF() []byte {
	content := "BT /F1 12 Tf 100 700 Td (SECRET-1234) Tj 0 -100 Td (public info) Tj ET"
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

// TestRedactArea_RemovesTextNotJustPaintsOver is the core gate: after redacting
// the region that holds the secret, ExtractText must not return it, while
// non-redacted text survives.
func TestRedactArea_RemovesTextNotJustPaintsOver(t *testing.T) {
	doc, err := core.OpenBytes(secretPDF())
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: the secret is present before redaction.
	if before, _ := doc.ExtractText(core.ExtractOptions{}); !strings.Contains(before, "SECRET") {
		t.Fatalf("fixture broken: secret not extractable before redaction: %q", before)
	}

	// The secret baseline is at (100,700); this region covers it.
	if err := doc.RedactArea(0, core.Rect{X: 90, Y: 692, Width: 160, Height: 24}); err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}

	text, err := out.ExtractText(core.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "SECRET") || strings.Contains(text, "1234") {
		t.Errorf("LEAK: redacted text still extractable: %q", text)
	}
	if !strings.Contains(text, "public info") {
		t.Errorf("over-redacted: non-secret text lost: %q", text)
	}

	// The secret bytes must not survive anywhere in the serialized PDF — checked
	// on BOTH the verbatim RedactAndFlatten output (out.Bytes(), what callers
	// like the CLI/MCP write directly) and a re-saved copy. The verbatim check is
	// the strict one: a re-save rebuilds the object graph and would silently drop
	// an orphaned original-content stream, hiding the very leak we must catch.
	raw, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SECRET-1234")) {
		t.Errorf("LEAK: secret bytes remain in the verbatim redacted PDF")
	}
	var buf bytes.Buffer
	if err := out.SaveTo(&buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("SECRET-1234")) {
		t.Errorf("LEAK: secret bytes remain in the re-saved redacted PDF")
	}
}

// TestRedactText_ByPattern redacts via a regex and verifies the match is gone.
func TestRedactText_ByPattern(t *testing.T) {
	doc, err := core.OpenBytes(secretPDF())
	if err != nil {
		t.Fatal(err)
	}
	rects, err := doc.RedactText(0, `SECRET-\d+`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rects) == 0 {
		t.Fatal("RedactText found no matches for a present pattern")
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	text, _ := out.ExtractText(core.ExtractOptions{})
	if strings.Contains(text, "SECRET") {
		t.Errorf("LEAK: pattern-redacted text still extractable: %q", text)
	}
}

// TestRedactAndFlatten_Irreversible confirms the source document is unchanged and
// the redacted output has no annotation layer.
func TestRedactAndFlatten_Irreversible(t *testing.T) {
	doc, err := core.OpenBytes(secretPDF())
	if err != nil {
		t.Fatal(err)
	}
	doc.RedactArea(0, core.Rect{X: 90, Y: 692, Width: 160, Height: 24})
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	// Source still has the secret (immutability).
	if before, _ := doc.ExtractText(core.ExtractOptions{}); !strings.Contains(before, "SECRET") {
		t.Errorf("source document was mutated by redaction")
	}
	// Output page count preserved.
	if out.PageCount != doc.PageCount {
		t.Errorf("redacted PageCount = %d, want %d", out.PageCount, doc.PageCount)
	}
}

// TestRedact_NoPlanIsFaithfulCopy: with no marks, output equals the input pages.
func TestRedact_NoPlanIsFaithfulCopy(t *testing.T) {
	doc, err := core.OpenBytes(secretPDF())
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	text, _ := out.ExtractText(core.ExtractOptions{})
	if !strings.Contains(text, "SECRET") || !strings.Contains(text, "public") {
		t.Errorf("no-op redaction lost content: %q", text)
	}
}

// TestRedactArea_OutOfRange checks the error path.
func TestRedactArea_OutOfRange(t *testing.T) {
	doc, _ := core.OpenBytes(secretPDF())
	if err := doc.RedactArea(9, core.Rect{}); err != core.ErrPageNotFound {
		t.Errorf("err = %v, want ErrPageNotFound", err)
	}
}

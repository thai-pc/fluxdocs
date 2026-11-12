package redaction

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// Leak-corpus rows for a repaired source (docs/design/xref-reconstruction.md
// §5): a file with no cross-reference data, opened by the object scan, that
// holds the secret where the scan's table does not lead — a shadowed earlier
// definition of the page's content stream, an "N G obj" header inside stream
// data, and an object nothing references — besides the page's visible text.
// Redacting the visible text must leave the secret nowhere in the output.

// repairedSourcePDF is the one-page fixture of onePage (catalog 1, pages 2,
// page 3, content 4, Helvetica 5), written with no xref, trailer or
// startxref, after the three hidden copies: object 4's earlier definition,
// stream 7 whose data holds "8 0 obj (secret) endobj", and the unreferenced
// object 9.
func repairedSourcePDF() []byte {
	masked := "8 0 obj\n" + S + "\nendobj\n"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	for _, o := range []struct {
		num  int
		body string
	}{
		{4, stm("", show(S)+" BT /F1 24 Tf 72 600 Td (shadowed copy) Tj ET")},
		{7, stm("", masked)},
		{9, S},
		{1, "<< /Type /Catalog /Pages 2 0 R >>"},
		{2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>"},
		{3, "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>"},
		{4, stm("", show(S))},
		{5, helvetica},
	} {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", o.num, o.body)
	}
	return b.Bytes()
}

// repairCases are the repaired-source rows of the leak corpus.
func repairCases() []leakCase {
	return []leakCase{
		{name: "repaired/hidden copies outside the table", pdf: repairedSourcePDF, ops: []leakOp{opText, opArea}, want: clean},
	}
}

// TestRepairedSourceFixture: the fixture is what its rows need — it opens by
// the object scan, with one shadowed definition, and every hidden copy of the
// secret is in its bytes but not in what the document shows.
func TestRepairedSourceFixture(t *testing.T) {
	data := repairedSourcePDF()
	doc, err := core.OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := doc.RepairReport()
	if !ok || rep.Method != core.RepairObjectScan || rep.Duplicates != 1 {
		t.Fatalf("RepairReport = %+v, %v; want an object scan with one shadowed definition", rep, ok)
	}
	if n := bytes.Count(data, []byte(secret)); n != 4 {
		t.Fatalf("fixture holds the secret %d times, want 4 (visible, shadowed, masked, unreferenced)", n)
	}
	text, err := doc.ExtractText(core.ExtractOptions{})
	if err != nil || !containsNeedle(text, secret) || containsNeedle(text, "shadowed copy") {
		t.Fatalf("ExtractText = %q, %v; want the visible secret from the winning definition only", text, err)
	}
}

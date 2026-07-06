package core

import (
	"bytes"
	"fmt"
	"testing"
)

// piiPDF builds a one-page PDF whose text contains PII plus a public line.
func piiPDF(t *testing.T) []byte {
	t.Helper()
	// Two text-showing operators: one with PII, one public.
	content := "BT /F1 12 Tf 72 700 Td (SSN 123-45-6789 card 4111 1111 1111 1111) Tj " +
		"0 -20 Td (Public quarterly summary) Tj ET"
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
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

func TestAutoRedactPII_RemovesPIIKeepsPublic(t *testing.T) {
	doc, err := OpenBytes(piiPDF(t))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	counts, err := doc.AutoRedactPII(PIISSN, PIICreditCard)
	if err != nil {
		t.Fatalf("AutoRedactPII: %v", err)
	}
	if counts[PIISSN] < 1 || counts[PIICreditCard] < 1 {
		t.Fatalf("counts = %v, want at least one SSN and one card region", counts)
	}

	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}

	// Leak gate (SECURITY.md §1): the PII must be gone from the extracted text
	// AND the raw bytes — not merely painted over.
	text, err := out.ExtractText(ExtractOptions{})
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	for _, secret := range []string{"123-45-6789", "4111 1111 1111 1111"} {
		if bytes.Contains([]byte(text), []byte(secret)) {
			t.Errorf("LEAK: %q survives in extracted text", secret)
		}
	}
	raw, err := out.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if bytes.Contains(raw, []byte("123-45-6789")) {
		t.Errorf("LEAK: SSN survives in the redacted PDF bytes")
	}
	// The public content must survive.
	if !bytes.Contains([]byte(text), []byte("Public quarterly summary")) {
		t.Errorf("over-redaction: public text was removed (%q)", text)
	}
}

func TestAutoRedactPII_RequiresBytes(t *testing.T) {
	// A Document not opened from bytes has no resolver.
	doc := &Document{Pages: []Page{{Index: 0}}}
	if _, err := doc.AutoRedactPII(); err != ErrInvalidPDF {
		t.Fatalf("err = %v, want ErrInvalidPDF", err)
	}
}

package core

import (
	"fmt"
	"testing"
)

// coreStreamObj builds an uncompressed content-stream object body.
func coreStreamObj(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func extractTestDoc(t *testing.T) *Document {
	t.Helper()
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>",
		4: coreStreamObj("BT /F1 12 Tf 72 720 Td (Page one) Tj ET"),
		5: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 7 0 R >>",
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		7: coreStreamObj("BT /F1 12 Tf 72 720 Td (Page two) Tj ET"),
	}, "<< /Size 8 /Root 1 0 R >>")

	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func TestExtractTextByPage(t *testing.T) {
	doc := extractTestDoc(t)

	got, err := doc.ExtractTextByPage(0, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Page one" {
		t.Errorf("page 0 text = %q, want %q", got, "Page one")
	}

	got, err = doc.ExtractTextByPage(1, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Page two" {
		t.Errorf("page 1 text = %q, want %q", got, "Page two")
	}
}

func TestExtractTextByPage_OutOfRange(t *testing.T) {
	doc := extractTestDoc(t)
	if _, err := doc.ExtractTextByPage(5, ExtractOptions{}); err != ErrPageNotFound {
		t.Errorf("err = %v, want ErrPageNotFound", err)
	}
}

func TestExtractText_AllPagesFormFeedSeparated(t *testing.T) {
	doc := extractTestDoc(t)
	got, err := doc.ExtractText(ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "Page one\fPage two"
	if got != want {
		t.Errorf("ExtractText = %q, want %q", got, want)
	}
}

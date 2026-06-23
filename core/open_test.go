package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildCorePDF assembles a PDF from indirect-object bodies (object numbers
// 1-based and contiguous) plus a trailer body, with a classic xref table and
// dynamically computed offsets. Mirrors the parse package's test builder.
func buildCorePDF(objects map[int]string, trailer string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.7\n")

	n := len(objects)
	for i := 1; i <= n; i++ {
		off[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i, objects[i]))
	}

	xrefStart := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", n+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n" + trailer + "\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xrefStart))
	return b.Bytes()
}

func minimalDocBytes() []byte {
	return buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Title (Contract) /Author (Legal Team) /Keywords (a, b ,c) >>",
	}, "<< /Size 5 /Root 1 0 R /Info 4 0 R >>")
}

func TestOpenBytes_Minimal(t *testing.T) {
	doc, err := OpenBytes(minimalDocBytes())
	if err != nil {
		t.Fatal(err)
	}
	if doc.PageCount != 1 || len(doc.Pages) != 1 {
		t.Fatalf("PageCount = %d, len(Pages) = %d, want 1/1", doc.PageCount, len(doc.Pages))
	}
	p := doc.Pages[0]
	if p.Width != 612 || p.Height != 792 {
		t.Errorf("page size = %.0fx%.0f, want 612x792", p.Width, p.Height)
	}
	if p.Index != 0 || p.ID != "page-0" {
		t.Errorf("page identity = %d/%q, want 0/page-0", p.Index, p.ID)
	}
	if doc.Title != "Contract" {
		t.Errorf("Title = %q, want Contract", doc.Title)
	}
	if doc.Metadata.Author != "Legal Team" {
		t.Errorf("Author = %q, want Legal Team", doc.Metadata.Author)
	}
	if got := doc.Metadata.Keywords; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Keywords = %q, want [a b c]", got)
	}
}

func TestOpenBytes_InvalidHeader(t *testing.T) {
	if _, err := OpenBytes([]byte("not a pdf at all")); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("err = %v, want ErrInvalidPDF", err)
	}
}

func TestOpenBytes_Encrypted(t *testing.T) {
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R /Encrypt 9 0 R >>")

	if _, err := OpenBytes(pdf); !errors.Is(err, ErrEncryptedDocument) {
		t.Errorf("err = %v, want ErrEncryptedDocument", err)
	}
}

func TestOpenBytes_UTF16Title(t *testing.T) {
	// /Title as a UTF-16BE hex string with a BOM: FEFF 0048 0069 = "Hi".
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Title <FEFF00480069> >>",
	}, "<< /Size 5 /Root 1 0 R /Info 4 0 R >>")

	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Hi" {
		t.Errorf("Title = %q, want Hi (UTF-16BE decode)", doc.Title)
	}
}

func TestOpenDocument_FileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "min.pdf")
	if err := os.WriteFile(path, minimalDocBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != DocumentID(path) {
		t.Errorf("ID = %q, want %q", doc.ID, path)
	}
	if doc.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", doc.PageCount)
	}
}

func TestOpenDocument_MissingFile(t *testing.T) {
	_, err := OpenDocument(filepath.Join(t.TempDir(), "nope.pdf"))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want a not-exist error", err)
	}
}

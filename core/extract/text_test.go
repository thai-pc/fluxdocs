package extract

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// buildPDF assembles a PDF from indirect-object bodies (keyed 1-based) plus a
// trailer, writing a classic xref with computed offsets. Mirrors the helper in
// the parse tests; duplicated because test helpers are not exported.
func buildPDF(objects map[int]string, trailer string) []byte {
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

func streamObj(dictExtra, content string) string {
	return fmt.Sprintf("<< /Length %d%s >>\nstream\n%s\nendstream", len(content), dictExtra, content)
}

// firstPageText builds a resolver from pdf and extracts page 0's text.
func firstPageText(t *testing.T, pdf []byte) string {
	t.Helper()
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no pages")
	}
	got, err := PageText(r, pages[0], Options{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPageText_SimpleFontLatin1(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		5: streamObj("", "BT /F1 12 Tf 72 720 Td (Hello World) Tj ET"),
	}, "<< /Size 6 /Root 1 0 R >>")

	if got := firstPageText(t, pdf); got != "Hello World" {
		t.Errorf("PageText = %q, want %q", got, "Hello World")
	}
}

func TestPageText_LineBreaks(t *testing.T) {
	// Two lines positioned with T* (leading 14). Expect a newline between them.
	content := "BT /F1 12 Tf 14 TL 72 720 Td (Line one) Tj T* (Line two) Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		5: streamObj("", content),
	}, "<< /Size 6 /Root 1 0 R >>")

	if got := firstPageText(t, pdf); got != "Line one\nLine two" {
		t.Errorf("PageText = %q, want %q", got, "Line one\nLine two")
	}
}

func TestPageText_TJWordSpacing(t *testing.T) {
	// TJ with a large negative adjustment between two runs -> a space.
	content := "BT /F1 12 Tf 72 720 Td [(Hello) -300 (World)] TJ ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		5: streamObj("", content),
	}, "<< /Size 6 /Root 1 0 R >>")

	if got := firstPageText(t, pdf); got != "Hello World" {
		t.Errorf("PageText = %q, want %q", got, "Hello World")
	}
}

func TestPageText_Type0WithToUnicode(t *testing.T) {
	cmapContent := "1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
		"2 beginbfchar\n<0001> <0048>\n<0002> <0069>\nendbfchar\n"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 6 0 R >>",
		5: streamObj("", "BT /F1 12 Tf 72 720 Td <00010002> Tj ET"),
		6: streamObj("", cmapContent),
	}, "<< /Size 7 /Root 1 0 R >>")

	if got := firstPageText(t, pdf); got != "Hi" {
		t.Errorf("PageText = %q, want %q", got, "Hi")
	}
}

func TestPageText_NoContents(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R >>")

	if got := firstPageText(t, pdf); got != "" {
		t.Errorf("PageText = %q, want empty", got)
	}
}

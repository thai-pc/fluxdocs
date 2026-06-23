package parse

import (
	"bytes"
	"fmt"
	"testing"
)

// buildMinimalPDF builds a minimal valid PDF (catalog -> pages -> page + one
// content stream) with a classic xref table; offsets are computed dynamically
// so they never drift.
func buildMinimalPDF() []byte {
	var b bytes.Buffer
	off := map[int]int{}

	b.WriteString("%PDF-1.7\n")

	off[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	off[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	off[3] = b.Len()
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>\nendobj\n")

	content := "BT /F1 12 Tf (Hello) Tj ET"
	off[4] = b.Len()
	b.WriteString(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content))

	xrefStart := b.Len()
	b.WriteString("xref\n")
	b.WriteString("0 5\n")
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 4; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xrefStart))

	return b.Bytes()
}

func TestFindStartXref(t *testing.T) {
	pdf := buildMinimalPDF()
	got, err := FindStartXref(pdf)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Index(pdf, []byte("xref\n0 5"))
	if got != want {
		t.Errorf("FindStartXref = %d, want %d", got, want)
	}
}

func TestParseXref(t *testing.T) {
	x, err := ParseXref(buildMinimalPDF())
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Entries) != 5 {
		t.Errorf("len(Entries) = %d, want 5", len(x.Entries))
	}
	if e := x.Entries[0]; !e.Free {
		t.Error("entry 0 must be free")
	}
	if e := x.Entries[1]; e.Free || e.Offset <= 0 {
		t.Errorf("entry 1 = %+v, want in-use with offset > 0", e)
	}
	if size, ok := x.Trailer.GetInt("Size"); !ok || size != 5 {
		t.Errorf("trailer /Size = %d,%v want 5,true", size, ok)
	}
	if _, ok := x.Trailer.GetReference("Root"); !ok {
		t.Error("trailer /Root must be a reference")
	}
}

func TestResolverCatalogAndPageTree(t *testing.T) {
	r, err := NewResolver(buildMinimalPDF())
	if err != nil {
		t.Fatal(err)
	}

	cat, err := r.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if typ, _ := cat.GetName("Type"); typ != "Catalog" {
		t.Errorf("catalog /Type = %q, want Catalog", typ)
	}

	// Catalog -> Pages -> Kids[0] -> Page
	pagesRef := cat["Pages"]
	pages, ok, err := r.ResolveDict(pagesRef)
	if err != nil || !ok {
		t.Fatalf("resolve Pages: ok=%v err=%v", ok, err)
	}
	if cnt, _ := pages.GetInt("Count"); cnt != 1 {
		t.Errorf("Pages /Count = %d, want 1", cnt)
	}

	kids, _ := pages.GetArray("Kids")
	if len(kids) != 1 {
		t.Fatalf("len(Kids) = %d, want 1", len(kids))
	}
	page, ok, err := r.ResolveDict(kids[0])
	if err != nil || !ok {
		t.Fatalf("resolve Page: ok=%v err=%v", ok, err)
	}
	if typ, _ := page.GetName("Type"); typ != "Page" {
		t.Errorf("page /Type = %q, want Page", typ)
	}
	mb, _ := page.GetArray("MediaBox")
	if len(mb) != 4 || mb[2] != Integer(612) || mb[3] != Integer(792) {
		t.Errorf("MediaBox = %v, want [0 0 612 792]", mb)
	}
}

func TestResolverStreamContent(t *testing.T) {
	r, err := NewResolver(buildMinimalPDF())
	if err != nil {
		t.Fatal(err)
	}
	// Object 4 is the content stream.
	obj, err := r.object(4)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := obj.(*Stream)
	if !ok {
		t.Fatalf("object 4 = %T, want *Stream", obj)
	}
	want := "BT /F1 12 Tf (Hello) Tj ET"
	if string(st.Raw) != want {
		t.Errorf("stream raw = %q, want %q", st.Raw, want)
	}
	if length, _ := st.Dict.GetInt("Length"); int(length) != len(want) {
		t.Errorf("/Length = %d, want %d", length, len(want))
	}
}

func TestParseIndirectObject_Stream_LengthFallback(t *testing.T) {
	// /Length is deliberately WRONG (0) -> must fall back to scanning 'endstream'.
	content := "raw stream bytes here"
	src := fmt.Sprintf("9 0 obj\n<< /Length 0 >>\nstream\n%s\nendstream\nendobj\n", content)
	num, gen, obj, _, err := ParseIndirectObjectAt([]byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	if num != 9 || gen != 0 {
		t.Errorf("num,gen = %d,%d want 9,0", num, gen)
	}
	st, ok := obj.(*Stream)
	if !ok {
		t.Fatalf("obj = %T, want *Stream", obj)
	}
	if string(st.Raw) != content {
		t.Errorf("raw = %q, want %q (endstream-scan fallback)", st.Raw, content)
	}
}

func TestParseXref_MissingStartxref(t *testing.T) {
	if _, err := ParseXref([]byte("%PDF-1.7\nrubbish")); err == nil {
		t.Error("ParseXref without startxref must error")
	}
}

package parse

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"testing"
)

// beBytes encodes v as n big-endian bytes.
func beBytes(v int64, n int) []byte {
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

// packXref packs rows of [type, field2, field3] using the given field widths.
func packXref(rows [][3]int64, w [3]int) []byte {
	var b []byte
	for _, r := range rows {
		b = append(b, beBytes(r[0], w[0])...)
		b = append(b, beBytes(r[1], w[1])...)
		b = append(b, beBytes(r[2], w[2])...)
	}
	return b
}

func zlibBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// writeStreamObj writes "n 0 obj <dict> stream\n<raw>\nendstream endobj\n" and
// returns the object's start offset. The dict must already include a correct
// /Length.
func writeStreamObj(b *bytes.Buffer, num int, dict string, raw []byte) int {
	off := b.Len()
	b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nstream\n", num, dict))
	b.Write(raw)
	b.WriteString("\nendstream\nendobj\n")
	return off
}

// TestParseXrefStream builds a PDF whose cross-reference data is an uncompressed
// xref stream (no /Filter, so DecodeStream returns the raw entries) and checks
// that the resolver reads the catalog and page tree through it.
func TestParseXrefStream(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R >>")

	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // obj 0: free
		{1, int64(off[1]), 0}, // obj 1
		{1, int64(off[2]), 0}, // obj 2
		{1, int64(off[3]), 0}, // obj 3
		{1, int64(xoff), 0},   // obj 4: the xref stream itself
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 5 /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 4, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Catalog(); err != nil {
		t.Fatalf("Catalog via xref stream: %v", err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 612 {
		t.Errorf("pages = %d, width0 = %.0f; want 1 page, 612", len(pages), mustWidth(pages))
	}
}

// TestCompressedObjectStream stores the page object inside an /ObjStm and marks
// it as a type-2 entry in the xref stream, exercising compressed-object
// resolution.
func TestCompressedObjectStream(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 600] >>")

	// ObjStm (object 4) holding object 3 (the page), uncompressed.
	header := "3 0 " // objNum=3, offset=0 (relative to /First)
	body := "<< /Type /Page /Parent 2 0 R >>"
	objstm := []byte(header + body)
	osDict := fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Length %d >>", len(header), len(objstm))
	off[4] = writeStreamObj(&b, 4, osDict, objstm)

	// Xref stream (object 5).
	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // obj 0: free
		{1, int64(off[1]), 0}, // obj 1
		{1, int64(off[2]), 0}, // obj 2
		{2, 4, 0},             // obj 3: compressed in ObjStm 4 at index 0
		{1, int64(off[4]), 0}, // obj 4: the ObjStm
		{1, int64(xoff), 0},   // obj 5: the xref stream itself
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 6 /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 5, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	// Resolve the compressed object directly.
	obj, err := r.Resolve(Reference{Number: 3})
	if err != nil {
		t.Fatalf("resolve compressed object 3: %v", err)
	}
	d, ok := obj.(Dict)
	if !ok {
		t.Fatalf("object 3 = %T, want Dict", obj)
	}
	if typ, _ := d.GetName("Type"); typ != "Page" {
		t.Errorf("object 3 /Type = %q, want Page", typ)
	}

	// And via the page tree.
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 400 {
		t.Errorf("pages = %d, width = %.0f; want 1 page, 400", len(pages), mustWidth(pages))
	}
}

func mustWidth(pages []PageInfo) float64 {
	if len(pages) == 0 {
		return -1
	}
	return pages[0].MediaBox.Width()
}

// TestDecodeStream_FlatePredictor covers the FlateDecode + PNG predictor 12 (Up)
// path used by real-world xref streams.
func TestDecodeStream_FlatePredictor(t *testing.T) {
	// Two rows of 3 bytes each.
	rows := [][]byte{
		{10, 20, 30},
		{11, 22, 33},
	}
	rowLen := 3

	// Encode with PNG Up predictor: filter byte 2, then row[i]-prev[i].
	var encoded []byte
	prev := make([]byte, rowLen)
	for _, row := range rows {
		encoded = append(encoded, 2) // Up
		for i := 0; i < rowLen; i++ {
			encoded = append(encoded, byte(int(row[i])-int(prev[i])))
		}
		prev = row
	}

	st := &Stream{
		Dict: Dict{
			"Filter":      Name("FlateDecode"),
			"DecodeParms": Dict{"Predictor": Integer(12), "Columns": Integer(rowLen)},
		},
		Raw: zlibBytes(encoded),
	}

	got, err := DecodeStream(st)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{10, 20, 30, 11, 22, 33}
	if !bytes.Equal(got, want) {
		t.Errorf("DecodeStream = %v, want %v", got, want)
	}
}

func TestDecodeStream_UnsupportedFilter(t *testing.T) {
	st := &Stream{Dict: Dict{"Filter": Name("JPXDecode")}, Raw: []byte("x")}
	if _, err := DecodeStream(st); err == nil {
		t.Error("DecodeStream with unsupported filter = nil error, want an error")
	}
}

// TestParseXrefStream_ExplicitIndex uses /Index [0 1 1 4] (two ranges covering
// objects 0 and 1–4), exercising xrefIndex's multi-range array path instead of
// the default [0 Size].
func TestParseXrefStream_ExplicitIndex(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 300] >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R >>")

	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // range [0 1]: obj 0 free
		{1, int64(off[1]), 0}, // range [1 4]: obj 1
		{1, int64(off[2]), 0}, // obj 2
		{1, int64(off[3]), 0}, // obj 3
		{1, int64(xoff), 0},   // obj 4: the xref stream
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 5 /Index [0 1 1 4] /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 4, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 300 {
		t.Errorf("pages = %d, width = %.0f; want 1 page, 300", len(pages), mustWidth(pages))
	}
}

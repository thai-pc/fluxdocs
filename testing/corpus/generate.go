//go:build ignore

// Command generate writes the synthetic PDF corpus used by corpus_test.go into
// testing/corpus/clean and testing/corpus/malformed. The corpus is fully
// synthetic (no real documents, no PII — SECURITY/testing README) and
// reproducible: run `go run testing/corpus/generate.go` to regenerate it.
//
// clean/ holds spec-shaped PDFs exercising the common features (text, multi-page,
// rotation, vector fills, Flate-compressed content, a form XObject). malformed/
// holds spec violations the parser must survive leniently (truncated, bad xref
// offsets, a missing endobj, a cyclic page tree, junk before the header, no
// trailer).
package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	// Derive the corpus dir from this source file's location (os.Args[0] is the
	// temp-built binary under `go run`).
	_, thisFile, _, _ := runtime.Caller(0)
	base := filepath.Dir(thisFile)

	clean := filepath.Join(base, "clean")
	malformed := filepath.Join(base, "malformed")
	must(os.MkdirAll(clean, 0o755))
	must(os.MkdirAll(malformed, 0o755))

	// --- clean ---
	write(clean, "text.pdf", textPDF())
	write(clean, "multipage.pdf", multiPagePDF(3))
	write(clean, "rotated.pdf", rotatedPDF())
	write(clean, "vector.pdf", vectorPDF())
	write(clean, "flate.pdf", flatePDF())
	write(clean, "form_xobject.pdf", formXObjectPDF())

	// --- malformed ---
	write(malformed, "truncated.pdf", truncated())
	write(malformed, "bad_xref.pdf", badXref())
	write(malformed, "missing_endobj.pdf", missingEndobj())
	write(malformed, "cyclic_pages.pdf", cyclicPages())
	write(malformed, "junk_prefix.pdf", junkPrefix())
	write(malformed, "no_trailer.pdf", noTrailer())

	fmt.Println("corpus generated under", base)
}

// --- PDF builder (classic xref, computed offsets) ---

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

func stream(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func textPDF() []byte {
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream("BT /F1 18 Tf 20 100 Td (Corpus text sample) Tj ET"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

func multiPagePDF(n int) []byte {
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	fontObj := 3 + 2*n
	objs[fontObj] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	kids := ""
	obj := 3
	for i := 0; i < n; i++ {
		p, c := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", p)
		objs[p] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, c)
		objs[c] = stream(fmt.Sprintf("BT /F1 14 Tf 20 100 Td (Page %d of %d) Tj ET", i, n))
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	return buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", fontObj+1))
}

func rotatedPDF() []byte {
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 300] /Rotate 90 >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream("BT /F1 12 Tf 20 100 Td (Rotated page) Tj ET"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

func vectorPDF() []byte {
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: stream("1 0 0 rg 10 10 40 40 re f 0 0 1 RG 2 w 55 55 m 90 90 l S"),
	}, "<< /Size 5 /Root 1 0 R >>")
}

func flatePDF() []byte {
	content := "BT /F1 16 Tf 20 100 Td (Flate compressed) Tj ET 0 1 0 rg 10 10 50 20 re f"
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	zw.Write([]byte(content))
	zw.Close()
	raw := zbuf.Bytes()
	obj4 := fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(raw), raw)
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: obj4,
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

func formXObjectPDF() []byte {
	form := stream2("<< /Type /XObject /Subtype /Form /BBox [0 0 50 50] >>", "0 0 1 rg 0 0 50 50 re f")
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Fm 5 0 R >> >> /Contents 4 0 R >>",
		4: stream("q 1 0 0 1 30 30 cm /Fm Do Q"),
		5: form,
	}, "<< /Size 6 /Root 1 0 R >>")
}

// stream2 is like stream but with a caller-provided dictionary (Length appended).
func stream2(dict, content string) string {
	d := dict[:len(dict)-3] // strip trailing " >>"
	return fmt.Sprintf("%s /Length %d >>\nstream\n%s\nendstream", d, len(content), content)
}

// --- malformed variants ---

func truncated() []byte {
	b := textPDF()
	return b[:len(b)/2] // cut mid-file
}

func badXref() []byte {
	// Valid objects but every xref offset is wrong -> forces lenient recovery.
	b := textPDF()
	return bytes.ReplaceAll(b, []byte("0000000"), []byte("9999999"))
}

func missingEndobj() []byte {
	b := textPDF()
	return bytes.Replace(b, []byte("endobj\n"), []byte(""), 1) // drop first endobj
}

func cyclicPages() []byte {
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [2 0 R] /Count 1 >>", // self-referential
	}, "<< /Size 3 /Root 1 0 R >>")
}

func junkPrefix() []byte {
	return append([]byte("%!PS some junk bytes before the header\n\x00\x01\x02"), textPDF()...)
}

func noTrailer() []byte {
	b := textPDF()
	if i := bytes.Index(b, []byte("trailer")); i >= 0 {
		return b[:i] // cut everything from the trailer on
	}
	return b
}

// --- helpers ---

func write(dir, name string, data []byte) {
	must(os.WriteFile(filepath.Join(dir, name), data, 0o644))
	fmt.Printf("  %s (%d bytes)\n", filepath.Join(filepath.Base(dir), name), len(data))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

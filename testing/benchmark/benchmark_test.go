// Package benchmark measures the throughput of the core operations (parse,
// extract, render, redact, merge) so performance claims rest on numbers, not
// adjectives (CLAUDE.md §9). The fixtures are synthetic and built in-process, so
// the benchmarks are reproducible on any machine.
//
//	go test ./testing/benchmark -bench=. -benchmem
package benchmark

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
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objects[i])
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n+1)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n%s\nstartxref\n%d\n%%%%EOF\n", trailer, x)
	return b.Bytes()
}

func streamObj(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

// textDoc builds an n-page PDF with a paragraph of text per page (a realistic
// text-extraction / render workload).
func textDoc(n int) []byte {
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	fontObj := 3 + 2*n
	objs[fontObj] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	kids := ""
	obj := 3
	for i := 0; i < n; i++ {
		p, c := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", p)
		var content strings.Builder
		content.WriteString("BT /F1 11 Tf 50 750 Td 13 TL ")
		for line := 0; line < 40; line++ {
			fmt.Fprintf(&content, "(The quick brown fox jumps over the lazy dog, line %d of page %d.) Tj T* ", line, i)
		}
		content.WriteString("ET")
		objs[p] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, c)
		objs[c] = streamObj(content.String())
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	return buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", fontObj+1))
}

// vectorDoc builds an n-page PDF whose pages are dense vector graphics (a
// rendering-heavy workload).
func vectorDoc(n int) []byte {
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	kids := ""
	obj := 3
	for i := 0; i < n; i++ {
		p, c := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", p)
		var content strings.Builder
		for r := 0; r < 200; r++ {
			x := (r * 7) % 580
			y := (r * 11) % 760
			fmt.Fprintf(&content, "%d.%d 0.%d 0.%d rg %d %d 20 20 re f ", r%9, r%7, (r*3)%9, (r*5)%9, x, y)
		}
		objs[p] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R >>", c)
		objs[c] = streamObj(content.String())
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	return buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", obj))
}

func BenchmarkOpen(b *testing.B) {
	data := textDoc(20)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := core.OpenBytes(data)
		if err != nil {
			b.Fatal(err)
		}
		doc.Close()
	}
}

func BenchmarkExtractText(b *testing.B) {
	data := textDoc(20)
	b.ReportMetric(20, "pages/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, _ := core.OpenBytes(data)
		if _, err := doc.ExtractText(core.ExtractOptions{}); err != nil {
			b.Fatal(err)
		}
		doc.Close()
	}
}

func BenchmarkRenderPageText(b *testing.B) {
	doc, _ := core.OpenBytes(textDoc(1))
	defer doc.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := doc.RenderPage(0, core.RenderOptions{DPI: 150, Format: "png"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderPageVector(b *testing.B) {
	doc, _ := core.OpenBytes(vectorDoc(1))
	defer doc.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := doc.RenderPage(0, core.RenderOptions{DPI: 150, Format: "png"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderAllPagesParallel(b *testing.B) {
	doc, _ := core.OpenBytes(textDoc(16))
	defer doc.Close()
	b.ReportMetric(16, "pages/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := doc.RenderAllPages(core.RenderOptions{DPI: 100, Format: "png"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMerge(b *testing.B) {
	a, _ := core.OpenBytes(textDoc(10))
	c, _ := core.OpenBytes(textDoc(10))
	defer a.Close()
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		merged, err := a.Merge(c)
		if err != nil {
			b.Fatal(err)
		}
		merged.Close()
	}
}

func BenchmarkRedact(b *testing.B) {
	data := textDoc(5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, _ := core.OpenBytes(data)
		for p := 0; p < doc.PageCount; p++ {
			doc.RedactText(p, `line \d+`)
		}
		out, err := doc.RedactAndFlatten()
		if err != nil {
			b.Fatal(err)
		}
		out.Close()
		doc.Close()
	}
}

func BenchmarkSaveTo(b *testing.B) {
	doc, _ := core.OpenBytes(textDoc(20))
	defer doc.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := doc.SaveTo(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

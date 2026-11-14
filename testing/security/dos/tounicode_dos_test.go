package dos

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// toUnicodePDF returns a one-page document whose font's /ToUnicode CMap maps
// code <01> to dstUnits copies of U+0041 and code <02> to "A", and whose page
// shows code shown n times in one Tj. The two codes cost the same to parse
// and to show; only what they read to differs.
func toUnicodePDF(dstUnits, n int, shown byte) []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <00> <FF> endcodespacerange\n" +
		"2 beginbfchar <01> <" + strings.Repeat("0041", dstUnits) + "> <02> <0041> endbfchar\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end"
	content := fmt.Sprintf("BT /F1 12 Tf 10 100 Td <%s> Tj ET", strings.Repeat(fmt.Sprintf("%02X", shown), n))
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap), cmap),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := make([]int, len(objs))
	for i, o := range objs {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// toUnicodeSearchPDF is toUnicodePDF's page as the second page of a document
// whose first shows "SECRET-1234" in Helvetica: redacting that text searches
// the second page for it (automatic extension), reading through the font.
func toUnicodeSearchPDF(dstUnits, n int, shown byte) []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <00> <FF> endcodespacerange\n" +
		"2 beginbfchar <01> <" + strings.Repeat("0041", dstUnits) + "> <02> <0041> endbfchar\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end"
	first := "BT /F1 12 Tf 10 700 Td (SECRET-1234) Tj ET"
	content := fmt.Sprintf("BT /F2 12 Tf 10 100 Td <%s> Tj ET", strings.Repeat(fmt.Sprintf("%02X", shown), n))
	stream := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s) }
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 7 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F2 8 0 R >> >> /Contents 6 0 R >>",
		stream(first),
		stream(content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 9 0 R >>",
		stream(cmap),
	})
}

// measured runs fn after a GC and returns its process CPU time and the bytes
// it allocated.
func measured(fn func()) (time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := cpuTime()
	fn()
	elapsed := cpuTime() - start
	runtime.ReadMemStats(&after)
	return elapsed, after.TotalAlloc - before.TotalAlloc
}

// TestDoS_ToUnicodeExpansionIsBounded: a /ToUnicode destination of 1 MiB
// (512 Ki code units) made every code shown through it 1 MiB of text, so one
// Tj of 1,400 such codes decoded to 700 MiB: ExtractTextByPage took 13 s of
// CPU and allocated 4.7 GiB for it (243× and 191× the plain page), and
// RedactText allocated as much before its reading budget refused the page
// (it now refuses the font up front: its readings are cut).
// With destinations cut to 512 bytes and each code's text to 64 bytes per
// byte (89,600 bytes of text), the hostile page costs about what the same
// page showing a code that maps to one letter costs (≈ 1.0–1.1×): both parse
// the same CMap and show the same bytes. So does an automatic extension
// that searches such a page for text removed from another (a text search
// reads on through a cut font, recording the dispute; ≈ 1.1–1.2×).
// Each bound is on the median of three alternated runs (the ratios measure
// ≈ 1.0, under half the bound; allocation is nearly deterministic; CPU time
// is the process's, so host load affects both alike).
func TestDoS_ToUnicodeExpansionIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	const (
		dstUnits = 1 << 19 // a 1 MiB destination
		shows    = 1400
	)
	hostile := toUnicodePDF(dstUnits, shows, 0x01)
	plain := toUnicodePDF(dstUnits, shows, 0x02)
	searchHostile := toUnicodeSearchPDF(dstUnits, shows, 0x01)
	searchPlain := toUnicodeSearchPDF(dstUnits, shows, 0x02)
	// Each run opens the document afresh (RedactText caches its page model
	// per document) and is repeated to about a quarter second of CPU time.
	open := func(data []byte) *core.Document {
		d, err := core.OpenBytes(data)
		if err != nil {
			t.Fatalf("OpenBytes: %v", err)
		}
		return d
	}
	for _, op := range []struct {
		name   string
		bound  float64 // on the hostile/plain ratio, CPU and allocation alike
		search bool    // run on the toUnicodeSearchPDF documents
		run    func([]byte) error
	}{
		{"ExtractTextByPage", 2, false, func(data []byte) error {
			_, err := open(data).ExtractTextByPage(0, core.ExtractOptions{})
			return err
		}},
		// RedactText refuses the font (its readings are cut: fontmodel
		// TextCut), on both pages; the refusal must be as cheap.
		{"RedactText", 2, false, func(data []byte) error {
			_, err := open(data).RedactText(0, `ZZZZ`)
			if !errors.Is(err, core.ErrRedactionUnsupported) {
				return fmt.Errorf("want a content_ambiguous refusal, got %v", err)
			}
			return nil
		}},
		// Automatic extension searches the second page for the text
		// removed from the first: a text search records the cut font as a
		// dispute and reads on through it, and the redaction is refused
		// once the search ends; reading on must be as cheap.
		{"RedactAndFlatten search", 2, true, func(data []byte) error {
			d := open(data)
			if _, err := d.RedactText(0, `SECRET-\d+`); err != nil {
				return fmt.Errorf("RedactText: %v", err)
			}
			out, err := d.RedactAndFlatten()
			var re *core.RedactionError
			if out != nil || !errors.As(err, &re) || re.Reason != core.RedactionContentAmbiguous {
				return fmt.Errorf("want a content_ambiguous refusal, got (%v, %v)", out, err)
			}
			return nil
		}},
	} {
		hostile, plain := hostile, plain
		if op.search {
			hostile, plain = searchHostile, searchPlain
		}
		t.Run(op.name, func(t *testing.T) {
			one, _ := measured(func() { op.run(plain) })
			reps := max(int(250*time.Millisecond/max(one, time.Millisecond)), 3)
			batch := func(d []byte) (time.Duration, uint64) {
				var err error
				cpu, alloc := measured(func() {
					for i := 0; i < reps; i++ {
						if e := op.run(d); e != nil {
							err = e
						}
					}
				})
				if err != nil {
					t.Fatalf("%s: %v", op.name, err)
				}
				return cpu, alloc
			}
			var cpuRatios, allocRatios []float64
			for run := 0; run < 3; run++ {
				refCPU, refAlloc := batch(plain)
				gotCPU, gotAlloc := batch(hostile)
				cpuRatios = append(cpuRatios, float64(gotCPU)/float64(refCPU))
				allocRatios = append(allocRatios, float64(gotAlloc)/float64(refAlloc))
			}
			slices.Sort(cpuRatios)
			slices.Sort(allocRatios)
			t.Logf("%d reps per run; hostile/plain: CPU median %.2f (runs %.2f), allocation median %.2f (runs %.2f); bound %.0f",
				reps, cpuRatios[1], cpuRatios, allocRatios[1], allocRatios, op.bound)
			if cpuRatios[1] > op.bound || allocRatios[1] > op.bound {
				t.Errorf("a 1 MiB destination shown %d times costs %.2f× the CPU and %.2f× the allocation of one-letter text, want ≤ %.0f×",
					shows, cpuRatios[1], allocRatios[1], op.bound)
			}
		})
	}
}

package dos

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// The standard-14 substitution gates (docs/design/type1-std14-fonts.md §5,
// Phase 3). The bundled faces are trusted, but everything a non-embedded
// font dictionary supplies is not: /BaseFont, /Widths, /FirstChar,
// /Encoding /Differences and the descriptor's /Flags, /ItalicAngle and
// /MissingWidth.

// hugeReal is about 9e307 spelled in full (PDF numbers have no exponent).
var hugeReal = "9" + strings.Repeat("0", 307) + ".0"

// hostileWidths is a /Widths array of n entries mixing huge, negative,
// fractional and non-numeric values.
func hostileWidths(n int) string {
	var b strings.Builder
	b.WriteString("[")
	vals := []string{hugeReal, "-99999999999", "0", "0.000001", "/Name", "(str)", "600", "999999999999999999999"}
	for i := 0; i < n; i++ {
		b.WriteString(vals[i%len(vals)])
		b.WriteByte(' ')
	}
	b.WriteString("]")
	return b.String()
}

// hostileDifferences is an /Encoding /Differences array of n entries over
// every code, naming glyphs real (A, alpha, a1), uniXXXX, overlong and
// meaningless.
func hostileDifferences(n int) string {
	var b strings.Builder
	b.WriteString("<< /Differences [0 ")
	names := []string{"/A", "/alpha", "/a1", "/uni20AC", "/u1F600", "/" + strings.Repeat("x", 4000), "/f_f_i", "/.notdef", "/uniD800"}
	for i := 0; i < n; i++ {
		if i%256 == 0 {
			fmt.Fprintf(&b, "%d ", 0)
		}
		b.WriteString(names[i%len(names)])
		b.WriteByte(' ')
	}
	b.WriteString("] >>")
	return b.String()
}

// TestDoS_SubstituteManyFontDictsAreBounded: 100,000 distinct non-embedded
// font dictionaries, each with a full /Differences, /Widths and a
// descriptor, selected once each by the page. Each loaded dictionary builds
// its own code map, widths and fit table (≈ 2.5 KiB, a few µs), charged
// fontLoadCost; at most maxFontLoads load, so the page ends promptly with
// bounded allocation. The bound is above the Type 1 gate's because every
// dictionary here also carries a 256-entry /Differences, which fontmodel
// reads into a per-dictionary map (≈ 26 KiB allocated each, ≈ 105 MiB over
// 4,096 loads, whatever the font's program); measured ≈ 250 MiB in all.
func TestDoS_SubstituteManyFontDictsAreBounded(t *testing.T) {
	skipUnlessTimed(t)
	const n = 100000
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"", // page
		"", // content
		"<< /Type /FontDescriptor /FontName /Verdana /Flags 98 /ItalicAngle -12 /MissingWidth 500 >>",
		hostileWidths(256),
		hostileDifferences(256),
	}
	var fonts, content strings.Builder
	content.WriteString("BT\n")
	bases := []string{"Helvetica", "Times-Bold", "Symbol", "ZapfDingbats", "Verdana", "Arial,BoldItalic"}
	for i := 0; i < n; i++ {
		num := len(objs) + 1
		objs = append(objs, fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /FirstChar 0 /LastChar 255 /Widths 6 0 R /Encoding 7 0 R /FontDescriptor 5 0 R >>", bases[i%len(bases)]))
		fmt.Fprintf(&fonts, "/F%d %d 0 R ", i, num)
		fmt.Fprintf(&content, "/F%d 1 Tf (A) Tj\n", i)
	}
	content.WriteString("ET\n")
	objs[2] = "<< /Type /Page /Parent 2 0 R /Resources << /Font << " + fonts.String() + ">> >> /Contents 4 0 R >>"
	objs[3] = streamBody(content.String())
	doc, err := core.OpenBytes(classicPDF(objs))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), t1Deadline)
	defer cancel()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err = doc.RenderPageContext(ctx, 0, core.RenderOptions{DPI: 72, Format: "png"})
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Errorf("err = %v, want a render or ErrRenderBudgetExceeded", err)
	}
	if took > 3*time.Second {
		t.Errorf("render took %v, want < 3 s", took)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 320<<20 {
		t.Errorf("render allocated %d MiB, want < 320 MiB", alloc>>20)
	}
	t.Logf("%d non-embedded font dictionaries: %v, %d MiB allocated, err %v", n, took, (after.TotalAlloc-before.TotalAlloc)>>20, err)
}

// TestDoS_SubstituteHostileFontDict renders text in non-embedded fonts with
// hostile metrics: a 10^5-entry /Widths of huge, negative and non-numeric
// values (far past the 256 codes a simple font has), a /FirstChar of 255, a
// /Differences of 10^4 entries with overlong names (past fontmodel's
// maxDiffItems), out-of-range /Flags and a huge /ItalicAngle. It must
// render (or trip the budget) promptly, without panicking. The arrays are
// kept just large enough to pass every bound: larger ones only time the
// parser's lexing, not the substitution.
func TestDoS_SubstituteHostileFontDict(t *testing.T) {
	skipUnlessTimed(t)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R /F2 6 0 R /F3 7 0 R >> >> /Contents 4 0 R >>",
		streamBody("BT /F1 12 Tf 10 100 Td (ABC\\377) Tj /F2 " + hugeReal + " Tf (A) Tj /F3 -12 Tf [(AB) -" + hugeReal + " (C)] TJ ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /" + strings.Repeat("Helvetica", 1000) + " /FirstChar 0 /Widths " + hostileWidths(100_000) + " /Encoding " + hostileDifferences(10_000) + " >>",
		"<< /Type /Font /Subtype /TrueType /BaseFont /Unknown /FirstChar 255 /Widths [" + hugeReal + "] /FontDescriptor 8 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Symbol /FirstChar -5 /Widths [-1 -2 -3] /Encoding /NoSuchEncoding >>",
		"<< /Type /FontDescriptor /Flags 99999999999999999999 /ItalicAngle " + hugeReal + " /MissingWidth -" + hugeReal + " >>",
	}
	err, took := renderT1(t, classicPDF(objs))
	if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Errorf("err = %v, want a render or ErrRenderBudgetExceeded", err)
	}
	if took > t1Deadline/2 {
		t.Errorf("render took %v", took)
	}
	t.Logf("hostile non-embedded fonts: %v, err %v", took, err)
}

// TestDoS_SubstituteGlyphFloodIsBounded shows 10^6 large substitute glyphs:
// each costs its outline points (glyphPointCost) and its fill, so the page
// trips the raster-work budget or finishes, promptly either way.
func TestDoS_SubstituteGlyphFloodIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	text := strings.Repeat("@W", 5000)
	var content strings.Builder
	content.WriteString("BT /F1 150 Tf\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&content, "1 0 0 1 0 %d Tm (%s) Tj\n", i%200, text)
	}
	content.WriteString("ET\n")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		streamBody(content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Times-BoldItalic >>",
	}
	err, took := renderT1(t, classicPDF(objs))
	if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Errorf("err = %v, want a render or ErrRenderBudgetExceeded", err)
	}
	if took > t1Deadline/2 {
		t.Errorf("render took %v", took)
	}
	t.Logf("10^6 substitute glyphs: %v, err %v", took, err)
}

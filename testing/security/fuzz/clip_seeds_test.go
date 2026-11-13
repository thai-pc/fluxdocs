package fuzz

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// clipSeedBytes are PDF-level seeds for clipping paths (W, W*;
// docs/design/clipping-paths.md §10.3): the lenient cases of §2.2 (no path,
// zero area, W across Q, Do and BT, W then more path, W W*), deep q/W nests
// past maxSaveDepth with rectangle and general clips, clips with NaN, Inf and
// 1e300 coordinates, clip-only and unbalanced forms, W inside BT, and text
// clipping (Tr 4–7, §5): every clip mode with Tj, TJ, ' and ", non-integer
// and out-of-range Tr, empty glyphs, a missing font or ET, BT, q, Q and Do
// inside an open text object, forms that leave one open, nested text clips
// under general W clips, huge and degenerate text matrices, and floods of
// clip-mode text objects.
func clipSeedBytes() [][]byte {
	big := "1" + strings.Repeat("0", 300) + ".0"
	tri := "0 0 m 100 0 l 0 100 l h"
	page := func(content, form string) []byte {
		objs := []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Fm 6 0 R /Im 7 0 R >> >> /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /XObject << /Fm 6 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(form), form),
			"<< /Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 4 >>\nstream\n\x00\x40\x80\xff\nendstream",
		}
		return classicPDF(objs)
	}
	const fill = " 0 0 100 100 re f"
	var out [][]byte
	for _, c := range []struct{ content, form string }{
		{"W n" + fill, ""},
		{"h W n" + fill, ""},
		{"10 10 0 50 re W n" + fill, ""},
		{"10 10 m W* n" + fill, ""},
		{"q 20 20 20 20 re W Q n" + fill, ""},
		{"20 20 20 20 re W 60 60 20 20 re f" + fill, ""},
		{"10 10 80 80 re 30 30 40 40 re W W* n" + fill, ""},
		{"20 20 20 20 re W /Fm Do /Im Do n" + fill, tri + " W"},
		{"BT 20 20 20 20 re W n /F1 24 Tf 10 10 Td (Clip) Tj ET" + fill, ""},
		{strings.Repeat("q "+tri+" W n ", 1100) + fill + strings.Repeat(" Q", 1100), ""},
		{strings.Repeat("q 1 1 98 98 re W n ", 1100) + fill, ""},
		{strings.Repeat("q "+tri+" W* n Q ", 2000) + fill, ""},
		{"0 0 m " + big + " 0 l 0 -" + big + " l h W n" + fill, ""},
		{"q " + big + " 0 0 " + big + " 0 0 cm " + big + " 0 0 " + big + " 0 0 cm 0 0 1 1 re Q W n" + fill, ""},
		{"-" + big + " -" + big + " " + big + " " + big + " re W n" + fill, ""},
		{"q 0 0 0 0 0 0 cm " + tri + " W n Q" + fill, ""},
		{strings.Repeat("/Fm Do ", 50) + fill, tri + " W n " + tri + " W* n"},
		{"/Fm Do" + fill, "Q Q Q q " + tri + " W n /Fm Do"},
		{"/Fm Do" + fill, "q q q " + tri + " W n"},
		{"BT /F1 48 Tf 7 Tr 10 10 Td (CLIP) Tj ET" + fill, ""},
		{"BT /F1 48 Tf 4.5 Tr 10 10 Td (CLIP) Tj ET" + fill, ""},
		{"BT /F1 48 Tf 7 Tr 10 10 Td (" + strings.Repeat("W", 5000) + ") Tj ET" + fill, ""},
		{"10 10 m 90 90 l 50 10 l W 3 w S" + fill, ""},
		{"BT /F1 30 Tf 4 Tr (A) Tj 5 Tr [(B) -200 (C)] TJ 6 Tr 20 TL (D) ' 7 Tr 1 2 (E) \" ET" + fill, ""},
		{"BT /F1 30 Tf 3.5 Tr (A) Tj 7.9 Tr (B) Tj -4 Tr (C) Tj 8 Tr (D) Tj " + big + " Tr (E) Tj 7.0 Tr (F) Tj ET" + fill, ""},
		{"BT /F1 30 Tf 7 Tr (    ) Tj ET" + fill, ""},
		{"BT 7 Tr ET BT 7 Tr /F1 30 Tf () Tj ET BT 7 Tr 10 10 Td (no font) Tj ET" + fill, ""},
		{"BT /F1 30 Tf 7 Tr (A) Tj" + fill, ""},
		{"BT /F1 30 Tf 7 Tr (A) Tj BT (B) Tj ET q BT 7 Tr (C) Tj Q ET BT 7 Tr q (D) Tj ET Q" + fill, ""},
		{"BT /F1 30 Tf 7 Tr (A) Tj /Fm Do /Im Do ET" + fill, "BT /F1 20 Tf 7 Tr (F) Tj"},
		{strings.Repeat("/Fm Do ", 40) + fill, "BT 7 Tr /F1 20 Tf (F) Tj ET Q BT 7 Tr (G) Tj"},
		{"q " + tri + " W n BT /F1 60 Tf 7 Tr (AB) Tj ET 0 0 m 100 100 l 0 100 l h W* n BT 4 Tr (CD) Tj ET" + fill + " Q", ""},
		{"BT /F1 30 Tf 7 Tr " + big + " 0 0 " + big + " 0 0 Tm (A) Tj 0 0 0 0 0 0 Tm (B) Tj ET" + fill, ""},
		{"BT /F1 " + big + " Tf 7 Tr (W) Tj ET BT /F1 30 Tf 7 Tr " + big + " Tz " + big + " Ts " + big + " Tc (WW) Tj ET" + fill, ""},
		{strings.Repeat("q BT /F1 12 Tf 7 Tr 10 10 Td (W) Tj ET Q ", 3000) + fill, ""},
		{strings.Repeat("BT /F1 40 Tf 7 Tr 5 5 Td (O) Tj ET ", 200) + fill, ""},
		{strings.Repeat("q ", 1100) + "BT /F1 40 Tf 7 Tr (W) Tj ET" + strings.Repeat(" Q", 1100) + fill, ""},
		{"W" + strings.Repeat(" 1 1 m 99 99 l", 1000) + " n" + fill, ""},
	} {
		out = append(out, page(c.content, c.form))
	}
	return out
}

// TestClipSeedsHandled runs every clip seed through the fuzz pipeline: no
// panic (recovered or not), and the render returns within the deadline plus
// slack.
func TestClipSeedsHandled(t *testing.T) {
	for i, seed := range clipSeedBytes() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			doc, err := core.OpenBytes(seed)
			if err != nil {
				t.Fatalf("OpenBytes: %v (seed must reach the renderer)", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), renderDeadline)
			defer cancel()
			start := time.Now()
			_, err = doc.RenderPageContext(ctx, 0, core.RenderOptions{DPI: 72, Format: "png"})
			failOnInternal(t, err)
			if elapsed := time.Since(start); elapsed > renderDeadline+renderSlack {
				t.Errorf("render took %v, want ≤ %v", elapsed, renderDeadline+renderSlack)
			}
			exercise(t, seed)
		})
	}
}

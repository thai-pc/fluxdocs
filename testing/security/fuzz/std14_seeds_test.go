package fuzz

import (
	"fmt"
	"strings"
)

// std14SeedBytes are PDF-level seeds for the substitution of non-embedded
// fonts (docs/design/type1-std14-fonts.md Phase 3): each of the standard 14
// names and an alias without /Widths; /Widths, /Differences and descriptor
// flags that select the AFM, /MissingWidth, fit and not-drawn paths; and
// hostile fonts (huge and negative widths, an indirect /BaseFont, a
// /FirstChar past 255, an overlong /Differences name, and an indirect
// /Subtype naming Type0 on a font with no descendant, which must not be
// drawn from a substitute one byte per glyph).
func std14SeedBytes() [][]byte {
	const content = "BT /F1 24 Tf 10 100 Td (Abg\\200\\377!) Tj [(A) -500 (W)] TJ 3 Tr (x) Tj ET"
	page := func(fontDict, desc string) []byte {
		objs := []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
			"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
			fontDict,
		}
		if desc != "" {
			objs = append(objs, desc)
		}
		return classicPDF(objs)
	}
	var out [][]byte
	for _, base := range []string{"Helvetica", "Times-BoldItalic", "Courier-Oblique", "Symbol", "ZapfDingbats", "ABCDEF+Arial,Bold", "TimesNewRomanPS-ItalicMT"} {
		out = append(out, page("<< /Type /Font /Subtype /Type1 /BaseFont /"+base+" >>", ""))
	}
	for _, c := range [][2]string{
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 65 /LastChar 67 /Widths [100 5000 -300] /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /alpha /uni20AC /" + strings.Repeat("q", 300) + "] >> >>", ""},
		{"<< /Type /Font /Subtype /TrueType /BaseFont /Verdana,Italic /FontDescriptor 6 0 R /FirstChar 255 /Widths [600 600] >>", "<< /Type /FontDescriptor /Flags 262242 /ItalicAngle -11 /MissingWidth 400 >>"},
		{"<< /Type /Font /Subtype /TrueType /BaseFont /Wingdings /FontDescriptor 6 0 R /FirstChar 0 /Widths [1 2 3] >>", "<< /Type /FontDescriptor /Flags 4 >>"},
		{"<< /Type /Font /Subtype /Type1 /BaseFont 6 0 R /Encoding /MacRomanEncoding >>", "/Courier-Bold"},
		{"<< /Type /Font /Subtype /MMType1 /BaseFont /MyriadMM_400_600_ /FontDescriptor 6 0 R >>", "<< /Type /FontDescriptor /Flags 34 /FontBBox [0 0 0 0] >>"},
		{"<< /Type /Font /BaseFont /Symbol /Encoding /StandardEncoding /FirstChar -1 /Widths 99 >>", ""},
		{"<< /Type /Font /Subtype 6 0 R /BaseFont /Helvetica /Encoding /Identity-H >>", "/Type0"},
	} {
		out = append(out, page(c[0], c[1]))
	}
	return out
}

//go:build !js

package render

import (
	"image"
	"os"
	"os/exec"
	"testing"
)

// TestCFF_GhostscriptOracle renders the hand-built CFF font pages (Type1C
// simple fonts, CIDFontType0C and a Type1C as a CIDFont) with Ghostscript
// and compares the ink bounding boxes with ours, whole page and by region
// (±1 px at 72 DPI; design docs/design/type1-std14-fonts.md §7.1). It is an
// independent check of the INDEX, DICT, charset, Encoding and FDSelect
// reading and of the Type 2 operators, widths, hint masks, flex and
// endchar-seac semantics. Ghostscript is an external black-box tool (AGPL)
// run as a separate process in tests only; nothing is linked or vendored.
// It skips without gs on PATH unless FLUXDOCS_REQUIRE_GS=1.
func TestCFF_GhostscriptOracle(t *testing.T) {
	gs, _ := exec.LookPath("gs")
	if gs == "" {
		if os.Getenv("FLUXDOCS_REQUIRE_GS") == "1" {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not installed")
	}
	font := cffTestFontR().Bytes()
	cid := cidTestFontR().Bytes()
	for _, tt := range []struct {
		name string
		pdf  []byte
	}{
		{"square", cffPDF(font, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"ring", cffPDF(font, "", "BT /F1 100 Tf 20 30 Td (B) Tj ET")},
		{"seac", cffPDF(font, "", "BT /F1 100 Tf 20 30 Td (C) Tj ET")},
		{"curves", cffPDF(font, "", "BT /F1 100 Tf 20 30 Td (E) Tj ET")},
		{"flex", cffPDF(font, "", "BT /F1 100 Tf 20 30 Td (F) Tj ET")},
		{"advance", cffPDF(font, "", "BT /F1 100 Tf 0 30 Td (AB) Tj ET")},
		{"widths", cffPDF(font, "/FirstChar 65 /LastChar 65 /Widths [1000]", "BT /F1 100 Tf 0 30 Td (AB) Tj ET")},
		{"winansi", cffPDF(font, "/Encoding /WinAnsiEncoding", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"cid fd0", cidPDF(cid, "BT /F1 100 Tf 20 30 Td <01F4> Tj ET")},
		{"cid fd1 advance", cidPDF(cid, "BT /F1 100 Tf 0 30 Td <000701F4> Tj ET")},
		{"cid /W", cidPDFWith(cid, "/W [7 [600]]", "BT /F1 100 Tf 0 30 Td <000701F4> Tj ET")},
		{"type1c as cidfont", cidPDF(font, "BT /F1 100 Tf 0 30 Td <00010002> Tj ET")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ours := renderPDF(t, tt.pdf)
			theirs := gsPNG(t, gs, tt.pdf)
			for _, half := range []image.Rectangle{image.Rect(0, 0, 200, 200), image.Rect(0, 0, 200, 135), image.Rect(0, 135, 200, 200), image.Rect(0, 0, 75, 200)} {
				a, okA := inkBox(ours, half)
				b, okB := inkBox(theirs, half)
				if okA != okB || okA && !near(a, b) {
					t.Errorf("region %v: ink box = %v (%v), Ghostscript %v (%v)", half, a, okA, b, okB)
				}
			}
		})
	}
}

package dos

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/testing/internal/cffmin"
)

// The CFF font gates (docs/design/type1-std14-fonts.md §5.4): the Type 2
// twins of the Type 1 gates, plus the CFF structures a hostile program can
// repeat (Font DICTs sharing or overlapping one Private DICT).

// cffBombGlyph draws a small square, then fans out through two levels of
// local subroutines (40 × 40 × 40 operators) far past the per-glyph
// operator bound: every build runs, and is charged, 16,384 operators and
// is then dropped.
func cffBombGlyph() []byte {
	cs := cffmin.CS(500, 10, 10, cffmin.RMoveTo, 100, 100, -100, cffmin.HLineTo)
	return append(cs, append(bytes.Repeat(cffmin.CS(-106, cffmin.CallSubr), 40), cffmin.CS(cffmin.EndChar)...)...)
}

func cffBombSubrs() [][]byte {
	return [][]byte{
		cffmin.CS(cffmin.Return),
		append(bytes.Repeat(cffmin.CS(-105, cffmin.CallSubr), 40), cffmin.CS(cffmin.Return)...),
		append(bytes.Repeat(cffmin.CS(cffmin.HStem), 40), cffmin.CS(cffmin.Return)...),
	}
}

// TestDoS_CFFGlyphBombIsBounded shows 10^5 glyphs of a CFF font whose every
// glyph runs into the operator bound. With 32 distinct glyphs the outline
// cache charges each build once and the page renders; with 4,096 (16 font
// dictionaries over one program, /Differences selecting 256 glyphs each)
// the charged operators pass the default raster-work budget and the render
// stops with ErrRenderBudgetExceeded. Either way it ends promptly.
func TestDoS_CFFGlyphBombIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	for _, tt := range []struct {
		name       string
		fonts      int
		codes      int
		wantBudget bool
	}{
		{"32 glyphs, cached", 1, 32, false},
		{"4096 glyphs, budget", 16, 256, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			codes := make([]byte, tt.codes)
			for i := range codes {
				codes[i] = byte(i)
			}
			show := fmt.Sprintf("<%x> Tj\n", codes)
			glyphs := make([][]byte, 256*tt.fonts)
			for i := range glyphs {
				glyphs[i] = cffBombGlyph()
			}
			fonts := map[string]string{}
			var content strings.Builder
			content.WriteString("BT\n")
			reps := 100000 / (tt.codes * tt.fonts)
			for f := 0; f < tt.fonts; f++ {
				var diffs strings.Builder
				for c := 0; c < 256; c++ {
					fmt.Fprintf(&diffs, " /g%d", f*256+c)
				}
				name := fmt.Sprintf("F%d", f)
				fonts[name] = "/Encoding << /Differences [0" + diffs.String() + "] >>"
				fmt.Fprintf(&content, "/%s 1 Tf\n", name)
				for r := 0; r <= reps; r++ {
					content.WriteString(show)
				}
			}
			content.WriteString("ET\n")
			pdf := cffmin.PDF(cffmin.Program(glyphs, cffBombSubrs(), 0), nil, fonts, content.String())
			err := renderT1(t, pdf)
			switch {
			case tt.wantBudget && !errors.Is(err, core.ErrRenderBudgetExceeded):
				t.Errorf("err = %v, want ErrRenderBudgetExceeded", err)
			case !tt.wantBudget && err != nil:
				t.Errorf("err = %v, want a render", err)
			}
			boundedT1(t, tt.name, pdf, 3)
		})
	}
}

// TestDoS_CFFSharedProgramParsedOnce: 10,000 font dictionaries share one
// 30 MiB CFF program (Flate-compressed to a few KiB), and the page selects
// every one. The program is decoded once per render (the program cache, by
// stream reference); decoding it per font would pass the decoded-bytes
// budget after 17 fonts.
func TestDoS_CFFSharedProgramParsedOnce(t *testing.T) {
	skipUnlessTimed(t)
	prog := cffmin.Program([][]byte{cffBombGlyph()}, cffBombSubrs(), 30<<20)
	fonts := map[string]string{}
	var content strings.Builder
	content.WriteString("BT\n")
	for i := 0; i < 10000; i++ {
		name := fmt.Sprintf("F%d", i)
		fonts[name] = ""
		fmt.Fprintf(&content, "/%s 1 Tf <00> Tj\n", name)
	}
	content.WriteString("ET\n")
	pdf := cffmin.PDF(prog, flateBytes(t, prog), fonts, content.String())
	if err := renderT1(t, pdf); err != nil {
		t.Errorf("err = %v, want a render (the program decoded once)", err)
	}
	boundedT1(t, "10,000 fonts over one 30 MiB CFF program", pdf, 10)
}

// TestDoS_CFFFontDictsSharingPrivate: a CID-keyed program of 256 Font
// DICTs whose Private DICTs are one 16 MiB DICT. Shared, the DICT is parsed
// once (the Private cache, by offset and size); overlapping (each Font
// DICT's Private two bytes shorter at the front), the distinct DICTs would
// parse ≈ 4 GiB, and the font is rejected once they pass the program's
// size. Either way the page renders promptly.
func TestDoS_CFFFontDictsSharingPrivate(t *testing.T) {
	skipUnlessTimed(t)
	glyph := cffmin.CS(500, 10, 10, cffmin.RMoveTo, 100, 100, -100, cffmin.HLineTo, cffmin.EndChar)
	for _, overlap := range []bool{false, true} {
		t.Run(fmt.Sprintf("overlap %v", overlap), func(t *testing.T) {
			prog := cffmin.CIDProgram(glyph, 256, 16<<20, overlap)
			pdf := cffmin.CIDPDF(prog, map[string]string{"F1": ""}, "BT /F1 50 Tf 10 10 Td <0001> Tj ET")
			if err := renderT1(t, pdf); err != nil {
				t.Errorf("err = %v, want a render", err)
			}
			boundedT1(t, fmt.Sprintf("256 Font DICTs, overlap %v", overlap), pdf, 10)
		})
	}
}

// cidWidths returns a CIDFont /W entry of n items: one "0 [500 …]" list, or
// n "c c 500" ranges at CIDs no shown code selects ("ranges").
func cidWidths(form string, n int) string {
	var w strings.Builder
	w.WriteString("/W [")
	if form == "ranges" {
		for i := 0; i < n; i++ {
			fmt.Fprintf(&w, " %d %d 500", 100000+i, 100000+i)
		}
	} else if form == "empty" {
		w.WriteString(strings.Repeat(" 0 []", n))
	} else {
		w.WriteString("0 [")
		w.WriteString(strings.Repeat(" 500", n))
		w.WriteString("]")
	}
	w.WriteString("]")
	return w.String()
}

// TestDoS_CIDWidthsCharged: composite CFF fonts advance by their CIDFont's
// /W, read through fontmodel per font dictionary. 4,096 Type 0 dictionaries
// sharing one CIDFont of 64 Ki /W widths read and pay for it once (the
// metrics cache, by descendant and /Encoding): the page renders (72 s, past
// a 60 s deadline, uncached and uncharged). 32 dictionaries with a CIDFont
// each pay cidWidthItemCost per item each, pass the raster-work budget and
// stop with ErrRenderBudgetExceeded. Either way it ends promptly.
func TestDoS_CIDWidthsCharged(t *testing.T) {
	skipUnlessTimed(t)
	glyph := cffmin.CS(500, 10, 10, cffmin.RMoveTo, 100, 100, -100, cffmin.HLineTo, cffmin.EndChar)
	prog := cffmin.CIDProgram(glyph, 1, 16, false)
	for _, tt := range []struct {
		name       string
		fonts      int
		perFont    bool
		wantBudget bool
	}{
		{"4096 fonts, one CIDFont", 4096, false, false},
		{"32 fonts, a CIDFont each", 32, true, true},
		{"32 fonts, a CIDFont each, empty lists", 32, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			form := "lists"
			if strings.HasSuffix(tt.name, "empty lists") {
				form = "empty"
			}
			fonts := map[string]string{}
			var content strings.Builder
			content.WriteString("BT\n")
			for i := 0; i < tt.fonts; i++ {
				name := fmt.Sprintf("F%d", i)
				fonts[name] = ""
				fmt.Fprintf(&content, "/%s 1 Tf <0001> Tj\n", name)
			}
			content.WriteString("ET\n")
			pdf := cffmin.CIDPDFExtra(prog, fonts, content.String(), cidWidths(form, 65535), tt.perFont)
			err := renderT1(t, pdf)
			switch {
			case tt.wantBudget && !errors.Is(err, core.ErrRenderBudgetExceeded):
				t.Errorf("err = %v, want ErrRenderBudgetExceeded", err)
			case !tt.wantBudget && err != nil:
				t.Errorf("err = %v, want a render", err)
			}
			boundedT1(t, tt.name, pdf, 10)
		})
	}
}

// TestDoS_CIDWidthRangesIndexed: one composite CFF font whose /W is 65,535
// "c c w" ranges, none holding a shown CID, and 200,000 glyphs shown. Each
// advance is a binary search of the ranges (indexed at load), not a scan of
// all of them (21 s, unbudgeted, when it was).
func TestDoS_CIDWidthRangesIndexed(t *testing.T) {
	skipUnlessTimed(t)
	glyph := cffmin.CS(500, 10, 10, cffmin.RMoveTo, 100, 100, -100, cffmin.HLineTo, cffmin.EndChar)
	prog := cffmin.CIDProgram(glyph, 1, 16, false)
	var content strings.Builder
	content.WriteString("BT /F0 1 Tf\n")
	show := "<" + strings.Repeat("0002", 1000) + "> Tj\n"
	for i := 0; i < 200; i++ {
		content.WriteString(show)
	}
	content.WriteString("ET\n")
	pdf := cffmin.CIDPDFExtra(prog, map[string]string{"F0": ""}, content.String(), cidWidths("ranges", 65535), false)
	if err := renderT1(t, pdf); err != nil {
		t.Errorf("err = %v, want a render", err)
	}
	boundedT1(t, "200,000 glyphs over 65,535 /W ranges", pdf, 10)
}

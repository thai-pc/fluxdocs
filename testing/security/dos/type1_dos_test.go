package dos

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/internal/reltime"
	"github.com/thai-pc/fluxdocs/testing/internal/t1min"
)

// The Type 1 font gates (docs/design/type1-std14-fonts.md §5.4). A font
// program is untrusted input: its glyphs run charstrings (bounded per glyph
// at 16,384 operators, charged to raster work once per glyph per render),
// and its bytes are decoded and decrypted (charged to decoded bytes).

// The gates bound each render's CPU time relative to fontGateRef, measured in
// the same test (reltime, perTestRef), never by an absolute time.

// fontGateRef is the font gates' reference: their 200 × 200 pt page at 72 DPI
// filled until the default raster-work budget stops it (its 16 Mi floor,
// ≈ 420 fills): the most raster work a default render of the page may do.
// A font gate's render is bounded at a small multiple of it (boundedT1).
// The reference grows with the default budget, so it does not anchor the
// budget's size itself: TestDoS_DefaultBudgetBoundsPage does, against a
// normal page, and the render-package unit test TestResolveLimits pins the
// budget's values.
func fontGateRef(t *testing.T) reltime.Prepared {
	t.Helper()
	pdf := classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		streamBody(strings.Repeat("0 0 200 200 re f\n", 10_000)),
	})
	return func() func() {
		doc, err := core.OpenBytes(pdf)
		if err != nil {
			t.Fatalf("OpenBytes: %v", err)
		}
		return func() {
			if _, err := doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"}); !errors.Is(err, core.ErrRenderBudgetExceeded) {
				t.Fatalf("reference render: err = %v, want ErrRenderBudgetExceeded", err)
			}
		}
	}
}

// boundedT1 bounds the CPU time of rendering page 0 of pdf (opened afresh
// each time, untimed) at bound × fontGateRef, measured once per test
// (perTestRef): the median ratio of reltime.FewRuns runs, each repeated to
// about reltime.CPUTarget. Every font gate measures under half its bound
// (the largest, ≈ 5.8× against 16×).
func boundedT1(t *testing.T, what string, pdf []byte, bound float64) {
	t.Helper()
	ref := perTestRef(t, "the default budget's worth of fills (fontGateRef)", fontGateRef(t))
	render := func() func() {
		doc, err := core.OpenBytes(pdf)
		if err != nil {
			t.Fatalf("OpenBytes: %v", err)
		}
		return func() { doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"}) }
	}
	reltime.Check(t, what+", against the default budget's worth of fills", reltime.AgainstFixed(reltime.FewRuns, reltime.CPU, ref, render), bound)
}

func flateBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, _ := zlib.NewWriterLevel(&b, zlib.BestCompression)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// bombGlyph is a glyph whose charstring fans out through two levels of
// subroutines (40 × 40 × 40 operators) far past the per-glyph operator
// bound, after drawing a small square: every build runs, and is charged,
// 16,384 operators and is then dropped.
func bombGlyph(name string) t1min.Glyph {
	cs := t1min.CS(0, 500, t1min.HSBW, 10, 10, t1min.RMoveTo, 100, 0, t1min.RLineTo, 0, 100, t1min.RLineTo, t1min.ClosePath)
	for i := 0; i < 40; i++ {
		cs = append(cs, t1min.CS(1, t1min.CallSubr)...)
	}
	return t1min.Glyph{Name: name, CS: append(cs, t1min.CS(t1min.EndChar)...)}
}

func bombSubrs() [][]byte {
	fan := func(n int, call []byte) []byte {
		return append(bytes.Repeat(call, n), t1min.CS(t1min.Return)...)
	}
	return [][]byte{
		t1min.CS(t1min.Return),
		fan(40, t1min.CS(2, t1min.CallSubr)),
		fan(40, t1min.CS(t1min.HStem)),
	}
}

// renderT1 renders page 0 of pdf at 72 DPI with the default budget and
// returns the error; boundedT1 bounds its cost.
func renderT1(t *testing.T, pdf []byte) error {
	t.Helper()
	doc, err := core.OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	_, err = doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"})
	return err
}

// TestDoS_Type1GlyphBombIsBounded shows 10^5 glyphs of a font whose every
// glyph runs into the operator bound (16,384 operators, 256 Ki raster-work
// units at charstringOpCost 16). With 32 distinct glyphs the outline cache
// charges each build once (8 Mi units, under the 16 Mi default floor) and
// the page renders; uncached, the same page would charge 10^5 builds. With
// 4,096 (16 font dictionaries over one program, /Differences selecting 256
// glyphs each) the charged operators pass the default raster-work budget
// and the render stops with ErrRenderBudgetExceeded. Either way it ends
// promptly.
func TestDoS_Type1GlyphBombIsBounded(t *testing.T) {
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
			glyphs := make([]t1min.Glyph, 256*tt.fonts)
			for i := range glyphs {
				glyphs[i] = bombGlyph(fmt.Sprintf("g%d", i))
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
			pdf := t1min.PDF(t1min.Program(glyphs, bombSubrs(), 0), nil, fonts, content.String())
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

// TestDoS_Type1SharedProgramParsedOnce: 10,000 font dictionaries share one
// 30 MiB program (Flate-compressed to a few KiB), and the page selects every
// one. The program is decoded and decrypted once per render (the program
// cache, by stream reference); decoding it per font would pass the decoded
// bytes budget (512 MiB) after 17 fonts. (Only the first 4,096 dictionaries
// load: see TestDoS_Type1ManyFontDictsAreBounded.)
func TestDoS_Type1SharedProgramParsedOnce(t *testing.T) {
	skipUnlessTimed(t)
	prog := t1min.Program([]t1min.Glyph{bombGlyph("a")}, bombSubrs(), 30<<20)
	fonts := map[string]string{}
	var content strings.Builder
	content.WriteString("BT\n")
	for i := 0; i < 10000; i++ {
		name := fmt.Sprintf("F%d", i)
		fonts[name] = ""
		fmt.Fprintf(&content, "/%s 1 Tf <00> Tj\n", name)
	}
	content.WriteString("ET\n")
	pdf := t1min.PDF(prog, flateBytes(t, prog), fonts, content.String())
	if err := renderT1(t, pdf); err != nil {
		t.Errorf("err = %v, want a render (the program decoded once)", err)
	}
	boundedT1(t, "10,000 fonts over one 30 MiB program", pdf, 10)
}

// TestDoS_Type1ManyFontDictsAreBounded: 100,000 distinct font dictionaries
// share one small program and the page selects each once. The program is
// parsed once, but every dictionary loaded builds its own metrics and code
// map: uncharged, 200,000 of them took 7.4 s and ≈ 930 MiB. A render loads at
// most 4,096 (maxFontLoads; later fonts select nothing), each charged to
// raster work, so the page renders promptly and allocates a bounded amount.
func TestDoS_Type1ManyFontDictsAreBounded(t *testing.T) {
	skipUnlessTimed(t)
	const n = 100000
	sq := t1min.CS(0, 500, t1min.HSBW, 10, 10, t1min.RMoveTo, 100, 0, t1min.RLineTo, 0, 100, t1min.RLineTo, t1min.ClosePath, t1min.EndChar)
	prog := t1min.Program([]t1min.Glyph{{Name: "a", CS: sq}}, nil, 0)
	fonts := make(map[string]string, n)
	var content strings.Builder
	content.WriteString("BT\n")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("F%d", i)
		fonts[name] = ""
		fmt.Fprintf(&content, "/%s 1 Tf <00> Tj\n", name)
	}
	content.WriteString("ET\n")
	pdf := t1min.PDF(prog, nil, fonts, content.String())
	doc, err := core.OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"})
	runtime.ReadMemStats(&after)
	if err != nil && !errors.Is(err, core.ErrRenderBudgetExceeded) {
		t.Errorf("err = %v, want a render or ErrRenderBudgetExceeded", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 256<<20 {
		t.Errorf("render allocated %d MiB, want < 256 MiB", alloc>>20)
	}
	t.Logf("%d font dictionaries over one program: %d MiB allocated, err %v", n, (after.TotalAlloc-before.TotalAlloc)>>20, err)
	boundedT1(t, fmt.Sprintf("%d font dictionaries over one program", n), pdf, 10)
}

// TestDoS_Type1UnusedFontsAreNotLoaded: a /Font dictionary of 2,000 fonts,
// each with its own 1 MiB program (Flate bombs of a few KiB each), of which
// the page selects one. Fonts load on first Tf, so one program is decoded;
// loading every resource entry would decode 2 GiB and pass the decoded-bytes
// budget.
func TestDoS_Type1UnusedFontsAreNotLoaded(t *testing.T) {
	skipUnlessTimed(t)
	prog := t1min.Program([]t1min.Glyph{bombGlyph("a")}, bombSubrs(), 1<<20)
	z := flateBytes(t, prog)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"", // page
		streamBody("BT /F7 10 Tf 20 20 Td <00> Tj ET"),
	}
	var fontDict strings.Builder
	for i := 0; i < 2000; i++ {
		n := len(objs) + 1 // font, descriptor, program
		objs = append(objs,
			fmt.Sprintf("<< /Type /Font /Subtype /Type1 /FontDescriptor %d 0 R >>", n+1),
			fmt.Sprintf("<< /Type /FontDescriptor /FontFile %d 0 R >>", n+2),
			fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(z), z))
		fmt.Fprintf(&fontDict, " /F%d %d 0 R", i, n)
	}
	objs[2] = "<< /Type /Page /Parent 2 0 R /Resources << /Font <<" + fontDict.String() + " >> >> /Contents 4 0 R >>"
	pdf := classicPDF(objs)
	if err := renderT1(t, pdf); err != nil {
		t.Errorf("err = %v, want a render (one font loaded)", err)
	}
	boundedT1(t, "2,000 declared fonts, one used", pdf, 3)
}

func streamBody(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

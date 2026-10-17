package redact

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// runsOf returns the runs of a page analysed from a fixture.
func runsOf(t *testing.T, f fixture) []textRun {
	t.Helper()
	p, _, err := analyze(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p.runs
}

// bareRuns returns the runs of a bare content stream (no fonts).
func bareRuns(t *testing.T, content string) []textRun {
	t.Helper()
	p, err := analyzeContent([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return p.runs
}

func TestRunBoxes(t *testing.T) {
	// Helvetica 10 without /Widths: each glyph advances [0, 11] and inks up to
	// 11 past its origin; a run's box spans every position its glyphs can take.
	for _, tc := range []struct {
		name    string
		content string
		run     int
		// the box must cover [minX, maxX] × [minY, maxY]
		minX, maxX, minY, maxY float64
	}{
		{"unknown widths", "BT /F1 10 Tf 100 700 Td (ABCDE) Tj ET", 0, 100, 155, 697.5, 709},
		{"Tc spreads the glyphs (tc_spread)", "BT /F1 10 Tf 60 Tc 0 700 Td (ABCDE) Tj ET", 0, 0, 4*71 + 11, 697.5, 709},
		{"negative Tc moves left", "BT /F1 10 Tf -50 Tc 300 700 Td (ABCDE) Tj ET", 0, 300 - 200, 311, 697.5, 709},
		{"Tw on spaces", "BT /F1 10 Tf 100 Tw 0 700 Td (A B C) Tj ET", 0, 0, 200 + 4*11 + 11, 697.5, 709},
		{"\" sets Tw and Tc", "BT /F1 10 Tf 20 TL 0 700 Td 100 0 (A B) \" ET", 0, 0, 100 + 2*11 + 11, 677.5, 689},
		{"TJ adjustment", "BT /F1 10 Tf 0 700 Td [(A) -5000 (B)] TJ ET", 0, 0, 11 + 50 + 11, 697.5, 709},
		{"nested TJ string", "BT /F1 10 Tf 0 700 Td [(A) [(BBBB)] (C)] TJ ET", 0, 0, 6 * 11, 697.5, 709},
		{"Ts raises the box", "BT /F1 10 Tf 100 Ts 0 700 Td (A) Tj ET", 0, 0, 11, 797.5, 809},
		{"later run on the same line (interval pen)", "BT /F1 10 Tf 0 700 Td (WWWW) Tj (X) Tj ET", 1, 0, 55, 697.5, 709},
		{"pen resets on Td", "BT /F1 10 Tf 0 700 Td (WWWW) Tj 0 -20 Td (X) Tj ET", 1, 0, 11, 677.5, 689},
		{"gs /Font sets font and size (N6)", "BT /GS1 gs 72 700 Td (AB) Tj ET", 0, 72, 72 + 2*26.4, 694, 721.6},
		{"rotated by cm", "q 0 1 -1 0 0 0 cm BT /F1 10 Tf 100 100 Td (AB) Tj ET Q", 0, -109, -97.5, 100, 122},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := runsOf(t, fixture{content: tc.content,
				res: "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /Font [5 0 R 24] >> >> >>"})
			if tc.run >= len(runs) {
				t.Fatalf("%d runs", len(runs))
			}
			b := runs[tc.run].box
			const eps = 1e-6
			if runs[tc.run].nonFinite || b[0] > tc.minX+eps || b[2] < tc.maxX-eps || b[1] > tc.minY+eps || b[3] < tc.maxY-eps {
				t.Errorf("box %v does not cover [%g, %g] × [%g, %g]", b, tc.minX, tc.maxX, tc.minY, tc.maxY)
			}
		})
	}
}

func TestRunBoxesWithMetrics(t *testing.T) {
	res := "<< /Font << /W 6 0 R /V 7 0 R /T3 9 0 R >> >>"
	extra := map[int]string{
		// 'W' is 3 em wide by /Widths; 'i' 0.25 em.
		// Embedded (/FontFile2), so no substitute's ink widens it.
		6:  "<< /Type /Font /Subtype /TrueType /BaseFont /X /FirstChar 87 /LastChar 105 /Widths [3000 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 250] /FontDescriptor 10 0 R >>",
		10: "<< /FontFile2 11 0 R >>",
		11: stm("", "x"),
		7:  "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-V /DescendantFonts [8 0 R] >>",
		8:  "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X >>",
		9:  "<< /Type /Font /Subtype /Type3 /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << >> >>",
	}
	runs := runsOf(t, fixture{content: "BT /W 10 Tf 0 700 Td (WW) Tj (i) Tj ET " +
		"BT /V 10 Tf 200 400 Td <000100020003> Tj ET " +
		"BT /T3 10 Tf 50 50 Td (a) Tj ET", res: res, extra: extra})
	if len(runs) != 4 {
		t.Fatalf("%d runs", len(runs))
	}
	// /Widths are exact: the second run starts at exactly 60, so its box
	// reaches 60 + 11 (the em bound exceeds 'i' 2.5) and no further left.
	if b := runs[0].box; b[2] < 60 {
		t.Errorf("wide-glyph run box %v, want past x 60", b)
	}
	if b := runs[1].box; b[0] != 60 || b[2] < 71 {
		t.Errorf("run after exact widths: box %v, want from x 60", b)
	}
	// Vertical writing: three glyphs may run 33 below and above the origin.
	if b := runs[2].box; b[1] > 400-33 || b[3] < 400+12 || b[0] > 200-11 {
		t.Errorf("vertical run box %v, want it to cover glyphs below the origin", b)
	}
	// A Type 3 font without /FontBBox cannot be bounded.
	if !runs[3].nonFinite {
		t.Errorf("unbounded Type 3 run box %v, want non-finite", runs[3].box)
	}
}

func TestRunsIntersect(t *testing.T) {
	big := "1" + strings.Repeat("0", 50) + ".0" // 1e50; eight cm overflow to Inf
	runs := bareRuns(t, "BT /F1 10 Tf 100 700 Td (A) Tj ET "+
		"BT /F1 0 Tf 100 600 Td (B) Tj ET "+
		"BT /F1 10 Tf 0 Tz 100 500 Td (C) Tj ET "+
		"BT /F1 10 Tf 0 0 0 0 100 400 Tm (D) Tj ET "+
		"q "+strings.Repeat(big+" 0 0 "+big+" 0 0 cm ", 8)+"BT /F1 10 Tf (E) Tj ET Q")
	if len(runs) != 5 {
		t.Fatalf("%d runs", len(runs))
	}
	// Closed intervals: a region touching the box edge meets it.
	edge := Rect{X: runs[0].box[2], Y: 0, Width: 50, Height: 800}
	if !runs[0].intersects(edge) {
		t.Errorf("run %v does not meet a region touching its right edge", runs[0].box)
	}
	// Degenerate runs meet a region containing them.
	for i, at := range []float64{600, 500, 400} {
		r := runs[i+1]
		if r.nonFinite {
			t.Errorf("degenerate run %d is non-finite", i+1)
		}
		if !r.intersects(Rect{X: 90, Y: at - 5, Width: 20, Height: 10}) {
			t.Errorf("degenerate run %d box %v misses the region around it", i+1, r.box)
		}
		if r.intersects(Rect{X: 300, Y: at - 5, Width: 20, Height: 10}) {
			t.Errorf("degenerate run %d box %v meets a far region", i+1, r.box)
		}
	}
	// An overflowing run meets every region.
	if !runs[4].nonFinite || !runs[4].intersects(Rect{X: 1, Y: 1, Width: 1, Height: 1}) {
		t.Errorf("overflowing run %v, want non-finite and meeting every region", runs[4].box)
	}
	if !math.IsInf(runs[4].box[2], 0) && !math.IsNaN(runs[4].box[2]) {
		t.Errorf("overflowing run box %v, want an infinite edge", runs[4].box)
	}
}

func TestMatch(t *testing.T) {
	re := regexp.MustCompile(`SECRET-\d+`)
	identity := map[int]string{
		6: "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /ToUnicode 7 0 R /DescendantFonts [8 0 R] >>",
		7: stm("", "1 begincodespacerange <0000> <FFFF> endcodespacerange 1 beginbfrange <0020> <007E> <0020> endbfrange"),
		8: "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X >>",
		9: "<< /Type /Font /Subtype /Type1 /BaseFont /X /Encoding << /Differences [65 /S /E /C /R /T /hyphen /one /two /three /four] >> >>",
	}
	hex := func(s string) string {
		var b strings.Builder
		b.WriteByte('<')
		for _, r := range s {
			fmt.Fprintf(&b, "%04X", r)
		}
		b.WriteByte('>')
		return b.String()
	}
	for _, tc := range []struct {
		name, content string
		matches       int
	}{
		{"Identity-H via /ToUnicode (N1)", "BT /H 24 Tf 72 700 Td " + hex("SECRET-1234") + " Tj ET", 1},
		{"kerned TJ (N1)", "BT /F1 24 Tf 72 700 Td [(SEC) -20 (RET-1234)] TJ ET", 1},
		{"word gap inside the match", "BT /F1 24 Tf 72 700 Td [(SECRET-) -300 (1234)] TJ ET", 1},
		{"nested TJ (C6)", "BT /F1 24 Tf 72 700 Td [(A) [(SECRET-1234)]] TJ ET", 1},
		{"zero font size", "BT /F1 0 Tf 72 700 Td (SECRET-1234) Tj ET", 1},
		{"/Differences glyph names", "BT /D 24 Tf 72 700 Td (ABCDBEFGHIJ) Tj ET", 1},
		{"no match", "BT /F1 24 Tf 72 700 Td (public) Tj ET", 0},
		{"split across operators (Phase 2)", "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (1234) Tj ET", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := analyze(t, fixture{content: tc.content, res: "<< /Font << /F1 5 0 R /H 6 0 R /D 9 0 R >> >>", extra: identity}, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := p.Match(re)
			if len(m.Rects) != tc.matches || len(m.Marks) != tc.matches {
				t.Fatalf("%d rects, %d marks, want %d", len(m.Rects), len(m.Marks), tc.matches)
			}
			if tc.matches == 0 {
				return
			}
			res, err := p.Rewrite(nil, nil, m.Marks)
			if err != nil {
				t.Fatal(err)
			}
			out := res.Content
			if !strings.Contains(string(out), "BT") || strings.Contains(string(out), "Tj") || strings.Contains(string(out), "TJ") {
				t.Errorf("matched operator not removed by its mark alone: %q", out)
			}
			if len(m.Needles) == 0 {
				t.Error("no needle recorded")
			}
		})
	}
}

func TestPropertyListMatch(t *testing.T) {
	re := regexp.MustCompile(`SECRET-\d+`)
	utf16 := "<FEFF" + "0053004500430052004500540028" + ">" // "SECRET(" — then a PDFDocEncoding row below
	for _, tc := range []struct {
		name, content, res string
		extra              map[int]string
		marks, rects       int    // marks: stripped keys + removed runs
		gone, kept         string // in the rewritten content
		named              bool   // the strip is in /Properties
	}{
		{"inline /ActualText", "/Span << /ActualText (SECRET-1234) /MCID 3 >> BDC BT /F1 9 Tf (XXX) Tj ET EMC BT (keep) Tj ET",
			"", nil, 2, 1, "SECRET", "/MCID 3", false},
		{"inline /Alt, no runs", "/Figure << /Alt (SECRET-1234) >> BDC 0 0 1 1 re f EMC", "", nil, 1, 0, "SECRET", "/Figure", false},
		{"inline /E via DP", "/Span << /E (SECRET-1234) >> DP BT (keep) Tj ET", "", nil, 1, 0, "SECRET", "keep", false},
		{"UTF-16BE value", "/Span << /ActualText " + strings.Replace(utf16, "0028", "002D0031", 1) + " >> BDC EMC", "", nil, 1, 0, "FEFF", "/Span", false},
		{"PDFDocEncoding value", "/Span << /ActualText (\x93SECRET-1) >> BDC EMC", "", nil, 1, 0, "SECRET", "/Span", false},
		{"nested sequences", "/Span << /ActualText (SECRET-1) >> BDC BT (a) Tj ET /Span << /MCID 1 >> BDC BT (b) Tj ET EMC EMC BT (keep) Tj ET",
			"", nil, 3, 1, "(b)", "keep", false},
		{"named /Properties", "/Span /MC0 BDC BT (XXX) Tj ET EMC", "<< /Properties << /MC0 6 0 R >> >>",
			map[int]string{6: "<< /ActualText (SECRET-1234) /Lang (en) >>"}, 2, 1, "XXX", "/MC0", true},
		// No match marks nothing; once the page is redacted for another
		// reason, D7 strips every text key anyway.
		{"no match marks nothing", "/Span << /ActualText (public) >> BDC BT (x) Tj ET EMC", "", nil, 0, 0, "public", "/Span", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := analyze(t, fixture{content: tc.content, res: tc.res, extra: tc.extra}, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := p.Match(re)
			if len(m.Marks) != tc.marks || len(m.Rects) != tc.rects {
				t.Fatalf("%d marks, %d rects; want %d, %d", len(m.Marks), len(m.Rects), tc.marks, tc.rects)
			}
			rw, err := p.Rewrite(nil, nil, m.Marks)
			if err != nil {
				t.Fatal(err)
			}
			out, res := rw.Content, rw.Resources
			if tc.gone != "" && strings.Contains(string(out), tc.gone) {
				t.Errorf("%q survives: %q", tc.gone, out)
			}
			if !strings.Contains(string(out), tc.kept) {
				t.Errorf("%q lost: %q", tc.kept, out)
			}
			if _, err := parse.ScanContentSpansStrict(out); err != nil {
				t.Errorf("rewritten content does not scan: %v (%q)", err, out)
			}
			if tc.named {
				if res == nil {
					t.Fatal("no page-private resources for a named strip")
				}
				if s := string(parse.SerializeObject(res)); strings.Contains(s, "SECRET") || !strings.Contains(s, "/Lang") {
					t.Errorf("resources %s: want /ActualText stripped and /Lang kept", s)
				}
			}
		})
	}
}

func TestRewriteRejectsForeignMarks(t *testing.T) {
	p, err := analyzeContent([]byte("BT (a) Tj ET"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rewrite(nil, nil, []Mark{{start: 1, end: 2}}); err == nil {
		t.Error("Rewrite accepted a mark naming no operator")
	}
	if _, err := p.Rewrite(nil, nil, []Mark{{start: 3, end: 10, strip: "Alt"}}); err == nil {
		t.Error("Rewrite accepted a strip naming no property list")
	}
}

func TestTextString(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                    "plain",
		"\xFE\xFF\x00S\x00E":       "SE",
		"\xEF\xBB\xBFS\xC3\xA9":    "Sé",
		"\x93a\x8D":                "ﬁa“",
		"\xFF\xFES\x00E\x00":       "SE",
		"\xFE\xFF\xD8\x3D\xDE\x00": "\U0001F600",
	} {
		if got := textString([]byte(in)); got != want {
			t.Errorf("textString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRewriteNormalizesRects(t *testing.T) {
	out, err := redactContent([]byte("BT /F1 12 Tf 100 700 Td (SECRET) Tj ET"), []Rect{{X: 200, Y: 720, Width: -150, Height: -40}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SECRET") {
		t.Errorf("a region with negative size missed the run: %q", out)
	}
}

func TestRewriteQuoteOperatorsKeepLineMove(t *testing.T) {
	content := "BT /F1 24 Tf 30 TL 72 700 Td (keep1) Tj 0.5 -1.25 (SECRET) \" (SECRET) ' (keep2) Tj ET"
	p, err := analyzeContent([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	m := p.Match(regexp.MustCompile("SECRET"))
	rw, err := p.Rewrite(nil, nil, m.Marks)
	if err != nil {
		t.Fatal(err)
	}
	out := rw.Content
	want := "BT /F1 24 Tf 30 TL 72 700 Td (keep1) Tj 0.5 Tw -1.25 Tc T*  T*  (keep2) Tj ET"
	if string(out) != want {
		t.Errorf("rewritten\n%q, want\n%q", out, want)
	}
	// keep2 is still shown two lines below keep1, as in the input.
	q, err := analyzeContent(out)
	if err != nil {
		t.Fatal(err)
	}
	if y := q.runs[len(q.runs)-1].box[1]; y > 640-6+1e-9 || y < 640-6-1e-9 {
		t.Errorf("keep2 box bottom %g, want 634 (baseline 640)", y)
	}
}

func TestMatchRectsAreDisplayBoxes(t *testing.T) {
	// A tall /FontBBox widens the removal box but not the RedactText rect,
	// which is display only: lines 13.8 apart are not covered (S1).
	f := fixture{content: "BT /A 12 Tf 13.8 TL 72 700 Td (above) Tj T* (SECRET-1) Tj T* (below) Tj ET",
		res: "<< /Font << /A 6 0 R >> >>",
		extra: map[int]string{6: "<< /Type /Font /Subtype /TrueType /BaseFont /A /FirstChar 32 /Widths [556] /FontDescriptor 7 0 R >>",
			7: "<< /FontBBox [-665 -325 2000 1006] >>"}}
	p, _, err := analyze(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := p.Match(regexp.MustCompile(`SECRET-\d`))
	if len(m.Rects) != 1 {
		t.Fatalf("rects %v", m.Rects)
	}
	r := m.Rects[0]
	if r.Y < 686.2-3.01 || r.Y+r.Height > 686.2+10.81 {
		t.Errorf("display rect %+v reaches past the em extent of its line", r)
	}
	if box := p.runs[1].box; box[3] < 686.2+12 {
		t.Errorf("removal box %v ignores the /FontBBox", box)
	}
	rw, err := p.Rewrite(nil, m.Rects, m.Marks)
	if err != nil {
		t.Fatal(err)
	}
	out := rw.Content
	if !strings.Contains(string(out), "(above)") || !strings.Contains(string(out), "(below)") {
		t.Errorf("neighbouring lines removed: %q", out)
	}
}

func TestRewriteRejectsMarksOfAnotherPage(t *testing.T) {
	two := testPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: stm("", "BT (SECRET) Tj ET"),
		5: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>", // same content, so the offsets fit
	})
	r, err := parse.NewResolver(two)
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := r.Pages()
	seg, _ := r.PageContentSegments(pages[0])
	p0, err := AnalyzePage(r, pages[0], seg)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := AnalyzePage(r, pages[1], seg)
	if err != nil {
		t.Fatal(err)
	}
	m := p0.Match(regexp.MustCompile("SECRET"))
	if _, err := p1.Rewrite(nil, nil, m.Marks); !errors.Is(err, ErrMarkMismatch) {
		t.Errorf("page 0's marks on page 1: %v, want ErrMarkMismatch", err)
	}
	again, err := AnalyzePage(r, pages[0], seg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Rewrite(nil, nil, m.Marks); err != nil {
		t.Errorf("marks on the same page analysed again: %v", err)
	}
}

func TestType3D1Width(t *testing.T) {
	// /Widths say 0, d1 says one em: the box covers the d1 advance.
	f := fixture{content: "BT /T3 20 Tf 10 700 Td (aaaa) Tj ET", res: "<< /Font << /T3 6 0 R >> >>", extra: map[int]string{
		6: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 500 500] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << /a 7 0 R >> " +
			"/Encoding << /Differences [97 /a] >> /FirstChar 97 /LastChar 97 /Widths [0] >>",
		7: stm("", "1000 0 0 0 500 500 d1 0 0 500 500 re f")}}
	runs := runsOf(t, f)
	if b := runs[0].box; b[2] < 10+3*20+10 {
		t.Errorf("box %v, want the fourth glyph at x 70 (d1 width) covered", b)
	}
}

package redact

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// rewriteArea analyses a fixture and rewrites it with one region.
func rewriteArea(t *testing.T, f fixture, region Rect) Output {
	t.Helper()
	p, _, err := analyze(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Rewrite([]Rect{region}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parse.ScanContentSpansStrict(out.Content); err != nil {
		t.Fatalf("rewritten content does not scan: %v (%q)", err, out.Content)
	}
	return out
}

// TestRewriteRemovesWhatMeetsRegions pins the D5 rules (§6.10): what the page
// paints besides text is removed whole when its box meets a region (closed
// intervals) and kept otherwise.
func TestRewriteRemovesWhatMeetsRegions(t *testing.T) {
	img := map[int]string{6: stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x80")}
	form := func(dict string) map[int]string {
		return map[int]string{6: stm("/Type /XObject /Subtype /Form "+dict, "0 0 10 10 re f")}
	}
	xres := "<< /XObject << /X 6 0 R >> >>"
	for _, tc := range []struct {
		name    string
		f       fixture
		region  Rect
		gone    []string // must not survive
		kept    []string // must survive
		usesX   bool     // /X stays in the resources
		wantOut string   // exact content, when set
	}{
		{"image under the region", fixture{content: "q 100 0 0 100 50 50 cm /X Do Q", res: xres, extra: img},
			Rect{X: 140, Y: 140, Width: 5, Height: 5}, []string{"/X Do"}, []string{"q", "cm"}, false, ""},
		{"image touching the region's edge", fixture{content: "q 100 0 0 100 50 50 cm /X Do Q", res: xres, extra: img},
			Rect{X: 150, Y: 150, Width: 5, Height: 5}, []string{"/X Do"}, nil, false, ""},
		{"image beside the region", fixture{content: "q 100 0 0 100 50 50 cm /X Do Q", res: xres, extra: img},
			Rect{X: 151, Y: 151, Width: 5, Height: 5}, nil, []string{"/X Do"}, true, ""},
		{"form by /BBox and /Matrix", fixture{content: "/X Do", res: xres, extra: form("/BBox [0 0 10 10] /Matrix [1 0 0 1 300 300]")},
			Rect{X: 305, Y: 305, Width: 1, Height: 1}, []string{"/X Do"}, nil, false, ""},
		{"form elsewhere", fixture{content: "/X Do", res: xres, extra: form("/BBox [0 0 10 10] /Matrix [1 0 0 1 300 300]")},
			Rect{X: 0, Y: 0, Width: 10, Height: 10}, nil, []string{"/X Do"}, true, ""},
		{"form without a readable /BBox meets every region", fixture{content: "/X Do", res: xres, extra: form("/BBox [0 0 10]")},
			Rect{X: 0, Y: 0, Width: 1, Height: 1}, []string{"/X Do"}, nil, false, ""},
		{"inline image", fixture{content: "q 10 0 0 10 20 20 cm BI /W 1 /H 1 /BPC 8 /CS /G ID \x80 EI Q"},
			Rect{X: 25, Y: 25, Width: 1, Height: 1}, []string{"BI", "EI"}, []string{"q", "Q"}, false, ""},
		{"filled path", fixture{content: "0 0 1 rg 10 10 m 50 10 l 50 50 l h f 1 0 0 rg 200 200 20 20 re f"},
			Rect{X: 30, Y: 30, Width: 1, Height: 1}, nil, nil, false, "0 0 1 rg      1 0 0 rg 200 200 20 20 re f"},
		{"stroke reaches past its path", fixture{content: "5 w 1 M 100 100 m 200 100 l S"},
			Rect{X: 150, Y: 103, Width: 1, Height: 1}, []string{" S", " m", " l"}, nil, false, ""},
		{"stroke kept clear of the region", fixture{content: "1 w 1 M 100 100 m 200 100 l S"},
			Rect{X: 150, Y: 103, Width: 1, Height: 1}, nil, []string{"m", "l", "S"}, false, ""},
		{"clipping path keeps its clip", fixture{content: "q 10 10 50 50 re W f /X Do Q", res: xres, extra: img},
			Rect{X: 20, Y: 20, Width: 1, Height: 1}, []string{" f"}, []string{"re W n", "/X Do"}, true, ""},
		{"clip-only path is kept", fixture{content: "q 10 10 50 50 re W n Q"},
			Rect{X: 20, Y: 20, Width: 1, Height: 1}, nil, []string{"re W n"}, false, ""},
		{"sh inside a clip that meets the region", fixture{content: "q 10 10 50 50 re W n /Sh sh Q", res: "<< /Shading << /Sh 7 0 R >> >>",
			extra: map[int]string{7: "<< /ShadingType 2 /ColorSpace /DeviceGray /Coords [0 0 1 0] /Function << /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >> >>"}},
			Rect{X: 20, Y: 20, Width: 1, Height: 1}, []string{"sh"}, []string{"re W n"}, false, ""},
		// The clip is not tracked (C46): FluxDocs' renderer paints a
		// shading over the whole page whatever the W clip, so sh is bounded
		// by the page only.
		{"sh under a W clip away from the region still meets it", fixture{content: "q 10 10 50 50 re W n /Sh sh Q", res: "<< /Shading << /Sh 7 0 R >> >>",
			extra: map[int]string{7: "<< /ShadingType 2 >>"}},
			Rect{X: 100, Y: 100, Width: 1, Height: 1}, []string{"sh"}, []string{"re W n"}, false, ""},
		{"sh meets every region on the page", fixture{content: "/Sh sh", res: "<< /Shading << /Sh 7 0 R >> >>",
			extra: map[int]string{7: "<< /ShadingType 2 >>"}},
			Rect{X: 399, Y: 799, Width: 1, Height: 1}, []string{"sh"}, nil, false, ""},
		{"sh is bounded by the page", fixture{content: "/Sh sh", res: "<< /Shading << /Sh 7 0 R >> >>",
			extra: map[int]string{7: "<< /ShadingType 2 >>"}},
			Rect{X: 1000, Y: 1000, Width: 1, Height: 1}, nil, []string{"/Sh sh"}, false, ""},
		{"path built under two CTMs meets every region", fixture{content: "q 10 10 m 1 0 0 1 500 500 cm 20 20 l S Q"},
			Rect{X: 900, Y: 900, Width: 1, Height: 1}, []string{" S"}, nil, false, ""},
		// B1 (batch C review): the CTM changes between the last point and the
		// painting operator; Ghostscript maps the points with the CTM at
		// painting (here the fill covers x 30–40), others with the CTM of each
		// point (x 300–400).
		{"cm before the fill", fixture{content: "10 0 0 10 0 0 cm 30 10 m 30 70 l 40 70 l h 0.1 0 0 0.1 0 0 cm 1 0 0 rg f"},
			Rect{X: 34, Y: 40, Width: 2, Height: 2}, []string{" f"}, nil, false, ""},
		{"cm before the stroke", fixture{content: "1 M 300 100 m 300 700 l 10 0 0 10 0 0 cm 5 w S"},
			Rect{X: 315, Y: 300, Width: 5, Height: 10}, []string{" S"}, nil, false, ""},
		{"q cm before the stroke", fixture{content: "1 M 300 100 m 300 700 l q 10 0 0 10 0 0 cm 5 w S Q"},
			Rect{X: 315, Y: 300, Width: 5, Height: 10}, []string{" S"}, nil, false, ""},
		{"cm before W n: the clip is unknown, so sh meets the region", fixture{content: "q 100 100 50 50 re W 10 0 0 10 0 0 cm n /Sh sh Q",
			res: "<< /Shading << /Sh 7 0 R >> >>", extra: map[int]string{7: "<< /ShadingType 2 >>"}},
			Rect{X: 300, Y: 700, Width: 1, Height: 1}, []string{"sh"}, []string{"re W"}, false, ""},
		// B2: an ExtGState sets the line width (and miter limit).
		{"gs /LW widens the stroke", fixture{content: "/GS1 gs 300 100 m 300 700 l S",
			res: "<< /ExtGState << /GS1 << /LW 50 /ML 1 >> >> >>"},
			Rect{X: 315, Y: 300, Width: 5, Height: 10}, []string{" S"}, nil, false, ""},
		{"gs /LW from a fallback scope widens too", fixture{content: "/GS1 gs 300 100 m 300 700 l S",
			res: "<< /ExtGState << >> >>", pagesRes: "<< /ExtGState << /GS1 << /LW 50 >> >> >>"},
			Rect{X: 315, Y: 300, Width: 5, Height: 10}, []string{" S"}, nil, false, ""},
		{"gs /LW not a number meets every region", fixture{content: "/GS1 gs 300 100 m 300 700 l S",
			res: "<< /ExtGState << /GS1 << /LW /Big >> >> >>"},
			Rect{X: 0, Y: 0, Width: 1, Height: 1}, []string{" S"}, nil, false, ""},
		// No clip narrows a paint (C46, reverting C44): FluxDocs' renderer
		// ignores W/W* and text clips, so what a clip hides is still shown in
		// our viewer, and readers disagree on edge cases (Tr 4.5, a clip
		// with no point).
		{"text clip does not bound a later fill", fixture{content: "BT 7 Tr /F1 48 Tf 50 50 Td (CLIP) Tj ET 0 0 612 792 re f"},
			Rect{X: 340, Y: 340, Width: 5, Height: 5}, []string{"re f"}, []string{"(CLIP) Tj"}, false, ""},
		{"W clip does not bound an image", fixture{content: "q 0 0 50 50 re W n 612 0 0 792 0 0 cm /X Do Q", res: xres, extra: img},
			Rect{X: 340, Y: 340, Width: 5, Height: 5}, []string{"/X Do"}, []string{"re W n"}, false, ""},
		{"a clip with no point does not bound an image", fixture{content: "q h W n 612 0 0 792 0 0 cm /X Do Q", res: xres, extra: img},
			Rect{X: 340, Y: 340, Width: 5, Height: 5}, []string{"/X Do"}, nil, false, ""},
		// N1: a hairline under a shrinking CTM still paints a device pixel.
		{"hairline reaches 1 pt", fixture{content: "0.01 0 0 0.01 0 0 cm 0 w 1 M 10000 10000 m 20000 10000 l S"},
			Rect{X: 150, Y: 100.8, Width: 1, Height: 1}, []string{" S"}, nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := rewriteArea(t, tc.f, tc.region)
			got := string(out.Content)
			if tc.wantOut != "" && got != tc.wantOut {
				t.Errorf("content %q, want %q", got, tc.wantOut)
			}
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survives: %q", g, got)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q lost: %q", k, got)
				}
			}
			xo, _ := out.Resources["XObject"].(parse.Dict)
			if _, has := xo["X"]; has != tc.usesX {
				t.Errorf("/XObject /X kept = %v, want %v (resources %v)", has, tc.usesX, out.Resources)
			}
		})
	}
}

// TestRewriteResourceGC: the page-private resources keep only what the
// surviving content, and the units it invokes, can use (§5.5, N4).
func TestRewriteResourceGC(t *testing.T) {
	f := fixture{
		content: "/GS1 gs /CS0 cs /P1 scn BT /F1 9 Tf (a) Tj ET /Fm1 Do /Span /MC0 BDC EMC q 1 0 0 1 300 300 cm /Im9 Do Q",
		res: "<< /Font << /F1 5 0 R /F2 5 0 R >> /ExtGState << /GS1 << /CA 1 >> /GS2 << /CA 1 >> >> " +
			"/ColorSpace << /CS0 /DeviceRGB /CS1 /DeviceRGB /DefaultRGB /DeviceRGB >> /Pattern << /P1 8 0 R /P2 8 0 R >> " +
			"/XObject << /Fm1 6 0 R /Fm2 6 0 R /Im9 9 0 R /FmUnused 7 0 R >> /Properties << /MC0 << /MCID 0 >> /MC1 << /MCID 1 >> >> " +
			"/Shading << /Sh 10 0 R >> /ProcSet [/PDF /Text] /Unknown << /X 7 0 R >> >>",
		extra: map[int]string{
			// Fm1 has no /Resources: the names it uses come from the page.
			6: stm("/Type /XObject /Subtype /Form /BBox [0 0 1 1]", "/GS2 gs 0 0 1 1 re f"),
			7: stm("/Type /XObject /Subtype /Form /BBox [0 0 1 1] /Resources << /Font << /F1 5 0 R >> >>", "BT /F1 9 Tf (SECRET) Tj ET"),
			8: stm("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 1 1] /XStep 1 /YStep 1 /Resources << >>", "0 0 1 1 re f"),
			9: stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x80"),
		},
	}
	out := rewriteArea(t, f, Rect{X: 300, Y: 300, Width: 1, Height: 1}) // removes the image only
	want := map[parse.Name][]parse.Name{
		"Font":       {"F1"},
		"ExtGState":  {"GS1", "GS2"},
		"ColorSpace": {"CS0", "DefaultRGB"},
		"Pattern":    {"P1"},
		"XObject":    {"Fm1"},
		"Properties": {"MC0"},
	}
	for cat, names := range want {
		sub, _ := out.Resources[cat].(parse.Dict)
		if len(sub) != len(names) {
			t.Errorf("/%s = %v, want exactly %v", cat, sub, names)
		}
		for _, n := range names {
			if _, ok := sub[n]; !ok {
				t.Errorf("/%s lost /%s", cat, n)
			}
		}
	}
	for _, k := range []parse.Name{"Shading", "Unknown"} {
		if _, ok := out.Resources[k]; ok {
			t.Errorf("/%s kept: nothing uses it", k)
		}
	}

	if ps, _ := out.Resources["ProcSet"].(parse.Array); len(ps) != 2 {
		t.Errorf("/ProcSet = %v, want it kept", out.Resources["ProcSet"])
	}

	// A removed sh drops its shading; one kept keeps it (a region off the
	// page meets no sh).
	sh := fixture{content: "q 10 10 5 5 re W n /Sh1 sh Q",
		res: "<< /Shading << /Sh1 7 0 R /Sh2 7 0 R >> >>", extra: map[int]string{7: "<< /ShadingType 2 >>"}}
	if out := rewriteArea(t, sh, Rect{X: 102, Y: 102, Width: 1, Height: 1}); out.Resources["Shading"] != nil {
		t.Errorf("/Shading = %v, want it dropped with the removed sh", out.Resources["Shading"])
	}
	if out := rewriteArea(t, sh, Rect{X: 1000, Y: 1000, Width: 1, Height: 1}); len(out.Resources["Shading"].(parse.Dict)) != 1 {
		t.Errorf("/Shading = %v, want only /Sh1", out.Resources["Shading"])
	}
}

// TestRewriteStripsEveryTextKey: on a redacted page every /ActualText, /Alt
// and /E is stripped (D7), inline and named, matched or not, and their values
// are returned for verification.
func TestRewriteStripsEveryTextKey(t *testing.T) {
	f := fixture{
		content: "/Span << /ActualText (one) /Lang (en) >> BDC EMC /Figure << /Alt (two) >> DP /Span /MC0 BDC EMC /Span << /MCID 4 >> BDC EMC",
		res:     "<< /Properties << /MC0 6 0 R >> >>",
		extra:   map[int]string{6: "<< /E (three) /MCID 2 >>"},
	}
	out := rewriteArea(t, f, Rect{X: -1, Y: -1, Width: 0, Height: 0})
	got := string(out.Content)
	for _, s := range []string{"one", "two", "/ActualText", "/Alt"} {
		if strings.Contains(got, s) {
			t.Errorf("%q survives: %q", s, got)
		}
	}
	for _, s := range []string{"/Lang", "/MCID 4", "/MC0 BDC"} {
		if !strings.Contains(got, s) {
			t.Errorf("%q lost: %q", s, got)
		}
	}
	props, _ := out.Resources["Properties"].(parse.Dict)
	mc0, _ := props["MC0"].(parse.Dict)
	if _, has := mc0["E"]; has || mc0["MCID"] != parse.Integer(2) {
		t.Errorf("/MC0 = %v, want /E stripped and /MCID kept", props["MC0"])
	}
	if strings.Join(out.Stripped, ",") != "one,two,three" {
		t.Errorf("stripped values %q, want one, two, three", out.Stripped)
	}
}

// TestRewriteDropsUnconsumedState: a pattern colour, colour space or
// ExtGState that only removed paint used goes with it, and the resource it
// names (B3): an image inside the pattern or soft mask must not survive. One
// a surviving paint uses stays.
func TestRewriteDropsUnconsumedState(t *testing.T) {
	pat := map[int]string{
		6: stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "\x80"),
		7: stm("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10 /Resources << /XObject << /Im1 6 0 R >> >>", "/Im1 Do"),
		8: stm("/Type /XObject /Subtype /Form /BBox [0 0 10 10] /Group << /S /Transparency >> /Resources << /XObject << /Im1 6 0 R >> >>", "/Im1 Do"),
	}
	res := "<< /Font << /F1 5 0 R >> /Pattern << /P1 7 0 R >> /ExtGState << /GS1 << /SMask << /S /Luminosity /G 8 0 R >> >> >> >>"
	for _, tc := range []struct {
		name, content string
		region        Rect
		gone, kept    []string
		removed       []int // Output.Removed
	}{
		{"pattern fill removed", "/Pattern cs /P1 scn 0 0 100 100 re f", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			[]string{"/P1 scn", " f"}, nil, []int{6, 7}}, // the pattern and the image it paints ("/Pattern cs" names no resource: kept)
		{"pattern fill kept elsewhere", "/Pattern cs /P1 scn 0 0 100 100 re f 300 300 10 10 re f", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			nil, []string{"/P1 scn", "300 300 10 10 re f"}, nil},
		{"pattern used by later text", "/Pattern cs /P1 scn 0 0 100 100 re f BT /F1 9 Tf 300 300 Td (x) Tj ET", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			nil, []string{"/P1 scn", "(x) Tj"}, nil},
		{"stroke does not use the fill pattern", "/Pattern cs /P1 scn 0 0 100 100 re f 300 300 m 310 310 l S", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			[]string{"/P1 scn"}, []string{" S"}, []int{6, 7}},
		{"soft mask used only by removed paint", "/GS1 gs 0 0 100 100 re f", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			[]string{"/GS1 gs"}, nil, []int{6, 8}},
		{"soft mask used by kept paint", "/GS1 gs 0 0 100 100 re f 300 300 10 10 re f", Rect{X: 50, Y: 50, Width: 1, Height: 1},
			nil, []string{"/GS1 gs"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := rewriteArea(t, fixture{content: tc.content, res: res, extra: pat}, tc.region)
			got := string(out.Content)
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survives: %q", g, got)
				}
			}
			for _, k := range tc.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q lost: %q", k, got)
				}
			}
			if fmt.Sprint(out.Removed) != fmt.Sprint(tc.removed) {
				t.Errorf("removed objects %v, want %v", out.Removed, tc.removed)
			}
		})
	}
}

// TestRewriteSubstitutedGlyphInk: a region on ink a reader paints for a
// non-embedded standard 14 font, outside the em defaults, removes the run
// (batch C review round 3, SF-2; Ghostscript paints Helvetica's Å up to y 293
// and Times-Italic's f from x 83 here).
func TestRewriteSubstitutedGlyphInk(t *testing.T) {
	for _, tc := range []struct {
		font, text string
		region     Rect
	}{
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>", "\\305", Rect{X: 127, Y: 292, Width: 0.5, Height: 0.5}},
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Times-Italic >>", "f", Rect{X: 91, Y: 179, Width: 0.5, Height: 0.5}},
		// Round 5 (S-2): glyphs Ghostscript's substitutes draw past the Adobe
		// boxes, named through /Differences (100 pt at (100, 200)).
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /Aringacute] >> >>", "A", Rect{X: 130, Y: 316, Width: 0.5, Height: 0.5}},
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /Scommaaccent] >> >>", "A", Rect{X: 130, Y: 163.5, Width: 0.5, Height: 0.5}},
		{"<< /Type /Font /Subtype /Type1 /BaseFont /Bookman-LightItalic /Encoding << /Differences [65 /AE] >> >>", "A", Rect{X: 225, Y: 230, Width: 0.5, Height: 0.5}},
	} {
		out := rewriteArea(t, fixture{content: "BT /G 100 Tf 100 200 Td (" + tc.text + ") Tj ET",
			res: "<< /Font << /G 6 0 R >> >>", extra: map[int]string{6: tc.font}}, tc.region)
		if strings.Contains(string(out.Content), "Tj") {
			t.Errorf("%s: the run survives a region on its ink: %q", tc.font, out.Content)
		}
	}
}

// TestRewriteNonIntegerTr: only an exact integer 0–7 is a text rendering
// mode; any other Tr operand is undefined (Ghostscript renders 1.5 Tr as mode
// 0), so kept text consumes every state and keeps both the fill and the
// stroke pattern (batch C review round 5, S-3).
func TestRewriteNonIntegerTr(t *testing.T) {
	res := "<< /Font << /F1 5 0 R >> /Pattern << /P1 6 0 R /P2 6 0 R >> >>"
	pat := map[int]string{6: stm("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10 /Resources << >>", "0 0 5 5 re f")}
	for _, tr := range []string{"1.5", "4.5", "-0.5", "8", "2.0"} {
		content := "/Pattern CS /P1 SCN /Pattern cs /P2 scn BT " + tr + " Tr /F1 12 Tf 50 500 Td (KEEP) Tj ET"
		out := rewriteArea(t, fixture{content: content, res: res, extra: pat}, Rect{X: 5, Y: 5, Width: 1, Height: 1})
		for _, k := range []string{"/P1 SCN", "/P2 scn"} {
			if !strings.Contains(string(out.Content), k) {
				t.Errorf("%s Tr: %q removed from %q", tr, k, out.Content)
			}
		}
	}
	// An exact integer mode still decides: stroke-only text drops the fill.
	out := rewriteArea(t, fixture{content: "/Pattern cs /P2 scn BT 1 Tr /F1 12 Tf 50 500 Td (KEEP) Tj ET", res: res, extra: pat},
		Rect{X: 5, Y: 5, Width: 1, Height: 1})
	if strings.Contains(string(out.Content), "/P2 scn") {
		t.Errorf("1 Tr: the unconsumed fill pattern survives: %q", out.Content)
	}
}

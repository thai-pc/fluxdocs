package redaction

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file checks the other side of the D5 deny list (docs/design/
// redaction-hardening.md C36, C41): what redaction removes from page 0 must
// not change how a page that is not redacted renders, even when the removed
// content shares objects with it — a resource dictionary, an indirect
// subdictionary, a colour space used by an inline image or as /DefaultRGB, a
// content stream. Each case renders page 1 with Ghostscript in a faithful copy
// (RedactAndFlatten with no plan) and in the redacted output; the hashes must
// match. Rows from the batch C review round 2 harness (Q1, Q2, Q5, Q6, O1–O3).

// twoPagePDF assembles objs (pages 3 and 10 under 2) with a catalog.
func twoPagePDF(objs map[int]string) []byte {
	o := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 10 0 R] /Count 2 /MediaBox [0 0 612 792] >>",
		5: helvetica,
	}
	for k, v := range objs {
		o[k] = v
	}
	return leakPDF(o, "")
}

func pageObj(res, contents string) string {
	return "<< /Type /Page /Parent 2 0 R /Resources " + res + " /Contents " + contents + " >>"
}

var unredactedCases = []struct {
	name string
	objs map[int]string
	font bool // page 1's /F1 must still resolve to the font
}{
	{"Q1 removed form shares page 1's resource dictionary", map[int]string{
		3:  pageObj("20 0 R", "4 0 R"),
		4:  stm("", "/Fm1 Do"),
		8:  stm("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources 20 0 R", "0 0 1 rg 300 300 100 100 re f"),
		10: pageObj("20 0 R", "11 0 R"),
		11: stm("", "BT /F1 24 Tf 100 600 Td (KEEPTWO) Tj ET"),
		20: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 8 0 R >> >>",
	}, true},
	{"Q2 removed form shares the /Font subdictionary", map[int]string{
		3:  pageObj("<< /XObject << /Fm1 8 0 R >> >>", "4 0 R"),
		4:  stm("", "/Fm1 Do"),
		8:  stm("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /Font 21 0 R >>", "0 0 1 rg 300 300 100 100 re f BT /F1 1 Tf ET"),
		10: pageObj("<< /Font 21 0 R >>", "11 0 R"),
		11: stm("", "BT /F1 24 Tf 100 600 Td (KEEPTWO) Tj ET"),
		21: "<< /F1 5 0 R >>",
	}, true},
	{"Q5 removed form shares the resource dictionary of a page painting an image", map[int]string{
		3:  pageObj("20 0 R", "4 0 R"),
		4:  stm("", "/Fm1 Do"),
		6:  stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray", "\x00"),
		8:  stm("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources 20 0 R", "0 0 1 rg 300 300 100 100 re f"),
		10: pageObj("20 0 R", "11 0 R"),
		11: stm("", "q 300 0 0 300 100 100 cm /Im2 Do Q"),
		20: "<< /Font << /F1 5 0 R >> /XObject << /Fm1 8 0 R /Im2 6 0 R >> >>",
	}, false},
	{"Q6 removed form shares an indirect /XObject subdictionary", map[int]string{
		3:  pageObj("<< /XObject 21 0 R >>", "4 0 R"),
		4:  stm("", "/Fm1 Do"),
		6:  stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray", "\x00"),
		8:  stm("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /XObject 21 0 R >>", "0 0 1 rg 300 300 100 100 re f"),
		10: pageObj("<< /XObject 21 0 R >>", "11 0 R"),
		11: stm("", "q 300 0 0 300 100 100 cm /Im2 Do Q"),
		21: "<< /Fm1 8 0 R /Im2 6 0 R >>",
	}, false},
	{"O1 inline-image colour space shared with the removed image", map[int]string{
		3:  pageObj("<< /XObject << /Im1 6 0 R >> >>", "4 0 R"),
		4:  stm("", "q 100 0 0 100 300 300 cm /Im1 Do Q"),
		6:  stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace 9 0 R", "\x00"),
		9:  "[/Indexed /DeviceRGB 0 <FF0000>]",
		10: pageObj("<< /ColorSpace << /CS0 9 0 R >> >>", "11 0 R"),
		11: stm("", "q 300 0 0 300 100 100 cm BI /W 1 /H 1 /BPC 8 /CS /CS0 ID \x00 EI Q"),
	}, false},
	{"O2 /DefaultRGB shared with the removed image", map[int]string{
		3:  pageObj("<< /XObject << /Im1 6 0 R >> >>", "4 0 R"),
		4:  stm("", "q 100 0 0 100 300 300 cm /Im1 Do Q"),
		6:  stm("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace 9 0 R", "abc"),
		9:  "[/CalRGB << /WhitePoint [0.9505 1 1.089] /Matrix [0 0 1.089 0 1 0 0.9505 0 0] >>]",
		10: pageObj("<< /ColorSpace << /DefaultRGB 9 0 R >> >>", "11 0 R"),
		11: stm("", "1 0 0 rg 100 100 300 300 re f"),
	}, false},
	{"O3 page 1's content stream is page 0's removed form", map[int]string{
		3:  pageObj("<< /XObject << /Fm1 8 0 R >> >>", "4 0 R"),
		4:  stm("", "/Fm1 Do"),
		8:  stm("/Type /XObject /Subtype /Form /BBox [0 0 612 792]", "0 0 1 rg 300 300 100 100 re f"),
		10: pageObj("<< >>", "8 0 R"),
	}, false},
}

// TestRedactionKeepsUnredactedPages runs unredactedCases with a region on
// page 0 over the removed content.
func TestRedactionKeepsUnredactedPages(t *testing.T) {
	for _, tc := range unredactedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := twoPagePDF(tc.objs)
			faithful := redactBytes(t, in, nil)
			out := redactBytes(t, in, &core.Rect{X: 340, Y: 340, Width: 5, Height: 5})
			if tc.font {
				fontSurvives(t, out)
			}
			want, ok := gsRender(t, faithful)
			if !ok {
				if requireGS {
					t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
				}
				t.Skip("gs not found; render comparison skipped")
			}
			got, _ := gsRender(t, out)
			if len(got) != 2 || len(want) != 2 || got[1] != want[1] {
				t.Errorf("page 1 renders differently after redacting page 0: %.10v, want %.10v", got, want)
			}
		})
	}
}

// redactBytes redacts region on page 0 of pdf (none: a faithful copy) and
// returns the output bytes.
func redactBytes(t *testing.T, pdf []byte, region *core.Rect) []byte {
	t.Helper()
	doc, err := core.OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if region != nil {
		if err := doc.RedactArea(0, *region); err != nil {
			t.Fatal(err)
		}
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fontSurvives fails unless page 1 of out still finds Helvetica under /F1.
func fontSurvives(t *testing.T, out []byte) {
	t.Helper()
	r, err := parse.NewResolver(out)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil || len(pages) != 2 {
		t.Fatalf("pages %d, %v", len(pages), err)
	}
	fonts, _, _ := r.ResolveDict(pages[1].Resources["Font"])
	f, ok, _ := r.ResolveDict(fonts["F1"])
	if !ok || f["BaseFont"] != parse.Name("Helvetica") {
		keys := make([]string, 0, len(pages[1].Resources))
		for k := range pages[1].Resources {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		t.Errorf("page 1 /F1 = %v (resources %v), want the Helvetica font", f, keys)
	}
	if !bytes.Contains(out, []byte("/Helvetica")) {
		t.Error(fmt.Sprintf("the font object was not written"))
	}
}

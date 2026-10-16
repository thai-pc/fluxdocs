package redact

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// testPDF assembles objs (object number -> body) into a classic-xref PDF.
func testPDF(objs map[int]string) []byte {
	keys := make([]int, 0, len(objs))
	for k := range objs {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	size := keys[len(keys)-1] + 1
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, size)
	for _, k := range keys {
		offs[k] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", k, objs[k])
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		if offs[i] == 0 {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xref)
	return b.Bytes()
}

func stm(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// fixture is a one-page document: catalog 1, pages 2 (with pagesRes), page 3
// (with res), content 4, Helvetica 5, and extra objects from 6.
type fixture struct {
	content  string
	res      string // page /Resources; "" is << /Font << /F1 5 0 R >> >>
	pagesRes string
	extra    map[int]string
}

func (f fixture) objs() map[int]string {
	res := f.res
	if res == "" {
		res = "<< /Font << /F1 5 0 R >> >>"
	}
	pagesRes := ""
	if f.pagesRes != "" {
		pagesRes = " /Resources " + f.pagesRes
	}
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800]" + pagesRes + " >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources " + res + " /Contents 4 0 R >>",
		4: stm("", f.content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	if res == "NONE" {
		objs[3] = "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>"
	}
	for k, v := range f.extra {
		objs[k] = v
	}
	return objs
}

// analyze builds the page model of a fixture with the walker's budgets set
// by tune (nil keeps the defaults), and returns the walker too.
func analyze(t *testing.T, f fixture, tune func(*limits)) (*Page, *walker, error) {
	t.Helper()
	r, err := parse.NewResolver(testPDF(f.objs()))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil || len(pages) != 1 {
		t.Fatalf("Pages = %d, %v", len(pages), err)
	}
	seg, err := r.PageContentSegments(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	w := newWalker(r)
	if tune != nil {
		tune(&w.lim)
	}
	p, err := analyzePage(w, pages[0], seg)
	return p, w, err
}

// reasonOf is a refusal's reason, "ok" for nil, "error" for anything else.
func reasonOf(err error) Reason {
	var re *RefusalError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &re):
		return re.Reason
	}
	return "error"
}

const textBody = "BT /F1 24 Tf 72 500 Td (SECRET) Tj ET"

func form(extraDict, content string) string {
	return stm("/Type /XObject /Subtype /Form /BBox [0 0 400 800] "+extraDict, content)
}

func TestWalkRoutes(t *testing.T) {
	type3 := func(proc string) map[int]string {
		return map[int]string{
			6: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1000 1000] /FontMatrix [0.001 0 0 0.001 0 0] /CharProcs << /a 7 0 R >> " +
				"/Encoding << /Differences [97 /a] >> /FirstChar 97 /LastChar 97 /Widths [1000] /Resources << /Font << /F1 5 0 R >> >> >>",
			7: stm("", "1000 0 0 0 1000 1000 d1 "+proc),
		}
	}
	tiling := "/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10 /Resources << /Font << /F1 5 0 R >> >>"
	for _, tc := range []struct {
		name string
		f    fixture
		want Reason
	}{
		{"form with text", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /Font << /F1 5 0 R >> >>", textBody)}}, "form_text"},
		{"form without text", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("", "0 0 m 10 10 l S")}}, "ok"},
		{"form with an indirect /Subtype", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: stm("/BBox [0 0 1 1] /Width 1 /Height 1 /Subtype 7 0 R", textBody), 7: "/Form"}}, "form_text"},
		{"untyped XObject with /BBox is a form", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: stm("/BBox [0 0 1 1] /Width 1 /Height 1", textBody)}}, "form_text"},
		{"untyped XObject with /Width only is a form", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: stm("/Width 1", textBody)}}, "form_text"},
		{"untyped XObject with /Width and /Height is an image", fixture{content: "/Im1 Do", res: "<< /XObject << /Im1 6 0 R >> >>",
			extra: map[int]string{6: stm("/Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8", "x")}}, "ok"},
		{"PostScript XObject", fixture{content: "/P Do", res: "<< /XObject << /P 6 0 R >> >>",
			extra: map[int]string{6: stm("/Subtype /PS", "x")}}, "unsupported_xobject"},
		{"unknown XObject subtype", fixture{content: "/P Do", res: "<< /XObject << /P 6 0 R >> >>",
			extra: map[int]string{6: stm("/Subtype /Foo", "x")}}, "unsupported_xobject"},
		{"form name with #xx escape", fixture{content: "/Fm#31 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("", textBody)}}, "form_text"},
		{"form found in the page scope (fallback)", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R /Fm2 7 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /ProcSet [/PDF] >>", "/Fm2 Do"), 7: form("", textBody)}}, "form_text"},
		{"form found in a /Pages ancestor's resources", fixture{content: "/Fm1 Do", res: "<< /Font << /F1 5 0 R >> >>",
			pagesRes: "<< /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: form("", textBody)}}, "form_text"},
		{"inherited page resources", fixture{content: "/Fm1 Do", res: "NONE",
			pagesRes: "<< /XObject << /Fm1 6 0 R >> >>", extra: map[int]string{6: form("", textBody)}}, "form_text"},
		{"indirect resources and subdictionary", fixture{content: "/Fm1 Do", res: "8 0 R",
			extra: map[int]string{6: form("", textBody), 8: "<< /XObject 9 0 R >>", 9: "<< /Fm1 6 0 R >>"}}, "form_text"},
		{"form with /ActualText", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("", "/Span << /ActualText (x) >> BDC 0 0 m EMC")}}, "form_text"},
		{"form with /Alt via /Properties", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /Properties << /P0 << /Alt (x) >> >> >>", "/Figure /P0 BDC EMC")}}, "form_text"},
		{"form with a plain property list", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("", "/Span << /MCID 0 >> BDC EMC")}}, "ok"},
		{"self-referencing form without text", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /XObject << /Fm1 6 0 R >> >>", "/Fm1 Do")}}, "ok"},
		{"self-referencing form with text", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /XObject << /Fm1 6 0 R >> /Font << /F1 5 0 R >> >>", "/Fm1 Do "+textBody)}}, "form_text"},

		{"soft-mask group with text", fixture{content: "/GS1 gs", res: "<< /ExtGState << /GS1 << /SMask << /S /Luminosity /G 6 0 R >> >> >> >>",
			extra: map[int]string{6: form("/Group << /S /Transparency >>", textBody)}}, "softmask_text"},
		{"soft-mask group through indirect objects", fixture{content: "/GS1 gs", res: "<< /ExtGState << /GS1 7 0 R >> >>",
			extra: map[int]string{6: form("", textBody), 7: "<< /SMask 8 0 R >>", 8: "<< /S /Alpha /G 6 0 R >>"}}, "softmask_text"},
		{"soft-mask group without text", fixture{content: "/GS1 gs", res: "<< /ExtGState << /GS1 << /SMask << /S /Luminosity /G 6 0 R >> >> >> >>",
			extra: map[int]string{6: form("", "0 0 10 10 re f")}}, "ok"},
		{"/SMask /None", fixture{content: "/GS1 gs", res: "<< /ExtGState << /GS1 << /SMask /None >> >> >>"}, "ok"},
		{"soft-mask group inside a form", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /ExtGState << /G1 << /SMask << /G 7 0 R >> >> >> >>", "/G1 gs"), 7: form("", textBody)}}, "softmask_text"},

		{"tiling pattern selected by scn", fixture{content: "/Pattern cs /P1 scn 0 0 10 10 re f", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm(tiling, textBody)}}, "pattern_text"},
		{"tiling pattern selected by SCN", fixture{content: "/Pattern CS /P1 SCN 0 0 m 1 1 l S", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm(tiling, textBody)}}, "pattern_text"},
		{"unused tiling pattern (reachability)", fixture{content: "0 0 10 10 re f", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm(tiling, textBody)}}, "pattern_text"},
		{"unused tiling pattern in a form's resources", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 7 0 R >> >>",
			extra: map[int]string{6: stm(tiling, textBody), 7: form("/Resources << /Pattern << /P1 6 0 R >> >>", "")}}, "pattern_text"},
		{"tiling pattern with an indirect /PatternType", fixture{content: "/P1 scn", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm("/PatternType 7 0 R /BBox [0 0 1 1] /XStep 1 /YStep 1", textBody), 7: "1"}}, "pattern_text"},
		{"shading pattern", fixture{content: "/Pattern cs /P1 scn 0 0 10 10 re f", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: "<< /PatternType 2 /Shading << /ShadingType 2 /ColorSpace /DeviceGray /Coords [0 0 1 0] /Function << /FunctionType 2 /Domain [0 1] /N 1 >> >> >>"}}, "ok"},
		{"tiling pattern without text", fixture{content: "/P1 scn", res: "<< /Pattern << /P1 6 0 R >> >>",
			extra: map[int]string{6: stm(tiling, "0 0 5 5 re f")}}, "ok"},

		{"Type 3 glyph with text, selected by Tf", fixture{content: "BT /T3 24 Tf (a) Tj ET", res: "<< /Font << /F1 5 0 R /T3 6 0 R >> >>",
			extra: type3("BT /F1 200 Tf (SECRET) Tj ET")}, "type3_text"},
		{"Type 3 glyph with text, selected by gs /Font", fixture{content: "BT /GS1 gs (a) Tj ET", res: "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /Font [6 0 R 24] >> >> >>",
			extra: type3("BT /F1 200 Tf (SECRET) Tj ET")}, "type3_text"},
		{"Type 3 glyph without text", fixture{content: "BT /T3 24 Tf (a) Tj ET", res: "<< /Font << /T3 6 0 R >> >>",
			extra: type3("0 0 1000 1000 re f")}, "ok"},
		{"Type 3 glyph drawing a form with text", fixture{content: "BT /T3 24 Tf (a) Tj ET", res: "<< /Font << /T3 6 0 R >> /XObject << /Fm1 8 0 R >> >>",
			extra: func() map[int]string {
				m := type3("/Fm1 Do")
				m[8] = form("/Resources << /Font << /F1 5 0 R >> >>", textBody)
				return m
			}()}, "form_text"},
		{"Type 3 font in a form", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 8 0 R >> >>",
			extra: func() map[int]string {
				m := type3("BT /F1 200 Tf (SECRET) Tj ET")
				m[8] = form("/Resources << /Font << /T3 6 0 R >> >>", "BT /T3 1 Tf ET")
				return m
			}()}, "type3_text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := analyze(t, tc.f, nil)
			if got := reasonOf(err); got != tc.want {
				t.Errorf("reason %s (%v), want %s", got, err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Errorf("refusal holds document text: %v", err)
			}
		})
	}
}

// chain is a fixture whose page paints form 6, which paints form 7, … n forms
// deep; the last shows text if text is set.
func chain(n int, text bool) fixture {
	extra := map[int]string{}
	for i := 0; i < n; i++ {
		num := 6 + i
		body := fmt.Sprintf("/X%d Do", num+1)
		res := fmt.Sprintf("/Resources << /XObject << /X%d %d 0 R >> >>", num+1, num+1)
		if i == n-1 {
			body, res = "0 0 m", ""
			if text {
				body, res = textBody, "/Resources << /Font << /F1 5 0 R >> >>"
			}
		}
		extra[num] = form(res, body)
	}
	return fixture{content: "/X6 Do", res: "<< /XObject << /X6 6 0 R >> >>", extra: extra}
}

func TestWalkBudgets(t *testing.T) {
	if _, _, err := analyze(t, chain(maxUnitDepth, false), nil); err != nil {
		t.Errorf("%d nested forms: %v, want accepted", maxUnitDepth, err)
	}
	if _, _, err := analyze(t, chain(maxUnitDepth+1, false), nil); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("%d nested forms: %v, want budget_exceeded", maxUnitDepth+1, err)
	}
	if _, _, err := analyze(t, chain(maxUnitDepth, true), nil); reasonOf(err) != ReasonFormText {
		t.Errorf("text %d forms deep: %v, want form_text", maxUnitDepth, err)
	}

	many := fixture{content: strings.Repeat("/Fm1 Do ", 20), res: "<< /XObject << /Fm1 6 0 R >> >>",
		extra: map[int]string{6: form("", "0 0 m 1 1 l S")}}
	if _, _, err := analyze(t, many, func(l *limits) { l.invocations = 19 }); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("20 invocations over a budget of 19: %v, want budget_exceeded", err)
	}
	if _, _, err := analyze(t, many, func(l *limits) { l.invocations = 20 }); err != nil {
		t.Errorf("20 invocations within budget: %v", err)
	}
	if _, _, err := analyze(t, many, func(l *limits) { l.ops = 30 }); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("operator budget: %v, want budget_exceeded", err)
	}
	if _, _, err := analyze(t, many, func(l *limits) { l.decoded = int64(len(many.content)) + 5 }); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("decode budget: %v, want budget_exceeded", err)
	}
	type3 := fixture{content: "BT /T3 24 Tf (a) Tj ET", res: "<< /Font << /T3 6 0 R >> >>", extra: map[int]string{
		6: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /CharProcs << /a 7 0 R /b 7 0 R /c 8 0 R >> >>",
		7: stm("", "0 0 m"), 8: stm("", "0 0 m")}}
	if _, _, err := analyze(t, type3, func(l *limits) { l.glyphProcs = 2 }); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("glyph-procedure budget: %v, want budget_exceeded", err)
	}
}

func TestWalkMemoizes(t *testing.T) {
	// One form painted 200 times, and through a second form 200 times more:
	// each stream is decoded and scanned once per scope chain.
	f := fixture{content: strings.Repeat("/Fm1 Do /Fm2 Do ", 100) + strings.Repeat("/Fm1 Do ", 100),
		res: "<< /XObject << /Fm1 6 0 R /Fm2 7 0 R >> >>",
		extra: map[int]string{
			6: form("", strings.Repeat("0 0 m 1 1 l S ", 1000)),
			7: form("", "/Fm1 Do"),
		}}
	_, w, err := analyze(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.content) != 2 {
		t.Errorf("%d streams decoded, want 2 (each once)", len(w.content))
	}
	if len(w.memo) != 2 {
		t.Errorf("%d unit walks, want 2", len(w.memo))
	}
	// 3000 operators of form 6 once, plus small change — not 300 × 3000.
	if w.ops > 10000 {
		t.Errorf("%d operator visits, want each unit interpreted once", w.ops)
	}
}

func TestInlineColorSpaceScopes(t *testing.T) {
	img := func(cs string) string { return "q BI /W 1 /H 1 /BPC 8 /CS " + cs + " ID \x80 EI Q" }
	for _, tc := range []struct {
		name string
		f    fixture
		ok   bool
	}{
		{"named, in the page resources", fixture{content: img("/CS0"), res: "<< /ColorSpace << /CS0 /DeviceRGB >> >>"}, false},
		{"named gray, in the page resources", fixture{content: img("/CS0"), res: "<< /ColorSpace << /CS0 /DeviceGray >> >>"}, true},
		{"named, indirect", fixture{content: img("/CS0"), res: "<< /ColorSpace 6 0 R >>", extra: map[int]string{6: "<< /CS0 7 0 R >>", 7: "/DeviceGray"}}, true},
		{"named Indexed with a lookup stream", fixture{content: img("/CS0"), res: "<< /ColorSpace << /CS0 [/Indexed /DeviceRGB 0 6 0 R] >> >>",
			extra: map[int]string{6: stm("", "abc")}}, true},
		{"named, only in a /Pages ancestor", fixture{content: img("/CS0"), pagesRes: "<< /ColorSpace << /CS0 /DeviceGray >> >>"}, false},
		{"named, undefined", fixture{content: img("/CS0")}, false},
		{"named ICCBased", fixture{content: img("/CS0"), res: "<< /ColorSpace << /CS0 [/ICCBased 6 0 R] >> >>", extra: map[int]string{6: stm("/N 1", "x")}}, false},
		{"abbreviation redefined by the page", fixture{content: img("/G"), res: "<< /ColorSpace << /G /DeviceRGB >> >>"}, false},
		{"abbreviation redefined by an ancestor", fixture{content: img("/G"), pagesRes: "<< /ColorSpace << /G /DeviceGray >> >>"}, false},
		{"family name I defined as a resource", fixture{content: img("/I"), res: "<< /ColorSpace << /I /DeviceGray >> >>"}, false},
		{"device name", fixture{content: img("/DeviceGray"), res: "<< /ColorSpace << /DeviceGray /DeviceRGB >> >>"}, true},
		{"named, in a form's own resources", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> >>",
			extra: map[int]string{6: form("/Resources << /ColorSpace << /CS0 /DeviceGray >> >>", img("/CS0"))}}, true},
		{"named, only in the page while the form has resources", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> /ColorSpace << /CS0 /DeviceGray >> >>",
			extra: map[int]string{6: form("/Resources << /ProcSet [/PDF] >>", img("/CS0"))}}, false},
		{"named, in the page, form without resources", fixture{content: "/Fm1 Do", res: "<< /XObject << /Fm1 6 0 R >> /ColorSpace << /CS0 /DeviceGray >> >>",
			extra: map[int]string{6: form("", img("/CS0"))}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := analyze(t, tc.f, nil)
			if (err == nil) != tc.ok {
				t.Errorf("err = %v, want accepted %v", err, tc.ok)
			}
			if err != nil && !strings.Contains(err.Error(), "inline image") {
				t.Errorf("err = %v, want an inline-image loss", err)
			}
		})
	}
}

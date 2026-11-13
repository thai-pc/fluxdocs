package core

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestRedactExtend_OwnedUnitRefused: removed text held by a unit only
// redacted pages paint (or by an object the unit uses) is a refusal of that
// unit's reason, found by automatic extension before verification — also
// when the removed text does not propagate (RedactText(0, `\d`) matches one
// digit at a time). It was ErrRedactionVerification (leak fixture
// r2_other_form_comment with `\d` on page 0 and a region on page 1).
func TestRedactExtend_OwnedUnitRefused(t *testing.T) {
	area1 := Rect{X: 100, Y: 200, Width: 200, Height: 400}
	form := func(dict, data string) string {
		return extStream("/Type /XObject /Subtype /Form /BBox [0 0 10 10]"+dict, data)
	}
	withForm := "<< /Font << /F1 5 0 R >> /XObject << /Fm1 100 0 R >> >>"
	cases := []struct {
		name    string
		content string // page 1, after showing "public"
		res     string
		extra   map[int]string
		// want is the refusal reason with `\d` on page 0 and a region on
		// page 1; secret the outcome with `SECRET-\d+` on page 0 alone ("" is
		// a clean redaction).
		want, secret RedactionReason
	}{
		{"form_comment", " /Fm1 Do", withForm,
			map[int]string{100: form("", "%"+extSecret+"\n0 0 1 1 re f")}, RedactionFormText, RedactionFormText},
		{"form_dict_string", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Foo ("+extSecret+")", "0 0 1 1 re f")}, RedactionFormText, ""},
		{"form_metadata", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Metadata 101 0 R", "0 0 1 1 re f"),
				101: extStream("/Type /Metadata /Subtype /XML", "<x>"+extSecret+"</x>")}, RedactionFormText, RedactionFormText},
		{"form_resource_string", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Resources << /ExtGState << /G1 101 0 R >> >>", "/G1 gs 0 0 1 1 re f"),
				101: "<< /Type /ExtGState /CA 1 /Foo (" + extSecret + ") >>"}, RedactionFormText, RedactionFormText},
		{"pattern_comment", " /Pattern cs /P1 scn 0 0 10 10 re f", "<< /Font << /F1 5 0 R >> /Pattern << /P1 100 0 R >> >>",
			map[int]string{100: extStream("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 5 5] /XStep 5 /YStep 5",
				"%"+extSecret+"\n0 0 1 1 re f")}, RedactionPatternText, RedactionPatternText},
		{"softmask_comment", " /GS1 gs 0 0 10 10 re f", "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 << /SMask << /S /Luminosity /G 100 0 R >> >> >> >>",
			map[int]string{100: form(" /Group << /S /Transparency /CS /DeviceGray >>", "%"+extSecret+"\n0 0 1 1 re f")},
			RedactionSoftMaskText, RedactionSoftMaskText},
		{"type3_comment", " BT /F3 1 Tf 10 10 Td (a) Tj ET", "<< /Font << /F1 5 0 R /F3 100 0 R >> >>",
			map[int]string{100: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /CharProcs << /a 101 0 R >> " +
				"/Encoding << /Type /Encoding /Differences [97 /a] >> /FirstChar 97 /LastChar 97 /Widths [1] >>",
				101: extStream("", "%"+extSecret+"\n1 0 0 0 1 1 d1 0 0 1 1 re f")}, RedactionType3Text, RedactionType3Text},
		// A glyph procedure the page never shows is still walked.
		{"type3_unshown_glyph", " BT /F3 1 Tf 10 10 Td (a) Tj ET", "<< /Font << /F1 5 0 R /F3 100 0 R >> >>",
			map[int]string{100: "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /CharProcs << /a 101 0 R /b 102 0 R >> " +
				"/Encoding << /Type /Encoding /Differences [97 /a /b] >> /FirstChar 97 /LastChar 98 /Widths [1 1] >>",
				101: extStream("", "1 0 0 0 1 1 d1 0 0 1 1 re f"),
				102: extStream("", "%"+extSecret+"\n1 0 0 0 1 1 d1 0 0 1 1 re f")}, RedactionType3Text, RedactionType3Text},
		// Units inside units: a painted nested form, a form listed in a
		// painted form's resources but never painted (an object the unit
		// uses), a tiling pattern of a form.
		{"form_nested", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Resources << /XObject << /Fm2 101 0 R >> >>", "/Fm2 Do"),
				101: form("", "%"+extSecret+"\n0 0 1 1 re f")}, RedactionFormText, RedactionFormText},
		{"form_nested_unpainted", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Resources << /XObject << /Fm2 101 0 R >> >>", "0 0 1 1 re f"),
				101: form("", "%"+extSecret+"\n0 0 1 1 re f")}, RedactionFormText, RedactionFormText},
		{"pattern_in_form", " /Fm1 Do", withForm,
			map[int]string{100: form(" /Resources << /Pattern << /P1 101 0 R >> >>", "/Pattern cs /P1 scn 0 0 1 1 re f"),
				101: extStream("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 5 5] /XStep 5 /YStep 5",
					"%"+extSecret+"\n0 0 1 1 re f")}, RedactionPatternText, RedactionPatternText},
		// Controls: what is not a unit is rewritten or not searched, and a
		// form the page lists but never paints is not kept.
		{"page_extgstate_string", " /GS1 gs", "<< /Font << /F1 5 0 R >> /ExtGState << /GS1 100 0 R >> >>",
			map[int]string{100: "<< /Type /ExtGState /CA 1 /Foo (" + extSecret + ") >>"}, "", ""},
		{"page_font_string", "", "<< /Font << /F1 100 0 R >> >>",
			map[int]string{100: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Foo (" + extSecret + ") >>"}, "", ""},
		{"form_unpainted_sibling", " /Fm1 Do", "<< /Font << /F1 5 0 R >> /XObject << /Fm1 100 0 R /Fm2 101 0 R >> >>",
			map[int]string{100: form("", "0 0 1 1 re f"), 101: form("", "%"+extSecret+"\n0 0 1 1 re f")}, "", ""},
	}
	check := func(t *testing.T, what string, out *Document, err error, want RedactionReason) {
		t.Helper()
		if want == "" {
			if err != nil || out == nil {
				t.Errorf("%s: RedactAndFlatten = (%v, %v), want a document", what, out, err)
			} else if strings.ContainsAny(extText(t, out), "0123456789") && what != "regions on both pages" {
				t.Errorf("%s: the output still shows a removed digit", what)
			}
			return
		}
		var re *RedactionError
		if out != nil || errors.Is(err, ErrRedactionVerification) || !errors.As(err, &re) || re.Reason != want || re.Page != 1 {
			t.Errorf("%s: RedactAndFlatten = (%v, %v), want a %s refusal on page 1", what, out, err, want)
			return
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "1234") {
			t.Errorf("%s: the error holds document text: %v", what, err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdf := extPDF([]extPage{{content: showAt(extSecret, 700)},
				{content: showAt("public", 700) + tc.content, res: tc.res}}, tc.extra, "")
			_, out, err := redactWith(t, pdf, func(d *Document) {
				redactText(0, `\d`)(d)
				redactArea(1, area1)(d)
			})
			check(t, `\d + region on page 1`, out, err, tc.want)
			// The variants keep their outcomes: `\d` alone and regions that
			// remove nothing redact; `SECRET-\d+` propagates.
			_, out, err = redactWith(t, pdf, redactText(0, `\d`))
			check(t, `\d`, out, err, "")
			_, out, err = redactWith(t, pdf, func(d *Document) {
				redactArea(0, area1)(d)
				redactArea(1, area1)(d)
			})
			check(t, "regions on both pages", out, err, "")
			_, out, err = redactWith(t, pdf, redactText(0, `SECRET-\d+`))
			check(t, `SECRET-\d+`, out, err, tc.secret)
		})
	}
}

// TestDoS_OwnedAttribution: attributing removed text in an object only a
// redacted page keeps (the page's walk again, and a graph of what its units
// reach) costs at most 1.5× the same redaction that finds nothing to
// attribute, on a page that owns large forms and a Type 3 font of many glyph
// procedures (median CPU of 5 alternated runs of each; relative, never an
// absolute time).
func TestDoS_OwnedAttribution(t *testing.T) {
	if testing.Short() || raceEnabled || cpuTime() == 0 {
		t.Skip("CPU-time ratio gate: needs CPU time, no -race, no -short")
	}
	const forms, opsPerForm, glyphs = 8, 20_000, 1_000
	build := func(comment string) []byte {
		extra := map[int]string{}
		var do, xobj, procs, diffs strings.Builder
		body := strings.Repeat("0 0 1 1 re f\n", opsPerForm)
		for i := 0; i < forms; i++ {
			extra[200+i] = extStream("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", body)
			fmt.Fprintf(&xobj, " /Fm%d %d 0 R", i, 200+i)
			fmt.Fprintf(&do, " /Fm%d Do", i)
		}
		// The form whose comment holds (or not) the removed text.
		extra[300] = extStream("/Type /XObject /Subtype /Form /BBox [0 0 10 10]", "%"+comment+"\n0 0 1 1 re f")
		xobj.WriteString(" /Hold 300 0 R")
		do.WriteString(" /Hold Do")
		for i := 0; i < glyphs; i++ {
			fmt.Fprintf(&procs, " /g%d %d 0 R", i, 1000+i)
			fmt.Fprintf(&diffs, " /g%d", i)
			extra[1000+i] = extStream("", "1 0 0 0 1 1 d1 0 0 1 1 re f")
		}
		extra[400] = "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [1 0 0 1 0 0] /CharProcs <<" + procs.String() +
			" >> /Encoding << /Type /Encoding /Differences [0" + diffs.String() + "] >> /FirstChar 0 /LastChar 0 /Widths [1] >>"
		res := "<< /Font << /F1 5 0 R /F3 400 0 R >> /XObject <<" + xobj.String() + " >> >>"
		return extPDF([]extPage{{content: showAt(extSecret, 700)},
			{content: showAt("public", 700) + " BT /F3 1 Tf 10 10 Td <00> Tj ET" + do.String(), res: res}}, extra, "")
	}
	pdfs := [2][]byte{build("nothing removed here"), build(extSecret)} // 0: clean, 1: attributed
	area1 := Rect{X: 100, Y: 200, Width: 200, Height: 400}
	var cpus [2][]time.Duration
	for i := 0; i < 10; i++ {
		k := i % 2
		d, err := OpenBytes(pdfs[k])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.RedactText(0, `\d`); err != nil {
			t.Fatal(err)
		}
		if err := d.RedactArea(1, area1); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		c0 := cpuTime()
		out, err := d.RedactAndFlatten()
		cpus[k] = append(cpus[k], cpuTime()-c0)
		var re *RedactionError
		switch {
		case k == 0 && err != nil:
			t.Fatalf("clean variant: %v", err)
		case k == 1 && (out != nil || !errors.As(err, &re) || re.Reason != RedactionFormText):
			t.Fatalf("attributed variant: RedactAndFlatten = (%v, %v), want a form_text refusal", out, err)
		}
		if out != nil {
			out.Close()
		}
		d.Close()
	}
	med := func(c []time.Duration) time.Duration {
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		return c[len(c)/2]
	}
	clean, attributed := med(cpus[0]), med(cpus[1])
	ratio := float64(attributed) / float64(max(clean, time.Microsecond))
	t.Logf("CPU clean %v, attributed %v (%.2f×)", clean, attributed, ratio)
	if ratio > 1.5 {
		t.Errorf("attribution %.2f× the redaction that attributes nothing, want ≤ 1.5×", ratio)
	}
}

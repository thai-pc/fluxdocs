package core

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Tests of automatic plan extension (D4), the redaction report (P8) and the
// post-redaction verification (P3): docs/design/redaction-hardening.md §5.6,
// §6.11, §12.2. Every fixture is synthetic; the secrets are constants.

const extSecret = "SECRET-1234"

// extPage is one page of an extension fixture: its content, more
// page-dictionary entries, and its /Resources ("" is the two fonts).
type extPage struct {
	content, extra, res string
}

// extPDF is a fixture of pages (400x800, Helvetica /F1 is object 5, the ligature
// font /F2 object 6), page i being object 10+2i with content 11+2i, extra
// objects from 100, and the trailer /Info info when not empty.
func extPDF(pages []extPage, extra map[int]string, info string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 7 0 R >>",
		7: extStream("", "1 begincodespacerange <00> <FF> endcodespacerange "+
			"1 beginbfrange <20> <7E> <0020> endbfrange 1 beginbfchar <93> <FB01> endbfchar"),
	}
	var kids []string
	for i, p := range pages {
		n := 10 + 2*i
		kids = append(kids, fmt.Sprintf("%d 0 R", n))
		res := p.res
		if res == "" {
			res = "<< /Font << /F1 5 0 R /F2 6 0 R >> >>"
		}
		objs[n] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Resources %s /Contents %d 0 R%s >>", res, n+1, p.extra)
		objs[n+1] = extStream("", p.content)
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 400 800] >>", strings.Join(kids, " "), len(pages))
	for k, v := range extra {
		objs[k] = v
	}
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
	if info != "" {
		info = " /Info " + info
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", size, info, xref)
	return b.Bytes()
}

func extStream(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// showAt shows the literal string s with /F1 at (72, y).
func showAt(s string, y int) string {
	return fmt.Sprintf("BT /F1 24 Tf 72 %d Td (%s) Tj ET\n", y, s)
}

// extText returns the text of every page of d.
func extText(t *testing.T, d *Document) string {
	t.Helper()
	text, err := d.ExtractText(ExtractOptions{})
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	return text
}

// redactWith opens pdf, applies mark and flattens.
func redactWith(t *testing.T, pdf []byte, mark func(d *Document)) (*Document, *Document, error) {
	t.Helper()
	d, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	mark(d)
	out, err := d.RedactAndFlatten()
	return d, out, err
}

func redactText(page int, pattern string) func(d *Document) {
	return func(d *Document) {
		if _, err := d.RedactText(page, pattern); err != nil {
			panic(err)
		}
	}
}

func redactArea(page int, r Rect) func(d *Document) {
	return func(d *Document) {
		if err := d.RedactArea(page, r); err != nil {
			panic(err)
		}
	}
}

// mustReport returns out's redaction report, failing if it has none.
func mustReport(t *testing.T, out *Document) RedactionReport {
	t.Helper()
	r, ok := out.RedactionReport()
	if !ok {
		t.Fatal("RedactionReport: ok = false on a redacted document")
	}
	if s := fmt.Sprintf("%+v", r); strings.Contains(s, "SECRET") || strings.Contains(s, "1234") ||
		strings.Contains(s, "LONGTEXT") || strings.Contains(s, "confidential") {
		t.Errorf("the report holds document text: %s", s)
	}
	return r
}

func TestRedactExtend_MatchNeedleOtherPage(t *testing.T) {
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt("keep", 600)}, {content: showAt(extSecret, 400) + showAt("public", 300)}}, nil, "")
	d, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	if text := extText(t, out); strings.Contains(text, extSecret) || !strings.Contains(text, "public") || !strings.Contains(text, "keep") {
		t.Errorf("output text %q: want the secret gone everywhere and the rest kept", text)
	}
	r := mustReport(t, out)
	if len(r.Extensions) != 1 {
		t.Fatalf("extensions = %+v, want one (page 2)", r.Extensions)
	}
	e := r.Extensions[0]
	if e.Page != 2 || e.Source != "text-match" || e.Where != "content" || len(e.Rects) != 1 {
		t.Errorf("extension = %+v, want page 2, text-match, content, one rect", e)
	}
	if _, ok := d.RedactionReport(); ok {
		t.Error("the source document has a redaction report")
	}
}

func TestRedactExtend_Thresholds(t *testing.T) {
	cases := []struct {
		name     string
		mark     func(*Document)
		page0    string
		other    string
		extended bool
	}{
		// A match needle propagates from 4 skeleton characters.
		{"match 3 characters", redactText(0, `XYZ`), "XYZ", "XYZ", false},
		{"match 4 characters", redactText(0, `WXYZ`), "WXYZ", "WXYZ", true},
		// Text removed under a region propagates from 8.
		{"area 7 characters", redactArea(0, Rect{X: 0, Y: 690, Width: 400, Height: 40}), "SHORT77", "SHORT77", false},
		{"area 8 characters", redactArea(0, Rect{X: 0, Y: 690, Width: 400, Height: 40}), "LONGTEXT", "LONGTEXT", true},
		// Matching is case-sensitive.
		{"case differs", redactText(0, `confidential`), "confidential", "CONFIDENTIAL", false},
		// White-space inside the occurrence does not hide it.
		{"spaces", redactText(0, `confidential`), "confidential", "confi dential", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdf := extPDF([]extPage{{content: showAt(tc.page0, 700)}, {content: showAt(tc.other, 400)}}, nil, "")
			_, out, err := redactWith(t, pdf, tc.mark)
			if err != nil {
				t.Fatalf("RedactAndFlatten: %v", err)
			}
			text, err := out.ExtractTextByPage(1, ExtractOptions{})
			if err != nil {
				t.Fatal(err)
			}
			kept := strings.TrimSpace(text) != ""
			r := mustReport(t, out)
			if kept == tc.extended || (len(r.Extensions) > 0) != tc.extended {
				t.Errorf("page 1 text %q, extensions %+v; want extended %v", text, r.Extensions, tc.extended)
			}
		})
	}
}

func TestRedactExtend_LigatureNFKC(t *testing.T) {
	// Page 1 shows "conﬁdential" with code 0x93 mapped to U+FB01 by its
	// /ToUnicode: NFKC folds it to "fi".
	pdf := extPDF([]extPage{{content: showAt("confidential", 700)},
		{content: "BT /F2 24 Tf 72 400 Td <636F6E9364656E7469616C> Tj ET"}}, nil, "")
	_, out, err := redactWith(t, pdf, redactText(0, `confidential`))
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	r := mustReport(t, out)
	if len(r.Extensions) != 1 || r.Extensions[0].Page != 1 {
		t.Errorf("extensions = %+v, want page 1", r.Extensions)
	}
}

func TestRedactExtend_AreaNeedleSamePage(t *testing.T) {
	// The text removed under the region also occurs on the same page,
	// outside it: removed there too, and reported as an extension of page 0.
	pdf := extPDF([]extPage{{content: showAt("LONGTEXT-99", 700) + showAt("LONGTEXT-99", 300) + showAt("public", 200)}}, nil, "")
	_, out, err := redactWith(t, pdf, redactArea(0, Rect{X: 0, Y: 690, Width: 400, Height: 40}))
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	if text := extText(t, out); strings.Contains(text, "LONGTEXT") || !strings.Contains(text, "public") {
		t.Errorf("output text %q", text)
	}
	r := mustReport(t, out)
	if len(r.Extensions) != 1 || r.Extensions[0].Page != 0 || r.Extensions[0].Source != "area-text" {
		t.Errorf("extensions = %+v, want page 0 area-text", r.Extensions)
	}
}

func TestRedactExtend_Annotations(t *testing.T) {
	annots := " /Annots [100 0 R 101 0 R 102 0 R 103 0 R 104 0 R 106 0 R]"
	extra := map[int]string{
		// A note holding the secret, its popup and a reply to it.
		100: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (" + extSecret + ") /Popup 101 0 R >>",
		101: "<< /Type /Annot /Subtype /Popup /Rect [0 0 10 10] /Parent 100 0 R >>",
		102: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /IRT 100 0 R /Contents (agreed) >>",
		// A widget whose field value holds it.
		103: "<< /Type /Annot /Subtype /Widget /Rect [0 0 10 10] /Parent 105 0 R >>",
		105: "<< /FT /Tx /T (id) /V (" + extSecret + ") /Kids [103 0 R] >>",
		// An unrelated link that stays, and an attachment that goes.
		104: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] >>",
		106: "<< /Type /Annot /Subtype /FileAttachment /Rect [0 0 10 10] /FS << /Type /Filespec /F (a.txt) >> >>",
	}
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt("public", 400), extra: annots}}, extra, "")
	_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	r := mustReport(t, out)
	var got []string
	for _, a := range r.RemovedAnnotations {
		got = append(got, fmt.Sprintf("%d/%s/%s", a.Page, a.Subtype, a.Reason))
	}
	sort.Strings(got)
	want := []string{"1/FileAttachment/attachment-policy", "1/Popup/needle", "1/Text/needle", "1/Text/needle", "1/Widget/needle"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("removed annotations %v, want %v", got, want)
	}
	if n := len(out.pageAnnots(1)); n != 1 {
		t.Errorf("page 1 keeps %d annotations, want 1 (the link)", n)
	}
	raw, _ := out.Bytes()
	if bytes.Contains(raw, []byte(extSecret)) {
		t.Error("the output bytes hold the secret")
	}
}

// pageAnnots returns the entries of page i's /Annots.
func (d *Document) pageAnnots(i int) parse.Array {
	v, err := d.resolver.Resolve(d.pageInfos[i].Dict["Annots"])
	if err != nil {
		return nil
	}
	arr, _ := v.(parse.Array)
	return arr
}

func TestRedactExtend_AttachmentsOnlyWhenPlanned(t *testing.T) {
	extra := map[int]string{100: "<< /Type /Annot /Subtype /FileAttachment /Rect [0 0 10 10] /FS << /Type /Filespec /F (a.txt) >> >>"}
	pdf := extPDF([]extPage{{content: showAt("public", 700), extra: " /Annots [100 0 R]"}}, extra, "")
	_, out, err := redactWith(t, pdf, func(*Document) {})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.pageAnnots(0)); n != 1 {
		t.Errorf("with no plan the attachment is removed (%d annotations kept)", n)
	}
	_, out, err = redactWith(t, pdf, redactArea(0, Rect{X: 0, Y: 0, Width: 1, Height: 1}))
	if err != nil {
		t.Fatal(err)
	}
	r := mustReport(t, out)
	if len(r.RemovedAnnotations) != 1 || r.RemovedAnnotations[0].Reason != "attachment-policy" {
		t.Errorf("removed annotations %+v, want the attachment", r.RemovedAnnotations)
	}
}

func TestRedactExtend_InfoFields(t *testing.T) {
	extra := map[int]string{100: "<< /Title (Report " + extSecret + ") /Author (someone) /Subject (secret plans) >>"}
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}}, extra, "100 0 R")
	_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
	if err != nil {
		t.Fatal(err)
	}
	r := mustReport(t, out)
	if !reflect.DeepEqual(r.RemovedInfoFields, []string{"Title"}) {
		t.Errorf("removed info fields %v, want [Title]", r.RemovedInfoFields)
	}
	if out.Title != "" || out.Metadata.Author != "someone" {
		t.Errorf("output title %q author %q", out.Title, out.Metadata.Author)
	}
	// A field that matches a RedactText pattern goes too.
	_, out, err = redactWith(t, pdf, redactText(0, `secret plans|SECRET-\d+`))
	if err != nil {
		t.Fatal(err)
	}
	if r := mustReport(t, out); !reflect.DeepEqual(r.RemovedInfoFields, []string{"Subject", "Title"}) {
		t.Errorf("removed info fields %v, want [Subject Title]", r.RemovedInfoFields)
	}
}

func TestRedactExtend_FormOnOtherPageRefused(t *testing.T) {
	extra := map[int]string{100: extStream("/Type /XObject /Subtype /Form /BBox [0 0 400 800] /Resources << /Font << /F1 5 0 R >> >>",
		showAt(extSecret, 500))}
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: "/Fm1 Do", res: "<< /XObject << /Fm1 100 0 R >> >>"}}, extra, "")
	_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
	var re *RedactionError
	if out != nil || !errors.As(err, &re) || re.Reason != RedactionFormText || re.Page != 1 {
		t.Fatalf("RedactAndFlatten = (%v, %v), want a form_text refusal on page 1", out, err)
	}
}

func TestRedactExtend_SourcePlanUnchanged(t *testing.T) {
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt(extSecret, 400)}}, nil, "")
	d, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RedactText(0, `SECRET-\d+`); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		d.mu.Lock()
		defer d.mu.Unlock()
		return fmt.Sprintf("%v|%v|%v|%v|%v|%d", d.redactions, d.redactRegions, d.redactNeedles, d.redactPatterns, d.redactErr, len(d.redactMarks[0]))
	}
	before := snap()
	for i := 0; i < 2; i++ {
		if _, err := d.RedactAndFlatten(); err != nil {
			t.Fatal(err)
		}
		if after := snap(); after != before {
			t.Fatalf("the source plan changed:\n%s\n%s", before, after)
		}
	}
}

// withHooks runs f with the redaction test hooks set, then clears them.
// Tests that use it do not run in parallel.
func withHooks(t *testing.T, noPrepass, noop bool, mutate func([]byte) []byte, f func()) {
	t.Helper()
	redactHooks.noPrepass, redactHooks.noopExtension, redactHooks.mutate = noPrepass, noop, mutate
	defer func() { redactHooks.noPrepass, redactHooks.noopExtension, redactHooks.mutate = false, false, nil }()
	f()
}

func TestRedactExtend_VerificationRoundExtends(t *testing.T) {
	// Without the pre-pass, verification finds the secret on page 1 (its
	// text) and in an /Info field, and round 1 removes them.
	extra := map[int]string{100: "<< /Title (" + extSecret + ") >>"}
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt(extSecret, 400)}}, extra, "100 0 R")
	withHooks(t, true, false, nil, func() {
		_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
		if err != nil {
			t.Fatalf("RedactAndFlatten: %v", err)
		}
		if strings.Contains(extText(t, out), extSecret) || out.Title != "" {
			t.Error("the secret survives")
		}
		r := mustReport(t, out)
		if len(r.Extensions) != 1 || r.Extensions[0].Page != 1 || !reflect.DeepEqual(r.RemovedInfoFields, []string{"Title"}) {
			t.Errorf("report %+v", r)
		}
	})
}

func TestRedactExtend_PageHygiene(t *testing.T) {
	// Page 1 does not show the secret but carries it (an unused form in its
	// resources): verification finds it and redacts page 1 with no region,
	// which drops the unused resource.
	extra := map[int]string{100: extStream("/Type /XObject /Subtype /Form /BBox [0 0 1 1]", "BT /F1 1 Tf ("+extSecret+") Tj ET"),
		101: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.com) >> >>"}
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)},
		{content: showAt("public", 400), res: "<< /Font << /F1 5 0 R >> /XObject << /Unused 100 0 R >> >>", extra: " /Annots [101 0 R]"}}, extra, "")
	_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	raw, _ := out.Bytes()
	if bytes.Contains(raw, []byte(extSecret)) {
		t.Error("the output holds the secret")
	}
	r := mustReport(t, out)
	if len(r.Extensions) != 1 || r.Extensions[0].Page != 1 || r.Extensions[0].Where != "page-hygiene" {
		t.Errorf("extensions %+v, want page-hygiene on page 1", r.Extensions)
	}
	if !strings.Contains(extText(t, out), "public") {
		t.Error("page 1 lost its text")
	}
	// The link page hygiene dropped with the page's annotations is listed.
	if !reflect.DeepEqual(r.RemovedAnnotations, []RemovedAnnotation{{Page: 1, Subtype: "Link", Reason: "page-hygiene"}}) {
		t.Errorf("removed annotations %+v, want the link (page-hygiene)", r.RemovedAnnotations)
	}
}

func TestRedactExtend_NotConverged(t *testing.T) {
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt(extSecret, 400)}}, nil, "")
	withHooks(t, true, true, nil, func() {
		_, out, err := redactWith(t, pdf, redactText(0, `SECRET-\d+`))
		if out != nil || !errors.Is(err, ErrRedactionVerification) || !errors.Is(err, ErrInternal) ||
			!strings.Contains(err.Error(), "not_converged") {
			t.Fatalf("RedactAndFlatten = (%v, %v), want ErrRedactionVerification not_converged", out, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("the error holds document text: %v", err)
		}
	})
}

// appendUpdate appends an incremental update to pdf that adds or replaces
// objs (§7.5.6).
func appendUpdate(t *testing.T, pdf []byte, objs map[int]string) []byte {
	t.Helper()
	r, err := parse.NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	tail := pdf[bytes.LastIndex(pdf, []byte("startxref"))+len("startxref"):]
	prev, err := strconv.Atoi(strings.Fields(string(tail))[0])
	if err != nil {
		t.Fatal(err)
	}
	size := r.MaxObjectNumber() + 1
	for n := range objs {
		size = max(size, n+1)
	}
	var b bytes.Buffer
	b.Write(pdf)
	b.WriteString("\n")
	offs := map[int]int{}
	nums := make([]int, 0, len(objs))
	for n := range objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	x := b.Len()
	b.WriteString("xref\n")
	for _, n := range nums {
		fmt.Fprintf(&b, "%d 1\n%010d 00000 n \n", n, offs[n])
	}
	tr := r.Trailer()
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %s /Info %s /Prev %d >>\nstartxref\n%d\n%%%%EOF\n",
		size, parse.SerializeObject(tr["Root"]), parse.SerializeObject(tr["Info"]), prev, x)
	return b.Bytes()
}

// TestRedactVerify_SeededLeaks: the verification catches leaks seeded into
// the built output (§12.2 "Verifier").
func TestRedactVerify_SeededLeaks(t *testing.T) {
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700) + showAt("public", 300)}, {content: showAt("other", 400)}}, nil, "")
	// pageOf returns the output's page 0 object number and dictionary.
	pageOf := func(t *testing.T, built []byte, i int) (int, string) {
		r, err := parse.NewResolver(built)
		if err != nil {
			t.Fatal(err)
		}
		pis, err := r.Pages()
		if err != nil {
			t.Fatal(err)
		}
		return pis[i].ObjectNumber(), string(parse.SerializeObject(pis[i].Dict))
	}
	cases := []struct {
		name   string
		mutate func(t *testing.T, built []byte) []byte
	}{
		{"orphan stream", func(t *testing.T, built []byte) []byte {
			return appendUpdate(t, built, map[int]string{900: extStream("", "("+extSecret+") Tj")})
		}},
		{"owned form", func(t *testing.T, built []byte) []byte {
			n, d := pageOf(t, built, 0)
			d = strings.Replace(d, "/Resources <<", "/Resources << /XObject << /Hidden 900 0 R >>", 1)
			return appendUpdate(t, built, map[int]string{n: d,
				900: extStream("/Type /XObject /Subtype /Form /BBox [0 0 1 1]", "BT /F1 1 Tf ("+extSecret+") Tj ET")})
		}},
		{"thumbnail stand-in", func(t *testing.T, built []byte) []byte {
			n, d := pageOf(t, built, 0)
			d = strings.Replace(d, "/Type /Page", "/Type /Page /Thumb 900 0 R", 1)
			return appendUpdate(t, built, map[int]string{n: d,
				900: extStream("/Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8", "stand-in "+extSecret)})
		}},
		{"surviving /ActualText", func(t *testing.T, built []byte) []byte {
			n, d := pageOf(t, built, 0)
			d = regexp.MustCompile(`/Contents \d+ 0 R`).ReplaceAllString(d, "/Contents 900 0 R")
			return appendUpdate(t, built, map[int]string{n: d,
				900: extStream("", "/Span << /ActualText (code) >> BDC EMC")})
		}},
		{"a glyph run in a region", func(t *testing.T, built []byte) []byte {
			n, d := pageOf(t, built, 0)
			d = regexp.MustCompile(`/Contents \d+ 0 R`).ReplaceAllString(d, "/Contents 900 0 R")
			return appendUpdate(t, built, map[int]string{n: d, 900: extStream("", showAt("x", 100))})
		}},
		{"unexplained after page hygiene", func(t *testing.T, built []byte) []byte {
			// Page 1 keeps the secret in an entry page hygiene keeps (/Group).
			n, d := pageOf(t, built, 1)
			d = strings.Replace(d, "/Type /Page", "/Type /Page /Group << /S /Transparency /X ("+extSecret+") >>", 1)
			return appendUpdate(t, built, map[int]string{n: d})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHooks(t, false, false, func(b []byte) []byte { return tc.mutate(t, b) }, func() {
				_, out, err := redactWith(t, pdf, func(d *Document) {
					redactText(0, `SECRET-\d+`)(d)
					redactArea(0, Rect{X: 0, Y: 80, Width: 400, Height: 50})(d)
				})
				if out != nil || !errors.Is(err, ErrRedactionVerification) {
					t.Fatalf("RedactAndFlatten = (%v, %v), want ErrRedactionVerification", out, err)
				}
				if strings.Contains(err.Error(), "SECRET") {
					t.Errorf("the error holds document text: %v", err)
				}
			})
		})
	}
}

func TestRedactionReport_Summary(t *testing.T) {
	if s := (RedactionReport{}).Summary(); s != "" {
		t.Errorf("empty report summary = %q", s)
	}
	att := RedactionReport{RemovedAnnotations: []RemovedAnnotation{{Page: 2, Subtype: "FileAttachment", Reason: "attachment-policy"}}}
	if s := att.Summary(); s != "attachments policy: removed 1 file attachment(s) on page(s) 2" {
		t.Errorf("attachments only: %q (no automatic extension happened)", s)
	}
	var exts []RedactionExtension
	for p := 0; p < 14; p++ {
		exts = append(exts, RedactionExtension{Page: p, Source: "text-match", Where: "content"})
	}
	r := RedactionReport{Extensions: exts, RemovedInfoFields: []string{"Title"},
		RemovedAnnotations: []RemovedAnnotation{{Page: 3, Subtype: "Text", Reason: "needle"}}}
	want := "automatic extension: extended to 14 place(s) on page(s) 0, 1, 2, 3, 4, 5, 6, 7, 8, 9 and 4 more; " +
		"removed 1 annotation(s) on page(s) 3; removed document information field(s) Title"
	if s := r.Summary(); s != want {
		t.Errorf("summary = %q\nwant      %q", s, want)
	}
}

// TestRedactModelCache: a page is read once per document (RedactText on
// each PII kind and the pre-pass reuse its model), and Close drops the
// models.
func TestRedactModelCache(t *testing.T) {
	pdf := extPDF([]extPage{{content: showAt(extSecret, 700)}, {content: showAt("public", 400)}}, nil, "")
	d, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.redactionPage(0)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := d.redactionPage(0); again != first {
		t.Error("the page was read again")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if d.pageModels != nil {
		t.Error("Close keeps the page models")
	}
}

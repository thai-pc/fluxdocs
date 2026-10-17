package core

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redactFixture is a one-page 400x800 PDF with content and page resources
// res (Helvetica as /F1 is object 5), plus extra objects.
func redactFixture(content, res string, extra map[int]string) []byte {
	if res == "" {
		res = "<< /Font << /F1 5 0 R >> >>"
	}
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources " + res + " /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
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
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		if offs[i] == 0 {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, x)
	return b.Bytes()
}

// TestRedactArea_ValidatesRect pins P2: NaN or Inf is rejected with
// ErrInvalidArgument and marks nothing; a negative size is flipped.
func TestRedactArea_ValidatesRect(t *testing.T) {
	doc, err := OpenBytes(redactFixture("BT /F1 12 Tf 100 700 Td (SECRET-1) Tj ET", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Rect{
		{X: math.NaN(), Width: 1, Height: 1},
		{Y: math.Inf(1), Width: 1, Height: 1},
		{Width: math.Inf(-1), Height: 1},
		{Width: 1, Height: math.NaN()},
	} {
		if err := doc.RedactArea(0, r); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("RedactArea(%+v) = %v, want ErrInvalidArgument", r, err)
		}
	}
	if len(doc.redactions) != 0 {
		t.Fatalf("rejected rects were added: %v", doc.redactions)
	}
	if err := doc.RedactArea(99, Rect{X: math.NaN()}); !errors.Is(err, ErrPageNotFound) {
		t.Errorf("bad index with a bad rect = %v, want ErrPageNotFound first", err)
	}
	// Negative width and height, spanning the secret at (100, 700).
	if err := doc.RedactArea(0, Rect{X: 300, Y: 720, Width: -250, Height: -40}); err != nil {
		t.Fatal(err)
	}
	if got := doc.redactions[0][0]; got != (Rect{X: 50, Y: 680, Width: 250, Height: 40}) {
		t.Errorf("normalized rect = %+v", got)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := out.ExtractText(ExtractOptions{}); strings.Contains(text, "SECRET") {
		t.Errorf("secret survives a flipped region: %q", text)
	}
}

// TestRedactText_PropertyListMarksPage pins D14: a pattern matching only the
// /ActualText of a sequence that shows nothing still marks the page, and the
// flattened page no longer holds the value; the needle is recorded.
func TestRedactText_PropertyListMarksPage(t *testing.T) {
	doc, err := OpenBytes(redactFixture("/Span << /ActualText (SECRET-77) >> BDC EMC BT /F1 12 Tf 100 600 Td (public) Tj ET", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	rects, err := doc.RedactText(0, `SECRET-\d+`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rects) != 0 {
		t.Errorf("rects = %v, want none (the sequence shows nothing)", rects)
	}
	if len(doc.redactMarks[0]) != 1 {
		t.Fatalf("marks = %v, want the property mark", doc.redactMarks[0])
	}
	if got := doc.redactNeedles[0]; len(got) != 1 || got[0] != "SECRET-77" {
		t.Errorf("needles = %q", got)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SECRET")) {
		t.Error("the /ActualText value survives in the output")
	}
	if text, _ := out.ExtractText(ExtractOptions{}); !strings.Contains(text, "public") {
		t.Errorf("unmatched text removed: %q", text)
	}
}

// TestAutoRedactPII_ActualText: PII present only in /ActualText is found and
// stripped.
func TestAutoRedactPII_ActualText(t *testing.T) {
	doc, err := OpenBytes(redactFixture("/Span /MC0 BDC BT /F1 12 Tf 100 700 Td (XXXXXXXX) Tj ET EMC",
		"<< /Font << /F1 5 0 R >> /Properties << /MC0 6 0 R >> >>",
		map[int]string{6: "<< /ActualText (mail bob@example.com) >>"}))
	if err != nil {
		t.Fatal(err)
	}
	counts, err := doc.AutoRedactPII(PIIEmail)
	if err != nil {
		t.Fatal(err)
	}
	if counts[PIIEmail] != 1 {
		t.Errorf("counts = %v, want one email region (the sequence's run)", counts)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := out.Bytes()
	if bytes.Contains(raw, []byte("bob@example.com")) || bytes.Contains(raw, []byte("XXXXXXXX")) {
		t.Error("the property value or the sequence's text survives")
	}
}

// TestRedactText_RefusalReasons: routes the page cannot rewrite are refused
// with a *RedactionError matching ErrRedactionUnsupported, not ErrInvalidPDF
// (D2), and the plan stays failed.
func TestRedactText_RefusalReasons(t *testing.T) {
	doc, err := OpenBytes(redactFixture("/P1 scn 0 0 10 10 re f BT /F1 12 Tf 100 700 Td (SECRET-1) Tj ET",
		"<< /Font << /F1 5 0 R >> /Pattern << /P1 6 0 R >> >>",
		map[int]string{6: "<< /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 1 1] /XStep 1 /YStep 1 " +
			"/Resources << /Font << /F1 5 0 R >> >> /Length 29 >>\nstream\nBT /F1 1 Tf (SECRET-2) Tj ET\nendstream"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = doc.RedactText(0, `SECRET`)
	if err == nil || errors.Is(err, ErrInvalidPDF) || !strings.Contains(err.Error(), "tiling pattern") {
		t.Fatalf("RedactText = %v, want a pattern_text refusal", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("refusal holds document text: %v", err)
	}
	var re *RedactionError
	if !errors.As(err, &re) || re.Reason != RedactionPatternText || re.Page != 0 || !errors.Is(err, ErrRedactionUnsupported) {
		t.Errorf("RedactText = %#v, want a *RedactionError pattern_text on page 0", err)
	}
	out, err := doc.RedactAndFlatten()
	if out != nil || err == nil {
		t.Error("RedactAndFlatten ran after a refused RedactText")
	}
	// The sticky plan error keeps the refusal reachable.
	if !errors.As(err, &re) || re.Reason != RedactionPatternText || !errors.Is(err, ErrRedactionUnsupported) {
		t.Errorf("RedactAndFlatten = %v, want the pattern_text refusal wrapped", err)
	}
}

// TestRedactionErrors pins the error surface of P1 and P3: each refusal
// reason, the error chain, and text that never quotes the document.
func TestRedactionErrors(t *testing.T) {
	if !errors.Is(ErrRedactionVerification, ErrInternal) || errors.Is(ErrInternal, ErrRedactionVerification) {
		t.Error("ErrRedactionVerification must match ErrInternal (P3), not the reverse")
	}
	for _, tc := range []struct {
		name, content, res string
		extra              map[int]string
		reason             RedactionReason
	}{
		{"operand type", "BT /F1 12 Tf 20 Tc (x) Tc 100 700 Td (SECRET-1) Tj ET", "", nil, RedactionContentAmbiguous},
		{"keyword in a TJ array", "BT /F1 12 Tf 100 700 Td [ (A) SECRET-1 ] TJ ET", "", nil, RedactionContentAmbiguous},
		{"inline image", "BI /W 1 /H 1 /BPC 8 /CS /G ID AEI BT /F1 12 Tf 100 700 Td (SECRET-1) Tj ET", "", nil, RedactionInlineImage},
		{"form text", "/Fm1 Do", "<< /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >>",
			map[int]string{6: "<< /Type /XObject /Subtype /Form /BBox [0 0 9 9] /Resources << /Font << /F1 5 0 R >> >> /Length 28 >>\nstream\nBT /F1 9 Tf (SECRET-1) Tj ET\nendstream"},
			RedactionFormText},
		{"PostScript XObject", "/Ps Do", "<< /XObject << /Ps 6 0 R >> >>",
			map[int]string{6: "<< /Type /XObject /Subtype /PS /Length 0 >>\nstream\n\nendstream"}, RedactionUnsupportedXObject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytes(redactFixture(tc.content, tc.res, tc.extra))
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.RedactArea(0, Rect{X: 0, Y: 0, Width: 612, Height: 792}); err != nil {
				t.Fatal(err)
			}
			out, err := doc.RedactAndFlatten()
			var re *RedactionError
			if out != nil || !errors.As(err, &re) || re.Reason != tc.reason || re.Page != 0 {
				t.Fatalf("RedactAndFlatten = (%v, %v), want a %s refusal on page 0", out != nil, err, tc.reason)
			}
			if !errors.Is(err, ErrRedactionUnsupported) || errors.Is(err, ErrInvalidPDF) {
				t.Errorf("%v: want ErrRedactionUnsupported and not ErrInvalidPDF (D2)", err)
			}
			if form := tc.reason == RedactionFormText || tc.reason == RedactionUnsupportedXObject; form != errors.Is(err, redact.ErrFormXObjectText) {
				t.Errorf("%v: matches ErrFormXObjectText = %v, want %v", err, !form, form)
			}
			if msg := err.Error(); strings.Contains(msg, "SECRET") || strings.Contains(msg, "lexer error") ||
				!strings.HasPrefix(msg, "fluxdocs: content cannot be redacted safely: page 0 ("+string(tc.reason)+"): ") {
				t.Errorf("message %q: want the page and reason, and no document text", msg)
			}
		})
	}
}

// TestRedactAndFlatten_ForeignMarkIsInternal: a mark that does not belong to
// its page (a bug: marks are made and used on the same document) is
// ErrInternal naming the page, never a silent removal of another operator.
func TestRedactAndFlatten_ForeignMarkIsInternal(t *testing.T) {
	src := redactFixture("BT /F1 12 Tf 100 700 Td (SECRET-1) Tj ET", "", nil)
	a, err := OpenBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RedactText(0, `SECRET-\d`); err != nil {
		t.Fatal(err)
	}
	b.redactMarks = a.redactMarks // same offsets, another document's page
	out, err := b.RedactAndFlatten()
	if out != nil || !errors.Is(err, ErrInternal) || !strings.Contains(err.Error(), "page 0") {
		t.Fatalf("RedactAndFlatten = (%v, %v), want ErrInternal naming page 0", out != nil, err)
	}
}

// TestRedactAndFlatten_AfterRotate: a plan made before Rotate (which clones
// the page dictionary) still applies to the page (batch C review S3).
func TestRedactAndFlatten_AfterRotate(t *testing.T) {
	doc, err := OpenBytes(redactFixture("BT /F1 12 Tf 100 700 Td (SECRET-1) Tj ET", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RedactText(0, `SECRET-\d`); err != nil {
		t.Fatal(err)
	}
	if err := doc.Rotate(0, 90); err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatalf("RedactAndFlatten after Rotate: %v", err)
	}
	if out.Pages[0].Rotation != 90 {
		t.Errorf("rotation %d, want 90", out.Pages[0].Rotation)
	}
	if raw, _ := out.Bytes(); bytes.Contains(raw, []byte("SECRET-1")) {
		t.Error("secret survives")
	}
}

// TestRedactAndFlatten_SharedObjectsPaintedElsewhere: an image removed under
// a region on page 0 stays for page 1 when page 1 paints it, and is denied
// when page 1 only lists it in a shared resource dictionary (B3, C36).
func TestRedactAndFlatten_SharedObjectsPaintedElsewhere(t *testing.T) {
	two := func(content1 string) []byte {
		objs := map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [3 0 R 8 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
			3: "<< /Type /Page /Parent 2 0 R /Resources 7 0 R /Contents 4 0 R >>",
			4: "<< /Length 30 >>\nstream\nq 400 0 0 800 0 0 cm /Im1 Do Q\nendstream",
			5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			6: "<< /Type /XObject /Subtype /Image /Width 9 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 9 >>\nstream\nIMGMARKER\nendstream",
			7: "<< /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >>",
			8: "<< /Type /Page /Parent 2 0 R /Resources 7 0 R /Contents 9 0 R >>",
			9: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content1), content1),
		}
		return buildCorePDF(objs, "<< /Size 10 /Root 1 0 R >>")
	}
	for _, tc := range []struct {
		name, content1 string
		kept           bool
	}{
		{"page 1 paints the image", "q 10 0 0 10 0 0 cm /Im1 Do Q", true},
		{"page 1 does not", "BT /F1 12 Tf 72 700 Td (public) Tj ET", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytes(two(tc.content1))
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.RedactArea(0, Rect{X: 0, Y: 0, Width: 400, Height: 800}); err != nil {
				t.Fatal(err)
			}
			out, err := doc.RedactAndFlatten()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := out.Bytes()
			if got := bytes.Contains(raw, []byte("IMGMARKER")); got != tc.kept {
				t.Errorf("image written = %v, want %v", got, tc.kept)
			}
			if !tc.kept {
				return
			}
			// Page 1 still finds the image under /Im1.
			xo, ok, _ := out.resolver.ResolveDict(out.pageInfos[1].Resources["XObject"])
			if !ok {
				t.Fatal("page 1 lost its /XObject")
			}
			if im, _ := out.resolver.Resolve(xo["Im1"]); im == nil {
				t.Error("page 1's /Im1 no longer resolves")
			} else if _, isStream := im.(*parse.Stream); !isStream {
				t.Errorf("page 1's /Im1 = %v, want the image", im)
			}
		})
	}
}

// TestRedactionErrorDocumentLevel: a refusal for the document as a whole
// (Page -1) says so instead of naming a page.
func TestRedactionErrorDocumentLevel(t *testing.T) {
	err := &RedactionError{Page: -1, Reason: RedactionBudgetExceeded, Detail: "the objects shared with removed content exceed 1048576"}
	want := "fluxdocs: content cannot be redacted safely: document (budget_exceeded): the objects shared with removed content exceed 1048576"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrRedactionUnsupported) {
		t.Error("a document-level refusal must match ErrRedactionUnsupported")
	}
}

package core

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
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
// with a *redact.RefusalError, not ErrInvalidPDF, and the plan stays failed.
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
	if out, err := doc.RedactAndFlatten(); out != nil || err == nil {
		t.Error("RedactAndFlatten ran after a refused RedactText")
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

package redaction

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/docops/redact"
)

// The tests in this file pin redaction's fail-closed rule (SECURITY.md §1)
// against content the scanner cannot read losslessly. Other viewers may render
// a string that the lenient scanner drops (a syntax error that discards the
// pending operands, an inline image, a `stream` keyword) or pairs with a
// different operator (surplus operands, an unknown operator — a stack-based
// interpreter such as Ghostscript hands the leftover string to the next Tj).
// Before the fix, each case left the secret invisible to RedactText and in the
// output bytes after a full-page RedactArea. Now redaction refuses to run.

// unreadableContentCases is page content the strict scanner cannot read
// losslessly, each with the secret where some viewer may show it. The leak
// corpus (leakcorpus_test.go) runs them too.
var unreadableContentCases = []struct {
	name    string
	content string
}{
	{"TJ, unterminated over-deep array",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + strings.Repeat("[", overDeep) + " ] TJ ET"},
	{"TJ, unterminated over-deep dictionary",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + strings.Repeat("<< /A ", overDeep) + " ] TJ ET"},
	{"TJ, invalid hex string sibling",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") <zz> ] TJ ET"},
	{"TJ, stray ')' sibling",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") ) ] TJ ET"},
	{"TJ, stray '>' sibling",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") > ] TJ ET"},
	{"TJ, non-name dictionary key sibling",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") << 1 2 >> ] TJ ET"},
	{"Tj, stray ']' between operand and operator",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") ] Tj ET"},
	{"Tj, invalid hex string operand",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") <zz> Tj ET"},
	{"inline image dictionary with a syntax error",
		"BI /W 1 /H 1 /D <zz> ID \x00 EI BT /F1 24 Tf 72 700 Td (" + secret + ") Tj ET"},
	{"inline image without EI",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") Tj ET BI /W 1 /H 1 ID \x00\x01"},

	// An over-deep subtree may hold only what an operand may hold.
	{"operator inside an over-deep array",
		"BT /F1 24 Tf 72 700 Td " + openArrays(overDeep) + " (" + secret + ") Tj " + closeArrays(overDeep) + " TJ ET"},
	{"inline image inside an over-deep array, '(' in its data",
		"BT /F1 24 Tf 72 700 Td " + openArrays(overDeep) + " BI /W 1 /H 1 /BPC 8 /CS /G ID\n( EI " + closeArrays(overDeep) + " TJ\n(" +
			secret + ") Tj %)\n" + closeArrays(overDeep) + " TJ ET"},
	{"stream keyword inside an over-deep array",
		"BT /F1 24 Tf 72 700 Td " + openArrays(overDeep) + " << >> stream\n(" + secret + ") Tj\nendstream " + closeArrays(overDeep) + " TJ ET"},
	{"over-deep array closed by '>>'",
		"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + openArrays(overDeep) + "1" + closeArrays(overDeep-1) + ">> ] TJ ET"},

	// Operands pending at an inline image or at the end of the stream.
	{"operand pending before an inline image",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") BI /W 1 /H 1 /BPC 8 /CS /G ID\n\x00 EI Tj ET"},
	{"operand pending at end of stream",
		"BT /F1 24 Tf 72 700 Td (public) Tj ET (" + secret + ")"},

	// No stream data inside a content stream (ISO 32000-1 §7.3.8.1).
	{"stream keyword after a dictionary",
		"BT /F1 24 Tf 72 700 Td << >> stream\n(" + secret + ") Tj\nendstream ET"},

	// Operands that do not match their operator (ISO 32000-1 Annex A).
	{"Tj with an over-deep sibling operand after the string",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") " + deepArrays(overDeep) + " Tj ET"},
	{"Tj with an over-deep sibling operand before the string",
		"BT /F1 24 Tf 72 700 Td " + deepArrays(overDeep) + " (" + secret + ") Tj ET"},
	{"surplus operand before Td",
		"BT /F1 24 Tf (" + secret + ") 72 700 Td Tj ET"},
	{"surplus string before Tj",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") (x) Tj Tj ET"},
	{"unknown operator with an operand",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") Xq Tj ET"},
	{"unknown operator with an operand inside BX/EX",
		"BX BT /F1 24 Tf 72 700 Td (" + secret + ") Xq Tj ET EX"},
	{"procedure brace with an operand",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") { Tj ET"},
	{"string shown by TJ",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") TJ ET"},
	{"string left over by sc",
		"BT /F1 24 Tf 72 700 Td (" + secret + ") 1 0 0 sc Tj ET"},
}

func openArrays(n int) string  { return strings.Repeat("[", n) }
func closeArrays(n int) string { return strings.Repeat("]", n) }

// TestRedact_UnreadableOperandFailsClosed: when the content cannot be read
// losslessly — an operand that does not parse, operands the scanner would drop,
// or operands a viewer could pair with a different operator — redaction cannot
// know what a viewer would show, so RedactText, AutoRedactPII and
// RedactAndFlatten return an error and produce no document.
func TestRedact_UnreadableOperandFailsClosed(t *testing.T) {
	for _, tc := range unreadableContentCases {
		t.Run(tc.name, func(t *testing.T) {
			pdf := contentPDF(tc.content)

			doc, err := core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			rects, err := doc.RedactText(0, `SECRET-\d+`)
			if err == nil {
				t.Errorf("RedactText err = nil (rects %v), want a fail-closed error", rects)
			}
			if out, err := doc.RedactAndFlatten(); err == nil || out != nil {
				t.Errorf("RedactAndFlatten after failed RedactText = (%v, %v), want (nil, error)", out, err)
			}

			doc, err = core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := doc.AutoRedactPII(); err == nil {
				t.Errorf("AutoRedactPII err = nil, want a fail-closed error")
			}

			doc, err = core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.RedactArea(0, fullPage); err != nil {
				t.Fatal(err)
			}
			if out, err := doc.RedactAndFlatten(); err == nil || out != nil {
				t.Errorf("RedactAndFlatten after RedactArea = (%v, %v), want (nil, error)", out, err)
			}
		})
	}
}

// TestRedact_MalformedPageNotInPlanIsCopied: fail-closed applies only to pages
// that are actually redacted; an unreadable page elsewhere is copied as-is.
func TestRedact_MalformedPageNotInPlanIsCopied(t *testing.T) {
	doc, err := core.OpenBytes(buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj("BT /F1 24 Tf 72 700 Td (" + secret + ") Tj ET"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
		7: streamObj("BT /F1 24 Tf 72 700 Td [ (public) <zz> ] TJ ET"),
	}, "<< /Size 8 /Root 1 0 R >>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RedactText(0, `SECRET-\d+`); err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	assertNoSecret(t, out)
	if out.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", out.PageCount)
	}
}

// formPDF is a one-page PDF whose page paints the Form XObject /Fm1 (object 6)
// with pageContent; formContent is /Fm1's content, and extra adds objects (a
// nested form, an image) with the page's /XObject entries in xobjects.
func formPDF(pageContent, formContent string, extra map[int]string, xobjects string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R " + xobjects +
			" >> >> /Contents 4 0 R >>",
		4: streamObj(pageContent),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6: fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 400 800] /Resources << /Font << /F1 5 0 R >> "+
			"/XObject << /Fm2 7 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(formContent), formContent),
	}
	for k, v := range extra {
		objs[k] = v
	}
	return buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1))
}

// TestRedact_FormXObjectTextFailsClosed: redaction rewrites only the page's own
// content stream, so a page that draws text through a Form XObject — directly,
// nested, or through a form that cannot be read — is refused (interim, until
// forms are rewritten). Forms without text and image XObjects are fine.
func TestRedact_FormXObjectTextFailsClosed(t *testing.T) {
	text := "BT /F1 24 Tf 72 700 Td (" + secret + ") Tj ET"
	vector := "0 0 m 10 10 l S"
	nested := func(content string) map[int]string {
		return map[int]string{7: fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 400 800] "+
			"/Resources << /Font << /F1 5 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(content), content)}
	}
	refuse := []struct {
		name string
		pdf  []byte
	}{
		{"text in the form", formPDF("q /Fm1 Do Q", text, nested(vector), "")},
		{"text in a nested form", formPDF("q /Fm1 Do Q", "q /Fm2 Do Q", nested(text), "")},
		{"TJ in the form", formPDF("/Fm1 Do", "BT /F1 24 Tf 72 700 Td [("+secret+")] TJ ET", nested(vector), "")},
		{"unreadable form content", formPDF("/Fm1 Do", "("+secret+") Xq Tj", nested(vector), "")},
		{"self-referencing form with text", formPDF("/Fm1 Do", "/Fm1 Do "+text, nested(vector), "")},
	}
	for _, tc := range refuse {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := core.OpenBytes(tc.pdf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := doc.RedactText(0, `SECRET-\d+`); err == nil {
				t.Errorf("RedactText err = nil, want a fail-closed error")
			}
			if out, err := doc.RedactAndFlatten(); err == nil || out != nil {
				t.Errorf("RedactAndFlatten after RedactText = (%v, %v), want (nil, error)", out, err)
			}

			doc, err = core.OpenBytes(tc.pdf)
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.RedactArea(0, fullPage); err != nil {
				t.Fatal(err)
			}
			out, err := doc.RedactAndFlatten()
			if err == nil || out != nil {
				t.Fatalf("RedactAndFlatten after RedactArea = (%v, %v), want (nil, error)", out, err)
			}
			if strings.Contains(tc.name, "text") && !errors.Is(err, redact.ErrFormXObjectText) {
				t.Errorf("err = %v, want redact.ErrFormXObjectText", err)
			}
		})
	}

	// A form with no text, and an image XObject, do not block redaction of the
	// page's own text.
	image := map[int]string{
		7: "<< /Type /XObject /Subtype /Form /BBox [0 0 1 1] /Length 0 >>\nstream\n\nendstream",
		8: "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x00\nendstream",
	}
	doc, err := core.OpenBytes(formPDF("q /Fm1 Do /Im1 Do Q "+text, "q /Fm2 Do Q "+vector, image, "/Im1 8 0 R"))
	if err != nil {
		t.Fatal(err)
	}
	if rects, err := doc.RedactText(0, `SECRET-\d+`); err != nil || len(rects) == 0 {
		t.Fatalf("RedactText = (%v, %v), want a match", rects, err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	assertNoSecret(t, out)
}

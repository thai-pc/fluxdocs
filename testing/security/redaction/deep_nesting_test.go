package redaction

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// The tests in this file pin redaction against operands nested past the
// parser's depth bound (SECURITY.md §1). Other viewers render a string in a
// TJ/Tj operand list even when a sibling operand is over-deep, so the content
// scanner must keep that string visible (the over-deep part becomes one Null)
// for redaction to find and remove it. Before, the depth error discarded the
// whole operand list, so the secret was invisible to RedactText and survived a
// full-page RedactArea in the output bytes.

const secret = "SECRET-1234"

// overDeep is the nesting depth used for the over-deep sibling operand: past the
// parser's bound (256) but far from the stack limit.
const overDeep = 300

// contentPDF is a one-page 400x800 PDF whose page content is content, with a
// Helvetica /F1.
func contentPDF(content string) []byte {
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

func deepArrays(n int) string { return strings.Repeat("[", n) + "1" + strings.Repeat("]", n) }
func deepDicts(n int) string  { return strings.Repeat("<< /A ", n) + "1" + strings.Repeat(" >>", n) }
func deepMixed(n int) string {
	return strings.Repeat("[ << /A ", n/2) + "1" + strings.Repeat(" >> ]", n/2)
}

// fullPage covers the whole 400x800 page.
var fullPage = core.Rect{X: 0, Y: 0, Width: 400, Height: 800}

// assertNoSecret fails if the redacted document still carries the secret in its
// serialized bytes (the rewritten content stream is stored uncompressed) or in
// its extracted text.
func assertNoSecret(t *testing.T, out *core.Document) {
	t.Helper()
	raw, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Errorf("LEAK: secret bytes remain in the redacted PDF")
	}
	text, err := out.ExtractText(core.ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "SECRET") || strings.Contains(text, "1234") {
		t.Errorf("LEAK: redacted text still extractable: %q", text)
	}
}

// TestRedact_OverDeepSiblingOperand: a balanced over-deep array/dictionary next
// to the secret is skipped as one placeholder, so the secret stays visible to
// extraction and RedactText, and both RedactText and a full-page RedactArea
// remove it.
func TestRedact_OverDeepSiblingOperand(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"TJ, deep array after the string",
			"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + deepArrays(overDeep) + " ] TJ ET"},
		{"TJ, deep array before the string",
			"BT /F1 24 Tf 72 700 Td [ " + deepArrays(overDeep) + " (" + secret + ") ] TJ ET"},
		{"TJ, deep dictionary after the string",
			"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + deepDicts(overDeep) + " ] TJ ET"},
		{"TJ, deep mixed array/dictionary after the string",
			"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + deepMixed(overDeep) + " ] TJ ET"},
		{"TJ, deep elements on both sides",
			"BT /F1 24 Tf 72 700 Td [ " + deepDicts(overDeep) + " (" + secret + ") " + deepArrays(overDeep) + " ] TJ ET"},
		{"Tj, deep array after the string",
			"BT /F1 24 Tf 72 700 Td (" + secret + ") " + deepArrays(overDeep) + " Tj ET"},
		{"Tj, deep array before the string",
			"BT /F1 24 Tf 72 700 Td " + deepArrays(overDeep) + " (" + secret + ") Tj ET"},
		{"TJ, brackets inside strings in the deep subtree do not count",
			"BT /F1 24 Tf 72 700 Td [ (" + secret + ") " + strings.Repeat("[ (]]>>) <5d5d> % ]]\n", overDeep) +
				strings.Repeat("]", overDeep) + " ] TJ ET"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pdf := contentPDF(tc.content)

			doc, err := core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			if before, _ := doc.ExtractText(core.ExtractOptions{}); !strings.Contains(before, secret) {
				t.Errorf("secret not extractable before redaction (other viewers render it): %q", before)
			}
			rects, err := doc.RedactText(0, `SECRET-\d+`)
			if err != nil {
				t.Fatalf("RedactText: %v", err)
			}
			if len(rects) == 0 {
				t.Errorf("RedactText found no match for a rendered secret")
			}
			out, err := doc.RedactAndFlatten()
			if err != nil {
				t.Fatalf("RedactAndFlatten after RedactText: %v", err)
			}
			assertNoSecret(t, out)

			doc, err = core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.RedactArea(0, fullPage); err != nil {
				t.Fatal(err)
			}
			out, err = doc.RedactAndFlatten()
			if err != nil {
				t.Fatalf("RedactAndFlatten after RedactArea: %v", err)
			}
			assertNoSecret(t, out)
		})
	}
}

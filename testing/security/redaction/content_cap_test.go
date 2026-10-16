package redaction

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// oversizedContentPDF is secret's page whose /Contents array lists the text
// stream once and then a 1 MiB stream of spaces 300 times: about 300 MiB of
// content, past parse.MaxPageContent (render-budget.md §9 R1).
func oversizedContentPDF() []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(bytes.Repeat([]byte(" "), 1<<20))
	zw.Close()
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents [4 0 R " +
			strings.Repeat("6 0 R ", 300) + "] >>",
		4: streamObj("BT /F1 24 Tf 72 700 Td (" + secret + ") Tj ET"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6: fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes()),
	}, "<< /Size 7 /Root 1 0 R >>")
}

// TestRedact_OversizedContentFailsClosed: a page whose content exceeds the
// per-page cap cannot be read, so it cannot be redacted. RedactText,
// AutoRedactPII and RedactAndFlatten return an error naming the cap
// (parse.ErrDecodeLimit) and produce no document; a failed RedactText leaves
// the plan failed, so a later RedactAndFlatten refuses too. Before the cap the
// content was concatenated whole and redaction ran over it.
func TestRedact_OversizedContentFailsClosed(t *testing.T) {
	pdf := oversizedContentPDF()
	open := func() *core.Document {
		t.Helper()
		doc, err := core.OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}

	doc := open()
	rects, err := doc.RedactText(0, `SECRET-\d+`)
	if !errors.Is(err, parse.ErrDecodeLimit) {
		t.Errorf("RedactText = %v, %v; want an error wrapping parse.ErrDecodeLimit", rects, err)
	}
	if out, err := doc.RedactAndFlatten(); out != nil || !errors.Is(err, parse.ErrDecodeLimit) ||
		!strings.Contains(err.Error(), "redaction plan incomplete") {
		t.Errorf("RedactAndFlatten after failed RedactText = (%v, %v), want (nil, plan incomplete)", out, err)
	}

	if _, err := open().AutoRedactPII(); !errors.Is(err, parse.ErrDecodeLimit) {
		t.Errorf("AutoRedactPII err = %v, want an error wrapping parse.ErrDecodeLimit", err)
	}

	doc = open()
	if err := doc.RedactArea(0, fullPage); err != nil {
		t.Fatal(err)
	}
	if out, err := doc.RedactAndFlatten(); out != nil || !errors.Is(err, parse.ErrDecodeLimit) {
		t.Errorf("RedactAndFlatten after RedactArea = (%v, %v), want (nil, an error wrapping parse.ErrDecodeLimit)", out, err)
	}

	// Extraction reports the same error rather than returning partial text.
	if text, err := open().ExtractText(core.ExtractOptions{}); !errors.Is(err, parse.ErrDecodeLimit) || text != "" {
		t.Errorf("ExtractText = %d bytes, %v; want an error wrapping parse.ErrDecodeLimit", len(text), err)
	}
}

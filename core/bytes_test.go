package core

import (
	"bytes"
	"testing"
)

func TestDocumentBytes(t *testing.T) {
	doc := docWithText(t, "verbatim content")
	var saved bytes.Buffer
	if err := doc.SaveTo(&saved); err != nil {
		t.Fatal(err)
	}
	src, err := OpenBytes(saved.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// Bytes() returns the exact opened bytes.
	got, err := src.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, saved.Bytes()) {
		t.Errorf("Bytes() != opened bytes (len %d vs %d)", len(got), len(saved.Bytes()))
	}
	// The returned slice is a copy: mutating it must not affect the document.
	if len(got) > 0 {
		got[0] = 'X'
	}
	again, _ := src.Bytes()
	if again[0] == 'X' {
		t.Errorf("Bytes() returned an aliased slice, not a copy")
	}
}

func TestDocumentBytes_ResolverLess(t *testing.T) {
	if _, err := (&Document{}).Bytes(); err != ErrInvalidPDF {
		t.Errorf("err = %v, want ErrInvalidPDF", err)
	}
}

// TestConvertToPDFA_BytesPreservesMarkers proves ConvertToPDFA -> Bytes() keeps
// the PDF/A markers (unlike SaveTo, which rebuilds and drops them).
func TestConvertToPDFA_BytesPreservesMarkers(t *testing.T) {
	doc := docWithText(t, "archive")
	pdfaDoc, err := doc.ConvertToPDFA()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pdfaDoc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := reopened.ValidatePDFA(); !r.Conformant {
		t.Errorf("PDF/A markers lost through Bytes() round-trip: %+v", r.Checks)
	}
}

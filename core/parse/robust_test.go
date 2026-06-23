package parse

import "testing"

// These tests pin down hostile-input robustness (SECURITY.md §2): the parser
// must never panic on out-of-range offsets, only return errors / EOF.

func TestNextToken_NegativeOffset(t *testing.T) {
	// A negative offset (e.g. from a malformed xref) must yield EOF, not panic.
	tok, _, err := nextToken([]byte("1 0 obj"), -5)
	if err != nil {
		t.Fatalf("nextToken negative offset err = %v, want nil", err)
	}
	if tok.kind != tokEOF {
		t.Errorf("nextToken negative offset kind = %v, want tokEOF", tok.kind)
	}
}

func TestParseIndirectObjectAt_OffsetBeyondEnd(t *testing.T) {
	// An offset past end of input must error, not panic.
	data := []byte("1 0 obj << >> endobj")
	if _, _, _, _, err := ParseIndirectObjectAt(data, len(data)+100); err == nil {
		t.Error("ParseIndirectObjectAt past end = nil error, want an error")
	}
}

func TestResolver_OutOfRangeXrefOffset(t *testing.T) {
	// Craft a valid xref whose single in-use entry points far past EOF. Resolving
	// it must return an error, never panic.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [] /Count 0 >>",
	}, "<< /Size 3 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper: point object 2 at a wild offset.
	r.xref.Entries[2] = XrefEntry{Offset: int64(len(pdf) + 10_000)}

	if _, err := r.Resolve(Reference{Number: 2}); err == nil {
		t.Error("Resolve with out-of-range offset = nil error, want an error")
	}
}

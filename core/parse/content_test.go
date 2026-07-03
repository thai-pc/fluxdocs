package parse

import (
	"fmt"
	"testing"
)

func TestScanContent_OperandsAndOperators(t *testing.T) {
	ops := ScanContent([]byte("BT /F1 12 Tf 100 700 Td (Hello) Tj ET"))

	want := []struct {
		op       string
		operands int
	}{
		{"BT", 0},
		{"Tf", 2},
		{"Td", 2},
		{"Tj", 1},
		{"ET", 0},
	}
	if len(ops) != len(want) {
		t.Fatalf("got %d ops, want %d: %+v", len(ops), len(want), ops)
	}
	for i, w := range want {
		if ops[i].Op != w.op || len(ops[i].Operands) != w.operands {
			t.Errorf("op %d = %q(%d operands), want %q(%d)", i, ops[i].Op, len(ops[i].Operands), w.op, w.operands)
		}
	}

	// Tf operands: /F1 and 12.
	if n, ok := ops[1].Operands[0].(Name); !ok || n != "F1" {
		t.Errorf("Tf operand 0 = %v, want /F1", ops[1].Operands[0])
	}
	if i, ok := ops[1].Operands[1].(Integer); !ok || i != 12 {
		t.Errorf("Tf operand 1 = %v, want 12", ops[1].Operands[1])
	}
	// Tj operand: (Hello).
	if s, ok := ops[3].Operands[0].(String); !ok || string(s.Value) != "Hello" {
		t.Errorf("Tj operand = %v, want (Hello)", ops[3].Operands[0])
	}
}

func TestScanContent_TJArray(t *testing.T) {
	ops := ScanContent([]byte("[(A) -250 (B)] TJ"))
	if len(ops) != 1 || ops[0].Op != "TJ" {
		t.Fatalf("got %+v, want a single TJ op", ops)
	}
	arr, ok := ops[0].Operands[len(ops[0].Operands)-1].(Array)
	if !ok || len(arr) != 3 {
		t.Fatalf("TJ operand = %v, want a 3-element array", ops[0].Operands)
	}
	if s, ok := arr[0].(String); !ok || string(s.Value) != "A" {
		t.Errorf("arr[0] = %v, want (A)", arr[0])
	}
	if n, ok := arr[1].(Integer); !ok || n != -250 {
		t.Errorf("arr[1] = %v, want -250", arr[1])
	}
}

func TestScanContent_BooleansAndReals(t *testing.T) {
	ops := ScanContent([]byte("true 1.5 /X gs"))
	if len(ops) != 1 || ops[0].Op != "gs" {
		t.Fatalf("got %+v, want a single gs op", ops)
	}
	if len(ops[0].Operands) != 3 {
		t.Fatalf("gs operands = %v, want 3", ops[0].Operands)
	}
	if b, ok := ops[0].Operands[0].(Boolean); !ok || !bool(b) {
		t.Errorf("operand 0 = %v, want true", ops[0].Operands[0])
	}
	if r, ok := ops[0].Operands[1].(Real); !ok || r != 1.5 {
		t.Errorf("operand 1 = %v, want 1.5", ops[0].Operands[1])
	}
}

func TestScanContent_SkipsInlineImage(t *testing.T) {
	// The binary between ID and EI contains bytes that would break a naive
	// tokenizer, including a '(' and a stray 'E'. The (Text) Tj after EI must
	// still be recovered.
	content := "q BI /W 2 /H 2 /BPC 8 /CS /G ID \x00(\xffE\x80\x10 EI Q (Text) Tj"
	ops := ScanContent([]byte(content))

	var sawTj bool
	for _, op := range ops {
		if op.Op == "ID" || op.Op == "BI" {
			t.Errorf("inline-image operator %q leaked into ops", op.Op)
		}
		if op.Op == "Tj" {
			sawTj = true
			if s, ok := op.Operands[0].(String); !ok || string(s.Value) != "Text" {
				t.Errorf("Tj after inline image = %v, want (Text)", op.Operands[0])
			}
		}
	}
	if !sawTj {
		t.Errorf("did not recover the Tj after the inline image; ops = %+v", ops)
	}
}

func TestScanContent_LenientOnGarbage(t *testing.T) {
	// A stray '>' is a lexer error mid-stream; scanning must recover, not hang.
	ops := ScanContent([]byte("(a) Tj > (b) Tj"))
	var count int
	for _, op := range ops {
		if op.Op == "Tj" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("recovered %d Tj ops across a garbage token, want 2", count)
	}
}

// streamObj builds an indirect-object body for an uncompressed content stream.
func streamObj(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func TestPageContent_SingleStream(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: streamObj("(hi) Tj"),
	}, "<< /Size 5 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.PageContent(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "(hi) Tj" {
		t.Errorf("PageContent = %q, want %q", got, "(hi) Tj")
	}
}

func TestPageContent_ArrayOfStreams(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents [4 0 R 5 0 R] >>",
		4: streamObj("(a) Tj"),
		5: streamObj("(b) Tj"),
	}, "<< /Size 6 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.PageContent(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	// Streams are joined with a newline separator.
	if string(got) != "(a) Tj\n(b) Tj\n" {
		t.Errorf("PageContent = %q, want %q", got, "(a) Tj\n(b) Tj\n")
	}
}

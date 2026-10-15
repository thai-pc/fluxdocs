package parse

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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

// TestScanContentSpansStrict reports every place the lenient scanner discards
// input — which could hide a string from redaction — and nothing else.
func TestScanContentSpansStrict(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"well-formed text", "BT /F1 12 Tf 100 700 Td (Hello) Tj [(A) -250 (B)] TJ ET", false},
		{"inline image", "q BI /W 2 /H 2 /BPC 8 /CS /G ID \x00(\xffE\x80\x10 EI Q (Text) Tj", false},
		{"marked content dictionary", "/Span << /ActualText (x) /MCID 3 >> BDC (x) Tj EMC", false},
		{"comments and empty array", "% hi\n[] 0 d (x) Tj % bye", false},
		{"empty stream", "", false},
		{"token error in TJ array", "[(SECRET) <zz>] TJ", true},
		{"unterminated string", "(SECRET) Tj (oops", true},
		{"unterminated array", "[(SECRET)", true},
		{"stray ']'", "(SECRET) ] Tj", true},
		{"stray '>>'", "(SECRET) >> Tj", true},
		{"stray ')'", "(SECRET) ) Tj", true},
		{"non-name dictionary key", "<< 1 2 >> BDC (SECRET) Tj", true},
		{"inline image token error", "BI /W <zz> ID x EI (SECRET) Tj", true},
		{"inline image without ID", "(SECRET) Tj BI /W 1", true},
		{"inline image without EI", "(SECRET) Tj BI /W 1 ID xx", true},
		{"operand pending before inline image", "(SECRET) BI /W 1 ID x EI Tj", true},
		{"operand pending at end of stream", "(x) Tj (SECRET)", true},
		{"stream keyword after a dictionary", "<< >> stream\n(SECRET) Tj\nendstream", true},
		{"operator inside an over-deep array", strings.Repeat("[", 300) + "(SECRET) Tj" + strings.Repeat("]", 300) + " TJ", true},
		{"over-deep array closed by '>>'", "[" + strings.Repeat("[", 300) + strings.Repeat("]", 299) + ">>] TJ", true},
		{"balanced over-deep TJ element", "[(SECRET) " + strings.Repeat("[", 300) + strings.Repeat("]", 300) + "] TJ", false},
		{"surplus operand", "(SECRET) 72 700 Td Tj", true},
		{"unknown operator with an operand", "(SECRET) Xq Tj", true},
		{"unknown operator without operands", "Xq BX Yq EX (x) Tj", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spans, err := ScanContentSpansStrict([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if spans != nil {
					t.Errorf("spans = %v on error, want nil", spans)
				}
				return
			}
			if want := ScanContentSpans([]byte(tc.in)); !reflect.DeepEqual(spans, want) {
				t.Errorf("strict spans = %v, want the lenient spans %v", spans, want)
			}
		})
	}
}

// TestOperandsFit pins the Annex A operand rules used by strict scanning.
func TestOperandsFit(t *testing.T) {
	str, num, name, arr := String{Value: []byte("s")}, Integer(1), Name("N"), Array{}
	tests := []struct {
		op       string
		operands []Object
		want     bool
	}{
		{"Tj", []Object{str}, true},
		{"Tj", []Object{num}, false},
		{"Tj", []Object{str, str}, false},
		{"Tj", nil, false},
		{"'", []Object{str}, true},
		{"\"", []Object{num, num, str}, true},
		{"\"", []Object{num, str, str}, false},
		{"TJ", []Object{arr}, true},
		{"TJ", []Object{str}, false},
		{"d", []Object{arr, num}, true},
		{"d", []Object{str, num}, false},
		{"Td", []Object{num, num}, true},
		{"Td", []Object{str, num, num}, false},
		{"cm", []Object{num, num, num, num, num, num}, true},
		{"cm", []Object{num, num, num, num, num}, false},
		{"Tf", []Object{name, num}, true},
		{"BDC", []Object{name, Dict{}}, true},
		{"EMC", nil, true},
		{"sc", []Object{num, num, num}, true},
		{"sc", []Object{str, num, num, num}, false},
		{"sc", []Object{num, num, num, num, num}, false},
		{"sc", nil, false},
		{"scn", []Object{num, num, num, name}, true},
		{"scn", []Object{name}, true},
		{"SCN", []Object{str, name}, false},
		{"Xq", nil, true},
		{"Xq", []Object{num}, false},
		{"{", []Object{str}, false},
	}
	for _, tc := range tests {
		if got := operandsFit(tc.op, tc.operands); got != tc.want {
			t.Errorf("operandsFit(%q, %v) = %v, want %v", tc.op, tc.operands, got, tc.want)
		}
	}
	if len(fixedOperands)+len([]string{"BI", "SC", "SCN", "sc", "scn"}) != 73 {
		t.Errorf("table has %d fixed-arity operators; Annex A lists 73 operators in total", len(fixedOperands))
	}
}

// TestScanContent_UnterminatedStringIsLinear: after a token that runs to end of
// input the scanner resumes at the end, not one byte later — otherwise each
// retry re-lexes to the end (1 MB of '(' took hours). The 2s bound is scaled by
// timingScale for -race and WASM.
func TestScanContent_UnterminatedStringIsLinear(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"literal strings", strings.Repeat("(", 1<<20)},
		{"array of literal strings", strings.Repeat("[(", 200_000)},
		{"hex strings", strings.Repeat("<0", 200_000)},
		{"inline-image dictionaries", strings.Repeat("BI (", 200_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			begin := time.Now()
			ScanContentSpans([]byte(tc.in))
			_, err := ScanContentSpansStrict([]byte(tc.in))
			if elapsed, limit := time.Since(begin), 2*time.Second*timingScale; elapsed > limit {
				t.Errorf("scan took %v, want < %v", elapsed, limit)
			}
			if err == nil {
				t.Errorf("strict scan of an unterminated string: err = nil")
			}
		})
	}
}

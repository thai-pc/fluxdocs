package parse

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"runtime"
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

// flateStreamObj is streamObj for a FlateDecode stream of content.
func flateStreamObj(content []byte) string {
	z := zlibBytes(content)
	return fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(z), z)
}

// contentsPage returns the resolver and page of a one-page PDF whose /Contents
// is contents, with streams as objects 4, 5, ….
func contentsPage(t *testing.T, contents string, streams ...string) (*Resolver, PageInfo) {
	t.Helper()
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents " + contents + " >>",
	}
	for i, s := range streams {
		objs[4+i] = s
	}
	r, err := NewResolver(buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", len(objs)+1)))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	return r, pages[0]
}

// TestPageContentLimit pins the total cap on a page's content (render-budget.md
// §9 R1): the decoded streams, each repeat counted again, plus one separator
// per stream. Content at the limit is returned with used = its length; one
// byte more fails with errLex and ErrDecodeLimit and no data.
func TestPageContentLimit(t *testing.T) {
	a := flateStreamObj([]byte("(a) Tj")) // 6 bytes decoded
	b := flateStreamObj([]byte("(b) Tj"))
	chain := fmt.Sprintf("<< /Filter [/FlateDecode /FlateDecode] /Length %d >>\nstream\n%s\nendstream",
		len(zlibBytes(zlibBytes([]byte("(c) Tj")))), zlibBytes(zlibBytes([]byte("(c) Tj"))))
	tests := []struct {
		name     string
		contents string
		streams  []string
		limit    int64
		want     string // when wantErr is false
		wantUsed int64  // when wantErr is false; -1 means len(want)
		wantErr  bool
	}{
		{"repeated reference", "[4 0 R 4 0 R 4 0 R]", []string{a}, MaxPageContent, "(a) Tj\n(a) Tj\n(a) Tj\n", -1, false},
		{"repeat at the limit", "[4 0 R 4 0 R]", []string{a}, 14, "(a) Tj\n(a) Tj\n", -1, false},
		{"repeat one byte over", "[4 0 R 4 0 R]", []string{a}, 13, "", 0, true},
		{"distinct streams at the limit", "[4 0 R 5 0 R]", []string{a, b}, 14, "(a) Tj\n(b) Tj\n", -1, false},
		{"distinct streams one byte over", "[4 0 R 5 0 R]", []string{a, b}, 13, "", 0, true},
		{"non-stream element skipped", "[4 0 R 1 0 R 4 0 R]", []string{a}, 14, "(a) Tj\n(a) Tj\n", -1, false},
		{"single stream at the limit", "4 0 R", []string{a}, 6, "(a) Tj", -1, false},
		{"single stream one byte over", "4 0 R", []string{a}, 5, "", 0, true},
		{"unfiltered stream counts its length", "4 0 R", []string{streamObj("(abc) Tj")}, 7, "", 0, true},
		{"unfiltered stream at the limit", "[4 0 R]", []string{streamObj("(abc) Tj")}, 9, "(abc) Tj\n", -1, false},
		// A chain's intermediate stage counts: (c) Tj's zlib form plus (c) Tj.
		{"filter chain counts every stage", "4 0 R", []string{chain}, MaxPageContent, "(c) Tj", int64(len(zlibBytes([]byte("(c) Tj"))) + 6), false},
		{"limit above MaxPageContent is clamped", "[4 0 R]", []string{a}, 1 << 62, "(a) Tj\n", -1, false},
		{"no content", "[]", nil, 0, "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, pi := contentsPage(t, tc.contents, tc.streams...)
			data, used, err := r.PageContentLimit(pi, tc.limit)
			if tc.wantErr {
				if !errors.Is(err, ErrDecodeLimit) || !errors.Is(err, errLex) || data != nil {
					t.Fatalf("err = %v, data %q; want errLex and ErrDecodeLimit, no data", err, data)
				}
				if used <= tc.limit {
					t.Errorf("used = %d, want > the limit %d", used, tc.limit)
				}
				return
			}
			if err != nil || string(data) != tc.want {
				t.Fatalf("PageContentLimit = %q, %v; want %q", data, err, tc.want)
			}
			want := tc.wantUsed
			if want < 0 {
				want = int64(len(tc.want))
			}
			if used != want {
				t.Errorf("used = %d, want %d", used, want)
			}
		})
	}
}

// TestPageContent_RepeatedBombIsCapped: a /Contents array listing one 1 MiB
// stream 300 times used to concatenate 300 MiB (N × 256 MiB for a real bomb,
// render-budget.md §9 R1). PageContent stops at MaxPageContent and decodes the
// repeated stream once, so the failure allocates about one decode.
func TestPageContent_RepeatedBombIsCapped(t *testing.T) {
	stream := flateStreamObj(bytes.Repeat([]byte(" "), 1<<20))
	r, pi := contentsPage(t, "["+strings.Repeat("4 0 R ", 300)+"]", stream)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := r.PageContent(pi)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrDecodeLimit) || !errors.Is(err, errLex) || data != nil {
		t.Fatalf("PageContent: %d bytes, err %v; want errLex and ErrDecodeLimit", len(data), err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(MaxPageContent)) {
		t.Errorf("err = %q, want it to name the cap %d", err, MaxPageContent)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Errorf("PageContent allocated %d MB, want ≤ 32 MB (one 1 MiB decode)", alloc>>20)
	}
}

// TestPageContent_RepeatedStreamDecodedOnce: within the cap, a repeated stream
// is decoded once and the output allocated once at its exact size, so 64
// repeats of a 1 MiB stream allocate about the 64 MiB output plus one decode,
// not 64 decodes.
func TestPageContent_RepeatedStreamDecodedOnce(t *testing.T) {
	stream := flateStreamObj(bytes.Repeat([]byte(" "), 1<<20))
	r, pi := contentsPage(t, "["+strings.Repeat("4 0 R ", 64)+"]", stream)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := r.PageContent(pi)
	runtime.ReadMemStats(&after)
	if err != nil || len(data) != 64*(1<<20+1) {
		t.Fatalf("PageContent: %d bytes, err %v; want %d bytes", len(data), err, 64*(1<<20+1))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 80<<20 {
		t.Errorf("PageContent allocated %d MB, want ≤ 80 MB (the 64 MB output plus one decode)", alloc>>20)
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
		{"inline image", "q BI /W 3 /H 2 /BPC 8 /CS /G ID \x00(\xffE\x80\x10 EI Q (Text) Tj", false},
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
		{"Tf", []Object{name, name}, false},
		{"Tf", []Object{str, num}, false},
		{"BDC", []Object{name, Dict{}}, true},
		{"BDC", []Object{name, name}, true},
		{"BDC", []Object{name, str}, false},
		{"BDC", []Object{str, Dict{}}, false},
		{"DP", []Object{name, Dict{}}, true},
		{"DP", []Object{name, num}, false},
		{"Do", []Object{name}, true},
		{"Do", []Object{str}, false},
		{"gs", []Object{num}, false},
		{"gs", []Object{name}, true},
		{"sh", []Object{str}, false},
		{"ri", []Object{str}, false},
		{"cs", []Object{arr}, false},
		{"CS", []Object{name}, true},
		{"MP", []Object{str}, false},
		{"BMC", []Object{name}, true},
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

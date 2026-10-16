package parse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

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

// objStmPDF builds a PDF 1.5 file whose page (object 3) lives in an object
// stream (object 4) with the given /N and /First values, indexed by an xref
// stream (object 5) declaring /W w. The ObjStm header and body are well formed
// for N=1; hostile values only change the dictionary. Offsets are computed.
func objStmPDF(n, first, w string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")
	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 600] >>")

	header := "3 0 "
	objstm := []byte(header + "<< /Type /Page /Parent 2 0 R >>")
	if first == "" {
		first = fmt.Sprint(len(header))
	}
	osDict := fmt.Sprintf("<< /Type /ObjStm /N %s /First %s /Length %d >>", n, first, len(objstm))
	off[4] = writeStreamObj(&b, 4, osDict, objstm)

	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},
		{1, int64(off[1]), 0},
		{1, int64(off[2]), 0},
		{2, 4, 0}, // obj 3: compressed in ObjStm 4 at index 0
		{1, int64(off[4]), 0},
		{1, int64(xoff), 0},
	}
	raw := packXref(rows, [3]int{1, 4, 2})
	dict := fmt.Sprintf("<< /Type /XRef /Size 6 /Root 1 0 R /W %s /Length %d >>", w, len(raw))
	writeStreamObj(&b, 5, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))
	return b.Bytes()
}

// TestLoadObjStm_HostileHeaderCounts pins that /N and /First are bounded by the
// decoded stream before they size anything (SECURITY.md §2). /N 2^60 used to
// reach make([]objStmEntry, 0, n) and panic with "makeslice: cap out of range"
// from Pages() during OpenBytes, crashing a native server on open.
func TestLoadObjStm_HostileHeaderCounts(t *testing.T) {
	tests := []struct {
		name     string
		n, first string
		wantErr  bool
	}{
		{"valid control", "1", "", false},
		{"N 2^60", "1152921504606846976", "", true},
		{"N MaxInt64", "9223372036854775807", "", true},
		{"N 2^31", "2147483648", "", true},
		{"N exceeds header pairs", "2", "", true},
		{"N zero", "0", "", true}, // no entries: index 0 is out of range
		{"N negative", "-1", "", true},
		{"First MaxInt64", "1", "9223372036854775807", true},
		{"First negative", "1", "-4", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewResolver(objStmPDF(tc.n, tc.first, "[1 4 2]"))
			if err != nil {
				t.Fatalf("NewResolver: %v", err)
			}
			_, err = r.Resolve(Reference{Number: 3})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Resolve(3) err = %v, wantErr %v", err, tc.wantErr)
			}
			if _, err := r.Pages(); err != nil && !tc.wantErr {
				t.Fatalf("Pages: %v", err)
			}
		})
	}
}

// TestParseXrefStream_HostileWidths pins that /W widths are bounded before they
// are summed: three widths of 2^62 wrapped the row length negative, slipped
// past the truncation check, and sliced decoded[0:2^62] (a panic).
func TestParseXrefStream_HostileWidths(t *testing.T) {
	for _, w := range []string{
		"[4611686018427387904 4611686018427387904 4611686018427387904]",
		"[9223372036854775807 1 0]",
		"[1 9223372036854775807 1]",
		"[1 4 9223372036854775807]",
	} {
		t.Run(w, func(t *testing.T) {
			if _, err := NewResolver(objStmPDF("1", "", w)); err == nil {
				t.Errorf("NewResolver with /W %s = nil error, want an error", w)
			}
		})
	}
}

// TestMaybeStream_HostileLength pins that a direct /Length past the end of the
// input falls back to the endstream scan. /Length MaxInt64 wrapped start+length
// negative, passed the "<= len(data)" check, and indexed data[-n] (a panic).
func TestMaybeStream_HostileLength(t *testing.T) {
	for _, length := range []string{"9223372036854775807", "9223372036854775000", "4611686018427387904"} {
		t.Run(length, func(t *testing.T) {
			data := []byte("1 0 obj\n<< /Length " + length + " >>\nstream\nabc\nendstream\nendobj\n")
			_, _, obj, _, err := ParseIndirectObjectAt(data, 0)
			if err != nil {
				t.Fatalf("ParseIndirectObjectAt: %v", err)
			}
			st, ok := obj.(*Stream)
			if !ok {
				t.Fatalf("object = %T, want *Stream", obj)
			}
			if string(st.Raw) != "abc" {
				t.Errorf("Raw = %q, want %q (endstream fallback)", st.Raw, "abc")
			}
		})
	}
}

// TestNewDecryptor_HostileKeyLength pins that /Length is bounded to the 40–128
// bits R2–R4 allow (ISO 32000-1 §7.6.3.2, Table 20) before it sizes the key:
// /Length 256 sliced the 16-byte MD5 sum to [:32] and panicked during
// OpenBytes.
func TestNewDecryptor_HostileKeyLength(t *testing.T) {
	tests := []struct {
		name   string
		enc    Dict
		wantOK bool
	}{
		{"RC4 40-bit control", encDict(1, 2, 40, ""), true},
		{"RC4 128-bit control", encDict(2, 3, 128, ""), true},
		{"R2 /Length 256 (ignored: 5-byte key)", encDict(1, 2, 256, ""), true},
		{"V2 /Length 256", encDict(2, 3, 256, ""), false},
		{"V2 /Length 2^62", encDict(2, 3, 1<<62, ""), false},
		{"V2 /Length 8", encDict(2, 3, 8, ""), false},
		{"V4 StdCF /Length 32 bytes", encDict(4, 4, 256, "V2"), false},
		{"V4 StdCF /Length 2^62", encDict(4, 4, 1<<62, "V2"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newDecryptor(tc.enc, []byte("id"), nil)
			if (err == nil) != tc.wantOK {
				t.Errorf("newDecryptor err = %v, wantOK %v", err, tc.wantOK)
			}
		})
	}
}

// TestNewDecryptor_R2KeyIsFiveBytes pins Algorithm 2 step (h): an R2 file key
// is always 5 bytes, whatever /Length says. It builds an RC4 R2 document whose
// content stream is encrypted with the per-object key derived from that 5-byte
// key (computed independently here) and checks it decrypts for /Length 40, 128
// and 256 — a 16-byte key from /Length 128 would garble it, and /Length 256 used
// to panic.
func TestNewDecryptor_R2KeyIsFiveBytes(t *testing.T) {
	id := []byte("r2-file-id")
	plain := "BT /F1 12 Tf (R2-OK) Tj ET"
	for _, length := range []int64{40, 128, 256} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			enc := encDict(1, 2, length, "")
			o := stringValue(enc["O"])
			fileKey := deriveKey(nil, o, -44, id, 2, 5, true)
			ct := rc4Enc((&decryptor{key: fileKey}).objectKey(4, 0), []byte(plain))

			pdf := buildPDF(map[int]string{
				1: "<< /Type /Catalog /Pages 2 0 R >>",
				2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
				3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
				4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(ct), ct),
				5: fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /Length %d /P -44 /O <%X> /U <%X> >>",
					length, o, stringValue(enc["U"])),
			}, fmt.Sprintf("<< /Size 6 /Root 1 0 R /Encrypt 5 0 R /ID [<%X> <%X>] >>", id, id))

			r, err := NewResolver(pdf)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.EnableDecryption(nil); err != nil {
				t.Fatalf("EnableDecryption: %v", err)
			}
			if len(r.decrypt.key) != 5 {
				t.Errorf("file key = %d bytes, want 5", len(r.decrypt.key))
			}
			obj, err := r.Resolve(Reference{Number: 4})
			if err != nil {
				t.Fatal(err)
			}
			if st, ok := obj.(*Stream); !ok || string(st.Raw) != plain {
				t.Errorf("decrypted content = %q, want %q", obj, plain)
			}
		})
	}
}

// nested returns depth copies of open, a leaf, then depth copies of close.
func nested(open, leaf, close string, depth int) string {
	return strings.Repeat(open, depth) + leaf + strings.Repeat(close, depth)
}

// TestParse_NestingDepthLimit pins the recursion bound on arrays and
// dictionaries (SECURITY.md §2). The parser recursed once per level with no
// limit, so 3,000,000 nested '[' (3 MB) overflowed the goroutine stack — a
// fatal error that no recover catches — from OpenBytes. maxNestingDepth levels
// still parse; one more is a lexer error, and so is a 100,000-deep value (deep
// enough to prove the bound, shallow enough not to overflow an unbounded
// parser, so a regression fails this test instead of killing the binary).
func TestParse_NestingDepthLimit(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"arrays at the limit", nested("[", "1", "]", maxNestingDepth), false},
		{"arrays one past the limit", nested("[", "1", "]", maxNestingDepth+1), true},
		{"dicts at the limit", nested("<< /A ", "1", " >>", maxNestingDepth), false},
		{"dicts one past the limit", nested("<< /A ", "1", " >>", maxNestingDepth+1), true},
		{"mixed one past the limit", nested("[ << /A ", "1", " >> ]", maxNestingDepth/2+1), true},
		{"100k arrays, unterminated", strings.Repeat("[", 100_000), true},
		{"100k dicts, unterminated", strings.Repeat("<< /A ", 100_000), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseObject([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseObject err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "nested deeper than") {
				t.Errorf("err = %v, want the nesting-depth error", err)
			}
		})
	}
}

// innermost follows the first element of each nested array (or /A of each
// nested dictionary) down to the leaf, iteratively.
func innermost(o Object) Object {
	for {
		switch v := o.(type) {
		case Array:
			if len(v) == 0 {
				return v
			}
			o = v[0]
		case Dict:
			o = v["A"]
		default:
			return o
		}
	}
}

// TestScanContent_DeepNestingIsBounded drives the content-stream scanner, which
// calls parseArray/parseDictOrStream directly (not through parseValue). Past
// maxNestingDepth the scanner is lenient without recursing: the over-deep part
// is skipped as one Null placeholder, so the enclosing operand — and the other
// operands next to it, e.g. the strings of a TJ array that redaction must see —
// survive. An unterminated over-deep subtree is dropped.
func TestScanContent_DeepNestingIsBounded(t *testing.T) {
	for _, deep := range []string{
		nested("[", "1", "]", 100_000),
		nested("<< /A ", "1", " >>", 100_000),
		nested("[ << /A ", "1", " >> ]", 50_000),
	} {
		ops := ScanContent([]byte(deep + " 1 0 0 1 5 7 cm"))
		if len(ops) != 1 || ops[0].Op != "cm" || len(ops[0].Operands) != 7 {
			t.Fatalf("ops = %d %.200v, want one cm with the deep operand + 6 numbers", len(ops), ops)
		}
		if _, ok := innermost(ops[0].Operands[0]).(Null); !ok {
			t.Errorf("innermost of the deep operand = %v, want the Null placeholder", innermost(ops[0].Operands[0]))
		}
	}

	for _, deep := range []string{strings.Repeat("[", 100_000), strings.Repeat("<< /A ", 100_000)} {
		// The skip stops at the cm keyword (an operator never belongs inside an
		// operand), so cm is recovered without the operands the skip consumed.
		ops := ScanContent([]byte("(keep) Tj " + deep + " 1 0 0 1 5 7 cm"))
		if len(ops) != 2 || ops[0].Op != "Tj" || ops[1].Op != "cm" || len(ops[1].Operands) != 0 {
			t.Errorf("unterminated: ops = %.200v, want Tj then a bare cm", ops)
		}
	}

	// At the boundary: a maxNestingDepth-deep operand parses whole; one level
	// more keeps the operand with a Null where the over-deep level was.
	for _, tc := range []struct {
		depth int
		leaf  Object
	}{
		{maxNestingDepth, Integer(1)},
		{maxNestingDepth + 1, Null{}},
	} {
		ops := ScanContent([]byte(nested("[", "1", "]", tc.depth) + " 1 0 0 1 5 7 cm"))
		if len(ops) != 1 || ops[0].Op != "cm" || len(ops[0].Operands) != 7 {
			t.Fatalf("depth %d: ops = %.200v, want one cm with 7 operands", tc.depth, ops)
		}
		if got := innermost(ops[0].Operands[0]); got != tc.leaf {
			t.Errorf("depth %d: innermost = %v, want %v", tc.depth, got, tc.leaf)
		}
	}
}

// TestSkipOverDeep_AcceptsOnlyWhatParseValueAccepts: the lenient skip of an
// over-deep subtree must not swallow anything the bounded parser would reject —
// an operator (an interpreter that runs operators inside arrays would draw a
// Tj there), an inline image, a stream keyword, a procedure brace, or a closer
// of the wrong kind — or brackets would be counted inside inline-image data.
func TestSkipOverDeep_AcceptsOnlyWhatParseValueAccepts(t *testing.T) {
	deep := maxNestingDepth + 44
	tests := []struct {
		name    string
		in      string // parsed after an already-consumed '['
		wantErr bool
	}{
		{"numbers, strings, names, booleans, null, refs",
			nested("[", "1 2.5 (s]) <5d> /N true false null 3 0 R << /K [] >>", "]", deep) + "]", false},
		{"brackets in strings and comments", nested("[", "(]]>>) % ]]\n", "]", deep) + "]", false},
		{"operator inside", nested("[", "(SECRET) Tj", "]", deep) + "]", true},
		{"inline image inside", nested("[", "BI /W 1 ID\n( EI", "]", deep) + "]", true},
		{"stream keyword inside", nested("[", "<< >> stream\nx\nendstream", "]", deep) + "]", true},
		{"procedure brace inside", nested("[", "{ 1 }", "]", deep) + "]", true},
		{"array closed by '>>'", strings.Repeat("[", deep) + "1" + strings.Repeat("]", deep-1) + ">>]", true},
		{"dictionary closed by ']'", strings.Repeat("<< /A ", deep) + "1" + strings.Repeat(" >>", deep-1) + "]]", true},
		{"unterminated", strings.Repeat("[", deep), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &iparser{data: []byte(tc.in), lenient: true}
			obj, err := p.parseArray()
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseArray = (%.80v, %v), wantErr %v", obj, err, tc.wantErr)
			}
			if err == nil {
				if _, ok := innermost(obj).(Null); !ok {
					t.Errorf("innermost = %v, want the Null placeholder", innermost(obj))
				}
				if p.pos != len(tc.in) {
					t.Errorf("pos = %d, want %d (just past the closing ']')", p.pos, len(tc.in))
				}
			}
		})
	}
}

// timingScale stretches wall-clock bounds under -race and on the WASM target
// (race_test.go, wasm_test.go).
var timingScale time.Duration = 1

// TestScanContent_HugeDeepOperandIsLinear: the over-deep skip is iterative, so
// the 3,000,000-level operand that used to overflow the stack scans in linear
// time with constant stack. The 2s bound (scaled under -race) is several times
// the measured time; it catches recursion or a quadratic rescan.
func TestScanContent_HugeDeepOperandIsLinear(t *testing.T) {
	const depth = 3_000_000
	for _, tc := range []struct{ name, open, close string }{
		{"arrays", "[", "]"},
		{"dicts", "<< /A ", " >>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte("[ (keep) " + nested(tc.open, "1", tc.close, depth) + " ] TJ")
			begin := time.Now()
			spans := ScanContentSpans(in)
			if elapsed, limit := time.Since(begin), 2*time.Second*timingScale; elapsed > limit {
				t.Errorf("scan of a %d-deep operand took %v, want < %v", depth, elapsed, limit)
			}
			if len(spans) != 1 || spans[0].Op != "TJ" {
				t.Fatalf("spans = %d, want one TJ", len(spans))
			}
			arr, ok := spans[0].Operands[0].(Array)
			if !ok || len(arr) != 2 {
				t.Fatalf("TJ operand = %T len %d, want a 2-element array", spans[0].Operands[0], len(arr))
			}
			if s, ok := arr[0].(String); !ok || string(s.Value) != "keep" {
				t.Errorf("TJ string = %v, want (keep)", arr[0])
			}
		})
	}
}

// TestParse_ObjectModeStaysStrictOnDeepNesting: only the content scanner is
// lenient; an over-deep value in an indirect object is still an error.
func TestParse_ObjectModeStaysStrictOnDeepNesting(t *testing.T) {
	in := []byte("1 0 obj [ (x) " + nested("[", "1", "]", maxNestingDepth+10) + " ] endobj")
	if _, _, _, next, err := ParseIndirectObjectAt(in, 0); err == nil || next != 0 {
		t.Errorf("ParseIndirectObjectAt err = %v next = %d, want the nesting-depth error at offset 0", err, next)
	}
}

// TestResolver_CachesFailures pins the failure caches: a reference to a broken
// object (or into a broken ObjStm) does its work once and then returns the same
// error value, so errors.Is behaves as before. Without them every Do of such an
// image re-inflated its ObjStm or re-parsed its stream outside the render
// budget.
func TestResolver_CachesFailures(t *testing.T) {
	t.Run("ObjStm with /First past the end", func(t *testing.T) {
		r, err := NewResolver(objStmPDF("1", "999999999999", "[1 4 2]"))
		if err != nil {
			t.Fatal(err)
		}
		_, err1 := r.Resolve(Reference{Number: 3})
		loads := r.loads
		_, err2 := r.Resolve(Reference{Number: 3})
		if err1 == nil || err2 != err1 || !errors.Is(err2, errLex) {
			t.Fatalf("errors = %v, %v; want the same errLex value twice", err1, err2)
		}
		// Another object in the same broken ObjStm reuses the cached ObjStm
		// failure: no second decode.
		r.xref.Entries[6] = XrefEntry{InObjStm: true, StreamNum: 4, StreamIdx: 1}
		if _, err := r.Resolve(Reference{Number: 6}); !errors.Is(err, errLex) {
			t.Fatalf("Resolve(6) err = %v, want errLex", err)
		}
		if r.loads != loads {
			t.Errorf("loads went %d -> %d: the failed ObjStm was decoded again", loads, r.loads)
		}
	})
	t.Run("object that fails to parse", func(t *testing.T) {
		pdf := buildPDF(map[int]string{
			1: "<< /Type /Catalog /Pages 2 0 R >>",
			2: "<< /Type /Pages /Kids [] /Count 0 >>",
			3: "<< /Broken [ 1 2 ",
		}, "<< /Size 4 /Root 1 0 R >>")
		r, err := NewResolver(pdf)
		if err != nil {
			t.Fatal(err)
		}
		_, err1 := r.Resolve(Reference{Number: 3})
		loads := r.loads
		_, err2 := r.Resolve(Reference{Number: 3})
		if err1 == nil || err2 != err1 {
			t.Fatalf("errors = %v, %v; want the same error value twice", err1, err2)
		}
		if r.loads != loads {
			t.Errorf("loads went %d -> %d: the failed object was parsed again", loads, r.loads)
		}
	})
	t.Run("success is still cached", func(t *testing.T) {
		r, err := NewResolver(objStmPDF("1", "", "[1 4 2]"))
		if err != nil {
			t.Fatal(err)
		}
		o1, err1 := r.Resolve(Reference{Number: 3})
		loads := r.loads
		o2, err2 := r.Resolve(Reference{Number: 3})
		if err1 != nil || err2 != nil || r.loads != loads {
			t.Fatalf("errs %v, %v, loads %d -> %d; want success from the cache", err1, err2, loads, r.loads)
		}
		if _, ok := o2.(Dict); !ok || o1 == nil {
			t.Fatalf("object = %T, want the page dictionary", o2)
		}
	})
}

// objStmsPDF builds a PDF 1.5 file with k Flate object streams (objects
// 3..k+2). ObjStm i holds two dictionaries, objects objStmObj(k, i, 0) and
// objStmObj(k, i, 1), and is padded with pad spaces. With 4 ≤ k ≤ 30 every
// object number has two digits, so every ObjStm decodes to the same size.
func objStmsPDF(k, pad int) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")
	off[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [] /Count 0 >>\nendobj\n")
	for i := 0; i < k; i++ {
		header := fmt.Sprintf("%d 0 %d 10 ", objStmObj(k, i, 0), objStmObj(k, i, 1))
		z := zlibBytes([]byte(header + "<< /I 1 >><< /I 2 >>" + strings.Repeat(" ", pad)))
		dict := fmt.Sprintf("<< /Type /ObjStm /N 2 /First %d /Filter /FlateDecode /Length %d >>", len(header), len(z))
		off[3+i] = writeStreamObj(&b, 3+i, dict, z)
	}
	xrefNum := 3*k + 3
	xoff := b.Len()
	rows := [][3]int64{{0, 0, 65535}}
	for num := 1; num <= k+2; num++ {
		rows = append(rows, [3]int64{1, int64(off[num]), 0})
	}
	for i := 0; i < k; i++ {
		rows = append(rows, [3]int64{2, int64(3 + i), 0}, [3]int64{2, int64(3 + i), 1})
	}
	rows = append(rows, [3]int64{1, int64(xoff), 0})
	raw := packXref(rows, [3]int{1, 4, 2})
	dict := fmt.Sprintf("<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 2] /Length %d >>", xrefNum+1, len(raw))
	writeStreamObj(&b, xrefNum, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))
	return b.Bytes()
}

// objStmObj is the object number of entry j (0 or 1) of ObjStm i in objStmsPDF.
func objStmObj(k, i, j int) int { return k + 3 + 2*i + j }

// TestResolver_ObjStmCacheIsBounded pins the ObjStm cache bound (R4,
// render-budget.md §9): the decoded ObjStms a Resolver keeps stay within its
// cap, least recently used evicted first; an evicted ObjStm is decoded again
// on demand within the re-decode budget, and once that is spent loading from
// it fails with a cached errLex. Without the bound every decoded ObjStm stayed
// cached for the document's lifetime.
func TestResolver_ObjStmCacheIsBounded(t *testing.T) {
	const k = 10
	pdf := objStmsPDF(k, 64<<10)
	newR := func(t *testing.T) (*Resolver, int64) {
		t.Helper()
		r, err := NewResolver(pdf)
		if err != nil {
			t.Fatal(err)
		}
		if r.objstmCap != maxObjStmCache || r.redecodeCap != maxObjStmRedecode {
			t.Fatalf("caps = %d, %d; want the defaults", r.objstmCap, r.redecodeCap)
		}
		// Measure one entry's size with the default caps.
		probe, _ := NewResolver(pdf)
		if _, err := probe.Resolve(Reference{Number: objStmObj(k, 0, 0)}); err != nil {
			t.Fatal(err)
		}
		return r, probe.objstmBytes
	}
	load := func(t *testing.T, r *Resolver, i, j int) error {
		t.Helper()
		o, err := r.Resolve(Reference{Number: objStmObj(k, i, j)})
		if err == nil {
			if d, ok := o.(Dict); !ok || d["I"] != Integer(j+1) {
				t.Fatalf("object %d = %v, want << /I %d >>", objStmObj(k, i, j), o, j+1)
			}
		}
		return err
	}

	t.Run("cache stays within its cap", func(t *testing.T) {
		r, size := newR(t)
		r.objstmCap = 2*size + size/2 // room for two
		for i := 0; i < k; i++ {
			if err := load(t, r, i, 0); err != nil {
				t.Fatal(err)
			}
			if r.objstmBytes > r.objstmCap {
				t.Fatalf("after ObjStm %d: cached %d bytes, cap %d", i, r.objstmBytes, r.objstmCap)
			}
		}
		if len(r.objstms) != 2 || r.objstmLRU.Len() != 2 || r.objstmBytes != 2*size {
			t.Errorf("cached %d ObjStms (%d listed, %d bytes), want 2 (%d bytes)", len(r.objstms), r.objstmLRU.Len(), r.objstmBytes, 2*size)
		}
	})

	t.Run("least recently used is evicted", func(t *testing.T) {
		r, size := newR(t)
		r.objstmCap = 2*size + size/2
		for _, step := range []struct{ i, j, wantLoads int }{
			{0, 0, 1}, // decode 0
			{1, 0, 2}, // decode 1
			{0, 1, 2}, // hit: 0 becomes the most recent
			{2, 0, 3}, // decode 2, evicting 1
			{0, 0, 3}, // already parsed: no ObjStm access
			{1, 1, 4}, // 1 was evicted: decoded again, evicting 0 (least recent)
		} {
			if err := load(t, r, step.i, step.j); err != nil {
				t.Fatal(err)
			}
			if r.loads != step.wantLoads {
				t.Fatalf("after object (%d,%d): %d ObjStm decodes, want %d", step.i, step.j, r.loads, step.wantLoads)
			}
		}
		if _, ok := r.objstms[3+0]; ok {
			t.Error("ObjStm 0 still cached, want it evicted as least recently used")
		}
		if r.redecoded <= 0 {
			t.Errorf("redecoded = %d, want the re-decode of ObjStm 1 charged", r.redecoded)
		}
	})

	t.Run("re-decode budget", func(t *testing.T) {
		r, size := newR(t)
		r.objstmCap = size + size/2 // room for one
		decoded := size - 2*objStmEntrySize
		r.redecodeCap = decoded + decoded/2 // one re-decode
		// Decode 0, decode 1 (evicting 0), then re-decode 0.
		for _, ij := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
			if err := load(t, r, ij[0], ij[1]); err != nil {
				t.Fatalf("object (%d,%d): %v", ij[0], ij[1], err)
			}
		}
		if r.redecoded != decoded {
			t.Fatalf("redecoded = %d, want %d", r.redecoded, decoded)
		}
		err1 := load(t, r, 1, 1) // 1 was evicted; the budget has half a decode left
		loads := r.loads
		err2 := load(t, r, 1, 1)
		if !errors.Is(err1, errLex) || err2 != err1 || !strings.Contains(err1.Error(), "re-decode budget") {
			t.Fatalf("errors = %v, %v; want the same errLex value naming the re-decode budget", err1, err2)
		}
		if r.loads != loads {
			t.Errorf("loads went %d -> %d: the failure was not cached", loads, r.loads)
		}
		if r.redecoded > r.redecodeCap+1 {
			t.Errorf("redecoded = %d, want the failed decode stopped at the budget %d", r.redecoded, r.redecodeCap)
		}
		// A first decode is not charged to the re-decode budget.
		if err := load(t, r, 5, 0); err != nil {
			t.Errorf("first decode of ObjStm 5 after the budget is spent: %v", err)
		}
	})

	t.Run("ObjStm larger than the cap is not kept", func(t *testing.T) {
		r, size := newR(t)
		r.objstmCap = size - 1
		if err := load(t, r, 0, 0); err != nil {
			t.Fatal(err)
		}
		if r.objstmBytes != 0 || len(r.objstms) != 0 || !r.evicted[3] {
			t.Fatalf("cached %d bytes in %d ObjStms, want none (evicted at once)", r.objstmBytes, len(r.objstms))
		}
		if err := load(t, r, 0, 1); err != nil || r.loads != 2 || r.redecoded == 0 {
			t.Errorf("second object: err %v, %d decodes, redecoded %d; want a charged re-decode", err, r.loads, r.redecoded)
		}
	})
}

// TestResolver_DecryptionClearsFailureCaches: an encrypted ObjStm resolved
// before EnableDecryption fails (its header is ciphertext). That failure must
// not outlive the change of decryption state: after EnableDecryption the same
// object resolves. It is the one way a failure is not permanent.
func TestResolver_DecryptionClearsFailureCaches(t *testing.T) {
	enc := encDict(2, 3, 128, "")
	d, err := newDecryptor(enc, []byte("id"), nil)
	if err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")
	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 600] >>")
	header := "3 0 "
	plain := []byte(header + "<< /Type /Page /Parent 2 0 R >>")
	cipherText := rc4Enc(d.objectKey(4, 0), plain)
	off[4] = writeStreamObj(&b, 4, fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Length %d >>", len(header), len(cipherText)), cipherText)
	xoff := b.Len()
	raw := packXref([][3]int64{
		{0, 0, 65535},
		{1, int64(off[1]), 0},
		{1, int64(off[2]), 0},
		{2, 4, 0},
		{1, int64(off[4]), 0},
		{1, int64(xoff), 0},
	}, [3]int{1, 4, 2})
	encStr := fmt.Sprintf("<< /Filter /Standard /V 2 /R 3 /Length 128 /O <%x> /U <%x> /P -44 >>",
		bytes.Repeat([]byte{0xAB}, 32), bytes.Repeat([]byte{0xCD}, 32))
	writeStreamObj(&b, 5, fmt.Sprintf("<< /Type /XRef /Size 6 /Root 1 0 R /Encrypt %s /ID [<6964> <6964>] /W [1 4 2] /Length %d >>", encStr, len(raw)), raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(Reference{Number: 3}); err == nil {
		t.Fatal("encrypted ObjStm resolved before decryption, want an error")
	}
	if err := r.EnableDecryption(nil); err != nil {
		t.Fatalf("EnableDecryption: %v", err)
	}
	o, err := r.Resolve(Reference{Number: 3})
	if err != nil {
		t.Fatalf("after EnableDecryption: %v (a stale cached failure?)", err)
	}
	if dct, ok := o.(Dict); !ok || dct["Type"] != Name("Page") {
		t.Fatalf("object = %v, want the page dictionary", o)
	}
}

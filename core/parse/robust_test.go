package parse

import (
	"bytes"
	"fmt"
	"testing"
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

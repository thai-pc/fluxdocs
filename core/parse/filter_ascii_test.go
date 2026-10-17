package parse

import (
	"bytes"
	"compress/lzw"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// lzwEncode is a minimal LZWDecode encoder for tests (§7.4.4, EarlyChange 1):
// a clear code first, 9–12-bit codes, EOD last.
func lzwEncode(data []byte) []byte {
	var out []byte
	var acc uint64
	nbits := 0
	width := 9
	put := func(code int) {
		acc = acc<<uint(width) | uint64(code)
		nbits += width
		for nbits >= 8 {
			out = append(out, byte(acc>>uint(nbits-8)))
			nbits -= 8
		}
	}
	dict := map[string]int{}
	reset := func() {
		dict = map[string]int{}
		for i := 0; i < 256; i++ {
			dict[string([]byte{byte(i)})] = i
		}
	}
	reset()
	next := 258
	put(256)
	w := ""
	for _, c := range data {
		wc := w + string([]byte{c})
		if _, ok := dict[wc]; ok {
			w = wc
			continue
		}
		put(dict[w])
		dict[wc] = next
		next++
		// The decoder's table lags one entry behind (it adds an entry when
		// it reads the next code), so with EarlyChange 1 the width grows when
		// next reaches 512, 1024, 2048.
		switch {
		case next >= 4094:
			put(256)
			reset()
			next = 258
			width = 9
		case next >= 2048:
			width = 12
		case next >= 1024:
			width = 11
		case next >= 512:
			width = 10
		}
		w = string([]byte{c})
	}
	if w != "" {
		put(dict[w])
	}
	put(257)
	if nbits > 0 {
		out = append(out, byte(acc<<uint(8-nbits)))
	}
	return out
}

// runLengthEncode encodes data as literal runs only (§7.4.5).
func runLengthEncode(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := min(len(data), 128)
		out = append(out, byte(n-1))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return append(out, 128)
}

func TestDecodeStream_TextFilters(t *testing.T) {
	text := []byte("BT /F1 24 Tf 72 700 Td (SECRET-1234) Tj ET\n")
	long := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog "), 400)
	cases := []struct {
		name   string
		filter Object
		raw    []byte
		want   []byte
	}{
		{"AHx", Name("ASCIIHexDecode"), []byte("42 54 2f\n463124>"), []byte("BT/F1$")},
		{"AHx odd digit", Name("AHx"), []byte("414>"), []byte("A@")},
		{"AHx no EOD", Name("AHx"), []byte("4142"), []byte("AB")},
		{"A85", Name("ASCII85Decode"), []byte("87cURD]i,\"Ebo80~>"), []byte("Hello World!")},
		{"A85 z and partial", Name("A85"), []byte("z!!*~>"), []byte{0, 0, 0, 0, 0, 1}},
		{"RL", Name("RunLengthDecode"), []byte{2, 'a', 'b', 'c', 254, 'x', 128}, []byte("abcxxx")},
		{"RL long", Name("RL"), runLengthEncode(long), long},
		{"LZW", Name("LZWDecode"), lzwEncode(text), text},
		{"LZW long", Name("LZW"), lzwEncode(long), long},
		{"AHx then RL", Array{Name("AHx"), Name("RL")}, []byte("02616263FE7880>"), []byte("abcxxx")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeStream(&Stream{Dict: Dict{"Filter": tc.filter}, Raw: tc.raw})
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Errorf("DecodeStream = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDecodeStream_TextFilterErrorsAndLimits(t *testing.T) {
	bad := []struct {
		name   string
		filter Name
		raw    []byte
	}{
		{"AHx bad byte", "AHx", []byte("41G2>")},
		{"A85 bad byte", "A85", []byte("87cU{~>")},
		{"A85 one-character group", "A85", []byte("87cURD]i,\"Ebo80!~>")},
		{"A85 z inside a group", "A85", []byte("8z~>")},
		{"RL truncated literal", "RL", []byte{5, 'a'}},
		{"RL truncated repeat", "RL", []byte{200}},
		{"LZW code past the table", "LZW", []byte{0x80, 0x7F, 0xF0}},
	}
	for _, tc := range bad {
		if _, err := DecodeStream(&Stream{Dict: Dict{"Filter": tc.filter}, Raw: tc.raw}); err == nil {
			t.Errorf("%s: DecodeStream succeeded, want an error", tc.name)
		}
	}
	// A run-length bomb stops at the caller's limit: 2 input bytes per 129
	// output bytes.
	bomb := bytes.Repeat([]byte{129, 'x'}, 1<<16)
	_, produced, err := DecodeStreamLimit(&Stream{Dict: Dict{"Filter": Name("RL")}, Raw: append(bomb, 128)}, 1000)
	if !errors.Is(err, ErrDecodeLimit) || produced > 1001 {
		t.Errorf("RL bomb: produced %d, err %v; want ErrDecodeLimit within the limit", produced, err)
	}
	lzwBomb := lzwEncode(bytes.Repeat([]byte{'a'}, 1<<20))
	_, produced, err = DecodeStreamLimit(&Stream{Dict: Dict{"Filter": Name("LZW")}, Raw: lzwBomb}, 4096)
	if !errors.Is(err, ErrDecodeLimit) || produced > 4097 {
		t.Errorf("LZW bomb: produced %d, err %v; want ErrDecodeLimit within the limit", produced, err)
	}
}

// TestDecodeStream_LZWEarlyChange0 checks the decoder against an independent
// encoder: Go's compress/lzw (MSB order, 8-bit literals) writes the variant
// without early change, PDF's /EarlyChange 0.
func TestDecodeStream_LZWEarlyChange0(t *testing.T) {
	data := bytes.Repeat([]byte("ISO 32000-1 LZWDecode, early change 0; "), 300)
	var b bytes.Buffer
	w := lzw.NewWriter(&b, lzw.MSB, 8)
	w.Write(data)
	w.Close()
	got, err := DecodeStream(&Stream{Dict: Dict{"Filter": Name("LZWDecode"), "DecodeParms": Dict{"EarlyChange": Integer(0)}}, Raw: b.Bytes()})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("DecodeStream = %d bytes, %v; want the %d input bytes", len(got), err, len(data))
	}
}

// TestIndirectLengthKeepsFinalEOLByte: a stream whose /Length is an indirect
// integer and whose data ends in CR is cut at that length, not by the
// endstream scan, which would take the data's final CR for the end-of-line
// before endstream (Ghostscript always writes indirect lengths).
func TestIndirectLengthKeepsFinalEOLByte(t *testing.T) {
	data := "AB\r"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off1 := b.Len()
	b.WriteString("1 0 obj\n<< /Length 2 0 R >>\nstream\n" + data + "\nendstream\nendobj\n")
	off2 := b.Len()
	b.WriteString("2 0 obj\n3\nendobj\n")
	off3 := b.Len()
	b.WriteString("3 0 obj\n<< /Type /Catalog >>\nendobj\n")
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 4\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n%010d 00000 n \n", off1, off2, off3)
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	obj, err := r.Resolve(Reference{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := obj.(*Stream); !ok || string(st.Raw) != data {
		t.Errorf("stream data = %q, want %q", st.Raw, data)
	}
}

// TestDirectLengthTargets: an indirect /Length is used only when it names an
// uncompressed "N G obj <integer> endobj" with the reference's number and
// generation; anything else falls back to the endstream scan, after reading a
// few tokens (never the whole target object), and the result is cached.
func TestDirectLengthTargets(t *testing.T) {
	build := func(target string, ref string) []byte {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		off1 := b.Len()
		b.WriteString("1 0 obj\n<< /Length " + ref + " >>\nstream\nAB\r\nendstream\nendobj\n")
		off2 := b.Len()
		b.WriteString("2 0 obj\n" + target + "\nendobj\n")
		off3 := b.Len()
		b.WriteString("3 0 obj\n<< /Type /Catalog >>\nendobj\n")
		x := b.Len()
		fmt.Fprintf(&b, "xref\n0 4\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \n%010d 00000 n \n", off1, off2, off3)
		fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
		return b.Bytes()
	}
	cases := []struct {
		name, target, ref, want string
	}{
		{"integer", "3", "2 0 R", "AB\r"},
		{"wrong generation", "3", "2 1 R", "AB"},
		{"array", "[3]", "2 0 R", "AB"},
		{"huge array", "[" + strings.Repeat("1 ", 100000) + "]", "2 0 R", "AB"},
		{"stream", "<< /Length 1 >>\nstream\nx\nendstream", "2 0 R", "AB"},
		{"negative", "-3", "2 0 R", "AB"},
	}
	for _, tc := range cases {
		r, err := NewResolver(build(tc.target, tc.ref))
		if err != nil {
			t.Fatal(err)
		}
		obj, err := r.Resolve(Reference{Number: 1})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if st, ok := obj.(*Stream); !ok || string(st.Raw) != tc.want {
			t.Errorf("%s: stream data = %q, want %q", tc.name, st.Raw, tc.want)
		}
		if n, ok := r.lengths[2]; !ok || (tc.name == "integer") != (n >= 0) {
			t.Errorf("%s: cached length = %d, %v", tc.name, n, ok)
		}
	}
}

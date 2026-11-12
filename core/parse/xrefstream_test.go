package parse

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"math"
	"testing"
)

// beBytes encodes v as n big-endian bytes.
func beBytes(v int64, n int) []byte {
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

// packXref packs rows of [type, field2, field3] using the given field widths.
func packXref(rows [][3]int64, w [3]int) []byte {
	var b []byte
	for _, r := range rows {
		b = append(b, beBytes(r[0], w[0])...)
		b = append(b, beBytes(r[1], w[1])...)
		b = append(b, beBytes(r[2], w[2])...)
	}
	return b
}

func zlibBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// writeStreamObj writes "n 0 obj <dict> stream\n<raw>\nendstream endobj\n" and
// returns the object's start offset. The dict must already include a correct
// /Length.
func writeStreamObj(b *bytes.Buffer, num int, dict string, raw []byte) int {
	off := b.Len()
	b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nstream\n", num, dict))
	b.Write(raw)
	b.WriteString("\nendstream\nendobj\n")
	return off
}

// TestParseXrefStream builds a PDF whose cross-reference data is an uncompressed
// xref stream (no /Filter, so DecodeStream returns the raw entries) and checks
// that the resolver reads the catalog and page tree through it.
func TestParseXrefStream(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R >>")

	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // obj 0: free
		{1, int64(off[1]), 0}, // obj 1
		{1, int64(off[2]), 0}, // obj 2
		{1, int64(off[3]), 0}, // obj 3
		{1, int64(xoff), 0},   // obj 4: the xref stream itself
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 5 /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 4, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Catalog(); err != nil {
		t.Fatalf("Catalog via xref stream: %v", err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 612 {
		t.Errorf("pages = %d, width0 = %.0f; want 1 page, 612", len(pages), mustWidth(pages))
	}
}

// TestCompressedObjectStream stores the page object inside an /ObjStm and marks
// it as a type-2 entry in the xref stream, exercising compressed-object
// resolution.
func TestCompressedObjectStream(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 600] >>")

	// ObjStm (object 4) holding object 3 (the page), uncompressed.
	header := "3 0 " // objNum=3, offset=0 (relative to /First)
	body := "<< /Type /Page /Parent 2 0 R >>"
	objstm := []byte(header + body)
	osDict := fmt.Sprintf("<< /Type /ObjStm /N 1 /First %d /Length %d >>", len(header), len(objstm))
	off[4] = writeStreamObj(&b, 4, osDict, objstm)

	// Xref stream (object 5).
	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // obj 0: free
		{1, int64(off[1]), 0}, // obj 1
		{1, int64(off[2]), 0}, // obj 2
		{2, 4, 0},             // obj 3: compressed in ObjStm 4 at index 0
		{1, int64(off[4]), 0}, // obj 4: the ObjStm
		{1, int64(xoff), 0},   // obj 5: the xref stream itself
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 6 /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 5, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	// Resolve the compressed object directly.
	obj, err := r.Resolve(Reference{Number: 3})
	if err != nil {
		t.Fatalf("resolve compressed object 3: %v", err)
	}
	d, ok := obj.(Dict)
	if !ok {
		t.Fatalf("object 3 = %T, want Dict", obj)
	}
	if typ, _ := d.GetName("Type"); typ != "Page" {
		t.Errorf("object 3 /Type = %q, want Page", typ)
	}

	// And via the page tree.
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 400 {
		t.Errorf("pages = %d, width = %.0f; want 1 page, 400", len(pages), mustWidth(pages))
	}
}

func mustWidth(pages []PageInfo) float64 {
	if len(pages) == 0 {
		return -1
	}
	return pages[0].MediaBox.Width()
}

// TestDecodeStream_FlatePredictor covers the FlateDecode + PNG predictor 12 (Up)
// path used by real-world xref streams.
func TestDecodeStream_FlatePredictor(t *testing.T) {
	// Two rows of 3 bytes each.
	rows := [][]byte{
		{10, 20, 30},
		{11, 22, 33},
	}
	rowLen := 3

	// Encode with PNG Up predictor: filter byte 2, then row[i]-prev[i].
	var encoded []byte
	prev := make([]byte, rowLen)
	for _, row := range rows {
		encoded = append(encoded, 2) // Up
		for i := 0; i < rowLen; i++ {
			encoded = append(encoded, byte(int(row[i])-int(prev[i])))
		}
		prev = row
	}

	st := &Stream{
		Dict: Dict{
			"Filter":      Name("FlateDecode"),
			"DecodeParms": Dict{"Predictor": Integer(12), "Columns": Integer(rowLen)},
		},
		Raw: zlibBytes(encoded),
	}

	got, err := DecodeStream(st)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{10, 20, 30, 11, 22, 33}
	if !bytes.Equal(got, want) {
		t.Errorf("DecodeStream = %v, want %v", got, want)
	}
}

func TestDecodeStream_UnsupportedFilter(t *testing.T) {
	st := &Stream{Dict: Dict{"Filter": Name("JPXDecode")}, Raw: []byte("x")}
	if _, err := DecodeStream(st); err == nil {
		t.Error("DecodeStream with unsupported filter = nil error, want an error")
	}
}

// TestParseXrefStream_ExplicitIndex uses /Index [0 1 1 4] (two ranges covering
// objects 0 and 1–4), exercising xrefIndex's multi-range array path instead of
// the default [0 Size].
func TestParseXrefStream_ExplicitIndex(t *testing.T) {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.5\n")

	writeObj := func(num int, body string) {
		off[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 300] >>")
	writeObj(3, "<< /Type /Page /Parent 2 0 R >>")

	w := [3]int{1, 4, 2}
	xoff := b.Len()
	rows := [][3]int64{
		{0, 0, 65535},         // range [0 1]: obj 0 free
		{1, int64(off[1]), 0}, // range [1 4]: obj 1
		{1, int64(off[2]), 0}, // obj 2
		{1, int64(off[3]), 0}, // obj 3
		{1, int64(xoff), 0},   // obj 4: the xref stream
	}
	raw := packXref(rows, w)
	dict := fmt.Sprintf("<< /Type /XRef /Size 5 /Index [0 1 1 4] /Root 1 0 R /W [1 4 2] /Length %d >>", len(raw))
	writeStreamObj(&b, 4, dict, raw)
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xoff))

	r, err := NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].MediaBox.Width() != 300 {
		t.Errorf("pages = %d, width = %.0f; want 1 page, 300", len(pages), mustWidth(pages))
	}
}

// xrefStreamChainPDF builds a file whose xref is a chain of xref streams, one
// per dict (newest first), each with /W [1 0 0] and body as its decoded rows
// (zlib-compressed). No objects are needed: the caps trip in ParseXref.
func xrefStreamChainPDF(dicts []string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	raw := zlibBytes(body)
	prev := -1
	for i := len(dicts) - 1; i >= 0; i-- {
		d := "<< /Type /XRef /W [1 0 0] " + dicts[i]
		if prev >= 0 {
			d += fmt.Sprintf(" /Prev %d", prev)
		}
		d += fmt.Sprintf(" /Filter /FlateDecode /Length %d >>", len(raw))
		prev = writeStreamObj(&b, 1+i, d, raw)
	}
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", prev))
	return b.Bytes()
}

// TestParseXref_EntryCaps pins the xref size bounds (SECURITY.md §2): /Index
// counts are charged before the stream is decoded, and a stream without
// /Index is charged the rows it really holds, up to /Size; per section
// (maxXrefEntries) and per /Prev chain (maxXrefRows), with start+count
// checked for overflow. A /W [1 0 0] stream inserted one entry per decoded
// byte, so a tiny file could ask for ~268 M entries; an oversized /Size over
// a short body is harmless and opens, as it did before the caps.
func TestParseXref_EntryCaps(t *testing.T) {
	const short = 64 // decoded rows of a short body
	idx := func(start, count int) string { return fmt.Sprintf("/Size 10 /Index [%d %d]", start, count) }
	tests := []struct {
		name      string
		dicts     []string // newest first
		body      int      // decoded rows (zero bytes) of every stream
		wantErr   bool
		wantLimit bool // the error must be errXrefLimit
	}{
		{"control /Size at the cap", []string{fmt.Sprintf("/Size %d", maxXrefEntries)}, short, false, false},
		{"/Size cap+1 over a short body", []string{fmt.Sprintf("/Size %d", maxXrefEntries+1)}, short, false, false},
		{"/Size 2^40 over a short body", []string{"/Size 1099511627776"}, short, false, false},
		{"/Size MaxInt64 over a short body", []string{"/Size 9223372036854775807"}, short, false, false},
		{"/Size cap+1 over cap+1 rows", []string{fmt.Sprintf("/Size %d", maxXrefEntries+1)}, maxXrefEntries + 1, true, true},
		{"/Size MaxInt64 over cap+1 rows", []string{"/Size 9223372036854775807"}, maxXrefEntries + 1, true, true},
		{"/Size negative", []string{"/Size -1"}, short, true, false},
		{"/Index sum over the cap", []string{fmt.Sprintf("/Size 10 /Index [0 %d %d 1]", maxXrefEntries, maxXrefEntries)}, short, true, true},
		{"/Index start MaxInt64", []string{"/Size 10 /Index [9223372036854775807 1]"}, short, true, false},
		{"/Index start+count overflows int32", []string{"/Size 10 /Index [2147483647 2]"}, short, true, false},
		{"/Index negative start", []string{"/Size 10 /Index [-5 10]"}, short, true, false},
		{"/Index negative count", []string{"/Size 10 /Index [0 -1]"}, short, true, false},
		{"control chain at maxXrefRows", repeat(idx(0, maxXrefEntries), maxXrefRows/maxXrefEntries), short, false, false},
		{"chain over maxXrefRows", repeat(idx(0, maxXrefEntries), maxXrefRows/maxXrefEntries+1), short, true, true},
		// The oldest section has no /Index: it is charged its real rows, and
		// the chain leaves room for exactly short of them.
		{"control /Size section filling the chain", append(repeat(idx(0, maxXrefEntries), maxXrefRows/maxXrefEntries-1), idx(0, maxXrefEntries-short), "/Size 9223372036854775807"), short, false, false},
		{"/Size section one row over the chain", append(repeat(idx(0, maxXrefEntries), maxXrefRows/maxXrefEntries-1), idx(0, maxXrefEntries-short+1), "/Size 9223372036854775807"), short, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, err := ParseXref(xrefStreamChainPDF(tc.dicts, make([]byte, tc.body)))
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseXref err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				if len(x.Entries) != tc.body {
					t.Errorf("%d entries, want %d (one per decoded row)", len(x.Entries), tc.body)
				}
				return
			}
			if x != nil {
				t.Error("partial Xref returned with the error")
			}
			if !errors.Is(err, errLex) || errors.Is(err, errXrefLimit) != tc.wantLimit {
				t.Errorf("err = %v, want errLex (errXrefLimit %v)", err, tc.wantLimit)
			}
		})
	}
}

// TestParseXref_MergedCapDisjointSections: two full-size sections over
// disjoint object numbers each fit the per-section cap and together fit
// maxXrefRows, but the merged table would hold 2·maxXrefEntries objects; it
// fails with errXrefLimit as the first object past the cap is added.
func TestParseXref_MergedCapDisjointSections(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a table of maxXrefEntries objects")
	}
	dicts := []string{
		fmt.Sprintf("/Size %d /Index [%d %d]", 2*maxXrefEntries, maxXrefEntries, maxXrefEntries),
		fmt.Sprintf("/Size %d /Index [0 %d]", maxXrefEntries, maxXrefEntries),
	}
	x, err := ParseXref(xrefStreamChainPDF(dicts, make([]byte, maxXrefEntries)))
	if x != nil || !errors.Is(err, errXrefLimit) {
		t.Fatalf("ParseXref: table returned %v, err %v; want no table and errXrefLimit", x != nil, err)
	}
	// Control: the same two sections over one range fit.
	dicts[0] = fmt.Sprintf("/Size %d /Index [0 %d]", maxXrefEntries, maxXrefEntries)
	if x, err := ParseXref(xrefStreamChainPDF(dicts, make([]byte, maxXrefEntries))); err != nil || len(x.Entries) != maxXrefEntries {
		t.Fatalf("overlapping control: err %v", err)
	}
}

// TestParseXref_DamagedHybridDropped: a hybrid file's /XRefStm section under
// the caps that does not parse is dropped and the document opens from its
// classic table. A damaged classic section reached through /XRefStm adds
// rows before it fails; those are removed again, so nothing of a dropped
// section is used.
func TestParseXref_DamagedHybridDropped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section func(b *bytes.Buffer) // writes the damaged section
	}{
		{"xref stream, corrupt Flate body", func(b *bytes.Buffer) {
			raw := []byte("this is not deflate data")
			writeStreamObj(b, 9, fmt.Sprintf("<< /Type /XRef /Size 10 /Index [5 2] /W [1 4 2] /Filter /FlateDecode /Length %d >>", len(raw)), raw)
		}},
		{"xref stream, /W missing", func(b *bytes.Buffer) {
			writeStreamObj(b, 9, "<< /Type /XRef /Size 10 /Length 3 >>", []byte("abc"))
		}},
		{"classic table, a good row then a bad one", func(b *bytes.Buffer) {
			b.WriteString("xref\n5 2\n0000000009 00000 n \nbroken\ntrailer\n<< /Size 10 >>\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			b.WriteString("%PDF-1.5\n")
			off := make([]int, 4)
			for i, o := range []string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200] >>",
				"<< /Type /Page /Parent 2 0 R >>",
			} {
				off[i+1] = b.Len()
				fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
			}
			stm := b.Len()
			tc.section(&b)
			classic := b.Len()
			b.WriteString("xref\n0 4\n0000000000 65535 f \n")
			for _, o := range off[1:] {
				fmt.Fprintf(&b, "%010d 00000 n \n", o)
			}
			fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R /XRefStm %d >>\nstartxref\n%d\n%%%%EOF\n", stm, classic)

			x, err := ParseXref(b.Bytes())
			if err != nil {
				t.Fatalf("ParseXref: %v", err)
			}
			if len(x.Entries) != 4 {
				t.Errorf("%d entries, want the classic table's 4 (dropped section undone)", len(x.Entries))
			}
			r, err := NewResolver(b.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if pages, err := r.Pages(); err != nil || len(pages) != 1 {
				t.Fatalf("Pages = %d, %v; want 1 page", len(pages), err)
			}
		})
	}
}

// TestDecodeStreamPrefix: the prefix decode returns DecodeStream's first n
// bytes exactly — with and without a PNG or TIFF predictor, through a chain,
// for every filter that can be last, and for predictor rows wider than
// prefixSlack — and stops near n instead of decoding the whole stream.
func TestDecodeStreamPrefix(t *testing.T) {
	varied := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i*31 + i/7)
		}
		return b
	}
	png := func(rows []byte, w int) []byte { // PNG "Up" rows
		var out []byte
		prev := make([]byte, w)
		for r := 0; r+w <= len(rows); r += w {
			out = append(out, 2)
			for i := 0; i < w; i++ {
				out = append(out, rows[r+i]-prev[i])
			}
			prev = rows[r : r+w]
		}
		return out
	}
	tiff := func(rows []byte, w int) []byte {
		out := append([]byte(nil), rows...)
		for r := 0; r+w <= len(rows); r += w {
			for i := w - 1; i >= 1; i-- {
				out[r+i] = rows[r+i] - rows[r+i-1]
			}
		}
		return out
	}
	a85 := func(b []byte) []byte {
		out := make([]byte, ascii85.MaxEncodedLen(len(b)))
		return append(out[:ascii85.Encode(out, b)], "~>"...)
	}
	parms := func(predictor, columns int) Dict {
		return Dict{"Predictor": Integer(predictor), "Columns": Integer(columns)}
	}
	full := varied(600 * 7)  // 600 rows of 7 bytes
	wide := varied(9001 * 5) // 5 rows of 9001 bytes: a row wider than prefixSlack
	hex := []byte(fmt.Sprintf("%X>", full))
	for _, tc := range []struct {
		name string
		st   *Stream
		want []byte
		row  int64 // predictor row length; 0 for none
	}{
		{"no filter", &Stream{Dict: Dict{}, Raw: full}, full, 0},
		{"Flate", &Stream{Dict: Dict{"Filter": Name("FlateDecode")}, Raw: zlibBytes(full)}, full, 0},
		{"Flate PNG predictor", &Stream{Dict: Dict{"Filter": Name("FlateDecode"), "DecodeParms": parms(12, 7)}, Raw: zlibBytes(png(full, 7))}, full, 7},
		{"Flate TIFF predictor", &Stream{Dict: Dict{"Filter": Name("FlateDecode"), "DecodeParms": parms(2, 7)}, Raw: zlibBytes(tiff(full, 7))}, full, 7},
		{"raw DEFLATE (zlib attempt fails)", &Stream{Dict: Dict{"Filter": Name("FlateDecode")}, Raw: zlibBytes(full)[2:]}, full, 0},
		{"LZW", &Stream{Dict: Dict{"Filter": Name("LZWDecode")}, Raw: lzwEncode(full)}, full, 0},
		{"LZW PNG predictor", &Stream{Dict: Dict{"Filter": Name("LZWDecode"), "DecodeParms": parms(12, 7)}, Raw: lzwEncode(png(full, 7))}, full, 7},
		{"RunLength", &Stream{Dict: Dict{"Filter": Name("RunLengthDecode")}, Raw: runLengthEncode(full)}, full, 0},
		{"ASCII85", &Stream{Dict: Dict{"Filter": Name("ASCII85Decode")}, Raw: a85(full)}, full, 0},
		{"ASCIIHex", &Stream{Dict: Dict{"Filter": Name("ASCIIHexDecode")}, Raw: hex}, full, 0},
		{"Flate then ASCIIHex", &Stream{Dict: Dict{"Filter": Array{Name("FlateDecode"), Name("ASCIIHexDecode")}}, Raw: zlibBytes(hex)}, full, 0},
		{"ASCIIHex then Flate PNG", &Stream{Dict: Dict{"Filter": Array{Name("ASCIIHexDecode"), Name("FlateDecode")}, "DecodeParms": Array{Null{}, parms(12, 7)}}, Raw: []byte(fmt.Sprintf("%X>", zlibBytes(png(full, 7))))}, full, 7},
		{"Flate PNG, 9001-byte rows", &Stream{Dict: Dict{"Filter": Name("FlateDecode"), "DecodeParms": parms(12, 9001)}, Raw: zlibBytes(png(wide, 9001))}, wide, 9001},
		{"LZW PNG, 9001-byte rows", &Stream{Dict: Dict{"Filter": Name("LZWDecode"), "DecodeParms": parms(12, 9001)}, Raw: lzwEncode(png(wide, 9001))}, wide, 9001},
		{"Flate, 9001-byte data", &Stream{Dict: Dict{"Filter": Name("FlateDecode")}, Raw: zlibBytes(wide)}, wide, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := DecodeStream(tc.st); err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("DecodeStream: %d bytes, %v; fixture broken", len(got), err)
			}
			size := int64(len(tc.want))
			for _, n := range []int64{0, 1, 6, 7, 8, 700, 4095, 4097, 9000, 9001, 9002, 20000, size - 1, size, 1 << 20} {
				got, produced, err := decodeStreamPrefix(tc.st, n, math.MaxInt64)
				if err != nil {
					t.Fatalf("n=%d: %v", n, err)
				}
				if int64(len(got)) < min(n, size) || !bytes.Equal(got, tc.want[:len(got)]) {
					t.Fatalf("n=%d: got %d bytes, not a prefix of at least %d", n, len(got), min(n, size))
				}
				// An unfiltered stream is the file's bytes: nothing is decoded.
				// Otherwise the output stops within one predictor row (with its
				// tag byte) and prefixSlack past n.
				if tc.st.Dict["Filter"] != nil && int64(len(got)) > n+prefixSlack+2*tc.row+1 {
					t.Errorf("n=%d: decoded %d bytes, want ≤ n + prefixSlack + one row", n, len(got))
				}
				if tc.st.Dict["Filter"] != nil && produced == 0 && size > 0 {
					t.Errorf("n=%d: produced 0, want the filter output counted", n)
				}
			}
		})
	}
}

// TestDecodeStreamPrefix_Limit: the prefix decode charges every stage to its
// limit; a prefix that needs more fails with ErrDecodeLimit (it is never
// silently cut short), and one that fits reports what it produced.
func TestDecodeStreamPrefix_Limit(t *testing.T) {
	full := make([]byte, 1<<20)
	st := &Stream{Dict: Dict{"Filter": Name("FlateDecode")}, Raw: zlibBytes(full)}
	if _, _, err := decodeStreamPrefix(st, 100000, 50000); !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("prefix over the limit: err = %v, want ErrDecodeLimit", err)
	}
	got, produced, err := decodeStreamPrefix(st, 100000, 200000)
	if err != nil || len(got) < 100000 || produced != int64(len(got)) || produced > 100000+prefixSlack {
		t.Fatalf("prefix within the limit: %d bytes, produced %d, err %v", len(got), produced, err)
	}
	// A chain: the first stage's output counts too.
	hex := &Stream{Dict: Dict{"Filter": Array{Name("FlateDecode"), Name("ASCIIHexDecode")}}, Raw: zlibBytes([]byte(fmt.Sprintf("%X>", full[:50000])))}
	if _, _, err := decodeStreamPrefix(hex, 10, 60000); !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("chain whose first stage exceeds the limit: err = %v, want ErrDecodeLimit", err)
	}
}

// TestParseXrefStream_FieldWidthCap: a /W field is at most 8 bytes (readField
// packs it into an int64). A wider one is damage (errLex, not errXrefLimit),
// rejected before the stream is decoded: /W [1 0 100000] made every row
// 100 KB, so /Size 2^28 decoded the full 256 MiB per section.
func TestParseXrefStream_FieldWidthCap(t *testing.T) {
	for _, tc := range []struct {
		w      string
		wantOK bool
	}{
		{"[1 0 8]", true},
		{"[8 8 8]", true},
		{"[1 0 9]", false},
		{"[9 0 0]", false},
		{"[1 0 100000]", false},
	} {
		t.Run(tc.w, func(t *testing.T) {
			var b bytes.Buffer
			b.WriteString("%PDF-1.5\n")
			raw := zlibBytes(make([]byte, 4096))
			off := writeStreamObj(&b, 1, fmt.Sprintf("<< /Type /XRef /W %s /Size 268435456 /Filter /FlateDecode /Length %d >>", tc.w, len(raw)), raw)
			fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", off)
			x, err := ParseXref(b.Bytes())
			if (err == nil) != tc.wantOK {
				t.Fatalf("ParseXref err = %v, wantOK %v", err, tc.wantOK)
			}
			if err != nil && (!errors.Is(err, errLex) || errors.Is(err, errXrefLimit) || x != nil) {
				t.Errorf("err = %v (table %v), want a plain errLex and no table", err, x != nil)
			}
		})
	}
}

// TestParseXrefStream_ChainDecodeBudget: the xref streams of one chain share
// a decode budget (maxXrefDecoded), charged with every filter stage's output
// whether or not the section is used. A stream whose prefix needs more than
// is left fails with errXrefLimit. The budget is lowered here so the test
// does not decode 256 MiB.
func TestParseXrefStream_ChainDecodeBudget(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	// /Columns 30000 makes one PNG predictor row 30 KB whatever /W says.
	rows := make([]byte, 3*30001) // three rows of filter type 0 and zeros
	raw := zlibBytes(rows)
	dict := fmt.Sprintf("<< /Type /XRef /W [1 0 0] /Size 2 /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 30000 >> /Length %d >>", len(raw))
	off1 := writeStreamObj(&b, 1, dict, raw)
	off2 := writeStreamObj(&b, 2, dict, raw)
	data := b.Bytes()

	budget := &xrefBudget{decodeLeft: 50000}
	x := &Xref{Entries: map[int]XrefEntry{}}
	if _, _, err := parseXrefStreamAt(data, off1, x, budget, nil); err != nil {
		t.Fatalf("first stream within the budget: %v", err)
	}
	if left := budget.decodeLeft; left >= 50000-30001 || left < 0 {
		t.Errorf("budget left %d after one ~30 KB row, want it charged", left)
	}
	if _, _, err := parseXrefStreamAt(data, off2, x, budget, nil); !errors.Is(err, errXrefLimit) {
		t.Fatalf("second stream past the budget: err = %v, want errXrefLimit", err)
	}
	// Control: with the default budget both fit.
	budget = newXrefBudget()
	for _, off := range []int{off1, off2} {
		if _, _, err := parseXrefStreamAt(data, off, x, budget, nil); err != nil {
			t.Fatalf("default budget: %v", err)
		}
	}
}

// TestParseXref_ClassicSubsectionCaps: the classic-table parser shares the
// bounds — a subsection header's count is charged before its rows are read.
func TestParseXref_ClassicSubsectionCaps(t *testing.T) {
	for _, tc := range []struct {
		header    string
		wantLimit bool
	}{
		{fmt.Sprintf("0 %d", maxXrefEntries+1), true},
		{"0 9223372036854775807", true},
		{"2147483647 2", false},
		{"0 -1", false},
		{"-1 1", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			pdf := []byte("%PDF-1.4\nxref\n" + tc.header + "\n0000000000 65535 f \ntrailer\n<< /Size 1 >>\nstartxref\n9\n%%EOF\n")
			x, err := ParseXref(pdf)
			if err == nil || x != nil {
				t.Fatalf("ParseXref = %v, %v; want an error and no table", x, err)
			}
			if errors.Is(err, errXrefLimit) != tc.wantLimit {
				t.Errorf("err = %v, want errXrefLimit %v", err, tc.wantLimit)
			}
		})
	}
}

// TestXrefAdd_Cap: the table merged along the /Prev chain stops at
// maxXrefEntries distinct objects and reports errXrefLimit; a number already
// present is kept (newest wins) and costs nothing, even at the cap.
func TestXrefAdd_Cap(t *testing.T) {
	x := &Xref{Entries: make(map[int]XrefEntry, maxXrefEntries)}
	for i := 0; i < maxXrefEntries; i++ {
		x.Entries[i] = XrefEntry{Free: true}
	}
	var added []int
	for _, num := range []int{0, 5} {
		if err := x.add(num, XrefEntry{Offset: 99}, &added); err != nil {
			t.Fatalf("adding existing number %d at the cap: %v", num, err)
		}
	}
	if !x.Entries[5].Free || len(added) != 0 {
		t.Errorf("entry 5 = %+v, added %v; want the existing entry kept and nothing added", x.Entries[5], added)
	}
	if err := x.add(maxXrefEntries, XrefEntry{}, &added); !errors.Is(err, errXrefLimit) {
		t.Fatalf("add past the cap err = %v, want errXrefLimit", err)
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

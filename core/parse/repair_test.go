package parse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Tests for the cross-reference repair (repair.go, NewResolverShifted):
// docs/design/xref-reconstruction.md §8.1, passes 1–2, duplicates, trailer and
// /Root synthesis, and the fail-closed /Encrypt recovery. Fixtures are built
// with computed offsets; "body" fixtures have no cross-reference data at all.

// body writes "%PDF-1.7" and the given objects in order (number, generation,
// body), with no xref, trailer or startxref, and returns the bytes and each
// object's offset in write order.
func body(objs ...repairObj) ([]byte, []int) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	var offs []int
	for _, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d %d obj\n%s\nendobj\n", o.num, o.gen, o.body)
	}
	return b.Bytes(), offs
}

type repairObj struct {
	num, gen int
	body     string
}

// onePage are the objects of a one-page document whose page has a 300×200
// MediaBox and a content stream.
var onePage = []repairObj{
	{1, 0, "<< /Type /Catalog /Pages 2 0 R >>"},
	{2, 0, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"},
	{3, 0, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R >>"},
	{4, 0, "<< /Length 23 >>\nstream\nBT (Hello repair) Tj ET\nendstream"},
}

// repaired opens data with NewRepairedResolver and walks its page tree.
func repaired(t *testing.T, data []byte) (*Resolver, []PageInfo, RepairInfo) {
	t.Helper()
	r, err := NewRepairedResolver(data)
	if err != nil {
		t.Fatalf("NewRepairedResolver: %v", err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	info, ok := r.Repair()
	if !ok || !info.Scanned {
		t.Fatalf("Repair() = %+v, %v; want a scan", info, ok)
	}
	return r, pages, info
}

// contentText returns the decoded content of page p.
func contentText(t *testing.T, r *Resolver, p PageInfo) string {
	t.Helper()
	obj, err := r.Resolve(p.Contents)
	if err != nil {
		t.Fatalf("resolve contents: %v", err)
	}
	st, ok := obj.(*Stream)
	if !ok {
		t.Fatalf("contents is %T, want a stream", obj)
	}
	b, err := DecodeStream(st)
	if err != nil {
		t.Fatalf("decode contents: %v", err)
	}
	return string(b)
}

func TestHeaderBehind(t *testing.T) {
	for _, tc := range []struct {
		in       string
		num, gen int
		ok       bool
	}{
		{"1 0 obj", 1, 0, true},
		{"\n12 3 obj", 12, 3, true},
		{"(x)7 0 obj", 7, 0, true}, // a delimiter before N
		{"01 0 obj", 1, 0, true},   // leading zero: the lexer reads 1
		{"8388607 65535 obj", 8388607, 65535, true},
		{"endobj", 0, 0, false},
		{"% 1 0 obj", 0, 0, false},     // a comment
		{"%x\n1 0 obj", 1, 0, true},    // the comment ended on the line before
		{"8388608 0 obj", 0, 0, false}, // past Annex C
		{"1 65536 obj", 0, 0, false},   // generation past 65535
		{"0 0 obj", 0, 0, false},       // object 0 is never in use
		{"x1 0 obj", 0, 0, false},      // N inside a longer word
		{"1 0obj", 0, 0, false},        // no white-space before obj
		{"1 obj", 0, 0, false},         // no generation
		{strings.Repeat(" ", 40) + "1" + strings.Repeat(" ", 31) + "0 obj", 0, 0, false}, // past the look-back window
	} {
		t.Run(tc.in, func(t *testing.T) {
			data := []byte(tc.in)
			h, ok := headerBehind(data, len(data)-3)
			if ok != tc.ok || (ok && (int(h.num) != tc.num || int(h.gen) != tc.gen)) {
				t.Fatalf("headerBehind(%q) = %+v, %v; want %d %d, %v", tc.in, h, ok, tc.num, tc.gen, tc.ok)
			}
			if ok && (data[h.off] < '0' || data[h.off] > '9' || (h.off > 0 && data[h.off-1] >= '0' && data[h.off-1] <= '9')) {
				t.Errorf("offset %d does not start at N", h.off)
			}
		})
	}
}

// TestRepair_Passes covers the object scan over damaged cross-reference data:
// each fixture must yield one 300×200 page whose content is "Hello repair".
func TestRepair_Passes(t *testing.T) {
	whole := buildPDF(map[int]string{1: onePage[0].body, 2: onePage[1].body, 3: onePage[2].body, 4: onePage[3].body},
		"<< /Size 5 /Root 1 0 R >>")
	noXref, _ := body(onePage...)
	xrefAt := bytes.Index(whole, []byte("\nxref\n")) + 1
	trailerAt := bytes.LastIndex(whole, []byte("trailer"))

	for _, tc := range []struct {
		name     string
		data     []byte
		fromScan bool
	}{
		{"no xref, trailer or startxref", noXref, true},
		{"startxref past the end", bytes.Replace(whole, []byte(fmt.Sprintf("startxref\n%d", xrefAt)), []byte("startxref\n99999999"), 1), false},
		{"startxref inside an object", bytes.Replace(whole, []byte(fmt.Sprintf("startxref\n%d", xrefAt)), []byte("startxref\n12"), 1), false},
		{"offsets all wrong", bytes.ReplaceAll(whole, []byte("\n00000"), []byte("\n99999")), false},
		{"cut at the trailer", whole[:trailerAt], true},
		{"cut inside the table", whole[:xrefAt+30], true},
		{"junk prefix and a broken table", append([]byte("junk\n"), whole[:xrefAt+30]...), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, pages, info := repaired(t, tc.data)
			if len(pages) != 1 || pages[0].MediaBox.Width() != 300 {
				t.Fatalf("pages = %+v, want one 300-wide page", pages)
			}
			if got := contentText(t, r, pages[0]); got != "BT (Hello repair) Tj ET" {
				t.Errorf("content = %q", got)
			}
			if info.Objects != 4 || info.Duplicates != 0 || info.CatalogFromScan != tc.fromScan || info.Truncated {
				t.Errorf("RepairInfo = %+v, want 4 objects, CatalogFromScan %v", info, tc.fromScan)
			}
		})
	}
}

// TestRepair_HeaderOffset: bytes before %PDF- leave every offset short by
// their length; NewResolverShifted reads the table with each offset moved.
func TestRepair_HeaderOffset(t *testing.T) {
	whole := buildMinimalPDF()
	junk := []byte("%!PS junk before the header\n\x00\x01")
	data := append(append([]byte{}, junk...), whole...)
	if r, err := NewResolver(data); err == nil {
		if _, err := r.Pages(); err == nil {
			t.Fatal("fixture opens strictly; want a broken strict open")
		}
	}
	r, err := NewResolverShifted(data, len(junk))
	if err != nil {
		t.Fatalf("NewResolverShifted: %v", err)
	}
	pages, err := r.Pages()
	if err != nil || len(pages) != 1 {
		t.Fatalf("Pages = %d, %v; want 1 page", len(pages), err)
	}
	info, ok := r.Repair()
	if !ok || info.Scanned || info.HeaderOffset != len(junk) || info.Objects != 4 {
		t.Errorf("Repair() = %+v, %v; want header offset %d, 4 objects", info, ok, len(junk))
	}
	for _, shift := range []int{0, -1, len(data)} {
		if _, err := NewResolverShifted(data, shift); !errors.Is(err, errLex) {
			t.Errorf("shift %d: err = %v, want errLex", shift, err)
		}
	}
	if _, ok := mustResolver(t, whole).Repair(); ok {
		t.Error("NewResolver reports a repair")
	}
}

func mustResolver(t *testing.T, data []byte) *Resolver {
	t.Helper()
	r, err := NewResolver(data)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestRepair_Duplicates: the last definition of a number in file order wins,
// whatever its generation; the shadowed ones are counted.
func TestRepair_Duplicates(t *testing.T) {
	page := func(w int) string {
		return fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d 200] /Contents 4 0 R >>", w)
	}
	for _, tc := range []struct {
		name  string
		objs  []repairObj
		width float64
		dups  int
	}{
		{"same generation, last wins", append(append([]repairObj{}, onePage...), repairObj{3, 0, page(500)}), 500, 1},
		{"later lower generation still wins", append([]repairObj{{3, 4, page(100)}}, append(append([]repairObj{}, onePage[:2]...), repairObj{3, 0, page(400)}, onePage[3])...), 400, 1},
		{"three definitions", append(append([]repairObj{{3, 0, page(100)}, {3, 1, page(200)}}, onePage[:2]...), repairObj{3, 2, page(600)}, onePage[3]), 600, 2},
		// A later definition that does not parse does not replace a good one.
		{"a later definition that does not parse loses", append(append([]repairObj{}, onePage...), repairObj{3, 0, "<< /Type /Page /MediaBox [0 0 900"}), 300, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := body(tc.objs...)
			_, pages, info := repaired(t, data)
			if len(pages) != 1 || pages[0].MediaBox.Width() != tc.width {
				t.Fatalf("page width = %v, want %v", pages[0].MediaBox.Width(), tc.width)
			}
			if info.Duplicates != tc.dups || info.Objects != 4 {
				t.Errorf("RepairInfo = %+v, want %d duplicates, 4 objects", info, tc.dups)
			}
		})
	}

	// An incremental update whose newest table is broken: the scan reads the
	// appended object, which is newer, as the winner.
	base := buildPDF(map[int]string{1: onePage[0].body, 2: onePage[1].body, 3: page(300), 4: onePage[3].body},
		"<< /Size 5 /Root 1 0 R >>")
	update := append(append([]byte{}, base...), []byte("3 0 obj\n"+page(700)+"\nendobj\nxref\n0 1\ngarbage\ntrailer\n<< /Root 1 0 R /Prev 1 >>\nstartxref\n5\n%%EOF\n")...)
	_, pages, info := repaired(t, update)
	if pages[0].MediaBox.Width() != 700 || info.Duplicates != 1 || info.CatalogFromScan {
		t.Errorf("incremental update: width %v, %+v; want 700, 1 duplicate, /Root from a trailer", pages[0].MediaBox.Width(), info)
	}
}

// TestRepair_StreamMasking: headers inside stream data are not objects. The
// embedded "1 0 obj" would win as the last definition of object 1 (with no
// /Pages, so no catalog) if it were not masked.
func TestRepair_StreamMasking(t *testing.T) {
	fake := "1 0 obj << /Type /Catalog /Pages 9 0 R >> endobj\n3 0 obj << /Type /Page >> endobj"
	for _, tc := range []struct {
		name string
		objs []repairObj
	}{
		{"direct /Length", []repairObj{{5, 0, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(fake), fake)}}},
		{"indirect /Length written after the stream", []repairObj{
			{5, 0, fmt.Sprintf("<< /Length 6 0 R >>\nstream\n%s\nendstream", fake)},
			{6, 0, fmt.Sprint(len(fake))}}},
		{"no /Length: the first endstream", []repairObj{{5, 0, "<<>>\nstream\n" + fake + "\nendstream"}}},
		{"wrong /Length: the first endstream", []repairObj{{5, 0, "<< /Length 3 >>\nstream\n" + fake + "\nendstream"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := body(append(append([]repairObj{}, onePage...), tc.objs...)...)
			r, pages, info := repaired(t, data)
			if len(pages) != 1 || pages[0].MediaBox.Width() != 300 {
				t.Fatalf("pages = %+v; the embedded headers won", pages)
			}
			if info.Candidates != 4+len(tc.objs)+2 || info.Objects != 4+len(tc.objs) || info.Duplicates != 0 {
				t.Errorf("RepairInfo = %+v; want the two embedded headers counted as candidates and masked", info)
			}
			obj, err := r.Resolve(Reference{Number: 5})
			if st, ok := obj.(*Stream); err != nil || !ok || !bytes.Contains(st.Raw, []byte("9 0 R")) {
				t.Errorf("object 5 = %v, %v; want the stream holding the embedded text", obj, err)
			}
		})
	}

	// An uncompressed embedded PDF with a valid /Length: its catalog, later in
	// the file than the real one, must not be the catalog the scan finds.
	inner, _ := body(repairObj{1, 0, "<< /Type /Catalog /Pages 2 0 R >>"},
		repairObj{2, 0, "<< /Type /Pages /Kids [] /Count 0 >>"})
	data, _ := body(onePage[1], onePage[2], onePage[3], onePage[0],
		repairObj{7, 0, fmt.Sprintf("<< /Type /EmbeddedFile /Length %d >>\nstream\n%s\nendstream", len(inner), inner)})
	_, pages, info := repaired(t, data)
	if len(pages) != 1 || !info.CatalogFromScan {
		t.Errorf("embedded PDF: %d pages, %+v; want the outer catalog's one page", len(pages), info)
	}

	// A stream with no endstream masks nothing: the objects after it stay.
	// (No other stream follows, so no endstream exists after its data.)
	data, _ = body(repairObj{5, 0, "<< /Length 99999 >>\nstream\nunterminated data"}, onePage[0], onePage[1], onePage[2])
	_, pages, info = repaired(t, data)
	if len(pages) != 1 || !info.Truncated || info.Objects != 4 {
		t.Errorf("stream without endstream: %d pages, %+v; want 1 page, Truncated, 4 objects", len(pages), info)
	}
}

// TestRepair_Trailer: /Root, /Info and /ID come from the last trailer
// candidate that has a usable one; a /Root naming a non-catalog falls through
// to the catalog scan; a cross-reference stream's dictionary is a candidate.
func TestRepair_Trailer(t *testing.T) {
	objs := append(append([]repairObj{}, onePage...), repairObj{5, 0, "<< /Title (Repaired) >>"},
		repairObj{6, 0, "<< /Type /Catalog /Pages 2 0 R /Lang (en) >>"})
	data, _ := body(objs...)
	withTrailer := func(tr string) []byte {
		return append(append([]byte{}, data...), []byte("trailer\n"+tr+"\n%%EOF\n")...)
	}

	t.Run("last usable candidate wins", func(t *testing.T) {
		d := append(withTrailer("<< /Root 6 0 R /Info 5 0 R /ID [<01> <02>] >>"), []byte("trailer\n<< /Root 1 0 R /Info 3 0 R >>\n")...)
		r, _, info := repaired(t, d)
		tr := r.Trailer()
		if tr["Root"] != (Reference{Number: 1}) || info.CatalogFromScan {
			t.Errorf("/Root = %v, want 1 0 R from the last trailer", tr["Root"])
		}
		if tr["Info"] != (Reference{Number: 3}) { // a dictionary, so it qualifies
			t.Errorf("/Info = %v, want 3 0 R", tr["Info"])
		}
		if id, ok := tr["ID"].(Array); !ok || len(id) != 2 {
			t.Errorf("/ID = %v, want the earlier trailer's", tr["ID"])
		}
	})
	t.Run("/Root naming a non-catalog", func(t *testing.T) {
		r, _, info := repaired(t, withTrailer("<< /Root 3 0 R >>"))
		if !info.CatalogFromScan || r.Trailer()["Root"] != (Reference{Number: 6}) {
			t.Errorf("/Root = %v, %+v; want the last catalog in the file, by scan", r.Trailer()["Root"], info)
		}
	})
	t.Run("/Root naming a missing object", func(t *testing.T) {
		_, _, info := repaired(t, withTrailer("<< /Root 42 0 R /Info 41 0 R >>"))
		if !info.CatalogFromScan {
			t.Errorf("%+v; want the catalog by scan", info)
		}
	})
	t.Run("trailer keyword inside stream data is ignored", func(t *testing.T) {
		// The embedded trailer is the last one in the file and names a usable
		// catalog: only masking keeps it out.
		s := "trailer << /Root 6 0 R >>"
		d := withTrailer("<< /Root 1 0 R >>")
		d = append(d, []byte(fmt.Sprintf("7 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(s), s))...)
		r, _, _ := repaired(t, d)
		if r.Trailer()["Root"] != (Reference{Number: 1}) {
			t.Errorf("/Root = %v, want 1 0 R", r.Trailer()["Root"])
		}
	})
	t.Run("cross-reference stream dictionary", func(t *testing.T) {
		var b bytes.Buffer
		b.Write(data)
		rows := packXref([][3]int64{{0, 0, 0}}, [3]int{1, 1, 1})
		writeStreamObj(&b, 8, fmt.Sprintf("<< /Type /XRef /W [1 1 1] /Size 1 /Root 1 0 R /Info 5 0 R /Length %d >>", len(rows)), rows)
		r, _, info := repaired(t, b.Bytes())
		if r.Trailer()["Root"] != (Reference{Number: 1}) || r.Trailer()["Info"] != (Reference{Number: 5}) || info.CatalogFromScan {
			t.Errorf("trailer = %v, %+v; want /Root and /Info from the xref stream", r.Trailer(), info)
		}
	})
}

// TestRepair_Failures: no catalog anywhere, a winning object stream (not
// expanded in this increment) and data with no header at all fail with
// errLex; nothing panics.
func TestRepair_Failures(t *testing.T) {
	noCatalog, _ := body(onePage[1:]...)
	objStm, _ := body(append(append([]repairObj{}, onePage...),
		repairObj{9, 0, "<< /Type /ObjStm /N 1 /First 4 /Length 8 >>\nstream\n7 0 null\nendstream"})...)
	shadowedObjStm, _ := body(append([]repairObj{{9, 0, "<< /Type /ObjStm /N 1 /First 4 /Length 8 >>\nstream\n7 0 null\nendstream"}},
		append(append([]repairObj{}, onePage...), repairObj{9, 0, "null"})...)...)
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"no catalog", noCatalog, "no document catalog"},
		{"winning object stream", objStm, "object stream"},
		{"no headers", []byte("%PDF-1.7\njust text\n"), "no document catalog"},
		{"empty", nil, "no document catalog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRepairedResolver(tc.data)
			if r != nil || !errors.Is(err, errLex) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewRepairedResolver = %v, %v; want errLex naming %q", r, err, tc.want)
			}
		})
	}
	// An object stream a later definition shadows is not in the table.
	if _, _, info := repaired(t, shadowedObjStm); info.Duplicates != 1 {
		t.Errorf("shadowed ObjStm: %+v, want 1 duplicate", info)
	}
}

// TestRepair_EncryptFailsClosed: a repaired encrypted file is decrypted with
// its encryption dictionary, never read as plaintext. With the trailer lost,
// the dictionary is found by its shape; without /ID the R3 key is wrong, and
// the password check refuses it.
func TestRepair_EncryptFailsClosed(t *testing.T) {
	id := []byte("0123456789abcdef")
	sec := StdSecurity{R: 3, KeyLen: 16, Owner: []byte("owner"), P: -44, ID: id}
	plain := "BT /F1 12 Tf 10 10 Td (Secret text) Tj ET"
	whole := EncryptedPDF(sec, 2, "", plain, nil)
	xrefAt := bytes.Index(whole, []byte("\nxref\n")) + 1
	trailerAt := bytes.LastIndex(whole, []byte("trailer"))

	t.Run("trailer survives: decrypts", func(t *testing.T) {
		broken := append(append([]byte{}, whole[:xrefAt]...), bytes.ReplaceAll(whole[xrefAt:], []byte("\n00000"), []byte("\n99999"))...) // every offset wrong
		r, err := NewRepairedResolver(broken)
		if err != nil {
			t.Fatal(err)
		}
		if r.Trailer()["Encrypt"] != (Reference{Number: 5}) {
			t.Fatalf("/Encrypt = %v, want 5 0 R from the trailer", r.Trailer()["Encrypt"])
		}
		if err := r.EnableDecryption(nil); err != nil {
			t.Fatalf("EnableDecryption: %v", err)
		}
		pages, err := r.Pages()
		if err != nil || len(pages) != 1 {
			t.Fatalf("Pages: %d, %v", len(pages), err)
		}
		if got := contentText(t, r, pages[0]); got != plain {
			t.Errorf("content = %q, want the decrypted text", got)
		}
	})
	t.Run("trailer lost: found by shape, refused without /ID", func(t *testing.T) {
		r, err := NewRepairedResolver(whole[:trailerAt])
		if err != nil {
			t.Fatal(err)
		}
		if r.Trailer()["Encrypt"] != (Reference{Number: 5}) {
			t.Fatalf("/Encrypt = %v, want 5 0 R by shape", r.Trailer()["Encrypt"])
		}
		if err := r.EnableDecryption(nil); err == nil {
			t.Fatal("EnableDecryption succeeded without /ID; want the password check to refuse the wrong key")
		}
	})
	t.Run("public-key shape", func(t *testing.T) {
		data, _ := body(append(append([]repairObj{}, onePage...), repairObj{5, 0, "<< /Filter /Adobe.PubSec /SubFilter /adbe.pkcs7.s4 /Recipients [<00>] >>"})...)
		r, err := NewRepairedResolver(data)
		if err != nil {
			t.Fatal(err)
		}
		if r.Trailer()["Encrypt"] != (Reference{Number: 5}) {
			t.Fatalf("/Encrypt = %v, want 5 0 R", r.Trailer()["Encrypt"])
		}
		if err := r.EnableDecryption(nil); err == nil {
			t.Fatal("EnableDecryption accepted a public-key handler")
		}
	})
	// The trailer and every usable encryption dictionary are gone, but the file
	// shows it had one: the scan fails with ErrEncryptionLost (core reports
	// ErrEncryptedDocument) instead of returning a table that opens the
	// ciphertext as plaintext.
	enc := "<< /Filter /Standard /V 1 /R 2 /Length 40 /P -4 /O (0123456789) /U (abcdefghij) >>"
	plainDoc, _ := body(onePage...)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated inside /O", append(append([]byte{}, plainDoc...), "5 0 obj\n<< /Filter /Standard /V 1 /R 2 /Length 40 /P -4 /O (\x11\x22"...)},
		{"truncated after the handler name", append(append([]byte{}, plainDoc...), "5 0 obj\n<< /Filter /Standard"...)},
		{"truncated redefinition of a plain object", append(append([]byte{}, plainDoc...), "4 0 obj\n<< /Filter /Standard /V 2 /O <0011"...)},
		{"shadowed by a later definition", func() []byte {
			d, _ := body(append(append([]repairObj{}, onePage...), repairObj{5, 0, enc}, repairObj{5, 0, "<< /Type /Other >>"})...)
			return d
		}()},
		{"masked as stream data", func() []byte {
			// Stream 6 has no endstream of its own: its data runs to stream 7's,
			// and the encryption dictionary between them is masked.
			d, _ := body(append(append([]repairObj{}, onePage...), repairObj{6, 0, "<< /Length 999 >>\nstream\nbinary"},
				repairObj{5, 0, enc}, repairObj{7, 0, "<< /Length 1 >>\nstream\nx\nendstream"})...)
			return d
		}()},
		{"trailer naming /Encrypt cut short", append(append([]byte{}, plainDoc...), "trailer\n<< /Root 1 0 R /Encrypt 5 0 R /ID [<01> <02"...)},
		{"cross-reference stream dictionary cut short", append(append([]byte{}, plainDoc...), "8 0 obj\n<< /Type /XRef /Root 1 0 R /Encrypt 5 0 R /W [1 1 1] /Size (9"...)},
		// A signature marker never hides /Filter /Standard or an encryption key.
		{"cut short naming /Contents", append(append([]byte{}, plainDoc...), "5 0 obj\n<< /Filter /Standard /V 1 /R 2 /Contents 0 /P -4 /O (\x11\x22"...)},
		{"garbled, its span running into a page", append(append([]byte{}, plainDoc...),
			"5 0 obj\n<< /Filter /Standard /V 1 /R 2 /P -4 /O (\x11\x22) /U (\x33) ]]] garbage\n<< /Type /Page /Contents 4 0 R >>\nendobj\n"...)},
		{"cut short naming /ByteRange", append(append([]byte{}, plainDoc...), "5 0 obj\n<< /Filter /Standard /ByteRange [0 1] /O (\x11"...)},
		{"cut short with a /Type /Sig spoof", append(append([]byte{}, plainDoc...), "5 0 obj\n<< /Type /Sig /Filter /Custom /V 2 /R 3 /O <0011"...)},
		// Public-key handlers (§7.6.5): /SubFilter is not a signature marker.
		{"public-key handler cut after /SubFilter", append(append([]byte{}, plainDoc...), "5 0 obj<</Filter/Adobe.PubSec/SubFilter/adbe.pkcs7.s5/V 4/Length 128"...)},
		{"public-key handler cut after its /SubFilter value", append(append([]byte{}, plainDoc...), "5 0 obj<</Filter/Adobe.PubSec/SubFilter/adbe.pkcs7.s4"...)},
		{"public-key /SubFilter before /Filter", append(append([]byte{}, plainDoc...), "5 0 obj<</SubFilter/adbe.pkcs7.s3/Filter/Adobe.PubSec/R 65537"...)},
		// An unterminated string does not hide the names after it.
		{"stray '(' before the keys", append(append([]byte{}, plainDoc...), "5 0 obj<<(\x00/Filter/Standard/V 2/R 3/O<00>/U<00>/P -4>>endobj\n"...)},
		{"stray '<' before the keys", append(append([]byte{}, plainDoc...), "5 0 obj<</Length 40 <\x01 /Filter/Standard/V 2/O(a)/U(b)/P -4 ]]"...)},
		// A third-party handler whose span swallowed an image dictionary.
		{"third-party handler cut, running into an image", append(append([]byte{}, plainDoc...),
			"5 0 obj<</Filter/FOPN_foweb/V 1/Length 40 ]]\n<</Type/XObject/Subtype/Image/Width 1>>endobj\n"...)},
		// A winner with the keys that cannot be read as one.
		{"followed by a stray stream keyword", func() []byte {
			d, _ := body(append(append([]repairObj{}, onePage...), repairObj{5, 0, enc[:len(enc)-3] + " >>\nstream\nzz\nendstream"})...)
			return d
		}()},
		{"indirect /Filter", func() []byte {
			d, _ := body(append(append([]repairObj{}, onePage...),
				repairObj{5, 0, "<< /Filter 6 0 R /V 1 /R 2 /P -4 /O (0123456789) /U (abcdefghij) >>"}, repairObj{6, 0, "/Standard"})...)
			return d
		}()},
	} {
		t.Run("lost: "+tc.name, func(t *testing.T) {
			r, err := NewRepairedResolver(tc.data)
			if r != nil || !errors.Is(err, ErrEncryptionLost) || !errors.Is(err, errLex) {
				t.Fatalf("NewRepairedResolver = %v, %v; want ErrEncryptionLost", r, err)
			}
		})
	}
	// Bytes that only resemble an encryption dictionary are not one.
	for _, tc := range []struct {
		name string
		obj  repairObj
	}{
		{"a broken stream dictionary", repairObj{5, 0, "<< /Filter /FlateDecode /Length (5"}},
		{"a broken signature dictionary", repairObj{5, 0, "<< /Type /Sig /Filter /Adobe.PPKLite /ByteRange [0 1 2 3] /Contents <00"}},
		{"names only as prefixes", repairObj{5, 0, "<< /Filters /Standard /Open /Use /Page"}},
		{"a broken DocMDP signature (/P in its transform parameters)", repairObj{5, 0,
			"<< /Type /Sig /Filter /Adobe.PPKLite /Reference [<< /TransformMethod /DocMDP /TransformParams << /P 2"}},
		{"a broken annotation, /Filter inside a string", repairObj{5, 0, "<< /Type /Annot /Subtype /Text /P 3 0 R /T (a /Filter b) ]] >>"}},
		{"a broken annotation, /Filter inside a nested, escaped string", repairObj{5, 0, "<< /P 3 0 R /T (a \\) (/Filter) b) ]] >>"}},
		{"a broken dictionary, /Filter inside a hex string", repairObj{5, 0, "<< /P 3 0 R /T </Filter> ]] >>"}},
		{"a broken dictionary, /Encrypt inside a string", repairObj{5, 0, "<< /Title (how /Encrypt works) ]] >>"}},
		{"a broken image, a filter name cut short", repairObj{5, 0, "<< /Subtype /Image /Filter /JBIG2 ]] >>"}},
		{"a broken signature with the adbe.pkcs7.sha1 SubFilter", repairObj{5, 0, "<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.sha1 /Contents <00"}},
	} {
		t.Run("not lost: "+tc.name, func(t *testing.T) {
			data, _ := body(append(append([]repairObj{}, onePage...), tc.obj)...)
			r, _, _ := repaired(t, data)
			if _, ok := r.Trailer()["Encrypt"]; ok {
				t.Errorf("/Encrypt = %v, want none", r.Trailer()["Encrypt"])
			}
		})
	}

	t.Run("a stream /Filter is not an encryption dictionary", func(t *testing.T) {
		data, _ := body(append(append([]repairObj{}, onePage...), repairObj{5, 0, "<< /Filter /FlateDecode /Length 0 /O 1 >>\nstream\n\nendstream"})...)
		r, _, _ := repaired(t, data)
		if _, ok := r.Trailer()["Encrypt"]; ok {
			t.Errorf("/Encrypt = %v, want none", r.Trailer()["Encrypt"])
		}
	})
}

// TestParseXref_ShiftZeroIdentical: the shifted parser with shift 0 is
// ParseXref, entry for entry.
func TestParseXref_ShiftZeroIdentical(t *testing.T) {
	data := buildMinimalPDF()
	a, err1 := ParseXref(data)
	b, err2 := parseXref(data, 0)
	if err1 != nil || err2 != nil || len(a.Entries) != len(b.Entries) {
		t.Fatalf("ParseXref %v %v", err1, err2)
	}
	for n, e := range a.Entries {
		if b.Entries[n] != e {
			t.Errorf("entry %d: %+v vs %+v", n, e, b.Entries[n])
		}
	}
}

// TestIsLimitError_Bounds: a failure on any resource bound is a limit error
// (core.OpenBytes does not repair it) and still matches errLex; damage is not.
func TestIsLimitError_Bounds(t *testing.T) {
	deepPages := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	const levels = maxPageTreeDepth + 2
	for i := 2; i < 2+levels; i++ {
		deepPages[i] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", i+1)
	}
	deepPages[2+levels] = "<< /Type /Page /MediaBox [0 0 10 10] >>"
	deepValue := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /X " + strings.Repeat("[", maxNestingDepth+1) + strings.Repeat("]", maxNestingDepth+1) + " >>",
		2: "<< /Type /Pages /Kids [] /Count 0 >>",
	}, "<< /Size 3 /Root 1 0 R >>")

	// A /Prev chain of maxXrefChain+1 sections, each a valid empty table.
	var chain bytes.Buffer
	chain.WriteString("%PDF-1.7\n")
	prev := -1
	for i := 0; i <= maxXrefChain; i++ {
		at := chain.Len()
		chain.WriteString("xref\n0 0\ntrailer\n<< /Size 1")
		if prev >= 0 {
			fmt.Fprintf(&chain, " /Prev %d", prev)
		}
		chain.WriteString(" >>\n")
		prev = at
	}
	fmt.Fprintf(&chain, "startxref\n%d\n%%%%EOF\n", prev)

	pagesErr := func(data []byte) error {
		r, err := NewResolver(data)
		if err != nil {
			return err
		}
		_, err = r.Pages()
		return err
	}
	for _, tc := range []struct {
		name  string
		err   error
		limit bool
	}{
		{"page tree depth", pagesErr(buildPDF(deepPages, "<< /Root 1 0 R >>")), true},
		{"nesting depth", pagesErr(deepValue), true},
		{"xref chain length", pagesErr(chain.Bytes()), true},
		{"damage: no catalog", pagesErr(buildPDF(map[int]string{1: "<< /Type /Pages >>"}, "<< /Root 9 0 R >>")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !errors.Is(tc.err, errLex) || IsLimitError(tc.err) != tc.limit {
				t.Fatalf("err = %v; want errLex with IsLimitError %v", tc.err, tc.limit)
			}
		})
	}
}

// TestRepairedLoad_ObjectNumberChecked: a repaired table whose offset lands
// on another object's header does not load that object under the requested
// number; the strict table keeps its lenient reading.
func TestRepairedLoad_ObjectNumberChecked(t *testing.T) {
	data := buildMinimalPDF()
	strict := mustResolver(t, data)
	at := strict.xref.Entries[2] // object 2's offset, entered for object 3
	x := &Xref{Entries: map[int]XrefEntry{3: at}, Trailer: Dict{}}

	if obj, err := newResolver(data, x).Resolve(Reference{Number: 3}); err != nil || obj == nil {
		t.Fatalf("strict table: %v, %v; want object 2's value", obj, err)
	}
	r := newResolver(data, x)
	r.repair = &RepairInfo{HeaderOffset: 1}
	if obj, err := r.Resolve(Reference{Number: 3}); err == nil || !errors.Is(err, errLex) {
		t.Fatalf("repaired table: %v, %v; want errLex for the wrong object number", obj, err)
	}
}

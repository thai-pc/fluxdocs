package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Tests for the open repair ladder (open.go, docs/design/xref-reconstruction.md
// §2, §5, §6): which files are repaired and how it is reported, that a
// repaired document saves as a well-formed file and redacts, and that it
// cannot be signed.

// repairText is the text of the repairable fixtures' page.
const repairText = "Repair sample"

// repairFixtures returns a well-formed one-page document and damaged copies
// of it: the same bytes behind a junk prefix (header-offset repair), and the
// bytes cut before the cross-reference table (object scan).
func repairFixtures() (whole, prefixed, cut []byte) {
	whole = redactFixture("BT /F1 12 Tf 20 700 Td ("+repairText+") Tj ET", "", nil)
	prefixed = append([]byte("%!PS junk \"quoted\" before the header\n"), whole...)
	cut = whole[:bytes.Index(whole, []byte("\nxref\n"))+1]
	return whole, prefixed, cut
}

func TestOpenBytes_RepairLadder(t *testing.T) {
	whole, prefixed, cut := repairFixtures()
	for _, tc := range []struct {
		name   string
		data   []byte
		method RepairMethod // "" for a strict open
	}{
		{"well-formed opens strictly", whole, ""},
		{"junk prefix: header offset", prefixed, RepairHeaderOffset},
		{"no xref or trailer: object scan", cut, RepairObjectScan},
		{"junk prefix and no xref: object scan", append([]byte("junk\n"), cut...), RepairObjectScan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytes(tc.data)
			if err != nil {
				t.Fatalf("OpenBytes: %v", err)
			}
			rep, ok := doc.RepairReport()
			if ok != (tc.method != "") || rep.Method != tc.method {
				t.Fatalf("RepairReport = %+v, %v; want method %q", rep, ok, tc.method)
			}
			text, err := doc.ExtractText(ExtractOptions{})
			if err != nil || !strings.Contains(text, repairText) {
				t.Errorf("ExtractText = %q, %v", text, err)
			}
			if !ok {
				return
			}
			if rep.StrictError == "" || rep.Objects != 5 {
				t.Errorf("RepairReport = %+v; want the strict error and 5 objects", rep)
			}
			if tc.method == RepairHeaderOffset && rep.HeaderOffset != len(prefixed)-len(whole) {
				t.Errorf("HeaderOffset = %d, want %d", rep.HeaderOffset, len(prefixed)-len(whole))
			}
			if tc.method == RepairObjectScan && !rep.CatalogFromScan {
				t.Errorf("CatalogFromScan = false; no trailer survives")
			}
			if strings.HasPrefix(rep.StrictError, ErrInvalidPDF.Error()) {
				t.Errorf("StrictError %q repeats the sentinel", rep.StrictError)
			}
		})
	}
}

// TestOpenBytes_RepairFails: a file no rung opens fails with ErrInvalidPDF,
// naming the strict failure and the repair failure.
func TestOpenBytes_RepairFails(t *testing.T) {
	_, _, cut := repairFixtures()
	noCatalog := bytes.Replace(cut, []byte("/Type /Catalog /Pages 2 0 R"), []byte("/Type /Other"), 1)
	doc, err := OpenBytes(noCatalog)
	if doc != nil || !errors.Is(err, ErrInvalidPDF) || !strings.Contains(err.Error(), "repair failed") ||
		strings.Count(err.Error(), ErrInvalidPDF.Error()) != 1 {
		t.Fatalf("OpenBytes = %v, %v; want one ErrInvalidPDF naming the repair failure", doc, err)
	}
	// The internal strict open never repairs.
	if _, err := openStrict(cut); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("openStrict(damaged) err = %v, want ErrInvalidPDF", err)
	}
}

// TestRepaired_SaveReopensStrictly (§5 rule R.1): saving a repaired document
// writes a full rebuild that opens without repair, with the same pages and
// text.
func TestRepaired_SaveReopensStrictly(t *testing.T) {
	_, prefixed, cut := repairFixtures()
	for _, data := range [][]byte{prefixed, cut} {
		doc, err := OpenBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := doc.SaveTo(&buf); err != nil {
			t.Fatalf("SaveTo: %v", err)
		}
		out, err := openStrict(buf.Bytes())
		if err != nil {
			t.Fatalf("strict re-open of the saved repair: %v", err)
		}
		if _, ok := out.RepairReport(); ok || out.PageCount != doc.PageCount {
			t.Errorf("saved output: repaired %v, %d pages; want a strict open of %d", ok, out.PageCount, doc.PageCount)
		}
		before, _ := doc.ExtractText(ExtractOptions{})
		after, _ := out.ExtractText(ExtractOptions{})
		if before != after {
			t.Errorf("text changed through the save: %q -> %q", before, after)
		}
	}
}

// TestRepaired_SignRefused (§5 rule R.2, decision Q2): an incremental update
// of a repaired document is refused with ErrInvalidPDF; the saved and
// re-opened document signs.
func TestRepaired_SignRefused(t *testing.T) {
	certPEM, keyPEM := testSigner(t)
	_, _, cut := repairFixtures()
	doc, err := OpenBytes(cut)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := doc.SignDocument(SignatureOptions{CertPEM: certPEM, KeyPEM: keyPEM})
	if signed != nil || !errors.Is(err, ErrInvalidPDF) ||
		!strings.Contains(err.Error(), "document was repaired; save and re-open it before signing") {
		t.Fatalf("SignDocument = %v, %v; want the repaired-document refusal", signed, err)
	}
	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatal(err)
	}
	clean, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clean.SignDocument(SignatureOptions{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Errorf("SignDocument after save and re-open: %v", err)
	}
}

// TestRepaired_RedactionReportsSource (decision Q1 (a)): redaction runs on a
// repaired document, removes the text, and the report says the source was
// repaired; the output opens strictly.
func TestRepaired_RedactionReportsSource(t *testing.T) {
	whole, _, cut := repairFixtures()
	for _, tc := range []struct {
		name     string
		data     []byte
		repaired bool
	}{{"strict source", whole, false}, {"repaired source", cut, true}} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytes(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := doc.RedactText(0, repairText); err != nil {
				t.Fatal(err)
			}
			out, err := doc.RedactAndFlatten()
			if err != nil {
				t.Fatalf("RedactAndFlatten: %v", err)
			}
			if text, _ := out.ExtractText(ExtractOptions{}); strings.Contains(text, repairText) {
				t.Fatalf("redacted output still holds the text: %q", text)
			}
			rep, ok := out.RedactionReport()
			if !ok || rep.SourceRepaired != tc.repaired {
				t.Errorf("RedactionReport = %+v, %v; want SourceRepaired %v", rep, ok, tc.repaired)
			}
			if _, ok := out.RepairReport(); ok {
				t.Error("the redacted output itself was repaired")
			}
		})
	}
}

// TestOpenBytes_EncryptedFlag: Document.Encrypted reports an encrypted source
// (it was never set before), and only then.
func TestOpenBytes_EncryptedFlag(t *testing.T) {
	data, err := os.ReadFile("testdata/encrypted_rc4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := OpenBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Encrypted {
		t.Error("Encrypted = false for an encrypted source")
	}
	plain, err := OpenBytes(minimalDocBytes())
	if err != nil {
		t.Fatal(err)
	}
	if plain.Encrypted {
		t.Error("Encrypted = true for a plain source")
	}
	// SaveTo writes the decrypted content without /Encrypt.
	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatal(err)
	}
	out, err := OpenBytes(buf.Bytes())
	if err != nil || out.Encrypted || bytes.Contains(buf.Bytes(), []byte("/Encrypt")) {
		t.Errorf("saved encrypted source: %v, Encrypted %v; want an unencrypted file", err, out != nil && out.Encrypted)
	}
}

func TestElideQuoted(t *testing.T) {
	for in, want := range map[string]string{
		`unexpected keyword "Secret"`:          `unexpected keyword "…"`,
		`a "x\"y" b "z"`:                       `a "…" b "…"`,
		`no quotes at offset 42`:               `no quotes at offset 42`,
		`unterminated "secret text to the end`: `unterminated "`,
		`invalid character 'S' at offset 9`:    `invalid character '…' at offset 9`,
		`invalid hex character '\x00'`:         `invalid hex character '…'`,
		`a '\'' b '\u00e9'`:                    `a '…' b '…'`,
		`missing 'startxref'`:                  `missing 'startxref'`,
		`it's a "q" and 'x'`:                   `it's a "…" and '…'`,
	} {
		if got := elideQuoted(in); got != want {
			t.Errorf("elideQuoted(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOpenBytes_EncryptionLostFailsClosed (design §3.6): a file cut inside
// its encryption dictionary, with the trailer gone, fails as encrypted; it is
// never opened as plaintext with Encrypted false.
func TestOpenBytes_EncryptionLostFailsClosed(t *testing.T) {
	_, _, cut := repairFixtures()
	data := append(append([]byte{}, cut...), "9 0 obj\n<< /Filter /Standard /V 1 /R 2 /P -4 /O (\x11\x22"...)
	doc, err := OpenBytes(data)
	if doc != nil || !errors.Is(err, ErrEncryptedDocument) || errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("OpenBytes = %v, %v; want ErrEncryptedDocument", doc, err)
	}
}

// TestOpenBytes_LimitEndsLadder: a junk-prefixed file whose strict open hits
// a resource bound fails with that error alone: no header-offset retry, no
// object scan (their failure would be named after "repair failed").
func TestOpenBytes_LimitEndsLadder(t *testing.T) {
	prefixed := func(objects map[int]string) []byte {
		var b bytes.Buffer
		b.WriteString("junk before the header\n%PDF-1.7\n")
		off := map[int]int{}
		for i := 1; i <= len(objects); i++ {
			off[i] = b.Len() // measured from the start of the file: the strict open reads them
			fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objects[i])
		}
		xref := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
		for i := 1; i <= len(objects); i++ {
			fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
		return b.Bytes()
	}
	deep := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	for i := 2; i < 62; i++ {
		deep[i] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", i+1)
	}
	deep[62] = "<< /Type /Page /MediaBox [0 0 10 10] >>"
	nested := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /X " + strings.Repeat("[", 300) + strings.Repeat("]", 300) + " >>",
		2: "<< /Type /Pages /Kids [] /Count 0 >>",
	}
	for _, tc := range []struct {
		name, want string
		data       []byte
	}{
		{"page tree depth", "page tree too deep", prefixed(deep)},
		{"nesting depth", "nested deeper than", prefixed(nested)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytes(tc.data)
			if doc != nil || !errors.Is(err, ErrInvalidPDF) || !strings.Contains(err.Error(), tc.want) ||
				strings.Contains(err.Error(), "repair failed") {
				t.Fatalf("OpenBytes = %v, %v; want ErrInvalidPDF naming %q, with no repair", doc, err, tc.want)
			}
		})
	}
}

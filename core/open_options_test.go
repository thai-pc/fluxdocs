package core

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// encryptedFixture reads a fixture of testing/corpus/encrypted (written by
// qpdf and read by Ghostscript before it was committed; see its README).
func encryptedFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testing", "corpus", "encrypted", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// reasonOf returns the reason of the *EncryptionError err carries, checking
// that it matches ErrEncryptedDocument; "" when it carries none.
func reasonOf(t *testing.T, err error) EncryptionReason {
	t.Helper()
	var ee *EncryptionError
	if !errors.As(err, &ee) {
		return ""
	}
	if !errors.Is(err, ErrEncryptedDocument) || errors.Is(err, ErrInvalidPDF) {
		t.Errorf("%v: an *EncryptionError must match ErrEncryptedDocument only", err)
	}
	return ee.Reason
}

// TestOpenBytesWithOptions_Reasons pins the *EncryptionError reasons of the
// open (Q2) and that no message holds the password.
func TestOpenBytesWithOptions_Reasons(t *testing.T) {
	user := encryptedFixture(t, "qpdf_r6_user.pdf")
	for _, tc := range []struct {
		name string
		data []byte
		pw   string
		want EncryptionReason
	}{
		{"no password", user, "", EncryptionPasswordRequired},
		{"wrong password", user, "Secret-Guess-123", EncryptionWrongPassword},
		{"user password", user, "user", ""},
		{"owner password", user, "owner", ""},
		{"owner-only document, no password", encryptedFixture(t, "qpdf_r6_owner_only.pdf"), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := OpenBytesWithOptions(tc.data, OpenOptions{Password: tc.pw})
			if got := reasonOf(t, err); got != tc.want {
				t.Fatalf("err = %v, want reason %q", err, tc.want)
			}
			if err != nil {
				if doc != nil {
					t.Error("non-nil document with an error")
				}
				if tc.pw != "" && strings.Contains(err.Error(), tc.pw) {
					t.Errorf("error %q holds the password", err)
				}
				return
			}
			if !doc.Encrypted {
				t.Error("Encrypted = false")
			}
		})
	}
	// A modified /UE (one hex digit of the file changed) unlocks a wrong
	// file key, which /Perms catches: tampered. An unsupported revision is
	// unsupported_handler.
	ownerOnly := encryptedFixture(t, "qpdf_r6_owner_only.pdf")
	i := bytes.Index(ownerOnly, []byte("/UE <")) + len("/UE <")
	tampered := append([]byte(nil), ownerOnly...)
	if tampered[i] == '0' {
		tampered[i] = '1'
	} else {
		tampered[i] = '0'
	}
	if _, err := OpenBytes(tampered); reasonOf(t, err) != EncryptionTampered {
		t.Errorf("modified /UE: %v, want tampered", err)
	}
	r7 := bytes.Replace(user, []byte("/R 6"), []byte("/R 7"), 1)
	if _, err := OpenBytes(r7); reasonOf(t, err) != EncryptionUnsupportedHandler {
		t.Errorf("/R 7: %v, want unsupported_handler", err)
	}
}

// writePaths are every operation that rebuilds the document without
// /Encrypt (aes256-decryption.md §10, Q3).
var writePaths = []struct {
	name string
	run  func(d *Document) error
}{
	{"SaveTo", func(d *Document) error { return d.SaveTo(&bytes.Buffer{}) }},
	{"Merge", func(d *Document) error { _, err := d.Merge(d); return err }},
	{"Split", func(d *Document) error { _, err := d.Split([][2]int{{0, 0}}); return err }},
	{"FlattenAnnotations", func(d *Document) error { _, err := d.FlattenAnnotations(); return err }},
	{"FlattenForm", func(d *Document) error { _, err := d.FlattenForm(); return err }},
	{"ConvertToPDFA", func(d *Document) error { _, err := d.ConvertToPDFA(); return err }},
	{"Watermark", func(d *Document) error { _, err := d.Watermark(WatermarkOptions{Text: "DRAFT"}); return err }},
	{"RedactAndFlatten", func(d *Document) error {
		d.RedactArea(0, Rect{X: 0, Y: 0, Width: 50, Height: 50})
		_, err := d.RedactAndFlatten()
		return err
	}},
}

// TestSavePolicy pins Q3: a document opened with a password the empty one
// cannot replace is never written decrypted unless AllowDecryptedSave; an
// owner-only document (the empty password opens it) is, with or without the
// owner password, as before.
func TestSavePolicy(t *testing.T) {
	user := encryptedFixture(t, "qpdf_r6_user.pdf")
	ownerOnly := encryptedFixture(t, "qpdf_r6_owner_only.pdf")
	for _, tc := range []struct {
		name    string
		data    []byte
		opts    OpenOptions
		refused bool
	}{
		{"user password", user, OpenOptions{Password: "user"}, true},
		{"owner password of a protected document", user, OpenOptions{Password: "owner"}, true},
		{"user password, AllowDecryptedSave", user, OpenOptions{Password: "user", AllowDecryptedSave: true}, false},
		{"owner-only, empty password", ownerOnly, OpenOptions{}, false},
		{"owner-only, owner password", ownerOnly, OpenOptions{Password: "owner"}, false},
	} {
		for _, wp := range writePaths {
			t.Run(tc.name+"/"+wp.name, func(t *testing.T) {
				doc, err := OpenBytesWithOptions(tc.data, tc.opts)
				if err != nil {
					t.Fatal(err)
				}
				err = wp.run(doc)
				if got := reasonOf(t, err); tc.refused != (got == EncryptionSaveWouldDecrypt) {
					t.Fatalf("err = %v, want refused %v", err, tc.refused)
				}
				if !tc.refused && err != nil {
					t.Fatalf("err = %v", err)
				}
			})
		}
	}
	t.Run("Merge refuses a protected second document", func(t *testing.T) {
		a, _ := OpenBytes(ownerOnly)
		b, err := OpenBytesWithOptions(user, OpenOptions{Password: "user"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Merge(b); reasonOf(t, err) != EncryptionSaveWouldDecrypt {
			t.Errorf("Merge = %v, want save_would_decrypt", err)
		}
	})
	t.Run("Save refuses before creating the file", func(t *testing.T) {
		doc, err := OpenBytesWithOptions(user, OpenOptions{Password: "user"})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "out.pdf")
		if err := doc.Save(path); reasonOf(t, err) != EncryptionSaveWouldDecrypt {
			t.Fatalf("Save = %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("Save created %s: %v", path, err)
		}
	})
}

// TestSignDocument_RefusesEncrypted pins F1: signing an encrypted source
// would append an unencrypted update; it is refused.
func TestSignDocument_RefusesEncrypted(t *testing.T) {
	certPEM, keyPEM := testSigner(t)
	for _, f := range []string{"qpdf_r6_owner_only.pdf", "r3_128_empty.pdf"} {
		doc, err := OpenBytes(encryptedFixture(t, f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.SignDocument(SignatureOptions{CertPEM: certPEM, KeyPEM: keyPEM}); reasonOf(t, err) != EncryptionSignUnsupported {
			t.Errorf("%s: SignDocument = %v, want sign_unsupported", f, err)
		}
	}
}

// TestRedactionReport_SourceEncrypted: a redaction of an encrypted source
// says so, and its output is plaintext without the removed text.
func TestRedactionReport_SourceEncrypted(t *testing.T) {
	doc, err := OpenBytes(encryptedFixture(t, "qpdf_r6_owner_only.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RedactText(0, "AES256-OK"); err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	rep, ok := out.RedactionReport()
	if !ok || !rep.SourceEncrypted || out.Encrypted {
		t.Errorf("report %+v (%v), output Encrypted %v", rep, ok, out.Encrypted)
	}
	text, _ := out.ExtractText(ExtractOptions{})
	b, _ := out.Bytes()
	if strings.Contains(text, "AES256-OK") || bytes.Contains(b, []byte("AES256-OK")) {
		t.Error("redacted output still holds the text")
	}
	// A plaintext source is not reported as encrypted.
	plain := renderTestDoc(t, 1)
	plain.RedactArea(0, Rect{X: 0, Y: 0, Width: 10, Height: 10})
	out, err = plain.RedactAndFlatten()
	if err != nil {
		t.Fatal(err)
	}
	if rep, _ := out.RedactionReport(); rep.SourceEncrypted {
		t.Error("SourceEncrypted set for a plaintext source")
	}
}

// TestDoS_RepairLadderDerivesKeyOnce gates the shared key derivation of the
// repair ladder (parse.DecryptionCache): an AES-256 file whose strict open
// decrypts and then fails (its catalog offset is wrong), and which the object
// scan then repairs, derives the file key once, not once per rung. The bound
// is relative to a measured baseline, the clean file's open, so it holds
// under -race, js/wasm and on slow CI runners: the key derivation dominates
// both opens, and deriving it again per rung would make the damaged open
// cost about twice the clean one; the gate allows 1.6×.
func TestDoS_RepairLadderDerivesKeyOnce(t *testing.T) {
	clean := encryptedFixture(t, "qpdf_r6_owner_only.pdf")
	i := bytes.Index(clean, []byte("\nxref\n"))
	j := bytes.Index(clean[i:], []byte("00000 n")) + i // object 1's entry
	damaged := append([]byte(nil), clean...)
	// The catalog's entry now holds object 2's offset (the /Info dictionary
	// here): the strict open decrypts, then finds no page tree.
	copy(damaged[j-11:j-1], damaged[j+9:j+19])
	doc, err := OpenBytes(damaged)
	if err != nil {
		t.Fatal(err)
	}
	if rep, ok := doc.RepairReport(); !ok || rep.Method != RepairObjectScan {
		t.Fatalf("RepairReport = %+v, %v; want an object-scan repair (the ladder ran)", rep, ok)
	}
	if text, _ := doc.ExtractText(ExtractOptions{}); !strings.Contains(text, "AES256-OK-r6-owner-only") {
		t.Fatalf("repaired text = %q", text)
	}
	// The owner password makes each derivation 4 hashes (a failed user
	// test, the owner test, the key, the empty-password check), so the key
	// derivation dominates both opens; the two are measured interleaved, the
	// fastest of each kept, so machine load affects both alike.
	opts := OpenOptions{Password: "owner"}
	once := func(data []byte) time.Duration {
		start := time.Now()
		if _, err := OpenBytesWithOptions(data, opts); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	base, ladder := time.Duration(1<<63-1), time.Duration(1<<63-1)
	for k := 0; k < 11; k++ {
		base = min(base, once(clean))
		ladder = min(ladder, once(damaged))
	}
	t.Logf("clean open %v, repaired open %v (%.2f×)", base, ladder, float64(ladder)/float64(base))
	if ladder > base*16/10 {
		t.Errorf("repaired open %v > 1.6 × the clean open %v: the key is derived per rung", ladder, base)
	}
}

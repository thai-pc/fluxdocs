package parse_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// TestOpenBytes_StandardSecurityPasswords opens synthetic R2–R4 documents built
// from the ISO 32000-1 algorithms (parse.StdSecurity, independent of
// crypt.go). An empty user password opens and decrypts; a document that needs
// a non-empty user password, or whose /U is too short to check, fails with
// core.ErrEncryptedDocument instead of opening as garbage.
func TestOpenBytes_StandardSecurityPasswords(t *testing.T) {
	const secret = "PASSWORD-CHECK-OK"
	plain := "BT /F1 12 Tf 20 100 Td (" + secret + ") Tj ET"
	id := []byte("fluxdocs-test-id")
	variants := []struct {
		name string
		v, r int
		kl   int
		cfm  parse.Name
	}{
		{"R2 RC4", 1, 2, 5, ""},
		{"R3 RC4", 2, 3, 16, ""},
		{"R4 RC4", 4, 4, 16, "V2"},
		{"R4 AESV2", 4, 4, 16, "AESV2"},
	}
	for _, vr := range variants {
		for _, tc := range []struct {
			name    string
			user    string
			shortU  bool
			wantErr error
		}{
			{"empty user password", "", false, nil},
			{"non-empty user password", "hunter2", false, core.ErrEncryptedDocument},
			{"short /U", "", true, core.ErrEncryptedDocument},
		} {
			t.Run(vr.name+"/"+tc.name, func(t *testing.T) {
				sec := parse.StdSecurity{R: vr.r, KeyLen: vr.kl, User: []byte(tc.user), Owner: []byte("owner"), P: -44, ID: id, EncryptMetadata: true}
				var u []byte
				if tc.shortU {
					u = []byte{1, 2, 3}
				}
				doc, err := core.OpenBytes(parse.EncryptedPDF(sec, vr.v, vr.cfm, plain, u))
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("OpenBytes err = %v, want %v", err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("OpenBytes: %v", err)
				}
				text, err := doc.ExtractText(core.ExtractOptions{})
				if err != nil {
					t.Fatalf("ExtractText: %v", err)
				}
				if !strings.Contains(text, secret) {
					t.Errorf("decrypted text = %q, want it to contain %q", text, secret)
				}
			})
		}
	}
}

// TestOpenBytes_GhostscriptFixtures opens the third-party encrypted fixtures
// in testing/corpus/encrypted (written by Ghostscript's pdfwrite, not by
// FluxDocs; see the README there): R2 40-bit, R3 40-bit and R3 128-bit RC4,
// each with an empty user password and with user password "user". The first
// kind must open and extract the exact text; the second must fail with
// core.ErrEncryptedDocument, since OpenBytes tries only the empty password.
func TestOpenBytes_GhostscriptFixtures(t *testing.T) {
	const text = "Encrypted fixture text"
	dir := filepath.Join("..", "..", "testing", "corpus", "encrypted")
	for _, tc := range []struct {
		file    string
		wantErr error
	}{
		{"r2_40_empty.pdf", nil},
		{"r2_40_user.pdf", core.ErrEncryptedDocument},
		{"r3_40_empty.pdf", nil},
		{"r3_40_user.pdf", core.ErrEncryptedDocument},
		{"r3_128_empty.pdf", nil},
		{"r3_128_user.pdf", core.ErrEncryptedDocument},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := core.OpenBytes(data)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("OpenBytes err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenBytes: %v", err)
			}
			got, err := doc.ExtractText(core.ExtractOptions{})
			if err != nil {
				t.Fatalf("ExtractText: %v", err)
			}
			if strings.TrimSpace(got) != text {
				t.Errorf("ExtractText = %q, want %q", got, text)
			}
		})
	}
}

// TestOpenBytes_GhostscriptFixturesWithPassword: the *_user fixtures, which
// the empty password cannot open (password_required), open with the user
// password "user" and with the owner password "owner" (Algorithm 7), and a
// wrong one is wrong_password.
func TestOpenBytes_GhostscriptFixturesWithPassword(t *testing.T) {
	const text = "Encrypted fixture text"
	dir := filepath.Join("..", "..", "testing", "corpus", "encrypted")
	for _, file := range []string{"r2_40_user.pdf", "r3_40_user.pdf", "r3_128_user.pdf"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		_, err = core.OpenBytes(data)
		if reason := encReason(err); reason != core.EncryptionPasswordRequired {
			t.Errorf("%s: no password: %v, want password_required", file, err)
		}
		for _, pw := range []string{"user", "owner"} {
			doc, err := core.OpenBytesWithOptions(data, core.OpenOptions{Password: pw})
			if err != nil {
				t.Fatalf("%s: password %q: %v", file, pw, err)
			}
			got, err := doc.ExtractText(core.ExtractOptions{})
			if err != nil || strings.TrimSpace(got) != text || !doc.Encrypted {
				t.Errorf("%s: password %q: text %q, %v, Encrypted %v", file, pw, got, err, doc.Encrypted)
			}
		}
		if _, err := core.OpenBytesWithOptions(data, core.OpenOptions{Password: "nope"}); encReason(err) != core.EncryptionWrongPassword {
			t.Errorf("%s: wrong password: %v, want wrong_password", file, err)
		}
	}
}

// encReason returns the reason of the *core.EncryptionError err carries, or
// "" when it carries none.
func encReason(err error) core.EncryptionReason {
	var ee *core.EncryptionError
	if errors.As(err, &ee) && errors.Is(err, core.ErrEncryptedDocument) {
		return ee.Reason
	}
	return ""
}

// aes256Fixture is one row of testing/corpus/encrypted/MANIFEST.tsv.
type aes256Fixture struct {
	file, producer, marker, user, owner, sha string
	r                                        int
}

// aes256Fixtures reads MANIFEST.tsv (testing/tools/aes256vectors/generate.sh).
func aes256Fixtures(t *testing.T) (string, []aes256Fixture) {
	t.Helper()
	dir := filepath.Join("..", "..", "testing", "corpus", "encrypted")
	data, err := os.ReadFile(filepath.Join(dir, "MANIFEST.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var out []aes256Fixture
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 11 {
			t.Fatalf("MANIFEST.tsv: %d fields in %q", len(f), line)
		}
		user, err1 := hex.DecodeString(f[3])
		owner, err2 := hex.DecodeString(f[4])
		r, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("MANIFEST.tsv: bad row %q", line)
		}
		out = append(out, aes256Fixture{file: f[0], producer: f[1], r: r, user: string(user), owner: string(owner), marker: f[5], sha: f[10]})
	}
	if len(out) < 10 {
		t.Fatalf("MANIFEST.tsv: %d fixtures", len(out))
	}
	return dir, out
}

// TestOpenBytes_AES256Fixtures opens every AES-256 fixture of
// testing/corpus/encrypted — written by qpdf or by the test-only encryptor,
// and read by Ghostscript (and qpdf) before it was committed (MANIFEST.tsv)
// — with no password, its user password, its owner password and a wrong one,
// and checks the marker, the reasons, and the save policy: a document opened
// with a password the empty one cannot replace saves only with
// AllowDecryptedSave, as plaintext.
func TestOpenBytes_AES256Fixtures(t *testing.T) {
	dir, fixtures := aes256Fixtures(t)
	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, fx.file))
			if err != nil {
				t.Fatal(err)
			}
			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != fx.sha {
				t.Fatalf("SHA-256 differs from MANIFEST.tsv")
			}
			text := func(doc *core.Document) string {
				s, err := doc.ExtractText(core.ExtractOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			doc, err := core.OpenBytes(data)
			if fx.user == "" {
				if err != nil || !strings.Contains(text(doc), fx.marker) || !doc.Encrypted {
					t.Fatalf("no password: %v", err)
				}
				var buf bytes.Buffer
				if err := doc.SaveTo(&buf); err != nil {
					t.Errorf("SaveTo of a document the empty password opens: %v", err)
				}
			} else if encReason(err) != core.EncryptionPasswordRequired {
				t.Fatalf("no password: %v, want password_required", err)
			}
			for _, pw := range []string{fx.user, fx.owner} {
				doc, err := core.OpenBytesWithOptions(data, core.OpenOptions{Password: pw})
				if err != nil || !strings.Contains(text(doc), fx.marker) {
					t.Fatalf("password %q: %v", pw, err)
				}
				var buf bytes.Buffer
				err = doc.SaveTo(&buf)
				if fx.user != "" && encReason(err) != core.EncryptionSaveWouldDecrypt {
					t.Errorf("password %q: SaveTo = %v, want save_would_decrypt", pw, err)
				}
				if fx.user == "" && err != nil {
					t.Errorf("owner password of a document the empty password opens: SaveTo = %v", err)
				}
			}
			doc, err = core.OpenBytesWithOptions(data, core.OpenOptions{Password: fx.owner, AllowDecryptedSave: true})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := doc.SaveTo(&buf); err != nil {
				t.Fatalf("SaveTo with AllowDecryptedSave: %v", err)
			}
			plain, err := core.OpenBytes(buf.Bytes())
			if err != nil || plain.Encrypted || !strings.Contains(text(plain), fx.marker) {
				t.Errorf("saved copy: %v, Encrypted %v", err, plain != nil && plain.Encrypted)
			}
			if bytes.Contains(buf.Bytes(), []byte("/Encrypt")) {
				t.Error("saved copy names /Encrypt")
			}
			if _, err := core.OpenBytesWithOptions(data, core.OpenOptions{Password: "not-the-password"}); encReason(err) != core.EncryptionWrongPassword {
				t.Errorf("wrong password: %v, want wrong_password", err)
			}
		})
	}
}

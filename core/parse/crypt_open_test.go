package parse_test

import (
	"errors"
	"os"
	"path/filepath"
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

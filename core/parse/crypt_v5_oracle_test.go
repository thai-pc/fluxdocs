//go:build !js

package parse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This file is the external-reader oracle of the test-only encryptor
// (docs/design/aes256-decryption.md §11.1 option 3): documents it writes are
// read by Ghostscript (txtwrite with -sPDFPassword) and by qpdf (--check,
// then decrypting with the password), separate programs, which must find the
// marker. If the encryptor and crypt_v5.go shared a misreading of ISO
// 32000-2, round trips alone would pass; these readers would not. Each tool
// skips when it is not installed, and fails then under FLUXDOCS_REQUIRE_GS=1
// / FLUXDOCS_REQUIRE_QPDF=1. Ghostscript (AGPL) and qpdf (Apache-2.0) run
// only as separate processes: nothing links them, and their source is never
// read. exec is unavailable under js/wasm, hence the build tag.

// oracleTool returns the path of tool, or skips (fails, when env is "1").
func oracleTool(t *testing.T, tool, env string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		if os.Getenv(env) == "1" {
			t.Fatalf("%s not found and %s=1", tool, env)
		}
		t.Skipf("%s not installed", tool)
	}
	return p
}

func runTool(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// qpdfOK reports whether a qpdf run succeeded: exit status 0, or 3 (it
// completed with warnings, per qpdf's documented exit codes).
func qpdfOK(err error) bool {
	var ee *exec.ExitError
	return err == nil || (errors.As(err, &ee) && ee.ExitCode() == 3)
}

// oracleDocs are the encryptor documents both readers must open: each with
// the passwords to try, all of which must work.
func oracleDocs() []struct {
	name      string
	doc       V5Doc
	passwords []string
	gsSkip    map[string]bool // passwords Ghostscript is known not to accept (with a reproduction)
} {
	page := func(m string, meta bool) map[int]Object {
		return V5OnePage(m, meta, "<x:xmpmeta>"+m+"-META</x:xmpmeta>")
	}
	other := int32(-1)
	return []struct {
		name      string
		doc       V5Doc
		passwords []string
		gsSkip    map[string]bool
	}{
		{"r6 user", V5Doc{Sec: AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -3904, EncryptMetadata: true, Seed: "o1"},
			Objects: page("AES256-ORACLE-r6-user", false), Root: 1, Info: 6}, []string{"user", "owner"}, nil},
		{"r6 empty user", V5Doc{Sec: AES256Security{R: 6, Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o2"},
			Objects: page("AES256-ORACLE-r6-empty", false), Root: 1, Info: 6}, []string{"", "owner"}, nil},
		{"r6 long 2.B chain (107 rounds)", V5Doc{Sec: AES256Security{R: 6, Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o3", UserSalt: longChainSalt},
			Objects: page("AES256-ORACLE-r6-long-chain", false), Root: 1, Info: 6}, []string{""}, nil},
		{"r6 127-byte password", V5Doc{Sec: AES256Security{R: 6, User: bytes.Repeat([]byte("k"), 127), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o4"},
			Objects: page("AES256-ORACLE-r6-127", false), Root: 1, Info: 6}, []string{string(bytes.Repeat([]byte("k"), 127))}, nil},
		{"r6 UTF-8 password", V5Doc{Sec: AES256Security{R: 6, User: []byte("pässwörd"), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o5"},
			Objects: page("AES256-ORACLE-r6-utf8", false), Root: 1, Info: 6}, []string{"pässwörd"}, nil},
		{"r6 object stream, cleartext metadata", V5Doc{Sec: AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -4, EncryptMetadata: false, Seed: "o6"},
			Objects: page("AES256-ORACLE-r6-objstm", true), Root: 1, Info: 6, InObjStm: []int{1, 2, 3, 5, 6}}, []string{"user"}, nil},
		// Option B (design §5): a /P that differs from /Perms is tolerated.
		// Both external readers agree (qpdf warns, exit status 3, and
		// decrypts). An earlier failure of this row was the 2.B stop-test bug
		// in the encryptor (design §14), not a reader's strictness.
		{"r6 /P differs from /Perms", V5Doc{Sec: AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -4, PermsP: &other, EncryptMetadata: true, Seed: "o7"},
			Objects: page("AES256-ORACLE-r6-p-mismatch", false), Root: 1, Info: 6}, []string{"user"}, nil},
		{"r5 user", V5Doc{Sec: AES256Security{R: 5, User: []byte("user"), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o8"},
			Objects: page("AES256-ORACLE-r5-user", false), Root: 1, Info: 6}, []string{"user", "owner"}, nil},
		// Ghostscript 10.07.1 does not open R5 with the empty password (it
		// does with the owner's); qpdf does. The one skip, and the tool is
		// at fault: it is reproduced on qpdf's own R5 output,
		// testing/corpus/encrypted/qpdf_r5_owner_only.pdf, whose empty user
		// password qpdf reports and accepts (MANIFEST.tsv, README).
		{"r5 empty user", V5Doc{Sec: AES256Security{R: 5, Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "o9"},
			Objects: page("AES256-ORACLE-r5-empty", false), Root: 1, Info: 6}, []string{"", "owner"}, map[string]bool{"": true}},
	}
}

func TestAES256Encryptor_GhostscriptOracle(t *testing.T) {
	gs := oracleTool(t, "gs", "FLUXDOCS_REQUIRE_GS")
	for _, tc := range oracleDocs() {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "in.pdf")
			if err := os.WriteFile(path, tc.doc.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := []byte(tc.doc.Objects[4].(*Stream).Raw[len("BT /F1 18 Tf 20 100 Td ("):])
			marker = marker[:bytes.IndexByte(marker, ')')]
			for _, pw := range tc.passwords {
				if tc.gsSkip[pw] {
					continue
				}
				out, err := runTool(t, gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=txtwrite",
					"-sOutputFile=-", "-sPDFPassword="+pw, path)
				if err != nil || !bytes.Contains(bytes.ReplaceAll(out, []byte{0}, nil), marker) {
					t.Errorf("Ghostscript with password %q: %v; output %q, want %q", pw, err, out, marker)
				}
			}
			for _, pw := range tc.passwords {
				r, err := NewResolver(tc.doc.Bytes())
				if err == nil {
					err = r.EnableDecryptionPassword(pw)
				}
				if err != nil {
					t.Errorf("FluxDocs with password %q: %v", pw, err)
				}
			}
		})
	}
}

func TestAES256Encryptor_QpdfOracle(t *testing.T) {
	qpdf := oracleTool(t, "qpdf", "FLUXDOCS_REQUIRE_QPDF")
	for _, tc := range oracleDocs() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "in.pdf")
			if err := os.WriteFile(path, tc.doc.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := []byte(tc.doc.Objects[4].(*Stream).Raw)
			for _, pw := range tc.passwords {
				if out, err := runTool(t, qpdf, "--check", "--password="+pw, path); !qpdfOK(err) {
					t.Errorf("qpdf --check with password %q: %v; %s", pw, err, out)
				}
				dec := filepath.Join(dir, "dec.pdf")
				if _, err := runTool(t, qpdf, "--qdf", "--object-streams=disable", "--password="+pw, "--decrypt", path, dec); !qpdfOK(err) {
					t.Errorf("qpdf --decrypt with password %q: %v", pw, err)
					continue
				}
				got, _ := os.ReadFile(dec)
				if !bytes.Contains(got, marker) {
					t.Errorf("qpdf --decrypt with password %q: the content stream is not in the output", pw)
				}
			}
		})
	}
}

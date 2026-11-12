package parse

import (
	"os"
	"path/filepath"
	"testing"
)

// encryptorFixtures are the AES-256 documents the test-only encryptor writes
// for testing/corpus/encrypted (design §11.1 option 3): what qpdf's command
// line cannot produce — an R5 document with an empty user password, and an
// R6 one with object streams, a cross-reference stream, cleartext metadata
// and a /Crypt /Identity stream at once. testing/tools/aes256vectors/
// generate.sh writes them with TestWriteAES256Fixtures, then has
// Ghostscript (and qpdf, when installed) read each one before it is
// committed.
var encryptorFixtures = []struct {
	file string
	doc  func() V5Doc
}{
	{"enc_r5_empty_user.pdf", func() V5Doc {
		sec := AES256Security{R: 5, Owner: []byte("owner"), P: -3904, EncryptMetadata: true, Seed: "enc_r5_empty_user"}
		return V5Doc{Sec: sec, Objects: V5OnePage("AES256-OK-enc-r5-empty-user", false, ""), Root: 1, Info: 6}
	}},
	{"enc_r6_objstm_meta.pdf", func() V5Doc {
		sec := AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -3904, EncryptMetadata: false, Seed: "enc_r6_objstm_meta"}
		objs := V5OnePage("AES256-OK-enc-r6-objstm-meta", true, "<x:xmpmeta>AES256-OK-enc-r6-objstm-meta-META</x:xmpmeta>")
		objs[8] = &Stream{Dict: Dict{"Filter": Name("Crypt"), "DecodeParms": Dict{"Name": Name("Identity")}}, Raw: []byte("identity crypt filter data")}
		return V5Doc{Sec: sec, Objects: objs, Root: 1, Info: 6, InObjStm: []int{1, 2, 3, 5, 6}}
	}},
}

// TestWriteAES256Fixtures writes encryptorFixtures into the directory
// FLUXDOCS_AES256_FIXTURES names; it skips without it. The output is
// deterministic (seeded salts, key and IVs).
func TestWriteAES256Fixtures(t *testing.T) {
	dir := os.Getenv("FLUXDOCS_AES256_FIXTURES")
	if dir == "" {
		t.Skip("FLUXDOCS_AES256_FIXTURES not set")
	}
	for _, f := range encryptorFixtures {
		if err := os.WriteFile(filepath.Join(dir, f.file), f.doc().Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEncryptorFixturesOpen: every encryptor fixture opens with its
// passwords through the decryptor (the committed copies are checked by
// crypt_open_test.go).
func TestEncryptorFixturesOpen(t *testing.T) {
	for _, f := range encryptorFixtures {
		t.Run(f.file, func(t *testing.T) {
			doc := f.doc()
			for _, pw := range []string{string(doc.Sec.User), "owner"} {
				r, err := NewResolver(doc.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				if err := r.EnableDecryptionPassword(pw); err != nil {
					t.Fatalf("password %q: %v", pw, err)
				}
				if _, err := r.Pages(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

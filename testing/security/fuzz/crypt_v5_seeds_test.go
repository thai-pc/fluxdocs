package fuzz

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// cryptV5Fixtures are AES-256 documents of testing/corpus/encrypted (written
// by qpdf and by the test-only encryptor, read by Ghostscript before they
// were committed): R6 with an empty user password, R6 with an object stream,
// and R5.
var cryptV5Fixtures = []string{"qpdf_r6_owner_only.pdf", "enc_r6_objstm_meta.pdf", "enc_r5_empty_user.pdf", "qpdf_r6_user.pdf"}

// cryptV5Mutations rewrite one field of a V5 encryption dictionary
// (docs/design/aes256-decryption.md §11.3): each string field cut short, the
// revision and version crossed, an unknown crypt filter method, /StmF naming
// a missing filter, a huge /Length, /Perms dropped, and /P outside int32.
var cryptV5Mutations = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`/O <[0-9A-Fa-f]+>`), "/O <00>"},
	{regexp.MustCompile(`/U <[0-9A-Fa-f]+>`), "/U <0011223344>"},
	{regexp.MustCompile(`/OE <[0-9A-Fa-f]+>`), "/OE <>"},
	{regexp.MustCompile(`/UE <[0-9A-Fa-f]+>`), "/UE <00>"},
	{regexp.MustCompile(`/Perms <[0-9A-Fa-f]+>`), "/Perms <0102>"},
	{regexp.MustCompile(`/Perms <`), "/Permz <"},
	{regexp.MustCompile(`/R 6`), "/R 4"},
	{regexp.MustCompile(`/R [56]`), "/R 9"},
	{regexp.MustCompile(`/V 5`), "/V 4"},
	{regexp.MustCompile(`/CFM /AESV3`), "/CFM /AESV9"},
	{regexp.MustCompile(`/StmF /StdCF`), "/StmF /Nope"},
	{regexp.MustCompile(`/StrF /StdCF`), "/StrF /Nope"},
	{regexp.MustCompile(`/Filter /Standard`), "/Filter /Standard /Length 4611686018427387904"},
	{regexp.MustCompile(`/P -[0-9]+`), "/P 1099511627776"},
}

// cryptV5SeedBytes returns the fixtures and every mutation of each that
// changes it (a mutation keeps the byte length when it can; offsets that move
// exercise the repair path too).
func cryptV5SeedBytes() [][]byte {
	var out [][]byte
	for _, name := range cryptV5Fixtures {
		data, err := os.ReadFile(filepath.Join("..", "..", "corpus", "encrypted", name))
		if err != nil {
			continue // the seeds are a corpus, not a test of the fixtures
		}
		out = append(out, data)
		for _, m := range cryptV5Mutations {
			if mut := m.re.ReplaceAll(data, []byte(m.with)); !bytes.Equal(mut, data) {
				out = append(out, mut)
			}
		}
	}
	return out
}

// TestCryptV5SeedsHandled pins the seeds' contract beyond "no panic": every
// mutated V5 dictionary either opens (a mutation the handler ignores: /P,
// /Length) or fails as encrypted — never as a recovered panic, and never by
// opening ciphertext as plaintext.
func TestCryptV5SeedsHandled(t *testing.T) {
	seeds := cryptV5SeedBytes()
	if len(seeds) < 2*len(cryptV5Mutations) {
		t.Fatalf("%d seeds: the fixtures in testing/corpus/encrypted are missing", len(seeds))
	}
	for i, s := range seeds {
		doc, err := core.OpenBytes(s)
		failOnInternal(t, err)
		if err != nil {
			if !errors.Is(err, core.ErrEncryptedDocument) && !errors.Is(err, core.ErrInvalidPDF) {
				t.Errorf("seed %d: %v, want an encrypted or invalid PDF error", i, err)
			}
			continue
		}
		if !doc.Encrypted {
			t.Errorf("seed %d opened as unencrypted", i)
		}
		exercise(t, s)
	}
}

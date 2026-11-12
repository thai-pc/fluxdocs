package parse

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fileV5Strings returns the /U, /O, /UE and /OE strings of the AES-256
// encryption dictionary in a qpdf-written file (hex strings).
func fileV5Strings(t *testing.T, data []byte) (u, o, ue, oe []byte) {
	t.Helper()
	get := func(name string) []byte {
		m := regexp.MustCompile(`/` + name + ` <([0-9A-Fa-f]+)>`).FindSubmatch(data)
		if m == nil {
			t.Fatalf("/%s not found", name)
		}
		b, err := hex.DecodeString(string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return get("U"), get("O"), get("UE"), get("OE")
}

// separates reports whether opening data with the user password pw takes a
// 2.B chain on which the two readings of the stop test disagree: under the
// off-by-one reading the user validation hash, or the user key hash, comes
// out different (and the file fails to open), under the correct one it
// matches /U.
func separates(t *testing.T, data []byte, pw string) (correctOK, offByOneDiffers bool) {
	t.Helper()
	u, _, _, _ := fileV5Strings(t, data)
	good, _ := isoHash2BOff(6, []byte(pw), u[32:40], nil, 0)
	bad, _ := isoHash2BOff(6, []byte(pw), u[32:40], nil, 1)
	kgood, _ := isoHash2BOff(6, []byte(pw), u[40:48], nil, 0)
	kbad, _ := isoHash2BOff(6, []byte(pw), u[40:48], nil, 1)
	correctOK = subtle.ConstantTimeCompare(good, u[:32]) == 1
	return correctOK, !bytes.Equal(good, bad) || !bytes.Equal(kgood, kbad)
}

// TestQpdfFixturesSeparateStopReadings pins the regression fixtures of the
// Algorithm 2.B stop test (hash2BStop): qpdf wrote them, Ghostscript and
// qpdf read them (MANIFEST.tsv), and each opens through a chain on which
// the off-by-one reading derives a different hash — so a decryptor with that
// bug cannot open them. TestOpenBytes_AES256Fixtures opens them.
func TestQpdfFixturesSeparateStopReadings(t *testing.T) {
	dir := filepath.Join("..", "..", "testing", "corpus", "encrypted")
	for _, tc := range []struct{ file, pw string }{
		{"qpdf_r6_chain_user.pdf", "user"},
		{"qpdf_r6_chain_owner_only.pdf", ""},
	} {
		data, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		ok, differs := separates(t, data, tc.pw)
		if !ok || !differs {
			t.Errorf("%s: correct reading matches /U: %v; off-by-one reading differs: %v", tc.file, ok, differs)
		}
	}
}

// TestSeparatesCheck is generate.sh's selection step: with
// FLUXDOCS_SEPARATES=<file>:<user password> it passes only when the file
// separates the readings; it skips otherwise.
func TestSeparatesCheck(t *testing.T) {
	arg := os.Getenv("FLUXDOCS_SEPARATES")
	if arg == "" {
		t.Skip("FLUXDOCS_SEPARATES not set")
	}
	file, pw, _ := strings.Cut(arg, ":")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if ok, differs := separates(t, data, pw); !ok || !differs {
		t.Fatalf("does not separate (correct reading matches: %v, differs: %v)", ok, differs)
	}
}

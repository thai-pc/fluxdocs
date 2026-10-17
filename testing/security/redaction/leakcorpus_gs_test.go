//go:build !js

package redaction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This file is the Ghostscript differential oracle of the leak corpus
// (docs/design/redaction-hardening.md §12.3). Ghostscript is AGPL; it runs as
// a separate process in tests only, so nothing is linked or shipped and the
// MIT build is unaffected. exec is unavailable under js/wasm, where
// leakcorpus_gs_js_test.go reports the oracle as absent.

// gsPath is the Ghostscript binary, or "" when it is not installed.
var gsPath, _ = exec.LookPath("gs")

// gsText returns Ghostscript's txtwrite text of pdf, and false when gs is not
// installed. A gs failure fails the test: an oracle that cannot read the file
// proves nothing.
func gsText(t *testing.T, pdf []byte) (string, bool) {
	t.Helper()
	if gsPath == "" {
		return "", false
	}
	path := filepath.Join(t.TempDir(), "in.pdf")
	if err := os.WriteFile(path, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gsPath, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER",
		"-sDEVICE=txtwrite", "-sOutputFile=-", path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gs txtwrite: %v", err)
	}
	// Some Ghostscript versions (10.02) write text from CID fonts as UTF-16,
	// one NUL before each ASCII character. Dropping NULs only ever makes a
	// secret easier to find, so it cannot hide a leak.
	return string(bytes.ReplaceAll(out, []byte{0}, nil)), true
}

// gsRender returns a hash of Ghostscript's 36 DPI rendering of each page of
// pdf, and false when gs is not installed.
func gsRender(t *testing.T, pdf []byte) ([]string, bool) {
	t.Helper()
	if gsPath == "" {
		return nil, false
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(path, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gsPath, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-r36",
		"-sDEVICE=png16m", "-sOutputFile="+filepath.Join(dir, "p%03d.png"), path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gs png16m: %v: %s", err, out)
	}
	var hashes []string
	for i := 1; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("p%03d.png", i)))
		if err != nil {
			break
		}
		hashes = append(hashes, fmt.Sprintf("%x", sha256.Sum256(b)))
	}
	return hashes, true
}

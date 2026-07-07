package core

import (
	"os"
	"strings"
	"testing"
)

// TestOpenEncrypted_RealRC4 opens a genuinely-encrypted PDF (Standard handler,
// V2/R3, RC4-128, empty user password) produced by Ghostscript — an independent
// oracle — and confirms the text decrypts. This proves the Standard Security
// Handler works against real-world encryption, not just our own round-trip.
func TestOpenEncrypted_RealRC4(t *testing.T) {
	data, err := os.ReadFile("testdata/encrypted_rc4.pdf")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := OpenBytes(data)
	if err != nil {
		t.Fatalf("OpenBytes on encrypted PDF: %v (decryption should have opened it)", err)
	}
	if doc.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", doc.PageCount)
	}
	text, err := doc.ExtractText(ExtractOptions{})
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if !strings.Contains(text, "DECRYPT-OK-SECRET-42") {
		t.Errorf("decrypted text = %q, want it to contain the secret", text)
	}
}

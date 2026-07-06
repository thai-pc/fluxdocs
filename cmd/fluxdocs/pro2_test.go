package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

func writeSigner(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9),
		Subject:      pkix.Name{CommonName: "CLI Test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o644)
	return
}

func TestSign(t *testing.T) {
	path := writePDF(t, 1)
	certPath, keyPath := writeSigner(t)
	out := filepath.Join(t.TempDir(), "signed.pdf")

	code, stdout, errs := runCLI("sign", path, "--cert", certPath, "--key", keyPath, "--reason", "OK", "-o", out)
	if code != 0 {
		t.Fatalf("sign: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "wrote") {
		t.Errorf("stdout=%q", stdout)
	}
	// The written file preserves the original bytes (incremental update) and the
	// signature field is present — proving writeExact did not rebuild.
	orig, _ := os.ReadFile(path)
	signed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(signed) <= len(orig) || string(signed[:len(orig)]) != string(orig) {
		t.Errorf("signed file did not append to the original bytes (was rebuilt?)")
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := doc.GetFormFields()
	hasSig := false
	for _, f := range fields {
		if f.Type == core.FieldSignature {
			hasSig = true
		}
	}
	if !hasSig {
		t.Errorf("signed file has no signature field: %+v", fields)
	}
}

func TestSign_RequiresCertKey(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, _ := runCLI("sign", path); code != 1 {
		t.Errorf("sign without --cert/--key: code=%d, want 1", code)
	}
}

func TestCompare(t *testing.T) {
	// Same fixture twice -> identical.
	a := writePDF(t, 2)
	code, stdout, _ := runCLI("compare", a, a)
	if code != 0 || !strings.Contains(stdout, "identical") {
		t.Errorf("compare identical: code=%d stdout=%q", code, stdout)
	}

	// Different content -> reports a diff.
	b := filepath.Join(t.TempDir(), "b.pdf")
	os.WriteFile(b, samplePDF(2), 0o644) // samplePDF pages say "Page N"
	// a (writePDF) also uses samplePDF, so a and b are the same here; make b differ.
	// Build a distinct doc via the core to be sure.
	doc := docText(t, "totally different")
	os.WriteFile(b, doc, 0o644)
	code, stdout, _ = runCLI("compare", a, b)
	if code != 0 || strings.Contains(stdout, "identical") {
		t.Errorf("compare different: code=%d stdout=%q", code, stdout)
	}
}

func TestCompare_NeedsTwo(t *testing.T) {
	a := writePDF(t, 1)
	if code, _, _ := runCLI("compare", a); code != 1 {
		t.Errorf("compare with one file: code=%d, want 1", code)
	}
}

func TestPDFA_ConvertAndValidate(t *testing.T) {
	path := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "archive.pdf")

	// Convert.
	if code, _, errs := runCLI("pdfa", path, "-o", out); code != 0 {
		t.Fatalf("pdfa convert: code=%d errs=%q", code, errs)
	}
	// Validate the converted file -> conformant (exit 0, all PASS).
	code, stdout, errs := runCLI("pdfa", out, "--validate")
	if code != 0 {
		t.Fatalf("pdfa validate: code=%d errs=%q\n%s", code, errs, stdout)
	}
	if strings.Contains(stdout, "[FAIL]") {
		t.Errorf("converted file failed validation:\n%s", stdout)
	}

	// Validating the ORIGINAL (non-PDF/A) file reports failures and exits 1.
	if code, out2, _ := runCLI("pdfa", path, "--validate"); code != 1 || !strings.Contains(out2, "[FAIL]") {
		t.Errorf("plain file validate: code=%d out=%q", code, out2)
	}
}

// docText builds a one-page PDF whose page shows the given text (helper for
// compare), returning the bytes.
func docText(t *testing.T, text string) []byte {
	t.Helper()
	content := "BT /F1 12 Tf 20 100 Td (" + text + ") Tj ET"
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

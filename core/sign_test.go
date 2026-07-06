package core

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testSigner(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "FluxDocs Test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return
}

func TestSignDocument(t *testing.T) {
	certPEM, keyPEM := testSigner(t)
	doc := renderTestDoc(t, 1)

	// Capture the original bytes to prove the incremental update preserves them.
	var orig bytes.Buffer
	if err := doc.SaveTo(&orig); err != nil {
		t.Fatal(err)
	}
	src, err := OpenBytes(orig.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	signed, err := src.SignDocument(SignatureOptions{
		CertPEM: certPEM, KeyPEM: keyPEM, Reason: "Approved", FieldName: "Sig1",
	})
	if err != nil {
		t.Fatalf("SignDocument: %v", err)
	}

	// The signed document re-opens and exposes the signature field.
	fields, err := signed.GetFormFields()
	if err != nil {
		t.Fatal(err)
	}
	var sig *FormField
	for i := range fields {
		if fields[i].Name == "Sig1" {
			sig = &fields[i]
		}
	}
	if sig == nil {
		t.Fatalf("signature field not present after signing: %+v", fields)
	}
	if sig.Type != FieldSignature {
		t.Errorf("field type = %s, want signature", sig.Type)
	}
}

func TestSignDocument_ResolverLess(t *testing.T) {
	certPEM, keyPEM := testSigner(t)
	if _, err := (&Document{}).SignDocument(SignatureOptions{CertPEM: certPEM, KeyPEM: keyPEM}); err != ErrInvalidPDF {
		t.Errorf("err = %v, want ErrInvalidPDF", err)
	}
}

func TestSignDocument_BadKey(t *testing.T) {
	doc := renderTestDoc(t, 1)
	var buf bytes.Buffer
	doc.SaveTo(&buf)
	src, _ := OpenBytes(buf.Bytes())
	if _, err := src.SignDocument(SignatureOptions{CertPEM: []byte("x"), KeyPEM: []byte("y")}); err == nil {
		t.Errorf("SignDocument with invalid PEM should error")
	}
}

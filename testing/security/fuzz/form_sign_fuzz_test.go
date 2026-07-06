package fuzz

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// formPDF builds a PDF whose single AcroForm field (object 6) has a
// caller-supplied dictionary body, so a fuzzer can drive the field-tree walk
// (parent chains, kids, odd types) with arbitrary structure.
func formPDF(fieldBody string) []byte {
	var b bytes.Buffer
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< >>",
		5: "<< /Fields [6 0 R] >>",
		6: "<< " + fieldBody + " >>",
	}
	b.WriteString("%PDF-1.7\n")
	off := map[int]int{}
	for i := 1; i <= 6; i++ {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 7\n0000000000 65535 f \n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

// FuzzFormFields drives the AcroForm reader/filler with a fuzzed field
// dictionary body. Reading and filling an untrusted form must never panic (a
// cyclic /Parent, deep /Kids nesting, or wrong-typed entries are all bounded).
func FuzzFormFields(f *testing.F) {
	f.Add("/FT /Tx /T (name) /V (Alice)")
	f.Add("/FT /Btn /T (x) /Ff 32768 /Kids [6 0 R]") // self-referential kid
	f.Add("/Parent 6 0 R /FT /Ch /Opt [(a) (b)]")    // self parent
	f.Add("/T (only-a-name)")
	f.Add("")

	f.Fuzz(func(t *testing.T, fieldBody string) {
		doc, err := core.OpenBytes(formPDF(fieldBody))
		if err != nil {
			return
		}
		fields, err := doc.GetFormFields()
		if err != nil {
			return
		}
		// Filling each discovered field must also be panic-free.
		for _, fld := range fields {
			_ = doc.SetFormFieldValue(fld.Name, "fuzz")
		}
	})
}

// validSigner returns a self-signed RSA cert+key PEM for a seed that exercises
// the full signing path (not just the parse rejection).
func validSigner() (certPEM, keyPEM []byte) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Fuzz"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return
}

// FuzzSign feeds arbitrary certificate and key bytes to SignDocument over a
// fixed minimal PDF. Parsing an untrusted cert/key must never panic — garbage
// yields an error, a valid pair produces a signed document.
func FuzzSign(f *testing.F) {
	cert, key := validSigner()
	f.Add(cert, key)
	f.Add([]byte("not a pem"), []byte("also not"))
	f.Add([]byte(""), []byte(""))
	f.Add(cert, []byte("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"))

	f.Fuzz(func(t *testing.T, certPEM, keyPEM []byte) {
		doc, err := core.OpenBytes(minimalSignPDF())
		if err != nil {
			t.Skip()
		}
		out, err := doc.SignDocument(core.SignatureOptions{CertPEM: certPEM, KeyPEM: keyPEM})
		if err != nil {
			return // garbage cert/key -> graceful error, never a panic
		}
		// A successful sign must re-open.
		if _, err := out.Bytes(); err != nil {
			t.Fatalf("signed document has no bytes: %v", err)
		}
	})
}

// minimalSignPDF is a tiny valid PDF used as the signing fixture.
func minimalSignPDF() []byte {
	var b bytes.Buffer
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	}
	b.WriteString("%PDF-1.7\n")
	off := make([]int, len(objs)+1)
	for i, o := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

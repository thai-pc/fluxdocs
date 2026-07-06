package sign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// testKeyPair creates a self-signed RSA certificate and returns cert+key PEM.
func testKeyPair(t *testing.T) (certPEM, keyPEM []byte, cert *x509.Certificate, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(12345),
		Subject:      pkix.Name{CommonName: "FluxDocs Test Signer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, cert, key
}

// verifyDetachedPKCS7 parses a detached CMS SignedData and verifies the RSA
// signature over the signed attributes and that the message-digest attribute
// matches SHA256(message). It is a self-contained verifier used to prove the
// signer's output is cryptographically valid.
func verifyDetachedPKCS7(t *testing.T, p7, message []byte, pub *rsa.PublicKey) {
	t.Helper()

	var ci contentInfo
	if _, err := asn1.Unmarshal(p7, &ci); err != nil {
		t.Fatalf("parse ContentInfo: %v", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		t.Fatalf("contentType = %v, want signedData", ci.ContentType)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		t.Fatalf("parse SignedData: %v", err)
	}
	if len(sd.SignerInfos) != 1 {
		t.Fatalf("got %d signerInfos, want 1", len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]

	// The signed attributes are carried as [0] IMPLICIT; to verify, re-tag them
	// as a SET (0x31) — that is exactly what was signed.
	attrs := make([]byte, len(si.SignedAttrs.FullBytes))
	copy(attrs, si.SignedAttrs.FullBytes)
	attrs[0] = 0x31
	attrDigest := sha256.Sum256(attrs)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, attrDigest[:], si.Signature); err != nil {
		t.Fatalf("RSA signature does not verify: %v", err)
	}

	// The message-digest signed attribute must equal SHA256(message). Parse the
	// attributes as a SEQUENCE OF (flip the SET tag to SEQUENCE; the inner
	// encoding is identical).
	wantDigest := sha256.Sum256(message)
	list := parseAttrs(t, attrs)
	md := messageDigest(list)
	if md == nil {
		t.Fatalf("no message-digest attribute in the signature")
	}
	if !bytes.Equal(md, wantDigest[:]) {
		t.Errorf("message-digest attr = %x, want %x", md, wantDigest[:])
	}
}

// messageDigest returns the message-digest signed-attribute value, or nil.
func messageDigest(list []attribute) []byte {
	for _, a := range list {
		if !a.Type.Equal(oidMessageDigest) {
			continue
		}
		// a.Values is a SET OF (0x31); flip to SEQUENCE OF to read the element.
		vals := make([]byte, len(a.Values.FullBytes))
		copy(vals, a.Values.FullBytes)
		if len(vals) > 0 {
			vals[0] = 0x30
		}
		var one []asn1.RawValue
		if _, err := asn1.Unmarshal(vals, &one); err != nil || len(one) != 1 {
			return nil
		}
		var md []byte
		if _, err := asn1.Unmarshal(one[0].FullBytes, &md); err != nil {
			return nil
		}
		return md
	}
	return nil
}

func TestBuildDetachedPKCS7_Verifies(t *testing.T) {
	_, _, cert, key := testKeyPair(t)
	msg := []byte("the quick brown fox jumps over the lazy dog")
	p7, err := BuildDetachedPKCS7(msg, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	verifyDetachedPKCS7(t, p7, msg, &key.PublicKey)

	// A different message must NOT verify against this signature's attributes.
	verifyDetachedPKCS7Fails(t, p7, []byte("tampered"), &key.PublicKey)
}

// verifyDetachedPKCS7Fails asserts the message-digest does not match a wrong
// message (the RSA signature over attrs still verifies, but the digest attr
// won't match — which is how a verifier detects tampering).
func verifyDetachedPKCS7Fails(t *testing.T, p7, wrongMessage []byte, pub *rsa.PublicKey) {
	t.Helper()
	var ci contentInfo
	asn1.Unmarshal(p7, &ci)
	var sd signedData
	asn1.Unmarshal(ci.Content.Bytes, &sd)
	attrs := make([]byte, len(sd.SignerInfos[0].SignedAttrs.FullBytes))
	copy(attrs, sd.SignerInfos[0].SignedAttrs.FullBytes)
	attrs[0] = 0x31
	wrong := sha256.Sum256(wrongMessage)
	if md := messageDigest(parseAttrs(t, attrs)); bytes.Equal(md, wrong[:]) {
		t.Errorf("message-digest matched a tampered message")
	}
}

// --- integration: sign a whole PDF ---

func minimalPDF() []byte {
	var b bytes.Buffer
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	}
	off := make([]int, len(objs)+1)
	b.WriteString("%PDF-1.7\n")
	for i, o := range objs {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestSign_IncrementalUpdateVerifies(t *testing.T) {
	certPEM, keyPEM, _, key := testKeyPair(t)
	original := minimalPDF()

	r, err := parse.NewResolver(original)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Sign(r, Options{CertPEM: certPEM, KeyPEM: keyPEM, Reason: "I approve"})
	if err != nil {
		t.Fatal(err)
	}

	// The original bytes are preserved verbatim (incremental update appends only).
	if !bytes.HasPrefix(signed, original) {
		t.Fatalf("signing modified the original bytes (not a pure incremental update)")
	}

	// The signed document must still parse.
	if _, err := parse.NewResolver(signed); err != nil {
		t.Fatalf("signed PDF does not re-parse: %v", err)
	}

	// Extract /ByteRange and /Contents, reconstruct the signed message, and
	// verify the embedded PKCS#7 over it.
	br := extractByteRange(t, signed)
	p7 := extractContents(t, signed)
	message := append(append([]byte{}, signed[br[0]:br[0]+br[1]]...), signed[br[2]:br[2]+br[3]]...)
	verifyDetachedPKCS7(t, p7, message, &key.PublicKey)

	// Sanity: the /Contents gap sits exactly between the two byte-range segments.
	if br[0]+br[1] >= br[2] || br[2] < br[0] {
		t.Errorf("byte range segments overlap or are out of order: %v", br)
	}
}

// parseAttrs decodes a SET OF Attribute (0x31...) as a slice by flipping the
// outer tag to SEQUENCE OF (0x30), whose inner encoding is identical.
func parseAttrs(t *testing.T, attrs []byte) []attribute {
	t.Helper()
	seq := make([]byte, len(attrs))
	copy(seq, attrs)
	seq[0] = 0x30
	var list []attribute
	if _, err := asn1.Unmarshal(seq, &list); err != nil {
		t.Fatalf("parse signed attributes: %v", err)
	}
	return list
}

// extractByteRange parses the /ByteRange [a b c d] array from the signed bytes.
func extractByteRange(t *testing.T, data []byte) [4]int {
	t.Helper()
	i := bytes.Index(data, []byte("/ByteRange ["))
	if i < 0 {
		t.Fatal("no /ByteRange")
	}
	i += len("/ByteRange [")
	end := bytes.IndexByte(data[i:], ']')
	fields := bytes.Fields(data[i : i+end])
	if len(fields) != 4 {
		t.Fatalf("ByteRange has %d fields", len(fields))
	}
	var br [4]int
	for k, f := range fields {
		br[k] = atoi(string(f))
	}
	return br
}

// extractContents parses the hex /Contents value into DER bytes.
func extractContents(t *testing.T, data []byte) []byte {
	t.Helper()
	i := bytes.Index(data, []byte("/Contents <"))
	if i < 0 {
		t.Fatal("no /Contents")
	}
	i += len("/Contents <")
	end := bytes.IndexByte(data[i:], '>')
	// Decode the whole hex field (signature DER followed by '0' padding). asn1
	// reads only the DER and ignores the trailing zero bytes, so we must NOT trim
	// (trimming '0' can eat a legitimate final nibble of the DER).
	hexStr := data[i : i+end]
	out := make([]byte, len(hexStr)/2)
	for j := 0; j < len(out); j++ {
		out[j] = hexNibble(hexStr[2*j])<<4 | hexNibble(hexStr[2*j+1])
	}
	return out
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int(s[i]-'0')
		}
	}
	return n
}

func TestSign_BadKey(t *testing.T) {
	r, _ := parse.NewResolver(minimalPDF())
	if _, err := Sign(r, Options{CertPEM: []byte("nope"), KeyPEM: []byte("nope")}); err == nil {
		t.Errorf("Sign with invalid PEM should error")
	}
}

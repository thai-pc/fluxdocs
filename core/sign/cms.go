// Package sign implements PDF digital signatures (Pro): a detached PKCS#7/CMS
// SignedData embedded into the document via an incremental update (ISO 32000-1
// §12.8). It is pure Go (crypto/x509, crypto/rsa, encoding/asn1) — no CGO.
//
// cms.go builds a minimal detached CMS SignedData (RFC 5652) over a message
// digest, with the standard signed attributes (content-type, message-digest,
// signing-time). RSA + SHA-256 is supported, which covers the common case.
package sign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"math/big"
	"time"
)

// Object identifiers (RFC 5652 / PKCS#9 / NIST).
var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
)

// --- ASN.1 structures ---

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue // SET OF AttributeValue
}

type issuerAndSerial struct {
	Issuer       asn1.RawValue
	SerialNumber *big.Int
}

type signerInfo struct {
	Version            int
	SID                issuerAndSerial
	DigestAlgorithm    algorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"` // IMPLICIT [0]
	SignatureAlgorithm algorithmIdentifier
	Signature          []byte
}

type encapContentInfo struct {
	EContentType asn1.ObjectIdentifier
	// eContent is omitted for a detached signature.
}

type signedData struct {
	Version          int
	DigestAlgorithms []algorithmIdentifier `asn1:"set"`
	EncapContentInfo encapContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"` // IMPLICIT [0] SET OF Certificate
	SignerInfos      []signerInfo  `asn1:"set"`
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	// Content carries the SignedData wrapped in an explicit [0] tag. We build the
	// wrapper by hand (wrapExplicit0) because asn1.Marshal ignores the explicit
	// tag directive when a RawValue already has FullBytes set; a plain RawValue
	// field then round-trips the pre-wrapped bytes.
	Content asn1.RawValue
}

// wrapExplicit0 wraps DER content in an explicit context-[0] constructed tag
// (0xA0) with a DER length.
func wrapExplicit0(content []byte) []byte {
	return append(append([]byte{0xA0}, derLength(len(content))...), content...)
}

// derLength encodes a length in DER (short form < 128, else long form).
func derLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte(n & 0xff)}, b...)
		n >>= 8
	}
	return append([]byte{byte(0x80 | len(b))}, b...)
}

// BuildDetachedPKCS7 returns a DER-encoded CMS SignedData over the SHA-256
// digest of `message`, signed by key with cert (RSA). The content is not
// embedded (detached), as required for a PDF /Contents signature.
func BuildDetachedPKCS7(message []byte, cert *x509.Certificate, key *rsa.PrivateKey) ([]byte, error) {
	if cert == nil || key == nil {
		return nil, errors.New("sign: certificate and private key are required")
	}
	digest := sha256.Sum256(message)

	// Signed attributes: content-type, signing-time, message-digest. They must be
	// DER-encoded as a SET when signed, then carried as [0] IMPLICIT.
	attrs := []attribute{
		newAttr(oidContentType, mustMarshal(oidData)),
		newAttr(oidSigningTime, mustMarshalTime(time.Now().UTC())),
		newAttr(oidMessageDigest, mustMarshal(digest[:])),
	}
	signedAttrsSET, err := marshalAttrsSET(attrs)
	if err != nil {
		return nil, err
	}
	// The signature is computed over the SET-tagged DER of the signed attributes.
	attrDigest := sha256.Sum256(signedAttrsSET)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, attrDigest[:])
	if err != nil {
		return nil, err
	}

	// Re-tag the signed attributes as [0] IMPLICIT for embedding in SignerInfo.
	signedAttrsImplicit := make([]byte, len(signedAttrsSET))
	copy(signedAttrsImplicit, signedAttrsSET)
	signedAttrsImplicit[0] = 0xA0 // context [0] constructed

	si := signerInfo{
		Version: 1, // issuerAndSerialNumber
		SID: issuerAndSerial{
			Issuer:       asn1.RawValue{FullBytes: cert.RawIssuer},
			SerialNumber: cert.SerialNumber,
		},
		DigestAlgorithm:    algorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue},
		SignedAttrs:        asn1.RawValue{FullBytes: signedAttrsImplicit},
		SignatureAlgorithm: algorithmIdentifier{Algorithm: oidRSA, Parameters: asn1.NullRawValue},
		Signature:          signature,
	}

	// certificates [0] IMPLICIT SET OF Certificate: a SET (0x31) of the raw cert.
	certSET, err := marshalCertSET(cert.Raw)
	if err != nil {
		return nil, err
	}

	sd := signedData{
		Version:          1,
		DigestAlgorithms: []algorithmIdentifier{{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}},
		EncapContentInfo: encapContentInfo{EContentType: oidData},
		Certificates:     asn1.RawValue{FullBytes: certSET},
		SignerInfos:      []signerInfo{si},
	}
	sdDER, err := asn1.Marshal(sd)
	if err != nil {
		return nil, err
	}

	ci := contentInfo{
		ContentType: oidSignedData,
		Content:     asn1.RawValue{FullBytes: wrapExplicit0(sdDER)},
	}
	return asn1.Marshal(ci)
}

// newAttr wraps a single attribute value as a SET OF (one element).
func newAttr(oid asn1.ObjectIdentifier, valueDER []byte) attribute {
	set := append([]byte{0x31, byte(len(valueDER))}, valueDER...)
	return attribute{Type: oid, Values: asn1.RawValue{FullBytes: set}}
}

// marshalAttrsSET DER-encodes the attributes as a SET OF Attribute (tag 0x31).
// asn1.Marshal encodes a slice as SEQUENCE OF (0x30); SET OF has identical inner
// encoding, so we flip only the outer tag.
func marshalAttrsSET(attrs []attribute) ([]byte, error) {
	seq, err := asn1.Marshal(attrs)
	if err != nil {
		return nil, err
	}
	seq[0] = 0x31 // SEQUENCE OF -> SET OF
	return seq, nil
}

// marshalCertSET encodes a raw certificate as a [0] IMPLICIT SET OF Certificate.
func marshalCertSET(certDER []byte) ([]byte, error) {
	seq, err := asn1.Marshal([]asn1.RawValue{{FullBytes: certDER}})
	if err != nil {
		return nil, err
	}
	seq[0] = 0xA0 // SEQUENCE OF -> [0] IMPLICIT
	return seq, nil
}

func mustMarshal(v interface{}) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic("sign: asn1 marshal: " + err.Error())
	}
	return b
}

func mustMarshalTime(t time.Time) []byte {
	// UTCTime is the conventional CMS signing-time encoding.
	b, err := asn1.Marshal(t)
	if err != nil {
		panic("sign: asn1 time marshal: " + err.Error())
	}
	return b
}

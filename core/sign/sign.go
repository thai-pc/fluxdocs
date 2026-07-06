package sign

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Options configures a signature.
type Options struct {
	Reason      string
	Location    string
	ContactInfo string
	FieldName   string // signature field name; empty -> "Signature1"
	CertPEM     []byte // PEM-encoded X.509 certificate
	KeyPEM      []byte // PEM-encoded RSA private key (PKCS#1 or PKCS#8)
}

// contentsHexLen is the fixed size (in hex characters) reserved for the PKCS#7
// signature in the /Contents placeholder. An RSA-2048 CMS signature with the
// certificate is ~2 KB, so 16384 hex chars (8 KB) leaves ample room.
const contentsHexLen = 16384

// byteRangeField is the fixed decimal width of each /ByteRange number, so the
// real offsets can be written back without shifting any bytes.
const byteRangeField = 10

// Sign appends a detached PKCS#7 digital signature to a PDF via an incremental
// update (ISO 32000-1 §12.8) and returns the signed bytes. The original bytes
// are preserved verbatim; a new signature object, signature field, AcroForm, and
// updated catalog are appended, along with an incremental xref/trailer. This is
// an invisible signature (no page widget).
//
// r must be the resolver the original bytes were parsed from. Returns an error
// if the key/cert cannot be parsed or the document lacks an indirect /Root.
func Sign(r *parse.Resolver, opts Options) ([]byte, error) {
	cert, key, err := parseKeyPair(opts.CertPEM, opts.KeyPEM)
	if err != nil {
		return nil, err
	}
	rootRef, ok := r.RootRef()
	if !ok {
		return nil, errors.New("sign: document trailer has no indirect /Root")
	}
	catalog, ok, err := r.ResolveDict(rootRef)
	if err != nil || !ok {
		return nil, errors.New("sign: cannot resolve the document catalog")
	}

	original := r.Bytes()
	base := r.MaxObjectNumber()
	sigNum := base + 1
	fieldNum := base + 2
	acroNum := base + 3

	fieldName := opts.FieldName
	if fieldName == "" {
		fieldName = "Signature1"
	}

	// Assemble the AcroForm: merge with any existing form fields.
	acroFields := existingFieldRefs(r, catalog)
	acroFields = append(acroFields, fmt.Sprintf("%d 0 R", fieldNum))

	// Re-emit the catalog with /AcroForm pointing at the new form object.
	catCopy := cloneDict(catalog)
	catCopy["AcroForm"] = parse.Reference{Number: acroNum}
	catalogBody := parse.SerializeObject(catCopy)

	// Build the appended objects. The signature object is written last data-wise
	// among the new ones so its /Contents offset is easy to compute, but object
	// order in the file does not matter for xref.
	now := time.Now()
	var appended bytes.Buffer
	appended.WriteByte('\n')

	offSig := len(original) + appended.Len()
	appended.WriteString(fmt.Sprintf("%d 0 obj\n", sigNum))
	appended.WriteString("<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached")
	appended.WriteString(" /M (" + pdfDate(now) + ")")
	if opts.Reason != "" {
		appended.WriteString(" /Reason (" + escapeLiteral(opts.Reason) + ")")
	}
	if opts.Location != "" {
		appended.WriteString(" /Location (" + escapeLiteral(opts.Location) + ")")
	}
	if opts.ContactInfo != "" {
		appended.WriteString(" /ContactInfo (" + escapeLiteral(opts.ContactInfo) + ")")
	}
	appended.WriteString(" /ByteRange [")
	appended.WriteString(strings.Repeat("0", byteRangeField) + " ")
	appended.WriteString(strings.Repeat("0", byteRangeField) + " ")
	appended.WriteString(strings.Repeat("0", byteRangeField) + " ")
	appended.WriteString(strings.Repeat("0", byteRangeField))
	appended.WriteString("] /Contents <")
	appended.WriteString(strings.Repeat("0", contentsHexLen))
	appended.WriteString("> >>\nendobj\n")

	offField := len(original) + appended.Len()
	appended.WriteString(fmt.Sprintf("%d 0 obj\n<< /FT /Sig /T (%s) /V %d 0 R >>\nendobj\n",
		fieldNum, escapeLiteral(fieldName), sigNum))

	offAcro := len(original) + appended.Len()
	appended.WriteString(fmt.Sprintf("%d 0 obj\n<< /Fields [%s] /SigFlags 3 >>\nendobj\n",
		acroNum, strings.Join(acroFields, " ")))

	offCat := len(original) + appended.Len()
	appended.WriteString(fmt.Sprintf("%d 0 obj\n", rootRef.Number))
	appended.Write(catalogBody)
	appended.WriteString("\nendobj\n")

	// Incremental xref + trailer.
	prevStart, _ := parse.FindStartXref(original)
	xrefOff := len(original) + appended.Len()
	writeIncrementalXref(&appended, rootRef.Number, offCat, sigNum, []int{offSig, offField, offAcro})
	appended.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root %d 0 R /Prev %d >>\n",
		acroNum+1, rootRef.Number, prevStart))
	appended.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xrefOff))

	full := append(append([]byte{}, original...), appended.Bytes()...)

	// Locate the /Contents placeholder and compute the ByteRange.
	lo, hi, err := contentsSpan(full, len(original))
	if err != nil {
		return nil, err
	}
	a := lo + 1 // first hex digit (range 1 includes '<')
	b := hi     // '>' starts range 2
	if err := writeByteRange(full, len(original), 0, a, b, len(full)-b); err != nil {
		return nil, err
	}

	// Sign the file with the /Contents hex digits excluded.
	message := append(append([]byte{}, full[:a]...), full[b:]...)
	p7, err := BuildDetachedPKCS7(message, cert, key)
	if err != nil {
		return nil, err
	}
	sigHex := []byte(hex.EncodeToString(p7))
	if len(sigHex) > (b - a) {
		return nil, errors.New("sign: signature larger than the reserved /Contents space")
	}
	// Fill the placeholder: hex signature followed by '0' padding.
	for i := a; i < b; i++ {
		full[i] = '0'
	}
	copy(full[a:], sigHex)
	return full, nil
}

// parseKeyPair decodes a PEM certificate and RSA private key (PKCS#1 or PKCS#8).
func parseKeyPair(certPEM, keyPEM []byte) (*x509.Certificate, *rsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, errors.New("sign: invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: parse certificate: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("sign: invalid private key PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(kb.Bytes); err == nil {
		return cert, key, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: parse private key: %w", err)
	}
	key, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("sign: only RSA private keys are supported")
	}
	return cert, key, nil
}

// existingFieldRefs returns "n g R" strings for the AcroForm's current /Fields,
// so a re-signed document keeps its existing fields.
func existingFieldRefs(r *parse.Resolver, catalog parse.Dict) []string {
	af, ok, err := r.ResolveDict(catalog["AcroForm"])
	if err != nil || !ok {
		return nil
	}
	arr, ok := af["Fields"].(parse.Array)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if ref, ok := e.(parse.Reference); ok {
			out = append(out, fmt.Sprintf("%d %d R", ref.Number, ref.Generation))
		}
	}
	return out
}

// writeIncrementalXref writes a classic incremental xref with two subsections:
// the updated catalog (one entry) and the contiguous new objects.
func writeIncrementalXref(buf *bytes.Buffer, catNum, catOff, firstNew int, newOffsets []int) {
	buf.WriteString("xref\n")
	buf.WriteString(fmt.Sprintf("%d 1\n", catNum))
	buf.WriteString(fmt.Sprintf("%010d 00000 n \n", catOff))
	buf.WriteString(fmt.Sprintf("%d %d\n", firstNew, len(newOffsets)))
	for _, off := range newOffsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
}

// contentsSpan finds the byte offsets of the '<' and '>' delimiting the
// /Contents hex string in the appended region (searching from `from`).
func contentsSpan(full []byte, from int) (lo, hi int, err error) {
	idx := bytes.Index(full[from:], []byte("/Contents <"))
	if idx < 0 {
		return 0, 0, errors.New("sign: /Contents placeholder not found")
	}
	lo = from + idx + len("/Contents ") // position of '<'
	hi = bytes.IndexByte(full[lo:], '>')
	if hi < 0 {
		return 0, 0, errors.New("sign: /Contents has no closing '>'")
	}
	return lo, lo + hi, nil
}

// writeByteRange overwrites the four zero-padded /ByteRange fields (each
// byteRangeField wide) with the given values, searching from `from`.
func writeByteRange(full []byte, from, v0, v1, v2, v3 int) error {
	idx := bytes.Index(full[from:], []byte("/ByteRange ["))
	if idx < 0 {
		return errors.New("sign: /ByteRange placeholder not found")
	}
	p := from + idx + len("/ByteRange [")
	for _, v := range []int{v0, v1, v2, v3} {
		field := fmt.Sprintf("%0*d", byteRangeField, v)
		if len(field) != byteRangeField {
			return errors.New("sign: ByteRange value too large for the reserved field")
		}
		copy(full[p:p+byteRangeField], field)
		p += byteRangeField + 1 // field + separator (space, or ']' after last)
	}
	return nil
}

func cloneDict(d parse.Dict) parse.Dict {
	out := make(parse.Dict, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

func pdfDate(t time.Time) string {
	_, off := t.Zone()
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	return fmt.Sprintf("D:%s%c%02d'%02d'", t.Format("20060102150405"), sign, off/3600, (off%3600)/60)
}

func escapeLiteral(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

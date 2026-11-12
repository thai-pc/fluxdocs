package parse

// Test-only exports for the external parse_test package (crypt_open_test.go):
// an independent Standard Security Handler (R2–R4) and an encrypted-document
// builder, so core.OpenBytes can be tested on documents not built by crypt.go.
// Being a _test.go file, none of this is part of the package API.

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
)

// StdSecurity is a test-only, independent implementation of the Standard
// Security Handler's password algorithms for R2–R4 (ISO 32000-1 §7.6.3.3–4),
// written from the standard rather than by calling crypt.go, so fixtures built
// with it are an oracle for newDecryptor. It is exported so the external
// parse_test package can build encrypted documents for core.OpenBytes.
type StdSecurity struct {
	R               int    // revision: 2, 3 or 4
	KeyLen          int    // file key length in bytes: 5 for R2, 5–16 otherwise
	User, Owner     []byte // passwords; an empty owner password means the user one
	P               int32  // permission flags
	ID              []byte // first element of the file /ID
	EncryptMetadata bool   // R4 /EncryptMetadata
}

// Build returns the /O and /U values and the file encryption key.
func (s StdSecurity) Build() (o, u, key []byte) {
	o = s.ownerHash()
	key = s.fileKey(s.User, o)
	if s.R == 2 {
		// Algorithm 4: RC4-encrypt the padding string with the file key.
		u = isoRC4(key, passwordPad)
	} else {
		// Algorithm 5: MD5(padding || ID[0]), RC4 with the key, then 19 more
		// passes with each key byte XORed with the pass number, then 16 bytes
		// of arbitrary padding.
		h := md5.New()
		h.Write(passwordPad)
		h.Write(s.ID)
		u = isoRC4(key, h.Sum(nil))
		for i := 1; i <= 19; i++ {
			u = isoRC4(xorKey(key, byte(i)), u)
		}
		u = append(u, bytes.Repeat([]byte{0x5A}, 16)...)
	}
	return o, u, key
}

// ObjectKey is Algorithm 1: the per-object key for num/gen, with the AES salt
// when aes is set.
func (s StdSecurity) ObjectKey(key []byte, num, gen int, aes bool) []byte {
	h := md5.New()
	h.Write(key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	if aes {
		h.Write([]byte("sAlT"))
	}
	return h.Sum(nil)[:min(len(key)+5, 16)]
}

// fileKey is Algorithm 2.
func (s StdSecurity) fileKey(password, o []byte) []byte {
	h := md5.New()
	h.Write(isoPad(password))
	h.Write(o)
	h.Write([]byte{byte(s.P), byte(s.P >> 8), byte(s.P >> 16), byte(s.P >> 24)})
	h.Write(s.ID)
	if s.R >= 4 && !s.EncryptMetadata {
		h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	sum := h.Sum(nil)
	if s.R == 2 {
		return sum[:5]
	}
	for i := 0; i < 50; i++ {
		next := md5.Sum(sum[:s.KeyLen])
		sum = next[:]
	}
	return sum[:s.KeyLen]
}

// ownerHash is Algorithm 3.
func (s StdSecurity) ownerHash() []byte {
	owner := s.Owner
	if len(owner) == 0 {
		owner = s.User
	}
	sum := md5.Sum(isoPad(owner))
	n := 5
	if s.R >= 3 {
		for i := 0; i < 50; i++ {
			sum = md5.Sum(sum[:])
		}
		n = s.KeyLen
	}
	rk := sum[:n]
	o := isoRC4(rk, isoPad(s.User))
	if s.R >= 3 {
		for i := 1; i <= 19; i++ {
			o = isoRC4(xorKey(rk, byte(i)), o)
		}
	}
	return o
}

func isoPad(pw []byte) []byte {
	return append(append([]byte(nil), pw[:min(len(pw), 32)]...), passwordPad[:32-min(len(pw), 32)]...)
}

func isoRC4(key, data []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

func xorKey(key []byte, b byte) []byte {
	out := make([]byte, len(key))
	for i := range key {
		out[i] = key[i] ^ b
	}
	return out
}

// EncryptedPDF is a test-only builder for a one-page document whose content
// stream (object 4) is encrypted with the file key from sec. v and cfm select
// the cipher: V1/V2 are RC4; V4 uses the /StdCF crypt filter cfm ("V2" or
// "AESV2"). A non-nil u replaces the computed /U (to forge a short or wrong
// value). The page uses Helvetica, so the decrypted text can be extracted.
func EncryptedPDF(sec StdSecurity, v int, cfm Name, plain string, u []byte) []byte {
	o, realU, key := sec.Build()
	if u == nil {
		u = realU
	}
	aes := cfm == "AESV2"
	objKey := sec.ObjectKey(key, 4, 0, aes)
	var ct []byte
	if aes {
		ct = aesEnc(objKey, []byte(plain))
	} else {
		ct = isoRC4(objKey, []byte(plain))
	}
	enc := fmt.Sprintf("<< /Filter /Standard /V %d /R %d /Length %d /P %d /O <%X> /U <%X>", v, sec.R, sec.KeyLen*8, sec.P, o, u)
	if cfm != "" {
		enc += fmt.Sprintf(" /CF << /StdCF << /CFM /%s /Length %d >> >> /StmF /StdCF /StrF /StdCF", string(cfm), sec.KeyLen)
	}
	if !sec.EncryptMetadata {
		enc += " /EncryptMetadata false"
	}
	enc += " >>"
	return buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(ct), ct),
		5: enc,
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, fmt.Sprintf("<< /Size 7 /Root 1 0 R /Encrypt 5 0 R /ID [<%X> <%X>] >>", sec.ID, sec.ID))
}

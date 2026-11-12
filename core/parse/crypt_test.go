package parse

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rc4"
	"errors"
	"testing"
)

// encDict builds a minimal Standard-handler /Encrypt dictionary whose /O and
// /U are computed by the ISO 32000-1 algorithms (StdSecurity, export_test.go)
// for an empty user password and file identifier id, so newDecryptor accepts
// it.
func encDict(v, r, length int64, cfm Name, id []byte) Dict {
	keyLen := 5
	switch {
	case r == 2:
	case cfm == "AESV2":
		keyLen = 16
	default:
		keyLen = int(min(max(length, 40), 128) / 8)
	}
	o, u, _ := StdSecurity{R: int(r), KeyLen: keyLen, Owner: []byte("owner"), P: -44, ID: id, EncryptMetadata: true}.Build()
	d := Dict{
		"Filter": Name("Standard"),
		"V":      Integer(v),
		"R":      Integer(r),
		"Length": Integer(length),
		"O":      String{Value: o},
		"U":      String{Value: u},
		"P":      Integer(-44),
	}
	if cfm != "" {
		d["CF"] = Dict{"StdCF": Dict{"CFM": cfm, "Length": Integer(length / 8)}}
	}
	return d
}

func rc4Enc(key, pt []byte) []byte {
	c, _ := rc4.NewCipher(key)
	out := make([]byte, len(pt))
	c.XORKeyStream(out, pt)
	return out
}

func aesEnc(key, pt []byte) []byte {
	block, _ := aes.NewCipher(key)
	n := aes.BlockSize - len(pt)%aes.BlockSize
	padded := append(append([]byte(nil), pt...), bytes.Repeat([]byte{byte(n)}, n)...)
	iv := make([]byte, aes.BlockSize)
	rand.Read(iv)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return append(iv, ct...)
}

func TestDecryptRC4RoundTrip(t *testing.T) {
	d, err := newDecryptor(encDict(2, 3, 128, "", []byte("file-id-0")), []byte("file-id-0"), nil)
	if err != nil {
		t.Fatalf("newDecryptor: %v", err)
	}
	if d.method != methodRC4 {
		t.Fatalf("method = %v, want RC4", d.method)
	}
	plain := []byte("the quick brown fox jumps over the lazy dog")
	ct := rc4Enc(d.objectKey(7, 0), plain)
	got := d.decryptBytes(7, 0, ct)
	if !bytes.Equal(got, plain) {
		t.Errorf("RC4 decrypt = %q, want %q", got, plain)
	}
	// A different object number must not decrypt correctly (per-object key).
	if bytes.Equal(d.decryptBytes(8, 0, ct), plain) {
		t.Error("decrypt with wrong object number should not match")
	}
}

func TestDecryptAES128RoundTrip(t *testing.T) {
	d, err := newDecryptor(encDict(4, 4, 128, "AESV2", []byte("file-id-1")), []byte("file-id-1"), nil)
	if err != nil {
		t.Fatalf("newDecryptor: %v", err)
	}
	if d.method != methodAESV2 {
		t.Fatalf("method = %v, want AESV2", d.method)
	}
	plain := []byte("AES-128 CBC payload with PKCS7 padding")
	ct := aesEnc(d.objectKey(3, 0), plain)
	got := d.decryptBytes(3, 0, ct)
	if !bytes.Equal(got, plain) {
		t.Errorf("AES decrypt = %q, want %q", got, plain)
	}
}

func TestDecryptObjectRecurses(t *testing.T) {
	d, _ := newDecryptor(encDict(2, 3, 128, "", []byte("id")), []byte("id"), nil)
	key := d.objectKey(5, 0)
	// A stream whose dict holds an encrypted string and whose body is encrypted.
	st := &Stream{
		Dict: Dict{"Title": String{Value: rc4Enc(key, []byte("Secret Title"))}},
		Raw:  rc4Enc(key, []byte("stream body")),
	}
	out := d.decryptObject(5, 0, st).(*Stream)
	if got := stringValue(out.Dict["Title"]); string(got) != "Secret Title" {
		t.Errorf("dict string = %q, want Secret Title", got)
	}
	if string(out.Raw) != "stream body" {
		t.Errorf("stream body = %q", out.Raw)
	}
}

func TestNewDecryptorRejectsUnsupported(t *testing.T) {
	// AES-256 (V5/R6) is not yet supported.
	if _, err := newDecryptor(encDict(5, 6, 256, "AESV3", nil), nil, nil); err == nil {
		t.Error("expected an error for R6/AES-256")
	}
	// Non-standard handler.
	if _, err := newDecryptor(Dict{"Filter": Name("Custom"), "R": Integer(3), "O": String{Value: bytes.Repeat([]byte{0}, 32)}}, nil, nil); err == nil {
		t.Error("expected an error for a non-Standard handler")
	}
}

// TestNewDecryptor_UserPassword pins Algorithm 6 (ISO 32000-1 §7.6.3.4): a
// password (empty or not) is accepted only when it is the user password, and
// then yields the Algorithm 2 key. R2 compares all 32 bytes of /U, R3–R4 the
// first 16; a /U too short to compare, or missing, is an error rather than a
// panic or an unchecked key.
func TestNewDecryptor_UserPassword(t *testing.T) {
	id := []byte("0123456789abcdef")
	type variant struct {
		name string
		v    int
		r    int
		kl   int
		cfm  Name
		meta bool
	}
	variants := []variant{
		{"R2 RC4-40", 1, 2, 5, "", true},
		{"R3 RC4-128", 2, 3, 16, "", true},
		{"R3 RC4-40", 2, 3, 5, "", true},
		{"R4 RC4 StdCF", 4, 4, 16, "V2", true},
		{"R4 AESV2", 4, 4, 16, "AESV2", true},
		{"R4 AESV2 EncryptMetadata false", 4, 4, 16, "AESV2", false},
	}
	for _, vr := range variants {
		for _, tc := range []struct {
			name     string
			user     string // user password the document is built with
			owner    string // owner password; empty means the user password
			password string // password given to newDecryptor
			p        int32  // /P the key is derived with
			dictP    Integer
			u        func(u []byte) []byte
			wantOK   bool
		}{
			{"empty user and owner password", "", "", "", -3904, -3904, nil, true},
			{"empty user password, owner password set", "", "owner", "", -3904, -3904, nil, true},
			{"non-empty user password, empty given", "secret", "owner", "", -3904, -3904, nil, false},
			{"non-empty user password, given", "secret", "owner", "secret", -3904, -3904, nil, true},
			{"non-empty user password, wrong one given", "secret", "owner", "secreT", -3904, -3904, nil, false},
			// ISO 32000-1 Table 21: /P is a 32-bit field. A writer that stores
			// it unsigned (4294967292 for -4) gives the same low-order bytes
			// to Algorithm 2.
			{"/P written unsigned", "", "owner", "", -4, 4294967292, nil, true},
			{"/U truncated to 15 bytes", "", "owner", "", -3904, -3904, func(u []byte) []byte { return u[:15] }, false},
			{"/U truncated to 31 bytes", "", "owner", "", -3904, -3904, func(u []byte) []byte { return u[:31] }, vr.r >= 3},
			{"/U missing", "", "owner", "", -3904, -3904, func([]byte) []byte { return nil }, false},
			{"/U first byte flipped", "", "owner", "", -3904, -3904, func(u []byte) []byte { u[0] ^= 1; return u }, false},
			{"/U byte 20 flipped", "", "owner", "", -3904, -3904, func(u []byte) []byte { u[20] ^= 1; return u }, vr.r >= 3},
		} {
			t.Run(vr.name+"/"+tc.name, func(t *testing.T) {
				sec := StdSecurity{R: vr.r, KeyLen: vr.kl, User: []byte(tc.user), Owner: []byte(tc.owner), P: tc.p, ID: id, EncryptMetadata: vr.meta}
				if tc.owner == "" {
					sec.Owner = nil
				}
				o, u, key := sec.Build()
				if tc.u != nil {
					u = tc.u(append([]byte(nil), u...))
				}
				enc := Dict{
					"Filter": Name("Standard"),
					"V":      Integer(vr.v),
					"R":      Integer(vr.r),
					"Length": Integer(vr.kl * 8),
					"O":      String{Value: o},
					"P":      tc.dictP,
				}
				if u != nil {
					enc["U"] = String{Value: u}
				}
				if vr.cfm != "" {
					enc["CF"] = Dict{"StdCF": Dict{"CFM": vr.cfm, "Length": Integer(vr.kl)}}
				}
				if !vr.meta {
					enc["EncryptMetadata"] = Boolean(false)
				}
				var password []byte
				if tc.password != "" {
					password = []byte(tc.password)
				}
				d, err := newDecryptor(enc, id, password)
				if (err == nil) != tc.wantOK {
					t.Fatalf("newDecryptor err = %v, wantOK %v", err, tc.wantOK)
				}
				if err != nil {
					if !errors.Is(err, errLex) {
						t.Errorf("err = %v, want a wrapped errLex", err)
					}
					return
				}
				if !bytes.Equal(d.key, key) {
					t.Errorf("file key = %X, want %X (independent Algorithm 2)", d.key, key)
				}
			})
		}
	}
}

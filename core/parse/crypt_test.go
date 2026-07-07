package parse

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rc4"
	"testing"
)

// encDict builds a minimal Standard-handler /Encrypt dictionary. O is a fixed
// 32-byte value; the file key is derived from it for the empty password.
func encDict(v, r, length int64, cfm Name) Dict {
	o := bytes.Repeat([]byte{0xAB}, 32)
	d := Dict{
		"Filter": Name("Standard"),
		"V":      Integer(v),
		"R":      Integer(r),
		"Length": Integer(length),
		"O":      String{Value: o},
		"U":      String{Value: bytes.Repeat([]byte{0xCD}, 32)},
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
	d, err := newDecryptor(encDict(2, 3, 128, ""), []byte("file-id-0"), nil)
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
	d, err := newDecryptor(encDict(4, 4, 128, "AESV2"), []byte("file-id-1"), nil)
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
	d, _ := newDecryptor(encDict(2, 3, 128, ""), []byte("id"), nil)
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
	if _, err := newDecryptor(encDict(5, 6, 256, "AESV3"), nil, nil); err == nil {
		t.Error("expected an error for R6/AES-256")
	}
	// Non-standard handler.
	if _, err := newDecryptor(Dict{"Filter": Name("Custom"), "R": Integer(3), "O": String{Value: bytes.Repeat([]byte{0}, 32)}}, nil, nil); err == nil {
		t.Error("expected an error for a non-Standard handler")
	}
}

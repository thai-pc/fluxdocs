package parse

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"math"
)

// crypt.go implements the PDF Standard Security Handler (ISO 32000 §7.6.3) for
// decryption: RC4 (V1/V2, R2/R3) and AES-128 (V4/R4, /AESV2), with the common
// empty user password (documents that restrict permissions but open without a
// password). All primitives are from the standard library — no dependency. Key
// derivation is Algorithm 2; per-object keys and cipher application follow
// Algorithms 1 and 1.A. AES-256 (V5/R6) is a documented follow-up.

type cryptMethod int

const (
	methodRC4 cryptMethod = iota
	methodAESV2
)

// decryptor holds the file encryption key and cipher chosen for a document.
type decryptor struct {
	key    []byte
	method cryptMethod
}

// passwordPad is the 32-byte padding string from Algorithm 2.
var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// newDecryptor builds a decryptor from the /Encrypt dictionary, the first
// element of the file /ID, and a user password (nil/empty for the common case).
func newDecryptor(enc Dict, id, password []byte) (*decryptor, error) {
	if name, _ := enc["Filter"].(Name); name != "Standard" {
		return nil, fmt.Errorf("%w: unsupported security handler %q", errLex, name)
	}
	v, _ := enc.GetInt("V")
	r, _ := enc.GetInt("R")
	if r < 2 || r > 4 {
		return nil, fmt.Errorf("%w: unsupported /R %d (only 2-4: RC4/AES-128)", errLex, r)
	}
	o := stringValue(enc["O"])
	if len(o) < 32 {
		return nil, fmt.Errorf("%w: /O too short", errLex)
	}
	p, _ := enc.GetInt("P")

	length := 40
	if l, ok := enc.GetInt("Length"); ok && l > 0 {
		length = keyBits(l)
	}

	method := methodRC4
	if v == 4 {
		var err error
		method, length, err = readCryptFilter(enc)
		if err != nil {
			return nil, err
		}
	}
	// Algorithm 2 step (h) (§7.6.3.3): an R2 key is always 5 bytes, whatever
	// /Length says. R3–R4 keys are /Length bits, 40–128 (§7.6.3.2, Table 20);
	// the key is cut from a 16-byte MD5 sum, so a hostile /Length 256 sliced it
	// to [:32] and panicked on open (SECURITY.md §2): reject out-of-range
	// lengths as unsupported.
	keyLen := 5
	if r >= 3 {
		if length < 40 || length > 128 {
			return nil, fmt.Errorf("%w: unsupported key /Length %d", errLex, length)
		}
		keyLen = length / 8
	}

	encMeta := true
	if b, ok := enc["EncryptMetadata"].(Boolean); ok {
		encMeta = bool(b)
	}

	key := deriveKey(password, o[:32], int32(p), id, int(r), keyLen, encMeta)
	return &decryptor{key: key, method: method}, nil
}

// readCryptFilter reads the /StdCF crypt filter (V4) to choose RC4 vs AES-128
// and the key length.
func readCryptFilter(enc Dict) (cryptMethod, int, error) {
	cf, _ := enc["CF"].(Dict)
	std, _ := cf["StdCF"].(Dict)
	cfm, _ := std["CFM"].(Name)
	switch cfm {
	case "V2":
		length := 128
		if l, ok := std.GetInt("Length"); ok && l > 0 {
			length = normalizeLen(keyBits(l))
		}
		return methodRC4, length, nil
	case "AESV2":
		return methodAESV2, 128, nil
	default:
		return 0, 0, fmt.Errorf("%w: unsupported crypt filter method %q", errLex, cfm)
	}
}

// keyBits narrows a positive /Length to int, clamping it in int64 first so a
// hostile value cannot wrap on a 32-bit int into the accepted 40–128 range
// (2^32+64 would become 64); the clamped value is rejected by the range check.
func keyBits(l Integer) int {
	if l > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(l)
}

// normalizeLen accepts a crypt-filter /Length given either in bytes or bits.
func normalizeLen(l int) int {
	if l <= 40 {
		return l * 8
	}
	return l
}

// deriveKey implements Algorithm 2 for R2-4 (empty or given user password).
func deriveKey(password, o []byte, p int32, id []byte, r, keyLen int, encMeta bool) []byte {
	padded := padPassword(password)

	h := md5.New()
	h.Write(padded)
	h.Write(o)
	h.Write([]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)})
	h.Write(id)
	if r >= 4 && !encMeta {
		h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	sum := h.Sum(nil)

	if r >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(sum[:keyLen])
			sum = s[:]
		}
	}
	return sum[:keyLen]
}

// padPassword truncates/pads a password to 32 bytes with the standard padding.
func padPassword(password []byte) []byte {
	out := make([]byte, 32)
	n := copy(out, password)
	copy(out[n:], passwordPad)
	return out
}

// objectKey derives the per-object key (Algorithm 1) for object num/gen.
func (d *decryptor) objectKey(num, gen int) []byte {
	h := md5.New()
	h.Write(d.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16)})
	h.Write([]byte{byte(gen), byte(gen >> 8)})
	if d.method == methodAESV2 {
		h.Write([]byte("sAlT"))
	}
	sum := h.Sum(nil)
	n := len(d.key) + 5
	if n > 16 {
		n = 16
	}
	return sum[:n]
}

// decryptBytes decrypts one string or stream payload for object num/gen. On any
// malformation it returns the input unchanged (lenient parsing, SECURITY.md §2).
func (d *decryptor) decryptBytes(num, gen int, data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	objKey := d.objectKey(num, gen)
	switch d.method {
	case methodAESV2:
		return aesCBCDecrypt(objKey, data)
	default:
		c, err := rc4.NewCipher(objKey)
		if err != nil {
			return data
		}
		out := make([]byte, len(data))
		c.XORKeyStream(out, data)
		return out
	}
}

// aesCBCDecrypt decrypts AES-CBC data prefixed with a 16-byte IV, stripping PKCS7
// padding. Malformed input is returned unchanged.
func aesCBCDecrypt(key, data []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil || len(data) < 16 || (len(data)-16)%aes.BlockSize != 0 {
		return data
	}
	iv, ct := data[:16], data[16:]
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	if n := int(out[len(out)-1]); n >= 1 && n <= aes.BlockSize && n <= len(out) {
		out = out[:len(out)-n]
	}
	return out
}

// decryptObject returns obj with every String and Stream payload decrypted for
// object num/gen (recursively through arrays and dictionaries).
func (d *decryptor) decryptObject(num, gen int, obj Object) Object {
	switch v := obj.(type) {
	case String:
		return String{Value: d.decryptBytes(num, gen, v.Value), Hex: v.Hex}
	case Array:
		for i, el := range v {
			v[i] = d.decryptObject(num, gen, el)
		}
		return v
	case Dict:
		for k, el := range v {
			v[k] = d.decryptObject(num, gen, el)
		}
		return v
	case *Stream:
		v.Dict = d.decryptObject(num, gen, v.Dict).(Dict)
		v.Raw = d.decryptBytes(num, gen, v.Raw)
		return v
	default:
		return obj
	}
}

// stringValue returns the bytes of a String object, or nil.
func stringValue(o Object) []byte {
	if s, ok := o.(String); ok {
		return s.Value
	}
	return nil
}

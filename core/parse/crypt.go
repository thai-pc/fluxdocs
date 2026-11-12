package parse

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// crypt.go implements the PDF Standard Security Handler for decryption: RC4
// (V1/V2, R2/R3) and AES-128 (V4/R4, /AESV2) per ISO 32000-1 §7.6.3, and
// AES-256 (V5, R6 and the Adobe R5 it replaced, /AESV3) per ISO 32000-2
// §7.6.4 (crypt_v5.go). All primitives are from the standard library — no
// dependency. For R2–R4 the file key is Algorithm 2; a password is accepted
// as the user password by Algorithm 6 (via Algorithms 4 and 5) or as the
// owner password by Algorithm 7 (which recovers the user password from /O
// with Algorithm 3's key). Per-object keys and cipher application follow
// Algorithms 1 and 1.A.

type cryptMethod int

const (
	methodRC4 cryptMethod = iota
	methodAESV2
	// methodAESV3 is AES-256-CBC with the file key itself (Algorithm 1.A):
	// no per-object key.
	methodAESV3
	// methodIdentity leaves the data as it is: /Identity, or a crypt filter
	// whose /CFM is /None (ISO 32000-1 Table 25).
	methodIdentity
	// methodUnsupported is a crypt filter FluxDocs cannot apply. Data it
	// covers is left as it is (a /Crypt stage naming it stays in the decode
	// chain, so decoding such a stream fails instead of returning
	// ciphertext as content).
	methodUnsupported
)

// Errors EnableDecryption returns (wrapped) so callers can tell why a
// document does not open. Both wrap errLex; any other decryption error means
// the handler, revision or dictionary is unsupported or malformed.
var (
	// ErrWrongPassword: the password tried (the empty one included) is
	// neither the user nor the owner password.
	ErrWrongPassword = fmt.Errorf("%w: the password is neither the user nor the owner password", errLex)
	// ErrEncryptionTampered: an AES-256 password was accepted, but the
	// file key it unlocked does not decrypt /Perms (Algorithm 13): /UE or
	// /OE was modified, so content would decrypt to garbage.
	ErrEncryptionTampered = fmt.Errorf("%w: the encryption dictionary does not authenticate its file key", errLex)
)

// DecryptionInfo describes how EnableDecryption opened an encrypted
// document. It holds no key and no password.
type DecryptionInfo struct {
	V, R int // the handler's /V and /R
	// Owner is set when the password was accepted as the owner password.
	Owner bool
	// EmptyUserPassword is set when the empty password opens the document
	// (as the user password): its content is not protected by a password.
	EmptyUserPassword bool
	// Permissions are the permission flags: for V5, the value /Perms
	// authenticates (Algorithm 13), which wins over a differing /P; for
	// V1–V4, /P. FluxDocs does not enforce them.
	Permissions int32
	// EncryptMetadata is false when /Type /Metadata streams are stored
	// unencrypted (/EncryptMetadata false; for V5, /Perms byte 8 wins).
	EncryptMetadata bool
}

// decryptor holds the file encryption key and the ciphers chosen for a
// document.
type decryptor struct {
	key []byte
	// method is the cipher of V1–V4 strings and streams (and of V4's
	// /StdCF); for V5 it is methodAESV3.
	method cryptMethod
	// str, stm and eff are the ciphers of strings, streams and embedded-file
	// streams: /StrF, /StmF and /EFF for V5; method for V1–V4, which keep
	// using /StdCF for both, as before.
	str, stm, eff cryptMethod
	// filters are the named crypt filters of /CF (V4 and V5), for streams
	// whose /Filter starts with /Crypt (ISO 32000-1 §7.4.10); nil for V1–V3.
	filters map[Name]cryptMethod
	// metaClear is set for V ≥ 4 with /EncryptMetadata false: /Type
	// /Metadata streams are not decrypted (ISO 32000-1 Table 20).
	metaClear bool
	info      DecryptionInfo
}

// passwordPad is the 32-byte padding string from Algorithm 2.
var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// newDecryptor builds a decryptor from the /Encrypt dictionary, the first
// element of the file /ID, and one password given as bytes (nil/empty for the
// common case): for R2–R4 the bytes as Algorithm 2 pads them, for R5/R6 the
// bytes as they are (truncated to 127), without SASLprep.
//
// Returns the errors of newDecryptorFor.
func newDecryptor(enc Dict, id, password []byte) (*decryptor, error) {
	return newDecryptorFor(enc, id, [][]byte{password}, &v5Hasher{})
}

// newDecryptorFor builds a decryptor trying each candidate encoding of one
// password in order, each first as the user and then as the owner password.
// h counts and bounds the AES-256 hashes (crypt_v5.go).
//
// Returns a wrapped errLex if the handler, revision, key length or
// dictionary is unsupported or malformed (/O or /U too short, …); a wrapped
// ErrWrongPassword if no candidate is the user or owner password — a wrong
// key would only decrypt to garbage; or a wrapped ErrEncryptionTampered for
// an AES-256 dictionary whose /Perms does not decrypt with the file key.
func newDecryptorFor(enc Dict, id []byte, cands [][]byte, h *v5Hasher) (*decryptor, error) {
	if name, _ := enc["Filter"].(Name); name != "Standard" {
		// The name is document bytes: keep it out of the message, which
		// core.OpenBytes passes on.
		return nil, fmt.Errorf("%w: unsupported security handler", errLex)
	}
	v, _ := enc.GetInt("V")
	r, _ := enc.GetInt("R")
	// Dispatch on (V, R) before any length check (aes256-decryption.md §6).
	switch {
	case v == 5 && (r == 5 || r == 6):
		return newV5Decryptor(enc, cands, h)
	case v == 5 || r >= 5:
		return nil, fmt.Errorf("%w: unsupported /V %d with /R %d", errLex, v, r)
	case r < 2 || r > 4:
		return nil, fmt.Errorf("%w: unsupported /R %d (only 2-6)", errLex, r)
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
	u := stringValue(enc["U"])

	d := &decryptor{method: method, str: method, stm: method, eff: method}
	d.info = DecryptionInfo{V: int(v), R: int(r), Permissions: int32(p), EncryptMetadata: encMeta}
	if v == 4 {
		d.filters = v4Filters(enc)
		d.metaClear = !encMeta
	}
	try := func(pw []byte) ([]byte, error) {
		key := deriveKey(pw, o[:32], int32(p), id, int(r), keyLen, encMeta)
		if err := checkUserPassword(key, u, id, int(r)); err != nil {
			return nil, err
		}
		return key, nil
	}
	for _, pw := range cands {
		key, err := try(pw)
		if err == nil {
			d.key = key
			d.info.EmptyUserPassword = len(pw) == 0
			return d, nil
		}
		if !errors.Is(err, ErrWrongPassword) {
			return nil, err // /U too short: malformed, whatever the password
		}
		// Algorithm 7: pw as the owner password yields the padded user
		// password, which Algorithm 6 then checks.
		upw, err := ownerToUserPassword(pw, o[:32], int(r), keyLen)
		if err != nil {
			return nil, err
		}
		key, err = try(upw)
		if err == nil {
			d.key = key
			d.info.Owner = true
			// An empty user password pads to passwordPad itself.
			d.info.EmptyUserPassword = len(pw) == 0 || bytes.Equal(upw, passwordPad)
			return d, nil
		}
		if !errors.Is(err, ErrWrongPassword) {
			return nil, err
		}
	}
	return nil, ErrWrongPassword
}

// checkUserPassword implements Algorithm 6 (ISO 32000-1 §7.6.3.4): it
// recomputes /U from the file key derived for the candidate password and
// compares it with the stored value — all 32 bytes for R2, the first 16 for
// R3–R4 (the rest of /U is arbitrary padding). The comparison is
// constant-time.
//
// Returns a wrapped errLex if /U is shorter than the bytes compared, or a
// wrapped ErrWrongPassword if it does not match.
func checkUserPassword(key, u, id []byte, r int) error {
	want, err := userPasswordHash(key, id, r)
	if err != nil {
		return err
	}
	if len(u) < len(want) {
		return fmt.Errorf("%w: /U too short (%d bytes)", errLex, len(u))
	}
	if subtle.ConstantTimeCompare(want, u[:len(want)]) != 1 {
		return fmt.Errorf("%w: password does not match /U", ErrWrongPassword)
	}
	return nil
}

// ownerToUserPassword implements Algorithm 7 steps (a)–(b) (ISO 32000-1
// §7.6.3.4): the RC4 key of Algorithm 3 steps (a)–(d) for the candidate
// owner password decrypts /O — once for R2, 20 times for R3–R4 with the key's
// bytes XORed with 19 down to 0 — giving the padded user password.
//
// Returns rc4XOR's error if the key is not a valid RC4 key.
func ownerToUserPassword(owner, o []byte, r, keyLen int) ([]byte, error) {
	sum := md5.Sum(padPassword(owner))
	n := 5
	if r >= 3 {
		n = keyLen
		// Algorithm 3 (c) re-hashes "the output from the previous MD5 hash"
		// 50 times. Read as its first n bytes (as Algorithm 2 (h) says
		// explicitly): with a 128-bit key the two readings agree, and for a
		// shorter key this is the one two independent programs use —
		// Ghostscript's pdfwrite wrote r3_40_user.pdf with it, and qpdf
		// accepts that file's owner password.
		for i := 0; i < 50; i++ {
			sum = md5.Sum(sum[:n])
		}
	}
	rk := sum[:n]
	if r == 2 {
		return rc4XOR(rk, o)
	}
	out := append([]byte(nil), o...)
	k := make([]byte, n)
	for i := 19; i >= 0; i-- {
		for j := range rk {
			k[j] = rk[j] ^ byte(i)
		}
		var err error
		if out, err = rc4XOR(k, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// userPasswordHash computes the checked part of /U for file key key:
// Algorithm 4 for R2 (the padding string RC4-encrypted with the key, 32
// bytes), Algorithm 5 for R3–R4 (MD5 of the padding and the first /ID
// element, RC4-encrypted with the key and then 19 more times with the key's
// bytes XORed with 1..19, 16 bytes).
//
// Returns rc4XOR's error if the key is not a valid RC4 key.
func userPasswordHash(key, id []byte, r int) ([]byte, error) {
	if r == 2 {
		return rc4XOR(key, passwordPad)
	}
	h := md5.New()
	h.Write(passwordPad)
	h.Write(id)
	out, err := rc4XOR(key, h.Sum(nil))
	if err != nil {
		return nil, err
	}
	k := make([]byte, len(key))
	for i := 1; i <= 19; i++ {
		for j := range key {
			k[j] = key[j] ^ byte(i)
		}
		if out, err = rc4XOR(k, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rc4XOR returns data RC4-encrypted (equivalently, decrypted) with key. The
// key is 5–16 bytes here, always valid for crypto/rc4, but it fails closed
// all the same: an invalid key is an error, never a fixed output that a
// crafted /U could match and so pass Algorithm 6.
//
// Returns a wrapped errLex if crypto/rc4 rejects the key.
func rc4XOR(key, data []byte) ([]byte, error) {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid RC4 key length %d", errLex, len(key))
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, nil
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
		return 0, 0, fmt.Errorf("%w: unsupported crypt filter method", errLex) // no document bytes
	}
}

// v4Filters maps the named crypt filters of a V4 /CF to their ciphers, for
// /Crypt stream filters: /V2 is RC4 and /AESV2 AES-128 (with the file key),
// /None (or no /CFM, its default) is Identity, anything else unsupported.
func v4Filters(enc Dict) map[Name]cryptMethod {
	cf, _ := enc["CF"].(Dict)
	out := make(map[Name]cryptMethod, len(cf))
	for name, v := range cf {
		d, _ := v.(Dict)
		switch cfm, _ := d["CFM"].(Name); cfm {
		case "V2":
			out[name] = methodRC4
		case "AESV2":
			out[name] = methodAESV2
		case "None", "":
			out[name] = methodIdentity
		default:
			out[name] = methodUnsupported
		}
	}
	return out
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

// pdfDocPassword encodes a password typed as a Go string for R2–R4, whose
// passwords are PDFDocEncoding bytes (ISO 32000-1 §7.6.3.3, Algorithm 2 step
// a; Annex D, Table D.2). It returns false when s holds a character
// PDFDocEncoding cannot represent, or is not valid UTF-8.
func pdfDocPassword(s string) ([]byte, bool) {
	if !utf8.ValidString(s) {
		return nil, false
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		b, ok := pdfDocByte(r)
		if !ok {
			return nil, false
		}
		out = append(out, b)
	}
	return out, true
}

// pdfDocByte returns the PDFDocEncoding code of r (Annex D, Table D.2): the
// codes that differ from Latin-1 are listed; 0x7F, 0x9F and 0xAD are
// undefined there, as are the Latin-1 characters those codes replace.
func pdfDocByte(r rune) (byte, bool) {
	for i, c := range pdfDocHigh {
		if c == r {
			return byte(0x80 + i), true
		}
	}
	for i, c := range pdfDocLow {
		if c == r {
			return byte(0x18 + i), true
		}
	}
	switch {
	case r == '€':
		return 0xA0, true
	case r < 0x18 || (r >= 0x20 && r < 0x7F) || (r > 0xA0 && r <= 0xFF && r != 0xAD):
		return byte(r), true
	}
	return 0, false
}

// pdfDocLow and pdfDocHigh are the PDFDocEncoding characters of codes
// 0x18–0x1F and 0x80–0x9E (ISO 32000-1 Annex D, Table D.2).
var (
	pdfDocLow  = []rune("˘ˇˆ˙˝˛˚˜")
	pdfDocHigh = []rune("•†‡…—–ƒ⁄‹›−‰„“”‘’‚™ﬁﬂŁŒŠŸŽıłœšž")
)

// objectKey derives the per-object key (Algorithm 1) for object num/gen,
// salted for AES-128 when the decryptor's method is AESV2.
func (d *decryptor) objectKey(num, gen int) []byte {
	return d.keyFor(d.method, num, gen)
}

// keyFor returns the key method m uses for object num/gen: the file key
// itself for AESV3 (Algorithm 1.A), otherwise Algorithm 1's MD5 of the file
// key, the object number and generation (and "sAlT" for AESV2).
func (d *decryptor) keyFor(m cryptMethod, num, gen int) []byte {
	if m == methodAESV3 {
		return d.key
	}
	h := md5.New()
	h.Write(d.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16)})
	h.Write([]byte{byte(gen), byte(gen >> 8)})
	if m == methodAESV2 {
		h.Write([]byte("sAlT"))
	}
	sum := h.Sum(nil)
	n := len(d.key) + 5
	if n > 16 {
		n = 16
	}
	return sum[:n]
}

// decryptBytes decrypts one payload for object num/gen with the decryptor's
// method. On any malformation it returns the input unchanged (lenient
// parsing, SECURITY.md §2).
func (d *decryptor) decryptBytes(num, gen int, data []byte) []byte {
	return d.decryptWith(d.method, num, gen, data)
}

// decryptWith decrypts data for object num/gen with method m; Identity and
// unsupported methods return it unchanged, and so does malformed data.
func (d *decryptor) decryptWith(m cryptMethod, num, gen int, data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	switch m {
	case methodAESV2, methodAESV3:
		return aesCBCDecrypt(d.keyFor(m, num, gen), data)
	case methodRC4:
		c, err := rc4.NewCipher(d.keyFor(m, num, gen))
		if err != nil {
			return data
		}
		out := make([]byte, len(data))
		c.XORKeyStream(out, data)
		return out
	default:
		return data
	}
}

// aesCBCDecrypt decrypts AES-CBC data prefixed with a 16-byte IV, stripping PKCS7
// padding. Malformed input is returned unchanged; an IV with no ciphertext
// after it decrypts to nothing (it indexed out[-1] and panicked before).
func aesCBCDecrypt(key, data []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil || len(data) < 16 || (len(data)-16)%aes.BlockSize != 0 {
		return data
	}
	iv, ct := data[:16], data[16:]
	if len(ct) == 0 {
		return []byte{}
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	if n := int(out[len(out)-1]); n >= 1 && n <= aes.BlockSize && n <= len(out) {
		out = out[:len(out)-n]
	}
	return out
}

// decryptObject returns obj with every String and Stream payload decrypted for
// object num/gen (recursively through arrays and dictionaries): strings with
// the string cipher, streams as streamMethod chooses. A signature
// dictionary's /Contents string is left as it is (isSignatureDict).
func (d *decryptor) decryptObject(num, gen int, obj Object) Object {
	switch v := obj.(type) {
	case String:
		return String{Value: d.decryptWith(d.str, num, gen, v.Value), Hex: v.Hex}
	case Array:
		for i, el := range v {
			v[i] = d.decryptObject(num, gen, el)
		}
		return v
	case Dict:
		sig := isSignatureDict(v)
		for k, el := range v {
			if _, isStr := el.(String); sig && isStr && k == "Contents" {
				continue
			}
			v[k] = d.decryptObject(num, gen, el)
		}
		return v
	case *Stream:
		if t, _ := v.Dict["Type"].(Name); t == "XRef" {
			// A cross-reference stream is the trailer's counterpart and is
			// read before decryption is set up: neither its data nor its
			// dictionary's strings (/ID) are encrypted.
			return v
		}
		v.Dict = d.decryptObject(num, gen, v.Dict).(Dict)
		v.Raw = d.decryptStream(num, gen, v)
		return v
	default:
		return obj
	}
}

// isSignatureDict reports whether d is a signature dictionary (ISO 32000-1
// §12.8.1, Table 252; its /Type is optional): /Type /Sig or /DocTimeStamp,
// or a /ByteRange array beside a /Contents string. Its /Contents is not
// decrypted (aes256-decryption.md §12 F3): the signature value is computed
// over the bytes /ByteRange names, and a reader verifies it as written.
// ISO 32000-1:2008 §7.6.1 lists, as the strings encryption does not apply
// to, the trailer /ID, the strings of the encryption dictionary and strings
// inside encrypted streams; it does not name the signature /Contents. The
// exemption is attributed to ISO 32000-2 by the design note, a clause not
// confirmed against the licensed text when this was written. Leaving the
// value as stored only keeps a signature for a future verifier; it never
// exposes content, since /Contents holds no page text.
func isSignatureDict(d Dict) bool {
	switch t, _ := d["Type"].(Name); t {
	case "Sig", "DocTimeStamp":
		return true
	}
	_, br := d["ByteRange"].(Array)
	_, c := d["Contents"].(String)
	return br && c
}

// decryptStream returns the decrypted payload of stream st of object
// num/gen. The cipher is, in order: the crypt filter a leading /Crypt stage
// names (ISO 32000-1 §7.4.10; its /DecodeParms /Name, /Identity when absent),
// whose stage is then dropped from the stream's filter chain when it was
// applied, even for a metadata stream under /EncryptMetadata false; none for
// a cross-reference stream (§7.5.8) and, with /EncryptMetadata false, for a
// /Type /Metadata stream without a /Crypt stage; /EFF for an embedded file;
// otherwise /StmF.
func (d *decryptor) decryptStream(num, gen int, st *Stream) []byte {
	m, hasCrypt := d.cryptFilter(st)
	typ, _ := st.Dict["Type"].(Name)
	switch {
	case typ == "XRef": // never encrypted (decryptObject)
		return st.Raw
	case hasCrypt:
		// A stream's own crypt filter wins over the document's defaults,
		// the metadata exemption included: /StmF is the filter used by
		// default for streams (ISO 32000-1 §7.6.5, Table 25), and a /Crypt
		// stage names the one this stream uses (§7.4.10). A cleartext
		// metadata stream written with /Crypt /Identity stays as it is; one
		// naming a real filter is decrypted with it.
	case typ == "Metadata" && d.metaClear:
		return st.Raw
	case typ == "EmbeddedFile":
		m = d.eff
	default:
		m = d.stm
	}
	return d.decryptWith(m, num, gen, st.Raw)
}

// cryptFilter reads a leading /Crypt stage of st's filter chain (V4 and V5
// only) and returns its cipher. A stage whose filter is Identity or one
// FluxDocs applies is removed from st.Dict (/Filter and the matching
// /DecodeParms entry), since the data it returns is decrypted; an unknown or
// unsupported filter leaves the stage in place and the data as it is, so the
// stream fails to decode instead of reading ciphertext as content.
func (d *decryptor) cryptFilter(st *Stream) (cryptMethod, bool) {
	if d.filters == nil {
		return 0, false
	}
	var parms Object
	switch f := st.Dict["Filter"].(type) {
	case Name:
		if f != "Crypt" {
			return 0, false
		}
		parms = st.Dict["DecodeParms"]
	case Array:
		if len(f) == 0 || f[0] != Name("Crypt") {
			return 0, false
		}
		if a, ok := st.Dict["DecodeParms"].(Array); ok && len(a) > 0 {
			parms = a[0]
		}
	default:
		return 0, false
	}
	name := Name("Identity")
	if p, ok := parms.(Dict); ok {
		if n, ok := p["Name"].(Name); ok {
			name = n
		}
	}
	m := methodIdentity
	if name != "Identity" {
		var ok bool
		if m, ok = d.filters[name]; !ok {
			m = methodUnsupported
		}
	}
	if m == methodUnsupported {
		return m, true
	}
	switch f := st.Dict["Filter"].(type) {
	case Name:
		delete(st.Dict, "Filter")
		delete(st.Dict, "DecodeParms")
	case Array:
		if len(f) == 1 {
			delete(st.Dict, "Filter")
			delete(st.Dict, "DecodeParms")
			break
		}
		st.Dict["Filter"] = append(Array(nil), f[1:]...)
		if a, ok := st.Dict["DecodeParms"].(Array); ok && len(a) > 0 {
			st.Dict["DecodeParms"] = append(Array(nil), a[1:]...)
		}
	}
	return m, true
}

// stringValue returns the bytes of a String object, or nil.
func stringValue(o Object) []byte {
	if s, ok := o.(String); ok {
		return s.Value
	}
	return nil
}

package parse

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
)

// crypt_v5.go is the AES-256 standard security handler (/V 5): revision 6
// (ISO 32000-2 §7.6.4) and revision 5, the Adobe Supplement to ISO 32000,
// ExtensionLevel 3 handler that R6 replaced (read only; ISO 32000-2
// deprecates it). Design: docs/design/aes256-decryption.md. Primitives:
// crypto/sha256, crypto/sha512, crypto/aes, crypto/cipher and crypto/subtle
// from the standard library only.
//
// Hostile-input bounds (design §7, SECURITY.md §2): every field length is
// checked before it is sliced and nothing in the dictionary sizes an
// allocation (the key is always 32 bytes, a password at most 127 bytes);
// Algorithm 2.B runs 64 to 287 rounds by construction and fails closed past
// hash2BMaxRounds (288); one open runs at most v5MaxHashes hashes.

const (
	// v5MaxPassword is the password length R5/R6 use: UTF-8 bytes, truncated
	// (Algorithm 2.A step a).
	v5MaxPassword = 127
	// hash2BMaxRounds bounds Algorithm 2.B: its stop test (hash2BStop) holds
	// after the 287th round at the latest, since the last byte of E is at
	// most 255 = 287 − 32 (design §3.1). The loop allows one more round as a
	// margin; past it the hash fails closed.
	hash2BMaxRounds = 288
	// v5MaxHashes bounds the hashes of one open: per candidate encoding of
	// the password, a user test, an owner test and the intermediate key;
	// two candidates (the SASLprep form and the raw UTF-8 fallback) and, for
	// an owner password, one more user test of the empty password (design
	// §7: one candidate needs at most 3, the fallback two more, and the
	// empty-password check one). core.OpenBytes shares one budget across its
	// repair ladder (DecryptionCache), so this bounds a whole open.
	v5MaxHashes = 6
)

// v5Hasher runs the R5/R6 password hash and counts its calls. hooks are for
// tests only: stopAt > 0 replaces 2.B's stop test so that exactly stopAt
// rounds run (a DoS gate forcing the 288-round worst case).
type v5Hasher struct {
	r      int // 5 or 6
	calls  int // hashes run
	rounds int // rounds of the last 2.B hash
	stopAt int // test hook; 0 in production
}

// hash returns the 32-byte hash of pw, the 8-byte salt and udata (the 48
// bytes of /U for an owner test or key, nil for a user one): one SHA-256 for
// R5, Algorithm 2.B for R6.
//
// Returns a wrapped errLex when more than v5MaxHashes hashes are asked for,
// or when 2.B does not stop within hash2BMaxRounds (unreachable).
func (h *v5Hasher) hash(pw, salt, udata []byte) ([]byte, error) {
	if h.calls >= v5MaxHashes {
		return nil, fmt.Errorf("%w: more than %d AES-256 password hashes", errLex, v5MaxHashes)
	}
	h.calls++
	s := sha256.New()
	s.Write(pw)
	s.Write(salt)
	s.Write(udata)
	k := s.Sum(nil)
	if h.r == 5 {
		return k, nil
	}
	return h.hash2B(pw, udata, k)
}

// hash2B is Algorithm 2.B (ISO 32000-2 §7.6.4.3.4) from its first K (the
// SHA-256 of pw, salt and udata). Each round builds K1, 64 copies of
// pw ‖ K ‖ udata, encrypts it with AES-128-CBC (key K[0:16], IV K[16:32], no
// padding) into E, and hashes E with SHA-256, -384 or -512 as the first 16
// bytes of E, a big-endian integer, are 0, 1 or 2 mod 3 — the sum of those
// bytes mod 3, since 256 ≡ 1 (mod 3). It stops as hash2BStop says. The
// result is K[0:32].
//
// The K1 buffer is allocated once: at most 64 × (127 + 64 + 48) = 15,296
// bytes.
func (h *v5Hasher) hash2B(pw, udata, first []byte) ([]byte, error) {
	buf := make([]byte, 0, 64*(len(pw)+sha512.Size+len(udata)))
	var kb [sha512.Size]byte // K, 32, 48 or 64 bytes, without an allocation per round
	k := kb[:copy(kb[:], first)]
	for round := 0; round < hash2BMaxRounds; round++ {
		e := buf[:0]
		for i := 0; i < 64; i++ {
			e = append(e, pw...)
			e = append(e, k...)
			e = append(e, udata...)
		}
		block, err := aes.NewCipher(k[:16])
		if err != nil {
			return nil, fmt.Errorf("%w: AES-128 key", errLex) // unreachable: K is ≥ 32 bytes
		}
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, e)
		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		switch sum % 3 {
		case 0:
			s := sha256.Sum256(e)
			k = kb[:copy(kb[:], s[:])]
		case 1:
			s := sha512.Sum384(e)
			k = kb[:copy(kb[:], s[:])]
		default:
			s := sha512.Sum512(e)
			k = kb[:copy(kb[:], s[:])]
		}
		h.rounds = round + 1
		stop := hash2BStop(round+1, e[len(e)-1])
		if h.stopAt > 0 {
			stop = round+1 == h.stopAt
		}
		if stop {
			return append([]byte(nil), k[:32]...), nil
		}
	}
	return nil, fmt.Errorf("%w: AES-256 password hash did not stop in %d rounds", errLex, hash2BMaxRounds)
}

// hash2BStop is Algorithm 2.B's stop test (ISO 32000-2 §7.6.4.3.4, steps e
// and f) after n rounds have run (n counts from 1): at least 64 rounds, and
// the last byte of the last E at most n − 32. The round number the standard
// compares with is the count of rounds run, the reading qpdf and Ghostscript
// accept: with the 0-based index instead (one less), about 5% of qpdf's R6
// files failed to open (docs/design/aes256-decryption.md §14). The longest
// chain is therefore 287 rounds (last byte 255).
func hash2BStop(n int, last byte) bool {
	return n >= 64 && int(last) <= n-32
}

// newV5Decryptor builds the decryptor of a /V 5 dictionary (R5 or R6): it
// validates the dictionary (design §2), retrieves the file key with
// Algorithm 2.A for the first candidate password that is the user or the
// owner password (each candidate tested as user first), and validates /Perms
// with Algorithm 13 (option B, design §5).
//
// Returns a wrapped errLex for a malformed or unsupported dictionary, a
// wrapped ErrWrongPassword when no candidate is the user or owner password,
// or a wrapped ErrEncryptionTampered when /Perms does not decrypt to "adb"
// with the file key.
func newV5Decryptor(enc Dict, cands [][]byte, h *v5Hasher) (*decryptor, error) {
	r, _ := enc.GetInt("R")
	o, u := stringValue(enc["O"]), stringValue(enc["U"])
	oe, ue, perms := stringValue(enc["OE"]), stringValue(enc["UE"]), stringValue(enc["Perms"])
	switch {
	case len(o) < 48:
		return nil, fmt.Errorf("%w: /O too short for /R %d", errLex, r)
	case len(u) < 48:
		return nil, fmt.Errorf("%w: /U too short for /R %d", errLex, r)
	case len(oe) < 32:
		return nil, fmt.Errorf("%w: /OE missing or too short", errLex)
	case len(ue) < 32:
		return nil, fmt.Errorf("%w: /UE missing or too short", errLex)
	case len(perms) < 16:
		return nil, fmt.Errorf("%w: /Perms missing or too short", errLex)
	}
	// Some writers pad /O and /U to 127 bytes: the first 48 are the value.
	o, u, oe, ue, perms = o[:48], u[:48], oe[:32], ue[:32], perms[:16]

	d, err := v5Filters(enc)
	if err != nil {
		return nil, err
	}
	h.r = int(r)
	p, _ := enc.GetInt("P")
	encMeta := true
	if b, ok := enc["EncryptMetadata"].(Boolean); ok {
		encMeta = bool(b)
	}
	d.info = DecryptionInfo{V: 5, R: int(r), Permissions: int32(p)}

	var key, used []byte
	for _, pw := range cands {
		pw = pw[:min(len(pw), v5MaxPassword)]
		var err error
		key, err = v5Unlock(h, pw, u[32:40], u[40:48], nil, u[:32], ue)
		if err != nil {
			return nil, err
		}
		if key != nil {
			used = pw
			break
		}
		key, err = v5Unlock(h, pw, o[32:40], o[40:48], u, o[:32], oe)
		if err != nil {
			return nil, err
		}
		if key != nil {
			used, d.info.Owner = pw, true
			break
		}
	}
	if key == nil {
		return nil, ErrWrongPassword
	}

	// Algorithm 13: /Perms, decrypted with the file key as one AES-256 block
	// (ECB), holds "adb" at bytes 9–11 only if the key is right. It is the
	// only check of the file key itself: /U and /O authenticate the
	// password, and a modified /UE or /OE yields a wrong key.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: AES-256 file key", errLex) // unreachable: 32 bytes
	}
	var pb [16]byte
	block.Decrypt(pb[:], perms)
	if pb[9] != 'a' || pb[10] != 'd' || pb[11] != 'b' {
		clear(key)
		return nil, ErrEncryptionTampered
	}
	// Option B: the authenticated permissions win over /P, and byte 8 over
	// /EncryptMetadata (neither of which is authenticated).
	d.info.Permissions = int32(binary.LittleEndian.Uint32(pb[0:4]))
	switch pb[8] {
	case 'T':
		encMeta = true
	case 'F':
		encMeta = false
	}
	d.info.EncryptMetadata = encMeta
	d.metaClear = !encMeta

	d.info.EmptyUserPassword = len(used) == 0
	if !d.info.EmptyUserPassword && d.info.Owner {
		// Does the empty password open it too (an owner password given for
		// a document anyone can open)? One more user test.
		hv, err := h.hash(nil, u[32:40], nil)
		if err != nil {
			clear(key)
			return nil, err
		}
		d.info.EmptyUserPassword = subtle.ConstantTimeCompare(hv, u[:32]) == 1
	}
	d.key = key
	return d, nil
}

// v5Unlock tests pw against a 32-byte validation hash (Algorithm 11 for the
// user, 12 for the owner): when Hash(pw, validation salt, udata) equals it,
// it returns the file key, the AES-256-CBC decryption (zero IV, no padding)
// of the 32-byte encrypted key ek with Hash(pw, key salt, udata) as the key
// (Algorithm 2.A steps c and e). It returns nil, nil when pw does not match.
// The comparison is constant-time; the intermediate key is cleared.
//
// Returns the hasher's error.
func v5Unlock(h *v5Hasher, pw, vsalt, ksalt, udata, want, ek []byte) ([]byte, error) {
	hv, err := h.hash(pw, vsalt, udata)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(hv, want) != 1 {
		return nil, nil
	}
	ik, err := h.hash(pw, ksalt, udata)
	if err != nil {
		return nil, err
	}
	defer clear(ik)
	block, err := aes.NewCipher(ik)
	if err != nil {
		return nil, fmt.Errorf("%w: AES-256 intermediate key", errLex) // unreachable: 32 bytes
	}
	key := make([]byte, 32)
	var iv [aes.BlockSize]byte
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(key, ek)
	return key, nil
}

// v5Filters reads the crypt filters of a V5 dictionary (design §2): each
// /CF entry with /CFM /AESV3 is AES-256, /None (or no /CFM, its default,
// ISO 32000-1 Table 25) is Identity, anything else is unsupported. /StmF and
// /StrF default to /Identity and must name /Identity or a filter of /CF that
// FluxDocs applies; /EFF defaults to /StmF's value, and one FluxDocs cannot
// apply only leaves embedded files undecrypted. The top-level and per-filter
// /Length are ignored: the key is 32 bytes.
//
// Returns a decryptor with its ciphers set, or a wrapped errLex when /StmF
// or /StrF names a missing or unsupported filter.
func v5Filters(enc Dict) (*decryptor, error) {
	cf, _ := enc["CF"].(Dict)
	filters := make(map[Name]cryptMethod, len(cf))
	for name, v := range cf {
		fd, _ := v.(Dict)
		switch cfm, _ := fd["CFM"].(Name); cfm {
		case "AESV3":
			filters[name] = methodAESV3
		case "None", "":
			filters[name] = methodIdentity
		default:
			filters[name] = methodUnsupported
		}
	}
	method := func(key Name, def Name) cryptMethod {
		name := def
		if n, ok := enc[key].(Name); ok {
			name = n
		}
		if name == "Identity" {
			return methodIdentity
		}
		if m, ok := filters[name]; ok {
			return m
		}
		return methodUnsupported
	}
	stmName := Name("Identity")
	if n, ok := enc["StmF"].(Name); ok {
		stmName = n
	}
	d := &decryptor{
		method:  methodAESV3,
		str:     method("StrF", "Identity"),
		stm:     method("StmF", "Identity"),
		eff:     method("EFF", stmName),
		filters: filters,
	}
	if d.str == methodUnsupported || d.stm == methodUnsupported {
		// The filter name is document bytes: keep it out of the message.
		return nil, fmt.Errorf("%w: /StmF or /StrF names a crypt filter that is missing or not AESV3", errLex)
	}
	return d, nil
}

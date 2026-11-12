package parse

// Test-only AES-256 encryptor (docs/design/aes256-decryption.md §11.1 option
// 3): the standard security handler's R6 password algorithms (ISO 32000-2
// §7.6.4.4: Algorithm 8 computes /U and /UE, Algorithm 9 /O and /OE,
// Algorithm 10 /Perms, with Algorithm 2.B as the hash) and the R5 variant
// (Adobe Supplement to ISO 32000, ExtensionLevel 3: one SHA-256 as the
// hash), written from the text rather than by calling crypt_v5.go, plus a
// document builder that encrypts strings and streams with AESV3. Being a
// _test.go file, none of it ships. Round trips through it alone could share
// a misreading of the standard with the decryptor, so the documents it
// writes are also read by Ghostscript, a separate program
// (crypt_v5_gs_test.go), and the committed fixtures in
// testing/corpus/encrypted were each read by Ghostscript first.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"math/big"
	"sort"
)

// AES256Security is an independent R5/R6 handler for building test
// documents. Passwords are the prepared bytes (UTF-8 after SASLprep, at most
// 127); an empty Owner means the user password. Every salt, the file key,
// the /Perms filler and every IV come from Seed, so a document is
// reproducible byte for byte.
type AES256Security struct {
	R               int // 5 or 6
	User, Owner     []byte
	P               int32
	EncryptMetadata bool
	Seed            string
	// PermsP, when non-nil, is the permission value written into /Perms
	// instead of P (a /P mismatch). PermsMeta, when non-zero, is byte 8 of
	// /Perms instead of 'T' or 'F' from EncryptMetadata.
	PermsP    *int32
	PermsMeta byte
	// UserSalt, when set, is the user validation salt instead of a seeded
	// one (a long 2.B chain, say).
	UserSalt []byte
}

// AES256Values are the encryption dictionary strings and the file key.
type AES256Values struct {
	O, U, OE, UE, Perms, Key []byte
}

// rnd returns n deterministic bytes for label: SHA-256 of seed, label and a
// counter.
func (s AES256Security) rnd(label string, n int) []byte {
	var out []byte
	for i := 0; len(out) < n; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", s.Seed, label, i)))
		out = append(out, sum[:]...)
	}
	return out[:n]
}

// Build computes /U, /UE (Algorithm 8), /O, /OE (Algorithm 9) and /Perms
// (Algorithm 10) for a file key drawn from the seed.
func (s AES256Security) Build() AES256Values {
	key := s.rnd("file key", 32)
	owner := s.Owner
	if len(owner) == 0 {
		owner = s.User
	}
	// Algorithm 8 (a): user validation salt and user key salt; /U is the
	// hash of the password and the validation salt, then both salts.
	uvs, uks := s.rnd("uvs", 8), s.rnd("uks", 8)
	if s.UserSalt != nil {
		uvs = s.UserSalt
	}
	u := append(append(s.isoHash(s.User, uvs, nil), uvs...), uks...)
	// (b): the hash of the password and the key salt encrypts the file key
	// (AES-256, CBC, no padding, zero IV).
	ue := aes256NoIV(s.isoHash(s.User, uks, nil), key)
	// Algorithm 9: the same with the owner password, its salts and the
	// 48-byte /U appended to every hash input.
	ovs, oks := s.rnd("ovs", 8), s.rnd("oks", 8)
	o := append(append(s.isoHash(owner, ovs, u), ovs...), oks...)
	oe := aes256NoIV(s.isoHash(owner, oks, u), key)
	// Algorithm 10: P extended to 64 bits with the upper 32 set, low-order
	// byte first; 'T' or 'F'; "adb"; 4 random bytes; AES-256 ECB with the
	// file key.
	p := s.P
	if s.PermsP != nil {
		p = *s.PermsP
	}
	var blk [16]byte
	binary.LittleEndian.PutUint64(blk[0:8], uint64(uint32(p))|0xFFFFFFFF00000000)
	blk[8] = 'F'
	if s.EncryptMetadata {
		blk[8] = 'T'
	}
	if s.PermsMeta != 0 {
		blk[8] = s.PermsMeta
	}
	copy(blk[9:12], "adb")
	copy(blk[12:16], s.rnd("perms", 4))
	c, _ := aes.NewCipher(key)
	perms := make([]byte, 16)
	c.Encrypt(perms, blk[:])
	return AES256Values{O: o, U: u, OE: oe, UE: ue, Perms: perms, Key: key}
}

func aes256NoIV(key, data []byte) []byte {
	c, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(c, make([]byte, 16)).CryptBlocks(out, data)
	return out
}

// isoHash is the R5 hash (one SHA-256 of password, salt and user key) or,
// for R6, Algorithm 2.B as ISO 32000-2 words it: K is the SHA-256 of the
// input; steps (a)–(d) run 64 times; then step (e) looks at the last byte
// of E and repeats (a)–(d) while it is greater than the number of rounds run
// minus 32. The mod-3 choice reads E[0:16] as a big-endian
// integer (math/big), not the byte-sum shortcut crypt_v5.go uses.
func (s AES256Security) isoHash(pw, salt, udata []byte) []byte {
	k, _ := isoHash2B(s.R, pw, salt, udata)
	return k
}

// isoHash2B returns the hash and, for R6, the number of rounds run.
func isoHash2B(r int, pw, salt, udata []byte) ([]byte, int) {
	return isoHash2BOff(r, pw, salt, udata, 0)
}

// isoHash2BOff is isoHash2B with the stop test compared against the round
// count minus off: off 1 is the off-by-one reading (the 0-based index of the
// last round), used only to show that a fixture separates the readings.
func isoHash2BOff(r int, pw, salt, udata []byte, off int) ([]byte, int) {
	in := append(append(append([]byte(nil), pw...), salt...), udata...)
	first := sha256.Sum256(in)
	k := first[:]
	if r == 5 {
		return k, 0
	}
	var e []byte
	round := 0
	step := func() {
		// (a) K1: 64 repetitions of password, K and the user key.
		var k1 []byte
		for i := 0; i < 64; i++ {
			k1 = append(k1, pw...)
			k1 = append(k1, k...)
			k1 = append(k1, udata...)
		}
		// (b) AES-128-CBC, key K[0:16], IV K[16:32], no padding.
		c, _ := aes.NewCipher(k[:16])
		e = make([]byte, len(k1))
		cipher.NewCBCEncrypter(c, k[16:32]).CryptBlocks(e, k1)
		// (c) the first 16 bytes of E, big-endian, mod 3.
		switch new(big.Int).Mod(new(big.Int).SetBytes(e[:16]), big.NewInt(3)).Int64() {
		case 0:
			h := sha256.Sum256(e)
			k = h[:]
		case 1:
			h := sha512.Sum384(e)
			k = h[:]
		default:
			h := sha512.Sum512(e)
			k = h[:]
		}
		round++
	}
	for round < 64 {
		step()
	}
	// (e)-(f): round counts the rounds run so far; keep going while the
	// last byte of E is greater than that count minus 32. (Comparing with
	// the 0-based index of the last round instead, one less, disagrees
	// with qpdf and Ghostscript on about 5% of chains.)
	for int(e[len(e)-1]) > round-32-off {
		step()
	}
	return k[:32], round
}

// V5Doc is a test document encrypted by an AES256Security handler. Objects
// are the plaintext objects by number; Root names the catalog. Strings are
// encrypted with /StrF and streams with /StmF (both StdCF, AESV3, unless
// set), except /Type /XRef streams and, without EncryptMetadata, /Type
// /Metadata streams; a stream whose /Filter starts with /Crypt uses the
// crypt filter its /DecodeParms /Name names. Objects listed in InObjStm are
// written into one object stream (itself encrypted) and the cross-reference
// data is then a stream; otherwise it is a classic table. Plain lists objects
// whose strings and stream data are written as given (a signature
// dictionary's /Contents, say). EncExtra is appended verbatim inside the
// encryption dictionary, and EncOverride replaces any of its computed
// strings (to forge a short or tampered value).
type V5Doc struct {
	Sec         AES256Security
	Objects     map[int]Object
	Root        int
	Info        int // the /Info object, or 0
	InObjStm    []int
	Plain       map[int]bool
	StmF, StrF  Name // "" means StdCF
	CFM         Name // the StdCF /CFM; "" means AESV3
	EncExtra    string
	EncOverride map[Name][]byte
	NoID        bool
}

// Bytes writes the document and returns it.
func (d V5Doc) Bytes() []byte {
	vals := d.Sec.Build()
	for k, v := range d.EncOverride {
		switch k {
		case "O":
			vals.O = v
		case "U":
			vals.U = v
		case "OE":
			vals.OE = v
		case "UE":
			vals.UE = v
		case "Perms":
			vals.Perms = v
		}
	}
	stmF, strF, cfm := d.StmF, d.StrF, d.CFM
	if stmF == "" {
		stmF = "StdCF"
	}
	if strF == "" {
		strF = "StdCF"
	}
	if cfm == "" {
		cfm = "AESV3"
	}
	ivn := 0
	encrypt := func(filter Name, data []byte) []byte {
		if filter == "Identity" || cfm != "AESV3" {
			return data
		}
		ivn++
		iv := d.Sec.rnd(fmt.Sprintf("iv%d", ivn), 16)
		n := 16 - len(data)%16
		padded := append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(n)}, n)...)
		c, _ := aes.NewCipher(vals.Key)
		out := make([]byte, len(padded))
		cipher.NewCBCEncrypter(c, iv).CryptBlocks(out, padded)
		return append(append([]byte(nil), iv...), out...)
	}
	var encObj func(o Object) Object
	encObj = func(o Object) Object {
		switch v := o.(type) {
		case String:
			return String{Value: encrypt(strF, v.Value), Hex: true}
		case Array:
			out := make(Array, len(v))
			for i, e := range v {
				out[i] = encObj(e)
			}
			return out
		case Dict:
			out := Dict{}
			for k, e := range v {
				out[k] = encObj(e)
			}
			return out
		case *Stream:
			sd := encObj(v.Dict).(Dict)
			filter := stmF
			typ, _ := v.Dict["Type"].(Name)
			if typ == "XRef" || (typ == "Metadata" && !d.Sec.EncryptMetadata) {
				filter = "Identity"
			}
			// A /Crypt stage names this stream's filter, over the defaults.
			if f, ok := v.Dict["Filter"].(Name); ok && f == "Crypt" {
				filter = "Identity"
				if p, ok := v.Dict["DecodeParms"].(Dict); ok {
					if n, ok := p["Name"].(Name); ok {
						filter = n
					}
				}
			}
			raw := encrypt(filter, v.Raw)
			sd["Length"] = Integer(len(raw))
			return &Stream{Dict: sd, Raw: raw}
		default:
			return o
		}
	}

	objs := map[int][]byte{}
	maxNum := 0
	for n := range d.Objects {
		maxNum = max(maxNum, n)
	}
	encNum := maxNum + 1
	inStm := map[int]bool{}
	for _, n := range d.InObjStm {
		inStm[n] = true
	}
	for n, o := range d.Objects {
		if inStm[n] {
			continue
		}
		if d.Plain[n] {
			objs[n] = SerializeObject(o)
		} else {
			objs[n] = SerializeObject(encObj(o))
		}
	}
	enc := fmt.Sprintf("<< /Filter /Standard /V 5 /R %d /Length 256 /P %d /O <%X> /U <%X> /OE <%X> /UE <%X> /Perms <%X>"+
		" /CF << /StdCF << /CFM /%s /AuthEvent /DocOpen /Length 32 >> >> /StmF /%s /StrF /%s",
		d.Sec.R, d.Sec.P, vals.O, vals.U, vals.OE, vals.UE, vals.Perms, string(cfm), string(stmF), string(strF))
	if !d.Sec.EncryptMetadata {
		enc += " /EncryptMetadata false"
	}
	enc += " " + d.EncExtra + " >>"
	objs[encNum] = []byte(enc)

	id := d.Sec.rnd("id", 16)
	trailerExtra := ""
	if !d.NoID {
		trailerExtra = fmt.Sprintf(" /ID [<%X> <%X>]", id, id)
	}
	if d.Info != 0 {
		trailerExtra += fmt.Sprintf(" /Info %d 0 R", d.Info)
	}
	var out bytes.Buffer
	out.WriteString("%PDF-2.0\n%\xE2\xE3\xCF\xD3\n")
	offsets := map[int]int{}
	nums := make([]int, 0, len(objs))
	for n := range objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	if len(d.InObjStm) == 0 {
		size := encNum + 1
		xref := out.Len()
		fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", size)
		for n := 1; n < size; n++ {
			if off, ok := offsets[n]; ok {
				fmt.Fprintf(&out, "%010d 00000 n \n", off)
			} else {
				out.WriteString("0000000000 65535 f \n")
			}
		}
		fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R /Encrypt %d 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
			size, d.Root, encNum, trailerExtra, xref)
		return out.Bytes()
	}

	// The object stream: "num offset" pairs, then the plain objects; the
	// stream as a whole is encrypted (strings inside it are not).
	stmNum := encNum + 1
	var hdr, body bytes.Buffer
	sorted := append([]int(nil), d.InObjStm...)
	sort.Ints(sorted)
	idx := map[int]int{}
	for i, n := range sorted {
		idx[n] = i
		fmt.Fprintf(&hdr, "%d %d ", n, body.Len())
		body.Write(SerializeObject(d.Objects[n]))
		body.WriteByte('\n')
	}
	osRaw := append(append([]byte(nil), hdr.Bytes()...), body.Bytes()...)
	osStream := encObj(&Stream{Dict: Dict{"Type": Name("ObjStm"), "N": Integer(len(sorted)), "First": Integer(hdr.Len())}, Raw: osRaw})
	offsets[stmNum] = out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", stmNum, SerializeObject(osStream))

	xrefNum := stmNum + 1
	size := xrefNum + 1
	var rows bytes.Buffer
	for n := 0; n < size; n++ {
		switch off, ok := offsets[n]; {
		case n == xrefNum:
			rows.Write([]byte{1})
			rows.Write(be32(out.Len()))
			rows.Write([]byte{0, 0})
		case ok:
			rows.Write([]byte{1})
			rows.Write(be32(off))
			rows.Write([]byte{0, 0})
		case inStm[n]:
			rows.Write([]byte{2})
			rows.Write(be32(stmNum))
			rows.Write([]byte{byte(idx[n] >> 8), byte(idx[n])})
		default:
			rows.Write([]byte{0, 0, 0, 0, 0, 0xFF, 0xFF})
		}
	}
	xref := out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root %d 0 R /Encrypt %d 0 R%s /Length %d >>\nstream\n",
		xrefNum, size, d.Root, encNum, trailerExtra, rows.Len())
	out.Write(rows.Bytes())
	fmt.Fprintf(&out, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes()
}

func be32(n int) []byte {
	return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

// V5OnePage returns the objects of a one-page document whose page shows
// marker in Helvetica, with an /Info /Title string (a string the handler
// encrypts) and, when meta is set, a /Metadata stream holding metaText.
// Objects: 1 catalog, 2 pages, 3 page, 4 content, 5 font, 6 info, 7 metadata.
func V5OnePage(marker string, meta bool, metaText string) map[int]Object {
	content := fmt.Sprintf("BT /F1 18 Tf 20 100 Td (%s) Tj ET", marker)
	cat := Dict{"Type": Name("Catalog"), "Pages": Reference{Number: 2}}
	objs := map[int]Object{
		1: cat,
		2: Dict{"Type": Name("Pages"), "Kids": Array{Reference{Number: 3}}, "Count": Integer(1), "MediaBox": Array{Integer(0), Integer(0), Integer(400), Integer(200)}},
		3: Dict{"Type": Name("Page"), "Parent": Reference{Number: 2}, "Contents": Reference{Number: 4},
			"Resources": Dict{"Font": Dict{"F1": Reference{Number: 5}}}},
		4: &Stream{Dict: Dict{}, Raw: []byte(content)},
		5: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica")},
		6: Dict{"Title": String{Value: []byte("Title " + marker)}},
	}
	if meta {
		cat["Metadata"] = Reference{Number: 7}
		objs[7] = &Stream{Dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML")}, Raw: []byte(metaText)}
	}
	return objs
}

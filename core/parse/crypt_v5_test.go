package parse

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// v5Enc returns the encryption dictionary AES256Security sec builds, as
// newDecryptor reads it, with the file key.
func v5Enc(sec AES256Security) (Dict, []byte) {
	v := sec.Build()
	d := Dict{
		"Filter": Name("Standard"), "V": Integer(5), "R": Integer(sec.R), "Length": Integer(256),
		"P": Integer(sec.P), "O": String{Value: v.O}, "U": String{Value: v.U},
		"OE": String{Value: v.OE}, "UE": String{Value: v.UE}, "Perms": String{Value: v.Perms},
		"CF":   Dict{"StdCF": Dict{"CFM": Name("AESV3"), "Length": Integer(32)}},
		"StmF": Name("StdCF"), "StrF": Name("StdCF"),
	}
	if !sec.EncryptMetadata {
		d["EncryptMetadata"] = Boolean(false)
	}
	return d, v.Key
}

// TestHash2B_Rounds is the Algorithm 2.B property test (design §11.2): over
// random salts and passwords of 0, 1, 127 bytes (and 200, truncated to 127
// by the caller), with and without the 48-byte user key, the hash is 32
// bytes, ran between 64 and 288 rounds, and equals the test-only encryptor's
// independent reading of the algorithm, round count included.
func TestHash2B_Rounds(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 160; i++ {
		pw := make([]byte, []int{0, 1, 127, 200}[i%4])
		rng.Read(pw)
		pw = pw[:min(len(pw), v5MaxPassword)]
		salt := make([]byte, 8)
		rng.Read(salt)
		var udata []byte
		if i%2 == 1 {
			udata = make([]byte, 48)
			rng.Read(udata)
		}
		h := &v5Hasher{r: 6}
		got, err := h.hash(pw, salt, udata)
		if err != nil {
			t.Fatal(err)
		}
		want, rounds := isoHash2B(6, pw, salt, udata)
		if len(got) != 32 || !bytes.Equal(got, want) {
			t.Fatalf("case %d: hash = %X, want %X", i, got, want)
		}
		if h.rounds < 64 || h.rounds > hash2BMaxRounds || h.rounds != rounds {
			t.Fatalf("case %d: %d rounds (independent reading: %d), want 64..288", i, h.rounds, rounds)
		}
	}
}

// TestHash2B_LongChain pins the round count of a salt found by an offline
// search for long chains (empty password, user test): far past the 64-round
// minimum, so the stop test's "last byte ≤ round − 32" is exercised many
// times, and equal in both readings of the algorithm.
func TestHash2B_LongChain(t *testing.T) {
	salt := longChainSalt
	h := &v5Hasher{r: 6}
	got, err := h.hash(nil, salt, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, rounds := isoHash2B(6, nil, salt, nil)
	if !bytes.Equal(got, want) || h.rounds != longChainRounds || rounds != longChainRounds {
		t.Fatalf("rounds = %d (independent %d), want %d; equal %v", h.rounds, rounds, longChainRounds, bytes.Equal(got, want))
	}
}

// TestHash2BStop pins Algorithm 2.B's stop test at its boundaries
// (hash2BStop): never before 64 rounds; after n rounds, the last byte of E
// must be at most n − 32, n counting from 1. The off-by-one reading (the
// 0-based index, n − 33) opened about 5% of qpdf's R6 files with the wrong
// key; it fails the "exactly n − 32" rows below.
func TestHash2BStop(t *testing.T) {
	for _, tc := range []struct {
		n    int
		last byte
		want bool
	}{
		{63, 0, false}, // 64 rounds are the minimum
		{64, 31, true},
		{64, 32, true}, // exactly n − 32
		{64, 33, false},
		{65, 33, true},  // exactly n − 32
		{100, 68, true}, // exactly n − 32
		{100, 69, false},
		{286, 254, true}, // exactly n − 32
		{286, 255, false},
		{287, 255, true}, // the longest chain: 287 rounds
	} {
		if got := hash2BStop(tc.n, tc.last); got != tc.want {
			t.Errorf("hash2BStop(%d, %d) = %v, want %v", tc.n, tc.last, got, tc.want)
		}
	}
	// No byte keeps a chain going past 287 rounds, inside the loop's cap.
	for b := 0; b < 256; b++ {
		if !hash2BStop(287, byte(b)) || 287 >= hash2BMaxRounds+1 {
			t.Fatalf("a chain could run past 287 rounds (last byte %d)", b)
		}
	}
}

// TestHash_R5IsOneSHA256: the R5 hash is SHA-256(password ‖ salt ‖ udata),
// one call, no rounds.
func TestHash_R5IsOneSHA256(t *testing.T) {
	h := &v5Hasher{r: 5}
	u := bytes.Repeat([]byte{7}, 48)
	got, err := h.hash([]byte("pw"), []byte("saltsalt"), u)
	want := sha256.Sum256(append([]byte("pwsaltsalt"), u...))
	if err != nil || !bytes.Equal(got, want[:]) || h.calls != 1 || h.rounds != 0 {
		t.Fatalf("hash = %X, %v (calls %d, rounds %d); want %X", got, err, h.calls, h.rounds, want)
	}
}

// TestHash2B_StopHookAndBound: the test hook forces exactly n rounds, and a
// hasher refuses a hash past v5MaxHashes (fail closed).
func TestHash2B_StopHookAndBound(t *testing.T) {
	h := &v5Hasher{r: 6, stopAt: hash2BMaxRounds}
	if _, err := h.hash(nil, make([]byte, 8), nil); err != nil || h.rounds != hash2BMaxRounds {
		t.Fatalf("forced: %d rounds, %v; want %d", h.rounds, err, hash2BMaxRounds)
	}
	h = &v5Hasher{r: 5}
	for i := 0; i < v5MaxHashes; i++ {
		if _, err := h.hash(nil, nil, nil); err != nil {
			t.Fatalf("hash %d: %v", i+1, err)
		}
	}
	if _, err := h.hash(nil, nil, nil); err == nil || !errors.Is(err, errLex) {
		t.Fatalf("hash %d: err = %v, want the bound", v5MaxHashes+1, err)
	}
}

// TestV5_Passwords is Algorithm 2.A on documents the test-only encryptor
// builds (R5 and R6): the empty, user, owner and wrong passwords; the owner
// and user paths yield the same file key; the role and whether the empty
// password opens the document are recorded; the hashes stay within the
// bound.
func TestV5_Passwords(t *testing.T) {
	for _, r := range []int{5, 6} {
		for _, tc := range []struct {
			name, user, owner, given string
			wantErr                  error
			owner2, emptyOK          bool
			maxHashes                int
		}{
			{"empty user, owner set, empty given", "", "owner", "", nil, false, true, 2},
			{"empty user, owner given", "", "owner", "owner", nil, true, true, 5},
			{"user set, empty given", "user", "owner", "", ErrWrongPassword, false, false, 2},
			{"user set, user given", "user", "owner", "user", nil, false, false, 2},
			{"user set, owner given", "user", "owner", "owner", nil, true, false, 5},
			{"user set, wrong given", "user", "owner", "wrong", ErrWrongPassword, false, false, 2},
			{"same user and owner", "same", "same", "same", nil, false, false, 2},
			{"127-byte user password", strings.Repeat("p", 127), "owner", strings.Repeat("p", 127), nil, false, false, 2},
		} {
			t.Run(fmt.Sprintf("R%d/%s", r, tc.name), func(t *testing.T) {
				sec := AES256Security{R: r, User: []byte(tc.user), Owner: []byte(tc.owner), P: -3904, EncryptMetadata: true, Seed: tc.name}
				enc, key := v5Enc(sec)
				h := &v5Hasher{}
				d, err := newDecryptorFor(enc, nil, [][]byte{[]byte(tc.given)}, h)
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) || d != nil {
						t.Fatalf("err = %v, want %v", err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(d.key, key) {
					t.Fatalf("file key = %X, want %X", d.key, key)
				}
				if d.info.Owner != tc.owner2 || d.info.EmptyUserPassword != tc.emptyOK || d.info.R != r {
					t.Errorf("info = %+v, want owner %v, empty user password %v", d.info, tc.owner2, tc.emptyOK)
				}
				if h.calls > tc.maxHashes {
					t.Errorf("%d hashes, want \u2264 %d", h.calls, tc.maxHashes)
				}
				if d.str != methodAESV3 || d.stm != methodAESV3 || d.metaClear {
					t.Errorf("ciphers %v %v, metaClear %v", d.str, d.stm, d.metaClear)
				}
			})
		}
	}
}

// TestV5_Perms pins /Perms option B (design §5): a /Perms that does not
// decrypt to "adb" with the file key, and a modified /UE, are
// ErrEncryptionTampered; a /P mismatch is accepted and the authenticated
// value recorded; byte 8 overrides /EncryptMetadata either way.
func TestV5_Perms(t *testing.T) {
	other := int32(-4)
	for _, tc := range []struct {
		name     string
		sec      AES256Security
		mutate   func(Dict)
		wantErr  error
		wantP    int32
		wantMeta bool
	}{
		{"valid", AES256Security{R: 6, P: -3904, EncryptMetadata: true}, nil, nil, -3904, true},
		{"/Perms random", AES256Security{R: 6, P: -3904, EncryptMetadata: true},
			func(d Dict) { d["Perms"] = String{Value: bytes.Repeat([]byte{0x42}, 16)} }, ErrEncryptionTampered, 0, false},
		{"/UE modified", AES256Security{R: 6, P: -3904, EncryptMetadata: true},
			func(d Dict) {
				ue := append([]byte(nil), stringValue(d["UE"])...)
				ue[3] ^= 1
				d["UE"] = String{Value: ue}
			}, ErrEncryptionTampered, 0, false},
		{"/P mismatch: /Perms wins", AES256Security{R: 6, P: -3904, PermsP: &other, EncryptMetadata: true}, nil, nil, -4, true},
		{"/P written unsigned", AES256Security{R: 6, P: -4, EncryptMetadata: true},
			func(d Dict) { d["P"] = Integer(4294967292) }, nil, -4, true},
		{"byte 8 F overrides /EncryptMetadata true", AES256Security{R: 6, P: -4, EncryptMetadata: true, PermsMeta: 'F'}, nil, nil, -4, false},
		{"byte 8 T overrides /EncryptMetadata false", AES256Security{R: 6, P: -4, EncryptMetadata: false, PermsMeta: 'T'}, nil, nil, -4, true},
		{"byte 8 other: /EncryptMetadata false kept", AES256Security{R: 6, P: -4, EncryptMetadata: false, PermsMeta: 'X'}, nil, nil, -4, false},
		{"R5 valid", AES256Security{R: 5, P: -44, EncryptMetadata: true}, nil, nil, -44, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.sec.Owner, tc.sec.Seed = []byte("owner"), tc.name
			enc, _ := v5Enc(tc.sec)
			if tc.mutate != nil {
				tc.mutate(enc)
			}
			d, err := newDecryptor(enc, nil, nil)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || d != nil {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.info.Permissions != tc.wantP || d.info.EncryptMetadata != tc.wantMeta || d.metaClear == tc.wantMeta {
				t.Errorf("info = %+v (metaClear %v), want P %d, EncryptMetadata %v", d.info, d.metaClear, tc.wantP, tc.wantMeta)
			}
		})
	}
}

// TestNewDecryptor_V5Hostile (design §11.2): malformed or unsupported V5
// dictionaries fail with an error, never a panic, and allocate nothing sized
// by the file; /Length and an out-of-range /P are ignored.
func TestNewDecryptor_V5Hostile(t *testing.T) {
	base := func() Dict {
		d, _ := v5Enc(AES256Security{R: 6, Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "hostile"})
		return d
	}
	short := func(key Name, n int) func(Dict) {
		return func(d Dict) { d[key] = String{Value: stringValue(d[key])[:n]} }
	}
	for _, tc := range []struct {
		name   string
		mutate func(Dict)
		wantOK bool
	}{
		{"control", func(Dict) {}, true},
		{"/O 0 bytes", short("O", 0), false},
		{"/O 32 bytes", short("O", 32), false},
		{"/O 47 bytes", short("O", 47), false},
		{"/U 47 bytes", short("U", 47), false},
		{"/U missing", func(d Dict) { delete(d, "U") }, false},
		{"/OE 31 bytes", short("OE", 31), false},
		{"/UE missing", func(d Dict) { delete(d, "UE") }, false},
		{"/Perms 15 bytes", short("Perms", 15), false},
		{"/Perms missing", func(d Dict) { delete(d, "Perms") }, false},
		{"/O of 1 MiB: only 48 bytes read", func(d Dict) { d["O"] = String{Value: append(stringValue(d["O"]), make([]byte, 1<<20)...)} }, true},
		{"/O padded to 127 bytes", func(d Dict) { d["O"] = String{Value: append(stringValue(d["O"]), make([]byte, 79)...)} }, true},
		{"V5 with R4", func(d Dict) { d["R"] = Integer(4) }, false},
		{"V4 with R6", func(d Dict) { d["V"] = Integer(4) }, false},
		{"R7", func(d Dict) { d["R"] = Integer(7) }, false},
		{"unknown /CFM in use", func(d Dict) { d["CF"] = Dict{"StdCF": Dict{"CFM": Name("AESV9")}} }, false},
		{"/StmF names a missing filter", func(d Dict) { d["StmF"] = Name("Nope") }, false},
		{"/StrF names a missing filter", func(d Dict) { d["StrF"] = Name("Nope") }, false},
		{"/CF not a dictionary", func(d Dict) { d["CF"] = Integer(1) }, false},
		{"/EFF names a missing filter (only embedded files)", func(d Dict) { d["EFF"] = Name("Nope") }, true},
		{"/Length 2^62 ignored", func(d Dict) { d["Length"] = Integer(1 << 62) }, true},
		{"/P outside int32", func(d Dict) { d["P"] = Integer(1 << 40) }, true},
		{"/P not an integer", func(d Dict) { d["P"] = Name("x") }, true},
		{"/O not a string", func(d Dict) { d["O"] = Integer(5) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc := base()
			tc.mutate(enc)
			d, err := newDecryptor(enc, nil, nil)
			if (err == nil) != tc.wantOK {
				t.Fatalf("err = %v, wantOK %v", err, tc.wantOK)
			}
			if err != nil && (d != nil || !errors.Is(err, errLex)) {
				t.Errorf("d = %v, err = %v; want nil and a wrapped errLex", d, err)
			}
			// Nothing the dictionary holds sizes an allocation: the K1
			// buffer and the rounds' work are fixed, whatever /O, /U or
			// /Length say.
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			newDecryptor(enc, nil, nil)
			runtime.ReadMemStats(&after)
			if n := after.TotalAlloc - before.TotalAlloc; n > 256<<10 {
				t.Errorf("%d bytes allocated, want a fixed bound (256 KiB)", n)
			}
		})
	}
	// Filters: /Identity and /None leave data as it is.
	enc := base()
	enc["StrF"] = Name("Identity")
	enc["CF"] = Dict{"StdCF": Dict{"CFM": Name("AESV3")}, "Clear": Dict{"CFM": Name("None")}}
	enc["StmF"] = Name("Clear")
	d, err := newDecryptor(enc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.str != methodIdentity || d.stm != methodIdentity || d.eff != methodIdentity || d.filters["StdCF"] != methodAESV3 {
		t.Errorf("ciphers str %v stm %v eff %v filters %v", d.str, d.stm, d.eff, d.filters)
	}
}

// openV5 builds doc and opens it with password through
// EnableDecryptionPassword.
func openV5(t *testing.T, doc V5Doc, password string) *Resolver {
	t.Helper()
	r, err := NewResolver(doc.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnableDecryptionPassword(password); err != nil {
		t.Fatalf("EnableDecryptionPassword: %v", err)
	}
	return r
}

// streamOf resolves object num to a stream and returns its decoded data.
func streamOf(t *testing.T, r *Resolver, num int) []byte {
	t.Helper()
	o, err := r.Resolve(Reference{Number: num})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := o.(*Stream)
	if !ok {
		t.Fatalf("object %d is %T", num, o)
	}
	data, err := DecodeStream(st)
	if err != nil {
		t.Fatalf("object %d: %v", num, err)
	}
	return data
}

// TestV5_ObjectDecryption covers design §6 on whole documents: strings
// (/StrF) and streams (/StmF) under AESV3, /Identity for either, an
// encrypted object stream with a cross-reference stream (not decrypted),
// /EncryptMetadata false, /Crypt stream filters, and a signature
// dictionary's /Contents, which is not decrypted.
func TestV5_ObjectDecryption(t *testing.T) {
	const marker = "AES256-OK-unit"
	sec := AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "objects"}
	check := func(t *testing.T, r *Resolver) {
		t.Helper()
		pages, err := r.Pages()
		if err != nil || len(pages) != 1 {
			t.Fatalf("Pages: %d, %v", len(pages), err)
		}
		if got := contentText(t, r, pages[0]); !strings.Contains(got, marker) {
			t.Errorf("content = %q, want the marker", got)
		}
		info, _, err := r.ResolveDict(Reference{Number: 6})
		if err != nil || string(stringValue(info["Title"])) != "Title "+marker {
			t.Errorf("/Title = %q, %v", stringValue(info["Title"]), err)
		}
	}
	t.Run("strings and streams", func(t *testing.T) {
		check(t, openV5(t, V5Doc{Sec: sec, Objects: V5OnePage(marker, false, ""), Root: 1, Info: 6}, "user"))
	})
	for _, f := range []struct{ stm, str Name }{{"Identity", "StdCF"}, {"StdCF", "Identity"}, {"Identity", "Identity"}} {
		t.Run(fmt.Sprintf("/StmF %s /StrF %s", f.stm, f.str), func(t *testing.T) {
			r := openV5(t, V5Doc{Sec: sec, Objects: V5OnePage(marker, false, ""), Root: 1, Info: 6, StmF: f.stm, StrF: f.str}, "user")
			check(t, r)
		})
	}
	t.Run("object stream and cross-reference stream", func(t *testing.T) {
		doc := V5Doc{Sec: sec, Objects: V5OnePage(marker, false, ""), Root: 1, Info: 6, InObjStm: []int{1, 2, 3, 5, 6}}
		r := openV5(t, doc, "owner")
		check(t, r)
		// The cross-reference stream (object 10: 7 /Encrypt, 8 ObjStm, 9...)
		// resolves to its raw, unencrypted rows.
		x, err := r.Resolve(Reference{Number: 9})
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := x.(*Stream); !ok || len(st.Raw)%7 != 0 || st.Raw[0] != 0 {
			t.Errorf("cross-reference stream = %v, want its 7-byte rows unchanged", x)
		}
	})
	t.Run("EncryptMetadata false", func(t *testing.T) {
		s := sec
		s.EncryptMetadata = false
		r := openV5(t, V5Doc{Sec: s, Objects: V5OnePage(marker, true, "<x:xmpmeta>clear</x:xmpmeta>"), Root: 1, Info: 6}, "user")
		check(t, r)
		if got := streamOf(t, r, 7); string(got) != "<x:xmpmeta>clear</x:xmpmeta>" {
			t.Errorf("metadata = %q, want it unchanged", got)
		}
	})
	t.Run("EncryptMetadata false: a /Crypt stage on the metadata stream wins", func(t *testing.T) {
		s := sec
		s.EncryptMetadata = false
		objs := V5OnePage(marker, true, "<x:xmpmeta>clear</x:xmpmeta>")
		// 7 is cleartext metadata (no /Crypt); 8 a metadata stream whose
		// /Crypt stage names StdCF, so it is encrypted; 9 one naming
		// /Identity, stored as it is.
		objs[8] = &Stream{Dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML"), "Filter": Name("Crypt"), "DecodeParms": Dict{"Name": Name("StdCF")}}, Raw: []byte("<x:xmpmeta>stdcf</x:xmpmeta>")}
		objs[9] = &Stream{Dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML"), "Filter": Name("Crypt"), "DecodeParms": Dict{"Name": Name("Identity")}}, Raw: []byte("<x:xmpmeta>identity</x:xmpmeta>")}
		r := openV5(t, V5Doc{Sec: s, Objects: objs, Root: 1, Info: 6}, "user")
		for n, want := range map[int]string{7: "<x:xmpmeta>clear</x:xmpmeta>", 8: "<x:xmpmeta>stdcf</x:xmpmeta>", 9: "<x:xmpmeta>identity</x:xmpmeta>"} {
			if got := streamOf(t, r, n); string(got) != want {
				t.Errorf("metadata %d = %q, want %q", n, got, want)
			}
		}
	})
	t.Run("EncryptMetadata true decrypts the metadata stream", func(t *testing.T) {
		r := openV5(t, V5Doc{Sec: sec, Objects: V5OnePage(marker, true, "<x:xmpmeta>enc</x:xmpmeta>"), Root: 1, Info: 6}, "user")
		if got := streamOf(t, r, 7); string(got) != "<x:xmpmeta>enc</x:xmpmeta>" {
			t.Errorf("metadata = %q", got)
		}
	})
	t.Run("/Crypt filters", func(t *testing.T) {
		objs := V5OnePage(marker, false, "")
		// 8: /Crypt /Identity, stored as it is. 9: [/Crypt /ASCIIHexDecode]
		// with /Name /StdCF, encrypted. 10: /Crypt naming an unknown filter.
		objs[8] = &Stream{Dict: Dict{"Filter": Name("Crypt"), "DecodeParms": Dict{"Type": Name("CryptFilterDecodeParms"), "Name": Name("Identity")}}, Raw: []byte("identity data")}
		objs[9] = &Stream{Dict: Dict{"Filter": Array{Name("Crypt"), Name("ASCIIHexDecode")}, "DecodeParms": Array{Dict{"Name": Name("StdCF")}, Null{}}}, Raw: []byte("48656C6C6F>")}
		objs[10] = &Stream{Dict: Dict{"Filter": Name("Crypt"), "DecodeParms": Dict{"Name": Name("Missing")}}, Raw: []byte("x")}
		r := openV5(t, V5Doc{Sec: sec, Objects: objs, Root: 1, Info: 6}, "user")
		if got := streamOf(t, r, 8); string(got) != "identity data" {
			t.Errorf("/Crypt /Identity = %q", got)
		}
		if got := streamOf(t, r, 9); string(got) != "Hello" {
			t.Errorf("[/Crypt /ASCIIHexDecode] = %q", got)
		}
		o, _ := r.Resolve(Reference{Number: 10})
		if _, err := DecodeStream(o.(*Stream)); err == nil {
			t.Error("a /Crypt filter naming a missing crypt filter decoded; want it to fail")
		}
	})
	t.Run("signature /Contents is not decrypted", func(t *testing.T) {
		objs := V5OnePage(marker, false, "")
		sig := []byte{0x30, 0x82, 0x01, 0x02, 0xAA}
		objs[8] = Dict{"Type": Name("Sig"), "ByteRange": Array{Integer(0), Integer(1), Integer(2), Integer(3)}, "Contents": String{Value: sig, Hex: true}}
		objs[9] = Dict{"ByteRange": Array{Integer(0), Integer(1)}, "Contents": String{Value: sig, Hex: true}} // /Type is optional
		r := openV5(t, V5Doc{Sec: sec, Objects: objs, Root: 1, Info: 6, Plain: map[int]bool{8: true, 9: true}}, "user")
		for _, n := range []int{8, 9} {
			d, _, err := r.ResolveDict(Reference{Number: n})
			if err != nil || !bytes.Equal(stringValue(d["Contents"]), sig) {
				t.Errorf("object %d /Contents = %X, %v; want it as stored", n, stringValue(d["Contents"]), err)
			}
		}
	})
}

// TestEncryptMetadataFalse_V4: with /EncryptMetadata false a V4 (AESV2)
// metadata stream is stored unencrypted and read as it is; the content stream
// is still decrypted.
func TestEncryptMetadataFalse_V4(t *testing.T) {
	id := []byte("v4-meta-file-id!")
	sec := StdSecurity{R: 4, KeyLen: 16, Owner: []byte("owner"), P: -4, ID: id, EncryptMetadata: false}
	o, u, key := sec.Build()
	plain := "BT /F1 12 Tf 20 100 Td (V4-META) Tj ET"
	ct := aesEnc(sec.ObjectKey(key, 4, 0, true), []byte(plain))
	meta := "<x:xmpmeta>v4 clear</x:xmpmeta>"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /Metadata 7 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(ct), ct),
		5: fmt.Sprintf("<< /Filter /Standard /V 4 /R 4 /Length 128 /P -4 /O <%X> /U <%X> /CF << /StdCF << /CFM /AESV2 /Length 16 >> >> /StmF /StdCF /StrF /StdCF /EncryptMetadata false >>", o, u),
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		7: fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(meta), meta),
	}, fmt.Sprintf("<< /Size 8 /Root 1 0 R /Encrypt 5 0 R /ID [<%X> <%X>] >>", id, id))
	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnableDecryption(nil); err != nil {
		t.Fatal(err)
	}
	if got := streamOf(t, r, 7); string(got) != meta {
		t.Errorf("metadata = %q, want it unchanged", got)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if got := contentText(t, r, pages[0]); got != plain {
		t.Errorf("content = %q, want %q", got, plain)
	}
}

// TestEnableDecryptionPassword_SASLprep: a typed password is SASLprep'd for
// V5 and, when that fails, tried as raw UTF-8 (design §4, Q4), within the
// hash bound.
func TestEnableDecryptionPassword_SASLprep(t *testing.T) {
	for _, tc := range []struct {
		name     string
		written  string // the bytes the writer used
		typed    string
		wantOpen bool
	}{
		{"prepared by the writer, soft hyphen typed", "IX", "I\u00ADX", true},
		{"prepared by the writer, roman numeral typed", "IX", "\u2168", true},
		{"prepared by the writer, ideographic space typed", "a b", "a\u3000b", true},
		{"fullwidth letters (NFKC)", "PASS", "\uFF30\uFF21\uFF33\uFF33", true},
		{"composed and decomposed", "p\u00E4ssw\u00F6rd", "pa\u0308ssw\u00F6rd", true},
		{"raw bytes by the writer (no SASLprep)", "I\u00ADX", "I\u00ADX", true},
		{"prohibited character, raw only", "a\u0007b", "a\u0007b", true},
		{"wrong", "IX", "XI", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := AES256Security{R: 6, User: []byte(tc.written), Owner: []byte("owner-x"), P: -4, EncryptMetadata: true, Seed: tc.name}
			r, err := NewResolver(V5Doc{Sec: sec, Objects: V5OnePage("SASL", false, ""), Root: 1, Info: 6}.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			err = r.EnableDecryptionPassword(tc.typed)
			if (err == nil) != tc.wantOpen {
				t.Fatalf("err = %v, want open %v", err, tc.wantOpen)
			}
			if err != nil && !errors.Is(err, ErrWrongPassword) {
				t.Errorf("err = %v, want ErrWrongPassword", err)
			}
		})
	}
}

// TestPasswordCandidates_HashBound: the worst open — a non-empty owner
// password that only its raw form matches, so both encodings are tried as
// user and owner, then the key and the empty-password check — runs exactly
// v5MaxHashes hashes; every other path fewer.
func TestPasswordCandidates_HashBound(t *testing.T) {
	sec := AES256Security{R: 6, User: []byte("user"), Owner: []byte("I\u00ADX"), P: -4, EncryptMetadata: true, Seed: "bound"}
	enc, key := v5Enc(sec)
	h := &v5Hasher{}
	d, err := newDecryptorFor(enc, nil, passwordCandidates(enc, "I\u00ADX"), h)
	if err != nil || !bytes.Equal(d.key, key) || !d.info.Owner {
		t.Fatalf("err = %v, owner %v", err, d != nil && d.info.Owner)
	}
	if h.calls != v5MaxHashes {
		t.Errorf("%d hashes, want %d", h.calls, v5MaxHashes)
	}
	h = &v5Hasher{}
	if _, err := newDecryptorFor(enc, nil, passwordCandidates(enc, "I\u00ADY"), h); !errors.Is(err, ErrWrongPassword) || h.calls != 4 {
		t.Errorf("wrong password: %v after %d hashes, want ErrWrongPassword after 4", err, h.calls)
	}
}

// TestPasswordCandidates_Encodings pins the encodings tried per revision.
func TestPasswordCandidates_Encodings(t *testing.T) {
	v4 := Dict{"V": Integer(4)}
	v5 := Dict{"V": Integer(5)}
	long := strings.Repeat("\u00E9", 100) // 200 UTF-8 bytes
	for _, tc := range []struct {
		name string
		enc  Dict
		pw   string
		want [][]byte
	}{
		{"empty", v5, "", [][]byte{nil}},
		{"V5 ASCII", v5, "user", [][]byte{[]byte("user")}},
		{"V5 SASLprep differs", v5, "I\u00ADX", [][]byte{[]byte("IX"), []byte("I\u00ADX")}},
		{"V5 prohibited: raw only", v5, "a\u0007", [][]byte{[]byte("a\u0007")}},
		{"V5 truncated to 127 bytes", v5, long, [][]byte{[]byte(long)[:127]}},
		{"V4 ASCII", v4, "user", [][]byte{[]byte("user")}},
		{"V4 Latin-1", v4, "p\u00E4ssw\u00F6rd", [][]byte{{'p', 0xE4, 's', 's', 'w', 0xF6, 'r', 'd'}, []byte("p\u00E4ssw\u00F6rd")}},
		{"V4 PDFDocEncoding specials", v4, "\u20AC\u2022\u017E", [][]byte{{0xA0, 0x80, 0x9E}, []byte("\u20AC\u2022\u017E")}},
		{"V4 not representable: raw only", v4, "\u5BC6\u7801", [][]byte{[]byte("\u5BC6\u7801")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := passwordCandidates(tc.enc, tc.pw)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tc.want) {
				t.Errorf("candidates = %q, want %q", got, tc.want)
			}
		})
	}
	if len(pdfDocHigh) != 31 || len(pdfDocLow) != 8 {
		t.Errorf("PDFDocEncoding tables: %d and %d characters, want 31 and 8", len(pdfDocHigh), len(pdfDocLow))
	}
}

// TestStdSecurity_OwnerPassword pins Algorithm 7 for R2–R4: the owner
// password opens a document whose user password is set (and records the
// role), and tells whether the empty password would have opened it.
func TestStdSecurity_OwnerPassword(t *testing.T) {
	id := []byte("owner-path-id-01")
	for _, vr := range []struct {
		name string
		v, r int
		kl   int
		cfm  Name
	}{{"R2", 1, 2, 5, ""}, {"R3", 2, 3, 16, ""}, {"R4 AESV2", 4, 4, 16, "AESV2"}} {
		for _, tc := range []struct {
			user, given      string
			wantOK, owner    bool
			emptyUserOpensIt bool
		}{
			{"secret", "owner", true, true, false},
			{"", "owner", true, true, true},
			{"secret", "secret", true, false, false},
			{"secret", "", false, false, false},
			{"secret", "0wner", false, false, false},
		} {
			t.Run(fmt.Sprintf("%s/user %q given %q", vr.name, tc.user, tc.given), func(t *testing.T) {
				sec := StdSecurity{R: vr.r, KeyLen: vr.kl, User: []byte(tc.user), Owner: []byte("owner"), P: -44, ID: id, EncryptMetadata: true}
				r, err := NewResolver(EncryptedPDF(sec, vr.v, vr.cfm, "BT (x) Tj ET", nil))
				if err != nil {
					t.Fatal(err)
				}
				err = r.EnableDecryptionPassword(tc.given)
				if (err == nil) != tc.wantOK {
					t.Fatalf("err = %v, wantOK %v", err, tc.wantOK)
				}
				if err != nil {
					if !errors.Is(err, ErrWrongPassword) {
						t.Errorf("err = %v, want ErrWrongPassword", err)
					}
					return
				}
				info, _ := r.Decryption()
				if info.Owner != tc.owner || info.EmptyUserPassword != tc.emptyUserOpensIt {
					t.Errorf("info = %+v", info)
				}
				_, _, key := sec.Build()
				if !bytes.Equal(r.decrypt.key, key) {
					t.Errorf("file key = %X, want %X", r.decrypt.key, key)
				}
			})
		}
	}
}

// TestEnableDecryption_DoesNotClearCallerBytes: the candidates are cleared
// after use, but never the caller's own slice.
func TestEnableDecryption_DoesNotClearCallerBytes(t *testing.T) {
	sec := AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "clear"}
	r, err := NewResolver(V5Doc{Sec: sec, Objects: V5OnePage("x", false, ""), Root: 1}.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pw := []byte("user")
	if err := r.EnableDecryption(pw); err != nil || string(pw) != "user" {
		t.Fatalf("err = %v, password bytes now %q", err, pw)
	}
}

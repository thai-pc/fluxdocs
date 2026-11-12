package parse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// longChainSalt is a user validation salt found by an offline search over
// 200,000 consecutive 8-byte salts (empty password, no user key): its
// Algorithm 2.B chain runs longChainRounds rounds, against a typical 64–70
// (TestHash2B_LongChain).
var longChainSalt = []byte{0, 0, 0, 0, 0, 0, 0x05, 0x95}

const longChainRounds = 107

// minDuration runs f n times and returns the fastest run: the measure least
// disturbed by other work on the machine.
func minDuration(n int, f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < n; i++ {
		start := time.Now()
		f()
		best = min(best, time.Since(start))
	}
	return best
}

// TestDoS_R6KeyDerivationIsBounded is the design §11.3 DoS gate. An attacker
// picks the salts, so they pick the 2.B round count; the longest chain is 287
// rounds (hash2BStop), and the loop's cap is 288. The worst open runs
// v5MaxHashes hashes — across the whole repair ladder (DecryptionCache) —
// with a 127-byte password and the 48-byte user key, each forced to the
// 288-round cap by the stop-test hook. Its cost
// is bounded relative to a measured baseline — one forced 64-round hash of
// the same inputs — not by an absolute time, so it holds under -race, on
// js/wasm and on a slow CI runner alike: the ratio is 6 × 288 / 64 = 27 and
// the gate allows 45. The open path itself never runs more than v5MaxHashes
// hashes (TestPasswordCandidates_HashBound) and a hostile dictionary cannot
// raise the round bound.
func TestDoS_R6KeyDerivationIsBounded(t *testing.T) {
	pw := bytes.Repeat([]byte("p"), v5MaxPassword)
	salt := make([]byte, 8)
	udata := bytes.Repeat([]byte{0xA5}, 48)
	run := func(rounds, hashes int) func() {
		return func() {
			h := &v5Hasher{r: 6, stopAt: rounds}
			for i := 0; i < hashes; i++ {
				if _, err := h.hash(pw, salt, udata); err != nil || h.rounds != rounds {
					t.Fatalf("hash %d: %v after %d rounds, want %d", i, err, h.rounds, rounds)
				}
			}
		}
	}
	base := minDuration(5, run(64, 1))
	worst := minDuration(3, run(hash2BMaxRounds, v5MaxHashes))
	t.Logf("one 64-round hash (127-byte password, user key): %v; worst open (%d hashes × %d rounds): %v (%.1f×)",
		base, v5MaxHashes, hash2BMaxRounds, worst, float64(worst)/float64(base))
	if limit := 45 * base; worst > limit {
		t.Errorf("worst open took %v, more than 45 × the %v baseline (%v): key derivation is not linear in rounds", worst, base, limit)
	}

	// Through the open path: a wrong 127-byte password whose SASLprep form
	// differs is tried twice, as user and owner: 4 hashes, all forced to 288
	// rounds, then ErrWrongPassword.
	enc, _ := v5Enc(AES256Security{R: 6, User: []byte("user"), Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "dos"})
	typed := "\u00AD" + string(bytes.Repeat([]byte("q"), 200))
	h := &v5Hasher{stopAt: hash2BMaxRounds}
	if _, err := newDecryptorFor(enc, nil, passwordCandidates(enc, typed), h); !errors.Is(err, ErrWrongPassword) || h.calls != 4 {
		t.Errorf("forced open: %v after %d hashes, want ErrWrongPassword after 4", err, h.calls)
	}
}

// BenchmarkHash2B measures Algorithm 2.B at 64, 100 and 288 rounds (forced
// by the stop-test hook) for an empty and a 127-byte password, as the user
// test (no user key) and the owner test (48-byte user key): the numbers of
// design §11.4. Run it under GOOS=js GOARCH=wasm too.
func BenchmarkHash2B(b *testing.B) {
	salt := make([]byte, 8)
	for _, rounds := range []int{64, 100, 288} {
		for _, pwLen := range []int{0, 127} {
			for _, owner := range []bool{false, true} {
				pw := bytes.Repeat([]byte("p"), pwLen)
				var udata []byte
				if owner {
					udata = make([]byte, 48)
				}
				b.Run(fmt.Sprintf("rounds=%d/pw=%d/owner=%v", rounds, pwLen, owner), func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						h := &v5Hasher{r: 6, stopAt: rounds}
						if _, err := h.hash(pw, salt, udata); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// BenchmarkDecryptAESV3 measures stream decryption throughput with AESV3
// (the file key) against AESV2 (a per-object MD5 key), on 64 KiB.
func BenchmarkDecryptAESV3(b *testing.B) {
	data := make([]byte, 16+64<<10)
	for _, m := range []cryptMethod{methodAESV2, methodAESV3} {
		key := make([]byte, 16)
		if m == methodAESV3 {
			key = make([]byte, 32)
		}
		d := &decryptor{key: key, method: m}
		name := map[cryptMethod]string{methodAESV2: "AESV2", methodAESV3: "AESV3"}[m]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				d.decryptWith(m, 4, 0, data)
			}
		})
	}
}

// TestDecryptionCache_Ladder pins the shared key derivation of one open
// (DecryptionCache): the strict, shifted and repaired resolvers of the same
// file meet the same encryption dictionary, so only the first derives the
// key — 2 hashes for the empty password, not 2 per rung. A rung that meets a
// different dictionary derives within what is left of the one budget, and
// fails closed once it is spent.
func TestDecryptionCache_Ladder(t *testing.T) {
	sec := AES256Security{R: 6, Owner: []byte("owner"), P: -4, EncryptMetadata: true, Seed: "ladder"}
	doc := V5Doc{Sec: sec, Objects: V5OnePage("LADDER", false, ""), Root: 1, Info: 6}.Bytes()
	shifted := append([]byte("JUNK"), doc...)
	c := &DecryptionCache{}
	rungs := []func() (*Resolver, error){
		func() (*Resolver, error) { return NewResolver(doc) },
		func() (*Resolver, error) { return NewResolverShifted(shifted, 4) },
		func() (*Resolver, error) { return NewRepairedResolver(doc) },
	}
	for i, rung := range rungs {
		r, err := rung()
		if err != nil {
			t.Fatalf("rung %d: %v", i, err)
		}
		if err := r.EnableDecryptionPasswordShared("", c); err != nil {
			t.Fatalf("rung %d: %v", i, err)
		}
		if pages, err := r.Pages(); err != nil || !strings.Contains(contentText(t, r, pages[0]), "LADDER") {
			t.Fatalf("rung %d: pages %v", i, err)
		}
	}
	if c.hasher.calls != 2 {
		t.Errorf("%d hashes over three rungs, want 2 (the key reused)", c.hasher.calls)
	}

	// Different dictionaries per rung (a hostile file): one budget. A wrong
	// 127-byte password whose SASLprep form differs costs 4 hashes per
	// dictionary, so the second dictionary runs out of budget and fails
	// closed; no open passes v5MaxHashes.
	c = &DecryptionCache{}
	typed := "­" + strings.Repeat("q", 200)
	for i, seed := range []string{"a", "b", "c"} {
		s := sec
		s.Seed = seed
		r, err := NewResolver(V5Doc{Sec: s, Objects: V5OnePage("X", false, ""), Root: 1}.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		err = r.EnableDecryptionPasswordShared(typed, c)
		if err == nil {
			t.Fatalf("dictionary %d opened with a wrong password", i)
		}
		if c.hasher.calls > v5MaxHashes {
			t.Fatalf("dictionary %d: %d hashes, want ≤ %d", i, c.hasher.calls, v5MaxHashes)
		}
	}
	if c.hasher.calls != v5MaxHashes {
		t.Errorf("%d hashes in all, want the budget, %d", c.hasher.calls, v5MaxHashes)
	}
}

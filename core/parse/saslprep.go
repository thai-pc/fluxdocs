package parse

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// saslprep.go prepares an AES-256 (R5/R6) password as ISO 32000-2 §7.6.4.3.3
// (Algorithm 2.A, step a) asks: SASLprep (RFC 4013), which is the stringprep
// profile of RFC 3454 with these steps (RFC 4013 §2):
//
//  1. map: non-ASCII space characters (table C.1.2) become U+0020, and the
//     characters of table B.1 are removed (§2.1). U+200B is in both tables;
//     §2.1 lists the space mapping first, so it becomes U+0020;
//  2. normalize to Unicode NFKC (§2.2);
//  3. prohibit tables C.1.2, C.2.1, C.2.2, C.3–C.9 (§2.3);
//  4. check bidirectional text (§2.4, RFC 3454 §6);
//  5. unassigned code points (table A.1) are allowed: a password typed at
//     open is a stored-string "query" (RFC 3454 §7).
//
// The tables are generated from the RFC text (saslprep_tables.go). NFKC is
// golang.org/x/text/unicode/norm, which implements a newer Unicode version
// than the 3.2 stringprep pins; the two differ only on code points that 3.2
// left unassigned (docs/design/aes256-decryption.md §4). PRECIS
// (x/text/secure/precis) is deliberately not used: its OpaqueString profile
// normalizes to NFC with other mappings and would derive other bytes.

// runeRange is an inclusive range of code points (saslprep_tables.go).
type runeRange struct{ lo, hi rune }

// errSASLprep reports a password SASLprep refuses: a prohibited character, a
// bidirectional-text violation, or invalid UTF-8. The message never holds the
// password.
var errSASLprep = errors.New("parse: password is not valid SASLprep input")

// inRanges reports whether r is in the sorted, disjoint ranges rs.
func inRanges(rs []runeRange, r rune) bool {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].hi >= r })
	return i < len(rs) && rs[i].lo <= r
}

// saslprep returns the SASLprep form of s (RFC 4013 §2), UTF-8 encoded, or
// errSASLprep when s is not valid UTF-8, holds a prohibited character after
// normalization, or fails the bidirectional check. The empty string maps to
// the empty string.
func saslprep(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errSASLprep
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case inRanges(saslNonASCIISpace, r):
			b.WriteByte(' ')
		case inRanges(saslMapToNothing, r):
		default:
			b.WriteRune(r)
		}
	}
	out := norm.NFKC.String(b.String())

	randAL, lcat := false, false
	for _, r := range out {
		if inRanges(saslProhibited, r) {
			return "", errSASLprep
		}
		if inRanges(saslRandALCat, r) {
			randAL = true
		}
		if inRanges(saslLCat, r) {
			lcat = true
		}
	}
	// RFC 3454 §6: a string with a RandALCat character holds no LCat
	// character, and starts and ends with a RandALCat character.
	if randAL {
		first, _ := utf8.DecodeRuneInString(out)
		last, _ := utf8.DecodeLastRuneInString(out)
		if lcat || !inRanges(saslRandALCat, first) || !inRanges(saslRandALCat, last) {
			return "", errSASLprep
		}
	}
	return out, nil
}

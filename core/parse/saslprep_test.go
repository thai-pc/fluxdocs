package parse

import (
	"errors"
	"testing"
	"unicode/utf8"
)

// TestSASLprep pins the examples of RFC 4013 §3 and one row per stringprep
// table SASLprep uses (RFC 3454: B.1, C.1.2, C.2.1, C.2.2, C.3, C.4, C.5,
// C.6, C.7, C.8, C.9, and the D.1/D.2 bidirectional rules of §6), plus
// unassigned code points, which a query allows.
func TestSASLprep(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		wantErr        bool
	}{
		// RFC 4013 §3.
		{"#1 SOFT HYPHEN mapped to nothing", "I\u00ADX", "IX", false},
		{"#2 no transformation", "user", "user", false},
		{"#3 case preserved", "USER", "USER", false},
		{"#4 output is NFKC", "\u00AA", "a", false},
		{"#5 output is NFKC, matches #1", "\u2168", "IX", false},
		{"#6 prohibited character", "\u0007", "", true},
		{"#7 bidirectional check", "\u06271", "", true},
		// One row per table.
		{"B.1 COMBINING GRAPHEME JOINER mapped to nothing", "a\u034Fb", "ab", false},
		// U+200B is in both B.1 and C.1.2; RFC 4013 §2.1 lists the C.1.2
		// mapping (to SPACE) first, and saslprep applies it.
		{"U+200B in B.1 and C.1.2: mapped to SPACE", "a\u200Bb", "a b", false},
		{"B.1 variation selector mapped to nothing", "a\uFE0Fb", "ab", false},
		{"C.1.2 IDEOGRAPHIC SPACE mapped to SPACE", "a\u3000b", "a b", false},
		{"C.1.2 NO-BREAK SPACE mapped to SPACE", "a\u00A0b", "a b", false},
		{"C.2.1 ASCII control", "a\u001Fb", "", true},
		{"C.2.1 DELETE", "a\u007F", "", true},
		{"C.2.2 non-ASCII control", "a\u0085", "", true},
		{"C.2.2 musical control", "a\U0001D173", "", true},
		{"C.3 private use", "a\uE000", "", true},
		{"C.3 private use, plane 16", "a\U00100000", "", true},
		{"C.4 non-character", "a\uFDD0", "", true},
		{"C.6 replacement character", "a\uFFFD", "", true},
		{"C.7 ideographic description", "a\u2FF0", "", true},
		{"C.8 left-to-right mark", "a\u200E", "", true},
		{"C.9 language tag", "a\U000E0001", "", true},
		{"D.1 RandALCat only", "\u05D0\u05D1", "\u05D0\u05D1", false},
		{"D.1 RandALCat first and last, digit inside", "\u06271\u0628", "\u06271\u0628", false},
		{"D.1 with D.2 LCat", "\u05D0a\u05D1", "", true},
		{"D.1 not first", "1\u05D0", "", true},
		{"unassigned (A.1) allowed in a query", "a\u0221", "a\u0221", false},
		{"fullwidth to ASCII", "\uFF30\uFF21\uFF33\uFF33", "PASS", false},
		{"empty", "", "", false},
		{"invalid UTF-8", "a\xffb", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := saslprep(tc.in)
			if tc.wantErr {
				if !errors.Is(err, errSASLprep) || got != "" {
					t.Fatalf("saslprep(%q) = %q, %v; want errSASLprep", tc.in, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("saslprep(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
	// C.5 surrogates cannot occur in valid UTF-8, which Go strings decode
	// to U+FFFD; the table is still present for completeness.
	if !inRanges(saslProhibited, 0xD800) || !inRanges(saslProhibited, 0xDFFF) {
		t.Error("C.5 surrogates missing from the prohibited table")
	}
}

// FuzzSASLprep: saslprep never panics, its output is valid UTF-8, and it is
// idempotent when it succeeds (design §11.3).
func FuzzSASLprep(f *testing.F) {
	for _, s := range []string{"I\u00ADX", "user", "\u2168", "\u06271", "a\u3000b", "\uFF30", "\u05D0\u05D1", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := saslprep(s)
		if err != nil {
			return
		}
		if !utf8.ValidString(out) {
			t.Fatalf("saslprep(%q) = %q, not UTF-8", s, out)
		}
		again, err := saslprep(out)
		if err != nil || again != out {
			t.Fatalf("saslprep(saslprep(%q)) = %q, %v; want %q", s, again, err, out)
		}
	})
}

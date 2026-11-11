package std14

import "testing"

// TestEncodingSizes cross-checks the Table D.2 transcription by size: Adobe
// StandardEncoding encodes 149 glyphs; MacRomanEncoding 208 (95 ASCII codes,
// 113 of the 128 high codes: Annex D leaves the 15 Mac codes outside the
// Latin set unassigned); WinAnsiEncoding all 224 codes from 040 (the notes map
// every unused code above 40 octal to bullet).
func TestEncodingSizes(t *testing.T) {
	win, _ := BaseEncoding("WinAnsiEncoding")
	mac, _ := BaseEncoding("MacRomanEncoding")
	for _, tt := range []struct {
		name string
		enc  Encoding
		want int
	}{
		{"StandardEncoding", StandardEncoding(), 149},
		{"MacRomanEncoding", mac, 208},
		{"WinAnsiEncoding", win, 224},
	} {
		n := 0
		for c, name := range tt.enc {
			if name != "" {
				n++
				if c < 040 {
					t.Errorf("%s: control code %#o encoded as %q", tt.name, c, name)
				}
			}
		}
		if n != tt.want {
			t.Errorf("%s: %d codes assigned, want %d", tt.name, n, tt.want)
		}
	}
}

// TestLatinSetCodesUnique checks no two rows share a code in one encoding,
// which a transcription slip would cause (buildLatin keeps the last).
func TestLatinSetCodesUnique(t *testing.T) {
	for i, pick := range []func(latinRow) byte{
		func(r latinRow) byte { return r.std },
		func(r latinRow) byte { return r.mac },
		func(r latinRow) byte { return r.win },
	} {
		seen := map[byte]string{}
		for c := byte('A'); c <= 'Z'; c++ {
			seen[c], seen[c+32] = string(rune(c)), string(rune(c+32))
		}
		for _, r := range latinSet {
			c := pick(r)
			if c == 0 {
				continue
			}
			if prev, dup := seen[c]; dup {
				t.Errorf("encoding %d: code %#o is both %q and %q", i, c, prev, r.name)
			}
			seen[c] = r.name
		}
	}
}

func TestEncodingSpotChecks(t *testing.T) {
	win, _ := BaseEncoding("WinAnsiEncoding")
	mac, _ := BaseEncoding("MacRomanEncoding")
	std := StandardEncoding()
	for _, tt := range []struct {
		enc  Encoding
		code byte
		want string
	}{
		{std, 047, "quoteright"}, {std, 0140, "quoteleft"}, {std, 0247, "section"},
		{std, 0341, "AE"}, {std, 0372, "oe"}, {std, 0200, ""},
		{win, 047, "quotesingle"}, {win, 0200, "Euro"}, {win, 0240, "space"},
		{win, 0255, "hyphen"}, {win, 0201, "bullet"}, {win, 0177, "bullet"}, {win, 0377, "ydieresis"},
		{mac, 0245, "bullet"}, {mac, 0312, "space"}, {mac, 0333, "currency"}, {mac, 0255, ""},
		{mac, 0360, ""}, {mac, 0377, "caron"},
	} {
		if got := tt.enc[tt.code]; got != tt.want {
			t.Errorf("code %#o = %q, want %q", tt.code, got, tt.want)
		}
	}
	if _, ok := BaseEncoding("MacExpertEncoding"); ok {
		t.Error("MacExpertEncoding is not transcribed yet and must report false")
	}
}

// TestEncodingsAreCopies checks a caller cannot modify the package's tables
// through what it returns (N4).
func TestEncodingsAreCopies(t *testing.T) {
	std := StandardEncoding()
	std['A'] = "mutated"
	win, _ := BaseEncoding("WinAnsiEncoding")
	win['A'] = "mutated"
	if got := StandardEncoding()['A']; got != "A" {
		t.Errorf("StandardEncoding()['A'] = %q after a caller's write, want %q", got, "A")
	}
	if got := StandardName('A'); got != "A" {
		t.Errorf("StandardName('A') = %q, want %q", got, "A")
	}
	if again, _ := BaseEncoding("WinAnsiEncoding"); again['A'] != "A" {
		t.Errorf("WinAnsiEncoding['A'] = %q after a caller's write, want %q", again['A'], "A")
	}
	if StandardName(0341) != "AE" || StandardName(0200) != "" {
		t.Errorf("StandardName(0341, 0200) = %q, %q; want AE, \"\"", StandardName(0341), StandardName(0200))
	}
}

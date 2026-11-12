package std14

import (
	"strings"
	"testing"
)

// TestNormalize documents the /BaseFont names that select a standard face:
// the 14 names, subset tags, style suffixes and the alias table.
func TestNormalize(t *testing.T) {
	for f := Face(0); f < NumFaces; f++ {
		if got, ok := Normalize(f.String()); !ok || got != f {
			t.Errorf("Normalize(%q) = %v, %v", f.String(), got, ok)
		}
	}
	for _, c := range []struct {
		name string
		face Face
		ok   bool
	}{
		{"ABCDEF+Helvetica", Helvetica, true},
		{"Helvetica,Bold", HelveticaBold, true},
		{"Helvetica,Italic", HelveticaOblique, true},
		{"Helvetica,BoldItalic", HelveticaBoldOblique, true},
		{"Helvetica-Italic", HelveticaOblique, true},
		{"Helvetica-Roman", Helvetica, true},
		{"Times", TimesRoman, true},
		{"Times-Oblique", TimesItalic, true},
		{"Times-BoldOblique", TimesBoldItalic, true},
		{"Courier-Italic", CourierOblique, true},
		{"Arial", Helvetica, true},
		{"ArialMT", Helvetica, true},
		{"Arial-BoldMT", HelveticaBold, true},
		{"Arial-ItalicMT", HelveticaOblique, true},
		{"Arial-BoldItalicMT", HelveticaBoldOblique, true},
		{"Arial,Bold", HelveticaBold, true},
		{"TimesNewRoman", TimesRoman, true},
		{"TimesNewRoman,Italic", TimesItalic, true},
		{"TimesNewRomanPSMT", TimesRoman, true},
		{"TimesNewRomanPS-BoldMT", TimesBold, true},
		{"TimesNewRomanPS-BoldItalicMT", TimesBoldItalic, true},
		{"Times New Roman", TimesRoman, true},
		{"CourierNew", Courier, true},
		{"CourierNewPSMT", Courier, true},
		{"CourierNewPS-BoldMT", CourierBold, true},
		{"Courier New,Bold", CourierBold, true},
		{"Symbol,Bold", Symbol, true},
		{"SymbolMT", Symbol, true},
		{"XYZABC+ZapfDingbats", ZapfDingbats, true},

		{"Helvetica-Narrow", 0, false},
		{"Helvetica-Light", 0, false},
		{"Arial-Black", 0, false},
		{"ArialNarrow", 0, false},
		{"Verdana", 0, false},
		{"helvetica", 0, false},
		{"abcdef+Helvetica", 0, false},
		{"", 0, false},
		{"MT", 0, false},
		{strings.Repeat("Helvetica", 20), 0, false},
	} {
		got, ok := Normalize(c.name)
		if ok != c.ok || ok && got != c.face {
			t.Errorf("Normalize(%q) = %v, %v; want %v, %v", c.name, got, ok, c.face, c.ok)
		}
	}
}

func TestFaceFor(t *testing.T) {
	for _, c := range []struct {
		fam          Family
		bold, italic bool
		want         Face
	}{
		{Mono, false, false, Courier}, {Mono, true, true, CourierBoldOblique},
		{Sans, false, true, HelveticaOblique}, {Sans, true, false, HelveticaBold},
		{Serif, false, false, TimesRoman}, {Serif, true, true, TimesBoldItalic},
		{Family(9), false, false, Helvetica},
	} {
		if got := FaceFor(c.fam, c.bold, c.italic); got != c.want {
			t.Errorf("FaceFor(%v, %v, %v) = %v, want %v", c.fam, c.bold, c.italic, got, c.want)
		}
	}
	if Face(200).String() != "" {
		t.Error("String of an out-of-range face is not empty")
	}
}

// TestWidth spot-checks the AFM tables against values of the AFM files.
func TestWidth(t *testing.T) {
	for _, c := range []struct {
		face Face
		name string
		want float64
	}{
		{Helvetica, "A", 667}, {Helvetica, "space", 278}, {HelveticaBold, "A", 722},
		{TimesRoman, "A", 722}, {TimesItalic, "f", 278}, {Courier, "W", 600},
		{CourierBoldOblique, "Aacute", 600}, {Symbol, "alpha", 631},
		{Symbol, "space", 250}, {Symbol, "Euro", 750}, {Helvetica, "Euro", 556}, {ZapfDingbats, "a1", 974}, {ZapfDingbats, "space", 278},
	} {
		if got, ok := Width(c.face, c.name); !ok || got != c.want {
			t.Errorf("Width(%v, %q) = %v, %v; want %v", c.face, c.name, got, ok, c.want)
		}
	}
	for _, c := range []struct {
		face Face
		name string
	}{{Helvetica, "alpha"}, {Symbol, "A"}, {ZapfDingbats, "A"}, {Helvetica, ""}, {NumFaces, "A"}, {Helvetica, "apple"}} {
		if _, ok := Width(c.face, c.name); ok {
			t.Errorf("Width(%v, %q) found, want none", c.face, c.name)
		}
	}
	if b := BBox(Helvetica); b != [4]float64{-166, -225, 1000, 931} {
		t.Errorf("BBox(Helvetica) = %v", b)
	}
	if b := BBox(NumFaces); b != [4]float64{} {
		t.Errorf("BBox(out of range) = %v", b)
	}
}

// TestLatinEncodingsCovered: every glyph name StandardEncoding,
// WinAnsiEncoding and MacRomanEncoding assign has an AFM width in every
// Latin face, and a Unicode value for drawing it from a substitute.
func TestLatinEncodingsCovered(t *testing.T) {
	win, _ := BaseEncoding("WinAnsiEncoding")
	mac, _ := BaseEncoding("MacRomanEncoding")
	for _, enc := range []Encoding{StandardEncoding(), win, mac} {
		for _, n := range enc {
			if n == "" {
				continue
			}
			if _, ok := Rune(n); !ok {
				t.Errorf("%q: no Unicode value", n)
			}
			for f := Courier; f < Symbol; f++ {
				if _, ok := Width(f, n); !ok {
					t.Errorf("%v: no width for %q", f, n)
				}
			}
		}
	}
}

// TestBuiltinEncoding checks the Symbol and ZapfDingbats built-in encodings
// (Annex D.5, D.6, from the AFM C values) at known codes, and that every
// assigned name has an AFM width.
func TestBuiltinEncoding(t *testing.T) {
	sym, dng := BuiltinEncoding(Symbol), BuiltinEncoding(ZapfDingbats)
	for _, c := range []struct {
		enc  Encoding
		code byte
		want string
	}{
		{sym, 'a', "alpha"}, {sym, 'A', "Alpha"}, {sym, 0x20, "space"}, {sym, 0xD6, "radical"},
		{sym, 0xA0, "Euro"}, {sym, 0xF0, ""}, {sym, 0x80, ""},
		{dng, '!', "a1"}, {dng, 0x20, "space"}, {dng, 0x6C, "a71"}, {dng, 0xFF, ""},
	} {
		if c.enc[c.code] != c.want {
			t.Errorf("code %#x: %q, want %q", c.code, c.enc[c.code], c.want)
		}
	}
	for face, enc := range map[Face]Encoding{Symbol: sym, ZapfDingbats: dng} {
		n := 0
		for _, name := range enc {
			if name == "" {
				continue
			}
			n++
			if _, ok := Width(face, name); !ok {
				t.Errorf("%v: no width for %q", face, name)
			}
		}
		if n < 180 {
			t.Errorf("%v: %d codes assigned", face, n)
		}
	}
	if BuiltinEncoding(Helvetica) != StandardEncoding() {
		t.Error("Helvetica's built-in encoding is not StandardEncoding")
	}
}

func TestRune(t *testing.T) {
	for _, c := range []struct {
		name string
		want rune
		ok   bool
	}{
		{"A", 'A', true}, {"space", ' ', true}, {"Euro", 0x20AC, true}, {"alpha", 0x03B1, true},
		{"a1", 0x2701, true}, {"a191", 0x27BE, true}, {"fi", 0xFB01, true},
		{"a.sc", 'a', true}, {"uni20AC", 0x20AC, true}, {"uni20ac", 0x20AC, true},
		{"u1F600", 0x1F600, true}, {"u00E9", 0xE9, true}, {"uni0041.alt", 'A', true},
		{"uniD800", 0, false}, {"u110000", 0, false}, {"uni20AC20AC", 0, false},
		{"f_f", 0, false}, {"", 0, false}, {".notdef", 0, false}, {"g123", 0, false},
		{"uniXYZW", 0, false}, {strings.Repeat("a", 100), 0, false},
	} {
		got, ok := Rune(c.name)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("Rune(%q) = %U, %v; want %U, %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if r, ok := AltRune("copyrightsans"); !ok || r != 0xA9 {
		t.Errorf("AltRune(copyrightsans) = %U, %v", r, ok)
	}
	if _, ok := AltRune("A"); ok {
		t.Error("AltRune(A) found")
	}
}

// TestGeneratedTablesSorted: the lookups binary-search the generated name
// lists.
func TestGeneratedTablesSorted(t *testing.T) {
	for name, list := range map[string][]string{
		"afmLatinNames": afmLatinNames[:], "afmSymbolNames": afmSymbolNames[:],
		"afmDingbatsNames": afmDingbatsNames[:], "aglNames": aglNames[:],
	} {
		for i := 1; i < len(list); i++ {
			if list[i-1] >= list[i] {
				t.Errorf("%s not strictly sorted at %d: %q, %q", name, i, list[i-1], list[i])
			}
		}
	}
	if len(aglNames) != len(aglRunes) || len(afmSymbolNames) != len(afmSymbolWidths) || len(afmDingbatsNames) != len(afmDingbatsWidths) {
		t.Error("parallel tables differ in length")
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"Helvetica", "ABCDEF+Arial,BoldItalic", "TimesNewRomanPS-BoldItalicMT", "Symbol", "-", ",", "+"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		face, ok := Normalize(s)
		if ok && face >= NumFaces {
			t.Fatalf("Normalize(%q) = %v", s, face)
		}
	})
}

func FuzzRune(f *testing.F) {
	for _, s := range []string{"A", "uni20AC", "u1F600", "a.sc", "f_f", "a1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, ok := Rune(s)
		if ok && (r < 0 || r > 0x10FFFF || r >= 0xD800 && r <= 0xDFFF) {
			t.Fatalf("Rune(%q) = %U", s, r)
		}
	})
}

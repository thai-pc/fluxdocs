package fontmodel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Tests for the bounds on what a code reads to: maxDstBytes (a /ToUnicode
// destination), maxTextPerCodeByte (codeText, every reading) and maxGlyphName
// (glyphRune). Real fonts are far inside them (the corpus's longest
// destination is 10 bytes); hostile ones made one shown string decode to
// hundreds of MiB.

// hexUnits returns n UTF-16BE code units u as hex digits.
func hexUnits(u string, n int) string { return strings.Repeat(u, n) }

func TestDstStringCap(t *testing.T) {
	pair := []byte{0xD8, 0x3D, 0xDE, 0x00} // U+1F600
	for _, tc := range []struct {
		name  string
		in    []byte
		units int // UTF-16 code units kept
	}{
		{"short", []byte{0x00, 0x41}, 1},
		{"at the cap", []byte(strings.Repeat("\x00A", maxDstBytes/2)), maxDstBytes / 2},
		{"one unit past the cap", []byte(strings.Repeat("\x00A", maxDstBytes/2+1)), maxDstBytes / 2},
		{"1 MiB", []byte(strings.Repeat("\x00A", 1<<19)), maxDstBytes / 2},
		{"a surrogate pair across the cut", append([]byte(strings.Repeat("\x00A", maxDstBytes/2-1)), pair...), maxDstBytes/2 - 1},
		{"a surrogate pair before the cut", append([]byte(strings.Repeat("\x00A", maxDstBytes/2-2)), pair...), maxDstBytes / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dstString(tc.in)
			n := 0
			for _, r := range got {
				if r == utf8.RuneError {
					t.Fatalf("dstString produced U+FFFD: %q", got)
				}
				n++
				if r > 0xFFFF {
					n++
				}
			}
			if n != tc.units {
				t.Errorf("dstString kept %d code units, want %d", n, tc.units)
			}
		})
	}
}

func TestCodeText(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		n    int
		want int // bytes kept
	}{
		{"ligature", "ffi", 1, 3},
		{"a Devanagari conjunct cluster", "क्षत्र", 1, len("क्षत्र")},
		{"U+FDFA decomposed (33 bytes)", "صلى الله عليه وسلم", 1, len("صلى الله عليه وسلم")},
		{"ASCII past the cap", strings.Repeat("A", 100), 1, maxTextPerCodeByte},
		{"two-byte characters past the cap", strings.Repeat("é", 40), 1, maxTextPerCodeByte},
		{"three-byte characters cut at a boundary", strings.Repeat("中", 30), 1, 63},
		{"a two-byte code", strings.Repeat("A", 300), 2, 2 * maxTextPerCodeByte},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codeText(tc.s, tc.n)
			if len(got) != tc.want || !utf8.ValidString(got) || !strings.HasPrefix(tc.s, got) {
				t.Errorf("codeText(%d bytes, %d) = %d bytes (valid %v), want a %d-byte prefix", len(tc.s), tc.n, len(got), utf8.ValidString(got), tc.want)
			}
		})
	}
}

// TestReadingsBounded: whatever a font's /ToUnicode destinations and
// /Differences glyph names hold, every reading of a shown string is at most
// maxTextPerCodeByte bytes per byte of it, and the per-byte tables still
// concatenate to the readings.
func TestReadingsBounded(t *testing.T) {
	long := "<" + hexUnits("0041", 1<<19) + ">" // 1 MiB destination
	cjk := "<" + hexUnits("4E2D", 300) + ">"    // 600 bytes
	cmap1 := "1 begincodespacerange <00> <FF> endcodespacerange " +
		"2 beginbfchar <01> " + long + " <02> " + cjk + " endbfchar " +
		"2 beginbfrange <03> <04> [" + cjk + " " + long + "] <05> <06> " + cjk + " endbfrange"
	cmap2 := "1 begincodespacerange <0000> <FFFF> endcodespacerange 1 beginbfchar <0001> " + long + " endbfchar"
	uni := "/uni" + strings.Repeat("0041", 200) // an 803-byte glyph name
	r := resolverWith(t, map[int]string{
		2: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R /Encoding << /Differences [7 " + uni + "] >> >>",
		3: "<< /Length " + itoa(len(cmap1)) + " >>\nstream\n" + cmap1 + "\nendstream",
		4: "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [] /ToUnicode 5 0 R >>",
		5: "<< /Length " + itoa(len(cmap2)) + " >>\nstream\n" + cmap2 + "\nendstream",
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [7 " + uni + " 8 /uni0041 9 /uni" + strings.Repeat("0041", 31) + "] >> >>",
	})
	load := func(n int) *Font { return Load(r, parse.Reference{Number: n}, Options{Metrics: true}) }
	simple, composite, diffs := load(2), load(4), load(6)
	if simple == nil || composite == nil || diffs == nil {
		t.Fatal("fonts did not load")
	}
	raw := []byte{1, 2, 3, 4, 5, 6, 7, 1, 1, 1}
	check := func(name string, s string, raw []byte) {
		t.Helper()
		if limit := maxTextPerCodeByte * len(raw); len(s) > limit {
			t.Errorf("%s: %d bytes for a %d-byte string, want ≤ %d", name, len(s), len(raw), limit)
		}
	}
	check("simple Text", simple.Text(raw), raw)
	if s, ok := simple.ByteText(raw); ok {
		check("simple ByteText", s, raw)
	}
	wide := []byte{0, 1, 0, 1, 0, 1}
	check("composite Text", composite.Text(wide), wide)
	if got := composite.Text([]byte{0, 1}); got != strings.Repeat("A", 2*maxTextPerCodeByte) {
		t.Errorf("composite Text of one code = %d bytes, want %d", len(got), 2*maxTextPerCodeByte)
	}

	// The long glyph name has no reading; names up to maxGlyphName do.
	if s, ok := diffs.AltText([]byte{7}); ok {
		t.Errorf("AltText read an %d-byte glyph name: %q", len(uni)-1, s)
	}
	if s, ok := diffs.AltText([]byte{8, 9}); !ok || s != strings.Repeat("A", 32) {
		t.Errorf("AltText of /uni0041 and a 127-byte uni name = %q, %v; want 32 A", s, ok)
	}

	// The tables are the readings, byte by byte, bounds included.
	tb := simple.Tables()
	if tb == nil {
		t.Fatal("no tables for a one-byte font")
	}
	var text, byt []byte
	for _, c := range raw {
		text = append(text, tb.Text[c]...)
		byt = append(byt, tb.Byte[c]...)
	}
	if string(text) != simple.Text(raw) {
		t.Error("Text differs from its tables")
	}
	if s, _ := simple.ByteText(raw); string(byt) != s {
		t.Error("ByteText differs from its tables")
	}
}

// TestTextCut: a font any of whose readings the bounds cut reports it
// (redaction refuses such a font), whichever bound cuts it; a font inside
// them does not.
func TestTextCut(t *testing.T) {
	hex := func(s string) string { // BMP text as a UTF-16BE hex string
		var b strings.Builder
		b.WriteByte('<')
		for _, r := range s {
			b.WriteString(hex4(r))
		}
		b.WriteByte('>')
		return b.String()
	}
	at := strings.Repeat("A", maxTextPerCodeByte)     // at the bound
	past := strings.Repeat("A", maxTextPerCodeByte+1) // one byte past it
	cjk := strings.Repeat("中", 22)                    // 66 bytes of UTF-8 in 44 bytes of UTF-16
	for _, tc := range []struct {
		name     string
		sections string // /ToUnicode mapping sections; "" is no /ToUnicode
		diffs    string // /Differences array body; "" is none
		cut      bool
	}{
		{"no /ToUnicode", "", "", false},
		{"bfchar at the bound", "1 beginbfchar <01> " + hex(at) + " endbfchar", "", false},
		{"bfchar past the bound", "1 beginbfchar <01> " + hex(past) + " endbfchar", "", true},
		{"bfchar of CJK past the bound", "1 beginbfchar <01> " + hex(cjk) + " endbfchar", "", true},
		{"bfchar past maxDstBytes", "1 beginbfchar <01> <" + strings.Repeat("0041", maxDstBytes/2+1) + "> endbfchar", "", true},
		{"bfchar of a two-byte code past the one-byte bound", "1 beginbfchar <0001> " + hex(past) + " endbfchar", "", true},
		{"bfrange array element past the bound", "1 beginbfrange <01> <02> [<0041> " + hex(past) + "] endbfrange", "", true},
		{"bfrange base at the bound, one code", "1 beginbfrange <01> <01> " + hex(at) + " endbfrange", "", false},
		{"bfrange base at the bound, incremented", "1 beginbfrange <01> <02> " + hex(at) + " endbfrange", "", true},
		{"bfrange base past the bound", "1 beginbfrange <01> <01> " + hex(past) + " endbfrange", "", true},
		{"short glyph names", "", "1 /A /uni0041 /f_f_i.sc /u1F600", false},
		{"a 127-byte uni name", "", "1 /uni" + strings.Repeat("0041", 31), false},
		{"a 127-byte uni name of CJK", "", "1 /uni" + strings.Repeat("4E2D", 31), true},
		{"a 131-byte uni name", "", "1 /uni" + strings.Repeat("0041", 32), true},
		{"a 128-byte name of no reading", "", "1 /" + strings.Repeat("a", 128), true},
		{"a long suffix", "", "1 /A." + strings.Repeat("a", 300), false},
		{"/Differences of maxDiffItems items", "", strings.Repeat("0 /a ", maxDiffItems/2), false},
		{"/Differences past maxDiffItems", "", strings.Repeat("0 /a ", maxDiffItems/2) + "1 /S", true},
		{"/Differences item that does not resolve", "", "1 /A 4 0 R /B", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			font := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica"
			objs := map[int]string{4: "4 0 R"} // a reference that does not resolve (a loop)
			if tc.sections != "" {
				cm := "1 begincodespacerange <00> <FF> endcodespacerange " + tc.sections
				objs[3] = "<< /Length " + itoa(len(cm)) + " >>\nstream\n" + cm + "\nendstream"
				font += " /ToUnicode 3 0 R"
			}
			if tc.diffs != "" {
				font += " /Encoding << /Differences [" + tc.diffs + "] >>"
			}
			objs[2] = font + " >>"
			f := Load(resolverWith(t, objs), parse.Reference{Number: 2}, Options{Metrics: true})
			if f == nil {
				t.Fatal("font did not load")
			}
			if got := f.TextCut(); got != tc.cut {
				t.Errorf("TextCut() = %v, want %v", got, tc.cut)
			}
		})
	}
	if (*Font)(nil).TextCut() {
		t.Error("nil TextCut() = true")
	}
}

// TestTextUnknown: a shown string some code of which a reader derives text
// for that no reading holds is reported (redaction refuses it), with its
// cause: a /ToUnicode with a usecmap parent; a Type 0 font with no
// /ToUnicode, one silent for a code shown or mapping it to no text (an empty
// destination, which Ghostscript reads as unmapped), or whose encoding splits
// the string into other codes than Text reads (or cannot be known to split it
// as Text does). A string every code of which /ToUnicode maps to some text,
// split alike, or of a simple font, is not.
func TestTextUnknown(t *testing.T) {
	const cmapHead = "1 begincodespacerange <0000> <FFFF> endcodespacerange "
	descendant := "/DescendantFonts [<< /Type /Font /Subtype /CIDFontType0 /BaseFont /KozMinPr6N-Regular " +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 6 >> >>]"
	type0 := func(sub, entries string) string {
		return "<< /Type /Font /Subtype " + sub + " /BaseFont /KozMinPr6N-Regular /Encoding /Identity-H " + descendant + entries + " >>"
	}
	// type0Enc is a Type 0 font with /Encoding enc and /ToUnicode 3 0 R.
	type0Enc := func(enc string) string {
		return "<< /Type /Font /Subtype /Type0 /BaseFont /KozMinPr6N-Regular /Encoding " + enc + " " + descendant + " /ToUnicode 3 0 R >>"
	}
	const (
		oneByteSpace = "1 begincodespacerange <00> <FF> endcodespacerange "
		mixedSpace   = "2 begincodespacerange <00> <7F> <8000> <FFFF> endcodespacerange "
	)
	for _, tc := range []struct {
		name  string
		font  string // object 2; /ToUnicode, when named, is object 3
		cmap  string // object 3's data; "" is none
		enc   string // object 5's data, an embedded encoding CMap; "" is none
		raw   string
		opts  Options
		cause string // "" is not unknown
	}{
		{name: "simple, no /ToUnicode", font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", raw: "\x01", opts: Options{Metrics: true}},
		{name: "simple, /ToUnicode silent for the code", font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R >>",
			cmap: oneByteSpace, raw: "\x01", opts: Options{Metrics: true}},
		{name: "simple, /ToUnicode maps the code to no text", font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R >>",
			cmap: oneByteSpace + "1 beginbfrange <00> <FF> <> endbfrange", raw: "\x01", opts: Options{Metrics: true}},
		{name: "simple, /ToUnicode with usecmap", font: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R >>",
			cmap: "/Adobe-Identity-UCS2 usecmap " + oneByteSpace, raw: "\x01", opts: Options{Metrics: true}, cause: "usecmap"},
		{name: "Type 0, no /ToUnicode", font: type0("/Type0", ""), raw: "\x00\x34", opts: Options{Metrics: true}, cause: "no_tounicode"},
		{name: "Type 0, no /ToUnicode, without metrics", font: type0("/Type0", ""), raw: "\x00\x34", cause: "no_tounicode"},
		{name: "Type 0 by an indirect /Subtype, no /ToUnicode", font: type0("4 0 R", ""), raw: "\x00\x34", opts: Options{Metrics: true}, cause: "no_tounicode"},
		{name: "Type 0, no /ToUnicode, empty string", font: type0("/Type0", ""), opts: Options{Metrics: true}},
		{name: "Type 0, no /ToUnicode, /ToUnicode not read", font: type0("/Type0", ""), raw: "\x00\x34", opts: Options{Metrics: true, SkipToUnicode: true}},
		{name: "Type 0, /ToUnicode maps every code", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0000> endbfrange", raw: "\x00\x34\x00\x26", opts: Options{Metrics: true}},
		{name: "Type 0, /ToUnicode silent for a code", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfchar <0034> <0053> endbfchar", raw: "\x00\x34\x00\x26", opts: Options{Metrics: true}, cause: "unmapped"},
		{name: "Type 0, a trailing byte", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0000> endbfrange", raw: "\x00\x34\x00", opts: Options{Metrics: true}, cause: "trailing"},
		{name: "Type 0, /ToUnicode with usecmap", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: "/Adobe-Japan1-UCS2 usecmap " + cmapHead, raw: "\x00\x34", opts: Options{Metrics: true}, cause: "usecmap"},

		// Empty destinations: mapped, to no text.
		{name: "Type 0, a bfrange maps every code to no text", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <> endbfrange", raw: "\x00\x34", opts: Options{Metrics: true}, cause: "empty_dst"},
		{name: "Type 0, a bfchar maps a code shown to no text", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "2 beginbfchar <0034> <0053> <0026> <> endbfchar", raw: "\x00\x34\x00\x26", opts: Options{Metrics: true}, cause: "empty_dst"},
		{name: "Type 0, a bfrange array maps a code shown to no text", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfrange <0034> <0035> [<0053> <>] endbfrange", raw: "\x00\x34\x00\x35", opts: Options{Metrics: true}, cause: "empty_dst"},
		{name: "Type 0, an empty destination for no code shown", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "2 beginbfchar <0034> <0053> <0026> <> endbfchar", raw: "\x00\x34", opts: Options{Metrics: true}},

		// The encoding's split against Text's.
		{name: "Type 0, a one-byte /ToUnicode on a two-byte encoding", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: oneByteSpace + "1 beginbfrange <00> <FF> <0078> endbfrange", raw: "\x00\x34", opts: Options{Metrics: true}, cause: "split_mismatch"},
		{name: "Type 0, a two-byte /ToUnicode on a one-byte encoding", font: type0Enc("5 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0078> endbfrange", enc: oneByteSpace + "1 begincidrange <20> <7E> 1 endcidrange",
			raw: "SECRET", opts: Options{Metrics: true}, cause: "split_mismatch"},
		{name: "Type 0, mixed-width codespaces, one-byte codes shown", font: type0Enc("5 0 R"),
			cmap: mixedSpace + "1 beginbfrange <0000> <FFFF> <0041> endbfrange", enc: mixedSpace,
			raw: "\x41\x42\x80\x01", opts: Options{Metrics: true}, cause: "split_mismatch"},
		{name: "Type 0, mixed-width codespaces, two-byte codes shown", font: type0Enc("5 0 R"),
			cmap: mixedSpace + "1 beginbfrange <0000> <FFFF> <0041> endbfrange", enc: mixedSpace,
			raw: "\x80\x01\x90\x02", opts: Options{Metrics: true}},
		{name: "Type 0, a UTF16 encoding shows a surrogate pair", font: type0Enc("/UniJIS-UTF16-H"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0041> endbfrange", raw: "\xD8\x3D\xDE\x00", opts: Options{Metrics: true}, cause: "split_mismatch"},
		{name: "Type 0, a predefined encoding of unknown codespace", font: type0Enc("/90ms-RKSJ-H"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0041> endbfrange", raw: "\x82\xA0", opts: Options{Metrics: true}, cause: "split_uncertain"},
		{name: "Type 0, bytes outside the encoding's codespace", font: type0Enc("5 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0041> endbfrange", enc: "1 begincodespacerange <8000> <FFFF> endcodespacerange",
			raw: "\x00\x34", opts: Options{Metrics: true}, cause: "split_uncertain"},
		{name: "Type 0, overlapping codespace ranges of two lengths", font: type0Enc("5 0 R"),
			cmap: oneByteSpace + "1 beginbfrange <00> <FF> <0078> endbfrange", enc: "2 begincodespacerange <00> <FF> <0100> <01FF> endcodespacerange",
			raw: "\x01\x34", opts: Options{Metrics: true}, cause: "split_uncertain"},
		{name: "Type 0, overlapping codespace ranges, the longer first", font: type0Enc("5 0 R"),
			cmap: oneByteSpace + "1 beginbfrange <00> <FF> <0078> endbfrange", enc: "2 begincodespacerange <0100> <01FF> <00> <FF> endcodespacerange",
			raw: "\x01\x34", opts: Options{Metrics: true}, cause: "split_uncertain"},
		{name: "Type 0, /ToUnicode maps every code, without metrics", font: type0("/Type0", " /ToUnicode 3 0 R"),
			cmap: cmapHead + "1 beginbfrange <0000> <FFFF> <0000> endbfrange", raw: "\x00\x34", cause: "split_uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := map[int]string{2: tc.font, 4: "/Type0"}
			if tc.cmap != "" {
				objs[3] = "<< /Length " + itoa(len(tc.cmap)) + " >>\nstream\n" + tc.cmap + "\nendstream"
			}
			if tc.enc != "" {
				objs[5] = "<< /Type /CMap /CMapName /X /Length " + itoa(len(tc.enc)) + " >>\nstream\n" + tc.enc + "\nendstream"
			}
			f := Load(resolverWith(t, objs), parse.Reference{Number: 2}, tc.opts)
			if f == nil {
				t.Fatal("font did not load")
			}
			if got := f.unknownCause([]byte(tc.raw)); got != tc.cause {
				t.Errorf("unknownCause(%q) = %q, want %q", tc.raw, got, tc.cause)
			}
			if got, want := f.TextUnknown([]byte(tc.raw)), tc.cause != ""; got != want {
				t.Errorf("TextUnknown(%q) = %v, want %v", tc.raw, got, want)
			}
		})
	}
	if (*Font)(nil).TextUnknown([]byte("a")) {
		t.Error("nil TextUnknown() = true")
	}
}

// TestSpaceOverlaps: an encoding CMap whose codespace ranges of different
// lengths meet on the shorter one's bytes splits codes uncertainly (Codes
// takes the shorter, Ghostscript the longer); disjoint ranges, ranges of one
// length, and the predefined codespaces do not. Past maxOverlapPairs pairs
// the codespace counts as overlapping.
func TestSpaceOverlaps(t *testing.T) {
	space := func(ranges string) []spaceRange {
		c := parseCMap([]byte(ranges))
		if c.bad {
			t.Fatalf("codespace %q does not parse", ranges)
		}
		return c.space
	}
	many := func(n1, n2 int) string { // n1 one-byte and n2 disjoint two-byte ranges, the latter from <80xx>
		var b strings.Builder
		b.WriteString(itoa(n1+n2) + " begincodespacerange ")
		for i := 0; i < n1; i++ {
			b.WriteString("<00> <7F> ")
		}
		for i := 0; i < n2; i++ {
			b.WriteString("<8000> <FFFF> ")
		}
		b.WriteString("endcodespacerange")
		return b.String()
	}
	for _, tc := range []struct {
		name    string
		ranges  string
		overlap bool
	}{
		{"one and two bytes, overlapping", "2 begincodespacerange <00> <FF> <0100> <01FF> endcodespacerange", true},
		{"the longer first", "2 begincodespacerange <0100> <01FF> <00> <FF> endcodespacerange", true},
		{"one byte inside a two-byte range's first byte", "2 begincodespacerange <05> <05> <0000> <FFFF> endcodespacerange", true},
		{"two and four bytes, overlapping on two", "2 begincodespacerange <0000> <80FF> <8000FF00> <80FFFFFF> endcodespacerange", true},
		{"two and four bytes, the first bytes meet but not the second", "2 begincodespacerange <0000> <80FF> <81000000> <8100FFFF> endcodespacerange", false},
		{"one and two bytes, disjoint (Shift-JIS shape)", "3 begincodespacerange <00> <80> <A0> <DF> <8140> <9FFC> endcodespacerange", false},
		{"one length only, overlapping", "2 begincodespacerange <0000> <80FF> <8000> <FFFF> endcodespacerange", false},
		{"mixed widths, disjoint", "2 begincodespacerange <00> <7F> <8000> <FFFF> endcodespacerange", false},
		{"pairs at the cap, disjoint", many(256, 256), false},
		{"pairs past the cap", many(256, 257), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spaceOverlaps(space(tc.ranges)); got != tc.overlap {
				t.Errorf("spaceOverlaps = %v, want %v", got, tc.overlap)
			}
		})
	}
	for _, name := range []parse.Name{"Identity-H", "UniJIS-UCS2-H", "UniJIS-UTF16-H", "UniGB-UTF16-V"} {
		sp, ok, _ := predefinedSpace(name)
		if !ok || spaceOverlaps(sp) {
			t.Errorf("predefined %s: known %v, overlaps %v; want known and not overlapping", name, ok, spaceOverlaps(sp))
		}
	}
	// Loaded as a font's /Encoding, an overlapping codespace leaves the split
	// uncertain; a disjoint one does not.
	for _, tc := range []struct {
		enc     string
		certain bool
	}{
		{"2 begincodespacerange <00> <FF> <0100> <01FF> endcodespacerange", false},
		{"2 begincodespacerange <00> <7F> <8000> <FFFF> endcodespacerange", true},
	} {
		f := Load(resolverWith(t, map[int]string{
			2: "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding 5 0 R /DescendantFonts [<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X >>] >>",
			5: "<< /Type /CMap /CMapName /X /Length " + itoa(len(tc.enc)) + " >>\nstream\n" + tc.enc + "\nendstream",
		}), parse.Reference{Number: 2}, Options{Metrics: true})
		if f == nil {
			t.Fatal("font did not load")
		}
		if _, ok := f.Codes([]byte{0x01, 0x34}); ok != tc.certain {
			t.Errorf("%s: Codes certain = %v, want %v", tc.enc, ok, tc.certain)
		}
	}
}

// TestByteTextEmptyDestination: a byte a simple font's /ToUnicode maps to no
// text is read by ByteText as one it does not map — through its /Differences
// glyph name, else as Latin-1 — as Ghostscript reads it; Text, which
// extraction keeps, still reads nothing. The per-byte tables agree.
func TestByteTextEmptyDestination(t *testing.T) {
	cm := "1 begincodespacerange <00> <FF> endcodespacerange 1 beginbfrange <00> <FF> <> endbfrange 1 beginbfchar <58> <0078> endbfchar"
	r := resolverWith(t, map[int]string{
		2: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R /Encoding << /Differences [1 /S /E /C /R /E /T] >> >>",
		3: "<< /Length " + itoa(len(cm)) + " >>\nstream\n" + cm + "\nendstream",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R >>",
	})
	raw := []byte("\x01\x02\x03\x04\x05\x06-X")
	for _, tc := range []struct {
		name string
		obj  int
		want string
	}{
		{"through /Differences", 2, "SECRET-x"},
		{"as Latin-1", 4, "\x01\x02\x03\x04\x05\x06-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Load(r, parse.Reference{Number: tc.obj}, Options{Metrics: true})
			if f == nil {
				t.Fatal("font did not load")
			}
			if got := f.Text(raw); got != "x" {
				t.Errorf("Text = %q, want %q", got, "x")
			}
			got, ok := f.ByteText(raw)
			if !ok || got != tc.want {
				t.Errorf("ByteText = %q, %v; want %q", got, ok, tc.want)
			}
			tb := f.Tables()
			if tb == nil {
				t.Fatal("no tables for a one-byte font")
			}
			var byt []byte
			for _, c := range raw {
				byt = append(byt, tb.Byte[c]...)
			}
			if string(byt) != got || tb.ByteIsText {
				t.Errorf("tables Byte = %q (ByteIsText %v), want %q", byt, tb.ByteIsText, got)
			}
		})
	}
}

// hex4 is r's four hex digits (a BMP code unit).
func hex4(r rune) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[r>>12&15], digits[r>>8&15], digits[r>>4&15], digits[r&15]})
}

// TestTextCutCorpus: no font of the test corpus (and of the PDFs listed in
// FLUXDOCS_EXTRA_CORPUS, one path per line, when set) is cut by the bounds,
// so redaction's refusal of such fonts refuses no real document. Fonts are
// read from every page's resources and the Form XObjects they reach. Reports
// file names and object numbers only.
func TestTextCutCorpus(t *testing.T) {
	var paths []string
	for _, dir := range []string{"clean", "malformed", "encrypted", "forms", "scanned"} {
		m, _ := filepath.Glob(filepath.Join("..", "..", "..", "testing", "corpus", dir, "*.pdf"))
		paths = append(paths, m...)
	}
	if len(paths) == 0 {
		t.Fatal("no PDFs under testing/corpus")
	}
	if list := os.Getenv("FLUXDOCS_EXTRA_CORPUS"); list != "" {
		data, err := os.ReadFile(list)
		if err != nil {
			t.Fatalf("FLUXDOCS_EXTRA_CORPUS: %v", err)
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				paths = append(paths, l)
			}
		}
	}
	files, fonts := 0, 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		r, err := parse.NewResolver(data)
		if err != nil {
			continue
		}
		pages, err := r.Pages()
		if err != nil {
			continue
		}
		files++
		seen := map[parse.Reference]bool{}
		var walk func(res parse.Dict, depth int)
		walk = func(res parse.Dict, depth int) {
			if res == nil || depth > 8 {
				return
			}
			if fd, ok, _ := r.ResolveDict(res["Font"]); ok {
				for _, v := range fd {
					ref, isRef := v.(parse.Reference)
					if isRef {
						if seen[ref] {
							continue
						}
						seen[ref] = true
					}
					f := Load(r, v, Options{Metrics: true})
					if f == nil {
						continue
					}
					fonts++
					if f.TextCut() {
						t.Errorf("%s: font %d 0 R: TextCut() = true", p, ref.Number)
					}
				}
			}
			xd, ok, _ := r.ResolveDict(res["XObject"])
			if !ok {
				return
			}
			for _, v := range xd {
				ref, isRef := v.(parse.Reference)
				if !isRef || seen[ref] {
					continue
				}
				seen[ref] = true
				st, ok := resolveStream(r, v)
				if !ok {
					continue
				}
				if sub, _ := st.Dict.GetName("Subtype"); sub != "Form" {
					continue
				}
				sub, _, _ := r.ResolveDict(st.Dict["Resources"])
				walk(sub, depth+1)
			}
		}
		for _, pi := range pages {
			walk(pi.Resources, 0)
		}
	}
	t.Logf("%d files, %d fonts read", files, fonts)
}

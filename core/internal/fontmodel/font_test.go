package fontmodel

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// resolverWith builds a resolver over a PDF holding objs (object number ->
// body); a catalog is object 1 unless objs defines it.
func resolverWith(t *testing.T, objs map[int]string) *parse.Resolver {
	t.Helper()
	if _, ok := objs[1]; !ok {
		objs[1] = "<< /Type /Catalog >>"
	}
	keys := make([]int, 0, len(objs))
	for k := range objs {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	size := keys[len(keys)-1] + 1
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, size)
	for _, k := range keys {
		offs[k] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", k, objs[k])
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
	for i := 1; i < size; i++ {
		if offs[i] == 0 {
			b.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xref)
	r, err := parse.NewResolver(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func stream(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// loadFont loads object 2 of objs with metrics.
func loadFont(t *testing.T, objs map[int]string) *Font {
	t.Helper()
	f := Load(resolverWith(t, objs), parse.Reference{Number: 2}, Options{Metrics: true})
	if f == nil {
		t.Fatal("Load returned nil")
	}
	return f
}

// codeEnds returns the end offsets of the codes, or nil if uncertain.
func codeEnds(f *Font, raw []byte) []int {
	codes, ok := f.Codes(raw)
	if !ok {
		return nil
	}
	ends := []int{}
	for _, c := range codes {
		ends = append(ends, c.End)
	}
	return ends
}

func TestCodes(t *testing.T) {
	embedded := "1 begincodespacerange <00> <7F> <8140> <9FFC> endcodespacerange"
	for _, tc := range []struct {
		name string
		objs map[int]string
		raw  string
		want []int // code ends; nil = uncertain
	}{
		{"simple: one byte per code", map[int]string{2: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}, "abc", []int{1, 2, 3}},
		{"Identity-H: two bytes", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H >>"}, "\x00A\x00B", []int{2, 4}},
		{"Identity-H, odd length: uncertain", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H >>"}, "\x00A\x00", nil},
		{"UTF16: surrogate pair is four bytes", map[int]string{2: "<< /Subtype /Type0 /Encoding /UniJIS-UTF16-H >>"},
			"\x00A\xD8\x3D\xDE\x00", []int{2, 6}},
		{"unknown predefined CMap: uncertain", map[int]string{2: "<< /Subtype /Type0 /Encoding /90ms-RKSJ-H >>"}, "AB", nil},
		{"embedded CMap: mixed widths", map[int]string{2: "<< /Subtype /Type0 /Encoding 3 0 R >>", 3: stream("", embedded)},
			"A\x81\x40B", []int{1, 3, 4}},
		{"embedded CMap: unmatched byte is uncertain", map[int]string{2: "<< /Subtype /Type0 /Encoding 3 0 R >>", 3: stream("", embedded)},
			"A\xFF", nil},
		{"embedded CMap using Identity-H", map[int]string{2: "<< /Subtype /Type0 /Encoding 3 0 R >>",
			3: stream("", "/Identity-H usecmap")}, "\x12\x34", []int{2}},
		{"embedded CMap using an unknown parent: uncertain", map[int]string{2: "<< /Subtype /Type0 /Encoding 3 0 R >>",
			3: stream("", "/90ms-RKSJ-H usecmap "+embedded)}, "A", nil},
		{"indirect /Subtype /Type0", map[int]string{2: "<< /Subtype 3 0 R /Encoding /Identity-H >>", 3: "/Type0"}, "\x00A", []int{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codeEnds(loadFont(t, tc.objs), []byte(tc.raw))
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("code ends = %v, want %v", got, tc.want)
			}
		})
	}
	// Without metrics, a Type 0 font's segmentation is unknown.
	r := resolverWith(t, map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H >>"})
	if _, ok := Load(r, parse.Reference{Number: 2}, Options{}).Codes([]byte("\x00A")); ok {
		t.Error("Codes without Options.Metrics is certain for a Type 0 font")
	}
	// The nil font is a simple font.
	if got := codeEnds(nil, []byte("ab")); fmt.Sprint(got) != "[1 2]" {
		t.Errorf("nil font code ends = %v", got)
	}
}

func TestAdvance(t *testing.T) {
	code := func(v uint32, n int) Code { return Code{Start: 0, End: n, Value: v} }
	for _, tc := range []struct {
		name   string
		objs   map[int]string
		c      Code
		lo, hi float64
	}{
		{"simple /Widths", map[int]string{2: "<< /Subtype /TrueType /FirstChar 65 /Widths [500 3000] >>"}, code(66, 1), 3, 3},
		{"simple, outside /Widths: em bound", map[int]string{2: "<< /Subtype /TrueType /FirstChar 65 /Widths [500] >>"}, code(70, 1), 0, 1.1},
		{"simple, /MissingWidth past the bound", map[int]string{2: "<< /Subtype /TrueType /FontDescriptor 3 0 R >>",
			3: "<< /MissingWidth 2000 >>"}, code(70, 1), 0, 2},
		{"simple, negative /MissingWidth", map[int]string{2: "<< /Subtype /TrueType /FontDescriptor 3 0 R >>",
			3: "<< /MissingWidth -500 >>"}, code(70, 1), -0.5, 1.1},
		{"standard 14, no /Widths", map[int]string{2: "<< /Subtype /Type1 /BaseFont /Helvetica >>"}, code(65, 1), 0, 1.1},
		{"Identity-H /W list", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType2 /W [65 [250 750]] >>"}, code(66, 2), 0.75, 0.75},
		{"Identity-H /W range", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType2 /W [10 20 333] >>"}, code(15, 2), 0.333, 0.333},
		{"Identity-H /DW", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType2 /DW 600 >>"}, code(15, 2), 0.6, 0.6},
		{"non-Identity Type 0: em bound", map[int]string{2: "<< /Subtype /Type0 /Encoding /UniJIS-UCS2-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType0 /DW 600 >>"}, code(15, 2), 0, 1.1},
		{"non-Identity Type 0: hull of /DW", map[int]string{2: "<< /Subtype /Type0 /Encoding /UniJIS-UCS2-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType0 /DW 3000 >>"}, code(15, 2), 0, 3},
		{"non-Identity Type 0: hull of /W", map[int]string{2: "<< /Subtype /Type0 /Encoding /UniJIS-UCS2-H /DescendantFonts [3 0 R] >>",
			3: "<< /Subtype /CIDFontType0 /W [1 [-500 4000] 10 20 2500] >>"}, code(15, 2), -0.5, 4},
		{"Type 3 /Widths × /FontMatrix", map[int]string{2: "<< /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /FontBBox [0 0 100 100] " +
			"/FirstChar 97 /Widths [50] >>"}, code(97, 1), 0.5, 0.5},
		{"Type 3 unknown width: /FontBBox extent", map[int]string{2: "<< /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /FontBBox [-10 0 300 100] >>"},
			code(97, 1), -0.1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := loadFont(t, tc.objs).Advance(tc.c)
			if math.Abs(lo-tc.lo) > 1e-9 || math.Abs(hi-tc.hi) > 1e-9 {
				t.Errorf("Advance = [%g, %g], want [%g, %g]", lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

func TestInkAndUnbounded(t *testing.T) {
	// Embedded programs (/FontFile2): the /FontBBox and the em defaults.
	f := loadFont(t, map[int]string{2: "<< /Subtype /Type1 /FirstChar 32 /Widths [500] /FontDescriptor 3 0 R >>", 3: "<< /FontBBox [-200 -300 1500 1200] /FontFile2 4 0 R >>"})
	if x0, y0, x1, y1 := f.Ink(); x0 != -0.2 || y0 != -0.3 || x1 != 1.5 || y1 != 1.2 {
		t.Errorf("Ink = %g %g %g %g, want the /FontBBox (wider than the em defaults)", x0, y0, x1, y1)
	}
	f = loadFont(t, map[int]string{2: "<< /Subtype /Type1 /FirstChar 32 /Widths [500] /FontDescriptor 3 0 R >>", 3: "<< /FontBBox [0 0 500 500] /FontFile2 4 0 R >>"})
	if x0, y0, x1, y1 := f.Ink(); x0 != 0 || y0 != -0.25 || x1 != 1.1 || y1 != 0.9 {
		t.Errorf("Ink = %g %g %g %g, want at least the em defaults", x0, y0, x1, y1)
	}
	for name, objs := range map[string]map[int]string{
		"no /FontMatrix":   {2: "<< /Subtype /Type3 /FontBBox [0 0 1 1] >>"},
		"no /FontBBox":     {2: "<< /Subtype /Type3 /FontMatrix [1 0 0 1 0 0] >>"},
		"zero /FontBBox":   {2: "<< /Subtype /Type3 /FontMatrix [1 0 0 1 0 0] /FontBBox [0 0 0 0] >>"},
		"short FontMatrix": {2: "<< /Subtype /Type3 /FontMatrix [1 0 0 1] /FontBBox [0 0 1 1] >>"},
	} {
		if !loadFont(t, objs).Unbounded() {
			t.Errorf("%s: Type 3 font not Unbounded", name)
		}
	}
	f = loadFont(t, map[int]string{2: "<< /Subtype /Type3 /FontMatrix [0 1 -1 0 0 0] /FontBBox [0 0 2 1] >>"})
	if f.Unbounded() {
		t.Error("Type 3 font with matrix and box is Unbounded")
	}
	if x0, y0, x1, y1 := f.Ink(); x0 != -1 || y0 != 0 || x1 != 0 || y1 != 2 {
		t.Errorf("rotated Type 3 Ink = %g %g %g %g, want -1 0 0 2", x0, y0, x1, y1)
	}
	if !loadFont(t, map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-V >>"}).Vertical() {
		t.Error("Identity-V is not Vertical")
	}
}

func TestUnboundedFonts(t *testing.T) {
	for _, tc := range []struct {
		name string
		objs map[int]string
		want bool
	}{
		{"embedded program, no /Widths", map[int]string{2: "<< /Subtype /TrueType /FontDescriptor 3 0 R >>", 3: "<< /FontFile2 4 0 R >>", 4: stream("", "x")}, true},
		{"embedded program with /Widths", map[int]string{2: "<< /Subtype /TrueType /FirstChar 32 /Widths [500] /FontDescriptor 3 0 R >>", 3: "<< /FontFile2 4 0 R >>", 4: stream("", "x")}, false},
		{"standard 14, no /Widths", map[int]string{2: "<< /Subtype /Type1 /BaseFont /Helvetica >>"}, false},
		{"Type 0 /W truncated", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>", 3: "<< /W [1 [500] 7] >>"}, true},
		{"Type 0 /W with a bad width", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>", 3: "<< /W [1 [500 /x]] >>"}, true},
		{"Type 0 /W complete", map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>", 3: "<< /W [1 [500] 7 9 300] >>"}, false},
	} {
		if got := loadFont(t, tc.objs).Unbounded(); got != tc.want {
			t.Errorf("%s: Unbounded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestByteText(t *testing.T) {
	// A simple font whose /ToUnicode declares two-byte codes: Text reads pairs
	// (nothing maps), ByteText each byte.
	f := loadFont(t, map[int]string{2: "<< /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 3 0 R /Encoding << /Differences [66 /hyphen] >> >>",
		3: stream("", "1 begincodespacerange <0000> <FFFF> endcodespacerange 1 beginbfchar <0041> <0058> endbfchar")})
	if got := f.Text([]byte("AB")); got != "" {
		t.Errorf("Text = %q, want extraction's pair reading (no text)", got)
	}
	if got, ok := f.ByteText([]byte("ABC")); !ok || got != "X-C" {
		t.Errorf("ByteText = %q, %v; want X-C (ToUnicode, glyph name, Latin-1)", got, ok)
	}
	if _, ok := loadFont(t, map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H >>"}).ByteText([]byte("AB")); ok {
		t.Error("ByteText for a Type 0 font")
	}
}

func TestText(t *testing.T) {
	// Text is extraction's decoding, unchanged by the move into fontmodel.
	toUni := stream("", "1 begincodespacerange <0000> <FFFF> endcodespacerange 1 beginbfrange <0041> <005A> <0041> endbfrange")
	r := resolverWith(t, map[int]string{
		2: "<< /Subtype /Type0 /Encoding /Identity-H /ToUnicode 3 0 R >>",
		3: toUni,
		4: "<< /Subtype /Type0 /Encoding /Identity-H >>",
		5: "<< /Subtype 6 0 R /Encoding /Identity-H >>",
		6: "/Type0",
	})
	for _, tc := range []struct {
		num  int
		raw  string
		want string
	}{
		{2, "\x00S\x00E\x00C", "SEC"},
		{4, "\x00S", ""},      // composite, no /ToUnicode: no text
		{5, "\x00S", "\x00S"}, // an indirect /Subtype was never read as Type 0 by extraction
	} {
		for _, metrics := range []bool{false, true} {
			f := Load(r, parse.Reference{Number: tc.num}, Options{Metrics: metrics})
			if got := f.Text([]byte(tc.raw)); got != tc.want {
				t.Errorf("object %d (metrics %v): Text = %q, want %q", tc.num, metrics, got, tc.want)
			}
		}
	}
	var nilFont *Font
	if got := nilFont.Text([]byte("\xe9t\xe9")); got != "été" {
		t.Errorf("nil font Text = %q, want Latin-1", got)
	}
	if Load(r, parse.Integer(3), Options{}) != nil {
		t.Error("Load of a non-dictionary is not nil")
	}
}

func TestAltText(t *testing.T) {
	f := loadFont(t, map[int]string{2: "<< /Subtype /Type1 /Encoding << /Differences [65 /S /E /C /R /T /hyphen /one /uni0032 /u0033 /four.sc] >> >>"})
	if got, ok := f.AltText([]byte("ABCDBEFGHIJ")); !ok || got != "SECRET-1234" {
		t.Errorf("AltText = %q, %v; want SECRET-1234", got, ok)
	}
	if got := f.Text([]byte("AB")); got != "AB" {
		t.Errorf("Text = %q, want the raw codes (extraction's reading)", got)
	}
	if _, ok := f.AltText([]byte("xyz")); ok {
		t.Error("AltText for codes /Differences does not rename")
	}
	// /ToUnicode takes precedence: no second reading.
	f = loadFont(t, map[int]string{2: "<< /Subtype /Type1 /ToUnicode 3 0 R /Encoding << /Differences [65 /S] >> >>",
		3: stream("", "1 beginbfchar <41> <0058> endbfchar")})
	if _, ok := f.AltText([]byte("A")); ok {
		t.Error("AltText with /ToUnicode")
	}
}

func TestGlyphRune(t *testing.T) {
	for name, want := range map[parse.Name]string{
		"A": "A", "zero": "0", "hyphen": "-", "uni0041": "A", "uni00410042": "AB", "u1F600": "\U0001F600",
		"a.sc": "a", "fi": "fi", "ffl": "ffl",
	} {
		if got, ok := glyphRune(name); !ok || got != want {
			t.Errorf("glyphRune(%s) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []parse.Name{"g12", "uniD800", "uni004", "uFFFFFFF", ".notdef"} {
		if got, ok := glyphRune(name); ok {
			t.Errorf("glyphRune(%s) = %q, want no reading", name, got)
		}
	}
}

func TestLoadBoundsHostileArrays(t *testing.T) {
	// A /W array far past maxWItems is read up to the bound, not past it.
	var w bytes.Buffer
	w.WriteString("[0 [")
	for i := 0; i < maxWItems+100; i++ {
		w.WriteString("1 ")
	}
	w.WriteString("]]")
	f := loadFont(t, map[int]string{2: "<< /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [3 0 R] >>",
		3: "<< /Subtype /CIDFontType2 /W " + w.String() + " >>"})
	if len(f.cidW) > maxWItems {
		t.Errorf("%d /W entries kept, want at most %d", len(f.cidW), maxWItems)
	}
	if !f.Unbounded() {
		t.Error("a /W array read only up to the bound leaves the font bounded")
	}
}

// TestInkOfSubstitutedFonts: a font without an embedded program is painted
// with a reader's substitute, whatever its /BaseFont: its ink reaches
// substitutedBBox (batch C reviews, SF-2 and round 5 S-2: Ghostscript's URW
// clones and /Differences glyphs reach past the Adobe AFM boxes).
func TestInkOfSubstitutedFonts(t *testing.T) {
	for _, font := range []string{
		"<< /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Subtype /Type1 /BaseFont /Times-Italic >>",
		"<< /Subtype /Type1 /BaseFont /ABCDEF+Helvetica-Bold >>",
		"<< /Subtype /TrueType /BaseFont /Arial >>",
		"<< /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [3 0 R] >>",
	} {
		f := loadFont(t, map[int]string{2: font, 3: "<< /Subtype /CIDFontType2 /BaseFont /X >>"})
		x0, y0, x1, y1 := f.Ink()
		if math.Abs(x0+0.3) > 1e-9 || math.Abs(y0+0.45) > 1e-9 || math.Abs(x1-1.45) > 1e-9 || math.Abs(y1-1.25) > 1e-9 {
			t.Errorf("%s: Ink = %g %g %g %g, want -0.3 -0.45 1.45 1.25", font, x0, y0, x1, y1)
		}
	}
}

// TestVerticalMetrics: /DW2 and /W2 (both forms) as hulls in text space; an
// unreadable entry is not ok.
func TestVerticalMetrics(t *testing.T) {
	type0 := "<< /Subtype /Type0 /Encoding /Identity-V /DescendantFonts [3 0 R] >>"
	for _, tc := range []struct {
		cid        string
		vx, vy, wy [2]float64
		ok         bool
	}{
		{"<< /Subtype /CIDFontType2 >>", [2]float64{0.5, 0.5}, [2]float64{0.88, 0.88}, [2]float64{-1, -1}, true},
		{"<< /Subtype /CIDFontType2 /DW 600 /DW2 [900 -1100] >>", [2]float64{0.3, 0.3}, [2]float64{0.9, 0.9}, [2]float64{-1.1, -1.1}, true},
		{"<< /Subtype /CIDFontType2 /W2 [5 [-1000 500 2000 -900 400 880] 10 20 -4000 300 100] >>",
			[2]float64{0.3, 0.5}, [2]float64{0.1, 2}, [2]float64{-4, -0.9}, true},
		{"<< /Subtype /CIDFontType2 /W2 [5 [-1000 500]] >>", [2]float64{}, [2]float64{}, [2]float64{}, false},
		{"<< /Subtype /CIDFontType2 /W2 [5 6 -1000 500] >>", [2]float64{}, [2]float64{}, [2]float64{}, false},
		{"<< /Subtype /CIDFontType2 /DW2 [880] >>", [2]float64{}, [2]float64{}, [2]float64{}, false},
	} {
		f := loadFont(t, map[int]string{2: type0, 3: tc.cid})
		vx, vy, wy, ok := f.VerticalMetrics()
		if ok != tc.ok || (ok && (vx != tc.vx || vy != tc.vy || wy != tc.wy)) {
			t.Errorf("%s: VerticalMetrics = %v %v %v %v, want %v %v %v %v", tc.cid, vx, vy, wy, ok, tc.vx, tc.vy, tc.wy, tc.ok)
		}
	}
}

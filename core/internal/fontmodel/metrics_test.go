package fontmodel

import (
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

func TestWidthAndMissingWidth(t *testing.T) {
	tests := []struct {
		name      string
		objs      map[int]string
		code      uint32
		want      float64
		wantOK    bool
		missing   float64
		missingOK bool
	}{
		{
			name:   "simple font /Widths entry, 1/1000 whatever the program",
			objs:   map[int]string{2: "<< /Type /Font /Subtype /Type1 /FirstChar 65 /Widths [500 2000] >>"},
			code:   66,
			want:   2,
			wantOK: true,
		},
		{
			name: "code outside /Widths, descriptor /MissingWidth",
			objs: map[int]string{
				2: "<< /Type /Font /Subtype /TrueType /FirstChar 65 /Widths [500] /FontDescriptor 3 0 R >>",
				3: "<< /Type /FontDescriptor /MissingWidth 250 >>",
			},
			code:      90,
			missing:   0.25,
			missingOK: true,
		},
		{
			name:   "non-numeric /Widths entry is unknown",
			objs:   map[int]string{2: "<< /Type /Font /Subtype /Type1 /FirstChar 65 /Widths [/x] >>"},
			code:   65,
			wantOK: false,
		},
		{
			name:   "Type 3 widths go through /FontMatrix",
			objs:   map[int]string{2: "<< /Type /Font /Subtype /Type3 /FontMatrix [0.01 0 0 0.01 0 0] /FirstChar 0 /Widths [50] >>"},
			code:   0,
			want:   0.5,
			wantOK: true,
		},
		{
			name: "Type 0 font has no /Widths",
			objs: map[int]string{2: "<< /Type /Font /Subtype /Type0 /FirstChar 0 /Widths [50] >>"},
			code: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := loadFont(t, tt.objs)
			got, ok := f.Width(tt.code)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("Width(%d) = %v, %v; want %v, %v", tt.code, got, ok, tt.want, tt.wantOK)
			}
			mw, ok := f.MissingWidth()
			if ok != tt.missingOK || mw != tt.missing {
				t.Errorf("MissingWidth() = %v, %v; want %v, %v", mw, ok, tt.missing, tt.missingOK)
			}
		})
	}
}

func TestWidthNeedsMetrics(t *testing.T) {
	r := resolverWith(t, map[int]string{2: "<< /Type /Font /Subtype /Type1 /FirstChar 0 /Widths [500] >>"})
	f := Load(r, parse.Reference{Number: 2}, Options{})
	if _, ok := f.Width(0); ok {
		t.Error("Width without Options.Metrics reported a width")
	}
	var nilFont *Font
	if _, ok := nilFont.Width(0); ok {
		t.Error("nil Font reported a width")
	}
	if _, ok := nilFont.MissingWidth(); ok {
		t.Error("nil Font reported a /MissingWidth")
	}
}

func TestSkipToUnicode(t *testing.T) {
	cmap := "1 begincodespacerange <00> <FF> endcodespacerange 1 beginbfchar <41> <0042> endbfchar"
	objs := map[int]string{
		2: "<< /Type /Font /Subtype /Type1 /ToUnicode 3 0 R >>",
		3: stream("", cmap),
	}
	r := resolverWith(t, objs)
	decoded := 0
	opts := Options{Metrics: true, SkipToUnicode: true, Decode: func(st *parse.Stream) ([]byte, error) {
		decoded++
		return parse.DecodeStream(st)
	}}
	if got := Load(r, parse.Reference{Number: 2}, opts).Text([]byte("A")); got != "A" {
		t.Errorf("Text with SkipToUnicode = %q, want the Latin-1 reading %q", got, "A")
	}
	if decoded != 0 {
		t.Errorf("SkipToUnicode decoded %d streams, want 0", decoded)
	}
	if got := Load(r, parse.Reference{Number: 2}, Options{}).Text([]byte("A")); got != "B" {
		t.Errorf("Text = %q, want %q (the /ToUnicode reading)", got, "B")
	}
}

func TestDifferencesAndBaseEncoding(t *testing.T) {
	for _, tt := range []struct {
		name   string
		objs   map[int]string
		base   parse.Name
		baseOK bool
		diffs  map[byte]parse.Name
	}{
		{
			name:   "encoding name",
			objs:   map[int]string{2: "<< /Type /Font /Subtype /Type1 /Encoding /WinAnsiEncoding >>"},
			base:   "WinAnsiEncoding",
			baseOK: true,
		},
		{
			name: "dictionary with base and differences",
			objs: map[int]string{
				2: "<< /Type /Font /Subtype /Type1 /Encoding 3 0 R >>",
				3: "<< /Type /Encoding /BaseEncoding /MacRomanEncoding /Differences [65 /square /ring 97 /x] >>",
			},
			base:   "MacRomanEncoding",
			baseOK: true,
			diffs:  map[byte]parse.Name{65: "square", 66: "ring", 97: "x"},
		},
		{
			name:  "differences only",
			objs:  map[int]string{2: "<< /Type /Font /Subtype /TrueType /Encoding << /Differences [32 /space] >> >>"},
			diffs: map[byte]parse.Name{32: "space"},
		},
		{
			name: "Type 0 has no base encoding",
			objs: map[int]string{2: "<< /Type /Font /Subtype /Type0 /Encoding /Identity-H >>"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := loadFont(t, tt.objs)
			base, ok := f.BaseEncoding()
			if base != tt.base || ok != tt.baseOK {
				t.Errorf("BaseEncoding() = %q, %v; want %q, %v", base, ok, tt.base, tt.baseOK)
			}
			d := f.Differences()
			if len(d) != len(tt.diffs) {
				t.Fatalf("Differences() = %v, want %v", d, tt.diffs)
			}
			for c, n := range tt.diffs {
				if d[c] != n {
					t.Errorf("Differences()[%d] = %q, want %q", c, d[c], n)
				}
			}
			if len(d) > 0 {
				for c := range d {
					d[c] = "changed"
				}
				if again := f.Differences(); len(tt.diffs) > 0 && again[firstKey(tt.diffs)] == "changed" {
					t.Error("Differences() returned the font's own map")
				}
			}
		})
	}
}

func firstKey(m map[byte]parse.Name) byte {
	for k := range m {
		return k
	}
	return 0
}

func TestHasWidths(t *testing.T) {
	for _, tt := range []struct {
		name string
		font string
		want bool
	}{
		{"simple font with /Widths", "<< /Type /Font /Subtype /Type1 /FirstChar 65 /Widths [500] >>", true},
		{"/Widths of null entries", "<< /Type /Font /Subtype /Type1 /FirstChar 65 /Widths [null] >>", true},
		{"no /Widths", "<< /Type /Font /Subtype /Type1 /FirstChar 65 >>", false},
		{"/Widths without /FirstChar", "<< /Type /Font /Subtype /Type1 /Widths [500] >>", false},
		{"Type 0 font", "<< /Type /Font /Subtype /Type0 /FirstChar 0 /Widths [50] >>", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := loadFont(t, map[int]string{2: tt.font}).HasWidths(); got != tt.want {
				t.Errorf("HasWidths() = %v, want %v", got, tt.want)
			}
		})
	}
	var nilFont *Font
	if nilFont.HasWidths() {
		t.Error("nil Font has /Widths")
	}
}

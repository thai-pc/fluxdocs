package fontmodel

import (
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// TestTablesMatchReadings: for every byte string, the concatenation of the
// per-byte table entries is what Text, AltText and ByteText return.
func TestTablesMatchReadings(t *testing.T) {
	cmapData := "1 begincodespacerange <00> <FF> endcodespacerange " +
		"1 beginbfrange <20> <7E> <0020> endbfrange 1 beginbfchar <93> <FB01> endbfchar"
	r := resolverWith(t, map[int]string{
		1: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		2: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [65 /fi /uni00E9 /bogus] >> >>",
		3: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 4 0 R /Encoding << /Differences [66 /eacute] >> >>",
		4: "<< /Length " + itoa(len(cmapData)) + " >>\nstream\n" + cmapData + "\nendstream",
		5: "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [] >>",
	})
	load := func(n int) *Font { return Load(r, parse.Reference{Number: n}, Options{Metrics: true}) }
	fonts := map[string]*Font{"nil": nil, "plain": load(1), "differences": load(2), "tounicode": load(3)}
	var all []byte
	for c := 0; c < 256; c++ {
		all = append(all, byte(c))
	}
	inputs := [][]byte{all, []byte("ABC def"), []byte("xyz"), {0x93, 'A'}, {'B', 'C'}, {}}
	for name, f := range fonts {
		if name != "nil" && f == nil {
			t.Fatalf("%s: font did not load", name)
		}
		tb := f.Tables()
		if tb == nil {
			t.Fatalf("%s: no tables", name)
		}
		for _, raw := range inputs {
			var text, alt, byt []byte
			altOK := false
			for _, c := range raw {
				text = append(text, tb.Text[c]...)
				byt = append(byt, tb.Byte[c]...)
				if tb.Alt != nil {
					alt = append(alt, tb.Alt[c]...)
					altOK = altOK || tb.AltDiffers[c]
				}
			}
			if got := f.Text(raw); got != string(text) {
				t.Errorf("%s %q: Text %q, tables %q", name, raw, got, text)
			}
			if got, ok := f.ByteText(raw); !ok || got != string(byt) {
				t.Errorf("%s %q: ByteText %q %v, tables %q", name, raw, got, ok, byt)
			}
			if got, ok := f.AltText(raw); ok != altOK || (ok && got != string(alt)) {
				t.Errorf("%s %q: AltText %q %v, tables %q %v", name, raw, got, ok, alt, altOK)
			}
		}
	}
	// A composite font reads codes wider than a byte: no tables.
	if f := load(5); f == nil || f.Tables() != nil {
		t.Errorf("Type 0 font %v: want it loaded, with no per-byte tables", f)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

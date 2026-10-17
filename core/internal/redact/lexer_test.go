package redact

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

func TestWriteBlankedComments(t *testing.T) {
	cases := []struct{ in, want string }{
		{"(public) %hidden\n Tj", "(public)  \n Tj"},
		{"q 1 0 0 %x\r\n1 0 0 cm", "q 1 0 0  \r\n1 0 0 cm"},
		{"(a % not a comment) Tj", "(a % not a comment) Tj"},
		{"(nested (a % b) \\) %c) Tj", "(nested (a % b) \\) %c) Tj"},
		{"<41%42> Tj %end", "<41%42> Tj  "},
		{"<< /A (x) >> BDC %c", "<< /A (x) >> BDC  "},
	}
	for _, tc := range cases {
		var b bytes.Buffer
		writeBlanked(&b, []byte(tc.in))
		if b.String() != tc.want {
			t.Errorf("writeBlanked(%q) = %q, want %q", tc.in, b.String(), tc.want)
		}
	}
}

func TestInlineDictEnd(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{" BI /W 1 /H 1 /BPC 8 /CS /G ID x EI", len(" BI /W 1 /H 1 /BPC 8 /CS /G ID")},
		{"BI /W 1 %ID in a comment\n/H 1 ID x EI", len("BI /W 1 %ID in a comment\n/H 1 ID")},
		{"BI /ID 1 /D [(ID)] ID x EI", len("BI /ID 1 /D [(ID)] ID")},
		{"BI /W 1 no-id", 0},
	}
	for _, tc := range cases {
		if got := inlineDictEnd([]byte(tc.in)); got != tc.want {
			t.Errorf("inlineDictEnd(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestScrubProps(t *testing.T) {
	in := parse.Dict{
		"MCID": parse.Integer(3), "Lang": parse.String{Value: []byte("en-US")},
		"ActualText": parse.String{Value: []byte("x")}, "X": parse.String{Value: []byte("secret")},
		"Nested":  parse.Dict{"Y": parse.String{Value: []byte("secret")}, "Z": parse.Name("keep")},
		"Arr":     parse.Array{parse.String{Value: []byte("s")}, parse.Integer(1)},
		"BadLang": parse.String{Value: []byte("not a tag!")},
	}
	if !needsScrub(in) {
		t.Fatal("needsScrub = false")
	}
	out := scrubProps(in)
	if _, ok := out["ActualText"]; ok || out["X"] != nil || out["BadLang"] != nil {
		t.Errorf("scrubProps kept a string: %v", out)
	}
	if out["MCID"] != parse.Integer(3) || out["Lang"] != nil {
		t.Errorf("scrubProps dropped /MCID or kept the language tag: %v", out)
	}
	if n := out["Nested"].(parse.Dict); n["Y"] != nil || n["Z"] != parse.Name("keep") {
		t.Errorf("nested: %v", n)
	}
	if a := out["Arr"].(parse.Array); len(a) != 1 || a[0] != parse.Integer(1) {
		t.Errorf("array: %v", a)
	}
	if in["X"] == nil {
		t.Error("scrubProps modified its input")
	}
	if needsScrub(parse.Dict{"MCID": parse.Integer(0), "O": parse.Name("OC")}) {
		t.Error("needsScrub on a list without strings")
	}
	if !needsScrub(parse.Dict{"Lang": parse.String{Value: []byte("SECRET-1234")}}) {
		t.Error("needsScrub ignores /Lang (a valid tag shape can hold text)")
	}
}

// TestPageSizeCountsArrayOperands: the model's size estimate counts the
// elements of array operands (a TJ-heavy page holds most of its memory
// there), so the page-model cache bound is not undercounted.
func TestPageSizeCountsArrayOperands(t *testing.T) {
	var c strings.Builder
	c.WriteString("BT /F1 10 Tf 72 700 Td ")
	const ops, elems = 2000, 11
	for i := 0; i < ops; i++ {
		c.WriteString("[(ab) -20 (cd) 30 (ef) -40 (gh) 10 (ij) -5 (kl)] TJ ")
	}
	c.WriteString("ET")
	p, _, err := analyze(t, fixture{content: c.String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if min := int64(ops * elems * 48); p.Size() < min {
		t.Errorf("Size = %d, want at least %d for %d array elements", p.Size(), min, ops*elems)
	}
}

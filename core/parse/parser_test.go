package parse

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestParseScalarsAndComposites(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Object
	}{
		{"integer", "42", Integer(42)},
		{"negative integer", "-17", Integer(-17)},
		{"positive sign", "+5", Integer(5)},
		{"real", "3.14", Real(3.14)},
		{"real leading dot", ".5", Real(0.5)},
		{"real negative", "-2.0", Real(-2.0)},
		{"true", "true", Boolean(true)},
		{"false", "false", Boolean(false)},
		{"null", "null", Null{}},
		{"name", "/Type", Name("Type")},
		{"name hex escape", "/A#20B", Name("A B")},
		{"reference", "12 0 R", Reference{Number: 12, Generation: 0}},
		{"empty array", "[]", Array{}},
		{"int array", "[1 2 3]", Array{Integer(1), Integer(2), Integer(3)}},
		{"mixed array", "[1 /Foo true]", Array{Integer(1), Name("Foo"), Boolean(true)}},
		{"array with ref", "[1 0 R 2]", Array{Reference{Number: 1, Generation: 0}, Integer(2)}},
		{"nested array", "[[1 2] [3]]", Array{Array{Integer(1), Integer(2)}, Array{Integer(3)}}},
		{
			"simple dict",
			"<< /Type /Page /Count 3 >>",
			Dict{"Type": Name("Page"), "Count": Integer(3)},
		},
		{
			"dict with ref and array",
			"<< /Kids [4 0 R 5 0 R] /Count 2 >>",
			Dict{
				"Kids":  Array{Reference{Number: 4, Generation: 0}, Reference{Number: 5, Generation: 0}},
				"Count": Integer(2),
			},
		},
		{
			"nested dict",
			"<< /A << /B 1 >> >>",
			Dict{"A": Dict{"B": Integer(1)}},
		},
		{"comment skipped", "% comment\n42", Integer(42)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseObject([]byte(tt.in))
			if err != nil {
				t.Fatalf("ParseObject(%q) lỗi: %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseObject(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseStrings(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []byte
		wantHex bool
	}{
		{"literal", "(Hello)", []byte("Hello"), false},
		{"literal nested parens", "(a (b) c)", []byte("a (b) c"), false},
		{"escape newline", `(line\n)`, []byte("line\n"), false},
		{"escape paren", `(\(\))`, []byte("()"), false},
		{"escape backslash", `(a\\b)`, []byte(`a\b`), false},
		{"octal", `(\101\102)`, []byte("AB"), false}, // \101=A \102=B
		{"line continuation", "(a\\\nb)", []byte("ab"), false},
		{"hex even", "<48656C6C6F>", []byte("Hello"), true},
		{"hex odd padded", "<48656C6C6F7>", []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x70}, true},
		{"hex with spaces", "<48 65>", []byte("He"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseObject([]byte(tt.in))
			if err != nil {
				t.Fatalf("ParseObject(%q) lỗi: %v", tt.in, err)
			}
			s, ok := got.(String)
			if !ok {
				t.Fatalf("ParseObject(%q) = %T, want String", tt.in, got)
			}
			if !bytes.Equal(s.Value, tt.want) {
				t.Errorf("value = %q, want %q", s.Value, tt.want)
			}
			if s.Hex != tt.wantHex {
				t.Errorf("hex = %v, want %v", s.Hex, tt.wantHex)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"unterminated literal string", "(abc"},
		{"unterminated hex string", "<4865"},
		{"unclosed array", "[1 2 3"},
		{"unclosed dict", "<< /A 1"},
		{"lone gt", ">"},
		{"empty input", ""},
		{"bare keyword", "obj"},
		{"dict key not name", "<< 1 2 >>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseObject([]byte(tt.in)); err == nil {
				t.Errorf("ParseObject(%q) = nil error, want lỗi", tt.in)
			}
		})
	}
}

func TestParseStreamUnsupported(t *testing.T) {
	in := "<< /Length 5 >>\nstream\nhello\nendstream"
	_, err := ParseObject([]byte(in))
	if !errors.Is(err, errStreamUnsupported) {
		t.Fatalf("err = %v, want errStreamUnsupported", err)
	}
}

func TestDictAccessors(t *testing.T) {
	obj, err := ParseObject([]byte("<< /Type /Page /Count 3 /Kids [1 0 R] /Resources << /Font 2 0 R >> >>"))
	if err != nil {
		t.Fatal(err)
	}
	d := obj.(Dict)

	if name, ok := d.GetName("Type"); !ok || name != "Page" {
		t.Errorf("GetName(Type) = %q,%v want Page,true", name, ok)
	}
	if cnt, ok := d.GetInt("Count"); !ok || cnt != 3 {
		t.Errorf("GetInt(Count) = %d,%v want 3,true", cnt, ok)
	}
	if _, ok := d.GetArray("Kids"); !ok {
		t.Error("GetArray(Kids) ok=false, want true")
	}
	if _, ok := d.GetDict("Resources"); !ok {
		t.Error("GetDict(Resources) ok=false, want true")
	}
	if _, ok := d.GetInt("Type"); ok {
		t.Error("GetInt(Type) ok=true dù Type là Name, want false")
	}
	if _, ok := d.GetName("Missing"); ok {
		t.Error("GetName(Missing) ok=true dù không có key, want false")
	}
}

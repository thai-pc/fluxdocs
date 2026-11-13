package textflow

import (
	"reflect"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// op is one operator of a test stream with the owner it is applied with.
type op struct {
	name     string
	operands []parse.Object
	owner    int32
}

func str(s string) parse.String { return parse.String{Value: []byte(s)} }

func nums(v ...float64) []parse.Object {
	out := make([]parse.Object, len(v))
	for i, x := range v {
		if x == float64(int(x)) {
			out[i] = parse.Integer(int(x))
		} else {
			out[i] = parse.Real(x)
		}
	}
	return out
}

func tf(size float64) op {
	return op{"Tf", []parse.Object{parse.Name("F1"), parse.Integer(int(size))}, NoOwner}
}
func td(x, y float64) op          { return op{"Td", nums(x, y), NoOwner} }
func tj(s string, owner int32) op { return op{"Tj", []parse.Object{str(s)}, owner} }
func tjArr(owner int32, els ...parse.Object) op {
	return op{"TJ", []parse.Object{parse.Array(els)}, owner}
}

func run(ops []op) *Flow {
	f := New(nil)
	for _, o := range ops {
		f.Apply(o.name, o.operands, o.owner)
	}
	return f
}

func TestFlow_Text(t *testing.T) {
	bt, et := op{"BT", nil, NoOwner}, op{"ET", nil, NoOwner}
	tests := []struct {
		name string
		ops  []op
		want string
	}{
		{"empty", nil, ""},
		{"single Tj", []op{bt, tf(12), td(72, 720), tj("Hello", 0), et}, "Hello"},
		{"no separator between operators", []op{bt, tf(12), tj("SECRET-", 0), tj("1234", 1), et}, "SECRET-1234"},
		{"adjacent BT on the same line joined", []op{
			bt, tf(24), td(72, 700), tj("SECRET-", 0), et,
			bt, tf(24), td(177.4, 700), tj("1234", 1), et,
		}, "SECRET-1234"},
		{"T* new line", []op{bt, tf(12), {"TL", nums(14), NoOwner}, td(72, 720), tj("a", 0), {"T*", nil, NoOwner}, tj("b", 1), et}, "a\nb"},
		{"small Td stays on the line", []op{bt, tf(12), td(72, 720), tj("x", 0), td(0, 5), tj("2", 1), et}, "x2"},
		{"move larger than half the size breaks", []op{bt, tf(12), td(72, 720), tj("x", 0), td(0, -6.5), tj("y", 1), et}, "x\ny"},
		{"zero size uses threshold 2", []op{bt, tf(0), td(72, 720), tj("x", 0), td(0, -2.5), tj("y", 1), et}, "x\ny"},
		{"no newline before the first glyph", []op{bt, tf(12), td(72, 720), td(0, -100), tj("x", 0), et}, "x"},
		{"several moves write one newline each", []op{bt, tf(12), td(72, 720), tj("x", 0), td(0, -20), td(0, -20), tj("y", 1), et}, "x\n\ny"},
		{"quote moves then shows", []op{bt, tf(12), {"TL", nums(14), NoOwner}, td(72, 720), tj("a", 0), {"'", []parse.Object{str("b")}, 1}, et}, "a\nb"},
		{"double quote moves then shows", []op{bt, tf(12), {"TL", nums(14), NoOwner}, td(72, 720), tj("a", 0), {"\"", []parse.Object{parse.Integer(1), parse.Integer(2), str("b")}, 1}, et}, "a\nb"},
		{"TD sets leading", []op{bt, tf(12), td(72, 720), tj("a", 0), {"TD", nums(0, -14), NoOwner}, tj("b", 1), {"T*", nil, NoOwner}, tj("c", 2), et}, "a\nb\nc"},
		{"Tm absolute line", []op{bt, tf(12), {"Tm", nums(1, 0, 0, 1, 72, 720), NoOwner}, tj("a", 0), {"Tm", nums(1, 0, 0, 1, 72, 700), NoOwner}, tj("b", 1), et}, "a\nb"},
		{"TJ gap is a space", []op{bt, tf(12), tjArr(0, str("Hello"), parse.Integer(-300), str("World")), et}, "Hello World"},
		{"TJ real gap", []op{bt, tf(12), tjArr(0, str("a"), parse.Real(-100), str("b")), et}, "a b"},
		{"TJ kerning is not a space", []op{bt, tf(12), tjArr(0, str("a"), parse.Integer(-99), str("b")), et}, "ab"},
		{"TJ leading gap on empty output", []op{bt, tf(12), tjArr(0, parse.Integer(-500), str("a")), et}, "a"},
		{"TJ gap after whitespace", []op{bt, tf(12), tj("a ", 0), tjArr(1, parse.Integer(-500), str("b")), et}, "a b"},
		{"TJ two gaps one space", []op{bt, tf(12), tjArr(0, str("a"), parse.Integer(-500), parse.Integer(-500), str("b")), et}, "a b"},
		{"Tc Tw Tz Ts ignored", []op{bt, tf(12), tj("SECRET-", 0), {"Tc", nums(5), NoOwner}, {"Tw", nums(10), NoOwner}, {"Tz", nums(150), NoOwner}, {"Ts", nums(3), NoOwner}, tj("1234", 1), et}, "SECRET-1234"},
		{"q Q do not restore the size", []op{bt, tf(24), td(72, 700), tj("a", 0), {"q", nil, NoOwner}, tf(2), {"Q", nil, NoOwner}, td(0, -5), tj("b", 1), et}, "a\nb"},
		{"empty string writes nothing", []op{bt, tf(12), tj("", 0), et}, ""},
		{"wrong operand types ignored", []op{bt, {"Tf", []parse.Object{parse.Integer(1), parse.Integer(2)}, NoOwner}, {"Tj", []parse.Object{parse.Integer(1)}, 0}, {"TJ", []parse.Object{str("x")}, 0}, {"Td", nums(1), NoOwner}, et}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.ops).String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFlow_Owned(t *testing.T) {
	bt := op{"BT", nil, NoOwner}
	tests := []struct {
		name string
		ops  []op
		text string
		want []Piece
	}{
		{"nothing written", []op{bt, tf(12), td(72, 720)}, "", nil},
		{"one piece per operator", []op{bt, tf(12), tj("SEC", 0), tj("RET", 1)}, "SECRET",
			[]Piece{{0, 0}, {3, 1}}},
		{"TJ spaces owned by their operator", []op{bt, tf(12), tj("a", 0), tjArr(1, parse.Integer(-300), str("b"), parse.Integer(-300), str("c"))}, "a b c",
			[]Piece{{0, 0}, {1, 1}}},
		{"newline is a separator", []op{bt, tf(12), td(72, 720), tj("a", 0), td(0, -20), tj("b", 1)}, "a\nb",
			[]Piece{{0, 0}, {1, NoOwner}, {2, 1}}},
		{"quote newline is a separator", []op{bt, tf(12), {"TL", nums(14), NoOwner}, td(72, 720), tj("a", 0), {"'", []parse.Object{str("b")}, 1}}, "a\nb",
			[]Piece{{0, 0}, {1, NoOwner}, {2, 1}}},
		{"consecutive newlines one piece", []op{bt, tf(12), td(72, 720), tj("a", 0), td(0, -20), td(0, -20), tj("b", 1)}, "a\n\nb",
			[]Piece{{0, 0}, {1, NoOwner}, {3, 1}}},
		{"unattributed", []op{bt, tf(12), td(72, 720), tj("a", NoOwner), td(0, -20), tj("b", NoOwner)}, "a\nb",
			[]Piece{{0, NoOwner}}},
		{"empty operator owns nothing", []op{bt, tf(12), tj("a", 0), tj("", 1), tj("b", 2)}, "ab",
			[]Piece{{0, 0}, {1, 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := run(tt.ops)
			if got := f.String(); got != tt.text {
				t.Fatalf("String() = %q, want %q", got, tt.text)
			}
			if got := f.Owned(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Owned() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFlow_Fonts(t *testing.T) {
	var asked []parse.Name
	f := New(func(name parse.Name) *fontmodel.Font {
		asked = append(asked, name)
		return nil // Latin-1 fallback
	})
	f.Apply("Tf", []parse.Object{parse.Name("F2"), parse.Integer(12)}, NoOwner)
	f.Apply("Tj", []parse.Object{parse.String{Value: []byte{0xE9}}}, 0)
	if got := f.String(); got != "é" {
		t.Errorf("String() = %q, want Latin-1 %q", got, "é")
	}
	if !reflect.DeepEqual(asked, []parse.Name{"F2"}) {
		t.Errorf("fonts asked for %v, want [F2]", asked)
	}
}

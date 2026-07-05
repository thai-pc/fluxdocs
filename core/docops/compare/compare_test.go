package compare

import (
	"strings"
	"testing"
)

// render turns segments into a compact "op:text" string for assertions.
func render(segs []Segment) string {
	labels := map[Op]string{Equal: "=", Insert: "+", Delete: "-"}
	var parts []string
	for _, s := range segs {
		parts = append(parts, labels[s.Op]+s.Text)
	}
	return strings.Join(parts, "|")
}

func TestWords(t *testing.T) {
	tests := []struct {
		name, a, b, want string
	}{
		{"identical", "the cat sat", "the cat sat", "=the cat sat"},
		{"insert_end", "the cat", "the cat sat", "=the cat|+sat"},
		{"delete_end", "the cat sat", "the cat", "=the cat|-sat"},
		{"substitute_middle", "the cat sat", "the dog sat", "=the|-cat|+dog|=sat"},
		{"insert_start", "world", "hello world", "+hello|=world"},
		{"all_new", "a b", "c d", "-a b|+c d"},
		{"empty_both", "", "", ""},
		{"empty_to_text", "", "x y", "+x y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(Words(tt.a, tt.b)); got != tt.want {
				t.Errorf("Words(%q,%q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestWords_CoarseFallback(t *testing.T) {
	// Beyond maxTokens the diff degrades to a whole-block delete+insert rather
	// than an O(n*m) fine diff.
	a := strings.Repeat("a ", maxTokens+10)
	b := strings.Repeat("b ", maxTokens+10)
	segs := Words(a, b)
	if len(segs) != 2 || segs[0].Op != Delete || segs[1].Op != Insert {
		t.Errorf("coarse diff = %d segs (%v), want [Delete, Insert]", len(segs), segs)
	}
	// Identical large inputs collapse to a single Equal.
	same := Words(a, a)
	if len(same) != 1 || same[0].Op != Equal {
		t.Errorf("coarse identical = %v, want one Equal", same)
	}
}

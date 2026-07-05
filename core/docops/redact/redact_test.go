package redact

import (
	"regexp"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// hasShowOp reports whether the content still contains a text-showing operator
// whose string operand contains want.
func hasText(content []byte, want string) bool {
	for _, op := range parse.ScanContent(content) {
		if op.Op != "Tj" && op.Op != "TJ" && op.Op != "'" && op.Op != "\"" {
			continue
		}
		for _, o := range op.Operands {
			if s, ok := o.(parse.String); ok && strings.Contains(string(s.Value), want) {
				return true
			}
			if a, ok := o.(parse.Array); ok {
				for _, e := range a {
					if s, ok := e.(parse.String); ok && strings.Contains(string(s.Value), want) {
						return true
					}
				}
			}
		}
	}
	return false
}

func TestRedactContent_RemovesIntersectingTj(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (SECRET) Tj 0 -100 Td (public) Tj ET")
	// A region over the secret's baseline (100,700).
	out := RedactContent(content, []Rect{{X: 90, Y: 692, Width: 120, Height: 24}})

	if hasText(out, "SECRET") {
		t.Errorf("secret Tj survived redaction: %q", out)
	}
	if !hasText(out, "public") {
		t.Errorf("non-secret Tj was removed: %q", out)
	}
	// A black rectangle is drawn over the region.
	if !strings.Contains(string(out), "re f") {
		t.Errorf("no black rectangle appended: %q", out)
	}
}

func TestRedactContent_RemovesTJArray(t *testing.T) {
	content := []byte("BT /F1 12 Tf 50 500 Td [(TOP) -250 (SECRET)] TJ ET")
	out := RedactContent(content, []Rect{{X: 40, Y: 492, Width: 200, Height: 24}})
	if hasText(out, "SECRET") || hasText(out, "TOP") {
		t.Errorf("TJ array not fully removed: %q", out)
	}
}

func TestRedactContent_NoRegionKeepsText(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (keep me) Tj ET")
	// A region far from the text.
	out := RedactContent(content, []Rect{{X: 0, Y: 0, Width: 10, Height: 10}})
	if !hasText(out, "keep me") {
		t.Errorf("text outside the region was removed: %q", out)
	}
}

func TestRedactContent_EmptyRectsUnchanged(t *testing.T) {
	content := []byte("BT (x) Tj ET")
	if got := RedactContent(content, nil); string(got) != string(content) {
		t.Errorf("empty rects changed content: %q", got)
	}
}

func TestFindTextRects(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (SECRET-1234) Tj 0 -100 Td (public) Tj ET")
	rects := FindTextRects(content, regexp.MustCompile(`SECRET-\d+`))
	if len(rects) != 1 {
		t.Fatalf("got %d rects, want 1", len(rects))
	}
	// The rect should sit near the text baseline (100,700).
	r := rects[0]
	if r.X < 90 || r.X > 110 || r.Y < 680 || r.Y > 705 {
		t.Errorf("match rect = %+v, not near the text origin (100,700)", r)
	}
	if r.Width <= 0 || r.Height <= 0 {
		t.Errorf("degenerate match rect: %+v", r)
	}
}

func TestRedactContent_RotatedText(t *testing.T) {
	// A 90° rotation via cm; the redaction rect is given in the rotated page
	// space, so the transformed run box must still be caught.
	content := []byte("q 0 1 -1 0 0 0 cm BT /F1 12 Tf 100 100 Td (SECRET) Tj ET Q")
	// After cm [0 1 -1 0 0 0], text at text-space (100,100) maps to page
	// (-100,100); cover that neighborhood.
	out := RedactContent(content, []Rect{{X: -140, Y: 92, Width: 80, Height: 120}})
	if hasText(out, "SECRET") {
		t.Errorf("rotated secret survived: %q", out)
	}
}

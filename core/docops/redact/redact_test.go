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

// mustRedact is RedactContent for well-formed content: an error fails the test.
func mustRedact(t *testing.T, content []byte, rects []Rect) []byte {
	t.Helper()
	out, err := RedactContent(content, rects)
	if err != nil {
		t.Fatalf("RedactContent: %v", err)
	}
	return out
}

func TestRedactContent_RemovesIntersectingTj(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (SECRET) Tj 0 -100 Td (public) Tj ET")
	// A region over the secret's baseline (100,700).
	out := mustRedact(t, content, []Rect{{X: 90, Y: 692, Width: 120, Height: 24}})

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
	out := mustRedact(t, content, []Rect{{X: 40, Y: 492, Width: 200, Height: 24}})
	if hasText(out, "SECRET") || hasText(out, "TOP") {
		t.Errorf("TJ array not fully removed: %q", out)
	}
}

func TestRedactContent_NoRegionKeepsText(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (keep me) Tj ET")
	// A region far from the text.
	out := mustRedact(t, content, []Rect{{X: 0, Y: 0, Width: 10, Height: 10}})
	if !hasText(out, "keep me") {
		t.Errorf("text outside the region was removed: %q", out)
	}
}

func TestRedactContent_EmptyRectsUnchanged(t *testing.T) {
	content := []byte("BT (x) Tj ET")
	if got := mustRedact(t, content, nil); string(got) != string(content) {
		t.Errorf("empty rects changed content: %q", got)
	}
}

func TestFindTextRects(t *testing.T) {
	content := []byte("BT /F1 12 Tf 100 700 Td (SECRET-1234) Tj 0 -100 Td (public) Tj ET")
	rects, err := FindTextRects(content, regexp.MustCompile(`SECRET-\d+`))
	if err != nil {
		t.Fatal(err)
	}
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
	out := mustRedact(t, content, []Rect{{X: -140, Y: 92, Width: 80, Height: 120}})
	if hasText(out, "SECRET") {
		t.Errorf("rotated secret survived: %q", out)
	}
}

// TestRedact_FailsClosedOnDiscardedOperands: when the scanner would drop
// operands to recover from a syntax error, both entry points return an error
// and no result, so a string next to the bad operand cannot escape unseen.
// A balanced over-deep sibling is not an error: it becomes a Null placeholder
// and the string is still found.
func TestRedact_FailsClosedOnDiscardedOperands(t *testing.T) {
	re := regexp.MustCompile(`SECRET`)
	region := []Rect{{X: 0, Y: 0, Width: 1000, Height: 1000}}
	deep := strings.Repeat("[", 300) + strings.Repeat("]", 300)

	for _, bad := range []string{
		"BT /F1 12 Tf 50 500 Td [(SECRET) <zz>] TJ ET",
		"BT /F1 12 Tf 50 500 Td [(SECRET) " + strings.Repeat("[", 300) + "] TJ ET",
		"BT /F1 12 Tf 50 500 Td (SECRET) ] Tj ET",
	} {
		if rects, err := FindTextRects([]byte(bad), re); err == nil || rects != nil {
			t.Errorf("FindTextRects(%.40q) = (%v, %v), want (nil, error)", bad, rects, err)
		}
		if out, err := RedactContent([]byte(bad), region); err == nil || out != nil {
			t.Errorf("RedactContent(%.40q) = (%q, %v), want (nil, error)", bad, out, err)
		}
	}

	ok := []byte("BT /F1 12 Tf 50 500 Td [(SECRET) " + deep + "] TJ ET")
	if rects, err := FindTextRects(ok, re); err != nil || len(rects) != 1 {
		t.Errorf("FindTextRects(deep sibling) = (%v, %v), want one rect", rects, err)
	}
	if out := mustRedact(t, ok, region); hasText(out, "SECRET") {
		t.Errorf("secret next to a deep sibling survived: %.80q", out)
	}
}

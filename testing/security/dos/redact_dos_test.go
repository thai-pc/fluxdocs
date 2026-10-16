package dos

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/docops/redact"
)

// fontCandidatesDoc is a page whose content selects, by gs, each of n
// ExtGStates that only the /Pages node's resources define (so every gs may
// or may not apply, and the redaction text model keeps both the current and
// the new font), each with its own font, then shows m strings. Before the
// font-set cap one RedactText on n = 4000, m = 1600 took 77 s and 404 MB:
// every string was decoded once per accumulated font (go-reviewer, batch B).
func fontCandidatesDoc(t *testing.T, n, m int) *core.Document {
	t.Helper()
	var eg, c strings.Builder
	eg.WriteString("<< /ExtGState << ")
	c.WriteString("BT 10 700 Td ")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&eg, "/G%d << /Font [<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica "+
			"/Encoding << /Differences [120 /uni%04X] >> >> 12] >> ", i, 0x4E00+i)
		fmt.Fprintf(&c, "/G%d gs ", i)
	}
	eg.WriteString(">> >>")
	c.WriteString(strings.Repeat("(x) Tj ", m))
	c.WriteString("ET")
	content := c.String()
	doc, err := core.OpenBytes(classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] /Resources " + eg.String() + " >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDoS_RedactFontCandidatesAreCapped: past the font-set cap the page is
// refused (budget_exceeded) at once; at the cap the work stays linear.
func TestDoS_RedactFontCandidatesAreCapped(t *testing.T) {
	skipUnlessTimed(t)

	start := time.Now()
	_, err := fontCandidatesDoc(t, 4000, 1600).RedactText(0, `SECRET`)
	var re *redact.RefusalError
	if !errors.As(err, &re) || re.Reason != redact.ReasonBudgetExceeded {
		t.Fatalf("RedactText = %v, want a budget_exceeded refusal", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("refusal took %v, want it prompt", d)
	}

	// Seven fallback fonts plus "no font": the largest set allowed.
	start = time.Now()
	if _, err := fontCandidatesDoc(t, 7, 50000).RedactText(0, `SECRET`); err != nil {
		t.Fatalf("RedactText at the cap: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("RedactText at the cap took %v for 50000 strings, want linear work", d)
	}
}

package redaction

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// FuzzRedact hardens the redaction leak gate: over a fixed secret document, it
// always redacts the exact region covering the secret, then piles on a FUZZED
// regex pattern and a FUZZED extra region (arbitrary — including NaN, Inf, huge,
// or negative coordinates). No matter what garbage is added:
//   - redaction must never panic or fail its own verification (a fuzz
//     failure), and
//   - the secret must never survive extraction (adding regions only removes
//     more), and must not remain in the serialized bytes.
//
// Run: go test ./testing/security/redaction -run=xxx -fuzz=FuzzRedact -fuzztime=60s
func FuzzRedact(f *testing.F) {
	f.Add("SECRET-\\d+", 90.0, 692.0, 160.0, 24.0)
	f.Add("", 0.0, 0.0, 0.0, 0.0)
	f.Add("(", -1e9, 1e9, -5.0, 1e18) // invalid regex + degenerate rect
	f.Add(".*", math.NaN(), math.Inf(1), math.Inf(-1), math.NaN())
	f.Add("[0-9]{4}", 1e30, 1e30, 1e30, 1e30) // absurdly large

	f.Fuzz(func(t *testing.T, pattern string, x, y, w, h float64) {
		doc, err := core.OpenBytes(secretPDF())
		if err != nil {
			t.Skip()
		}

		// The intended redaction: a fixed region over the secret baseline.
		if err := doc.RedactArea(0, core.Rect{X: 90, Y: 692, Width: 160, Height: 24}); err != nil {
			t.Fatalf("fixed RedactArea failed: %v", err)
		}
		// Fuzzed additions — errors are fine, panics are not. The API recovers
		// a panic into ErrInternal, so that error counts as one.
		_, err = doc.RedactText(0, pattern)
		failOnInternal(t, err)
		_ = doc.RedactArea(0, core.Rect{X: x, Y: y, Width: w, Height: h})

		out, err := doc.RedactAndFlatten()
		failOnInternal(t, err)
		if err != nil {
			return // a graceful failure is acceptable; a panic is not
		}

		text, err := out.ExtractText(core.ExtractOptions{})
		failOnInternal(t, err)
		if err != nil {
			return
		}
		if strings.Contains(text, "SECRET") || strings.Contains(text, "1234") {
			t.Fatalf("LEAK: secret survived redaction (pattern=%q rect=%g,%g,%g,%g): %q",
				pattern, x, y, w, h, text)
		}

		// Verbatim output (what the CLI/MCP write) is the strict check — a
		// re-save would rebuild the graph and drop an orphaned original-content
		// stream, hiding a leak.
		if raw, err := out.Bytes(); err == nil {
			if bytes.Contains(raw, []byte("SECRET-1234")) {
				t.Fatalf("LEAK: secret bytes remain in verbatim output (pattern=%q)", pattern)
			}
		}
		var buf bytes.Buffer
		if err := out.SaveTo(&buf); err == nil {
			if bytes.Contains(buf.Bytes(), []byte("SECRET-1234")) {
				t.Fatalf("LEAK: secret bytes remain after re-save (pattern=%q)", pattern)
			}
		} else {
			failOnInternal(t, err)
		}
	})
}

// failOnInternal fails the test, with the recovered stack, when err is
// ErrInternal: a panic recovered by the public API is still a redaction bug,
// and so is a verification failure (ErrRedactionVerification: the output
// still held removed text, §12.5).
func failOnInternal(t *testing.T, err error) {
	t.Helper()
	var ie *core.InternalError
	if errors.As(err, &ie) {
		t.Fatalf("recovered panic (a bug, not an input error): %v\n%s", err, ie.Stack)
	}
	if errors.Is(err, core.ErrRedactionVerification) {
		t.Fatalf("redaction verification failed (a bug): %v", err)
	}
}

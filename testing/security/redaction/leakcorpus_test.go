package redaction

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf16"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/docops/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file runs the leak corpus (docs/design/redaction-hardening.md §12.1):
// one table (leakcorpus_cases_test.go) holding every repro from every review
// round. Each row is a fixture, an operation and an expectation:
//
//   - clean: redaction succeeds and the secret is gone by all three checks —
//     our ExtractText of the output; the output bytes, a re-save, every decoded
//     stream and every decoded string (including the strings content streams
//     show, TJ elements joined); and Ghostscript's txtwrite text of the output
//     (whose input must show the secret, unless the row is gsBlind).
//   - refuse: redaction fails closed with the expected reason, no document is
//     returned, and the plan stays failed.
//   - knownLeak: a leak a later Phase 1 step closes (closesIn). It is skipped,
//     with the step, while it reproduces, and fails once it no longer does.
//
// The Ghostscript checks skip per subtest when gs is not installed, unless
// FLUXDOCS_REQUIRE_GS=1 (set in CI), which makes its absence a failure.

type leakOp int

const (
	opText leakOp = iota // RedactText(page, textPattern) on every marked page
	opArea               // RedactArea(page, area or the whole page)
)

func (o leakOp) String() string { return [...]string{"text", "area"}[o] }

type leakExpect int

const (
	clean leakExpect = iota
	refuse
	knownLeak
)

// leakCase is one leak-corpus fixture; it runs once per operation in ops.
type leakCase struct {
	name     string
	pdf      func() []byte
	ops      []leakOp
	area     *core.Rect // opArea region; nil is the whole 400x800 page
	want     leakExpect
	reason   string // refuse: the expected reason, alternatives separated by '|'
	closesIn string // knownLeak: the step that closes it -> its outcome then
	gsBlind  bool   // clean: Ghostscript does not show the secret even in the input
}

// textPattern is the RedactText pattern of every opText row.
const textPattern = `SECRET-\d+`

// requireGS reports whether a missing Ghostscript fails the oracle checks.
var requireGS = os.Getenv("FLUXDOCS_REQUIRE_GS") == "1"

// TestLeakCorpus runs every leak-corpus row.
func TestLeakCorpus(t *testing.T) {
	seen := map[string]bool{}
	for _, lc := range leakCases() {
		for _, op := range lc.ops {
			name := lc.name + "/" + op.String()
			if seen[name] {
				t.Fatalf("duplicate leak-corpus row %s", name)
			}
			seen[name] = true
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				runLeakCase(t, lc, op)
			})
		}
	}
}

// leakRun is the outcome of one operation on a fixture.
type leakRun struct {
	input   []byte
	markErr error // RedactText's error (opText)
	out     *core.Document
	err     error // RedactAndFlatten's error
	doc     *core.Document
}

// apply runs op on a fresh document opened from lc's fixture.
func apply(t *testing.T, lc leakCase, op leakOp) leakRun {
	t.Helper()
	r := leakRun{input: lc.pdf()}
	doc, err := core.OpenBytes(r.input)
	if err != nil {
		t.Fatalf("fixture does not open: %v", err)
	}
	r.doc = doc
	switch op {
	case opText:
		_, r.markErr = doc.RedactText(0, textPattern)
	case opArea:
		area := core.Rect{X: 0, Y: 0, Width: 400, Height: 800}
		if lc.area != nil {
			area = *lc.area
		}
		if err := doc.RedactArea(0, area); err != nil {
			t.Fatal(err)
		}
	}
	r.out, r.err = doc.RedactAndFlatten()
	return r
}

func runLeakCase(t *testing.T, lc leakCase, op leakOp) {
	r := apply(t, lc, op)
	switch lc.want {
	case refuse:
		if r.err == nil || r.out != nil {
			t.Fatalf("RedactAndFlatten = (%v, %v), want (nil, refusal %s)", r.out, r.err, lc.reason)
		}
		if got := refusalReason(r.err); !matchReason(got, lc.reason) {
			t.Errorf("refusal reason %s (%v), want %s", got, r.err, lc.reason)
		}
		if out, err := r.doc.RedactAndFlatten(); err == nil || out != nil {
			t.Errorf("second RedactAndFlatten = (%v, %v), want the refusal again", out, err)
		}

	case clean:
		if r.markErr != nil {
			t.Fatalf("RedactText: %v", r.markErr)
		}
		if r.err != nil {
			t.Fatalf("RedactAndFlatten: %v", r.err)
		}
		t.Run("ours", func(t *testing.T) {
			for _, l := range ourLeaks(t, r.out) {
				t.Errorf("LEAK: %s", l)
			}
		})
		t.Run("gs", func(t *testing.T) {
			if !lc.gsBlind {
				in, ok := gsOrSkip(t, r.input)
				if ok && !containsNeedle(in, secret) {
					t.Fatalf("fixture broken: Ghostscript does not show the secret in the input: %q", in)
				}
			}
			raw, err := r.out.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if text, ok := gsOrSkip(t, raw); ok && containsNeedle(text, secret) {
				t.Errorf("LEAK: Ghostscript shows the secret in the output: %q", text)
			}
		})

	case knownLeak:
		if lc.closesIn == "" {
			t.Fatal("knownLeak row without closesIn")
		}
		var leaks []string
		switch {
		case r.markErr != nil || r.err != nil:
			// Refused: not a leak any more.
		default:
			leaks = ourLeaks(t, r.out)
			raw, err := r.out.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			text, ok := gsText(t, raw)
			if ok && containsNeedle(text, secret) {
				leaks = append(leaks, "Ghostscript shows the secret")
			}
			if len(leaks) == 0 && !ok {
				if requireGS {
					t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
				}
				t.Skipf("known leak, closes in step %s; gs not found, cannot confirm it still reproduces", lc.closesIn)
			}
		}
		if len(leaks) == 0 {
			t.Fatalf("known leak no longer reproduces (markErr %v, err %v): promote the row to clean or refuse (was: closes in %s)",
				r.markErr, r.err, lc.closesIn)
		}
		t.Skipf("known leak, closes in step %s: %s", lc.closesIn, strings.Join(leaks, "; "))
	}
}

// gsOrSkip returns Ghostscript's text of pdf. Without gs it skips the calling
// subtest, or fails it when FLUXDOCS_REQUIRE_GS=1.
func gsOrSkip(t *testing.T, pdf []byte) (string, bool) {
	t.Helper()
	text, ok := gsText(t, pdf)
	if !ok {
		if requireGS {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not found; oracle skipped")
	}
	return text, ok
}

// containsNeedle reports whether haystack holds needle once white-space is
// removed from both (extraction spacing is heuristic).
func containsNeedle(haystack, needle string) bool {
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
	}
	return strings.Contains(strip(haystack), strip(needle))
}

// ourLeaks returns where the secret survives in out by our own checks (§12.1
// assertions 1 and 2): ExtractText; the verbatim bytes and a re-save; and every
// object of the output — strings decoded as Latin-1 or UTF-16BE, names,
// streams decoded through their filters, and the strings content streams show
// (each, and joined in order, so a TJ split across elements still matches).
func ourLeaks(t *testing.T, out *core.Document) []string {
	t.Helper()
	var leaks []string
	if text, err := out.ExtractText(core.ExtractOptions{}); err != nil {
		t.Fatalf("ExtractText: %v", err)
	} else if containsNeedle(text, secret) {
		leaks = append(leaks, "ExtractText shows the secret")
	}
	raw, err := out.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		leaks = append(leaks, "verbatim output bytes hold the secret")
	}
	var buf bytes.Buffer
	if err := out.SaveTo(&buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte(secret)) {
		leaks = append(leaks, "re-saved bytes hold the secret")
	}
	r, err := parse.NewResolver(raw)
	if err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	for num := 1; num <= r.MaxObjectNumber(); num++ {
		obj, err := r.Resolve(parse.Reference{Number: num})
		if err != nil || obj == nil {
			continue
		}
		if where := objectLeak(obj); where != "" {
			leaks = append(leaks, fmt.Sprintf("object %d: %s", num, where))
		}
	}
	return leaks
}

// objectLeak returns where the secret occurs in o, or "".
func objectLeak(o parse.Object) string {
	switch v := o.(type) {
	case parse.String:
		if stringLeak(v.Value) {
			return "a string"
		}
	case parse.Name:
		if strings.Contains(string(v), secret) {
			return "a name"
		}
	case parse.Array:
		for _, el := range v {
			if w := objectLeak(el); w != "" {
				return w
			}
		}
	case parse.Dict:
		for _, el := range v {
			if w := objectLeak(el); w != "" {
				return w
			}
		}
	case *parse.Stream:
		if w := objectLeak(v.Dict); w != "" {
			return "stream dictionary: " + w
		}
		data, err := parse.DecodeStream(v)
		if err != nil {
			data = v.Raw
		}
		if bytes.Contains(data, []byte(secret)) || stringLeak(data) {
			return "stream data"
		}
		var shown []byte
		for _, op := range parse.ScanContent(data) {
			for _, operand := range op.Operands {
				collectStrings(operand, &shown)
			}
		}
		if stringLeak(shown) {
			return "strings shown by a content stream"
		}
	}
	return ""
}

// collectStrings appends every string in o, at any depth, to dst.
func collectStrings(o parse.Object, dst *[]byte) {
	switch v := o.(type) {
	case parse.String:
		*dst = append(*dst, v.Value...)
	case parse.Array:
		for _, el := range v {
			collectStrings(el, dst)
		}
	case parse.Dict:
		for _, el := range v {
			collectStrings(el, dst)
		}
	}
}

// stringLeak reports whether b holds the secret as Latin-1/PDFDocEncoding
// bytes or as UTF-16BE (with or without a byte-order mark, at either byte
// alignment — 2-byte font codes equal to code points read as UTF-16BE).
func stringLeak(b []byte) bool {
	if bytes.Contains(b, []byte(secret)) {
		return true
	}
	for off := 0; off < 2 && off < len(b); off++ {
		u := make([]uint16, 0, len(b)/2)
		for i := off; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		if strings.Contains(string(utf16.Decode(u)), secret) {
			return true
		}
	}
	return false
}

// inlineImageLoss matches the strict scanner's inline-image losses.
var inlineImageLoss = regexp.MustCompile(`content stream: (malformed inline image|inline image )|for redaction: inline image `)

// refusalReason classifies a redaction error into the reason codes of the
// design (§9 P1). Until core.RedactionError carries the reason (step 1.8), it
// is derived from the error chain — a *redact.RefusalError carries its reason
// (form_text, pattern_text, softmask_text, type3_text, budget_exceeded,
// unsupported_xobject) — and the scanner's loss description, which never
// holds document bytes. testing/corpus/redaction_regress_test.go has the
// same classifier.
func refusalReason(err error) string {
	var ie *core.InternalError
	var re *redact.RefusalError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &ie):
		return "internal"
	case errors.As(err, &re):
		return string(re.Reason)
	case errors.Is(err, redact.ErrFormXObjectText):
		return "form_text"
	case inlineImageLoss.MatchString(err.Error()):
		return "inline_image"
	case strings.Contains(err.Error(), "cannot be scanned safely"):
		return "content_ambiguous"
	}
	return "error"
}

// matchReason reports whether got is one of the '|'-separated reasons in want.
func matchReason(got, want string) bool {
	for _, w := range strings.Split(want, "|") {
		if got == w {
			return true
		}
	}
	return false
}

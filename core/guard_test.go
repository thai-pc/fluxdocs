package core

import (
	"errors"
	"strings"
	"testing"
)

// injectPanic installs testHookPanic for the test's duration: it panics with a
// runtime error (an out-of-range index, like a real parser bug) when op and
// page match one of the targets.
func injectPanic(t *testing.T, targets map[string][]int) {
	t.Helper()
	testHookPanic = func(op string, page int) {
		for _, p := range targets[op] {
			if p == page {
				var s []int
				_ = s[page+5] // runtime error: index out of range
			}
		}
	}
	t.Cleanup(func() { testHookPanic = nil })
}

// asInternal asserts err is an *InternalError matching ErrInternal for op and
// page, with a captured stack, and returns it.
func asInternal(t *testing.T, err error, op string, page int) *InternalError {
	t.Helper()
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("err = %v, want ErrInternal", err)
	}
	var ie *InternalError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %T, want *InternalError", err)
	}
	if ie.Op != op || ie.Page != page {
		t.Errorf("InternalError{Op: %q, Page: %d}, want {%q, %d}", ie.Op, ie.Page, op, page)
	}
	if !strings.Contains(string(ie.Stack), "injectTestPanic") {
		t.Errorf("Stack does not show the panicking frame:\n%s", ie.Stack)
	}
	return ie
}

func TestRenderPage_RecoversPanic(t *testing.T) {
	doc := multiPageDoc(t, "one", "two")
	injectPanic(t, map[string][]int{"RenderPage": {1}})

	img, err := doc.RenderPage(1, RenderOptions{DPI: 36, Format: "png"})
	if img != nil {
		t.Errorf("RenderPage returned %d bytes with an error, want nil", len(img))
	}
	ie := asInternal(t, err, "RenderPage", 1)
	msg := ie.Error()
	for _, want := range []string{"fluxdocs: internal error in RenderPage", "(page 1)", "index out of range"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}

	// Only that call failed: other pages still render.
	if _, err := doc.RenderPage(0, RenderOptions{DPI: 36, Format: "png"}); err != nil {
		t.Errorf("RenderPage(0) after a recovered panic: %v", err)
	}
}

// TestRenderAllPages_WorkerPanicIsPageError forces panics inside the worker
// goroutines — natively an unrecovered one there kills the process — and
// checks each becomes that page's error under the existing batch semantics:
// no buffers, the first failing page in page order.
func TestRenderAllPages_WorkerPanicIsPageError(t *testing.T) {
	tests := []struct {
		name     string
		targets  map[string][]int
		op       string
		wantPage int
	}{
		{"panic in the worker", map[string][]int{"RenderAllPages": {2}}, "RenderAllPages", 2},
		{"panic in RenderPage under a worker", map[string][]int{"RenderPage": {1}}, "RenderPage", 1},
		{"several pages: first in page order", map[string][]int{"RenderAllPages": {3, 1}}, "RenderAllPages", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := multiPageDoc(t, "a", "b", "c", "d")
			injectPanic(t, tc.targets)
			imgs, err := doc.RenderAllPages(RenderOptions{DPI: 36, Format: "png"})
			if imgs != nil {
				t.Errorf("RenderAllPages returned %d buffers with an error, want nil", len(imgs))
			}
			asInternal(t, err, tc.op, tc.wantPage)
		})
	}
}

func TestOpenBytes_RecoversPanic(t *testing.T) {
	injectPanic(t, map[string][]int{"OpenBytes": {-1}})
	doc, err := OpenBytes(minimalDocBytes())
	if doc != nil {
		t.Error("OpenBytes returned a document with an error, want nil")
	}
	ie := asInternal(t, err, "OpenBytes", -1)
	if strings.Contains(ie.Error(), "(page") {
		t.Errorf("Error() = %q, want no page for a document-level operation", ie.Error())
	}
}

// TestInternalError_NeverEchoesPanicValue pins that Error() shows a panic value
// only when the runtime generated its text: an arbitrary value (which could be
// built from document bytes) is reported by type alone.
func TestInternalError_NeverEchoesPanicValue(t *testing.T) {
	for _, v := range []any{"SSN 078-05-1120", errors.New("SSN 078-05-1120"), []byte("SSN 078-05-1120")} {
		msg := (&InternalError{Op: "ExtractTextByPage", Page: 0, Value: v}).Error()
		if strings.Contains(msg, "078-05-1120") {
			t.Errorf("Error() = %q echoes the panic value", msg)
		}
		if !strings.HasPrefix(msg, "fluxdocs: internal error in ExtractTextByPage (page 0): panic value of type ") {
			t.Errorf("Error() = %q, want the op, page, and value type", msg)
		}
	}
}

// TestRenderPage_CallbackPanicPropagates pins that OnPageRendered runs outside
// RenderPage's recovery: a panic in the caller's own callback is the caller's
// bug and reaches the caller unchanged, not as a FluxDocs ErrInternal.
func TestRenderPage_CallbackPanicPropagates(t *testing.T) {
	doc := multiPageDoc(t, "one")
	doc.OnPageRendered(func(int, []byte) { panic("caller callback bug") })

	defer func() {
		if r := recover(); r != "caller callback bug" {
			t.Errorf("recovered %v, want the callback's own panic value", r)
		}
	}()
	_, err := doc.RenderPage(0, RenderOptions{DPI: 36, Format: "png"})
	t.Fatalf("RenderPage returned (err = %v), want the callback panic to propagate", err)
}

// TestRenderAllPages_CallbackPanicReraisedOnCaller pins that a callback panic
// on a worker goroutine neither kills the process (an unrecovered worker panic
// would) nor turns into ErrInternal: it is re-raised on the calling goroutine
// after every worker has finished.
func TestRenderAllPages_CallbackPanicReraisedOnCaller(t *testing.T) {
	doc := multiPageDoc(t, "a", "b", "c", "d")
	doc.OnPageRendered(func(idx int, _ []byte) {
		if idx == 2 {
			panic("caller callback bug")
		}
	})

	defer func() {
		if r := recover(); r != "caller callback bug" {
			t.Errorf("recovered %v, want the callback's own panic value", r)
		}
	}()
	_, err := doc.RenderAllPages(RenderOptions{DPI: 36, Format: "png"})
	t.Fatalf("RenderAllPages returned (err = %v), want the callback panic re-raised", err)
}

// TestRedactAndFlatten_FailsClosedAfterRedactTextPanic pins the fail-closed
// rule (SECURITY.md §1): once RedactText fails after accepting its arguments,
// nothing was marked for that pattern, so RedactAndFlatten must refuse instead
// of saving the page unredacted. This is the MCP redact_text →
// redact_and_save sequence.
func TestRedactAndFlatten_FailsClosedAfterRedactTextPanic(t *testing.T) {
	doc := docWithText(t, "SSN 078-05-1120", "public")
	if err := doc.RedactArea(1, Rect{X: 0, Y: 0, Width: 10, Height: 10}); err != nil {
		t.Fatal(err)
	}
	injectPanic(t, map[string][]int{"RedactText": {0}})

	if _, err := doc.RedactText(0, `\d{3}-\d{2}-\d{4}`); !errors.Is(err, ErrInternal) {
		t.Fatalf("RedactText err = %v, want ErrInternal", err)
	}
	out, err := doc.RedactAndFlatten()
	if out != nil {
		t.Fatal("RedactAndFlatten returned a document after a failed marking, want nil")
	}
	if !errors.Is(err, ErrInternal) || !strings.Contains(err.Error(), "redaction plan incomplete") {
		t.Fatalf("RedactAndFlatten err = %v, want a plan-incomplete error wrapping ErrInternal", err)
	}

	// Sticky: a later successful marking does not clear it.
	testHookPanic = nil
	if _, err := doc.RedactText(0, `\d{3}-\d{2}-\d{4}`); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RedactAndFlatten(); err == nil {
		t.Fatal("RedactAndFlatten succeeded after a failed marking, want the plan to stay failed")
	}
}

// TestRedactAndFlatten_FailsClosedAfterAutoRedactPIIPartialFailure: an
// AutoRedactPII pass that fails midway has marked some pages but not others;
// flattening that partial plan would leak the unmarked pages.
func TestRedactAndFlatten_FailsClosedAfterAutoRedactPIIPartialFailure(t *testing.T) {
	doc := docWithText(t, "alice@example.com", "bob@example.com")
	injectPanic(t, map[string][]int{"RedactText": {1}})

	counts, err := doc.AutoRedactPII(PIIEmail)
	if counts != nil || !errors.Is(err, ErrInternal) {
		t.Fatalf("AutoRedactPII = (%v, %v), want (nil, ErrInternal)", counts, err)
	}
	if len(doc.redactions[0]) == 0 {
		t.Fatal("test setup: page 0 should have been marked before page 1 failed")
	}
	out, err := doc.RedactAndFlatten()
	if out != nil || err == nil || !strings.Contains(err.Error(), "redaction plan incomplete") {
		t.Fatalf("RedactAndFlatten = (document returned: %v, %v), want (nil, plan-incomplete error)", out != nil, err)
	}
}

// TestRedactText_ArgumentErrorsKeepPlanUsable pins the other side: rejected
// arguments mark nothing and leave the plan intact, so they do not block a
// later RedactAndFlatten.
func TestRedactText_ArgumentErrorsKeepPlanUsable(t *testing.T) {
	doc := docWithText(t, "SSN 078-05-1120")
	if _, err := doc.RedactText(5, "x"); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("RedactText bad page err = %v, want ErrPageNotFound", err)
	}
	if _, err := doc.RedactText(0, "("); err == nil {
		t.Fatal("RedactText bad regexp = nil error, want a compile error")
	}
	if _, err := doc.RedactText(0, `\d{3}-\d{2}-\d{4}`); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.RedactAndFlatten(); err != nil {
		t.Fatalf("RedactAndFlatten after argument errors: %v", err)
	}
}

// TestCompare_PropagatesExtractionFailure pins that a page whose text cannot be
// extracted fails Compare: it used to be read as "" on both sides, so two
// documents whose pages both failed compared as Identical.
func TestCompare_PropagatesExtractionFailure(t *testing.T) {
	a := docWithText(t, "alpha")
	b := docWithText(t, "beta")
	injectPanic(t, map[string][]int{"ExtractTextByPage": {0}})

	res, err := a.Compare(b)
	if res != nil {
		t.Errorf("Compare returned a result (Identical = %v) with an error, want nil", res.Identical)
	}
	asInternal(t, err, "ExtractTextByPage", 0)
}

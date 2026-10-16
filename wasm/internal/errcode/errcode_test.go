package errcode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/render"
)

// TestOf pins one row per code of the closed set (render-budget.md §6.1) and the
// precedence between codes an error matches more than once.
func TestOf(t *testing.T) {
	// The shapes core actually returns: a canvas error wraps both
	// ErrRenderBudgetExceeded and render.ErrCanvasTooLarge.
	canvas := fmt.Errorf("%w: page 0: %w", core.ErrRenderBudgetExceeded,
		fmt.Errorf("%w: 1e+18x1e+18 px exceeds 67108864 pixels", render.ErrCanvasTooLarge))
	budget := fmt.Errorf("%w: page 2: %w", core.ErrRenderBudgetExceeded,
		fmt.Errorf("%w: paint work 17 > 16", render.ErrBudgetExceeded))
	deadline := fmt.Errorf("render: %w", context.DeadlineExceeded)
	canceledWithCause := fmt.Errorf("render: %w: %w", context.Canceled, errors.New("client gone"))

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil is success", nil, ""},
		{"canvas cap beats budget", canvas, CanvasTooLarge},
		{"bare canvas sentinel", render.ErrCanvasTooLarge, CanvasTooLarge},
		{"budget trip", budget, BudgetExceeded},
		{"wrapped deadline", deadline, Timeout},
		{"bare deadline", context.DeadlineExceeded, Timeout},
		{"cancel with a cause", canceledWithCause, Canceled},
		{"invalid pdf", fmt.Errorf("open: %w", core.ErrInvalidPDF), InvalidPDF},
		{"encrypted", core.ErrEncryptedDocument, Encrypted},
		{"page not found", core.ErrPageNotFound, PageNotFound},
		{"core unsupported format", core.ErrUnsupportedFormat, UnsupportedFormat},
		{"render unsupported format", fmt.Errorf("%w: \"svg\"", render.ErrUnsupportedFormat), UnsupportedFormat},
		{"invalid annotation", core.ErrInvalidAnnotation, InvalidAnnotation},
		{"annotation not found", core.ErrAnnotationNotFound, InvalidAnnotation},
		{"invalid argument", core.ErrInvalidArgument, InvalidArgument},
		{"core recovered panic", &core.InternalError{Op: "RenderPage", Page: 0, Value: "x"}, Internal},
		{"unmapped error", errors.New("flate: corrupt input"), Unknown},
		{"unmapped sentinel", core.ErrReadOnly, Unknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Of(tc.err); got != tc.want {
				t.Errorf("Of(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestOf_NeverBoundaryOnlyCodes checks Of leaves invalid_handle to the WASM
// boundary and always returns a non-empty code for a non-nil error.
func TestOf_NeverBoundaryOnlyCodes(t *testing.T) {
	for _, err := range []error{errors.New("x"), core.ErrFeatureRequiresPro, core.ErrFormFieldNotFound, core.ErrInvalidPageOrder} {
		got := Of(err)
		if got == "" || got == InvalidHandle {
			t.Errorf("Of(%v) = %q, want a non-empty mapped code", err, got)
		}
	}
}

// TestPanicDetail applies the core.InternalError policy: a runtime error's own
// text, otherwise only the panic value's type, never its content.
func TestPanicDetail(t *testing.T) {
	var runtimeErr error
	func() {
		defer func() { runtimeErr, _ = recover().(error) }()
		var s []int
		_ = s[3]
	}()
	if got := PanicDetail(runtimeErr); !strings.Contains(got, "index out of range") {
		t.Errorf("PanicDetail(runtime error) = %q, want the runtime's text", got)
	}
	for _, v := range []any{"SSN 078-05-1120", errors.New("SSN 078-05-1120"), []byte("SSN 078-05-1120")} {
		got := PanicDetail(v)
		if strings.Contains(got, "078-05-1120") {
			t.Errorf("PanicDetail(%T) = %q echoes the value", v, got)
		}
		if !strings.HasPrefix(got, "panic value of type ") {
			t.Errorf("PanicDetail(%T) = %q, want the type only", v, got)
		}
	}
}

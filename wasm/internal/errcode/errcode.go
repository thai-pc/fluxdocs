// Package errcode maps FluxDocs errors to the stable "code" field of the WASM
// error envelope (design: docs/design/render-budget.md §6.1). It is a plain,
// untagged package so its table is tested by the native CI; wasm/main.go, which
// only builds under GOOS=js GOARCH=wasm, calls it.
package errcode

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/render"
)

// The closed set of envelope codes. New codes are additive; an existing code's
// meaning never changes. "" means success.
const (
	CanvasTooLarge    = "canvas_too_large"
	BudgetExceeded    = "budget_exceeded"
	Timeout           = "timeout"
	Canceled          = "canceled"
	InvalidPDF        = "invalid_pdf"
	Encrypted         = "encrypted"
	PageNotFound      = "page_not_found"
	UnsupportedFormat = "unsupported_format"
	InvalidAnnotation = "invalid_annotation"
	InvalidArgument   = "invalid_argument" // also the WASM boundary's own argument checks
	InvalidHandle     = "invalid_handle"   // WASM boundary only: missing or unknown document handle
	Internal          = "internal"         // a recovered Go panic: core.ErrInternal, or the export wrapper's recover
	Unknown           = "unknown"          // any other error (fallback)
)

// table is tested in order and the first match wins: one error can match several
// sentinels (a canvas-cap error also matches core.ErrRenderBudgetExceeded), so
// the more specific entry comes first.
var table = []struct {
	code     string
	sentinel []error
}{
	{CanvasTooLarge, []error{render.ErrCanvasTooLarge}},
	{BudgetExceeded, []error{core.ErrRenderBudgetExceeded}},
	{Timeout, []error{context.DeadlineExceeded}},
	{Canceled, []error{context.Canceled}},
	{InvalidPDF, []error{core.ErrInvalidPDF}},
	{Encrypted, []error{core.ErrEncryptedDocument}},
	{PageNotFound, []error{core.ErrPageNotFound}},
	{UnsupportedFormat, []error{core.ErrUnsupportedFormat, render.ErrUnsupportedFormat}},
	{InvalidAnnotation, []error{core.ErrInvalidAnnotation, core.ErrAnnotationNotFound}},
	{InvalidArgument, []error{core.ErrInvalidArgument}},
	// A panic core recovered at its entry point (an *InternalError). The
	// export wrapper's own recover sets Internal directly.
	{Internal, []error{core.ErrInternal}},
}

// Of returns the envelope code for err: "" for nil, the first matching table
// entry, or Unknown. It never returns InvalidHandle, which only the WASM
// boundary's handle check sets. An unmapped error is Unknown, not Internal, so
// it stays distinguishable from a recovered panic.
func Of(err error) string {
	if err == nil {
		return ""
	}
	for _, e := range table {
		for _, s := range e.sentinel {
			if errors.Is(err, s) {
				return e.code
			}
		}
	}
	return Unknown
}

// PanicDetail describes a recovered panic value for an error message without
// echoing data that may come from the document: a Go runtime error's own text
// (index out of range, nil dereference, …), otherwise only the value's type.
// It applies the same policy as core.InternalError.Error.
func PanicDetail(r any) string {
	if re, ok := r.(runtime.Error); ok {
		return re.Error()
	}
	return fmt.Sprintf("panic value of type %T", r)
}

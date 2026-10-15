// Package core is the main FluxDocs engine: parsing, rendering, annotating, and
// processing PDF documents. This package is pure Go and MIT-licensed —
// CGO-dependent features (OCR, high-fidelity rendering) live behind their own
// build tags.
package core

import (
	"errors"
	"fmt"
	"runtime"
)

// Sentinel errors for common cases. Callers test them with errors.Is, e.g.:
//
//	img, err := doc.RenderPage(0, opts)
//	if errors.Is(err, core.ErrPageNotFound) {
//	    // ...
//	}
//
// Convention: every message is prefixed with "fluxdocs:" so it stays
// recognizable in logs after being wrapped (fmt.Errorf("...: %w", err)).
var (
	// ErrInvalidPDF — the file is not a valid PDF, or its structure is damaged
	// beyond recovery even after a brute-force scan fallback.
	ErrInvalidPDF = errors.New("fluxdocs: invalid PDF structure")

	// ErrEncryptedDocument — the document is encrypted and no valid password or
	// decryption key has been supplied.
	ErrEncryptedDocument = errors.New("fluxdocs: document is encrypted")

	// ErrPageNotFound — the page index is outside [0, PageCount).
	ErrPageNotFound = errors.New("fluxdocs: page not found")

	// ErrInvalidPageOrder — a page-organization argument is malformed: a Reorder
	// permutation of the wrong length or with a duplicate/missing index, or a
	// Rotate angle that is not a multiple of 90 degrees. An out-of-range page
	// index stays ErrPageNotFound.
	ErrInvalidPageOrder = errors.New("fluxdocs: invalid page order")

	// --- Beyond §10.2: cases the public API in §7 needs to distinguish ---

	// ErrAnnotationNotFound — no annotation matches the given AnnotationID.
	ErrAnnotationNotFound = errors.New("fluxdocs: annotation not found")

	// ErrInvalidAnnotation — an annotation failed validation: an unsupported
	// type for AddAnnotation (only highlight/note/draw/shape are editable), a
	// rect with negative or non-finite dimensions, an invalid patch, or an
	// imported layer that exceeds the size cap.
	ErrInvalidAnnotation = errors.New("fluxdocs: invalid annotation")

	// ErrFormFieldNotFound — no form field matches the given name.
	ErrFormFieldNotFound = errors.New("fluxdocs: form field not found")

	// ErrInvalidArgument — an operation was called with a malformed argument
	// (e.g. an empty watermark text). Distinct from the structural ErrInvalidPDF.
	ErrInvalidArgument = errors.New("fluxdocs: invalid argument")

	// ErrUnsupportedFormat — RenderOptions.Format is not in the supported set
	// ("png" | "jpeg" | "svg").
	ErrUnsupportedFormat = errors.New("fluxdocs: unsupported render format")

	// ErrReadOnly — an edit operation was rejected because the document/viewer
	// is read-only.
	ErrReadOnly = errors.New("fluxdocs: document is read-only")

	// ErrFeatureRequiresPro — a Pro feature (forms, signature, redaction, OCR,
	// compare, watermark) was called but the license/build does not allow it.
	ErrFeatureRequiresPro = errors.New("fluxdocs: feature requires a Pro license")

	// ErrFeatureRequiresCloud — a Cloud feature (AI extraction, document Q&A)
	// was called without a configured Cloud backend.
	ErrFeatureRequiresCloud = errors.New("fluxdocs: feature requires the Cloud tier")

	// ErrInternal — FluxDocs recovered a recoverable panic while processing the
	// document. It signals a FluxDocs bug, not a caller error: the input should
	// have been rejected with a specific error such as ErrInvalidPDF. Only that
	// one call fails; the process stays up. Fatal runtime errors (stack
	// overflow, out of memory, concurrent map writes) cannot be recovered and
	// still terminate the process. The concrete error is an
	// *InternalError naming the operation (errors.As). Every entry point that
	// parses, renders, extracts from, or rebuilds a document reports panics this
	// way: OpenBytes/OpenDocument, RenderPage, RenderAllPages, the extraction,
	// form, redaction, flatten, watermark, PDF/A, signing, and Merge/Split/Save
	// operations.
	ErrInternal = errors.New("fluxdocs: internal error")
)

// InternalError is returned by a public entry point that recovered a
// recoverable panic from the parser, renderer, or another layer working on the
// document (fatal runtime errors are not recoverable), so a hostile
// file fails one call instead of crashing the process (SECURITY.md §2). It
// matches ErrInternal with errors.Is.
//
// Error() names the operation and, when it is page-scoped, the page index, and
// never includes document bytes: the panic value is shown only when it is a Go
// runtime error (text the runtime generates — index, slice bounds, nil
// dereference, makeslice); any other panic value is reported by its type alone.
// Value and Stack are diagnostics for the caller to log deliberately; Value may
// hold arbitrary data and Stack holds raw argument words. Only Error() is
// filtered: formatting the struct with %#v (or %+v) still shows Value and
// Stack.
type InternalError struct {
	Op    string // the public operation, e.g. "RenderPage"
	Page  int    // 0-based page index, or -1 when the operation is not page-scoped
	Value any    // the recovered panic value
	Stack []byte // the panicking goroutine's stack (runtime/debug.Stack)
}

// Error returns "fluxdocs: internal error in <Op>[ (page N)]: <runtime error or
// panic value type>".
func (e *InternalError) Error() string {
	msg := "fluxdocs: internal error in " + e.Op
	if e.Page >= 0 {
		msg += fmt.Sprintf(" (page %d)", e.Page)
	}
	if re, ok := e.Value.(runtime.Error); ok {
		return msg + ": " + re.Error()
	}
	return msg + fmt.Sprintf(": panic value of type %T", e.Value)
}

// Unwrap returns ErrInternal, so errors.Is(err, ErrInternal) matches.
func (e *InternalError) Unwrap() error { return ErrInternal }

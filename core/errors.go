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

	// ErrRenderBudgetExceeded — a page render hit a resource limit: raster
	// work, interpreter steps, image samples, content bytes, decoded bytes,
	// path size, or the canvas-size cap (RenderOptions.MaxPaintPixels / MaxOperators; SECURITY.md
	// §2). It is a deterministic property of the document and options, so
	// retrying does not help: raise the limit or reject the document. A context
	// deadline is NOT this error; it stays context.DeadlineExceeded.
	ErrRenderBudgetExceeded = errors.New("fluxdocs: render budget exceeded")

	// ErrRedactionUnsupported — RedactText, AutoRedactPII or RedactAndFlatten
	// refused a page it cannot redact safely: content other readers could
	// read differently (an ambiguous inline image, operands a viewer could
	// pair differently, a token split across content streams), text shown
	// through a route redaction does not rewrite yet (a form XObject, tiling
	// pattern, soft-mask group or Type 3 glyph procedure), or a redaction
	// budget exceeded. Redaction fails closed rather than miss text a viewer
	// shows; rendering and text extraction stay lenient on the same content.
	// The concrete error is a *RedactionError naming the page and the reason
	// (errors.As). It is a property of the document: retrying does not help.
	ErrRedactionUnsupported = errors.New("fluxdocs: content cannot be redacted safely")

	// ErrRedactionVerification — the redacted output failed the mandatory
	// self-check: it would still contain content the redaction removed. No
	// document is returned. It signals a FluxDocs bug and matches ErrInternal
	// too (errors.Is), so "report a bug" handling applies. Not returned yet:
	// the verification lands with step 1.12 of
	// docs/design/redaction-hardening.md.
	ErrRedactionVerification error = &childError{msg: "fluxdocs: redaction verification failed", parent: ErrInternal}

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
	// way: OpenBytes/OpenDocument, RenderPage, RenderAllPages (and their Context
	// variants), the extraction,
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

// childError is a sentinel error that also matches a parent sentinel.
type childError struct {
	msg    string
	parent error
}

func (e *childError) Error() string { return e.msg }

// Unwrap returns the parent sentinel.
func (e *childError) Unwrap() error { return e.parent }

// RedactionReason is why redaction refused a page (*RedactionError).
type RedactionReason string

// The redaction refusal reasons.
const (
	// RedactionContentAmbiguous: the page content could be read differently
	// by other readers — operands a viewer could pair with another operator,
	// an operator out of place in a text object, a token or comment split
	// across the page's content streams — so text could escape redaction.
	RedactionContentAmbiguous RedactionReason = "content_ambiguous"
	// RedactionInlineImage: an inline image whose extent or decoding some
	// reader could disagree on (a reader that misreads it parses its data as
	// content), or one inside a text object.
	RedactionInlineImage RedactionReason = "inline_image"
	// RedactionFormText: text shown through a form XObject.
	RedactionFormText RedactionReason = "form_text"
	// RedactionPatternText: text shown through a tiling pattern.
	RedactionPatternText RedactionReason = "pattern_text"
	// RedactionSoftMaskText: text shown through a soft-mask group.
	RedactionSoftMaskText RedactionReason = "softmask_text"
	// RedactionType3Text: text shown by a Type 3 glyph procedure.
	RedactionType3Text RedactionReason = "type3_text"
	// RedactionBudgetExceeded: the page exceeds a redaction budget
	// (operators, decoded bytes, nesting, invocations, font candidates).
	RedactionBudgetExceeded RedactionReason = "budget_exceeded"
	// RedactionUnsupportedXObject: an XObject of a subtype redaction does not
	// handle (e.g. PostScript).
	RedactionUnsupportedXObject RedactionReason = "unsupported_xobject"
)

// RedactionError is returned (wrapped) by RedactText, AutoRedactPII and
// RedactAndFlatten when a page cannot be redacted safely. It matches
// ErrRedactionUnsupported with errors.Is, and also its cause Err when there
// is one. It never matches ErrInvalidPDF: every viewer may open the file.
//
// Error() names the page, the reason and the detail, and never includes
// document text.
type RedactionError struct {
	Page   int             // 0-based page index, or -1 when the refusal concerns the document as a whole
	Reason RedactionReason // why the page was refused
	Detail string          // where: the route, object number or content offset; never document text
	Err    error           // the cause, or nil (e.g. the strict scanner's loss)
}

// Error returns "fluxdocs: content cannot be redacted safely: page N
// (<reason>): <detail>", or "… safely: document (<reason>): <detail>" when
// Page is -1.
func (e *RedactionError) Error() string {
	where := fmt.Sprintf("page %d", e.Page)
	if e.Page < 0 {
		where = "document"
	}
	return fmt.Sprintf("%s: %s (%s): %s", ErrRedactionUnsupported.Error(), where, e.Reason, e.Detail)
}

// Unwrap returns ErrRedactionUnsupported and the cause.
func (e *RedactionError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrRedactionUnsupported}
	}
	return []error{ErrRedactionUnsupported, e.Err}
}

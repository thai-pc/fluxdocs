// Package core is the main FluxDocs engine: parsing, rendering, annotating, and
// processing PDF documents. This package is pure Go and MIT-licensed —
// CGO-dependent features (OCR, high-fidelity rendering) live behind their own
// build tags.
package core

import "errors"

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

	// --- Beyond §10.2: cases the public API in §7 needs to distinguish ---

	// ErrAnnotationNotFound — no annotation matches the given AnnotationID.
	ErrAnnotationNotFound = errors.New("fluxdocs: annotation not found")

	// ErrFormFieldNotFound — no form field matches the given name.
	ErrFormFieldNotFound = errors.New("fluxdocs: form field not found")

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
)

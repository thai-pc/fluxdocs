package core

import "github.com/thai-pc/fluxdocs/core/docops/pii"

// pii.go implements pattern-based auto-redaction of personally identifiable
// information (Cloud-tier feature, but pure Go — no network, no AI). It layers
// on the existing redaction machinery: for each requested PII kind it marks the
// matching regions on every page via RedactText, leaving the permanent removal
// to RedactAndFlatten. Because redaction's safe failure is to remove too much
// (.claude/SECURITY.md §1), matching is by regex over the content stream — a
// digit run that merely looks like a card number is redacted rather than risked.
// For precise, Luhn-validated text-level detection (e.g. reporting), use
// core/docops/pii.Detect directly.

// PIIKind re-exports pii.Kind so callers need not import the sub-package for the
// common case.
type PIIKind = pii.Kind

// The PII kinds, re-exported for convenience.
const (
	PIIEmail      = pii.Email
	PIISSN        = pii.SSN
	PIIPhone      = pii.Phone
	PIICreditCard = pii.CreditCard
)

// AutoRedactPII marks every occurrence of the given PII kinds (all kinds when
// none are named) for redaction across all pages, and returns how many regions
// were marked per kind. Nothing is removed until RedactAndFlatten. The document
// text never leaves the process.
//
// Returns ErrInvalidPDF if the document was not opened from bytes.
func (d *Document) AutoRedactPII(kinds ...PIIKind) (map[PIIKind]int, error) {
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}
	if len(kinds) == 0 {
		kinds = pii.All
	}
	counts := make(map[PIIKind]int, len(kinds))
	for _, k := range kinds {
		pattern := pii.Pattern(k)
		if pattern == "" {
			continue
		}
		for p := range d.Pages {
			rects, err := d.RedactText(p, pattern)
			if err != nil {
				return nil, err
			}
			counts[k] += len(rects)
		}
	}
	return counts, nil
}

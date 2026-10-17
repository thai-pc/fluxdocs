// Package redact is deprecated: the redaction engine moved to
// core/internal/redact, behind core's RedactText, RedactArea and
// RedactAndFlatten. It remains for one release only so that code testing
// errors.Is(err, redact.ErrFormXObjectText) keeps working; test for
// core.ErrRedactionUnsupported (and read (*core.RedactionError).Reason)
// instead.
package redact

import "github.com/thai-pc/fluxdocs/core/internal/redact"

// ErrFormXObjectText reports a page whose text is (or may be) drawn through a
// Form XObject, which redaction does not rewrite yet. A *core.RedactionError
// with reason form_text or unsupported_xobject matches it with errors.Is.
//
// Deprecated: test for core.ErrRedactionUnsupported and read
// (*core.RedactionError).Reason. This variable is removed after one release.
var ErrFormXObjectText = redact.ErrFormXObjectText

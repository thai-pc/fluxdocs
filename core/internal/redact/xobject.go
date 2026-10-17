package redact

import "errors"

// ErrFormXObjectText reports a page whose text is (or may be) drawn through a
// Form XObject. Redaction rewrites only the page's own content stream, so
// text inside a form would survive redaction; until forms are rewritten too,
// such pages are refused (fail closed, SECURITY.md §1). A *Refusal with
// reason form_text or unsupported_xobject wraps it. core/docops/redact
// re-exports it for one release (decision P1).
var ErrFormXObjectText = errors.New("redaction of form XObjects is not supported yet")

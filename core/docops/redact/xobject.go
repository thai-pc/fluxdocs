package redact

import (
	"errors"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// ErrFormXObjectText reports a page whose text is (or may be) drawn through a
// Form XObject. Redaction rewrites only the page's own content stream, so
// text inside a form would survive redaction; until forms are rewritten too,
// such pages are refused (fail closed, SECURITY.md §1). A *RefusalError with
// reason form_text or unsupported_xobject matches it.
var ErrFormXObjectText = errors.New("redaction of form XObjects is not supported yet")

// CheckFormXObjects reports whether content, painted with the given
// resources, paints any other content stream that shows text: a Form XObject
// (Do), a soft-mask group (gs), a tiling pattern (scn/SCN, or any tiling
// pattern in resources) or a Type 3 glyph procedure (Tf, gs /Font) — directly
// or nested, every name looked up in the unit's own resources and then the
// enclosing ones (see walk.go). Text shown by content itself is not checked.
//
// Returns nil if nothing it paints shows text; a *RefusalError (form_text,
// pattern_text, softmask_text, type3_text, unsupported_xobject or
// budget_exceeded) otherwise; or a wrapped parse error if content, a child's
// content, or a resource cannot be read.
//
// Legacy entry point with no compatibility guarantee; step 1.8 unexports or
// deletes it.
func CheckFormXObjects(r *parse.Resolver, resources parse.Dict, content []byte) error {
	_, err := AnalyzePage(r, parse.PageInfo{Resources: resources}, parse.ContentSegments{Data: content})
	return err
}

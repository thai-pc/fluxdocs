package redact

import (
	"errors"
	"fmt"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// ErrFormXObjectText reports a page whose text is (or may be) drawn through a
// Form XObject. RedactContent rewrites only the page's own content stream, so
// text inside a form would survive redaction; until forms are rewritten too,
// such pages are refused (fail closed, SECURITY.md §1).
var ErrFormXObjectText = errors.New("redaction of form XObjects is not supported yet")

// Form XObject walk limits, matching the renderer's (core/render/form.go):
// nesting depth and total form visits per page. A page past either limit
// cannot be fully checked, so it is refused rather than skipped.
const (
	maxFormDepth  = 12
	maxFormBudget = 4096
)

// CheckFormXObjects reports whether content, painted with the page's
// (inherited) resources, draws any Form XObject that shows text. It follows
// every Do — recursively through nested forms, which use their own /Resources
// or inherit the caller's (ISO 32000-1 §8.10.1) — and inspects each form's
// content with the strict scanner. Image XObjects are ignored; a self-reference
// is not followed twice on one path.
//
// Returns nil if no painted form shows text (Tj, TJ, ' or "); an error wrapping
// ErrFormXObjectText naming the form if one does, if a painted XObject has an
// unsupported subtype (e.g. PostScript), or if forms nest past maxFormDepth or
// exceed maxFormBudget visits; or a wrapped parse error if content, a form's
// content, or a resource cannot be read.
func CheckFormXObjects(r *parse.Resolver, resources parse.Dict, content []byte) error {
	spans, err := parse.ScanContentSpansStrict(content)
	if err != nil {
		return err
	}
	w := formWalker{r: r, budget: maxFormBudget, path: map[parse.Reference]bool{}}
	return w.walk(resources, spans, 0)
}

// formWalker carries the state of one CheckFormXObjects call.
type formWalker struct {
	r      *parse.Resolver
	budget int                      // form visits left (maxFormBudget)
	path   map[parse.Reference]bool // forms on the current Do chain (cycle guard)
}

// walk checks every form painted by spans under resources; depth is the
// number of enclosing forms.
func (w *formWalker) walk(resources parse.Dict, spans []parse.ContentOpSpan, depth int) error {
	var xobjects parse.Dict
	loaded := false
	for _, s := range spans {
		if s.Op != "Do" || len(s.Operands) != 1 {
			continue
		}
		name, ok := s.Operands[0].(parse.Name)
		if !ok {
			continue
		}
		if !loaded {
			d, _, err := w.r.ResolveDict(resources["XObject"])
			if err != nil {
				return err
			}
			xobjects, loaded = d, true
		}
		raw, ok := xobjects[name]
		if !ok {
			continue // nothing to paint
		}
		obj, err := w.r.Resolve(raw)
		if err != nil {
			return err
		}
		st, ok := obj.(*parse.Stream)
		if !ok {
			continue
		}
		switch kind := xobjectKind(st); kind {
		case "image":
			continue
		case "unsupported":
			return fmt.Errorf("draws XObject %s of an unsupported subtype: %w", name, ErrFormXObjectText)
		}

		ref, hasRef := raw.(parse.Reference)
		if hasRef && w.path[ref] {
			continue // already being checked further up this Do chain
		}
		if depth >= maxFormDepth || w.budget <= 0 {
			return fmt.Errorf("draws form XObject %s nested too deeply or too often to check: %w", name, ErrFormXObjectText)
		}
		w.budget--

		data, err := parse.DecodeStream(st)
		if err != nil {
			return fmt.Errorf("form XObject %s: %w", name, err)
		}
		formSpans, err := parse.ScanContentSpansStrict(data)
		if err != nil {
			return fmt.Errorf("form XObject %s: %w", name, err)
		}
		for _, fs := range formSpans {
			switch fs.Op {
			case "Tj", "TJ", "'", "\"":
				return fmt.Errorf("draws text through form XObject %s; %w", name, ErrFormXObjectText)
			}
		}

		formResources := resources
		d, ok, err := w.r.ResolveDict(st.Dict["Resources"])
		if err != nil {
			return err
		}
		if ok {
			formResources = d
		}
		if hasRef {
			w.path[ref] = true
		}
		err = w.walk(formResources, formSpans, depth+1)
		if hasRef {
			delete(w.path, ref)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// xobjectKind classifies an XObject stream as the renderer does
// (core/render/image.go): "form", "image", or "unsupported" for any other
// declared subtype. An untyped stream with /Width and /Height is an image;
// otherwise it is checked as a form.
func xobjectKind(st *parse.Stream) string {
	subtype, ok := st.Dict.GetName("Subtype")
	switch {
	case ok && subtype == "Form":
		return "form"
	case ok && subtype == "Image":
		return "image"
	case ok:
		return "unsupported"
	}
	_, hasW := st.Dict["Width"]
	_, hasH := st.Dict["Height"]
	if hasW && hasH {
		return "image"
	}
	return "form"
}

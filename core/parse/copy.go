package parse

// copy.go deep-copies a page and its transitive object closure from a source
// Resolver into a Writer's fresh object-number space, remapping every reference,
// then builds a minimal catalog/page tree. See
// docs/design/pdf-writer-merge-split.md §4–§5.
//
// The source is untrusted input, so every walk is bounded (object count, output
// size, recursion depth) and any failure returns a wrapped errLex — never a
// panic (SECURITY.md §2–3).

import (
	"fmt"
	"math"
)

// Copy guards (mirror the page-tree caps in pagetree.go).
const (
	maxCopyObjects = 1 << 20 // hard cap on indirect objects copied per build
	maxCopyDepth   = 256     // bound recursion into nested arrays/dicts
	maxOutputBytes = 4 << 30 // bound total stream bytes queued for emit (4 GiB)
)

// PageSource pairs a source Resolver with one resolved page from it. Merge and
// Split differ only in the ordered list of PageSources they hand to
// BuildDocument.
type PageSource struct {
	Resolver *Resolver
	Page     PageInfo

	// AppendContent, when non-empty, is appended to the copied page as an extra
	// content stream, concatenated after the page's own content (§7.8.2). It lets
	// a caller overlay marks in PDF user space — e.g. flattened annotations or a
	// watermark — without decoding the original content. It should be a
	// self-contained, q/Q-balanced content fragment.
	AppendContent []byte

	// ExtGStates are named graphics-state parameter dictionaries merged into the
	// copied page's /Resources /ExtGState (e.g. {"FdGS0": {"Type":"ExtGState",
	// "ca":Real(0.4),"CA":Real(0.4)}} for constant alpha). Each is injected into
	// a page-private clone of the resources so shared/deduplicated resource
	// objects are never mutated. Names should carry a distinctive prefix to avoid
	// colliding with the page's own entries.
	ExtGStates map[Name]Dict

	// ReplaceContent, when non-nil, REPLACES the copied page's /Contents entirely
	// with a single content stream of these bytes (the page's original content
	// objects are not referenced). Redaction uses this to emit a content stream
	// with the sensitive text-showing operators removed. An empty (non-nil) slice
	// yields an empty page.
	ReplaceContent []byte

	// DropAnnots, when true, removes the copied page's /Annots. Redaction sets it
	// so annotation objects that may carry the redacted text are not copied.
	DropAnnots bool

	// Fonts are named font dictionaries merged into the copied page's /Resources
	// /Font (e.g. {"WMHelv": {"Type":"Font","Subtype":"Type1","BaseFont":
	// "Helvetica"}}), so an overlay (e.g. a watermark) can reference a font the
	// page does not itself declare. Injected into the same page-private resources
	// clone as ExtGStates; use a distinctive name prefix.
	Fonts map[Name]Dict
}

// Copier copies indirect objects from one source Resolver into a Writer,
// remapping source object numbers to freshly allocated writer numbers. The
// visited map both deduplicates shared objects (a font used by two pages is
// copied once) and breaks reference cycles.
//
// A Copier is bound to a single source Resolver; use one Copier per source. All
// copiers in a build share the same Writer, so the object and output-size caps
// are enforced globally through it.
type Copier struct {
	w       *Writer
	src     *Resolver
	visited map[int]Reference // source object number -> new Reference in w
}

// NewCopier returns a Copier that copies from src into w.
func NewCopier(w *Writer, src *Resolver) *Copier {
	return &Copier{w: w, src: src, visited: make(map[int]Reference)}
}

// CopyPage materializes a self-contained copy of one page into the Writer and
// returns its Reference, with /Parent left unset (the caller patches it when
// the /Pages node exists). Inherited /MediaBox, /Resources, and /Rotate are made
// explicit; back-pointers (/Parent, tagged-PDF /StructParent(s), article /B)
// are dropped so the copy drags in no foreign structure.
//
// Returns a wrapped errLex if the closure violates a copy guard or a source
// object fails to resolve.
func (c *Copier) CopyPage(pi PageInfo) (Reference, error) {
	// Clone the page's own dictionary minus back-pointers, then copy its closure.
	stripped := make(Dict, len(pi.Dict))
	for k, v := range pi.Dict {
		switch k {
		case "Parent", "StructParents", "StructParent", "B":
			continue
		}
		stripped[k] = v
	}

	copied, err := c.copyValue(stripped, 0)
	if err != nil {
		return Reference{}, err
	}
	page, ok := copied.(Dict)
	if !ok {
		page = Dict{} // defensive: a page must serialize as a dictionary
	}
	page["Type"] = Name("Page")

	// Materialize inherited attributes so the page stands alone once /Parent is
	// gone. Present-in-own-dict values were already copied above; only fill gaps.
	if _, has := page["MediaBox"]; !has {
		page["MediaBox"] = rectArray(pi.MediaBox)
	}
	if _, has := page["Resources"]; !has {
		if pi.Resources != nil {
			res, err := c.copyValue(pi.Resources, 0)
			if err != nil {
				return Reference{}, err
			}
			page["Resources"] = res
		} else {
			page["Resources"] = Dict{}
		}
	}
	if _, has := page["Rotate"]; !has && pi.Rotate != 0 {
		page["Rotate"] = Integer(int64(pi.Rotate))
	}

	return c.w.Add(page), nil
}

// copyValue returns a direct copy of obj with every inner reference remapped
// into the Writer's number space. Depth is bounded to defeat deeply nested
// hostile input.
func (c *Copier) copyValue(obj Object, depth int) (Object, error) {
	if depth > maxCopyDepth {
		return nil, fmt.Errorf("%w: copy recursion too deep (suspected loop)", errLex)
	}

	switch v := obj.(type) {
	case nil:
		return Null{}, nil
	case Null, Boolean, Integer, Real, String, Name:
		return v, nil // scalars hold no references
	case Array:
		out := make(Array, len(v))
		for i, e := range v {
			ce, err := c.copyValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = ce
		}
		return out, nil
	case Dict:
		out := make(Dict, len(v))
		for k, val := range v {
			cv, err := c.copyValue(val, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = cv
		}
		return out, nil
	case *Stream:
		d, err := c.copyValue(v.Dict, depth+1)
		if err != nil {
			return nil, err
		}
		dict, _ := d.(Dict)
		c.w.outBytes += int64(len(v.Raw))
		if c.w.outBytes > maxOutputBytes {
			return nil, fmt.Errorf("%w: copied output exceeds size limit", errLex)
		}
		raw := make([]byte, len(v.Raw)) // fresh slice: never alias source bytes
		copy(raw, v.Raw)
		return &Stream{Dict: dict, Raw: raw}, nil
	case Reference:
		return c.copyRef(v, depth)
	default:
		return nil, fmt.Errorf("%w: unknown object type in copy", errLex)
	}
}

// copyRef copies one indirect object (memoized). It RESERVES the new number
// before recursing so a cycle that points back at this object finds the reserved
// reference instead of recursing forever.
func (c *Copier) copyRef(ref Reference, depth int) (Reference, error) {
	if nr, ok := c.visited[ref.Number]; ok {
		return nr, nil // already copied (dedup) or in-progress (cycle break)
	}
	if c.w.count() >= maxCopyObjects {
		return Reference{}, fmt.Errorf("%w: copied object count exceeds limit", errLex)
	}

	newRef := c.w.Reserve()
	c.visited[ref.Number] = newRef

	resolved, err := c.src.Resolve(ref)
	if err != nil {
		return Reference{}, err // do not emit a half object
	}
	body, err := c.copyValue(resolved, depth+1)
	if err != nil {
		return Reference{}, err
	}
	c.w.Fill(newRef, body)
	return newRef, nil
}

// BuildDocument copies the ordered pages into a fresh PDF and returns the
// serialized bytes. It runs the full pipeline: copy each page's closure (one
// Copier per distinct source Resolver, so shared objects within a source are
// deduplicated), synthesize a /Catalog -> /Pages -> pages tree, attach an
// optional /Info, and serialize.
//
// info, if non-empty, becomes the trailer /Info (its values must already be
// direct PDF objects). Returns a wrapped errLex on a guard violation, an
// unresolved object, or a nil source resolver.
func BuildDocument(sources []PageSource, info Dict) ([]byte, error) {
	w := NewWriter()

	// Reserve the /Pages node first: pages need it as their /Parent.
	pagesRef := w.Reserve()

	// One Copier per source Resolver, so pages from the same document share
	// (dedup) their common objects; pages from different documents never conflate
	// object numbers.
	copiers := make(map[*Resolver]*Copier)

	kids := make(Array, 0, len(sources))
	for _, s := range sources {
		if s.Resolver == nil {
			return nil, fmt.Errorf("%w: page source has no resolver", errLex)
		}
		cp := copiers[s.Resolver]
		if cp == nil {
			cp = NewCopier(w, s.Resolver)
			copiers[s.Resolver] = cp
		}
		pageRef, err := cp.CopyPage(s.Page)
		if err != nil {
			return nil, err
		}
		// Point the copied page back at the new /Pages node, and apply any
		// caller-supplied content changes (annotation overlay or redaction).
		if pd, ok := w.objects[pageRef.Number-1].(Dict); ok {
			pd["Parent"] = pagesRef
			if s.DropAnnots {
				delete(pd, "Annots")
			}
			if s.ReplaceContent != nil {
				pd["Contents"] = w.Add(&Stream{Dict: Dict{}, Raw: s.ReplaceContent})
			}
			if len(s.AppendContent) > 0 {
				applyOverlay(w, pd, s.AppendContent, s.ExtGStates, s.Fonts)
			}
		}
		kids = append(kids, pageRef)
	}

	w.Fill(pagesRef, Dict{
		"Type":  Name("Pages"),
		"Kids":  kids,
		"Count": Integer(int64(len(kids))),
	})

	catRef := w.Add(Dict{
		"Type":  Name("Catalog"),
		"Pages": pagesRef,
	})
	w.SetRoot(catRef)

	if len(info) > 0 {
		w.SetInfo(w.Add(info))
	}

	return w.Bytes()
}

// applyOverlay appends an extra content stream to a copied page and injects the
// caller's named ExtGState and Font dicts into a page-private clone of its
// /Resources. It mutates only freshly created objects (the new content stream)
// and clones the resource dictionaries before writing, so shared/deduplicated
// resource objects from the source are never corrupted.
func applyOverlay(w *Writer, page Dict, content []byte, exts, fonts map[Name]Dict) {
	// 1. Append the overlay as a new indirect content stream. A page's /Contents
	// may be a single stream reference, an array of them, an inline stream, or
	// absent; normalize to an array with the overlay last so it draws on top. A
	// leading newline separates it from the previous stream's final token so the
	// two cannot merge even in a reader that does not insert the §7.8.2
	// inter-stream whitespace.
	raw := make([]byte, 0, len(content)+1)
	raw = append(raw, '\n')
	raw = append(raw, content...)
	ov := w.Add(&Stream{Dict: Dict{}, Raw: raw})
	switch c := page["Contents"].(type) {
	case nil:
		page["Contents"] = Array{ov}
	case Array:
		na := make(Array, len(c), len(c)+1)
		copy(na, c)
		for i, e := range na {
			if st, ok := e.(*Stream); ok {
				na[i] = w.Add(st) // a stream cannot sit directly in an array
			}
		}
		page["Contents"] = append(na, ov)
	case *Stream:
		page["Contents"] = Array{w.Add(c), ov}
	default: // a single Reference (or other) content object
		page["Contents"] = Array{c, ov}
	}

	// 2. Inject ExtGState and Font entries into a page-private resources clone.
	if len(exts) == 0 && len(fonts) == 0 {
		return
	}
	res := cloneDictValue(w, page["Resources"])
	if len(exts) > 0 {
		eg := cloneDictValue(w, res["ExtGState"])
		for name, gs := range exts {
			eg[name] = gs
		}
		res["ExtGState"] = eg
	}
	if len(fonts) > 0 {
		fd := cloneDictValue(w, res["Font"])
		for name, f := range fonts {
			fd[name] = f
		}
		res["Font"] = fd
	}
	page["Resources"] = res
}

// cloneDictValue returns a fresh shallow copy of a dictionary value that may be
// a direct Dict, an indirect Reference into w, or absent. The returned map is
// always safe to mutate without affecting any shared source object.
func cloneDictValue(w *Writer, v Object) Dict {
	var src Dict
	switch d := v.(type) {
	case Dict:
		src = d
	case Reference:
		if idx := d.Number - 1; idx >= 0 && idx < len(w.objects) {
			if dd, ok := w.objects[idx].(Dict); ok {
				src = dd
			}
		}
	}
	out := make(Dict, len(src)+1)
	for k, val := range src {
		out[k] = val
	}
	return out
}

// rectArray renders a Rectangle as a 4-element PDF array [llx lly urx ury],
// emitting whole values as Integer and fractional ones as Real.
func rectArray(r Rectangle) Array {
	return Array{numObj(r.LLX), numObj(r.LLY), numObj(r.URX), numObj(r.URY)}
}

// numObj returns an Integer for a whole, finite value and a Real otherwise.
func numObj(f float64) Object {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) {
		return Integer(int64(f))
	}
	return Real(f)
}

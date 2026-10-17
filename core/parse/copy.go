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

	// ReplaceResources, when non-nil, REPLACES the copied page's /Resources:
	// the page's own /Resources entry and its inherited resources are not
	// copied (so objects reachable only through them are not written), and
	// this dictionary is copied in their place, its references resolved
	// against the source as usual. Redaction uses it for a page-private copy
	// of the resources with stripped marked-content property lists.
	ReplaceResources Dict

	// DropAnnots, when true, removes the copied page's /Annots after the copy
	// (the annotation objects are still written). Form flattening uses it;
	// redaction uses Redacted, which copies no annotation at all.
	DropAnnots bool

	// Redacted marks a page whose content redaction removed (docs/design/
	// redaction-hardening.md §5.5). Only the page-dictionary entries a viewer
	// needs to paint the page are copied: /Type, the page boxes (/MediaBox,
	// /CropBox, /BleedBox, /TrimBox, /ArtBox), /Rotate, /UserUnit, /Group,
	// /Resources (or ReplaceResources) and /Contents (or ReplaceContent).
	// Everything else — /Annots, /Thumb (a picture of the page), /Metadata,
	// /PieceInfo, /AA, /VP, /SeparationInfo, … — is neither copied nor
	// written. The page's original annotations are denied for the whole
	// build: a reference to one from any other copied object (a widget's
	// field /Kids, a popup's /Parent, …) is written as null, or dropped from
	// an array.
	Redacted bool

	// Deny lists source objects (by object number) that no copy from this
	// source may reach, for the whole build: a reference to one is written as
	// null, or dropped from an array. Redaction denies the objects only
	// removed content used (an image under a region), so a shared resource
	// dictionary cannot carry them into the output.
	Deny []int

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
//
// References to the source's page objects and page-tree nodes are never
// followed (docs/design/redaction-hardening.md §5.5, decision D11): a page
// that is part of the build is referred to by its copy, and any other page or
// page-tree node becomes null. Following them would copy the original page —
// its content, resources and annotations, and through /Parent the whole
// original page tree — into the output as an unreachable duplicate: a link
// destination or an annotation's /P would drag in a page the output omits or
// replaces (a split-off page, or one whose content redaction removed).
type Copier struct {
	w       *Writer
	src     *Resolver
	visited map[int]Reference // source object number -> new Reference in w

	pageTree map[int]bool      // source page-tree objects (nodes and leaves); nil until known
	pages    map[int]Reference // source page in this build -> its copy (zero until numbered)
	placed   map[int]bool      // source pages whose copy has been written
	deny     map[int]bool      // source objects written as null (Redacted pages' annotations)
}

// NewCopier returns a Copier that copies from src into w.
func NewCopier(w *Writer, src *Resolver) *Copier {
	return &Copier{w: w, src: src, visited: make(map[int]Reference),
		pages: map[int]Reference{}, placed: map[int]bool{}, deny: map[int]bool{}}
}

// pageTreeObjects returns the source's page-tree object numbers, reading the
// tree once per Copier.
func (c *Copier) pageTreeObjects() map[int]bool {
	if c.pageTree == nil {
		c.pageTree = c.src.pageTreeNumbers()
	}
	return c.pageTree
}

// addPage records that the source page pi is part of the build, so references
// to it resolve to its copy.
func (c *Copier) addPage(pi PageInfo) {
	if pi.num > 0 {
		if _, ok := c.pages[pi.num]; !ok {
			c.pages[pi.num] = Reference{}
		}
	}
}

// denyAnnots records the annotations of a page, and their popups, as objects
// no copy may reach (a Redacted page's, or the annotations of a page outside
// the build: see denyForeignAnnots).
func (c *Copier) denyAnnots(pi PageInfo) {
	c.annotsOf(pi.Dict, func(n int) { c.deny[n] = true })
}

// annotsOf calls f with the object number of each annotation in a page's
// /Annots and of each annotation's /Popup, for annotations referenced
// indirectly or written directly in the array.
func (c *Copier) annotsOf(page Dict, f func(int)) {
	annots, err := c.src.Resolve(page["Annots"])
	if err != nil {
		return
	}
	arr, _ := annots.(Array)
	for _, a := range arr {
		if ref, ok := a.(Reference); ok {
			f(ref.Number)
		}
		if d, ok, err := c.src.ResolveDict(a); err == nil && ok {
			if p, ok := d["Popup"].(Reference); ok {
				f(p.Number)
			}
		}
	}
}

// denyForeignAnnots denies the annotations of every source page that is not
// part of the build, unless a page of the build lists them too: a reply
// (/IRT), popup or field reached from a copied annotation must not carry
// another page's annotation — its text — into a split-off document.
func (c *Copier) denyForeignAnnots() {
	own := map[int]bool{}
	for num := range c.pages {
		if d, ok, err := c.src.ResolveDict(Reference{Number: num}); err == nil && ok {
			c.annotsOf(d, func(n int) { own[n] = true })
		}
	}
	for num := range c.pageTreeObjects() {
		if _, inBuild := c.pages[num]; inBuild {
			continue
		}
		d, ok, err := c.src.ResolveDict(Reference{Number: num})
		if err != nil || !ok {
			continue
		}
		if t, _ := d.GetName("Type"); t == "Pages" {
			continue
		}
		c.annotsOf(d, func(n int) {
			if !own[n] {
				c.deny[n] = true
			}
		})
	}
}

// IsPageTreeObject reports whether obj (resolved) is a page object or a
// page-tree node: a dictionary whose /Type, resolved, is /Page or /Pages (ISO 32000-1
// §7.7.3). A copy never follows a reference to one (see Copier).
func IsPageTreeObject(r *Resolver, obj Object) bool {
	d, ok := obj.(Dict)
	if !ok {
		return false
	}
	t, err := r.Resolve(d["Type"])
	if err != nil {
		return false
	}
	n, _ := t.(Name)
	return n == "Page" || n == "Pages"
}

// pageRef is what a reference to the source page-tree object num copies as:
// the page's copy if the page is part of the build (numbered on first use),
// null otherwise.
func (c *Copier) pageRef(num int) Object {
	ref, ok := c.pages[num]
	if !ok {
		return Null{}
	}
	if ref.Number == 0 {
		ref = c.w.Reserve()
		c.pages[num] = ref
	}
	return ref
}

// redactedPageKeys are the page-dictionary entries a Redacted page keeps.
var redactedPageKeys = map[Name]bool{
	"Type": true, "MediaBox": true, "CropBox": true, "BleedBox": true, "TrimBox": true,
	"ArtBox": true, "Rotate": true, "UserUnit": true, "Group": true, "Resources": true,
	"Contents": true,
}

// CopyPage materializes a self-contained copy of one page into the Writer and
// returns its Reference, with /Parent left unset (the caller patches it when
// the /Pages node exists). Inherited /MediaBox, /Resources, and /Rotate are made
// explicit; back-pointers (/Parent, tagged-PDF /StructParent(s), article /B)
// are dropped so the copy drags in no foreign structure.
//
// References to page objects follow the Copier rule: a reference to a page
// copied by an earlier CopyPage call on this Copier, or to pi itself, points
// at that copy; a reference to any other page — including one a later
// CopyPage call copies — is written as null, since the copier cannot know it
// will be part of the output. Use BuildDocument, which registers every page
// of the build first.
//
// Returns a wrapped errLex if the closure violates a copy guard or a source
// object fails to resolve.
func (c *Copier) CopyPage(pi PageInfo) (Reference, error) {
	c.pageTreeObjects()
	c.addPage(pi)
	return c.copyPage(PageSource{Page: pi})
}

// copyPage is CopyPage for a PageSource: the page's resources replaced by
// ReplaceResources when it is non-nil, its original /Contents not copied when
// ReplaceContent is non-nil (the caller sets the new one), and only the
// allow-listed entries of a Redacted page copied.
func (c *Copier) copyPage(s PageSource) (Reference, error) {
	pi, resources := s.Page, s.ReplaceResources
	// Clone the page's own dictionary minus back-pointers, then copy its closure.
	stripped := make(Dict, len(pi.Dict))
	for k, v := range pi.Dict {
		switch k {
		case "Parent", "StructParents", "StructParent", "B":
			continue
		case "Resources":
			if resources != nil {
				continue
			}
		case "Contents":
			if s.ReplaceContent != nil {
				continue
			}
		}
		if s.Redacted && !redactedPageKeys[k] {
			continue
		}
		stripped[k] = v
	}
	if resources != nil {
		stripped["Resources"] = resources
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

	// The first copy of a page is the one references to it resolve to; a
	// reference met while copying may already have numbered it.
	if ref, ok := c.pages[pi.num]; ok && pi.num > 0 && !c.placed[pi.num] {
		c.placed[pi.num] = true
		if ref.Number != 0 {
			c.w.Fill(ref, page)
			return ref, nil
		}
		ref = c.w.Add(page)
		c.pages[pi.num] = ref
		return ref, nil
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
		out := make(Array, 0, len(v))
		for _, e := range v {
			if ref, ok := e.(Reference); ok && c.deny[ref.Number] {
				continue // a denied object is dropped from arrays (/Kids, /Annots)
			}
			ce, err := c.copyValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, ce)
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
// reference instead of recursing forever. A page-tree object is not copied
// (see Copier), and a denied object copies as null.
func (c *Copier) copyRef(ref Reference, depth int) (Object, error) {
	if c.pageTreeObjects()[ref.Number] {
		return c.pageRef(ref.Number), nil
	}
	if c.deny[ref.Number] {
		return Null{}, nil
	}
	if nr, ok := c.visited[ref.Number]; ok {
		return nr, nil // already copied (dedup) or in-progress (cycle break)
	}
	resolved, err := c.src.Resolve(ref)
	if err != nil {
		return nil, err // do not emit a half object
	}
	// A page or page-tree node outside the catalog's tree (an orphan page a
	// link points at) is no less a page: never copied (D11).
	if IsPageTreeObject(c.src, resolved) {
		return c.pageRef(ref.Number), nil
	}
	if c.w.count() >= maxCopyObjects {
		return nil, fmt.Errorf("%w: copied object count exceeds limit", errLex)
	}

	newRef := c.w.Reserve()
	c.visited[ref.Number] = newRef

	body, err := c.copyValue(resolved, depth+1)
	if err != nil {
		return nil, err
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
	w, pagesRef, err := copyPages(sources)
	if err != nil {
		return nil, err
	}
	catRef := w.Add(Dict{"Type": Name("Catalog"), "Pages": pagesRef})
	w.SetRoot(catRef)
	if len(info) > 0 {
		w.SetInfo(w.Add(info))
	}
	return w.Bytes()
}

// PDFAParams carries the extra objects a PDF/A build adds to the catalog and
// trailer: the XMP metadata, the ICC output profile, and the file identifier.
type PDFAParams struct {
	XMP           []byte // XMP metadata stream content (uncompressed)
	ICC           []byte // ICC profile bytes for the OutputIntent
	ICCComponents int    // number of colour components in the ICC profile (3 for RGB)
	ID            []byte // trailer /ID (16 bytes)
	Condition     string // OutputConditionIdentifier, e.g. "sRGB"
}

// BuildDocumentPDFA is BuildDocument with the PDF/A-1b structural additions: an
// XMP /Metadata stream on the catalog, an sRGB /OutputIntents entry backed by an
// embedded ICC profile, and a trailer /ID.
func BuildDocumentPDFA(sources []PageSource, info Dict, p PDFAParams) ([]byte, error) {
	w, pagesRef, err := copyPages(sources)
	if err != nil {
		return nil, err
	}

	cat := Dict{"Type": Name("Catalog"), "Pages": pagesRef}
	if len(p.XMP) > 0 {
		metaRef := w.Add(&Stream{
			Dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML")},
			Raw:  p.XMP,
		})
		cat["Metadata"] = metaRef
	}
	if len(p.ICC) > 0 {
		iccRef := w.Add(&Stream{
			Dict: Dict{"N": Integer(int64(p.ICCComponents))},
			Raw:  p.ICC,
		})
		cond := p.Condition
		if cond == "" {
			cond = "sRGB"
		}
		oiRef := w.Add(Dict{
			"Type":                      Name("OutputIntent"),
			"S":                         Name("GTS_PDFA1"),
			"OutputConditionIdentifier": String{Value: []byte(cond)},
			"Info":                      String{Value: []byte(cond)},
			"DestOutputProfile":         iccRef,
		})
		cat["OutputIntents"] = Array{oiRef}
	}

	w.SetRoot(w.Add(cat))
	if len(info) > 0 {
		w.SetInfo(w.Add(info))
	}
	if len(p.ID) > 0 {
		w.SetID(p.ID)
	}
	return w.Bytes()
}

// copyPages copies the ordered pages into a fresh Writer and fills the /Pages
// node, returning the writer and the /Pages reference. Shared by BuildDocument
// and BuildDocumentPDFA.
func copyPages(sources []PageSource) (*Writer, Reference, error) {
	w := NewWriter()

	// Reserve the /Pages node first: pages need it as their /Parent.
	pagesRef := w.Reserve()

	// One Copier per source Resolver, so pages from the same document share
	// (dedup) their common objects; pages from different documents never conflate
	// object numbers.
	copiers := make(map[*Resolver]*Copier)

	kids := make(Array, 0, len(sources))
	// Before anything is copied, record every page of the build (references to
	// them resolve to their copies) and the annotations of Redacted pages
	// (denied), so the order of the pages does not matter.
	for _, s := range sources {
		if s.Resolver == nil {
			return nil, Reference{}, fmt.Errorf("%w: page source has no resolver", errLex)
		}
		cp := copiers[s.Resolver]
		if cp == nil {
			cp = NewCopier(w, s.Resolver)
			copiers[s.Resolver] = cp
		}
		cp.addPage(s.Page)
		if s.Redacted {
			cp.denyAnnots(s.Page)
		}
		for _, n := range s.Deny {
			cp.deny[n] = true
		}
	}
	for _, cp := range copiers {
		cp.denyForeignAnnots()
	}
	// replaced records the original page-content streams that ReplaceContent
	// (redaction) repointed away from. They are not copied for their page, but
	// another route may still reach one (an annotation appearance, a resource);
	// their bytes carry the removed text, so after all pages are copied any
	// such copy no page still uses as /Contents is blanked — otherwise the
	// secret would linger as an orphaned, unreferenced object in the output
	// (.claude/SECURITY.md §1: redaction must remove content, and a paint-over
	// or a dangling copy both fail that).
	type sourceRef struct {
		cp  *Copier
		num int
	}
	var replaced []sourceRef
	for _, s := range sources {
		cp := copiers[s.Resolver]
		pageRef, err := cp.copyPage(s)
		if err != nil {
			return nil, Reference{}, err
		}
		// Point the copied page back at the new /Pages node, and apply any
		// caller-supplied content changes (annotation overlay or redaction).
		if pd, ok := w.objects[pageRef.Number-1].(Dict); ok {
			pd["Parent"] = pagesRef
			if s.DropAnnots {
				delete(pd, "Annots")
			}
			if s.ReplaceContent != nil {
				for _, n := range sourceContentNums(s.Resolver, s.Page.Dict["Contents"]) {
					replaced = append(replaced, sourceRef{cp, n})
				}
				pd["Contents"] = w.Add(&Stream{Dict: Dict{}, Raw: s.ReplaceContent})
			}
			if len(s.AppendContent) > 0 {
				applyOverlay(w, pd, s.AppendContent, s.ExtGStates, s.Fonts)
			}
		}
		kids = append(kids, pageRef)
	}

	// Map the replaced streams to the copies other routes made of them.
	var copied []int
	for _, r := range replaced {
		if ref, ok := r.cp.visited[r.num]; ok {
			copied = append(copied, ref.Number)
		}
	}
	blankOrphanedContent(w, kids, copied)

	w.Fill(pagesRef, Dict{
		"Type":  Name("Pages"),
		"Kids":  kids,
		"Count": Integer(int64(len(kids))),
	})
	return w, pagesRef, nil
}

// contentRefNums returns the object numbers referenced by a page's /Contents,
// which is either a single Reference or an Array of References (inline streams,
// having no number, are ignored).
func contentRefNums(contents Object) []int {
	var nums []int
	switch c := contents.(type) {
	case Reference:
		nums = append(nums, c.Number)
	case Array:
		for _, el := range c {
			if ref, ok := el.(Reference); ok {
				nums = append(nums, ref.Number)
			}
		}
	}
	return nums
}

// sourceContentNums is contentRefNums for a source page's /Contents, plus the
// streams of an array it references indirectly.
func sourceContentNums(r *Resolver, contents Object) []int {
	nums := contentRefNums(contents)
	if ref, ok := contents.(Reference); ok {
		if arr, err := r.Resolve(ref); err == nil {
			if a, ok := arr.(Array); ok {
				nums = append(nums, contentRefNums(a)...)
			}
		}
	}
	return nums
}

// blankOrphanedContent replaces every stream in replaced that no kept page still
// references (via its final /Contents) with an empty stream, so a redacted
// page's original content bytes do not survive as an orphan. It only touches
// streams, and only ones gone unreferenced, so a content stream shared by an
// un-redacted page is left intact.
func blankOrphanedContent(w *Writer, kids Array, replaced []int) {
	if len(replaced) == 0 {
		return
	}
	live := make(map[int]bool)
	for _, k := range kids {
		kref, ok := k.(Reference)
		if !ok {
			continue
		}
		if pd, ok := w.objects[kref.Number-1].(Dict); ok {
			for _, n := range contentRefNums(pd["Contents"]) {
				live[n] = true
			}
		}
	}
	for _, n := range replaced {
		if live[n] || n < 1 || n > len(w.objects) {
			continue
		}
		if _, ok := w.objects[n-1].(*Stream); ok {
			w.objects[n-1] = &Stream{Dict: Dict{}, Raw: []byte{}}
		}
	}
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

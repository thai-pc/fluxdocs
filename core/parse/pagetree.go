package parse

import (
	"fmt"
	"math"
)

const (
	maxPageTreeDepth = 50      // bound a deeply nested / malicious page tree
	maxPages         = 1 << 20 // hard cap on pages emitted (DoS guard)
)

// Rectangle is a PDF rectangle [llx lly urx ury] in points.
type Rectangle struct {
	LLX, LLY, URX, URY float64
}

// Width returns the rectangle's width in points (always non-negative).
func (r Rectangle) Width() float64 { return absf(r.URX - r.LLX) }

// Height returns the rectangle's height in points (always non-negative).
func (r Rectangle) Height() float64 { return absf(r.URY - r.LLY) }

// PageInfo is a single leaf page with its inheritable attributes resolved.
// Contents is left unresolved (a Reference, an Array of References, or a Stream)
// for the render/extract layer to decode.
type PageInfo struct {
	Dict      Dict      // the page's own dictionary
	Resources Dict      // inherited /Resources (may be nil)
	MediaBox  Rectangle // inherited /MediaBox (defaults to US Letter if absent)
	Rotate    int       // inherited /Rotate, normalized to 0/90/180/270
	Contents  Object    // the page's /Contents, unresolved

	// num is the page object's number (0 when the page tree holds it
	// directly), so a build can map references to it (copy.go).
	num int
}

// ObjectNumber returns the page object's number in its document, or 0 when
// the page tree holds the page directly (or the PageInfo was not read from a
// page tree). It survives copies of the PageInfo that replace Dict (Rotate
// clones the page dictionary), so it identifies the page where the
// dictionary's identity does not.
func (p PageInfo) ObjectNumber() int { return p.num }

// inherited carries page-tree attributes that propagate from intermediate nodes
// to their descendants (ISO 32000-1 §7.7.3.4, Table 30).
type inherited struct {
	resources Dict
	mediaBox  *Rectangle // nil until some ancestor (or the page) defines it
	rotate    int
}

// defaultMediaBox is US Letter, used leniently when no ancestor defines one.
var defaultMediaBox = Rectangle{0, 0, 612, 792}

// Pages walks the document's page tree and returns the leaf pages in document
// order, with inheritable attributes applied.
//
// Returns a wrapped errLex if the catalog or /Pages root is missing, or the
// pages otherwise (an empty slice is possible for a degenerate tree).
func (r *Resolver) Pages() ([]PageInfo, error) {
	cat, err := r.Catalog()
	if err != nil {
		return nil, err
	}
	root, ok := cat["Pages"]
	if !ok {
		return nil, fmt.Errorf("%w: catalog missing /Pages", errLex)
	}

	var out []PageInfo
	visited := make(map[int]bool)
	if err := r.walkPageTree(root, inherited{}, 0, visited, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// walkPageTree recursively visits a page-tree node, accumulating inheritable
// attributes and appending leaf pages to out. A node entered via a Reference is
// recorded in visited so a cyclic /Kids cannot loop forever.
func (r *Resolver) walkPageTree(obj Object, inh inherited, depth int, visited map[int]bool, out *[]PageInfo) error {
	if depth > maxPageTreeDepth {
		return fmt.Errorf("%w: page tree too deep (suspected loop)", errLex)
	}
	if len(*out) >= maxPages {
		return fmt.Errorf("%w: page count exceeds limit", errLex)
	}

	// Cycle guard: a node reached through a reference is visited at most once.
	if ref, ok := obj.(Reference); ok {
		if visited[ref.Number] {
			return nil
		}
		visited[ref.Number] = true
	}

	dict, ok, err := r.ResolveDict(obj)
	if err != nil {
		return err
	}
	if !ok {
		return nil // not a dictionary — skip leniently
	}

	// Apply this node's own inheritable attributes over what we inherited.
	inh = r.applyInherited(inh, dict)

	// Intermediate node iff it has a non-empty /Kids and is not a /Page.
	kids, hasKids, err := r.resolveArray(dict["Kids"])
	if err != nil {
		return err
	}
	typ, _ := dict.GetName("Type")
	if typ != "Page" && hasKids && len(kids) > 0 {
		for _, kid := range kids {
			if err := r.walkPageTree(kid, inh, depth+1, visited, out); err != nil {
				return err
			}
		}
		return nil
	}

	// Leaf page.
	mb := defaultMediaBox
	if inh.mediaBox != nil {
		mb = *inh.mediaBox
	}
	num := 0
	if ref, ok := obj.(Reference); ok {
		num = ref.Number
	}
	*out = append(*out, PageInfo{
		Dict:      dict,
		Resources: inh.resources,
		MediaBox:  mb,
		Rotate:    inh.rotate,
		Contents:  dict["Contents"],
		num:       num,
	})
	return nil
}

// pageTreeNumbers returns the object numbers of every node of the document's
// page tree reached from the catalog's /Pages — intermediate nodes and leaf
// pages — with the bounds and leaf rule of Pages. A failure to read the tree
// yields what was found so far.
func (r *Resolver) pageTreeNumbers() map[int]bool {
	out := map[int]bool{}
	cat, err := r.Catalog()
	if err != nil {
		return out
	}
	var walk func(obj Object, depth int)
	walk = func(obj Object, depth int) {
		if depth > maxPageTreeDepth || len(out) > maxPages {
			return
		}
		if ref, ok := obj.(Reference); ok {
			if out[ref.Number] {
				return
			}
			out[ref.Number] = true
		}
		dict, ok, err := r.ResolveDict(obj)
		if err != nil || !ok {
			return
		}
		kids, hasKids, err := r.resolveArray(dict["Kids"])
		if err != nil {
			return
		}
		if typ, _ := dict.GetName("Type"); typ != "Page" && hasKids {
			for _, kid := range kids {
				walk(kid, depth+1)
			}
		}
	}
	walk(cat["Pages"], 0)
	return out
}

// applyInherited overlays a node's own /Resources, /MediaBox, and /Rotate onto
// the inherited set, returning the updated copy. Missing entries leave the
// inherited value unchanged.
func (r *Resolver) applyInherited(inh inherited, d Dict) inherited {
	if res, ok, err := r.ResolveDict(d["Resources"]); err == nil && ok {
		inh.resources = res
	}
	if mb, ok := r.resolveRectangle(d["MediaBox"]); ok {
		inh.mediaBox = &mb
	}
	if rot, ok := r.resolveRotate(d["Rotate"]); ok {
		inh.rotate = rot
	}
	return inh
}

// resolveRectangle resolves obj to a 4-number array and converts it to a
// Rectangle. ok is false if obj is absent or not a valid rectangle.
func (r *Resolver) resolveRectangle(obj Object) (Rectangle, bool) {
	arr, ok, err := r.resolveArray(obj)
	if err != nil || !ok || len(arr) < 4 {
		return Rectangle{}, false
	}
	var f [4]float64
	for i := 0; i < 4; i++ {
		v, err := r.Resolve(arr[i])
		if err != nil {
			return Rectangle{}, false
		}
		n, ok := asFloat(v)
		if !ok {
			return Rectangle{}, false
		}
		f[i] = n
	}
	return Rectangle{f[0], f[1], f[2], f[3]}, true
}

// resolveArray resolves obj (following a reference) to an Array.
func (r *Resolver) resolveArray(obj Object) (Array, bool, error) {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil, false, err
	}
	a, ok := resolved.(Array)
	return a, ok, nil
}

// resolveRotate resolves a /Rotate value (§7.7.3.3, a multiple of 90) to one of
// 0/90/180/270, accepting Integer and truncating Real. The value is reduced
// modulo 360 before it is converted to int — in int64 for an Integer, with
// math.Mod (exact) for a Real — because an out-of-range float→int conversion is
// implementation-defined: amd64 yields MinInt64 while js/wasm saturates, so
// /Rotate 1e30 rendered at 270° natively and 0° in the browser. A non-finite
// Real (not producible by the lexer) or a non-number yields ok=false, keeping
// the inherited value.
func (r *Resolver) resolveRotate(obj Object) (int, bool) {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return 0, false
	}
	switch v := resolved.(type) {
	case Integer:
		return normalizeRotate(int(int64(v) % 360)), true
	case Real:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return normalizeRotate(int(math.Mod(math.Trunc(f), 360))), true
	default:
		return 0, false
	}
}

// asFloat converts an Integer or Real to float64.
func asFloat(o Object) (float64, bool) {
	switch v := o.(type) {
	case Integer:
		return float64(v), true
	case Real:
		return float64(v), true
	default:
		return 0, false
	}
}

// normalizeRotate maps any degree value to one of 0/90/180/270.
func normalizeRotate(d int) int {
	d %= 360
	if d < 0 {
		d += 360
	}
	// Floor to a quarter turn so odd values stay valid (lenient).
	return (d / 90) * 90
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

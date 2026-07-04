package core

// pageops.go implements the in-memory page-organization operations Rotate and
// Reorder on Document. Both mutate the receiver in place, touching only page
// geometry/order metadata (never the content streams). They validate fully
// before mutating, so a bad argument leaves the document byte-for-byte
// unchanged. The change is written so it survives a later RenderPage, ExtractText,
// and — critically — a later SaveTo/Save. See docs/design/reorder-rotate.md.

import "github.com/thai-pc/fluxdocs/core/parse"

// Rotate rotates one page by degrees, which must be a multiple of 90 (positive
// or negative). The rotation is RELATIVE: the new angle is the page's current
// rotation plus degrees, normalized to one of {0, 90, 180, 270}. Rotate(i, 90)
// twice yields 180; Rotate(i, -90) backs a page off by a quarter turn. PDF
// /Rotate is a viewer-side display attribute (ISO 32000-1 §7.7.3.3): Wave 1 sets
// it, it does not re-draw page content.
//
// The new angle is written to all three views (the exported Page.Rotation, the
// PageInfo.Rotate render/save uses, and the page's own /Rotate dict entry) so
// render and save agree; the change persists through SaveTo.
//
// Returns ErrPageNotFound if pageIndex is out of range, or ErrInvalidPageOrder
// if degrees is not a multiple of 90. On either error the document is unchanged.
//
// Concurrency: Rotate takes d.mu, but the render read-path does not. Do not call
// Rotate concurrently with an in-flight RenderPage/RenderAllPages on the same
// *Document; quiesce renders first.
func (d *Document) Rotate(pageIndex int, degrees int) error {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return ErrPageNotFound
	}
	if degrees%90 != 0 {
		return ErrInvalidPageOrder
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// pageInfos is the source of truth for the current angle when present; a
	// resolver-less, test-constructed Document has none, so fall back to the
	// exported Page field.
	current := d.Pages[pageIndex].Rotation
	if pageIndex < len(d.pageInfos) {
		current = d.pageInfos[pageIndex].Rotate
	}
	newRot := normalizeDegrees(current + degrees)

	// View 1 (exported geometry) is always updated. Views 2 and 3 exist only
	// when the page was built from the parse layer.
	d.Pages[pageIndex].Rotation = newRot
	if pageIndex < len(d.pageInfos) {
		pi := d.pageInfos[pageIndex]
		pi.Rotate = newRot // view 2: field render.Page and the CopyPage fallback read

		// View 3: the page's OWN /Rotate dict entry, which CopyPage copies
		// verbatim and PREFERS over the inherited pi.Rotate (copy.go:96). It must
		// carry the new angle or a SaveTo would revert to the pre-rotation value.
		// pi.Dict is the resolver's CACHED map — mutating it in place would
		// corrupt parser state for any later resolve of this page object, so
		// shallow-clone first and write to the clone.
		clone := cloneDictShallow(pi.Dict)
		clone["Rotate"] = parse.Integer(newRot)
		pi.Dict = clone

		d.pageInfos[pageIndex] = pi
	}
	return nil
}

// Reorder rearranges pages according to newOrder, a permutation of
// [0, PageCount): newOrder[k] is the OLD index of the page that shall occupy
// position k after the call. Reorder([]int{2,0,1}) moves old page 2 to the front.
//
// A page keeps its identity as it moves: Page.ID travels with the page (only
// Page.Index is rewritten to match the new slot), so the separate annotation
// layer — keyed by PageID — keeps pointing at the same physical page and needs
// no update. The Page.ID suffix (from the original position) is an opaque token,
// not a position; it is intentionally NOT recomputed.
//
// Validation is complete before any mutation: on any bad input the document is
// left unchanged. An empty document with an empty/nil newOrder is a valid no-op.
//
// Returns ErrInvalidPageOrder if newOrder has the wrong length or a
// duplicate/missing index (not a permutation), or ErrPageNotFound if an entry is
// out of range. On any error the document is unchanged.
//
// Concurrency: Reorder takes d.mu, but the render read-path does not. Do not call
// Reorder concurrently with an in-flight RenderPage/RenderAllPages on the same
// *Document; quiesce renders first.
func (d *Document) Reorder(newOrder []int) error {
	n := d.PageCount

	// Validate fully, up front, before touching any state.
	if len(newOrder) != n {
		return ErrInvalidPageOrder
	}
	seen := make([]bool, n)
	for _, idx := range newOrder {
		if idx < 0 || idx >= n {
			return ErrPageNotFound
		}
		if seen[idx] {
			return ErrInvalidPageOrder // duplicate (hence some index is missing)
		}
		seen[idx] = true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Build fresh slices so the operation is atomic and Pages/pageInfos can never
	// diverge. Permute whichever parallel arrays are present, in lockstep.
	newPages := make([]Page, n)
	var newInfos []parse.PageInfo
	if len(d.pageInfos) == n {
		newInfos = make([]parse.PageInfo, n)
	}
	for k, old := range newOrder {
		newPages[k] = d.Pages[old]
		newPages[k].Index = k // restore the Index == slice-position invariant
		// Page.ID is left untouched: identity travels with the page.
		if newInfos != nil {
			newInfos[k] = d.pageInfos[old]
		}
	}

	d.Pages = newPages
	if newInfos != nil {
		d.pageInfos = newInfos
	}
	// PageCount is unchanged: a permutation preserves the count.
	return nil
}

// normalizeDegrees maps any degree value (including negatives and large
// magnitudes) to exactly one of {0, 90, 180, 270}. Rotate rejects non-multiples
// of 90 up front, so a plain modulo suffices — unlike parse.normalizeRotate, this
// does not floor odd values (that leniency is a read-path concern we do not want
// to couple to here).
func normalizeDegrees(x int) int {
	return ((x % 360) + 360) % 360
}

// cloneDictShallow returns a new Dict with every top-level key/value of src
// copied into it. A shallow copy is sufficient for Rotate: CopyPage/copyValue
// read only top-level keys (Rotate, MediaBox, Resources) and then walk nested
// references read-only, so nested values may be shared. This detaches an edit to
// the returned map from the resolver's cached page dict without a deep copy.
func cloneDictShallow(src parse.Dict) parse.Dict {
	dst := make(parse.Dict, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

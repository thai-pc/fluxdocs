package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// This file implements the §7.3 annotation data layer: create/update/remove/
// query annotations in memory and export/import the layer as a plain
// AnnotationLayer struct (JSON via the tags in types.go). Annotations are kept
// entirely separate from the source PDF (CLAUDE.md §1.4/§9); nothing here draws
// onto a page or removes anything from the PDF. Pure Go, WASM-safe, no I/O.
//
// Concurrency: all shared state (annotations, annotationSeq, the current-layer
// fields) is guarded by d.mu. The read-modify-write and the copy that is
// reported back are built inside the lock; the OnAnnotationChange callback is
// then snapshotted, the lock released, and only then invoked — d.mu is
// non-reentrant, so a callback that re-enters (e.g. a viewer re-reading on
// change) must not run under the lock or it self-deadlocks.

// maxImportAnnotations bounds how many annotations a single ImportAnnotationLayer
// will accept. An imported layer is untrusted input; the cap defends against a
// memory blow-up (SECURITY.md §3 spirit). Wave-1 documents carry tens of
// annotations, so this is far above any legitimate use.
const maxImportAnnotations = 100000

// AddAnnotation adds an annotation to the current layer and returns the stored
// copy with its ID and timestamps assigned.
//
// Validation (fail fast, in order): Type must be one of the Wave-1 editable
// types highlight/note/draw/shape — anything else (including the deferred
// stamp/redact/signature types) returns ErrInvalidAnnotation; PageID must match
// an existing page or ErrPageNotFound; Rect.Width/Height must be non-negative
// and all rect coordinates and Points finite (no NaN/Inf) or ErrInvalidAnnotation.
// Opacity is clamped to [0,1], with the zero value treated as unset and
// defaulted to 1.0.
//
// A caller-supplied a.ID is ignored and overwritten with a fresh monotonic
// "ann-<n>" ID. CreatedAt and UpdatedAt are set to time.Now().UTC(). The
// returned annotation is an independent deep copy (its Points/Meta do not alias
// the document's state); mutating it does not affect the document. Fires the
// OnAnnotationChange callback once, after the lock is released.
func (d *Document) AddAnnotation(a Annotation) (Annotation, error) {
	if !isEditableType(a.Type) {
		return Annotation{}, fmt.Errorf("%w: type %q is not editable", ErrInvalidAnnotation, a.Type)
	}
	if !d.hasPage(a.PageID) {
		return Annotation{}, ErrPageNotFound
	}
	if !validRect(a.Rect) {
		return Annotation{}, fmt.Errorf("%w: rect has negative or non-finite dimensions", ErrInvalidAnnotation)
	}
	if !validPoints(a.Points) {
		return Annotation{}, fmt.Errorf("%w: points contain a non-finite coordinate", ErrInvalidAnnotation)
	}

	d.mu.Lock()
	d.ensureLayerLocked()
	now := time.Now().UTC()
	stored := copyAnnotation(a)
	stored.ID = d.mintIDLocked()
	stored.Opacity = defaultOpacity(a.Opacity)
	stored.CreatedAt = now
	stored.UpdatedAt = now
	d.annotations = append(d.annotations, stored)
	reported := copyAnnotation(stored)
	fn := d.onAnnotationChange
	d.mu.Unlock()

	if fn != nil {
		fn(reported)
	}
	return copyAnnotation(stored), nil
}

// UpdateAnnotation applies a partial update by patch (a nil pointer field is
// left unchanged; a nil Points slice or nil Meta map is left unchanged, while a
// non-nil one replaces the stored value — clearing to empty is out of scope).
//
// A patched Rect is re-validated (finite, non-negative) and a patched Opacity is
// clamped to [0,1]; an invalid patch returns ErrInvalidAnnotation and leaves the
// annotation unmodified. UpdatedAt is bumped; CreatedAt is untouched. Returns
// ErrAnnotationNotFound if no annotation has the given id. The returned
// annotation is an independent deep copy. Fires OnAnnotationChange once, after
// the lock is released.
func (d *Document) UpdateAnnotation(id AnnotationID, patch AnnotationPatch) (Annotation, error) {
	if patch.Rect != nil && !validRect(*patch.Rect) {
		return Annotation{}, fmt.Errorf("%w: patched rect has negative or non-finite dimensions", ErrInvalidAnnotation)
	}

	d.mu.Lock()
	idx := d.indexOfLocked(id)
	if idx < 0 {
		d.mu.Unlock()
		return Annotation{}, ErrAnnotationNotFound
	}

	// Work on a copy so a mid-update failure never leaves a partial mutation.
	cur := copyAnnotation(d.annotations[idx])
	if patch.Rect != nil {
		cur.Rect = *patch.Rect
	}
	if patch.Color != nil {
		cur.Color = *patch.Color
	}
	if patch.Opacity != nil {
		cur.Opacity = clampOpacity(*patch.Opacity)
	}
	if patch.Content != nil {
		cur.Content = *patch.Content
	}
	if patch.Points != nil {
		cur.Points = clonePoints(patch.Points)
	}
	if patch.Meta != nil {
		cur.Meta = cloneMeta(patch.Meta)
	}
	cur.UpdatedAt = time.Now().UTC()

	d.annotations[idx] = cur
	reported := copyAnnotation(cur)
	fn := d.onAnnotationChange
	d.mu.Unlock()

	if fn != nil {
		fn(reported)
	}
	return copyAnnotation(cur), nil
}

// RemoveAnnotation deletes the annotation by ID, or returns
// ErrAnnotationNotFound. The remaining annotations keep their relative order.
// Fires OnAnnotationChange once with the removed annotation's last state (so a
// viewer knows what left), after the lock is released.
func (d *Document) RemoveAnnotation(id AnnotationID) error {
	d.mu.Lock()
	idx := d.indexOfLocked(id)
	if idx < 0 {
		d.mu.Unlock()
		return ErrAnnotationNotFound
	}
	removed := copyAnnotation(d.annotations[idx])
	d.annotations = append(d.annotations[:idx], d.annotations[idx+1:]...)
	fn := d.onAnnotationChange
	d.mu.Unlock()

	if fn != nil {
		fn(removed)
	}
	return nil
}

// GetAnnotations returns the annotations belonging to one page, in insertion
// order, as independent deep copies (mutating them does not affect the
// document). An unknown pageID is not an error — the annotation layer is
// deliberately decoupled from the live page set (a layer may be imported before
// or without its page) — it simply returns nil. Returns nil when the page has no
// annotations.
func (d *Document) GetAnnotations(pageID PageID) ([]Annotation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []Annotation
	for i := range d.annotations {
		if d.annotations[i].PageID == pageID {
			out = append(out, copyAnnotation(d.annotations[i]))
		}
	}
	return out, nil
}

// ExportAnnotationLayer exports the annotation layer independently of the
// original PDF (§6.2), for separate storage/sync or sharing among reviewers.
//
// It returns a bare *AnnotationLayer wrapping a deep copy of every annotation in
// insertion order under the current-layer identity; turning it into bytes is
// encoding/json's job via the tags in types.go. The layer carries only
// annotation geometry, colors, authored text, ids and timestamps — never any
// source-PDF bytes. Export never fails; the error is part of the API shape.
func (d *Document) ExportAnnotationLayer() (*AnnotationLayer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureLayerLocked()

	anns := make([]Annotation, len(d.annotations))
	for i := range d.annotations {
		anns[i] = copyAnnotation(d.annotations[i])
	}
	return &AnnotationLayer{
		ID:          d.layerID,
		DocumentID:  d.ID,
		Name:        d.layerName,
		Annotations: anns,
		Visible:     d.layerVisible,
		CreatedAt:   d.layerCreatedAt,
	}, nil
}

// ImportAnnotationLayer MERGES a previously exported annotation layer into the
// document: the incoming annotations are appended to whatever the document
// already holds (it does not replace them).
//
// Each incoming Rect is validated (finite, non-negative) up front; an invalid
// rect or a layer exceeding the import size cap returns ErrInvalidAnnotation and
// nothing is imported. Annotations of the deferred types (stamp/redact/
// signature) are preserved verbatim as inert data for round-trip fidelity — they
// are stored, never acted on, never rendered in Wave 1; a redact-typed record
// here is data, not a redaction, and removes nothing from any PDF. An incoming
// ID that is empty or collides with an existing one is re-keyed to a fresh
// "ann-<n>"; kept numeric IDs advance the counter so later AddAnnotation calls
// never collide. CreatedAt/UpdatedAt are preserved (not restamped) so
// export→import→export is stable. No per-annotation callback fires (import is a
// bulk load; a viewer refreshes via GetAnnotations). Points/Meta are deep-copied
// on the way in.
//
// When the document has no annotation layer yet, the imported layer's identity
// (id, documentId, name, visible, createdAt) is adopted so a full
// export→import→export round-trip on a fresh document is stable — not just the
// annotations. A document that already has a layer keeps its own identity and
// only gains the imported annotations (merge).
func (d *Document) ImportAnnotationLayer(layer AnnotationLayer) error {
	if len(layer.Annotations) > maxImportAnnotations {
		return fmt.Errorf("%w: layer has %d annotations, exceeds cap %d",
			ErrInvalidAnnotation, len(layer.Annotations), maxImportAnnotations)
	}
	// Validate everything before mutating so a bad record leaves the document
	// untouched (no partial import). Reject non-finite numbers (NaN/Inf) in the
	// rect, points, or opacity: unlike Add, import preserves Opacity verbatim
	// (no clamping), and encoding/json cannot marshal NaN/Inf — so an unchecked
	// value would silently store here and later break ExportAnnotationLayer.
	for i := range layer.Annotations {
		a := layer.Annotations[i]
		if !validRect(a.Rect) {
			return fmt.Errorf("%w: imported annotation %d has an invalid rect", ErrInvalidAnnotation, i)
		}
		if !validPoints(a.Points) {
			return fmt.Errorf("%w: imported annotation %d has a non-finite point", ErrInvalidAnnotation, i)
		}
		if !isFinite(a.Opacity) {
			return fmt.Errorf("%w: imported annotation %d has a non-finite opacity", ErrInvalidAnnotation, i)
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.adoptLayerIdentityLocked(layer)

	// claimed tracks every ID already in use (existing + kept + minted) so a
	// minted replacement never collides with any of them.
	claimed := make(map[AnnotationID]bool, len(d.annotations)+len(layer.Annotations))
	for i := range d.annotations {
		claimed[d.annotations[i].ID] = true
	}

	for i := range layer.Annotations {
		a := copyAnnotation(layer.Annotations[i])
		if a.ID == "" || claimed[a.ID] {
			a.ID = d.mintUniqueLocked(claimed)
		} else {
			d.advanceSeqLocked(a.ID)
		}
		claimed[a.ID] = true
		d.annotations = append(d.annotations, a)
	}
	return nil
}

// ensureLayerLocked initializes the current-layer identity on first use. Called
// under d.mu. A Document may be constructed directly (in tests) as well as via
// OpenBytes, so lazy initialization keeps both paths consistent without an
// explicit constructor step.
func (d *Document) ensureLayerLocked() {
	if d.layerID == "" && d.layerCreatedAt.IsZero() {
		d.layerID = "layer-1"
		d.layerVisible = true
		d.layerCreatedAt = time.Now().UTC()
	}
}

// adoptLayerIdentityLocked seeds the current-layer identity from an imported
// layer the first time a document receives one, so export→import→export
// round-trips the whole AnnotationLayer (id/documentId/name/visible/createdAt),
// not just the annotations. Once the document has a layer identity it is kept
// (a later import only contributes annotations — merge, never rename). Called
// under d.mu. Missing fields fall back to the ensureLayerLocked defaults so the
// invariant "a document always has a well-formed layer after touch" still holds.
func (d *Document) adoptLayerIdentityLocked(layer AnnotationLayer) {
	if d.layerID != "" || !d.layerCreatedAt.IsZero() {
		d.ensureLayerLocked() // already have a layer (or a partial one) — keep it
		return
	}
	if layer.ID != "" {
		d.layerID = layer.ID
	} else {
		d.layerID = "layer-1"
	}
	d.layerName = layer.Name
	d.layerVisible = layer.Visible
	if !layer.CreatedAt.IsZero() {
		d.layerCreatedAt = layer.CreatedAt
	} else {
		d.layerCreatedAt = time.Now().UTC()
	}
	// A document with no identity of its own adopts the layer's DocumentID so a
	// round-trip export reproduces it; a document opened from a real file keeps
	// its own ID (the layer is now part of that document).
	if d.ID == "" {
		d.ID = layer.DocumentID
	}
}

// mintIDLocked returns the next monotonic annotation ID ("ann-1", "ann-2", …).
// Deterministic and document-local (see the design note's O1: switch to a
// collision-free scheme at Wave-3 merge/collaboration). Called under d.mu.
func (d *Document) mintIDLocked() AnnotationID {
	d.annotationSeq++
	return AnnotationID("ann-" + strconv.FormatUint(d.annotationSeq, 10))
}

// mintUniqueLocked mints IDs until one is not already claimed. Called under
// d.mu.
func (d *Document) mintUniqueLocked(claimed map[AnnotationID]bool) AnnotationID {
	for {
		id := d.mintIDLocked()
		if !claimed[id] {
			return id
		}
	}
}

// advanceSeqLocked pushes annotationSeq past a kept "ann-<n>" ID so a later mint
// never reproduces it. Non-"ann-<n>" IDs are ignored. Called under d.mu.
func (d *Document) advanceSeqLocked(id AnnotationID) {
	const prefix = "ann-"
	s := string(id)
	if !strings.HasPrefix(s, prefix) {
		return
	}
	n, err := strconv.ParseUint(s[len(prefix):], 10, 64)
	if err != nil {
		return
	}
	if n > d.annotationSeq {
		d.annotationSeq = n
	}
}

// indexOfLocked returns the slice index of the annotation with id, or -1.
// Called under d.mu.
func (d *Document) indexOfLocked(id AnnotationID) int {
	for i := range d.annotations {
		if d.annotations[i].ID == id {
			return i
		}
	}
	return -1
}

// hasPage reports whether a page with the given ID exists. d.Pages is fixed at
// open time (Wave 1 does not mutate it), so this reads without the lock.
func (d *Document) hasPage(id PageID) bool {
	for i := range d.Pages {
		if d.Pages[i].ID == id {
			return true
		}
	}
	return false
}

// isEditableType reports whether t is one of the four Wave-1 editable annotation
// types. The deferred stamp/redact/signature types are not creatable via
// AddAnnotation (they may only arrive as inert data through import).
func isEditableType(t AnnotationType) bool {
	switch t {
	case AnnotationHighlight, AnnotationNote, AnnotationDraw, AnnotationShape:
		return true
	default:
		return false
	}
}

// validRect reports whether r is usable: non-negative width/height and all four
// coordinates finite (untrusted numeric input — reject NaN/Inf, CONVENTIONS §2).
func validRect(r Rect) bool {
	if r.Width < 0 || r.Height < 0 {
		return false
	}
	return isFinite(r.X) && isFinite(r.Y) && isFinite(r.Width) && isFinite(r.Height)
}

// validPoints reports whether every coordinate in pts is finite (no NaN/Inf).
// A nil/empty slice is valid. Guards the freehand-draw Points against untrusted
// numeric input that encoding/json cannot marshal (so Export never fails).
func validPoints(pts []Point) bool {
	for i := range pts {
		if !isFinite(pts[i].X) || !isFinite(pts[i].Y) {
			return false
		}
	}
	return true
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// defaultOpacity treats the zero value as "unset" and defaults it to 1.0,
// otherwise clamps to [0,1]. Used by AddAnnotation, where a zero Opacity is
// indistinguishable from an unset field.
func defaultOpacity(o float64) float64 {
	if o == 0 {
		return 1.0
	}
	return clampOpacity(o)
}

// clampOpacity clamps o to [0,1], mapping NaN to the safe default 1.0. Used for
// an explicit patch, where 0 is a real caller-chosen value.
func clampOpacity(o float64) float64 {
	switch {
	case math.IsNaN(o):
		return 1.0
	case o < 0:
		return 0
	case o > 1:
		return 1
	default:
		return o
	}
}

// copyAnnotation returns a deep copy of a: the Points slice and Meta map are
// cloned so no internal state aliases a caller's value (and vice versa). Meta
// values are copied shallowly (any) — acceptable at Wave-1 volumes.
func copyAnnotation(a Annotation) Annotation {
	out := a
	out.Points = clonePoints(a.Points)
	out.Meta = cloneMeta(a.Meta)
	return out
}

// clonePoints returns an independent copy of pts (nil stays nil).
func clonePoints(pts []Point) []Point {
	if pts == nil {
		return nil
	}
	cp := make([]Point, len(pts))
	copy(cp, pts)
	return cp
}

// cloneMeta returns an independent copy of m (nil stays nil). Values are copied
// shallowly.
func cloneMeta(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

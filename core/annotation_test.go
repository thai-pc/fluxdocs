package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// validAnn returns a minimal valid editable annotation on page-0 for the given
// type, so individual tests only override the fields they care about.
func validAnn(t AnnotationType) Annotation {
	return Annotation{
		PageID: "page-0",
		Type:   t,
		Rect:   Rect{X: 10, Y: 20, Width: 30, Height: 40},
		Color:  "#f59e0b",
	}
}

// ---------------------------------------------------------------------------
// AddAnnotation — happy path
// ---------------------------------------------------------------------------

func TestAddAnnotation_HappyPath(t *testing.T) {
	types := []AnnotationType{AnnotationHighlight, AnnotationNote, AnnotationDraw, AnnotationShape}

	for i, typ := range types {
		t.Run(string(typ), func(t *testing.T) {
			doc := newTestDoc(1)
			// A caller-supplied ID must be ignored and overwritten.
			in := validAnn(typ)
			in.ID = "caller-supplied-should-be-dropped"
			in.Opacity = 0 // zero == unset -> should default to 1.0

			got, err := doc.AddAnnotation(in)
			if err != nil {
				t.Fatalf("AddAnnotation(%s) err = %v, want nil", typ, err)
			}
			if got.ID != "ann-1" {
				t.Errorf("first add ID = %q, want %q", got.ID, "ann-1")
			}
			if got.Opacity != 1.0 {
				t.Errorf("Opacity = %v, want 1.0 (zero defaulted)", got.Opacity)
			}
			if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
				t.Errorf("timestamps not set: CreatedAt=%v UpdatedAt=%v", got.CreatedAt, got.UpdatedAt)
			}
			if !got.CreatedAt.Equal(got.UpdatedAt) {
				t.Errorf("CreatedAt(%v) != UpdatedAt(%v) on add", got.CreatedAt, got.UpdatedAt)
			}

			// The stored copy must carry the minted ID/timestamps, not the input's.
			stored, _ := doc.GetAnnotations("page-0")
			if len(stored) != 1 {
				t.Fatalf("stored count = %d, want 1", len(stored))
			}
			if stored[0].ID != "ann-1" {
				t.Errorf("stored ID = %q, want ann-1", stored[0].ID)
			}
			if !stored[0].CreatedAt.Equal(got.CreatedAt) {
				t.Errorf("stored CreatedAt = %v, want %v", stored[0].CreatedAt, got.CreatedAt)
			}
			_ = i
		})
	}
}

func TestAddAnnotation_MintsMonotonicIDs(t *testing.T) {
	doc := newTestDoc(1)
	for _, want := range []AnnotationID{"ann-1", "ann-2", "ann-3"} {
		got, err := doc.AddAnnotation(validAnn(AnnotationHighlight))
		if err != nil {
			t.Fatalf("AddAnnotation err = %v", err)
		}
		if got.ID != want {
			t.Errorf("minted ID = %q, want %q", got.ID, want)
		}
	}
}

// ---------------------------------------------------------------------------
// AddAnnotation — validation (incl. S2: NaN/Inf rejection on Add)
// ---------------------------------------------------------------------------

func TestAddAnnotation_Validation(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)

	tests := []struct {
		name    string
		mutate  func(a *Annotation)
		wantErr error
	}{
		{"stamp not editable", func(a *Annotation) { a.Type = AnnotationStamp }, ErrInvalidAnnotation},
		{"redact not editable", func(a *Annotation) { a.Type = AnnotationRedact }, ErrInvalidAnnotation},
		{"signature not editable", func(a *Annotation) { a.Type = AnnotationSignature }, ErrInvalidAnnotation},
		{"unknown type", func(a *Annotation) { a.Type = "scribble" }, ErrInvalidAnnotation},
		{"unknown page", func(a *Annotation) { a.PageID = "page-999" }, ErrPageNotFound},
		{"negative width", func(a *Annotation) { a.Rect.Width = -1 }, ErrInvalidAnnotation},
		{"negative height", func(a *Annotation) { a.Rect.Height = -1 }, ErrInvalidAnnotation},
		{"NaN rect X", func(a *Annotation) { a.Rect.X = nan }, ErrInvalidAnnotation},
		{"Inf rect width", func(a *Annotation) { a.Rect.Width = inf }, ErrInvalidAnnotation},
		{"NaN draw point", func(a *Annotation) {
			a.Type = AnnotationDraw
			a.Points = []Point{{X: 1, Y: 2}, {X: nan, Y: 4}}
		}, ErrInvalidAnnotation},
		{"Inf draw point", func(a *Annotation) {
			a.Type = AnnotationDraw
			a.Points = []Point{{X: inf, Y: 4}}
		}, ErrInvalidAnnotation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := newTestDoc(1)
			a := validAnn(AnnotationHighlight)
			tt.mutate(&a)

			_, err := doc.AddAnnotation(a)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AddAnnotation err = %v, want %v", err, tt.wantErr)
			}
			// A rejected add must not store anything.
			if n := len(doc.annotations); n != 0 {
				t.Errorf("after rejected add, stored count = %d, want 0", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateAnnotation
// ---------------------------------------------------------------------------

func TestUpdateAnnotation_PatchOnlySetFields(t *testing.T) {
	doc := newTestDoc(1)
	in := validAnn(AnnotationNote)
	in.Color = "#111111"
	in.Content = "original"
	added, err := doc.AddAnnotation(in)
	if err != nil {
		t.Fatalf("AddAnnotation err = %v", err)
	}

	// Guarantee a measurable UpdatedAt bump.
	time.Sleep(2 * time.Millisecond)

	newColor := "#222222"
	patched, err := doc.UpdateAnnotation(added.ID, AnnotationPatch{Color: &newColor})
	if err != nil {
		t.Fatalf("UpdateAnnotation err = %v", err)
	}
	if patched.Color != newColor {
		t.Errorf("Color = %q, want %q", patched.Color, newColor)
	}
	if patched.Content != "original" {
		t.Errorf("Content = %q, want unchanged %q", patched.Content, "original")
	}
	if !patched.CreatedAt.Equal(added.CreatedAt) {
		t.Errorf("CreatedAt changed: %v -> %v", added.CreatedAt, patched.CreatedAt)
	}
	if !patched.UpdatedAt.After(added.UpdatedAt) {
		t.Errorf("UpdatedAt not bumped: was %v, now %v", added.UpdatedAt, patched.UpdatedAt)
	}
}

func TestUpdateAnnotation_UnknownID(t *testing.T) {
	doc := newTestDoc(1)
	_, err := doc.UpdateAnnotation("ann-nope", AnnotationPatch{})
	if !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("err = %v, want ErrAnnotationNotFound", err)
	}
}

func TestUpdateAnnotation_InvalidPatchLeavesUnmodified(t *testing.T) {
	doc := newTestDoc(1)
	added, _ := doc.AddAnnotation(validAnn(AnnotationShape))

	bad := Rect{X: 0, Y: 0, Width: -5, Height: 10}
	_, err := doc.UpdateAnnotation(added.ID, AnnotationPatch{Rect: &bad})
	if !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("err = %v, want ErrInvalidAnnotation", err)
	}

	// Original rect must be intact.
	got, _ := doc.GetAnnotations("page-0")
	if got[0].Rect != (Rect{X: 10, Y: 20, Width: 30, Height: 40}) {
		t.Errorf("rect mutated after invalid patch: %+v", got[0].Rect)
	}
}

// ---------------------------------------------------------------------------
// RemoveAnnotation
// ---------------------------------------------------------------------------

func TestRemoveAnnotation_PreservesOrder(t *testing.T) {
	doc := newTestDoc(1)
	a1, _ := doc.AddAnnotation(validAnn(AnnotationHighlight))
	a2, _ := doc.AddAnnotation(validAnn(AnnotationNote))
	a3, _ := doc.AddAnnotation(validAnn(AnnotationShape))

	if err := doc.RemoveAnnotation(a2.ID); err != nil {
		t.Fatalf("RemoveAnnotation err = %v", err)
	}

	got, _ := doc.GetAnnotations("page-0")
	if len(got) != 2 {
		t.Fatalf("count = %d, want 2", len(got))
	}
	if got[0].ID != a1.ID || got[1].ID != a3.ID {
		t.Errorf("order = [%s %s], want [%s %s]", got[0].ID, got[1].ID, a1.ID, a3.ID)
	}
}

func TestRemoveAnnotation_UnknownID(t *testing.T) {
	doc := newTestDoc(1)
	if err := doc.RemoveAnnotation("ann-nope"); !errors.Is(err, ErrAnnotationNotFound) {
		t.Fatalf("err = %v, want ErrAnnotationNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// GetAnnotations
// ---------------------------------------------------------------------------

func TestGetAnnotations_FilterAndOrder(t *testing.T) {
	doc := newTestDoc(2)

	p0a := validAnn(AnnotationHighlight)
	p0b := validAnn(AnnotationNote)
	p1 := validAnn(AnnotationShape)
	p1.PageID = "page-1"

	id0a, _ := doc.AddAnnotation(p0a)
	_, _ = doc.AddAnnotation(p1)
	id0b, _ := doc.AddAnnotation(p0b)

	got0, err := doc.GetAnnotations("page-0")
	if err != nil {
		t.Fatalf("GetAnnotations err = %v", err)
	}
	if len(got0) != 2 || got0[0].ID != id0a.ID || got0[1].ID != id0b.ID {
		t.Errorf("page-0 = %v, want insertion order [%s %s]", ids(got0), id0a.ID, id0b.ID)
	}

	got1, _ := doc.GetAnnotations("page-1")
	if len(got1) != 1 {
		t.Errorf("page-1 count = %d, want 1", len(got1))
	}
}

func TestGetAnnotations_UnknownPageIsNilNoError(t *testing.T) {
	doc := newTestDoc(1)
	doc.AddAnnotation(validAnn(AnnotationHighlight))

	got, err := doc.GetAnnotations("page-does-not-exist")
	if err != nil {
		t.Fatalf("err = %v, want nil (layer is decoupled from live pages)", err)
	}
	if got != nil {
		t.Errorf("got = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Deep-copy discipline (no aliasing in either direction)
// ---------------------------------------------------------------------------

func TestAddAnnotation_NoInputAliasing(t *testing.T) {
	doc := newTestDoc(1)
	in := validAnn(AnnotationDraw)
	in.Points = []Point{{X: 1, Y: 1}}
	in.Meta = map[string]any{"k": "v"}

	doc.AddAnnotation(in)

	// Mutate the caller's originals after the add.
	in.Points[0] = Point{X: 999, Y: 999}
	in.Meta["k"] = "mutated"

	got, _ := doc.GetAnnotations("page-0")
	if got[0].Points[0] != (Point{X: 1, Y: 1}) {
		t.Errorf("stored Points aliased input: %+v", got[0].Points[0])
	}
	if got[0].Meta["k"] != "v" {
		t.Errorf("stored Meta aliased input: %v", got[0].Meta["k"])
	}
}

func TestGetAnnotations_ReturnedCopyIsIndependent(t *testing.T) {
	doc := newTestDoc(1)
	in := validAnn(AnnotationDraw)
	in.Points = []Point{{X: 1, Y: 1}}
	in.Meta = map[string]any{"k": "v"}
	doc.AddAnnotation(in)

	first, _ := doc.GetAnnotations("page-0")
	first[0].Points[0] = Point{X: 42, Y: 42}
	first[0].Meta["k"] = "clobbered"

	second, _ := doc.GetAnnotations("page-0")
	if second[0].Points[0] != (Point{X: 1, Y: 1}) {
		t.Errorf("mutating a returned Points slice leaked into the doc: %+v", second[0].Points[0])
	}
	if second[0].Meta["k"] != "v" {
		t.Errorf("mutating a returned Meta map leaked into the doc: %v", second[0].Meta["k"])
	}
}

// ---------------------------------------------------------------------------
// OnAnnotationChange callback
// ---------------------------------------------------------------------------

func TestOnAnnotationChange_FiresOncePerMutation(t *testing.T) {
	doc := newTestDoc(1)

	var calls int
	var last Annotation
	doc.OnAnnotationChange(func(a Annotation) {
		calls++
		last = a
	})

	added, _ := doc.AddAnnotation(validAnn(AnnotationHighlight))
	if calls != 1 || last.ID != added.ID {
		t.Fatalf("after add: calls=%d last=%s, want 1 and %s", calls, last.ID, added.ID)
	}

	newContent := "hello"
	updated, _ := doc.UpdateAnnotation(added.ID, AnnotationPatch{Content: &newContent})
	if calls != 2 || last.Content != "hello" || last.ID != updated.ID {
		t.Fatalf("after update: calls=%d last=%+v, want 2 with content hello", calls, last)
	}

	doc.RemoveAnnotation(added.ID)
	if calls != 3 || last.ID != added.ID {
		t.Fatalf("after remove: calls=%d last=%s, want 3 with removed id", calls, last.ID)
	}
}

func TestOnAnnotationChange_NotFiredOnValidationError(t *testing.T) {
	doc := newTestDoc(1)
	var calls int
	doc.OnAnnotationChange(func(Annotation) { calls++ })

	doc.AddAnnotation(validAnn(AnnotationRedact)) // rejected type
	if calls != 0 {
		t.Errorf("callback fired %d times on a rejected add, want 0", calls)
	}
}

// TestOnAnnotationChange_ReentrantNoDeadlock pins the unlock-before-callback
// discipline: a callback that re-enters the document (here GetAnnotations, which
// takes d.mu) must not self-deadlock. Run under -race.
func TestOnAnnotationChange_ReentrantNoDeadlock(t *testing.T) {
	doc := newTestDoc(1)
	doc.OnAnnotationChange(func(a Annotation) {
		// Re-enter: this would deadlock if the callback ran while holding d.mu.
		_, _ = doc.GetAnnotations(a.PageID)
	})

	done := make(chan struct{})
	go func() {
		doc.AddAnnotation(validAnn(AnnotationHighlight))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AddAnnotation deadlocked: callback re-entered while holding d.mu")
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip — pins S1 (import adopts the layer identity)
// ---------------------------------------------------------------------------

func TestAnnotationLayer_RoundTripStable(t *testing.T) {
	doc := newTestDoc(2)
	doc.ID = "doc-round-trip"
	// White-box: seed a layer name; ensureLayerLocked keeps it while minting the
	// rest of the identity, so the exported layer carries a non-default Name.
	doc.layerName = "reviewer layer"

	// A highlight at origin (zero x/y must survive), a draw with Points/Meta.
	h := validAnn(AnnotationHighlight)
	h.Rect = Rect{X: 0, Y: 0, Width: 100, Height: 10}
	doc.AddAnnotation(h)

	d := validAnn(AnnotationDraw)
	d.Points = []Point{{X: 1, Y: 2}, {X: 3, Y: 4}}
	d.Meta = map[string]any{"tool": "pen"}
	drawn, _ := doc.AddAnnotation(d)

	// Force a stored opacity of exactly 0 (Add would default 0 -> 1.0).
	zero := 0.0
	doc.UpdateAnnotation(drawn.ID, AnnotationPatch{Opacity: &zero})

	layer1, err := doc.ExportAnnotationLayer()
	if err != nil {
		t.Fatalf("ExportAnnotationLayer err = %v", err)
	}

	raw, err := json.Marshal(layer1)
	if err != nil {
		t.Fatalf("json.Marshal err = %v", err)
	}
	// Real zeros must be present in the JSON, not dropped by omitempty.
	if !strings.Contains(string(raw), `"opacity":0`) {
		t.Errorf(`JSON missing "opacity":0 — a real zero was dropped:\n%s`, raw)
	}
	if !strings.Contains(string(raw), `"x":0`) {
		t.Errorf(`JSON missing "x":0 — a real zero coord was dropped:\n%s`, raw)
	}

	var layer2 AnnotationLayer
	if err := json.Unmarshal(raw, &layer2); err != nil {
		t.Fatalf("json.Unmarshal err = %v", err)
	}

	// Import into a FRESH empty document — it must adopt the layer identity (S1).
	fresh := &Document{}
	if err := fresh.ImportAnnotationLayer(layer2); err != nil {
		t.Fatalf("ImportAnnotationLayer err = %v", err)
	}
	layer3, err := fresh.ExportAnnotationLayer()
	if err != nil {
		t.Fatalf("second ExportAnnotationLayer err = %v", err)
	}

	// The whole layer must be equal, including id/documentId/name/visible/
	// createdAt — not just the annotations. Compare via marshaled bytes so
	// time.Time is compared by its RFC3339 encoding (reflect.DeepEqual on
	// post-JSON time.Time is flaky re: monotonic/location).
	raw3, err := json.Marshal(layer3)
	if err != nil {
		t.Fatalf("json.Marshal(layer3) err = %v", err)
	}
	if !bytes.Equal(raw, raw3) {
		t.Errorf("round-trip layer not stable:\n first: %s\nsecond: %s", raw, raw3)
	}

	// Explicit identity assertions (documents WHY S1 matters: without adopting the
	// imported identity, DocumentID would be "" and CreatedAt would be time.Now()).
	if layer3.DocumentID != "doc-round-trip" {
		t.Errorf("DocumentID = %q, want doc-round-trip (identity not adopted)", layer3.DocumentID)
	}
	if layer3.Name != "reviewer layer" {
		t.Errorf("Name = %q, want reviewer layer (identity not adopted)", layer3.Name)
	}
	if !layer3.CreatedAt.Equal(layer1.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v (identity not adopted)", layer3.CreatedAt, layer1.CreatedAt)
	}
}

// ---------------------------------------------------------------------------
// Import — merge + re-key
// ---------------------------------------------------------------------------

func TestImportAnnotationLayer_MergeAndRekey(t *testing.T) {
	doc := newTestDoc(1)
	doc.AddAnnotation(validAnn(AnnotationHighlight)) // ann-1
	doc.AddAnnotation(validAnn(AnnotationNote))      // ann-2

	r := Rect{X: 1, Y: 1, Width: 2, Height: 2}
	layer := AnnotationLayer{
		Annotations: []Annotation{
			{ID: "ann-1", PageID: "page-0", Type: AnnotationShape, Rect: r}, // collides -> re-key
			{ID: "", PageID: "page-0", Type: AnnotationShape, Rect: r},      // empty -> re-key
			{ID: "custom-x", PageID: "page-0", Type: AnnotationShape, Rect: r},
			{ID: "ann-5", PageID: "page-0", Type: AnnotationShape, Rect: r}, // kept, advances seq
		},
	}
	if err := doc.ImportAnnotationLayer(layer); err != nil {
		t.Fatalf("ImportAnnotationLayer err = %v", err)
	}

	all, _ := doc.GetAnnotations("page-0")
	if len(all) != 6 {
		t.Fatalf("total annotations = %d, want 6", len(all))
	}

	// All IDs must be unique and non-empty.
	seen := map[AnnotationID]bool{}
	for _, a := range all {
		if a.ID == "" {
			t.Errorf("empty ID survived import")
		}
		if seen[a.ID] {
			t.Errorf("duplicate ID after import: %s", a.ID)
		}
		seen[a.ID] = true
	}
	// The two kept IDs must be present verbatim.
	if !seen["custom-x"] || !seen["ann-5"] {
		t.Errorf("kept IDs missing: custom-x=%v ann-5=%v", seen["custom-x"], seen["ann-5"])
	}

	// A subsequent Add must not collide, and the counter must have advanced past
	// the kept ann-5 (so it lands at ann-6).
	added, _ := doc.AddAnnotation(validAnn(AnnotationHighlight))
	if seen[added.ID] {
		t.Errorf("post-import Add collided with an existing ID: %s", added.ID)
	}
	if added.ID != "ann-6" {
		t.Errorf("post-import Add ID = %q, want ann-6 (seq advanced past ann-5)", added.ID)
	}
}

// ---------------------------------------------------------------------------
// Import — validation / DoS (pins S2 on import + no-partial-import)
// ---------------------------------------------------------------------------

func TestImportAnnotationLayer_RejectsNonFiniteNothingImported(t *testing.T) {
	r := Rect{X: 1, Y: 1, Width: 2, Height: 2}

	tests := []struct {
		name  string
		layer AnnotationLayer
	}{
		{"NaN opacity", AnnotationLayer{Annotations: []Annotation{
			{PageID: "page-0", Type: AnnotationShape, Rect: r},
			{PageID: "page-0", Type: AnnotationShape, Rect: r, Opacity: math.NaN()},
		}}},
		{"NaN point", AnnotationLayer{Annotations: []Annotation{
			{PageID: "page-0", Type: AnnotationDraw, Rect: r,
				Points: []Point{{X: math.NaN(), Y: 0}}},
		}}},
		{"Inf rect", AnnotationLayer{Annotations: []Annotation{
			{PageID: "page-0", Type: AnnotationShape, Rect: Rect{X: math.Inf(1)}},
		}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := newTestDoc(1)
			doc.AddAnnotation(validAnn(AnnotationHighlight)) // pre-existing state
			before := len(doc.annotations)

			err := doc.ImportAnnotationLayer(tt.layer)
			if !errors.Is(err, ErrInvalidAnnotation) {
				t.Fatalf("err = %v, want ErrInvalidAnnotation", err)
			}
			if len(doc.annotations) != before {
				t.Errorf("partial import: annotations went %d -> %d", before, len(doc.annotations))
			}
		})
	}
}

func TestImportAnnotationLayer_SizeCap(t *testing.T) {
	doc := newTestDoc(1)
	// A slice of zero-value annotations with valid (all-zero) rects: the size
	// check fires before per-annotation validation, so this stays cheap.
	layer := AnnotationLayer{Annotations: make([]Annotation, maxImportAnnotations+1)}

	err := doc.ImportAnnotationLayer(layer)
	if !errors.Is(err, ErrInvalidAnnotation) {
		t.Fatalf("err = %v, want ErrInvalidAnnotation for over-cap import", err)
	}
	if len(doc.annotations) != 0 {
		t.Errorf("over-cap import stored %d annotations, want 0", len(doc.annotations))
	}
}

// ---------------------------------------------------------------------------
// Import — deferred-type preservation (stored verbatim, round-trips)
// ---------------------------------------------------------------------------

func TestImportAnnotationLayer_PreservesDeferredTypes(t *testing.T) {
	doc := newTestDoc(1)
	r := Rect{X: 5, Y: 5, Width: 10, Height: 10}
	layer := AnnotationLayer{
		Annotations: []Annotation{
			{ID: "keep-redact", PageID: "page-0", Type: AnnotationRedact, Rect: r, Content: "secret"},
			{ID: "keep-stamp", PageID: "page-0", Type: AnnotationStamp, Rect: r},
		},
	}
	if err := doc.ImportAnnotationLayer(layer); err != nil {
		t.Fatalf("ImportAnnotationLayer err = %v", err)
	}

	// AddAnnotation would reject these types, but import stores them as inert data.
	got, _ := doc.GetAnnotations("page-0")
	if len(got) != 2 {
		t.Fatalf("count = %d, want 2", len(got))
	}
	byType := map[AnnotationType]Annotation{}
	for _, a := range got {
		byType[a.Type] = a
	}
	if byType[AnnotationRedact].Content != "secret" {
		t.Errorf("redact record not preserved verbatim: %+v", byType[AnnotationRedact])
	}
	if _, ok := byType[AnnotationStamp]; !ok {
		t.Errorf("stamp record not preserved")
	}
}

// ids is a small helper for readable failure messages.
func ids(anns []Annotation) []AnnotationID {
	out := make([]AnnotationID, len(anns))
	for i := range anns {
		out[i] = anns[i].ID
	}
	return out
}

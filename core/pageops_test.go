package core

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// ownRotateDoc builds an opened single-page Document whose PAGE dictionary
// carries its OWN /Rotate entry (not merely an inherited one). This is the
// regression-critical shape for Rotate + SaveTo: CopyPage prefers a page's own
// /Rotate over the inherited PageInfo.Rotate (parse/copy.go:96), so a rotation
// that fails to rewrite the own-dict entry would silently save the stale angle.
func ownRotateDoc(t *testing.T, ownRotate int) *Document {
	t.Helper()
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Rotate %d /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", ownRotate),
		4: coreStreamObj("BT /F1 12 Tf 72 720 Td (Rot) Tj ET"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	pdf := buildCorePDF(objs, "<< /Size 6 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// --- Reorder ---------------------------------------------------------------

// TestReorder_HappyPath permutes a 3-page doc and asserts the new order via
// per-page ExtractText, the Index==slot invariant, and an unchanged PageCount.
func TestReorder_HappyPath(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")

	if err := doc.Reorder([]int{2, 0, 1}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	if doc.PageCount != 3 {
		t.Errorf("PageCount = %d, want 3 (permutation preserves count)", doc.PageCount)
	}
	wantOrder := []string{"Charlie", "Alpha", "Bravo"}
	if got := pageTexts(t, doc); !equalStrings(got, wantOrder) {
		t.Errorf("page order after Reorder = %v, want %v", got, wantOrder)
	}
	for i := range doc.Pages {
		if doc.Pages[i].Index != i {
			t.Errorf("Pages[%d].Index = %d, want %d (Index must equal slot)", i, doc.Pages[i].Index, i)
		}
	}
}

// TestReorder_PageIDTravelsWithPage proves Page.ID moves with its page and is
// NOT recomputed from the new position: the page that was at old index 2 keeps
// its original ID even though it now sits at index 0.
func TestReorder_PageIDTravelsWithPage(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")

	ids := make([]PageID, doc.PageCount)
	for i := range doc.Pages {
		ids[i] = doc.Pages[i].ID
	}

	if err := doc.Reorder([]int{2, 0, 1}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	// newOrder[k] is the OLD index now at slot k: {2,0,1}.
	want := []PageID{ids[2], ids[0], ids[1]}
	for k := range want {
		if doc.Pages[k].ID != want[k] {
			t.Errorf("Pages[%d].ID = %q, want %q (identity travels with the page, not the slot)",
				k, doc.Pages[k].ID, want[k])
		}
	}
}

// TestReorder_AnnotationLinkageSurvives adds an annotation to the page at index
// 2 (via its stable PageID) and asserts the annotation is still reachable by
// that same PageID after the page is moved to the front — annotations key by
// PageID, independent of position.
func TestReorder_AnnotationLinkageSurvives(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")

	targetID := doc.Pages[2].ID
	added, err := doc.AddAnnotation(Annotation{
		Type:   AnnotationHighlight,
		PageID: targetID,
		Rect:   Rect{X: 1, Y: 1, Width: 10, Height: 10},
	})
	if err != nil {
		t.Fatalf("AddAnnotation: %v", err)
	}

	if err := doc.Reorder([]int{2, 0, 1}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	anns, err := doc.GetAnnotations(targetID)
	if err != nil {
		t.Fatalf("GetAnnotations: %v", err)
	}
	if len(anns) != 1 || anns[0].ID != added.ID {
		t.Errorf("GetAnnotations(%q) = %+v, want the annotation %q to survive Reorder",
			targetID, anns, added.ID)
	}
}

// TestReorder_PersistsThroughSaveTo proves the new order is written to the saved
// bytes, not merely reflected in the in-memory struct.
func TestReorder_PersistsThroughSaveTo(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")
	if err := doc.Reorder([]int{2, 0, 1}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	reopened, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-open saved bytes: %v", err)
	}

	wantOrder := []string{"Charlie", "Alpha", "Bravo"}
	if got := pageTexts(t, reopened); !equalStrings(got, wantOrder) {
		t.Errorf("reopened page order = %v, want %v (Reorder must persist through SaveTo)", got, wantOrder)
	}
}

// TestReorder_ValidationLeavesDocUnchanged asserts each invalid argument returns
// the documented sentinel AND leaves the document byte-identical (no partial
// mutation): the parallel Pages/pageInfos arrays and PageCount are untouched.
func TestReorder_ValidationLeavesDocUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		order   []int
		wantErr error
	}{
		{"wrong length", []int{0, 1}, ErrInvalidPageOrder},
		{"duplicate index", []int{0, 0, 1}, ErrInvalidPageOrder},
		{"out of range", []int{0, 1, 3}, ErrPageNotFound},
		{"negative index", []int{-1, 0, 1}, ErrPageNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")
			before := pageTexts(t, doc)

			err := doc.Reorder(tc.order)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Reorder(%v) err = %v, want %v", tc.order, err, tc.wantErr)
			}
			if doc.PageCount != 3 {
				t.Errorf("PageCount = %d after failed Reorder, want 3 (no mutation)", doc.PageCount)
			}
			if got := pageTexts(t, doc); !equalStrings(got, before) {
				t.Errorf("page order = %v after failed Reorder, want %v (no mutation)", got, before)
			}
		})
	}
}

// TestReorder_EmptyDocNilOrder: 0 pages + nil order is a valid no-op.
func TestReorder_EmptyDocNilOrder(t *testing.T) {
	doc := &Document{PageCount: 0}
	if err := doc.Reorder(nil); err != nil {
		t.Errorf("Reorder(nil) on empty doc = %v, want nil (valid no-op)", err)
	}
	if doc.PageCount != 0 || len(doc.Pages) != 0 {
		t.Errorf("empty doc changed: PageCount=%d len(Pages)=%d, want 0/0", doc.PageCount, len(doc.Pages))
	}
}

// TestReorder_ResolverLessDoc: a directly constructed Document (no pageInfos/
// resolver) reorders its Pages without panicking.
func TestReorder_ResolverLessDoc(t *testing.T) {
	doc := newTestDoc(3)
	ids := []PageID{doc.Pages[0].ID, doc.Pages[1].ID, doc.Pages[2].ID}

	if err := doc.Reorder([]int{2, 0, 1}); err != nil {
		t.Fatalf("Reorder on resolver-less doc: %v", err)
	}
	want := []PageID{ids[2], ids[0], ids[1]}
	for k := range want {
		if doc.Pages[k].ID != want[k] {
			t.Errorf("Pages[%d].ID = %q, want %q", k, doc.Pages[k].ID, want[k])
		}
		if doc.Pages[k].Index != k {
			t.Errorf("Pages[%d].Index = %d, want %d", k, doc.Pages[k].Index, k)
		}
	}
}

// --- Rotate ----------------------------------------------------------------

// TestRotate_RelativeAccumulation proves Rotate is additive and normalized:
// repeated quarter turns wrap 0→90→180→270→0, a negative turn backs off, and a
// large value normalizes.
func TestRotate_RelativeAccumulation(t *testing.T) {
	t.Run("wrap forward", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		want := []int{90, 180, 270, 0}
		for step, w := range want {
			if err := doc.Rotate(0, 90); err != nil {
				t.Fatalf("Rotate step %d: %v", step, err)
			}
			if got := doc.Pages[0].Rotation; got != w {
				t.Errorf("after %d x Rotate(90): Rotation = %d, want %d", step+1, got, w)
			}
		}
	})

	t.Run("negative from zero", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		if err := doc.Rotate(0, -90); err != nil {
			t.Fatalf("Rotate(-90): %v", err)
		}
		if got := doc.Pages[0].Rotation; got != 270 {
			t.Errorf("Rotate(-90) from 0: Rotation = %d, want 270", got)
		}
	})

	t.Run("large value normalizes", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		if err := doc.Rotate(0, 450); err != nil {
			t.Fatalf("Rotate(450): %v", err)
		}
		if got := doc.Pages[0].Rotation; got != 90 {
			t.Errorf("Rotate(450) from 0: Rotation = %d, want 90", got)
		}
	})
}

// TestRotate_BadInputNoMutation: out-of-range page → ErrPageNotFound; a non-90
// angle → ErrInvalidPageOrder, and the page's rotation is untouched.
func TestRotate_BadInputNoMutation(t *testing.T) {
	t.Run("page out of range", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		if err := doc.Rotate(5, 90); !errors.Is(err, ErrPageNotFound) {
			t.Errorf("Rotate(5,90) err = %v, want ErrPageNotFound", err)
		}
	})

	t.Run("negative page out of range", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		if err := doc.Rotate(-1, 90); !errors.Is(err, ErrPageNotFound) {
			t.Errorf("Rotate(-1,90) err = %v, want ErrPageNotFound", err)
		}
	})

	t.Run("degrees not multiple of 90", func(t *testing.T) {
		doc := multiPageDoc(t, "Alpha")
		if err := doc.Rotate(0, 90); err != nil { // establish a non-zero baseline
			t.Fatalf("baseline Rotate: %v", err)
		}
		before := doc.Pages[0].Rotation
		if err := doc.Rotate(0, 45); !errors.Is(err, ErrInvalidPageOrder) {
			t.Errorf("Rotate(0,45) err = %v, want ErrInvalidPageOrder", err)
		}
		if got := doc.Pages[0].Rotation; got != before {
			t.Errorf("Rotation = %d after rejected Rotate(45), want %d (no mutation)", got, before)
		}
	})
}

// TestRotate_PersistsThroughSaveTo_OwnRotate is the most important Rotate test.
// The page dict already carries its OWN /Rotate 90; Rotate(90) must yield 180
// AND rewrite the own-dict entry so SaveTo emits 180 (CopyPage prefers the own
// /Rotate over the inherited PageInfo.Rotate). Without the pi.Dict["Rotate"]
// write, SaveTo would copy the stale own /Rotate=90 and the reopened page would
// read 90 — see the report's failure analysis.
func TestRotate_PersistsThroughSaveTo_OwnRotate(t *testing.T) {
	doc := ownRotateDoc(t, 90)
	if got := doc.Pages[0].Rotation; got != 90 {
		t.Fatalf("baseline Rotation = %d, want 90 (own /Rotate on open)", got)
	}

	if err := doc.Rotate(0, 90); err != nil {
		t.Fatalf("Rotate(0,90): %v", err)
	}
	if got := doc.Pages[0].Rotation; got != 180 {
		t.Fatalf("Rotation = %d after Rotate(90) on a 90 page, want 180", got)
	}

	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	reopened, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-open saved bytes: %v", err)
	}
	if got := reopened.Pages[0].Rotation; got != 180 {
		t.Errorf("reopened Rotation = %d, want 180 (own /Rotate dict entry must be rewritten before save)", got)
	}
}

// TestRotate_PersistsThroughSaveTo_InheritedRotate covers the other CopyPage
// branch: a page with no own /Rotate persists via the inherited PageInfo.Rotate
// fallback.
func TestRotate_PersistsThroughSaveTo_InheritedRotate(t *testing.T) {
	doc := multiPageDoc(t, "Alpha") // no own /Rotate
	if err := doc.Rotate(0, 270); err != nil {
		t.Fatalf("Rotate(0,270): %v", err)
	}

	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	reopened, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got := reopened.Pages[0].Rotation; got != 270 {
		t.Errorf("reopened Rotation = %d, want 270 (rotation must persist through SaveTo)", got)
	}
}

// TestRotate_ShallowCloneDoesNotCorruptCache proves the write to the page's own
// /Rotate goes to a shallow CLONE, not to the resolver's cached page dict: the
// map instance captured before Rotate must be unchanged afterward, while the new
// pageInfos entry carries the new angle. If Rotate mutated pi.Dict in place, the
// cached map would show the new value and any later resolve of this page object
// would be corrupted.
func TestRotate_ShallowCloneDoesNotCorruptCache(t *testing.T) {
	doc := ownRotateDoc(t, 90)

	cached := doc.pageInfos[0].Dict // the resolver's cached page dict
	if cached["Rotate"] != parse.Integer(90) {
		t.Fatalf("cached /Rotate = %v, want Integer(90) before Rotate", cached["Rotate"])
	}

	if err := doc.Rotate(0, 90); err != nil {
		t.Fatalf("Rotate(0,90): %v", err)
	}

	if cached["Rotate"] != parse.Integer(90) {
		t.Errorf("cached /Rotate = %v after Rotate, want Integer(90) unchanged (in-place mutation corrupts the resolver cache)", cached["Rotate"])
	}
	if got := doc.pageInfos[0].Dict["Rotate"]; got != parse.Integer(180) {
		t.Errorf("pageInfos[0] /Rotate = %v, want Integer(180) on the clone", got)
	}
	// The cache still reading 90 (above) while the live dict reads 180 already
	// proves Rotate wrote a distinct clone rather than mutating the cached map.
}

// TestRotate_NoBleedBetweenPages proves rotating page i does not alias or leak
// into page j: after Rotate(0,90), rotating page 1 reads its OWN current angle.
func TestRotate_NoBleedBetweenPages(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")

	if err := doc.Rotate(0, 90); err != nil {
		t.Fatalf("Rotate(0,90): %v", err)
	}
	if err := doc.Rotate(1, 180); err != nil {
		t.Fatalf("Rotate(1,180): %v", err)
	}

	if got := doc.Pages[0].Rotation; got != 90 {
		t.Errorf("Pages[0].Rotation = %d, want 90", got)
	}
	if got := doc.Pages[1].Rotation; got != 180 {
		t.Errorf("Pages[1].Rotation = %d, want 180 (computed from page 1's own 0, no bleed from page 0)", got)
	}
	if got := doc.Pages[2].Rotation; got != 0 {
		t.Errorf("Pages[2].Rotation = %d, want 0 (untouched)", got)
	}
}

// TestRotate_ResolverLessDoc: a directly constructed Document (no pageInfos)
// updates Page.Rotation without panicking, using the exported field as the
// current-angle source.
func TestRotate_ResolverLessDoc(t *testing.T) {
	doc := newTestDoc(2)
	if err := doc.Rotate(1, 90); err != nil {
		t.Fatalf("Rotate on resolver-less doc: %v", err)
	}
	if got := doc.Pages[1].Rotation; got != 90 {
		t.Errorf("Pages[1].Rotation = %d, want 90", got)
	}
	// Accumulation still works off the exported field.
	if err := doc.Rotate(1, 90); err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	if got := doc.Pages[1].Rotation; got != 180 {
		t.Errorf("Pages[1].Rotation = %d, want 180 (accumulation via Page.Rotation)", got)
	}
	if got := doc.Pages[0].Rotation; got != 0 {
		t.Errorf("Pages[0].Rotation = %d, want 0 (untouched)", got)
	}
}

// TestNormalizeDegrees checks the {0,90,180,270} mapping over negatives, wraps,
// and large magnitudes.
func TestNormalizeDegrees(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},
		{90, 90},
		{180, 180},
		{270, 270},
		{360, 0},
		{450, 90},
		{-90, 270},
		{-180, 180},
		{-450, 270},
		{720, 0},
	}
	for _, tc := range cases {
		if got := normalizeDegrees(tc.in); got != tc.want {
			t.Errorf("normalizeDegrees(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

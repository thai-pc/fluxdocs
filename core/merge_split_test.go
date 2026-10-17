package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
)

// multiPageDoc builds an opened Document with one page per text, each page a
// distinct content stream showing that text (so ExtractText per page is a
// recognizable, order-sensitive fingerprint). A single Type1 font is shared by
// all pages.
func multiPageDoc(t *testing.T, texts ...string) *Document {
	t.Helper()
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		3: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	kids := ""
	num := 4
	for _, txt := range texts {
		pageObj, contentObj := num, num+1
		num += 2
		kids += fmt.Sprintf("%d 0 R ", pageObj)
		objs[pageObj] = fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObj)
		objs[contentObj] = coreStreamObj(fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", txt))
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(texts))

	pdf := buildCorePDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", num))
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

// pageTexts extracts each page's text into a slice, for order-sensitive compares.
func pageTexts(t *testing.T, d *Document) []string {
	t.Helper()
	out := make([]string, d.PageCount)
	for i := 0; i < d.PageCount; i++ {
		txt, err := d.ExtractTextByPage(i, ExtractOptions{})
		if err != nil {
			t.Fatalf("ExtractTextByPage(%d): %v", i, err)
		}
		out[i] = txt
	}
	return out
}

func TestSaveTo_RoundTrip(t *testing.T) {
	doc := multiPageDoc(t, "Alpha", "Bravo", "Charlie")
	want := pageTexts(t, doc)

	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	reopened, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-open saved bytes: %v", err)
	}
	if reopened.PageCount != 3 {
		t.Fatalf("re-opened PageCount = %d, want 3", reopened.PageCount)
	}
	got := pageTexts(t, reopened)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("page %d text after round-trip = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMerge_OrderAndImmutability(t *testing.T) {
	a := multiPageDoc(t, "A-zero", "A-one")
	b := multiPageDoc(t, "B-zero")

	merged, err := a.Merge(b)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.PageCount != 3 {
		t.Fatalf("merged PageCount = %d, want 3", merged.PageCount)
	}
	wantOrder := []string{"A-zero", "A-one", "B-zero"}
	if got := pageTexts(t, merged); !equalStrings(got, wantOrder) {
		t.Errorf("merged page order = %v, want %v", got, wantOrder)
	}

	// Immutability: A and B must be untouched by Merge.
	if a.PageCount != 2 {
		t.Errorf("source A PageCount = %d after Merge, want 2 (mutated)", a.PageCount)
	}
	if got := pageTexts(t, a); !equalStrings(got, []string{"A-zero", "A-one"}) {
		t.Errorf("source A text = %v after Merge, want [A-zero A-one] (mutated)", got)
	}
	if b.PageCount != 1 {
		t.Errorf("source B PageCount = %d after Merge, want 1 (mutated)", b.PageCount)
	}
	if got := pageTexts(t, b); !equalStrings(got, []string{"B-zero"}) {
		t.Errorf("source B text = %v after Merge, want [B-zero] (mutated)", got)
	}
}

func TestSplit_RangesAndContent(t *testing.T) {
	doc := multiPageDoc(t, "p0", "p1", "p2", "p3")

	parts, err := doc.Split([][2]int{{0, 1}, {3, 3}})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(parts))
	}
	if parts[0].PageCount != 2 {
		t.Errorf("parts[0].PageCount = %d, want 2", parts[0].PageCount)
	}
	if got := pageTexts(t, parts[0]); !equalStrings(got, []string{"p0", "p1"}) {
		t.Errorf("parts[0] text = %v, want [p0 p1]", got)
	}
	if parts[1].PageCount != 1 {
		t.Errorf("parts[1].PageCount = %d, want 1", parts[1].PageCount)
	}
	if got := pageTexts(t, parts[1]); !equalStrings(got, []string{"p3"}) {
		t.Errorf("parts[1] text = %v, want [p3]", got)
	}

	// Source is unchanged.
	if got := pageTexts(t, doc); !equalStrings(got, []string{"p0", "p1", "p2", "p3"}) {
		t.Errorf("source doc text = %v after Split, want [p0 p1 p2 p3]", got)
	}
}

func TestSplit_InvalidRanges(t *testing.T) {
	doc := multiPageDoc(t, "p0", "p1", "p2")

	cases := map[string][][2]int{
		"end out of range": {{0, 9}},
		"start > end":      {{2, 1}},
		"negative start":   {{-1, 0}},
	}
	for name, ranges := range cases {
		if _, err := doc.Split(ranges); !errors.Is(err, ErrPageNotFound) {
			t.Errorf("Split(%s) err = %v, want ErrPageNotFound", name, err)
		}
	}
}

func TestSplit_EmptyRanges(t *testing.T) {
	doc := multiPageDoc(t, "p0")
	parts, err := doc.Split(nil)
	if err != nil {
		t.Errorf("Split(nil) err = %v, want nil", err)
	}
	if len(parts) != 0 {
		t.Errorf("Split(nil) returned %d docs, want 0", len(parts))
	}
}

// TestSaveTo_OmitsAnnotationLayer proves the FluxDocs annotation JSON layer is
// NOT baked into the saved bytes (annotations are a separate layer, applied only
// on flatten). A saved-then-reopened document must carry no annotations.
func TestSaveTo_OmitsAnnotationLayer(t *testing.T) {
	doc := multiPageDoc(t, "p0")
	if _, err := doc.AddAnnotation(Annotation{
		Type:   AnnotationHighlight,
		PageID: "page-0",
		Rect:   Rect{X: 0, Y: 0, Width: 10, Height: 10},
	}); err != nil {
		t.Fatalf("AddAnnotation: %v", err)
	}

	var buf bytes.Buffer
	if err := doc.SaveTo(&buf); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	reopened, err := OpenBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	anns, err := reopened.GetAnnotations("page-0")
	if err != nil {
		t.Fatalf("GetAnnotations: %v", err)
	}
	if len(anns) != 0 {
		t.Errorf("re-opened doc has %d annotations, want 0 (layer must not be baked in)", len(anns))
	}
}

// TestResolverLessDocument_Errors: a Document with no resolver (constructed
// directly, never opened) cannot be serialized/merged.
func TestResolverLessDocument_Errors(t *testing.T) {
	bare := &Document{}
	if err := bare.SaveTo(io.Discard); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("SaveTo on resolver-less doc = %v, want ErrInvalidPDF", err)
	}

	opened := multiPageDoc(t, "p0")
	if _, err := bare.Merge(opened); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("Merge with resolver-less receiver = %v, want ErrInvalidPDF", err)
	}
	if _, err := opened.Merge(bare); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("Merge with resolver-less argument = %v, want ErrInvalidPDF", err)
	}
	if _, err := opened.Merge(nil); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("Merge(nil) = %v, want ErrInvalidPDF", err)
	}
	if _, err := bare.Split([][2]int{{0, 0}}); !errors.Is(err, ErrInvalidPDF) {
		t.Errorf("Split on resolver-less doc = %v, want ErrInvalidPDF", err)
	}
}

// TestConcurrentSaveAndRead runs SaveTo alongside concurrent GetAnnotations and
// RenderPage on the same Document under -race. SaveTo copies the source object
// graph through resolver.Resolve while RenderPage resolves objects for drawing;
// both go through Resolver.object()'s mutex, so there must be no data race.
func TestConcurrentSaveAndRead(t *testing.T) {
	doc := multiPageDoc(t, "p0", "p1", "p2")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := doc.SaveTo(io.Discard); err != nil {
				t.Errorf("SaveTo: %v", err)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := doc.GetAnnotations("page-0"); err != nil {
				t.Errorf("GetAnnotations: %v", err)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := doc.RenderPage(0, RenderOptions{DPI: 72, Format: "png"}); err != nil {
				t.Errorf("RenderPage: %v", err)
			}
		}()
	}
	wg.Wait()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSplit_PageReferencesDoNotDragInPages: a split-off page whose link
// points at another page does not carry that page (its content, annotations,
// or the original page tree) into the output: the destination becomes null
// (decision D11, docs/design/redaction-hardening.md §5.5). In a merge the
// link points at the page's copy.
func TestSplit_PageReferencesDoNotDragInPages(t *testing.T) {
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [4 0 R 6 0 R] /Count 2 >>",
		3: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
		5: coreStreamObj("BT /F1 12 Tf 72 720 Td (PRIVATE-PAGE-ONE) Tj ET"),
		6: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 7 0 R /Annots [8 0 R] >>",
		7: coreStreamObj("BT /F1 12 Tf 72 720 Td (Page two) Tj ET"),
		8: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [4 0 R /Fit] /P 6 0 R >>",
	}, "<< /Size 9 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := doc.Split([][2]int{{1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := parts[0].Bytes()
	if bytes.Contains(raw, []byte("PRIVATE-PAGE-ONE")) {
		t.Error("split-off page 1 carries page 0's content through its link")
	}
	if n := bytes.Count(raw, []byte("/Type /Pages")); n != 1 {
		t.Errorf("%d page-tree nodes in the split output, want 1", n)
	}

	merged, err := doc.Merge(doc)
	if err != nil {
		t.Fatal(err)
	}
	if merged.PageCount != 4 {
		t.Fatalf("merged %d pages, want 4", merged.PageCount)
	}
	raw, _ = merged.Bytes()
	if n := bytes.Count(raw, []byte("PRIVATE-PAGE-ONE")); n != 1 {
		t.Errorf("page 0's content written %d times in Merge(d, d), want once (shared stream)", n)
	}
}

// TestSplit_OrphanPagesAndForeignAnnotations: a link to a page object that is
// not in the page tree, and a reply (/IRT) to an annotation of a page outside
// the build, do not carry that page's content or annotation into the output
// (D11; batch C review S1, N5).
func TestSplit_OrphanPagesAndForeignAnnotations(t *testing.T) {
	pdf := buildCorePDF(map[int]string{
		1:  "<< /Type /Catalog /Pages 2 0 R >>",
		2:  "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		3:  "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Annots [7 0 R] >>",
		4:  coreStreamObj("BT /F1 12 Tf 72 720 Td (PAGE-ONE) Tj ET"),
		5:  "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Annots [8 0 R 9 0 R] >>",
		6:  coreStreamObj("BT /F1 12 Tf 72 720 Td (PAGE-TWO) Tj ET"),
		7:  "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (ANNOT-OF-PAGE-ONE) /P 3 0 R >>",
		8:  "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (reply) /IRT 7 0 R /P 5 0 R >>",
		9:  "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [10 0 R /Fit] >>",
		10: "<< /Type /Page /MediaBox [0 0 612 792] /Contents 11 0 R >>",
		11: coreStreamObj("BT /F1 12 Tf 72 720 Td (ORPHAN-PAGE) Tj ET"),
	}, "<< /Size 12 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := doc.Split([][2]int{{1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := parts[0].Bytes()
	for _, s := range []string{"ORPHAN-PAGE", "ANNOT-OF-PAGE-ONE", "PAGE-ONE"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("split-off page 1 carries %s", s)
		}
	}
	if !bytes.Contains(raw, []byte("reply")) {
		t.Error("page 1's own annotation was lost")
	}
	// Both pages: the reply keeps its /IRT.
	all, err := doc.Split([][2]int{{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = all[0].Bytes()
	if n := bytes.Count(raw, []byte("ANNOT-OF-PAGE-ONE")); n != 1 {
		t.Errorf("page 0's annotation written %d times, want once", n)
	}
}

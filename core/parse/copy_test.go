package parse

import (
	"bytes"
	"testing"
)

// TestBuildDocument_CycleTerminates is the critical security pin for the
// reserve-before-recurse copier. The source page's /Resources references an
// object that references itself, so the object closure copied by CopyPage
// contains a cycle.
//
// copyRef claims a new object number and stores it in the visited map BEFORE it
// recurses into the resolved body. When the recursion reaches the self-reference
// again it finds the reserved entry in visited and returns immediately, breaking
// the cycle. Without that ordering, copyRef(4) would resolve object 4, recurse
// into its /Self reference, call copyRef(4) again with an empty visited entry,
// and recurse forever — the test would hang (or blow the stack). The
// maxCopyDepth guard is a second backstop. This test simply completing proves
// termination.
func TestBuildDocument_CycleTerminates(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources 4 0 R /Contents 5 0 R >>",
		4: "<< /Kind /Res /Self 4 0 R >>", // object references itself -> cycle
		5: "<< /Length 4 >>\nstream\nnoop\nendstream",
	}, "<< /Size 6 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}

	built, err := BuildDocument([]PageSource{{Resolver: r, Page: pages[0]}}, nil)
	if err != nil {
		t.Fatalf("BuildDocument over a cyclic closure = %v, want nil (must terminate)", err)
	}

	// The output must itself be a valid, re-openable PDF with the page preserved.
	r2, err := NewResolver(built)
	if err != nil {
		t.Fatalf("re-open built PDF: %v", err)
	}
	pages2, err := r2.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages2) != 1 {
		t.Errorf("re-opened page count = %d, want 1", len(pages2))
	}
}

// TestBuildDocument_SharedObjectDedup proves the visited map deduplicates a
// shared object: two pages that reference the SAME font object must copy it once.
// After the copy, both pages' /Font /F1 must resolve to the same new object
// number (a doubled copy would give two distinct numbers).
func TestBuildDocument_SharedObjectDedup(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>",
		4: "<< /Length 4 >>\nstream\nnoop\nendstream",
		5: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 6 0 R >> >> /Contents 7 0 R >>",
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", // shared by both pages
		7: "<< /Length 4 >>\nstream\nnoop\nendstream",
	}, "<< /Size 8 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d, want 2", len(pages))
	}

	// BuildDocument uses one Copier per source Resolver, so both pages from r
	// share a visited map and dedup the font.
	built, err := BuildDocument([]PageSource{
		{Resolver: r, Page: pages[0]},
		{Resolver: r, Page: pages[1]},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	r2, err := NewResolver(built)
	if err != nil {
		t.Fatal(err)
	}
	pages2, err := r2.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages2) != 2 {
		t.Fatalf("re-opened page count = %d, want 2", len(pages2))
	}

	fontRef := func(pi PageInfo) Reference {
		res, ok := pi.Dict.GetDict("Resources")
		if !ok {
			t.Fatalf("page missing /Resources dict: %v", pi.Dict)
		}
		fonts, ok := res.GetDict("Font")
		if !ok {
			t.Fatalf("page /Resources missing /Font: %v", res)
		}
		ref, ok := fonts.GetReference("F1")
		if !ok {
			t.Fatalf("/Font /F1 is not a reference: %v", fonts["F1"])
		}
		return ref
	}

	f0, f1 := fontRef(pages2[0]), fontRef(pages2[1])
	if f0.Number != f1.Number {
		t.Errorf("shared font copied twice: page0 /F1 = obj %d, page1 /F1 = obj %d (want equal)", f0.Number, f1.Number)
	}

	// And that single copied object must actually be the font dict.
	fd, ok, err := r2.ResolveDict(f0)
	if err != nil || !ok {
		t.Fatalf("resolve copied font: ok=%v err=%v", ok, err)
	}
	if bf, _ := fd.GetName("BaseFont"); bf != "Helvetica" {
		t.Errorf("copied font /BaseFont = %q, want Helvetica", bf)
	}
}

// TestBuildDocument_ReplaceResources: a page built with ReplaceResources gets
// those resources, and objects reachable only through its original
// /Resources (indirect or inherited) are not written at all — redaction uses
// it to drop a stripped property list's original from the output.
func TestBuildDocument_ReplaceResources(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources 4 0 R /Contents 5 0 R >>",
		4: "<< /Font << /F1 6 0 R >> /Properties << /MC0 7 0 R >> >>",
		5: "<< /Length 4 >>\nstream\nnoop\nendstream",
		6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		7: "<< /ActualText (ORIGINAL-ONLY) >>",
	}, "<< /Size 8 /Root 1 0 R >>")
	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	replaced := Dict{"Font": Dict{"F1": Reference{Number: 6}}, "Properties": Dict{"MC0": Dict{"MCID": Integer(1)}}}
	built, err := BuildDocument([]PageSource{{Resolver: r, Page: pages[0], ReplaceResources: replaced}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(built, []byte("ORIGINAL-ONLY")) {
		t.Error("an object reachable only through the replaced /Resources was written")
	}
	r2, err := NewResolver(built)
	if err != nil {
		t.Fatal(err)
	}
	pages2, err := r2.Pages()
	if err != nil || len(pages2) != 1 {
		t.Fatalf("re-open: %d pages, %v", len(pages2), err)
	}
	font, ok, err := r2.ResolveDict(pages2[0].Resources["Font"])
	if err != nil || !ok {
		t.Fatalf("/Font = %v, %v", font, err)
	}
	if f, ok, _ := r2.ResolveDict(font["F1"]); !ok || f["BaseFont"] != Name("Helvetica") {
		t.Errorf("/F1 = %v, want the copied font", f)
	}
	props, _, _ := r2.ResolveDict(pages2[0].Resources["Properties"])
	if mc, _, _ := r2.ResolveDict(props["MC0"]); mc["MCID"] != Integer(1) {
		t.Errorf("/Properties /MC0 = %v, want the replacement", mc)
	}
}

// pagesFixture is a three-page document: pages 3, 6 and 9 with contents 4, 7
// and 10, Helvetica 5. Page 3 has a Text annotation 11 whose /P points back at
// it, and a Link 12 whose /Dest is page 9; page 6 has a widget 13 of field 14,
// whose other widget 15 sits on page 3; page 3 also carries /Thumb 16, /PieceInfo
// and /Metadata 17.
func pagesFixture(t *testing.T) (*Resolver, []PageInfo) {
	t.Helper()
	stream := func(s string) string {
		return "<< /Length " + itoa(len(s)) + " >>\nstream\n" + s + "\nendstream"
	}
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 6 0 R 9 0 R] /Count 3 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R " +
			"/Annots [11 0 R 12 0 R 15 0 R] /Thumb 16 0 R /PieceInfo << /X << /Private (PIECE-SECRET) >> >> /Metadata 17 0 R /CropBox [0 0 90 90] >>",
		4:  stream("BT /F1 9 Tf (PAGE-ONE) Tj ET"),
		5:  "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6:  "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 7 0 R /Annots [13 0 R] >>",
		7:  stream("BT /F1 9 Tf (PAGE-TWO) Tj ET"),
		9:  "<< /Type /Page /Parent 2 0 R /Contents 10 0 R >>",
		10: stream("BT /F1 9 Tf (PAGE-THREE) Tj ET"),
		11: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /P 3 0 R /Contents (NOTE-SECRET) >>",
		12: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [9 0 R /Fit] >>",
		13: "<< /Type /Annot /Subtype /Widget /Rect [0 0 10 10] /Parent 14 0 R /P 6 0 R >>",
		14: "<< /FT /Tx /T (f) /Kids [13 0 R 15 0 R] >>",
		15: "<< /Type /Annot /Subtype /Widget /Rect [0 0 10 10] /Parent 14 0 R /P 3 0 R /AP << /N 18 0 R >> >>",
		16: stream("THUMB-SECRET"),
		17: stream("XMP-SECRET"),
		18: stream("BT (WIDGET-SECRET) Tj ET"),
	}, "<< /Size 19 /Root 1 0 R >>")
	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil || len(pages) != 3 {
		t.Fatalf("pages: %d, %v", len(pages), err)
	}
	return r, pages
}

func itoa(n int) string { return string(SerializeObject(Integer(int64(n)))) }

// pageNumbers returns the object numbers of the output's leaf pages, in order.
func pageNumbers(t *testing.T, built []byte) (*Resolver, []int) {
	t.Helper()
	r, err := NewResolver(built)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := r.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	pages, _, _ := r.ResolveDict(cat["Pages"])
	kids, _, _ := r.resolveArray(pages["Kids"])
	var nums []int
	for _, k := range kids {
		nums = append(nums, k.(Reference).Number)
	}
	return r, nums
}

// TestBuildDocument_PageReferences: references to a source page from inside a
// copied closure (an annotation's /P, a link's /Dest) point at that page's
// copy when it is part of the build, and are null when it is not; the
// original page and the original page tree are never copied (decision D11).
func TestBuildDocument_PageReferences(t *testing.T) {
	r, pages := pagesFixture(t)

	// Split off page 0: its annotation's /P is itself, its link's /Dest a
	// page that is not in the output.
	built, err := BuildDocument([]PageSource{{Resolver: r, Page: pages[0]}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(built, []byte("/Type /Page ")) + bytes.Count(built, []byte("/Type /Page>")); n != 1 {
		t.Errorf("%d page objects written, want 1 (no copy of the original pages)", n)
	}
	for _, other := range []string{"PAGE-TWO", "PAGE-THREE"} {
		if bytes.Contains(built, []byte(other)) {
			t.Errorf("content of a page outside the build (%s) was copied", other)
		}
	}
	out, nums := pageNumbers(t, built)
	page, _, _ := out.ResolveDict(Reference{Number: nums[0]})
	annots, _, _ := out.resolveArray(page["Annots"])
	note, _, _ := out.ResolveDict(annots[0])
	if p, ok := note["P"].(Reference); !ok || p.Number != nums[0] {
		t.Errorf("annotation /P = %v, want the page's copy %d", note["P"], nums[0])
	}
	link, _, _ := out.ResolveDict(annots[1])
	if dest, _, _ := out.resolveArray(link["Dest"]); len(dest) != 2 || dest[0] != (Null{}) {
		t.Errorf("link /Dest = %v, want [null /Fit] (page not in the output)", dest)
	}

	// All three pages, in reverse order: references resolve to the copies
	// whichever is copied first.
	built, err = BuildDocument([]PageSource{{Resolver: r, Page: pages[2]}, {Resolver: r, Page: pages[1]}, {Resolver: r, Page: pages[0]}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, nums = pageNumbers(t, built)
	page, _, _ = out.ResolveDict(Reference{Number: nums[2]})
	annots, _, _ = out.resolveArray(page["Annots"])
	link, _, _ = out.ResolveDict(annots[1])
	if dest, _, _ := out.resolveArray(link["Dest"]); len(dest) != 2 || dest[0] != (Reference{Number: nums[0]}) {
		t.Errorf("link /Dest = %v, want [%d 0 R /Fit]", dest, nums[0])
	}
	if n := bytes.Count(built, []byte("/Type /Pages")); n != 1 {
		t.Errorf("%d page-tree nodes written, want 1", n)
	}
}

// TestBuildDocument_Redacted: a Redacted page keeps only the entries that
// paint it, and its annotations are written nowhere — not through another
// page's widget field either.
func TestBuildDocument_Redacted(t *testing.T) {
	r, pages := pagesFixture(t)
	built, err := BuildDocument([]PageSource{
		{Resolver: r, Page: pages[0], Redacted: true, ReplaceContent: []byte("BT ET")},
		{Resolver: r, Page: pages[1]},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"PAGE-ONE", "NOTE-SECRET", "PIECE-SECRET", "THUMB-SECRET", "XMP-SECRET", "WIDGET-SECRET"} {
		if bytes.Contains(built, []byte(s)) {
			t.Errorf("%s written for a Redacted page", s)
		}
	}
	out, nums := pageNumbers(t, built)
	page, _, _ := out.ResolveDict(Reference{Number: nums[0]})
	for k := range page {
		if !redactedPageKeys[k] && k != "Parent" {
			t.Errorf("Redacted page keeps /%s", k)
		}
	}
	if _, ok := page["CropBox"]; !ok {
		t.Error("Redacted page lost its /CropBox")
	}
	// Page 1's widget keeps its field, whose /Kids lose page 0's widget.
	other, _, _ := out.ResolveDict(Reference{Number: nums[1]})
	annots, _, _ := out.resolveArray(other["Annots"])
	widget, _, _ := out.ResolveDict(annots[0])
	field, _, _ := out.ResolveDict(widget["Parent"])
	if kids, _, _ := out.resolveArray(field["Kids"]); len(kids) != 1 || kids[0] != annots[0] {
		t.Errorf("field /Kids = %v, want only page 1's widget %v", kids, annots[0])
	}
}

// TestBuildDocument_RedactedDirectAnnotationPopup: a Redacted page whose
// /Annots holds an annotation dictionary directly still denies its /Popup,
// which another page's annotation reaches (batch C review N4).
func TestBuildDocument_RedactedDirectAnnotationPopup(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [<< /Subtype /Text /Rect [0 0 1 1] /Popup 5 0 R >>] >>",
		4: "<< /Length 0 >>\nstream\n\nendstream",
		5: "<< /Type /Annot /Subtype /Popup /Rect [0 0 1 1] /Contents (POPUP-SECRET) >>",
		6: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Annots [7 0 R] >>",
		7: "<< /Type /Annot /Subtype /Text /Rect [0 0 1 1] /Popup 5 0 R >>",
	}, "<< /Size 8 /Root 1 0 R >>")
	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	built, err := BuildDocument([]PageSource{
		{Resolver: r, Page: pages[0], Redacted: true, ReplaceContent: []byte{}},
		{Resolver: r, Page: pages[1]},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(built, []byte("POPUP-SECRET")) {
		t.Error("the Redacted page's popup was written through another page's annotation")
	}
}

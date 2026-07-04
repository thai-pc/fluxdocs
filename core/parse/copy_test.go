package parse

import "testing"

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

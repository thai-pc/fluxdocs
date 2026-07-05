package core

import (
	"fmt"
	"testing"
)

// docWithText builds a document whose pages show the given text strings.
func docWithText(t *testing.T, pageTexts ...string) *Document {
	t.Helper()
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	n := len(pageTexts)
	fontObj := 3 + 2*n
	objs[fontObj] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	kids := ""
	obj := 3
	for _, text := range pageTexts {
		p, c := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", p)
		objs[p] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, c)
		objs[c] = coreStreamObj(fmt.Sprintf("BT /F1 12 Tf 20 200 Td (%s) Tj ET", text))
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	pdf := buildCorePDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", fontObj+1))
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func hasSegment(pd PageDiff, op DiffOp, text string) bool {
	for _, s := range pd.Segments {
		if s.Op == op && s.Text == text {
			return true
		}
	}
	return false
}

func TestCompare_Identical(t *testing.T) {
	a := docWithText(t, "the quick brown fox", "second page")
	b := docWithText(t, "the quick brown fox", "second page")
	res, err := a.Compare(b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Identical {
		t.Errorf("identical docs reported as different: %+v", res.Pages)
	}
	for _, pd := range res.Pages {
		if !pd.Identical {
			t.Errorf("page %d marked changed: %+v", pd.PageIndex, pd.Segments)
		}
	}
}

func TestCompare_WordChange(t *testing.T) {
	a := docWithText(t, "the cat sat")
	b := docWithText(t, "the dog sat")
	res, err := a.Compare(b)
	if err != nil {
		t.Fatal(err)
	}
	if res.Identical {
		t.Fatal("changed docs reported as identical")
	}
	pd := res.Pages[0]
	if pd.Identical {
		t.Fatal("page 0 should be marked changed")
	}
	if !hasSegment(pd, DiffDelete, "cat") || !hasSegment(pd, DiffInsert, "dog") {
		t.Errorf("expected -cat +dog, got %+v", pd.Segments)
	}
	if !hasSegment(pd, DiffEqual, "the") {
		t.Errorf("expected =the retained, got %+v", pd.Segments)
	}
}

func TestCompare_PageCountMismatch(t *testing.T) {
	a := docWithText(t, "page one")
	b := docWithText(t, "page one", "extra page")
	res, err := a.Compare(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 2 {
		t.Fatalf("got %d page diffs, want 2", len(res.Pages))
	}
	if res.Pages[0].Identical != true {
		t.Errorf("page 0 should be identical")
	}
	// Page 1 exists only in b -> all inserted.
	if res.Pages[1].Identical || !hasSegment(res.Pages[1], DiffInsert, "extra page") {
		t.Errorf("page 1 should be all-inserted, got %+v", res.Pages[1].Segments)
	}
}

func TestCompare_ResolverLess(t *testing.T) {
	a := docWithText(t, "x")
	if _, err := a.Compare(nil); err != ErrInvalidPDF {
		t.Errorf("Compare(nil) err = %v, want ErrInvalidPDF", err)
	}
	if _, err := (&Document{}).Compare(a); err != ErrInvalidPDF {
		t.Errorf("resolver-less receiver err = %v, want ErrInvalidPDF", err)
	}
}

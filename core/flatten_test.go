package core

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// flattenPageContent returns the decoded, concatenated content stream of one
// page of doc (white-box: reaches the parse handles the Document holds).
func flattenPageContent(t *testing.T, doc *Document, page int) string {
	t.Helper()
	b, err := doc.resolver.PageContent(doc.pageInfos[page])
	if err != nil {
		t.Fatalf("PageContent: %v", err)
	}
	return string(b)
}

func TestFlattenAnnotations_BakesIntoContent(t *testing.T) {
	doc := blankPageDoc(t)
	if _, err := doc.AddAnnotation(Annotation{
		PageID: doc.Pages[0].ID, Type: AnnotationHighlight,
		Rect: Rect{X: 20, Y: 20, Width: 60, Height: 60}, Color: "#000000", Opacity: 1,
	}); err != nil {
		t.Fatal(err)
	}

	flat, err := doc.FlattenAnnotations()
	if err != nil {
		t.Fatalf("FlattenAnnotations: %v", err)
	}

	// The baked content stream carries the fill operators.
	content := flattenPageContent(t, flat, 0)
	if !strings.Contains(content, "re f") {
		t.Errorf("flattened content lacks a fill; got:\n%s", content)
	}

	// The flattened document's own annotation layer is empty (baked in).
	anns, _ := flat.GetAnnotations(flat.Pages[0].ID)
	if len(anns) != 0 {
		t.Errorf("flattened doc has %d annotations, want 0 (baked into content)", len(anns))
	}

	// The source document is unchanged — it still holds the annotation.
	srcAnns, _ := doc.GetAnnotations(doc.Pages[0].ID)
	if len(srcAnns) != 1 {
		t.Errorf("source doc has %d annotations after flatten, want 1 (unchanged)", len(srcAnns))
	}

	// Rendering the flattened page (bare, no layer) shows the baked mark.
	img := renderFlatPage(t, flat, RenderOptions{DPI: 72, Format: "png", SkipAnnotations: true})
	if coreNonWhite(img) == 0 {
		t.Errorf("flattened page renders blank; the mark was not baked into content")
	}
}

func TestFlattenAnnotations_TranslucentInjectsExtGState(t *testing.T) {
	doc := blankPageDoc(t)
	if _, err := doc.AddAnnotation(Annotation{
		PageID: doc.Pages[0].ID, Type: AnnotationHighlight,
		Rect: Rect{X: 10, Y: 10, Width: 30, Height: 30}, Color: "#ff0000", Opacity: 0.4,
	}); err != nil {
		t.Fatal(err)
	}
	flat, err := doc.FlattenAnnotations()
	if err != nil {
		t.Fatal(err)
	}

	content := flattenPageContent(t, flat, 0)
	if !strings.Contains(content, "gs") {
		t.Errorf("translucent highlight did not emit a `gs` op; got:\n%s", content)
	}

	// The named ExtGState is injected into the page resources with ca ≈ 0.4.
	res := flat.pageInfos[0].Resources
	eg, ok, err := flat.resolver.ResolveDict(res["ExtGState"])
	if err != nil || !ok {
		t.Fatalf("page /Resources has no /ExtGState dict (err=%v ok=%v)", err, ok)
	}
	found := false
	for name := range eg {
		gs, ok, _ := flat.resolver.ResolveDict(eg[name])
		if !ok {
			continue
		}
		if ca, ok := gs["ca"].(parse.Real); ok && ca > 0.39 && ca < 0.41 {
			found = true
		}
	}
	if !found {
		t.Errorf("no injected ExtGState with ca≈0.4 found in %v", eg)
	}
}

func TestFlattenAnnotations_SharedResourcesNotCorrupted(t *testing.T) {
	// Two pages share ONE /Resources object (6 0 R). Each gets a translucent
	// highlight at a DIFFERENT opacity. If injection mutated the shared resource
	// dict instead of cloning per page, one alpha would clobber the other.
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources 6 0 R /Contents 7 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources 6 0 R /Contents 8 0 R >>",
		5: "<< /Producer (x) >>",
		6: "<< >>", // the SHARED resources object
		7: coreStreamObj(""),
		8: coreStreamObj(""),
	}, "<< /Size 9 /Root 1 0 R /Info 5 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.AddAnnotation(Annotation{PageID: doc.Pages[0].ID, Type: AnnotationHighlight, Rect: Rect{X: 0, Y: 0, Width: 10, Height: 10}, Color: "#ff0000", Opacity: 0.3}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.AddAnnotation(Annotation{PageID: doc.Pages[1].ID, Type: AnnotationHighlight, Rect: Rect{X: 0, Y: 0, Width: 10, Height: 10}, Color: "#ff0000", Opacity: 0.6}); err != nil {
		t.Fatal(err)
	}

	flat, err := doc.FlattenAnnotations()
	if err != nil {
		t.Fatal(err)
	}

	caOf := func(page int) float64 {
		res := flat.pageInfos[page].Resources
		eg, ok, _ := flat.resolver.ResolveDict(res["ExtGState"])
		if !ok {
			t.Fatalf("page %d has no /ExtGState", page)
		}
		for name := range eg {
			gs, ok, _ := flat.resolver.ResolveDict(eg[name])
			if ok {
				if ca, ok := gs["ca"].(parse.Real); ok {
					return float64(ca)
				}
			}
		}
		t.Fatalf("page %d has no ca in /ExtGState", page)
		return 0
	}
	if ca := caOf(0); ca < 0.29 || ca > 0.31 {
		t.Errorf("page 0 ca = %v, want ~0.3 (shared-resource clone failed)", ca)
	}
	if ca := caOf(1); ca < 0.59 || ca > 0.61 {
		t.Errorf("page 1 ca = %v, want ~0.6 (shared-resource clone failed)", ca)
	}
}

func TestFlattenAnnotations_NoAnnotationsRoundTrips(t *testing.T) {
	doc := blankPageDoc(t)
	flat, err := doc.FlattenAnnotations()
	if err != nil {
		t.Fatalf("FlattenAnnotations with no annotations: %v", err)
	}
	if flat.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", flat.PageCount)
	}
}

func TestFlattenAnnotations_ResolverLessErrors(t *testing.T) {
	if _, err := (&Document{}).FlattenAnnotations(); err != ErrInvalidPDF {
		t.Errorf("err = %v, want ErrInvalidPDF", err)
	}
}

func renderFlatPage(t *testing.T, doc *Document, opts RenderOptions) image.Image {
	t.Helper()
	data, err := doc.RenderPage(0, opts)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

// --- unit tests for the content generators ---

func TestFlattenOps_PerType(t *testing.T) {
	tests := []struct {
		name    string
		ann     Annotation
		must    []string
		mustNot []string
	}{
		{"highlight", Annotation{Type: AnnotationHighlight, Rect: Rect{1, 2, 3, 4}, Color: "#ff0000", Opacity: 1}, []string{"rg", "re f"}, []string{"gs"}},
		{"shape", Annotation{Type: AnnotationShape, Rect: Rect{1, 2, 3, 4}, Color: "#00ff00", Opacity: 1}, []string{"RG", "re S", "w"}, nil},
		{"draw", Annotation{Type: AnnotationDraw, Color: "#0000ff", Opacity: 1, Points: []Point{{1, 1}, {2, 2}, {3, 3}}}, []string{"m", "l", "S"}, nil},
		{"note", Annotation{Type: AnnotationNote, Rect: Rect{5, 5, 2, 2}, Color: "#f59e0b", Opacity: 1}, []string{"rg", "re f"}, nil},
		{"deferred redact", Annotation{Type: AnnotationRedact, Rect: Rect{0, 0, 9, 9}}, nil, []string{"re", "rg", "S"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, _ := flattenOps([]Annotation{tt.ann})
			s := string(content)
			for _, m := range tt.must {
				if !strings.Contains(s, m) {
					t.Errorf("content %q missing %q", s, m)
				}
			}
			for _, m := range tt.mustNot {
				if strings.Contains(s, m) {
					t.Errorf("content %q should not contain %q", s, m)
				}
			}
		})
	}
}

func TestFlattenOps_DedupsExtGState(t *testing.T) {
	// Two highlights at the same opacity share one ExtGState; a third at a
	// different opacity adds a second.
	anns := []Annotation{
		{Type: AnnotationHighlight, Rect: Rect{0, 0, 1, 1}, Color: "#000", Opacity: 0.5},
		{Type: AnnotationHighlight, Rect: Rect{0, 0, 1, 1}, Color: "#000", Opacity: 0.5},
		{Type: AnnotationHighlight, Rect: Rect{0, 0, 1, 1}, Color: "#000", Opacity: 0.25},
	}
	_, exts := flattenOps(anns)
	if len(exts) != 2 {
		t.Errorf("got %d ExtGStates, want 2 (dedup by opacity)", len(exts))
	}
}

func TestFormatNum(t *testing.T) {
	tests := map[float64]string{
		1.5: "1.5", 2.0: "2", 0: "0", -3.25: "-3.25", 100: "100", 0.1: "0.1",
	}
	for in, want := range tests {
		if got := formatNum(in); got != want {
			t.Errorf("formatNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAnnotationColorRGB_Fallback(t *testing.T) {
	r, g, b := annotationColorRGB("#ff0000")
	if r != 1 || g != 0 || b != 0 {
		t.Errorf("#ff0000 = (%v,%v,%v), want (1,0,0)", r, g, b)
	}
	// Invalid → amber #f59e0b.
	r, g, b = annotationColorRGB("bogus")
	if int(r*255+0.5) != 0xf5 || int(g*255+0.5) != 0x9e || int(b*255+0.5) != 0x0b {
		t.Errorf("invalid hex → (%v,%v,%v), want amber", r, g, b)
	}
}

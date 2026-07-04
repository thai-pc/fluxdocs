package core

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/thai-pc/fluxdocs/core/render"
)

// blankPageDoc builds a single blank (white) 100x100 page document so an
// annotation overlay is easy to detect against the white background.
func blankPageDoc(t *testing.T) *Document {
	t.Helper()
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: coreStreamObj(""),
	}, "<< /Size 5 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func coreNonWhite(img image.Image) int {
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 < 250 || g>>8 < 250 || bl>>8 < 250 {
				n++
			}
		}
	}
	return n
}

func renderPageImage(t *testing.T, doc *Document, opts RenderOptions) image.Image {
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

func TestRenderPage_DrawsAnnotationLayerByDefault(t *testing.T) {
	doc := blankPageDoc(t)
	if _, err := doc.AddAnnotation(Annotation{
		PageID: doc.Pages[0].ID, Type: AnnotationHighlight,
		Rect: Rect{X: 20, Y: 20, Width: 60, Height: 60}, Color: "#000000", Opacity: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Default: the annotation is drawn → the page is no longer all white.
	img := renderPageImage(t, doc, RenderOptions{DPI: 72, Format: "png"})
	if coreNonWhite(img) == 0 {
		t.Errorf("default render has no ink; annotation overlay not drawn")
	}

	// SkipAnnotations: the bare page is fully white.
	clean := renderPageImage(t, doc, RenderOptions{DPI: 72, Format: "png", SkipAnnotations: true})
	if got := coreNonWhite(clean); got != 0 {
		t.Errorf("SkipAnnotations render has %d non-white pixels, want 0 (clean page)", got)
	}
}

func TestRenderPage_NoAnnotationsIsBlank(t *testing.T) {
	doc := blankPageDoc(t)
	img := renderPageImage(t, doc, RenderOptions{DPI: 72, Format: "png"})
	if got := coreNonWhite(img); got != 0 {
		t.Errorf("blank page with no annotations has %d non-white pixels, want 0", got)
	}
}

func TestRenderAnnotations_TranslatesAndSkipsDeferred(t *testing.T) {
	in := []Annotation{
		{Type: AnnotationHighlight, Rect: Rect{X: 1, Y: 2, Width: 3, Height: 4}, Color: "#abc", Opacity: 0.5},
		{Type: AnnotationDraw, Points: []Point{{X: 5, Y: 6}, {X: 7, Y: 8}}},
		{Type: AnnotationRedact, Rect: Rect{X: 0, Y: 0, Width: 9, Height: 9}}, // deferred → dropped
		{Type: AnnotationStamp}, // deferred → dropped
		{Type: AnnotationNote, Rect: Rect{X: 10, Y: 11, Width: 1, Height: 1}},
		{Type: AnnotationShape, Rect: Rect{X: 12, Y: 13, Width: 2, Height: 2}},
	}
	out := renderAnnotations(in)
	if len(out) != 4 {
		t.Fatalf("got %d render annotations, want 4 (deferred types dropped)", len(out))
	}
	// First: highlight fields carried across.
	h := out[0]
	if h.Type != "highlight" || h.X != 1 || h.Y != 2 || h.Width != 3 || h.Height != 4 ||
		h.Color != "#abc" || h.Opacity != 0.5 {
		t.Errorf("highlight translated wrong: %+v", h)
	}
	// Second: draw points carried across as render.AnnPoint.
	d := out[1]
	if d.Type != "draw" || len(d.Points) != 2 || d.Points[0] != (render.AnnPoint{X: 5, Y: 6}) {
		t.Errorf("draw translated wrong: %+v", d)
	}
	// No redact/stamp survived.
	for _, a := range out {
		if a.Type == "redact" || a.Type == "stamp" {
			t.Errorf("deferred type %q leaked into the overlay", a.Type)
		}
	}
}

func TestRenderAnnotations_Empty(t *testing.T) {
	if out := renderAnnotations(nil); out != nil {
		t.Errorf("renderAnnotations(nil) = %v, want nil", out)
	}
}

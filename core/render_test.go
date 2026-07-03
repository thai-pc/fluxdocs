package core

import (
	"bytes"
	"fmt"
	"image/png"
	"testing"
)

// renderTestDoc builds a multi-page document where each page fills itself with
// a solid color, so a render is easy to verify and RenderAllPages has real work
// to parallelize.
func renderTestDoc(t *testing.T, pages int) *Document {
	t.Helper()
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
	}
	kids := ""
	obj := 3
	for i := 0; i < pages; i++ {
		pageObj := obj
		contentObj := obj + 1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", pageObj)
		objs[pageObj] = fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 20 20] /Contents %d 0 R >>", contentObj)
		objs[contentObj] = coreStreamObj("0 0 0 rg 0 0 20 20 re f")
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages)

	pdf := buildCorePDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", obj))
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

func TestRenderPage_PNG(t *testing.T) {
	doc := renderTestDoc(t, 1)
	out, err := doc.RenderPage(0, RenderOptions{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 20 {
		t.Errorf("image = %dx%d, want 20x20", b.Dx(), b.Dy())
	}
	r, g, b, _ := img.At(10, 10).RGBA()
	if r>>8 > 8 || g>>8 > 8 || b>>8 > 8 {
		t.Errorf("center = (%d,%d,%d), want black fill", r>>8, g>>8, b>>8)
	}
}

func TestRenderPage_OutOfRange(t *testing.T) {
	doc := renderTestDoc(t, 1)
	if _, err := doc.RenderPage(9, RenderOptions{}); err != ErrPageNotFound {
		t.Errorf("err = %v, want ErrPageNotFound", err)
	}
}

func TestRenderPage_OnPageRenderedCallback(t *testing.T) {
	doc := renderTestDoc(t, 1)
	var gotIndex int
	var gotBytes int
	doc.OnPageRendered(func(idx int, img []byte) {
		gotIndex = idx
		gotBytes = len(img)
	})
	if _, err := doc.RenderPage(0, RenderOptions{DPI: 72, Format: "png"}); err != nil {
		t.Fatal(err)
	}
	if gotIndex != 0 || gotBytes == 0 {
		t.Errorf("callback got (index=%d, bytes=%d), want (0, >0)", gotIndex, gotBytes)
	}
}

// TestRenderAllPages_Parallel exercises the concurrent path; run with -race to
// verify the shared parse.Resolver is safe under the goroutine-per-page pool.
func TestRenderAllPages_Parallel(t *testing.T) {
	doc := renderTestDoc(t, 8)
	imgs, err := doc.RenderAllPages(RenderOptions{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 8 {
		t.Fatalf("got %d images, want 8", len(imgs))
	}
	for i, out := range imgs {
		if _, err := png.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("page %d not a valid PNG: %v", i, err)
		}
	}
}

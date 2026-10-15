package core

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"strings"
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

// hostileRenderDoc builds a two-page document: page 0 is a normal 20x20pt black
// page, page 1 carries a hostile 2^33 pt MediaBox (a 2^32 x 2^32 canvas at
// 36 DPI, whose int64 area used to wrap to 0 and slip past the canvas cap).
func hostileRenderDoc(t *testing.T) *Document {
	t.Helper()
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 20 20] /Contents 4 0 R >>",
		4: coreStreamObj("0 0 0 rg 0 0 20 20 re f"),
		5: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 8589934592 8589934592] /Contents 4 0 R >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	if doc.PageCount != 2 {
		t.Fatalf("PageCount = %d, want 2", doc.PageCount)
	}
	return doc
}

// TestRenderPage_HostileMediaBoxReturnsError drives the canvas-cap regression
// through the public API: the hostile page returns an error (not a panic, not a
// misleading sentinel), the callback does not fire for it, and the document
// stays usable for its well-formed page.
func TestRenderPage_HostileMediaBoxReturnsError(t *testing.T) {
	doc := hostileRenderDoc(t)
	var fired []int
	doc.OnPageRendered(func(idx int, _ []byte) { fired = append(fired, idx) })

	for _, dpi := range []int{36, 1, 0} {
		img, err := doc.RenderPage(1, RenderOptions{DPI: dpi, Format: "png"})
		if err == nil {
			t.Fatalf("DPI %d: rendered %d bytes for a 2^33 pt page, want canvas-too-large error", dpi, len(img))
		}
		if errors.Is(err, ErrPageNotFound) || errors.Is(err, ErrUnsupportedFormat) {
			t.Errorf("DPI %d: err = %v, want a canvas-size error, not a page/format sentinel", dpi, err)
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("DPI %d: err = %v, want canvas-too-large (\"exceeds\")", dpi, err)
		}
		if img != nil {
			t.Errorf("DPI %d: got %d image bytes alongside an error, want nil", dpi, len(img))
		}
	}
	if len(fired) != 0 {
		t.Errorf("OnPageRendered fired for pages %v, want no callback for failed renders", fired)
	}

	out, err := doc.RenderPage(0, RenderOptions{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatalf("well-formed page after hostile render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 20 {
		t.Errorf("image = %dx%d, want 20x20", b.Dx(), b.Dy())
	}
}

// TestRenderAllPages_HostilePageFailsWholeBatch runs the hostile page through the
// goroutine pool: before the fix the panic in a pool goroutine would have taken
// down the whole process. It must surface as the batch's error instead. Run with
// -race (the pool writes results/errs slices from several goroutines).
func TestRenderAllPages_HostilePageFailsWholeBatch(t *testing.T) {
	doc := hostileRenderDoc(t)
	imgs, err := doc.RenderAllPages(RenderOptions{DPI: 36, Format: "png"})
	if err == nil {
		t.Fatalf("RenderAllPages returned %d images, want canvas-too-large error from page 1", len(imgs))
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want canvas-too-large (\"exceeds\")", err)
	}
	if imgs != nil {
		t.Errorf("got %d images alongside an error, want nil", len(imgs))
	}
}

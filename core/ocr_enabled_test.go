//go:build ocr

package core

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// textJPEG renders text as a black-on-white JPEG for embedding as an image
// XObject.
func textJPEG(t *testing.T, text string, w, h int) []byte {
	t.Helper()
	ft, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: 48, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: face, Dot: fixed.P(20, h*2/3)}
	d.DrawString(text)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestOCRPage_ReadsScannedPage builds a page whose only content is an embedded
// image of text, then OCRs it end to end (render -> Tesseract).
func TestOCRPage_ReadsScannedPage(t *testing.T) {
	const w, h = 640, 160
	jpg := textJPEG(t, "OCR SCAN", w, h)
	imgObj := fmt.Sprintf(
		"<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n%s\nendstream",
		w, h, len(jpg), jpg)

	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 320 80] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im 5 0 R >> >> /Contents 4 0 R >>",
		4: coreStreamObj("q 320 0 0 80 0 0 cm /Im Do Q"),
		5: imgObj,
	}, "<< /Size 6 /Root 1 0 R >>")

	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	text, err := doc.OCRPage(0)
	if err != nil {
		t.Fatalf("OCRPage: %v", err)
	}
	up := strings.ToUpper(text)
	if !strings.Contains(up, "OCR") && !strings.Contains(up, "SCAN") {
		t.Errorf("OCRPage read %q, want it to contain OCR/SCAN", text)
	}
}

// TestOCRPage_OutOfRange checks the bounds error path under the ocr build.
func TestOCRPage_OutOfRange(t *testing.T) {
	doc := renderTestDoc(t, 1)
	if _, err := doc.OCRPage(5); err != ErrPageNotFound {
		t.Errorf("OCRPage(5) = %v, want ErrPageNotFound", err)
	}
}

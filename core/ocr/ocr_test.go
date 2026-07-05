//go:build ocr

package ocr

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// textPNG renders text as crisp black-on-white PNG bytes for OCR.
func textPNG(t *testing.T, text string) []byte {
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

	img := image.NewRGBA(image.Rect(0, 0, 640, 120))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: face, Dot: fixed.P(24, 80)}
	d.DrawString(text)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImage_ReadsText(t *testing.T) {
	got, err := Image(textPNG(t, "HELLO WORLD"), "eng", "")
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	up := strings.ToUpper(got)
	if !strings.Contains(up, "HELLO") || !strings.Contains(up, "WORLD") {
		t.Errorf("OCR = %q, want to contain HELLO WORLD", got)
	}
}

func TestImage_EmptyInput(t *testing.T) {
	if _, err := Image(nil, "eng", ""); err == nil {
		t.Errorf("Image(nil) should error")
	}
}

func TestImage_BadImage(t *testing.T) {
	if _, err := Image([]byte("not an image"), "eng", ""); err == nil {
		t.Errorf("Image(garbage) should error")
	}
}

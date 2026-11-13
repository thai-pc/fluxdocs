package core

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"
)

// TestClip_ClippedTextIsInvisibleButPresent pins docs/design/clipping-paths.md
// §8: text drawn entirely outside a W clip is not rendered, yet it is still in
// the content, so extraction (which ignores clips) returns it. Redaction treats
// it as present too (testing/security/redaction, d5_clip_text_outside_W).
func TestClip_ClippedTextIsInvisibleButPresent(t *testing.T) {
	pdf := buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: coreStreamObj("q 0 0 1 1 re W n 0 0 200 100 re f BT /F1 24 Tf 10 40 Td (Hidden) Tj ET Q"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	text, err := doc.ExtractText(ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hidden" {
		t.Errorf("ExtractText = %q, want the clipped text %q", text, "Hidden")
	}
	data, err := doc.RenderPage(0, RenderOptions{DPI: 72, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y != 0xff {
				ink++
			}
		}
	}
	if ink != 1 { // the 1×1 pt clip at the bottom-left corner
		t.Errorf("rendered ink = %d px, want 1 (everything else clipped)", ink)
	}
}

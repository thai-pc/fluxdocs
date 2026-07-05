//go:build ocr

package core

import "github.com/thai-pc/fluxdocs/core/ocr"

// ocrPage rasterizes the page to a PNG at a resolution suited to OCR and runs
// Tesseract on it (built with the `ocr` tag). Bounds are already checked by
// OCRPage.
func (d *Document) ocrPage(pageIndex int) (string, error) {
	img, err := d.RenderPage(pageIndex, RenderOptions{DPI: 300, Format: "png", SkipAnnotations: true})
	if err != nil {
		return "", err
	}
	return ocr.Image(img, "eng", "")
}

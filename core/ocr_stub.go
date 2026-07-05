//go:build !ocr

package core

// ocrPage is the default (no-CGO) OCR path: OCR is a Pro feature that requires a
// binary built with the `ocr` build tag (Tesseract via CGO). Keeping this behind
// a build tag is what lets the default build stay 100% pure Go and MIT-safe.
func (d *Document) ocrPage(pageIndex int) (string, error) {
	return "", ErrFeatureRequiresPro
}

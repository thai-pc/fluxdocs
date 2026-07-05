//go:build !ocr

package core

import (
	"errors"
	"testing"
)

// TestOCRPage_RequiresProByDefault verifies the default (pure-Go, no-CGO) build
// gates OCR behind the `ocr` build tag — the tier-separation guarantee.
func TestOCRPage_RequiresProByDefault(t *testing.T) {
	doc := renderTestDoc(t, 1)
	if _, err := doc.OCRPage(0); !errors.Is(err, ErrFeatureRequiresPro) {
		t.Errorf("OCRPage without the ocr tag = %v, want ErrFeatureRequiresPro", err)
	}
	// The bounds check still runs first.
	if _, err := doc.OCRPage(9); err != ErrPageNotFound {
		t.Errorf("OCRPage(9) = %v, want ErrPageNotFound", err)
	}
}

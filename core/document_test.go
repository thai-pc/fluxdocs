package core

import (
	"errors"
	"strconv"
	"testing"
)

// newTestDoc builds a Document with n pre-resolved pages, for testing
// index-based methods without a real parser.
func newTestDoc(n int) *Document {
	pages := make([]Page, n)
	for i := range pages {
		pages[i] = Page{
			ID:     PageID("page-" + strconv.Itoa(i)),
			Index:  i,
			Width:  612, // US Letter, points
			Height: 792,
		}
	}
	return &Document{PageCount: n, Pages: pages}
}

func TestGetPageCount(t *testing.T) {
	for _, n := range []int{0, 1, 12} {
		if got := newTestDoc(n).GetPageCount(); got != n {
			t.Errorf("GetPageCount() = %d, want %d", got, n)
		}
	}
}

func TestGetPage(t *testing.T) {
	doc := newTestDoc(3)

	tests := []struct {
		name    string
		index   int
		wantErr error
	}{
		{"first", 0, nil},
		{"last", 2, nil},
		{"negative", -1, ErrPageNotFound},
		{"out of range", 3, ErrPageNotFound},
		{"far out of range", 99, ErrPageNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := doc.GetPage(tt.index)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("GetPage(%d) err = %v, want %v", tt.index, err, tt.wantErr)
			}
			if tt.wantErr == nil {
				if p == nil {
					t.Fatalf("GetPage(%d) returned a nil page with no error", tt.index)
				}
				if p.Index != tt.index {
					t.Errorf("page.Index = %d, want %d", p.Index, tt.index)
				}
				// Must be a pointer to the actual element in Pages, not a copy.
				if p != &doc.Pages[tt.index] {
					t.Errorf("GetPage(%d) did not return a pointer to Pages[%d]", tt.index, tt.index)
				}
			} else if p != nil {
				t.Errorf("GetPage(%d) returned a non-nil page alongside an error: %+v", tt.index, p)
			}
		})
	}
}

func TestClose_Idempotent(t *testing.T) {
	doc := newTestDoc(1)

	if err := doc.Close(); err != nil {
		t.Fatalf("Close() call 1 = %v, want nil", err)
	}
	if !doc.closed {
		t.Error("after Close(), doc.closed = false, want true")
	}
	// Calling again must be safe: no panic, still nil.
	if err := doc.Close(); err != nil {
		t.Fatalf("Close() call 2 = %v, want nil", err)
	}
}

func TestRenderAllPages(t *testing.T) {
	t.Run("zero pages returns empty slice, no error", func(t *testing.T) {
		imgs, err := newTestDoc(0).RenderAllPages(RenderOptions{})
		if err != nil {
			t.Fatalf("RenderAllPages() = %v, want nil", err)
		}
		if len(imgs) != 0 {
			t.Errorf("len(imgs) = %d, want 0", len(imgs))
		}
	})

	t.Run("error from RenderPage is propagated", func(t *testing.T) {
		// RenderPage is unimplemented -> errNotImplemented for an in-range page;
		// RenderAllPages must surface that exact error.
		_, err := newTestDoc(4).RenderAllPages(RenderOptions{DPI: 150, Format: "png"})
		if !errors.Is(err, errNotImplemented) {
			t.Fatalf("RenderAllPages() err = %v, want errNotImplemented", err)
		}
	})
}

// TestPageIndexedMethods_BoundsError checks that every method taking a
// pageIndex returns ErrPageNotFound for an index outside [0, PageCount) —
// BEFORE reaching unimplemented code (it must not return errNotImplemented for
// a bad index).
func TestPageIndexedMethods_BoundsError(t *testing.T) {
	doc := newTestDoc(2)
	const badIndex = 5

	calls := map[string]func() error{
		"RenderPage": func() error {
			_, err := doc.RenderPage(badIndex, RenderOptions{})
			return err
		},
		"Rotate": func() error {
			return doc.Rotate(badIndex, 90)
		},
		"RedactText": func() error {
			_, err := doc.RedactText(badIndex, `\d+`)
			return err
		},
		"RedactArea": func() error {
			return doc.RedactArea(badIndex, Rect{})
		},
		"ExtractTextByPage": func() error {
			_, err := doc.ExtractTextByPage(badIndex, ExtractOptions{})
			return err
		},
		"ExtractTables": func() error {
			_, err := doc.ExtractTables(badIndex)
			return err
		},
		"OCRPage": func() error {
			_, err := doc.OCRPage(badIndex)
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrPageNotFound) {
				t.Errorf("%s(index=%d) err = %v, want ErrPageNotFound", name, badIndex, err)
			}
		})
	}
}

func TestEventCallbacks_Register(t *testing.T) {
	doc := newTestDoc(1)

	if doc.onAnnotationChange != nil || doc.onPageRendered != nil || doc.onFormFieldChange != nil {
		t.Fatal("callbacks must be nil before registration")
	}

	doc.OnAnnotationChange(func(Annotation) {})
	doc.OnPageRendered(func(int, []byte) {})
	doc.OnFormFieldChange(func(FormField) {})

	if doc.onAnnotationChange == nil {
		t.Error("OnAnnotationChange did not store the callback")
	}
	if doc.onPageRendered == nil {
		t.Error("OnPageRendered did not store the callback")
	}
	if doc.onFormFieldChange == nil {
		t.Error("OnFormFieldChange did not store the callback")
	}
}

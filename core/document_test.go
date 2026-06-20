package core

import (
	"errors"
	"strconv"
	"testing"
)

// newTestDoc dựng Document đã resolve sẵn n trang, dùng cho test các method
// thao tác theo index mà không cần parser thật.
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
					t.Fatalf("GetPage(%d) trả page nil dù không có lỗi", tt.index)
				}
				if p.Index != tt.index {
					t.Errorf("page.Index = %d, want %d", p.Index, tt.index)
				}
				// Phải là con trỏ tới phần tử thật trong Pages, không phải bản sao.
				if p != &doc.Pages[tt.index] {
					t.Errorf("GetPage(%d) không trả con trỏ tới Pages[%d]", tt.index, tt.index)
				}
			} else if p != nil {
				t.Errorf("GetPage(%d) trả page non-nil kèm lỗi: %+v", tt.index, p)
			}
		})
	}
}

func TestClose_Idempotent(t *testing.T) {
	doc := newTestDoc(1)

	if err := doc.Close(); err != nil {
		t.Fatalf("Close() lần 1 = %v, want nil", err)
	}
	if !doc.closed {
		t.Error("sau Close(), doc.closed = false, want true")
	}
	// Gọi lại phải an toàn, không panic, vẫn nil.
	if err := doc.Close(); err != nil {
		t.Fatalf("Close() lần 2 = %v, want nil", err)
	}
}

func TestRenderAllPages(t *testing.T) {
	t.Run("zero pages trả slice rỗng, không lỗi", func(t *testing.T) {
		imgs, err := newTestDoc(0).RenderAllPages(RenderOptions{})
		if err != nil {
			t.Fatalf("RenderAllPages() = %v, want nil", err)
		}
		if len(imgs) != 0 {
			t.Errorf("len(imgs) = %d, want 0", len(imgs))
		}
	})

	t.Run("lỗi từ RenderPage được propagate", func(t *testing.T) {
		// RenderPage chưa hiện thực -> errNotImplemented cho trang in-range;
		// RenderAllPages phải trả về đúng lỗi đó.
		_, err := newTestDoc(4).RenderAllPages(RenderOptions{DPI: 150, Format: "png"})
		if !errors.Is(err, errNotImplemented) {
			t.Fatalf("RenderAllPages() err = %v, want errNotImplemented", err)
		}
	})
}

// TestPageIndexedMethods_BoundsError kiểm tra mọi method nhận pageIndex đều
// trả ErrPageNotFound khi index ngoài [0, PageCount) — TRƯỚC khi chạm vào phần
// chưa hiện thực (không được trả errNotImplemented cho index sai).
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
		t.Fatal("callback phải nil trước khi đăng ký")
	}

	doc.OnAnnotationChange(func(Annotation) {})
	doc.OnPageRendered(func(int, []byte) {})
	doc.OnFormFieldChange(func(FormField) {})

	if doc.onAnnotationChange == nil {
		t.Error("OnAnnotationChange không lưu callback")
	}
	if doc.onPageRendered == nil {
		t.Error("OnPageRendered không lưu callback")
	}
	if doc.onFormFieldChange == nil {
		t.Error("OnFormFieldChange không lưu callback")
	}
}

// TestOpenDocument_Unimplemented đánh dấu trạng thái hiện tại (pre-build):
// OpenDocument chưa parse được nên trả errNotImplemented. Cập nhật test này khi
// parser layer được xây.
func TestOpenDocument_Unimplemented(t *testing.T) {
	doc, err := OpenDocument("testdata/nonexistent.pdf")
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("OpenDocument() err = %v, want errNotImplemented (chưa hiện thực)", err)
	}
	if doc != nil {
		t.Errorf("OpenDocument() trả doc non-nil kèm lỗi: %+v", doc)
	}
}

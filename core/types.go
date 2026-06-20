package core

import (
	"sync"
	"time"
)

// ID dùng type alias có kiểm soát (tương đương branded type trong TS) để tránh
// nhầm lẫn giữa các loại ID khi truyền vào hàm — không dùng string trần.
type (
	DocumentID   string
	PageID       string
	AnnotationID string
	LayerID      string
)

// Document là một tài liệu PDF đã mở. Pages được resolve sẵn (flat array) ngay
// khi OpenDocument để tránh duyệt lại page tree mỗi lần render (xem Appendix B).
type Document struct {
	ID        DocumentID
	Title     string
	PageCount int
	Pages     []Page
	Metadata  DocumentMetadata
	Encrypted bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// Runtime state (unexported) — chỉ tồn tại khi document đang mở, không
	// thuộc schema serialize ở Appendix A. Document vừa là DTO vừa là handle
	// sống mà OpenDocument trả về.
	mu                 sync.Mutex
	closed             bool
	onAnnotationChange func(a Annotation)
	onPageRendered     func(pageIndex int, img []byte)
	onFormFieldChange  func(f FormField)
}

type DocumentMetadata struct {
	Author   string
	Subject  string
	Keywords []string
	Producer string
	Custom   map[string]string
}

// Page mô tả kích thước hình học của một trang. Đơn vị là point (1/72 inch),
// theo hệ tọa độ PDF.
type Page struct {
	ID       PageID
	Index    int     // 0-based
	Width    float64 // points (1/72 inch)
	Height   float64
	Rotation int // 0, 90, 180, 270
}

type AnnotationType string

const (
	AnnotationHighlight AnnotationType = "highlight"
	AnnotationNote      AnnotationType = "note"
	AnnotationDraw      AnnotationType = "draw"
	AnnotationShape     AnnotationType = "shape"
	AnnotationStamp     AnnotationType = "stamp"
	AnnotationRedact    AnnotationType = "redact"
	AnnotationSignature AnnotationType = "signature"
)

// Annotation lưu độc lập với PDF gốc và chỉ áp lên khi render hoặc khi flatten.
// Cho phép multi-user annotate không sửa file gốc, dễ undo và đồng bộ.
type Annotation struct {
	ID        AnnotationID
	PageID    PageID
	Type      AnnotationType
	Rect      Rect
	Color     string  // hex, ví dụ "#f59e0b"
	Opacity   float64 // 0..1
	Content   string  // text nếu là note/stamp
	Points    []Point // dùng cho draw (freehand)
	AuthorID  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Meta      map[string]any
}

// Rect theo hệ tọa độ PDF (gốc ở góc dưới-trái, đơn vị point).
type Rect struct {
	X, Y, Width, Height float64
}

type Point struct {
	X, Y float64
}

type FormFieldType string

const (
	FieldText      FormFieldType = "text"
	FieldCheckbox  FormFieldType = "checkbox"
	FieldRadio     FormFieldType = "radio"
	FieldDropdown  FormFieldType = "dropdown"
	FieldSignature FormFieldType = "signature"
)

type FormField struct {
	Name     string
	Type     FormFieldType
	Value    string
	Options  []string // cho dropdown/radio
	Required bool
	ReadOnly bool
}

// RenderOptions điều khiển một lần render trang.
type RenderOptions struct {
	DPI       int    // mặc định 150
	Format    string // "png" | "jpeg" | "svg"
	PageRange []int  // rỗng = toàn bộ
	Quality   int    // 1-100, áp dụng cho jpeg
}

// ExtractOptions điều khiển việc trích xuất text/table.
type ExtractOptions struct {
	PreserveLayout bool
	IncludeTables  bool
	OCRFallback    bool // nếu trang là ảnh scan, fallback sang OCR (Pro)
}

// AnnotationLayer là tập annotation lưu tách biệt với PDF gốc (§6.2), có thể
// export/import độc lập để multi-user review.
type AnnotationLayer struct {
	ID          LayerID
	DocumentID  DocumentID
	Name        string // ví dụ "Bản review của Long"
	Annotations []Annotation
	Visible     bool
	CreatedAt   time.Time
}

// ViewerConfig cấu hình viewer ở client wrapper (§6.3). Các callback dùng cho
// binding sự kiện vì Go core không có event loop native.
type ViewerConfig struct {
	Theme            string // "light" | "dark" | "auto"
	InitialZoom      float64
	EnableAnnotation bool
	EnableForms      bool
	ReadOnly         bool
	Locale           string

	OnAnnotationChange func(a Annotation)
	OnPageChange       func(pageIndex int)
	OnFormFieldChange  func(field FormField)
}

// --- Supporting types được public API §7 tham chiếu (chưa định nghĩa ở §6) ---

// AnnotationPatch mô tả cập nhật một phần một Annotation. Field nil = giữ
// nguyên, tránh ghi đè ngoài ý muốn khi chỉ đổi một thuộc tính.
type AnnotationPatch struct {
	Rect    *Rect
	Color   *string
	Opacity *float64
	Content *string
	Points  []Point
	Meta    map[string]any
}

// Table là kết quả ExtractTables: lưới ô đã detect trên một trang.
type Table struct {
	PageIndex int
	Rows      [][]string // [hàng][cột]
	Bounds    Rect
}

// SignatureOptions cấu hình SignDocument (Pro, PKCS#7).
type SignatureOptions struct {
	Reason      string
	Location    string
	ContactInfo string
	CertPEM     []byte // certificate dạng PEM
	KeyPEM      []byte // private key dạng PEM — không nhúng vào client/WASM bundle
	FieldName   string // form field chữ ký để gắn, rỗng = tạo invisible signature
}

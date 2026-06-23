package core

import (
	"sync"
	"time"
)

// IDs use controlled type aliases (the equivalent of branded types in TS) to
// avoid mixing up ID kinds when passing them to functions — never pass a bare
// string.
type (
	DocumentID   string
	PageID       string
	AnnotationID string
	LayerID      string
)

// Document is an opened PDF document. Pages are resolved up front (a flat array)
// at OpenDocument time to avoid re-walking the page tree on every render (see
// Appendix B).
type Document struct {
	ID        DocumentID
	Title     string
	PageCount int
	Pages     []Page
	Metadata  DocumentMetadata
	Encrypted bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// Runtime state (unexported) — exists only while the document is open and
	// is not part of the serialized schema in Appendix A. A Document is both a
	// DTO and the live handle returned by OpenDocument.
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

// Page describes the geometry of a single page. Units are points (1/72 inch),
// in PDF coordinate space.
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

// Annotation is stored independently of the original PDF and is applied only
// when rendering or flattening. This lets multiple users annotate without
// modifying the source file, and makes undo and syncing easy.
type Annotation struct {
	ID        AnnotationID
	PageID    PageID
	Type      AnnotationType
	Rect      Rect
	Color     string  // hex, e.g. "#f59e0b"
	Opacity   float64 // 0..1
	Content   string  // text for note/stamp
	Points    []Point // used for draw (freehand)
	AuthorID  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Meta      map[string]any
}

// Rect is in PDF coordinate space (origin at bottom-left, units in points).
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
	Options  []string // for dropdown/radio
	Required bool
	ReadOnly bool
}

// RenderOptions controls a single page render.
type RenderOptions struct {
	DPI       int    // default 150
	Format    string // "png" | "jpeg" | "svg"
	PageRange []int  // empty = all pages
	Quality   int    // 1-100, applies to jpeg
}

// ExtractOptions controls text/table extraction.
type ExtractOptions struct {
	PreserveLayout bool
	IncludeTables  bool
	OCRFallback    bool // if the page is a scanned image, fall back to OCR (Pro)
}

// AnnotationLayer is a set of annotations stored separately from the original
// PDF (§6.2); it can be exported/imported independently for multi-user review.
type AnnotationLayer struct {
	ID          LayerID
	DocumentID  DocumentID
	Name        string // e.g. "Long's review"
	Annotations []Annotation
	Visible     bool
	CreatedAt   time.Time
}

// ViewerConfig configures the viewer in a client wrapper (§6.3). The callbacks
// are used for event binding because the Go core has no native event loop.
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

// --- Supporting types referenced by the public API in §7 (not defined in §6) ---

// AnnotationPatch describes a partial update to an Annotation. A nil field
// means "leave unchanged", avoiding accidental overwrites when changing a
// single attribute.
type AnnotationPatch struct {
	Rect    *Rect
	Color   *string
	Opacity *float64
	Content *string
	Points  []Point
	Meta    map[string]any
}

// Table is the result of ExtractTables: a grid of cells detected on a page.
type Table struct {
	PageIndex int
	Rows      [][]string // [row][column]
	Bounds    Rect
}

// SignatureOptions configures SignDocument (Pro, PKCS#7).
type SignatureOptions struct {
	Reason      string
	Location    string
	ContactInfo string
	CertPEM     []byte // certificate in PEM form
	KeyPEM      []byte // private key in PEM form — never embed in a client/WASM bundle
	FieldName   string // signature form field to attach to; empty = invisible signature
}

package core

import (
	"sync"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
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

	// Annotation layer (unexported source of truth). annotations is the ordered
	// list; insertion order is preserved so GetAnnotations and export/round-trip
	// are deterministic. annotationSeq mints monotonic "ann-<n>" IDs. The
	// current-layer identity fields are initialized lazily on first use (see
	// ensureLayerLocked in annotation.go) because a Document may be constructed
	// directly (tests) as well as via OpenBytes. All of this is guarded by mu.
	annotations    []Annotation
	annotationSeq  uint64
	layerID        LayerID
	layerName      string
	layerVisible   bool
	layerCreatedAt time.Time

	// redactions is the pending redaction plan: page index -> regions to remove.
	// It is populated by RedactText/RedactArea and consumed (permanently) by
	// RedactAndFlatten. Guarded by mu.
	redactions map[int][]Rect

	// formValues holds pending AcroForm field fills (field name -> value) set via
	// SetFormFieldValue and reflected by GetFormFields. Guarded by mu.
	formValues map[string]string

	// Parse-layer handles kept alive for lazy operations (text extraction,
	// rendering). resolver resolves indirect objects against the file bytes;
	// pageInfos[i] carries page i's unresolved /Contents and /Resources,
	// parallel to Pages[i]. Both are nil for a Document not built from parse
	// (e.g. constructed in a test).
	resolver  *parse.Resolver
	pageInfos []parse.PageInfo
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
//
// JSON tags are the serialization contract (spec Appendix A, camelCase). They
// are explicit — not left to Go's default casing — because the JS wrappers and
// stored .json sidecars depend on the exact keys. omitempty is applied only to
// fields that are semantically absent when empty (content/points/authorId/meta);
// never to rect/type/color/opacity or numeric coordinates, where a real zero
// (e.g. opacity 0, x 0) must survive a round trip. It is intentionally NOT used
// on the timestamps: encoding/json does not treat a struct as empty, so it would
// be a no-op there and misleading.
type Annotation struct {
	ID        AnnotationID   `json:"id"`
	PageID    PageID         `json:"pageId"`
	Type      AnnotationType `json:"type"`
	Rect      Rect           `json:"rect"`
	Color     string         `json:"color"`             // hex, e.g. "#f59e0b"
	Opacity   float64        `json:"opacity"`           // 0..1
	Content   string         `json:"content,omitempty"` // text for note/stamp
	Points    []Point        `json:"points,omitempty"`  // used for draw (freehand)
	AuthorID  string         `json:"authorId,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Rect is in PDF coordinate space (origin at bottom-left, units in points).
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
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

	// SkipAnnotations, when true, renders the bare page without the FluxDocs
	// annotation layer. By default (false) annotations for the page are drawn as
	// a non-destructive overlay — annotations are applied at render time (§9).
	SkipAnnotations bool
}

// ExtractOptions controls text/table extraction.
type ExtractOptions struct {
	PreserveLayout bool
	IncludeTables  bool
	OCRFallback    bool // if the page is a scanned image, fall back to OCR (Pro)
}

// WatermarkOptions configures a text watermark stamped over pages (Pro). The
// watermark is drawn centered and rotated on each target page, translucent, in a
// standard Helvetica font.
type WatermarkOptions struct {
	Text     string  // the watermark text (required)
	FontSize float64 // points; <=0 means 48
	Color    string  // hex "#rrggbb"; empty means a neutral gray
	Opacity  float64 // 0..1; <=0 means 0.3
	Rotation float64 // degrees counter-clockwise; default 45 when zero-valued
	Pages    []int   // 0-based target pages; empty = all pages
}

// AnnotationLayer is a set of annotations stored separately from the original
// PDF (§6.2); it can be exported/imported independently for multi-user review.
type AnnotationLayer struct {
	ID          LayerID      `json:"id"`
	DocumentID  DocumentID   `json:"documentId"`
	Name        string       `json:"name"` // e.g. "Long's review"
	Annotations []Annotation `json:"annotations"`
	Visible     bool         `json:"visible"`
	CreatedAt   time.Time    `json:"createdAt"`
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

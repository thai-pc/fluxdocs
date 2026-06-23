package core

import (
	"errors"
	"io"
	"runtime"
	"sync"
)

// errNotImplemented marks a method that exists in the public API but is not yet
// implemented during the pre-build phase. It will be replaced as each layer
// (parse, render, docops, …) is built. This is an internal error, not a public
// sentinel.
var errNotImplemented = errors.New("fluxdocs: not implemented")

// (OpenDocument and OpenBytes live in open.go, where the parse layer is wired
// in. The errNotImplemented sentinel above is still used by the unimplemented
// methods below.)

// --- §7.2 Document Operations ---

// GetPageCount returns the page count. Equivalent to reading the PageCount
// field directly; kept per the §7.2 API for consumers that do not touch the
// struct.
func (d *Document) GetPageCount() int { return d.PageCount }

// GetPage returns a pointer to the Page at index (0-based), or ErrPageNotFound
// if index is outside [0, PageCount).
func (d *Document) GetPage(index int) (*Page, error) {
	if index < 0 || index >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	return &d.Pages[index], nil
}

// RenderPage renders one page to a buffer in opts.Format ("png"|"jpeg"|"svg").
//
// Returns the encoded image bytes, or ErrPageNotFound if index is out of range.
func (d *Document) RenderPage(index int, opts RenderOptions) ([]byte, error) {
	if index < 0 || index >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	_ = opts
	return nil, errNotImplemented
}

// RenderAllPages renders every page in parallel via a goroutine pool bounded by
// runtime.NumCPU() (§13.3). Each page is independent — a natural advantage of
// Go over single-threaded JS.
//
// Returns one buffer per page in order, or the first error encountered.
func (d *Document) RenderAllPages(opts RenderOptions) ([][]byte, error) {
	results := make([][]byte, d.PageCount)
	errs := make([]error, d.PageCount)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for i := 0; i < d.PageCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			img, err := d.RenderPage(idx, opts)
			results[idx] = img
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// Merge appends other after d and returns a new document (d and other are left
// unchanged).
func (d *Document) Merge(other *Document) (*Document, error) {
	_ = other
	return nil, errNotImplemented
}

// Split divides d into multiple documents by page ranges [start, end] (0-based,
// inclusive).
func (d *Document) Split(pageRanges [][2]int) ([]*Document, error) {
	_ = pageRanges
	return nil, errNotImplemented
}

// Rotate rotates one page by a multiple of 90 degrees (90/180/270, or negative).
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) Rotate(pageIndex int, degrees int) error {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return ErrPageNotFound
	}
	_ = degrees
	return errNotImplemented
}

// Reorder rearranges pages according to newOrder (a permutation of
// [0, PageCount)).
func (d *Document) Reorder(newOrder []int) error {
	_ = newOrder
	return errNotImplemented
}

// Save writes the document (including flattened annotations, if any) to the
// file at path.
func (d *Document) Save(path string) error {
	_ = path
	return errNotImplemented
}

// SaveTo writes the document to w. Use it to stream a server response without
// touching disk, or to write into a browser buffer under WASM.
func (d *Document) SaveTo(w io.Writer) error {
	_ = w
	return errNotImplemented
}

// Close releases resources held by the document. Safe to call multiple times.
func (d *Document) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return nil
}

// --- §7.3 Annotation Operations ---

// AddAnnotation adds an annotation to the current layer and returns the stored
// copy with its ID and timestamps assigned.
func (d *Document) AddAnnotation(a Annotation) (Annotation, error) {
	return Annotation{}, errNotImplemented
}

// UpdateAnnotation applies a partial update by patch (a nil field is left
// unchanged).
//
// Returns the updated annotation, or ErrAnnotationNotFound.
func (d *Document) UpdateAnnotation(id AnnotationID, patch AnnotationPatch) (Annotation, error) {
	_ = id
	_ = patch
	return Annotation{}, errNotImplemented
}

// RemoveAnnotation deletes the annotation by ID, or returns
// ErrAnnotationNotFound.
func (d *Document) RemoveAnnotation(id AnnotationID) error {
	_ = id
	return errNotImplemented
}

// GetAnnotations returns all annotations belonging to one page.
func (d *Document) GetAnnotations(pageID PageID) ([]Annotation, error) {
	_ = pageID
	return nil, errNotImplemented
}

// FlattenAnnotations bakes annotations into the PDF content and returns a new
// document — flattened annotations can no longer be edited.
func (d *Document) FlattenAnnotations() (*Document, error) {
	return nil, errNotImplemented
}

// ExportAnnotationLayer exports the annotation layer independently of the
// original PDF (§6.2), for separate storage/sync or sharing among reviewers.
func (d *Document) ExportAnnotationLayer() (*AnnotationLayer, error) {
	return nil, errNotImplemented
}

// ImportAnnotationLayer applies a previously exported annotation layer onto the
// document.
func (d *Document) ImportAnnotationLayer(layer AnnotationLayer) error {
	_ = layer
	return errNotImplemented
}

// --- §7.4 Redaction (Pro) ---
// SECURITY WARNING: redaction must permanently remove content from the content
// stream, not merely paint over it (see .claude/SECURITY.md §1 and spec §13.4).
// After redaction, a mandatory test re-runs ExtractText over the redacted area
// -> it must return empty.

// RedactText marks regions whose text matches a regex pattern (e.g. national ID
// or credit-card numbers) and returns the list of Rects. Nothing is removed
// until RedactAndFlatten.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) RedactText(pageIndex int, pattern string) ([]Rect, error) {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	_ = pattern
	return nil, errNotImplemented
}

// RedactArea marks a specified region for redaction.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) RedactArea(pageIndex int, rect Rect) error {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return ErrPageNotFound
	}
	_ = rect
	return errNotImplemented
}

// RedactAndFlatten PERMANENTLY removes content within every marked region from
// the content stream plus hidden metadata/annotations, then returns a new
// document. This is NOT recoverable.
func (d *Document) RedactAndFlatten() (*Document, error) {
	return nil, errNotImplemented
}

// --- §7.5 Forms (Pro) ---

// GetFormFields lists the AcroForm fields in the document.
func (d *Document) GetFormFields() ([]FormField, error) {
	return nil, errNotImplemented
}

// SetFormFieldValue sets a field value by name, or returns ErrFormFieldNotFound.
func (d *Document) SetFormFieldValue(name string, value string) error {
	_ = name
	_ = value
	return errNotImplemented
}

// FlattenForm turns form fields into static content that can no longer be
// edited.
func (d *Document) FlattenForm() (*Document, error) {
	return nil, errNotImplemented
}

// SignDocument signs the document per opts (PKCS#7) and returns a new signed
// document.
func (d *Document) SignDocument(opts SignatureOptions) (*Document, error) {
	_ = opts
	return nil, errNotImplemented
}

// --- §7.6 Extraction ---

// ExtractText extracts all text in reading order (preserving layout if opts
// requests it).
func (d *Document) ExtractText(opts ExtractOptions) (string, error) {
	_ = opts
	return "", errNotImplemented
}

// ExtractTextByPage extracts the text of a single page.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) ExtractTextByPage(pageIndex int, opts ExtractOptions) (string, error) {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return "", ErrPageNotFound
	}
	_ = opts
	return "", errNotImplemented
}

// ExtractTables detects and extracts tables on a single page.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) ExtractTables(pageIndex int) ([]Table, error) {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	return nil, errNotImplemented
}

// OCRPage (Pro) runs OCR on a scanned-image page. Requires the `ocr` build tag
// (Tesseract via CGO).
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) OCRPage(pageIndex int) (string, error) {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return "", ErrPageNotFound
	}
	return "", errNotImplemented
}

// ExtractStructured (Cloud) takes a desired schema and calls an LLM to return
// structured JSON.
func (d *Document) ExtractStructured(schema any) (map[string]any, error) {
	_ = schema
	return nil, errNotImplemented
}

// AskDocument (Cloud) answers a question about the document via an LLM, with
// citations to page/position.
func (d *Document) AskDocument(question string) (string, error) {
	_ = question
	return "", errNotImplemented
}

// --- §7.7 Events (callbacks, since the Go core has no native event loop) ---

// OnAnnotationChange registers a callback invoked whenever an annotation is
// added/updated/removed.
func (d *Document) OnAnnotationChange(fn func(a Annotation)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onAnnotationChange = fn
}

// OnPageRendered registers a callback invoked after a page finishes rendering.
func (d *Document) OnPageRendered(fn func(pageIndex int, img []byte)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onPageRendered = fn
}

// OnFormFieldChange registers a callback invoked when a form field's value
// changes.
func (d *Document) OnFormFieldChange(fn func(f FormField)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onFormFieldChange = fn
}

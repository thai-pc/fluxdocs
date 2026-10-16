package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/thai-pc/fluxdocs/core/extract"
	"github.com/thai-pc/fluxdocs/core/parse"
	"github.com/thai-pc/fluxdocs/core/render"
	"github.com/thai-pc/fluxdocs/core/sign"
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

// RenderPage renders one page to a buffer in opts.Format ("png"|"jpeg"; "svg"
// is not yet supported and returns render.ErrUnsupportedFormat). It is
// RenderPageContext with context.Background(): the per-page budget in opts still
// applies, but there is no deadline. Server render paths should call
// RenderPageContext with one.
//
// Wave 1 rasterizes vector graphics and text glyphs from embedded TrueType/CFF
// fonts; image XObjects, Type1 fonts, and non-embedded fonts are not yet drawn
// (see the render package).
//
// The FluxDocs annotation layer for the page is drawn as a non-destructive
// overlay on top of the content (§9); set opts.SkipAnnotations to render the
// bare page. The source PDF is never modified.
//
// Returns the encoded image bytes, ErrPageNotFound if index is out of range,
// ErrRenderBudgetExceeded if the render exceeds its budget or the canvas-size
// cap, another render error, or ErrInternal (an *InternalError carrying the page
// index) if rendering panicked. The OnPageRendered callback runs after
// rendering, outside that recovery: a panic in the caller's callback propagates
// to the caller.
func (d *Document) RenderPage(index int, opts RenderOptions) ([]byte, error) {
	return d.RenderPageContext(context.Background(), index, opts)
}

// RenderPageContext is RenderPage under ctx. The render polls ctx every 2^20
// raster-work units or 4096 interpreter steps and also compares the clock with
// ctx's deadline (the only check that fires under js/wasm, where a synchronous
// call never yields to the timer goroutine), so it stops within about one poll
// quantum of a cancel or deadline.
//
// Returns the same results as RenderPage, plus the ctx error when ctx is done
// before or during the render: errors.Is(err, context.DeadlineExceeded) or
// errors.Is(err, context.Canceled) holds, and the error is NOT
// ErrRenderBudgetExceeded (a deadline depends on load and may pass on retry; a
// budget trip is a property of the document). On any error the bytes are nil (no
// partial image) and OnPageRendered does not fire. A nil ctx is treated as
// context.Background().
func (d *Document) RenderPageContext(ctx context.Context, index int, opts RenderOptions) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	img, err := d.renderPage(ctx, index, opts)
	if err != nil {
		return nil, err
	}
	if d.onPageRendered != nil {
		d.onPageRendered(index, img)
	}
	return img, nil
}

// renderPage is RenderPageContext without the OnPageRendered callback: the
// guarded render of one page, so a recovered panic is attributed to FluxDocs
// only when FluxDocs code raised it.
func (d *Document) renderPage(ctx context.Context, index int, opts RenderOptions) (_ []byte, err error) {
	defer guard("RenderPage", index, &err)
	if index < 0 || index >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	if d.resolver == nil || index >= len(d.pageInfos) {
		return nil, errNotImplemented
	}
	injectTestPanic("RenderPage", index)
	var overlay []render.Annotation
	if !opts.SkipAnnotations {
		if anns, err := d.GetAnnotations(d.Pages[index].ID); err == nil {
			overlay = renderAnnotations(anns)
		}
	}
	img, err := render.Page(d.resolver, d.pageInfos[index], render.Options{
		DPI:            opts.DPI,
		Format:         opts.Format,
		Quality:        opts.Quality,
		Annotations:    overlay,
		MaxPaintPixels: opts.MaxPaintPixels,
		MaxOperators:   opts.MaxOperators,
		Context:        ctx,
	})
	if err != nil {
		if errors.Is(err, render.ErrBudgetExceeded) || errors.Is(err, render.ErrCanvasTooLarge) {
			return nil, fmt.Errorf("%w: page %d: %w", ErrRenderBudgetExceeded, index, err)
		}
		return nil, err
	}
	return img, nil
}

// renderAnnotations translates the core annotation layer into the render layer's
// own annotation value type (render cannot import core — import cycle). Only the
// four Wave-1 drawable types are passed through; the deferred stamp/redact/
// signature records are skipped (not drawn).
func renderAnnotations(anns []Annotation) []render.Annotation {
	if len(anns) == 0 {
		return nil
	}
	out := make([]render.Annotation, 0, len(anns))
	for _, a := range anns {
		switch a.Type {
		case AnnotationHighlight, AnnotationNote, AnnotationDraw, AnnotationShape:
		default:
			continue // deferred type: not drawn in Wave 1
		}
		ra := render.Annotation{
			Type:    string(a.Type),
			X:       a.Rect.X,
			Y:       a.Rect.Y,
			Width:   a.Rect.Width,
			Height:  a.Rect.Height,
			Color:   a.Color,
			Opacity: a.Opacity,
		}
		if len(a.Points) > 0 {
			ra.Points = make([]render.AnnPoint, len(a.Points))
			for i, p := range a.Points {
				ra.Points[i] = render.AnnPoint{X: p.X, Y: p.Y}
			}
		}
		out = append(out, ra)
	}
	return out
}

// RenderAllPages renders every page in parallel via a goroutine pool bounded by
// runtime.NumCPU() (§13.3). Each page is independent — a natural advantage of
// Go over single-threaded JS. It is RenderAllPagesContext with
// context.Background(); see that method for the results and error contract.
func (d *Document) RenderAllPages(opts RenderOptions) ([][]byte, error) {
	return d.RenderAllPagesContext(context.Background(), opts)
}

// errBatchAborted is the cancellation cause RenderAllPagesContext gives the
// pages still running or waiting when another page fails. It is internal: the
// batch never returns it, it returns the failure that caused it.
var errBatchAborted = errors.New("fluxdocs: render batch aborted by another page's error")

// RenderAllPagesContext renders every page in parallel under ctx, each page with
// its own budget from opts, via a goroutine pool bounded by runtime.NumCPU()
// (§13.3).
//
// Returns one buffer per page in order. The batch fails as a whole: on any error
// the result is nil, and the first failure cancels the pages still running or
// waiting, so a hostile N-page document costs about NumCPU × the per-page
// budget, not N × it. Pages that finished before the failure may already have
// fired OnPageRendered; a failed page never does. The error is, in order:
//   - ctx's error, if ctx is done when the batch ends (the caller's deadline or
//     cancel wins, even when every page had already rendered: the results are
//     then discarded). It matches context.Canceled or context.DeadlineExceeded
//     with errors.Is, and also the cause given to context.WithCancelCause /
//     WithTimeoutCause when there is a distinct one;
//   - otherwise the lowest-index page error that is a root cause (e.g.
//     ErrRenderBudgetExceeded or ErrInternal), never the internal cancellation
//     of the pages it aborted.
//
// A page whose rendering panics fails with ErrInternal (an *InternalError
// carrying its index) like any other page error; the other workers and the
// process are unaffected. OnPageRendered runs on the worker goroutines; if it
// panics, the first such panic is re-raised on the calling goroutine once every
// worker has finished, so a caller bug surfaces where the caller can recover it
// instead of killing the process from a worker. A nil ctx is treated as
// context.Background().
func (d *Document) RenderAllPagesContext(parent context.Context, opts RenderOptions) ([][]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	results := make([][]byte, d.PageCount)
	errs := make([]error, d.PageCount)

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	var (
		cbOnce  sync.Once
		cbPanic any // first OnPageRendered panic; read only after wg.Wait
	)

	for i := 0; i < d.PageCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Defers run LIFO: guard records a panic as the page's error first,
			// then any page error cancels the rest of the batch.
			defer func() {
				if errs[idx] != nil {
					cancel(errBatchAborted)
				}
			}()
			defer guard("RenderAllPages", idx, &errs[idx])
			injectTestPanic("RenderAllPages", idx)

			// A page that waited for its slot while the batch failed or ctx
			// ended starts no work: render.Page returns the ctx error at once.
			img, err := d.renderPage(ctx, idx, opts)
			results[idx] = img
			errs[idx] = err
			if err == nil && d.onPageRendered != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							cbOnce.Do(func() { cbPanic = r })
						}
					}()
					d.onPageRendered(idx, img)
				}()
			}
		}(i)
	}
	wg.Wait()
	if cbPanic != nil {
		panic(cbPanic) // the caller's callback panicked: re-raise on its goroutine
	}

	if parent.Err() != nil {
		return nil, parentError(parent)
	}
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, errBatchAborted) {
			return nil, err // lowest-index root cause
		}
		if first == nil {
			first = err
		}
	}
	if first != nil {
		return nil, first // unreachable in practice: an abort always has a root cause
	}
	return results, nil
}

// parentError returns a done ctx's error for RenderAllPagesContext: ctx.Err(),
// wrapped together with the cancel cause when the canceler supplied a distinct
// one, so errors.Is matches context.Canceled / context.DeadlineExceeded and the
// cause alike (as RenderPageContext's errors do).
func parentError(ctx context.Context) error {
	err := ctx.Err()
	if cause := context.Cause(ctx); cause != nil && cause != err {
		return fmt.Errorf("fluxdocs: %w: %w", err, cause)
	}
	return err
}

// Merge appends other's pages after d's pages and returns a new document; d and
// other are left unchanged. Both documents are copied into a single fresh PDF
// (their reachable object graphs are deep-copied and renumbered), then re-opened
// so the returned handle is a fully wired Document (resolver, pages, metadata).
//
// The merged /Info is synthesized from d's metadata (Producer = "FluxDocs");
// merge does NOT union document-level metadata, form fields, bookmarks, or
// tagging — those catalog features are dropped in Wave 1 (see the design note).
// Original page /Annots are carried through; the FluxDocs annotation JSON layer
// (a separate mechanism) is not baked in.
//
// Returns ErrInvalidPDF if either document was not opened from bytes/a file
// (a resolver-less, test-constructed Document cannot be serialized), or if the
// serialize/re-open round-trip fails.
func (d *Document) Merge(other *Document) (_ *Document, err error) {
	defer guard("Merge", -1, &err)
	if other == nil || d.resolver == nil || other.resolver == nil {
		return nil, ErrInvalidPDF
	}

	sources := make([]parse.PageSource, 0, len(d.pageInfos)+len(other.pageInfos))
	for _, pi := range d.pageInfos {
		sources = append(sources, parse.PageSource{Resolver: d.resolver, Page: pi})
	}
	for _, pi := range other.pageInfos {
		sources = append(sources, parse.PageSource{Resolver: other.resolver, Page: pi})
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	return OpenBytes(built)
}

// Split divides d into multiple documents by page ranges [start, end] (0-based,
// inclusive). It returns one *Document per range, in the given order; d is left
// unchanged. Each output is a fresh, self-contained PDF built from d's pages and
// re-opened.
//
// An empty pageRanges returns an empty slice and no error. Any range with
// start < 0, end < start, or end >= PageCount returns ErrPageNotFound (a range
// referencing a non-existent page). Returns ErrInvalidPDF if d was not opened
// from bytes/a file, or if a build fails.
func (d *Document) Split(pageRanges [][2]int) (_ []*Document, err error) {
	defer guard("Split", -1, &err)
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}

	// Validate every range up front, before building anything.
	for _, rng := range pageRanges {
		start, end := rng[0], rng[1]
		if start < 0 || end < start || end >= len(d.pageInfos) {
			return nil, ErrPageNotFound
		}
	}

	out := make([]*Document, 0, len(pageRanges))
	for _, rng := range pageRanges {
		sources := make([]parse.PageSource, 0, rng[1]-rng[0]+1)
		for i := rng[0]; i <= rng[1]; i++ {
			sources = append(sources, parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]})
		}
		built, err := parse.BuildDocument(sources, buildInfoDict(d))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
		}
		doc, err := OpenBytes(built)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

// Rotate and Reorder — the page-organization operations — live in pageops.go.

// Save writes the document as a clean, single-file PDF to path. It writes the
// current page set WITHOUT baking in the FluxDocs annotation layer (annotations
// are a separate JSON layer; flattening them onto a page is a future increment,
// FlattenAnnotations). Original page /Annots present in the source PDF are
// preserved. Save is native-only (filesystem); use SaveTo on the WASM path.
//
// Returns the underlying os error if the file cannot be created/written, or
// ErrInvalidPDF if the document was not opened from bytes/a file.
func (d *Document) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := d.SaveTo(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SaveTo serializes the document's current page set and writes it to w. Use it
// to stream a server response without touching disk, or to write into a browser
// buffer under WASM (it never touches the filesystem or the network). The
// FluxDocs annotation layer is not baked in (see Save).
//
// Returns ErrInvalidPDF if the document was not opened from bytes/a file (a
// resolver-less, test-constructed Document cannot be serialized), or the writer
// error from w.
func (d *Document) SaveTo(w io.Writer) (err error) {
	defer guard("SaveTo", -1, &err)
	if d.resolver == nil {
		return ErrInvalidPDF
	}

	sources := make([]parse.PageSource, 0, len(d.pageInfos))
	for _, pi := range d.pageInfos {
		sources = append(sources, parse.PageSource{Resolver: d.resolver, Page: pi})
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	_, err = w.Write(built)
	return err
}

// Bytes returns the exact PDF bytes the document was opened from. Unlike SaveTo,
// which re-serializes the page set (dropping non-page catalog features such as a
// digital signature or PDF/A output intent), Bytes returns the file verbatim. It
// is the correct way to persist a document produced by SignDocument or
// ConvertToPDFA (both re-open from freshly built bytes), whose structure a
// rebuild would break. It does NOT reflect uncommitted in-memory annotation-layer
// edits.
//
// Returns ErrInvalidPDF if the document was not opened from bytes/a file.
func (d *Document) Bytes() ([]byte, error) {
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}
	src := d.resolver.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out, nil
}

// buildInfoDict synthesizes a minimal /Info dictionary from a document's
// metadata. Producer is always "FluxDocs"; Title/Author/Subject are carried
// through when present. Merge/Split/Save use this rather than copying the
// source /Info object graph (which could dangle after a page-only copy).
func buildInfoDict(d *Document) parse.Dict {
	info := parse.Dict{"Producer": pdfTextString("FluxDocs")}
	if d.Title != "" {
		info["Title"] = pdfTextString(d.Title)
	}
	if d.Metadata.Author != "" {
		info["Author"] = pdfTextString(d.Metadata.Author)
	}
	if d.Metadata.Subject != "" {
		info["Subject"] = pdfTextString(d.Metadata.Subject)
	}
	return info
}

// pdfTextString encodes a Go string as a PDF text string (§7.9.2.2): plain
// bytes when it is printable ASCII, otherwise UTF-16BE with a byte-order mark,
// emitted as a hex string so arbitrary bytes survive serialization.
func pdfTextString(s string) parse.String {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			ascii = false
			break
		}
	}
	if ascii {
		return parse.String{Value: []byte(s)}
	}

	u16 := utf16.Encode([]rune(s))
	buf := make([]byte, 0, 2+len(u16)*2)
	buf = append(buf, 0xFE, 0xFF) // UTF-16BE BOM
	for _, u := range u16 {
		buf = append(buf, byte(u>>8), byte(u))
	}
	return parse.String{Value: buf, Hex: true}
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
// The annotation data layer (AddAnnotation, UpdateAnnotation, RemoveAnnotation,
// GetAnnotations, ExportAnnotationLayer, ImportAnnotationLayer) lives in
// annotation.go; FlattenAnnotations (baking the layer into the page content
// streams) lives in flatten.go.

// --- §7.4 Redaction (Pro) ---
// SECURITY WARNING: redaction must permanently remove content from the content
// stream, not merely paint over it (see .claude/SECURITY.md §1 and spec §13.4).
// RedactText / RedactArea / RedactAndFlatten live in redact.go.

// --- §7.5 Forms (Pro) ---
// GetFormFields, SetFormFieldValue, and FlattenForm live in form.go.

// SignDocument attaches a detached PKCS#7 digital signature to the document via
// an incremental update and returns a new signed document (Pro). The original
// bytes are preserved verbatim (so the signature stays valid); an invisible
// signature field is added. RSA + SHA-256 is supported.
//
// Returns ErrInvalidPDF if the document was not opened from bytes, or an error
// if the certificate/key cannot be parsed or signing fails.
func (d *Document) SignDocument(opts SignatureOptions) (_ *Document, err error) {
	defer guard("SignDocument", -1, &err)
	if d.resolver == nil {
		return nil, ErrInvalidPDF
	}
	signed, err := sign.Sign(d.resolver, sign.Options{
		Reason:      opts.Reason,
		Location:    opts.Location,
		ContactInfo: opts.ContactInfo,
		FieldName:   opts.FieldName,
		CertPEM:     opts.CertPEM,
		KeyPEM:      opts.KeyPEM,
	})
	if err != nil {
		return nil, err
	}
	return OpenBytes(signed)
}

// --- §7.6 Extraction ---

// ExtractText extracts all text in reading order, one page after another
// separated by a form feed (U+000C, the conventional page break). Layout
// preservation (opts.PreserveLayout) is not yet implemented; text is always
// returned in reading order.
func (d *Document) ExtractText(opts ExtractOptions) (string, error) {
	var b strings.Builder
	for i := range d.Pages {
		text, err := d.ExtractTextByPage(i, opts)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteByte('\f') // page separator
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// ExtractTextByPage extracts the text of a single page in reading order.
//
// Returns ErrPageNotFound if pageIndex is out of range.
func (d *Document) ExtractTextByPage(pageIndex int, opts ExtractOptions) (_ string, err error) {
	defer guard("ExtractTextByPage", pageIndex, &err)
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return "", ErrPageNotFound
	}
	if d.resolver == nil || pageIndex >= len(d.pageInfos) {
		return "", errNotImplemented
	}
	injectTestPanic("ExtractTextByPage", pageIndex)
	return extract.PageText(d.resolver, d.pageInfos[pageIndex], extract.Options{
		PreserveLayout: opts.PreserveLayout,
	})
}

// ExtractTables detects and extracts tables on a single page using the
// stream/whitespace method: text runs are grouped into rows by baseline and
// assigned to columns inferred from the widest row. It finds grid-shaped layouts
// (Word/Excel/LaTeX exports) without needing ruled lines; it is a heuristic
// returning at most one dominant grid per page, and returns no tables when the
// page does not look tabular.
//
// Returns ErrPageNotFound if pageIndex is out of range, ErrInvalidPDF if the
// document was not opened from bytes.
func (d *Document) ExtractTables(pageIndex int) (_ []Table, err error) {
	defer guard("ExtractTables", pageIndex, &err)
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return nil, ErrPageNotFound
	}
	if d.resolver == nil || pageIndex >= len(d.pageInfos) {
		return nil, ErrInvalidPDF
	}
	found, err := extract.PageTables(d.resolver, d.pageInfos[pageIndex], extract.Options{})
	if err != nil {
		return nil, err
	}
	tables := make([]Table, len(found))
	for i, t := range found {
		tables[i] = Table{
			PageIndex: pageIndex,
			Rows:      t.Rows,
			Bounds:    Rect{X: t.Bounds[0], Y: t.Bounds[1], Width: t.Bounds[2] - t.Bounds[0], Height: t.Bounds[3] - t.Bounds[1]},
		}
	}
	return tables, nil
}

// OCRPage (Pro) runs OCR on a page: it rasterizes the page and recognizes the
// text with Tesseract. It requires a binary built with the `ocr` build tag
// (Tesseract via CGO); the default pure-Go build returns ErrFeatureRequiresPro.
//
// Returns ErrPageNotFound if pageIndex is out of range, ErrInvalidPDF if the
// document was not opened from bytes, or ErrFeatureRequiresPro when built
// without the `ocr` tag. The actual implementation lives in ocr_enabled.go
// (with the tag) / ocr_stub.go (without).
func (d *Document) OCRPage(pageIndex int) (string, error) {
	if pageIndex < 0 || pageIndex >= len(d.Pages) {
		return "", ErrPageNotFound
	}
	if d.resolver == nil || pageIndex >= len(d.pageInfos) {
		return "", ErrInvalidPDF
	}
	return d.ocrPage(pageIndex)
}

// ExtractStructured (Cloud) extracts the document text and asks the configured
// LLM backend to return it as structured JSON shaped by schema. schema accepts a
// core.Schema, a []core.SchemaField, or a map[string]string (field ->
// description). It returns ErrFeatureRequiresCloud if no AIClient is configured
// (SetAIClient) and ErrInvalidArgument if schema is malformed. The document text
// never leaves the process unless a Cloud backend is explicitly installed.
func (d *Document) ExtractStructured(schema any) (map[string]any, error) {
	s, ok := toSchema(schema)
	if !ok || len(s.Fields) == 0 {
		return nil, ErrInvalidArgument
	}
	d.mu.Lock()
	ai := d.ai
	d.mu.Unlock()
	if ai == nil {
		return nil, ErrFeatureRequiresCloud
	}
	text, err := d.ExtractText(ExtractOptions{})
	if err != nil {
		return nil, err
	}
	return ai.Extract(context.Background(), text, s)
}

// AskDocument (Cloud) answers a natural-language question grounded in the
// document text via the configured LLM backend. It returns
// ErrFeatureRequiresCloud if no AIClient is configured (SetAIClient).
func (d *Document) AskDocument(question string) (string, error) {
	if strings.TrimSpace(question) == "" {
		return "", ErrInvalidArgument
	}
	d.mu.Lock()
	ai := d.ai
	d.mu.Unlock()
	if ai == nil {
		return "", ErrFeatureRequiresCloud
	}
	text, err := d.ExtractText(ExtractOptions{})
	if err != nil {
		return "", err
	}
	return ai.Ask(context.Background(), text, question)
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

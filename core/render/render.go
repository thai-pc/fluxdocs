// Package render rasterizes a parsed PDF page to an image. Wave 1 supports PNG
// and JPEG output of vector graphics (path fills/strokes in RGB/gray/CMYK) and
// text glyphs from embedded TrueType (/FontFile2) and OpenType (/FontFile3)
// fonts, parsed via golang.org/x/image/font/sfnt, and embedded Type 1 fonts
// (/FontFile) and bare CFF fonts (/FontFile3 /Type1C and /CIDFontType0C),
// parsed by core/internal/psfont, plus a common subset of image XObjects (see
// image.go), clipped by W/W* clipping paths, Form XObject bounding boxes
// (clip.go) and text shown in the clipping render modes 4–7 (textclip.go).
// Simple fonts with no embedded program (the standard 14 included) are drawn
// with a bundled substitute face (substitute.go). Type 3 fonts, shadings and
// stroked text are not yet rendered (render modes 1, 2, 5 and 6 fill; 3 and
// 7 are invisible); the interpreter skips what it does not render rather
// than failing, so a page still rasterizes (minus those elements) instead of
// erroring.
//
// Two build targets share this code: the native binary and GOOS=js/GOARCH=wasm.
package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// ErrUnsupportedFormat is returned for an output format the renderer does not
// implement (e.g. "svg" in Wave 1).
var ErrUnsupportedFormat = errors.New("render: unsupported format")

// maxCanvasPixels bounds the output bitmap area to guard against a hostile
// MediaBox or DPI blowing up memory (SECURITY.md §2). 64 megapixels covers an
// A0 page at 300 DPI with margin.
const maxCanvasPixels = 64 << 20

// maxCanvasSide bounds each side of the output bitmap, in pixels: 65,535, the
// largest a JPEG frame can hold. Under the area cap alone a canvas could be
// 1 × 64 Mi px. Encoding such a sliver costs ≈ 5× a square canvas of the same
// area (the PNG encoder's per-row work dominates), and a single row of
// 64 Mi px makes it allocate six row buffers of 192 MiB each. JPEG
// refused it only after the whole render. A page within ISO 32000-1's page
// size limit (Annex C, Table C.1: 14,400 units, 200 in) still renders at up
// to 327 DPI along its longer side (render-budget.md §3).
const maxCanvasSide = 1<<16 - 1

// Options controls a single page render.
type Options struct {
	DPI     int    // dots per inch; <=0 means 150
	Format  string // "png" | "jpeg"
	Quality int    // JPEG quality 1..100; <=0 means 90

	// Annotations, if non-empty, are drawn as a non-destructive overlay on top
	// of the page content (in PDF user space, transformed by the same device
	// matrix). Empty = no overlay, so a page renders identically to a plain
	// content render.
	Annotations []Annotation

	// MaxPaintPixels bounds the raster work of the render in pixel visits
	// (content and overlay; overdraw counts every time). 0 = default: 32 × the
	// canvas pixels, clamped to [16 Mi, 1 Gi]; < 0 = unlimited (unsafe for
	// untrusted input); > 0 = absolute limit.
	MaxPaintPixels int64
	// MaxOperators bounds the interpreter steps: content operators at every
	// Form XObject depth plus every glyph shown or advanced and every TJ
	// element. 0 = default (8 Mi); < 0 = unlimited (unsafe for untrusted input).
	MaxOperators int64

	// Context, when non-nil, cancels the render: it is polled every 2^20
	// raster-work units or 4096 steps, and its deadline is also checked against
	// the clock (required under js/wasm). nil = context.Background(). This is a
	// field rather than a parameter because Page is internal plumbing; the
	// public core API takes ctx as its first parameter.
	Context context.Context

	// testLimits, when non-nil, replaces the resolved limits (including the
	// internal image, content and path caps) so unit tests can trip each
	// counter with a small input. Never set outside tests.
	testLimits *budgetLimits
}

// Page rasterizes one page to encoded image bytes in opts.Format, under the
// per-page budget in opts (SECURITY.md §2).
//
// Returns ErrUnsupportedFormat for a format other than png/jpeg, an error if the
// resolved canvas size is degenerate, ErrCanvasTooLarge (wrapped) if it exceeds
// the pixel cap in area or on a side, ErrBudgetExceeded (wrapped, naming the resource) if the render
// exceeds a limit, the wrapped context error (matching context.Canceled or
// context.DeadlineExceeded) if opts.Context is done before or during the render,
// or a content-decode error. On any error the result is nil: no partial image is
// encoded.
func Page(r *parse.Resolver, pi parse.PageInfo, opts Options) ([]byte, error) {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx) // before any work
	}

	format := opts.Format
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" && format != "jpg" {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, opts.Format)
	}

	dpi := opts.DPI
	if dpi <= 0 {
		dpi = 150
	}
	scale := float64(dpi) / 72.0

	base, pxW, pxH, err := deviceMatrix(pi.MediaBox, pi.Rotate, scale)
	if err != nil {
		return nil, err
	}

	lim := resolveLimits(opts.MaxPaintPixels, opts.MaxOperators, int64(pxW)*int64(pxH))
	if opts.testLimits != nil {
		lim = *opts.testLimits
	}
	b := newBudget(ctx, lim)
	if !b.poll() {
		return nil, b.err // a deadline already past by the clock
	}

	// Clearing the canvas is not raster work (it is not charged), but at the
	// caps it is ≈ 0.3 s native: it polls the context like the paint does.
	rs := newPageRaster(pxW, pxH, b)
	if rs == nil {
		return nil, b.err
	}
	// The page's own content is decoded within what is left of the content
	// counter (and parse.MaxPageContent), so a /Contents array repeating a
	// bomb stops at the cap instead of materializing every copy first.
	allow := b.lim.content - b.content
	content, used, err := r.PageContentLimit(pi, allow)
	if errors.Is(err, parse.ErrDecodeLimit) {
		limit := b.content + min(allow, parse.MaxPageContent)
		b.content += used
		b.trip("content bytes", b.content, limit)
		return nil, b.err
	}
	if err != nil {
		return nil, err
	}
	// Charge the bytes before scanning them: ScanContent materializes every
	// operator up front. used is what PageContentLimit counted (len(content)
	// for content without filter chains).
	if len(content) > 0 && b.chargeContent(used) {
		run(rs, base, parse.ScanContent(content), r, pi.Resources)
	}
	if len(opts.Annotations) > 0 && !b.stopped() {
		// The overlay is never clipped by page content: a page that ends
		// inside an unbalanced W/W* clip (or form) must not hide annotations
		// or redaction previews (clipping-paths.md §4).
		rs.resetClip()
		drawAnnotations(rs, base, opts.Annotations)
	}
	// Poll once more before encoding: the encode is not metered, and a render
	// that ran past its deadline should not spend more time. The encode polls
	// too, at every write (pollWriter): at the canvas caps it is ≈ 1 s native,
	// ≈ 10 s with coverage instrumentation, and a deadline must stop it.
	if !b.poll() {
		return nil, b.err
	}
	out, err := encode(rs, format, opts.Quality, b)
	if b.stopped() {
		return nil, b.err
	}
	return out, err
}

// newPageRaster is newRaster for Page: a canvas of w × h pixels, cleared to
// white a band of rows at a time with a poll of b between bands, and metered
// by b. Returns nil when b stops (a done context) before the canvas is clear.
func newPageRaster(w, h int, b *budget) *raster {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	band := max(pollPaintQuantum/w, 1) * img.Stride // ≈ 1 Mi px per poll
	for at := 0; at < len(img.Pix); at += band {
		if !b.poll() {
			return nil
		}
		row := img.Pix[at:min(at+band, len(img.Pix))]
		for i := range row {
			row[i] = 0xff // R=G=B=A=255
		}
	}
	return &raster{img: img, w: w, h: h, clip: fullClip(w, h), b: b}
}

// deviceMatrix builds the transform from PDF user space (origin bottom-left,
// y up, points) to device pixels (origin top-left, y down), applying the page's
// /Rotate (a clockwise viewing rotation) and the DPI scale. It returns the
// matrix and the output pixel dimensions.
//
// Derivation: normalize to u=x-llx, v=y-lly, then for each rotation map to
// pixels and read off the affine coefficients (px = a·x + c·y + e; py = b·x +
// d·y + f). See render_test.go for the corner mappings this must satisfy.
func deviceMatrix(mb parse.Rectangle, rotate int, s float64) (matrix, int, int, error) {
	w := mb.Width()
	h := mb.Height()
	if w <= 0 || h <= 0 {
		return matrix{}, 0, 0, fmt.Errorf("render: degenerate MediaBox %gx%g", w, h)
	}

	var m matrix
	var pxWf, pxHf float64
	switch rotate {
	case 90:
		m = matrix{0, s, s, 0, -mb.LLY * s, -mb.LLX * s}
		pxWf, pxHf = h*s, w*s
	case 180:
		m = matrix{-s, 0, 0, s, mb.URX * s, -mb.LLY * s}
		pxWf, pxHf = w*s, h*s
	case 270:
		m = matrix{0, -s, -s, 0, mb.URY * s, mb.URX * s}
		pxWf, pxHf = h*s, w*s
	default: // 0
		m = matrix{s, 0, 0, -s, -mb.LLX * s, mb.URY * s}
		pxWf, pxHf = w*s, h*s
	}

	// Bound each side in float64 before converting: a hostile MediaBox (e.g.
	// 1e18 pt) makes the float→int conversion implementation-defined (it
	// saturates under js/wasm) and the int64 area product below overflow. NaN
	// fails the `>= 0` test, +Inf the upper bound; an underflow to 0 is kept
	// and clamped to 1px below. With both sides ≤ maxCanvasSide the int64
	// product cannot overflow.
	if !(pxWf >= 0 && pxHf >= 0) || pxWf > maxCanvasSide || pxHf > maxCanvasSide {
		return matrix{}, 0, 0, fmt.Errorf("%w: %gx%g px exceeds %d pixels per side", ErrCanvasTooLarge, pxWf, pxHf, maxCanvasSide)
	}
	pxW := int(math.Ceil(pxWf))
	pxH := int(math.Ceil(pxHf))
	if pxW < 1 {
		pxW = 1
	}
	if pxH < 1 {
		pxH = 1
	}
	if int64(pxW)*int64(pxH) > maxCanvasPixels {
		return matrix{}, 0, 0, fmt.Errorf("%w: %dx%d px exceeds %d pixels", ErrCanvasTooLarge, pxW, pxH, maxCanvasPixels)
	}
	return m, pxW, pxH, nil
}

// encode serializes the raster as PNG or JPEG bytes, polling b at every write
// of the encoder (pollWriter). Returns b's error when b stops during the
// encode.
func encode(rs *raster, format string, quality int, b *budget) ([]byte, error) {
	var buf bytes.Buffer
	w := pollWriter{buf: &buf, b: b}
	switch format {
	case "jpeg", "jpg":
		if quality <= 0 || quality > 100 {
			quality = 90
		}
		if err := jpeg.Encode(w, rs.img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	default:
		if err := png.Encode(w, rs.img); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// pollWriter is the encoders' destination: each write polls b first and fails
// with b's error once it has stopped, which ends the encode (image/png and
// image/jpeg return the first write error). The PNG encoder writes a chunk
// per 32 KiB of compressed data — tens of polls for a blank canvas at the
// caps, which compresses ≈ 140:1 — and the JPEG encoder every 4 KiB.
type pollWriter struct {
	buf *bytes.Buffer
	b   *budget
}

func (w pollWriter) Write(p []byte) (int, error) {
	if !w.b.poll() {
		return 0, w.b.err
	}
	return w.buf.Write(p)
}

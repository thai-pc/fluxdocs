// Package render rasterizes a parsed PDF page to an image. Wave 1 supports PNG
// and JPEG output of vector graphics (path fills/strokes in RGB/gray/CMYK) and
// text glyphs from embedded TrueType (/FontFile2) and OpenType-CFF (/FontFile3)
// fonts, parsed via golang.org/x/image/font/sfnt, plus a common subset of image
// XObjects (see image.go). Type1 (/FontFile) and non-embedded standard-14 fonts,
// Form XObjects, shadings, and clipping are not yet rendered; the interpreter
// ignores those operators rather than failing, so a page still rasterizes (minus
// those elements) instead of erroring.
//
// Two build targets share this code: the native binary and GOOS=js/GOARCH=wasm.
package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
// the pixel cap, ErrBudgetExceeded (wrapped, naming the resource) if the render
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

	rs := newRaster(pxW, pxH)
	rs.b = b
	content, err := r.PageContent(pi)
	if err != nil {
		return nil, err
	}
	// Charge the bytes before scanning them: ScanContent materializes every
	// operator up front.
	if len(content) > 0 && b.chargeContent(int64(len(content))) {
		fonts := loadFonts(r, pi.Resources, b)
		run(rs, base, parse.ScanContent(content), fonts, r, pi.Resources)
	}
	if len(opts.Annotations) > 0 && !b.stopped() {
		drawAnnotations(rs, base, opts.Annotations)
	}
	// Poll once more before encoding: the encode is not metered, and a render
	// that ran past its deadline should not spend more time.
	if !b.poll() {
		return nil, b.err
	}
	return encode(rs, format, opts.Quality)
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
	// and clamped to 1px below. With both sides ≤ maxCanvasPixels the int64
	// product cannot overflow.
	if !(pxWf >= 0 && pxHf >= 0) || pxWf > maxCanvasPixels || pxHf > maxCanvasPixels {
		return matrix{}, 0, 0, fmt.Errorf("%w: %gx%g px exceeds %d pixels", ErrCanvasTooLarge, pxWf, pxHf, maxCanvasPixels)
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

// encode serializes the raster as PNG or JPEG bytes.
func encode(rs *raster, format string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case "jpeg", "jpg":
		if quality <= 0 || quality > 100 {
			quality = 90
		}
		if err := jpeg.Encode(&buf, rs.img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
	default:
		if err := png.Encode(&buf, rs.img); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

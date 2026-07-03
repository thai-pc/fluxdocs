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
// MediaBox or DPI blowing up memory (SECURITY.md §3). 64 megapixels covers an
// A0 page at 300 DPI with margin.
const maxCanvasPixels = 64 << 20

// Options controls a single page render.
type Options struct {
	DPI     int    // dots per inch; <=0 means 150
	Format  string // "png" | "jpeg"
	Quality int    // JPEG quality 1..100; <=0 means 90
}

// Page rasterizes one page to encoded image bytes in opts.Format.
//
// Returns ErrUnsupportedFormat for a format other than png/jpeg, an error if the
// resolved canvas size is degenerate or too large, or a content-decode error.
func Page(r *parse.Resolver, pi parse.PageInfo, opts Options) ([]byte, error) {
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

	rs := newRaster(pxW, pxH)
	content, err := r.PageContent(pi)
	if err != nil {
		return nil, err
	}
	if len(content) > 0 {
		fonts := loadFonts(r, pi.Resources)
		run(rs, base, parse.ScanContent(content), fonts, r, pi.Resources)
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

	pxW := int(math.Ceil(pxWf))
	pxH := int(math.Ceil(pxHf))
	if pxW < 1 {
		pxW = 1
	}
	if pxH < 1 {
		pxH = 1
	}
	if int64(pxW)*int64(pxH) > maxCanvasPixels {
		return matrix{}, 0, 0, fmt.Errorf("render: canvas %dx%d exceeds %d pixels", pxW, pxH, maxCanvasPixels)
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

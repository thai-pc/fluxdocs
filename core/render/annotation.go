package render

import (
	"image/color"
	"math"
)

// This file draws the FluxDocs annotation layer as a non-destructive overlay on
// top of a rendered page (design: docs/design/annotation-rendering.md). The
// renderer defines its own plain annotation value type because `core` imports
// `render`, so `render` must not import `core` (import cycle). core.RenderPage
// translates its []core.Annotation into []render.Annotation and hands them in
// via render.Options.

// Annotation is the render layer's view of a single annotation to overlay. Rect
// (X,Y,Width,Height) and Points are in PDF user space (origin bottom-left,
// points), matching core.Annotation; they are transformed by the page's device
// matrix so they align with the content.
type Annotation struct {
	Type    string // "highlight" | "note" | "draw" | "shape"
	X       float64
	Y       float64
	Width   float64
	Height  float64
	Color   string // hex, e.g. "#f59e0b"; invalid/empty falls back to amber
	Opacity float64
	Points  []AnnPoint // freehand points for "draw" (PDF space)
}

// AnnPoint is a single freehand point in PDF user space.
type AnnPoint struct{ X, Y float64 }

const (
	// defaultAnnotationColor is the design-token amber (CLAUDE.md §5/§8.2), used
	// when an annotation's Color is empty or unparsable.
	defaultAnnotationColor = "#f59e0b"

	// maxAnnPoints bounds the freehand points drawn for one annotation so a
	// hostile imported Points list cannot make rendering unbounded
	// (SECURITY.md §2–3). blend already bounds writes to the canvas.
	maxAnnPoints = 100000

	annShapeStrokePx = 1.5  // shape outline device width
	annDrawStrokePx  = 2.0  // freehand stroke device width
	annNoteMarkerPx  = 14.0 // note marker device size
)

// drawAnnotations paints every annotation over the raster using base (the PDF
// user space → device pixel matrix already used for the page content). It is
// called after the content is drawn, so annotations sit on top. A malformed
// annotation is skipped, never a panic. The overlay draws through the same
// raster, so it is charged to the page's budget and stops when that is spent.
func drawAnnotations(rs *raster, base matrix, annots []Annotation) {
	for i := range annots {
		if rs.b.stopped() {
			return
		}
		drawAnnotation(rs, base, annots[i])
	}
}

func drawAnnotation(rs *raster, base matrix, a Annotation) {
	col := parseHexColor(a.Color, a.Opacity)
	switch a.Type {
	case "highlight":
		rs.fillPath([][]point{rectCorners(base, a)}, col, false)
	case "shape":
		rs.strokePath([][]point{rectCorners(base, a)}, []bool{true}, col, annShapeStrokePx)
	case "draw":
		drawFreehand(rs, base, a, col)
	case "note":
		drawNoteMarker(rs, base, a, col)
	}
}

// rectCorners returns the four device-space corners of an annotation's Rect
// (bottom-left, bottom-right, top-right, top-left in PDF space).
func rectCorners(base matrix, a Annotation) []point {
	xs := [4][2]float64{
		{a.X, a.Y},
		{a.X + a.Width, a.Y},
		{a.X + a.Width, a.Y + a.Height},
		{a.X, a.Y + a.Height},
	}
	pts := make([]point, 4)
	for i, c := range xs {
		dx, dy := base.apply(c[0], c[1])
		pts[i] = point{dx, dy}
	}
	return pts
}

// drawFreehand strokes the polyline through a draw annotation's Points. Fewer
// than two points draws nothing; the count is capped for safety.
func drawFreehand(rs *raster, base matrix, a Annotation, col color.RGBA) {
	n := len(a.Points)
	if n > maxAnnPoints {
		n = maxAnnPoints
	}
	if n < 2 {
		return
	}
	poly := make([]point, n)
	for i := 0; i < n; i++ {
		dx, dy := base.apply(a.Points[i].X, a.Points[i].Y)
		poly[i] = point{dx, dy}
	}
	rs.strokePath([][]point{poly}, []bool{false}, col, annDrawStrokePx)
}

// drawNoteMarker paints a small solid square at the annotation's top-left corner
// (in PDF space that is (X, Y+Height)); its size is fixed in device pixels so it
// stays legible at any DPI. The note's text Content is not rendered in Wave 1.
func drawNoteMarker(rs *raster, base matrix, a Annotation, col color.RGBA) {
	ax, ay := base.apply(a.X, a.Y+a.Height)
	marker := []point{
		{ax, ay},
		{ax + annNoteMarkerPx, ay},
		{ax + annNoteMarkerPx, ay + annNoteMarkerPx},
		{ax, ay + annNoteMarkerPx},
	}
	rs.fillPath([][]point{marker}, col, false)
}

// parseHexColor parses "#rgb" or "#rrggbb" into an RGBA at the given opacity.
// An empty or malformed color falls back to the default amber; opacity is
// clamped to [0,1] and applied as the alpha channel. Total (never panics).
func parseHexColor(hex string, opacity float64) color.RGBA {
	r, g, b, ok := decodeHexRGB(hex)
	if !ok {
		r, g, b, _ = decodeHexRGB(defaultAnnotationColor)
	}
	a := opacity
	if math.IsNaN(a) || a < 0 {
		a = 0
	} else if a > 1 {
		a = 1
	}
	return color.RGBA{R: r, G: g, B: b, A: uint8(math.Round(a * 255))}
}

// decodeHexRGB decodes "#rgb" or "#rrggbb" (case-insensitive). ok is false for
// any other form.
func decodeHexRGB(s string) (r, g, b uint8, ok bool) {
	if len(s) == 0 || s[0] != '#' {
		return 0, 0, 0, false
	}
	body := s[1:]
	switch len(body) {
	case 3: // #rgb → each nibble doubled
		rn, ok1 := hexNibble(body[0])
		gn, ok2 := hexNibble(body[1])
		bn, ok3 := hexNibble(body[2])
		if !ok1 || !ok2 || !ok3 {
			return 0, 0, 0, false
		}
		return rn * 17, gn * 17, bn * 17, true
	case 6:
		rv, ok1 := hexByte(body[0], body[1])
		gv, ok2 := hexByte(body[2], body[3])
		bv, ok3 := hexByte(body[4], body[5])
		if !ok1 || !ok2 || !ok3 {
			return 0, 0, 0, false
		}
		return rv, gv, bv, true
	default:
		return 0, 0, 0, false
	}
}

func hexByte(hi, lo byte) (uint8, bool) {
	h, ok1 := hexNibble(hi)
	l, ok2 := hexNibble(lo)
	if !ok1 || !ok2 {
		return 0, false
	}
	return h<<4 | l, true
}

func hexNibble(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

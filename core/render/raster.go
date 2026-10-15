package render

import (
	"image"
	"image/color"
	"math"
	"sort"
)

// matrix is a 2x3 affine transform [a b c d e f] representing
// [[a b 0], [c d 0], [e f 1]]. A point is a row vector: (x', y') = (x, y) * M.
type matrix [6]float64

func identity() matrix { return matrix{1, 0, 0, 1, 0, 0} }

func translate(tx, ty float64) matrix { return matrix{1, 0, 0, 1, tx, ty} }

// mul returns m * n (apply m first, then n).
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// apply transforms point (x, y) by m.
func (m matrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// invert returns the inverse of the affine transform m. ok is false when the
// determinant is near zero (a degenerate/singular transform, e.g. a zero-area
// image placement), so callers can skip rather than divide by ~0.
func (m matrix) invert() (matrix, bool) {
	det := m[0]*m[3] - m[1]*m[2]
	if math.Abs(det) < 1e-12 {
		return matrix{}, false
	}
	ia := m[3] / det
	ib := -m[1] / det
	ic := -m[2] / det
	id := m[0] / det
	return matrix{
		ia, ib,
		ic, id,
		-(m[4]*ia + m[5]*ic),
		-(m[4]*ib + m[5]*id),
	}, true
}

// scaleFactor is the average axis scale of m, used to turn a user-space line
// width into a device pixel width.
func (m matrix) scaleFactor() float64 {
	sx := math.Hypot(m[0], m[1])
	sy := math.Hypot(m[2], m[3])
	return (sx + sy) / 2
}

// point is a device-space coordinate in pixels.
type point struct{ X, Y float64 }

// clipRect is an axis-aligned, half-open device-pixel clip rectangle
// [x0,x1)×[y0,y1). It is the only clipping the Wave 1 rasterizer models, and it
// exists solely to bound a Form XObject to its /BBox (see form.go); general
// path clipping via W/W* is still ignored. A rectangle is "empty" when it has no
// interior, in which case no pixel passes the clip.
type clipRect struct{ x0, y0, x1, y1 int }

// fullClip returns the clip covering the whole w×h canvas (no restriction).
func fullClip(w, h int) clipRect { return clipRect{0, 0, w, h} }

// intersect returns the overlap of two clip rectangles. The result may be empty.
func (c clipRect) intersect(o clipRect) clipRect {
	return clipRect{
		x0: maxInt(c.x0, o.x0),
		y0: maxInt(c.y0, o.y0),
		x1: minInt(c.x1, o.x1),
		y1: minInt(c.y1, o.y1),
	}
}

// empty reports whether the rectangle has no interior (nothing can be painted).
func (c clipRect) empty() bool { return c.x0 >= c.x1 || c.y0 >= c.y1 }

// contains reports whether device pixel (x, y) lies inside the clip.
func (c clipRect) contains(x, y int) bool {
	return x >= c.x0 && x < c.x1 && y >= c.y0 && y < c.y1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// raster is an RGBA drawing surface. Pixels are premultiplied-free (opaque
// output); alpha compositing uses source-over.
type raster struct {
	img  *image.RGBA
	w, h int

	// clip is the active device-space clip rectangle enforced by blend, kept in
	// sync with the interpreter's graphics-state clip. It starts as the full
	// canvas and is narrowed only while a Form XObject's /BBox is in effect. A
	// page is interpreted sequentially, so mutating this shared field is safe.
	clip clipRect
}

// newRaster returns a w×h surface filled with opaque white (the default PDF
// page background), with the clip set to the whole canvas.
func newRaster(w, h int) *raster {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff // R=G=B=A=255
	}
	return &raster{img: img, w: w, h: h, clip: fullClip(w, h)}
}

// blend composites src (source-over) onto the pixel at (x, y). Pixels outside
// the canvas bounds or the active clip rectangle are rejected — the single choke
// point through which fills, strokes, glyphs, and images honour the clip.
func (r *raster) blend(x, y int, src color.RGBA) {
	if x < 0 || y < 0 || x >= r.w || y >= r.h {
		return
	}
	if !r.clip.contains(x, y) {
		return
	}
	if src.A == 0 {
		return
	}
	if src.A == 0xff {
		r.img.SetRGBA(x, y, src)
		return
	}
	o := r.img.RGBAAt(x, y)
	a := float64(src.A) / 255
	r.img.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(src.R)*a + float64(o.R)*(1-a)),
		G: uint8(float64(src.G)*a + float64(o.G)*(1-a)),
		B: uint8(float64(src.B)*a + float64(o.B)*(1-a)),
		A: 0xff,
	})
}

// fillPath fills the subpaths (already in device space) with col, using the
// even-odd rule when evenOdd is true, otherwise the nonzero winding rule. It
// uses a simple scanline algorithm sampling at pixel-row centers.
func (r *raster) fillPath(subpaths [][]point, col color.RGBA, evenOdd bool) {
	if len(subpaths) == 0 {
		return
	}
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, sp := range subpaths {
		for _, p := range sp {
			// A NaN point (e.g. Inf×0 from an overflowing CTM) has no place on
			// the canvas, so the path's area is undefined: paint nothing. The
			// check must be explicit, as math.Max(NaN, +Inf) is +Inf.
			if math.IsNaN(p.X) || math.IsNaN(p.Y) {
				return
			}
			minY = math.Min(minY, p.Y)
			maxY = math.Max(maxY, p.Y)
		}
	}
	// Clamp in float64 before converting (see clampToInt), so a huge path
	// covers the same rows on every target.
	y0 := clampToInt(math.Floor(minY), 0, float64(r.h))
	y1 := clampToInt(math.Ceil(maxY), 0, float64(r.h))

	type edge struct{ x, dir float64 }
	for y := y0; y < y1; y++ {
		sy := float64(y) + 0.5
		var xs []edge
		located := true
		for _, sp := range subpaths {
			n := len(sp)
			if n < 2 {
				continue
			}
			for i := 0; i < n; i++ {
				a := sp[i]
				b := sp[(i+1)%n] // implicitly close each subpath
				if a.Y == b.Y {
					continue
				}
				// Half-open [min,max) test avoids double-counting shared vertices.
				if (sy >= a.Y && sy < b.Y) || (sy >= b.Y && sy < a.Y) {
					// A vertical edge crosses at its own x. Taking it directly
					// also keeps an edge at x=±Inf exact, where the general
					// form computes Inf−Inf = NaN.
					x := a.X
					if b.X != a.X {
						t := (sy - a.Y) / (b.Y - a.Y)
						x = a.X + t*(b.X-a.X)
					}
					if math.IsNaN(x) {
						located = false
					}
					dir := 1.0
					if b.Y < a.Y {
						dir = -1.0
					}
					xs = append(xs, edge{x: x, dir: dir})
				}
			}
		}
		// A crossing that cannot be located (NaN, e.g. from an Inf−Inf
		// interpolation) would also break the sort order the pairing relies on:
		// the row gets no ink rather than guessed spans.
		if len(xs) < 2 || !located {
			continue
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })

		if evenOdd {
			for i := 0; i+1 < len(xs); i += 2 {
				r.span(xs[i].x, xs[i+1].x, y, col)
			}
		} else {
			wind := 0
			for i := 0; i+1 < len(xs); i++ {
				wind += int(xs[i].dir)
				if wind != 0 {
					r.span(xs[i].x, xs[i+1].x, y, col)
				}
			}
		}
	}
}

// span fills a horizontal run of pixels [x0, x1) on row y, bounds rounded to the
// nearest pixel. It clamps to the active clip x-range for speed; blend re-checks
// each pixel. A NaN bound has no position on the row and paints nothing.
func (r *raster) span(x0, x1 float64, y int, col color.RGBA) {
	if y < r.clip.y0 || y >= r.clip.y1 {
		return
	}
	if math.IsNaN(x0) || math.IsNaN(x1) {
		return
	}
	lo, hi := float64(r.clip.x0), float64(r.clip.x1)
	ix0 := clampToInt(math.Round(x0), lo, hi)
	ix1 := clampToInt(math.Round(x1), lo, hi)
	for x := ix0; x < ix1; x++ {
		r.blend(x, y, col)
	}
}

// strokePath draws the subpaths as connected line segments of the given device
// width (minimum 1px). Wave 1 uses square-capped segments without joins, which
// is adequate for thin lines and table rules.
func (r *raster) strokePath(subpaths [][]point, closed []bool, col color.RGBA, width float64) {
	if width < 1 {
		width = 1
	}
	for si, sp := range subpaths {
		n := len(sp)
		for i := 0; i+1 < n; i++ {
			r.line(sp[i], sp[i+1], col, width)
		}
		if si < len(closed) && closed[si] && n > 2 {
			r.line(sp[n-1], sp[0], col, width)
		}
	}
}

// line draws a thick line between a and b: the union of axis-aligned squares of
// side width (≥ 1, see strokePath) centred on steps+1 evenly spaced samples,
// steps = ⌈length⌉+1, so consecutive samples are under 1px apart.
//
// Coordinates and width come from an untrusted content stream (SECURITY.md §2),
// so the work is bounded by the canvas, never by the input:
//   - non-finite input is dropped before any float→int conversion (those are
//     implementation-defined and saturate under js/wasm);
//   - the segment is clipped to the paintable box grown by half+1, the only
//     region where a sample's square can reach a pixel;
//   - the union is painted row by row instead of stamp by stamp. The samples
//     whose square reaches a row form one contiguous index range (cy is
//     monotonic in the sample index), found by bisection, and their squares
//     overlap along x (samples <1px apart; width ≥1 makes each square's pixel
//     footprint floor(c−h)..ceil(c+h) span ≥2 pixels), so the row's ink
//     is the single span between the first and last such sample.
//
// Work per segment is O(rows × (log steps + span)) ≤ O(canvas pixels), whatever
// the segment length, coordinate magnitude or line width. Each pixel is
// composited once per segment; for opaque color the result is pixel-identical
// to stamping every sample.
func (r *raster) line(a, b point, col color.RGBA, width float64) {
	if !isFinite(a.X) || !isFinite(a.Y) || !isFinite(b.X) || !isFinite(b.Y) || !isFinite(width) {
		return // e.g. an overflowing cm: such a point has no place on the canvas
	}
	box := r.clip.intersect(fullClip(r.w, r.h))
	if box.empty() {
		return
	}
	half := width / 2
	// A sample's square spans pixels floor(c−half)..ceil(c+half), so it reaches
	// box only when c lies in (x0−1−half, x1+half), likewise for y.
	a, b, ok := clipSegment(a, b,
		float64(box.x0)-1-half, float64(box.y0)-1-half,
		float64(box.x1)+half, float64(box.y1)+half)
	if !ok {
		return
	}
	dx := b.X - a.X
	dy := b.Y - a.Y
	// steps is an integer-valued float64: after clipping it is bounded by the
	// box size plus the width, but may still exceed the int range for an
	// absurd width. Only a width near MaxFloat64 can overflow it.
	steps := math.Ceil(math.Hypot(dx, dy)) + 1
	if math.IsInf(steps, 0) {
		return
	}
	cx := func(s float64) float64 { return a.X + s/steps*dx }
	cy := func(s float64) float64 { return a.Y + s/steps*dy }

	// Rows any sample can reach, clamped in float64 first: when yLo ≤ yHi both
	// lie inside box, so the int conversions are exact on every platform.
	yLo := math.Max(math.Floor(math.Min(a.Y, b.Y)-half), float64(box.y0))
	yHi := math.Min(math.Ceil(math.Max(a.Y, b.Y)+half), float64(box.y1-1))
	if yLo > yHi {
		return
	}
	for oy := int(yLo); oy <= int(yHi); oy++ {
		fy := float64(oy)
		// A sample's square reaches row oy iff it starts at or above the row
		// (above) and ends at or below it (below). As cy moves monotonically one
		// predicate turns true and the other false along the segment (both are
		// constant when dy == 0).
		above := func(s float64) bool { return math.Floor(cy(s)-half) <= fy }
		below := func(s float64) bool { return fy <= math.Ceil(cy(s)+half) }
		var lo, hi float64
		if dy >= 0 {
			lo, ok = firstTrue(steps, below)
			hi = lastTrue(steps, above)
		} else {
			lo, ok = firstTrue(steps, above)
			hi = lastTrue(steps, below)
		}
		if !ok || hi < lo || !above(lo) || !below(lo) {
			continue
		}
		x0, x1 := cx(lo), cx(hi)
		xLo := math.Max(math.Floor(math.Min(x0, x1)-half), float64(box.x0))
		xHi := math.Min(math.Ceil(math.Max(x0, x1)+half), float64(box.x1-1))
		if xLo > xHi {
			continue
		}
		for ox := int(xLo); ox <= int(xHi); ox++ {
			r.blend(ox, oy, col)
		}
	}
}

// firstTrue returns the smallest integer-valued s in [0, n] for which pred
// holds, given pred is monotonic (false…false true…true) over that range; ok is
// false when pred(n) is false. It bisects, so it costs O(log n) calls even when
// n exceeds the int range.
func firstTrue(n float64, pred func(float64) bool) (s float64, ok bool) {
	if !pred(n) {
		return 0, false
	}
	if pred(0) {
		return 0, true
	}
	lo, hi := 0.0, n // pred(lo) false, pred(hi) true
	for hi-lo > 1 {
		mid := math.Floor(lo + (hi-lo)/2)
		if mid <= lo || mid >= hi {
			break // no integer-valued float64 strictly between lo and hi
		}
		if pred(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi, true
}

// lastTrue returns the largest integer-valued s in [0, n] for which pred holds,
// given pred is monotonic (true…true false…false); it returns -1 when pred(0) is
// false. Like firstTrue it bisects.
func lastTrue(n float64, pred func(float64) bool) float64 {
	s, ok := firstTrue(n, func(s float64) bool { return !pred(s) })
	if !ok {
		return n // pred holds everywhere
	}
	if s == 0 {
		return -1
	}
	return s - 1
}

// clipSegment clips segment ab to the closed rectangle [x0,x1]×[y0,y1]
// (Liang–Barsky); ok is false when no part of it lies inside. An endpoint that
// is already inside is returned bit-for-bit, so a segment that needs no
// clipping rasterizes exactly as before. A moved endpoint is snapped onto the
// edge it was clipped at and clamped into the rectangle, so float rounding at
// huge magnitudes cannot carry it off the canvas.
func clipSegment(a, b point, x0, y0, x1, y1 float64) (point, point, bool) {
	dx := b.X - a.X
	dy := b.Y - a.Y
	if !isFinite(dx) || !isFinite(dy) {
		return a, b, false // extent overflows float64: no pixel-accurate position
	}
	t0, t1 := 0.0, 1.0
	e0, e1 := -1, -1 // edge that set t0 / t1
	pq := [4][2]float64{{-dx, a.X - x0}, {dx, x1 - a.X}, {-dy, a.Y - y0}, {dy, y1 - a.Y}}
	for i, e := range pq {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return a, b, false // parallel to this edge and outside it
			}
			continue
		}
		t := q / p
		if p < 0 {
			if t > t1 {
				return a, b, false
			}
			if t > t0 {
				t0, e0 = t, i
			}
		} else {
			if t < t0 {
				return a, b, false
			}
			if t < t1 {
				t1, e1 = t, i
			}
		}
	}
	at := func(t float64, edge int) point {
		p := point{a.X + t*dx, a.Y + t*dy}
		switch edge {
		case 0:
			p.X = x0
		case 1:
			p.X = x1
		case 2:
			p.Y = y0
		case 3:
			p.Y = y1
		}
		p.X = math.Min(math.Max(p.X, x0), x1)
		p.Y = math.Min(math.Max(p.Y, y0), y1)
		return p
	}
	na, nb := a, b
	if e0 >= 0 {
		na = at(t0, e0)
	}
	if e1 >= 0 {
		nb = at(t1, e1)
	}
	return na, nb, true
}

// isFinite reports whether v is neither NaN nor ±Inf.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// clampToInt converts v to an int clamped to [lo, hi], clamping in float64
// first. Go leaves the conversion of an out-of-range float (including ±Inf and
// NaN) implementation-defined: amd64 yields MinInt64 while js/wasm and arm64
// saturate, so converting untrusted geometry directly makes native and
// in-browser renders differ. NaN maps to lo, which callers use as "empty": a
// bound pair that is NaN collapses to [lo, lo) and paints nothing. lo and hi
// must be integer-valued and within the int range.
func clampToInt(v, lo, hi float64) int {
	if !(v > lo) { // also catches NaN
		return int(lo)
	}
	if v > hi {
		return int(hi)
	}
	return int(v)
}

// flattenCubic appends a polyline approximation of the cubic Bézier from p0
// through control points p1, p2 to p3 (all device space) to dst. The endpoint
// p3 is included; p0 is assumed already present.
func flattenCubic(dst []point, p0, p1, p2, p3 point) []point {
	const segments = 16
	for i := 1; i <= segments; i++ {
		t := float64(i) / segments
		u := 1 - t
		a := u * u * u
		b := 3 * u * u * t
		c := 3 * u * t * t
		d := t * t * t
		dst = append(dst, point{
			X: a*p0.X + b*p1.X + c*p2.X + d*p3.X,
			Y: a*p0.Y + b*p1.Y + c*p2.Y + d*p3.Y,
		})
	}
	return dst
}

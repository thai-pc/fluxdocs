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

// scaleFactor is the average axis scale of m, used to turn a user-space line
// width into a device pixel width.
func (m matrix) scaleFactor() float64 {
	sx := math.Hypot(m[0], m[1])
	sy := math.Hypot(m[2], m[3])
	return (sx + sy) / 2
}

// point is a device-space coordinate in pixels.
type point struct{ X, Y float64 }

// raster is an RGBA drawing surface. Pixels are premultiplied-free (opaque
// output); alpha compositing uses source-over.
type raster struct {
	img  *image.RGBA
	w, h int
}

// newRaster returns a w×h surface filled with opaque white (the default PDF
// page background).
func newRaster(w, h int) *raster {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff // R=G=B=A=255
	}
	return &raster{img: img, w: w, h: h}
}

// blend composites src (source-over) onto the pixel at (x, y).
func (r *raster) blend(x, y int, src color.RGBA) {
	if x < 0 || y < 0 || x >= r.w || y >= r.h {
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
			minY = math.Min(minY, p.Y)
			maxY = math.Max(maxY, p.Y)
		}
	}
	y0 := int(math.Floor(minY))
	y1 := int(math.Ceil(maxY))
	if y0 < 0 {
		y0 = 0
	}
	if y1 > r.h {
		y1 = r.h
	}

	type edge struct{ x, dir float64 }
	for y := y0; y < y1; y++ {
		sy := float64(y) + 0.5
		var xs []edge
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
					t := (sy - a.Y) / (b.Y - a.Y)
					x := a.X + t*(b.X-a.X)
					dir := 1.0
					if b.Y < a.Y {
						dir = -1.0
					}
					xs = append(xs, edge{x: x, dir: dir})
				}
			}
		}
		if len(xs) < 2 {
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

// span fills a horizontal run of pixels [x0, x1) on row y.
func (r *raster) span(x0, x1 float64, y int, col color.RGBA) {
	ix0 := int(math.Round(x0))
	ix1 := int(math.Round(x1))
	if ix0 < 0 {
		ix0 = 0
	}
	if ix1 > r.w {
		ix1 = r.w
	}
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

// line draws a thick line between a and b by sampling along its length and
// stamping a square of side width at each sample.
func (r *raster) line(a, b point, col color.RGBA, width float64) {
	dx := b.X - a.X
	dy := b.Y - a.Y
	length := math.Hypot(dx, dy)
	half := width / 2
	steps := int(math.Ceil(length)) + 1
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		cx := a.X + t*dx
		cy := a.Y + t*dy
		for oy := int(math.Floor(cy - half)); oy <= int(math.Ceil(cy+half)); oy++ {
			for ox := int(math.Floor(cx - half)); ox <= int(math.Ceil(cx+half)); ox++ {
				r.blend(ox, oy, col)
			}
		}
	}
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

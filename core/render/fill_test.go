package render

import (
	"bytes"
	"fmt"
	"image/color"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

// fillPathFullScan is the pre-active-edge fillPath, kept verbatim as the
// reference: every row scans every edge. The active-edge version must paint the
// same pixels (render-budget.md §2.1).
func fillPathFullScan(r *raster, subpaths [][]point, col color.RGBA, evenOdd bool) {
	if len(subpaths) == 0 {
		return
	}
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, sp := range subpaths {
		for _, p := range sp {
			if math.IsNaN(p.X) || math.IsNaN(p.Y) {
				return
			}
			minY = math.Min(minY, p.Y)
			maxY = math.Max(maxY, p.Y)
		}
	}
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
				b := sp[(i+1)%n]
				if a.Y == b.Y {
					continue
				}
				if (sy >= a.Y && sy < b.Y) || (sy >= b.Y && sy < a.Y) {
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

// TestFill_ActiveEdgeMatchesFullScan fills random paths — many subpaths,
// self-intersections, shared vertices on row centers, degenerate and
// off-canvas edges, infinities — with both algorithms and both fill rules, with
// translucent color so a pixel painted twice would show, and requires identical
// pixels. The wide canvas exercises the counting sort of edges by entry row; the
// tall one (rows far beyond 2E+64) the comparison-sort fallback.
func TestFill_ActiveEdgeMatchesFullScan(t *testing.T) {
	for _, size := range []struct{ w, h, iters int }{{64, 48, 3000}, {12, 900, 300}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			fillMatchesFullScan(t, size.w, size.h, size.iters)
		})
	}
}

func fillMatchesFullScan(t *testing.T, w, h, iters int) {
	rng := rand.New(rand.NewPCG(7, uint64(h)))
	coord := func(limit float64) float64 {
		switch rng.IntN(20) {
		case 0:
			return math.Round(rng.Float64()*limit) + 0.5 // exactly on a row/column center
		case 1:
			return math.Round(rng.Float64() * limit) // on a pixel boundary
		case 2:
			return (rng.Float64()*3 - 1) * limit * 10 // far off the canvas
		case 3:
			return math.Inf(1 - 2*rng.IntN(2))
		default:
			return (rng.Float64()*1.4 - 0.2) * limit
		}
	}
	col := color.RGBA{200, 30, 90, 0x80}
	for iter := 0; iter < iters; iter++ {
		var subpaths [][]point
		for s := rng.IntN(4) + 1; s > 0; s-- {
			var sp []point
			for n := rng.IntN(12); n > 0; n-- {
				p := point{coord(float64(w)), coord(float64(h))}
				if len(sp) > 0 && rng.IntN(6) == 0 {
					p.Y = sp[len(sp)-1].Y // horizontal edge
				}
				sp = append(sp, p)
			}
			subpaths = append(subpaths, sp)
		}
		for _, evenOdd := range []bool{false, true} {
			want := newRaster(w, h)
			fillPathFullScan(want, subpaths, col, evenOdd)
			got := newRaster(w, h)
			got.fillPath(subpaths, col, evenOdd)
			if !bytes.Equal(want.img.Pix, got.img.Pix) {
				t.Fatalf("iter %d evenOdd=%v: pixels differ for subpaths %v", iter, evenOdd, subpaths)
			}
		}
	}
}

// TestFill_ScratchReuse checks the reused per-raster buffers do not leak edges
// between fills: a big path followed by a small one paints only the small one.
func TestFill_ScratchReuse(t *testing.T) {
	rs := newRaster(20, 20)
	big := [][]point{{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, {{1, 1}, {19, 1}, {19, 19}, {1, 19}}}
	rs.fillPath(big, color.RGBA{0, 0, 0, 0}, false) // transparent: paints nothing
	rs.fillPath([][]point{{{5, 5}, {6, 5}, {6, 6}, {5, 6}}}, color.RGBA{0, 0, 0, 0xff}, false)
	want := newRaster(20, 20)
	fillPathFullScan(want, [][]point{{{5, 5}, {6, 5}, {6, 6}, {5, 6}}}, color.RGBA{0, 0, 0, 0xff}, false)
	if !bytes.Equal(want.img.Pix, rs.img.Pix) {
		t.Error("second fill differs: scratch state leaked between calls")
	}
}

// BenchmarkFillManyEdges fills a 50k-vertex thin polygon spanning 1650 rows
// (Letter at 150 DPI), the GIS/CAD case that motivated the active-edge table.
func BenchmarkFillManyEdges(b *testing.B) {
	const n, rows = 50000, 1650
	poly := make([]point, 0, n)
	for i := 0; i < n/2; i++ { // down the left side, back up the right
		poly = append(poly, point{100 + float64(i%7)*0.1, float64(i) * rows / (n / 2)})
	}
	for i := n/2 - 1; i >= 0; i-- {
		poly = append(poly, point{110 + float64(i%5)*0.1, float64(i) * rows / (n / 2)})
	}
	sub := [][]point{poly}
	col := color.RGBA{0, 0, 0, 0xff}
	b.Run("active-edge", func(b *testing.B) {
		rs := newRaster(1275, rows)
		for i := 0; i < b.N; i++ {
			rs.fillPath(sub, col, false)
		}
	})
	b.Run("full-scan", func(b *testing.B) {
		rs := newRaster(1275, rows)
		for i := 0; i < b.N; i++ {
			fillPathFullScan(rs, sub, col, false)
		}
	})
}

package render

import (
	"math"
	"math/bits"
)

// This file implements clipping paths, the W and W* operators (ISO 32000-1
// §8.5.4; design docs/design/clipping-paths.md). A clip is a device rectangle,
// optionally intersected with an immutable 1-bit mask over the clip's
// bounding box. Rectangles, the common case, allocate nothing; a mask is built
// once per non-rectangular clip and shared by pointer between the graphics
// states that hold it, so q copies it in O(1).
//
// Pixel rule (§2.1): a pixel lies inside a clip path exactly when fillPath
// would paint it for that path and winding rule. The mask is filled by the
// same scan (scanPath) and rounding (spanPixels) as a fill, and the rectangle
// fast path reproduces both, so a clip and a fill of one path cover the same
// pixels.
//
// Liveness (§3.4): a mask built while the graphics-state stack holds d
// entries can be referenced only by the current state and by stack entries
// at index ≥ d, so the interpreter knows exactly when a mask dies without
// reference counts. Live masks are charged to the budget's clip-mask counter
// (maxClipMaskBytes), and every mask build to raster work, so a hostile
// stream of clips is bounded in memory and time.

// clipState is the clip of a graphics state: the pixels inside rect and, when
// mask is non-nil, set in mask. rect is then within the mask's bounds (after a
// build it is the tight box of its set bits), so rect alone bounds what the
// clip lets through. The value is copied by q and by a form's saved state; the
// mask is shared and never mutated.
type clipState struct {
	rect clipRect
	mask *clipMask // nil: no clip beyond rect
}

// Values of interp.pendingClip.
const (
	clipNone    = 0
	clipNonzero = 1 // W
	clipEvenOdd = 2 // W*
)

// fullClipState returns the clip of the whole w×h canvas.
func fullClipState(w, h int) clipState { return clipState{rect: fullClip(w, h)} }

// empty reports whether no pixel passes the clip.
func (c clipState) empty() bool { return c.rect.empty() }

// clipMask is a 1-bit device mask over rows [y0, y1) and the 64-pixel word
// columns [x0w, x1w). Bit x&63 of word x>>6 is pixel x: words are aligned to
// canvas-global columns, so two masks intersect word by word without
// shifting. It is immutable once built.
type clipMask struct {
	x0w, x1w int
	y0, y1   int
	stride   int      // words per row, x1w - x0w
	bits     []uint64 // stride words per row
	rowX     []int32  // per row, [min, max) of the set columns; min == max if none
	depth    int      // stack length when built (liveness)
	bytes    int64    // charged to the live clip-mask counter
}

// bit reports whether pixel (x, y) is set. Pixels outside the mask's bounds
// are not.
func (m *clipMask) bit(x, y int) bool {
	// One unsigned compare per axis also rejects a negative row or column.
	row, w := y-m.y0, x>>6-m.x0w
	if uint(row) >= uint(m.y1-m.y0) || uint(w) >= uint(m.stride) {
		return false
	}
	return m.bits[row*m.stride+w]>>(uint(x)&63)&1 != 0
}

// word returns the mask word at canvas word column gx of row y, 0 outside the
// mask's bounds.
func (m *clipMask) word(y, gx int) uint64 {
	if y < m.y0 || y >= m.y1 || gx < m.x0w || gx >= m.x1w {
		return 0
	}
	return m.bits[(y-m.y0)*m.stride+gx-m.x0w]
}

// rowRange returns the columns [lo, hi) holding the set bits of row y
// (lo == hi when the row has none or lies outside the mask).
func (m *clipMask) rowRange(y int) (lo, hi int) {
	if y < m.y0 || y >= m.y1 {
		return 0, 0
	}
	i := 2 * (y - m.y0)
	return int(m.rowX[i]), int(m.rowX[i+1])
}

// setRun sets the bits of pixels [x0, x1) on row y; the run must lie inside
// the mask's bounds and be non-empty.
func (m *clipMask) setRun(y, x0, x1 int) {
	row := m.bits[(y-m.y0)*m.stride : (y-m.y0+1)*m.stride]
	w0, w1 := x0>>6-m.x0w, (x1-1)>>6-m.x0w
	lo := ^uint64(0) << (uint(x0) & 63)
	hi := ^uint64(0) >> (63 - (uint(x1-1) & 63))
	if w0 == w1 {
		row[w0] |= lo & hi
		return
	}
	row[w0] |= lo
	for w := w0 + 1; w < w1; w++ {
		row[w] = ^uint64(0)
	}
	row[w1] |= hi
}

// rectPath reports whether the device path is one rectangle with axis-aligned
// edges (§3.2): a single subpath of four points (or five, the last repeating
// the first) whose consecutive points share x or y exactly, alternately, all
// finite. It returns the rectangle's extremes. re, and m l l l h forms of one,
// qualify under any transform that keeps the axes (/Rotate 90, 180, 270
// included).
func rectPath(subpaths [][]point) (minX, minY, maxX, maxY float64, ok bool) {
	if len(subpaths) != 1 {
		return 0, 0, 0, 0, false
	}
	p := subpaths[0]
	if len(p) == 5 && p[4] == p[0] {
		p = p[:4]
	}
	if len(p) != 4 {
		return 0, 0, 0, 0, false
	}
	for _, q := range p {
		if !isFinite(q.X) || !isFinite(q.Y) {
			return 0, 0, 0, 0, false
		}
	}
	hv := p[0].Y == p[1].Y && p[1].X == p[2].X && p[2].Y == p[3].Y && p[3].X == p[0].X
	vh := p[0].X == p[1].X && p[1].Y == p[2].Y && p[2].X == p[3].X && p[3].Y == p[0].Y
	if !hv && !vh {
		return 0, 0, 0, 0, false
	}
	minX, maxX = math.Min(p[0].X, p[2].X), math.Max(p[0].X, p[2].X)
	minY, maxY = math.Min(p[0].Y, p[2].Y), math.Max(p[0].Y, p[2].Y)
	return minX, minY, maxX, maxY, true
}

// rectPixels returns the pixels fillPath paints for an axis-aligned rectangle
// with these finite extremes on a w×h canvas, by the §2.1 rule: rows whose
// center y+0.5 lies in [minY, maxY), found with fillStartRow's exact
// predicate, and columns [round(minX), round(maxX)) as spanPixels rounds
// them. The result may be empty.
func rectPixels(minX, minY, maxX, maxY float64, w, h int) clipRect {
	y0 := clampToInt(math.Floor(minY), 0, float64(h))
	y1 := clampToInt(math.Ceil(maxY), 0, float64(h))
	if y0 >= y1 {
		return clipRect{}
	}
	ry0 := fillStartRow(minY, y0, y1) // first row with y+0.5 >= minY
	ry1 := fillStartRow(maxY, y0, y1) // first row with y+0.5 >= maxY: past the last
	ix0, ix1, ok := spanPixels(minX, maxX, 0, w)
	if !ok || ry0 >= ry1 {
		return clipRect{}
	}
	return clipRect{x0: ix0, y0: ry0, x1: ix1, y1: ry1}
}

// clipWords is the raster work of n mask words in one pass (zeroing, OR, AND,
// extent scan): clipWordsPerUnit words per unit, rounded up.
func clipWords(n int) int64 { return (int64(n) + clipWordsPerUnit - 1) / clipWordsPerUnit }

// buildClip returns cur intersected with the inside of the device path under
// the winding rule (evenOdd: even-odd, else nonzero), as a new mask built at
// stack depth depth. It is the general path of a W/W* clip; rectangles take
// rectPixels instead.
//
// The mask covers the rows and columns the path touches within cur.rect, so
// a small clip stays small. Its words and rows are charged to raster work and
// its bytes to the live clip-mask counter, both before the allocation
// (SECURITY.md §2); a trip returns cur unchanged and stops the render. A path
// fill would paint nothing for (no area, a NaN point, nothing inside cur)
// yields an empty clip.
func (r *raster) buildClip(cur clipState, subpaths [][]point, evenOdd bool, depth int) clipState {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, sp := range subpaths {
		for _, p := range sp {
			if math.IsNaN(p.X) || math.IsNaN(p.Y) {
				return clipState{} // fill paints nothing for a NaN path (§2.1)
			}
			minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
			minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
		}
	}
	// Columns: every span lies between round(minX) and round(maxX), so the
	// floor/ceil box holds them; rows as scanPath clamps them.
	box := cur.rect.intersect(clipRect{
		x0: clampToInt(math.Floor(minX), 0, float64(r.w)),
		y0: clampToInt(math.Floor(minY), 0, float64(r.h)),
		x1: clampToInt(math.Ceil(maxX), 0, float64(r.w)),
		y1: clampToInt(math.Ceil(maxY), 0, float64(r.h)),
	})
	if box.empty() {
		return clipState{}
	}
	x0w, x1w := box.x0>>6, (box.x1+63)>>6
	stride, rows := x1w-x0w, box.y1-box.y0
	// box lies inside the canvas (≤ maxCanvasPixels), so these products are
	// small; they are formed in int64 all the same.
	nbytes := int64(stride)*int64(rows)*8 + int64(rows)*8
	passes := int64(1) // zeroing and the row-extent scan
	if cur.mask != nil {
		passes = 2 // and the AND with the enclosing mask
	}
	if !r.b.chargePaint(int64(rows) * (clipRowCost + passes*clipWords(stride))) {
		return cur
	}
	if !r.b.chargeClipMask(nbytes) {
		return cur
	}
	m := &clipMask{
		x0w: x0w, x1w: x1w, y0: box.y0, y1: box.y1, stride: stride,
		bits:  make([]uint64, stride*rows),
		rowX:  make([]int32, 2*rows),
		depth: depth, bytes: nbytes,
	}
	r.scanPath(subpaths, evenOdd, box.y0, box.y1, func(x0, x1 float64, y int) {
		ix0, ix1, ok := spanPixels(x0, x1, box.x0, box.x1)
		if !ok || !r.b.chargePaint(clipWords((ix1-1)>>6-ix0>>6+1)) {
			return
		}
		m.setRun(y, ix0, ix1)
	})
	if r.b.stopped() {
		r.b.releaseClipMask(nbytes)
		return cur
	}

	// Intersect with the current mask and record each row's extent and the
	// tight box of what is left.
	tight := clipRect{x0: math.MaxInt, y0: math.MaxInt, x1: math.MinInt, y1: math.MinInt}
	for y := box.y0; y < box.y1; y++ {
		row := m.bits[(y-box.y0)*stride : (y-box.y0+1)*stride]
		if cur.mask != nil {
			for i := range row {
				row[i] &= cur.mask.word(y, x0w+i)
			}
		}
		first, last := -1, -1
		for i, v := range row {
			if v != 0 {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 {
			continue
		}
		lo := (x0w+first)<<6 + bits.TrailingZeros64(row[first])
		hi := (x0w+last)<<6 + 64 - bits.LeadingZeros64(row[last])
		m.rowX[2*(y-box.y0)] = int32(lo)
		m.rowX[2*(y-box.y0)+1] = int32(hi)
		tight.x0, tight.x1 = minInt(tight.x0, lo), maxInt(tight.x1, hi)
		tight.y0, tight.y1 = minInt(tight.y0, y), maxInt(tight.y1, y+1)
	}
	if tight.empty() {
		r.b.releaseClipMask(nbytes)
		return clipState{}
	}
	return clipState{rect: tight, mask: m}
}

// applyPendingClip applies a W/W* mark left by the path just painted (§4.1):
// the clip becomes the current one intersected with the path's inside, under
// the mark's winding rule. It runs after the painting operator paints, so the
// paint used the old clip (§8.5.4, deferred effect), and before the path is
// cleared. A path with no point at all does not narrow the clip (§2.2,
// fail-visible).
func (in *interp) applyPendingClip() {
	rule := in.pendingClip
	if rule == clipNone {
		return
	}
	in.pendingClip = clipNone
	in.flushSub(false) // an open subpath is implicitly closed, as for fill
	if len(in.subpaths) == 0 || in.b.stopped() {
		return
	}
	cur := in.gs.clip
	if minX, minY, maxX, maxY, ok := rectPath(in.subpaths); ok {
		// The fast path keeps the mask: narrowing rect narrows rect ∩ mask.
		cur.rect = cur.rect.intersect(rectPixels(minX, minY, maxX, maxY, in.rs.w, in.rs.h))
		in.setClip(cur)
		return
	}
	in.setClip(in.rs.buildClip(cur, in.subpaths, rule == clipEvenOdd, len(in.stack)))
}

// setClip replaces the current clip with c. The mask it replaces dies when it
// was built at the current stack depth (no saved state can hold it) and no
// active form's saved state does either (see execForm).
func (in *interp) setClip(c clipState) {
	if old := in.gs.clip.mask; old != nil && old != c.mask && old.depth == len(in.stack) && !in.formPinned(old) {
		in.b.releaseClipMask(old.bytes)
	}
	in.gs.clip = c
	in.syncClip()
}

// restoreState pops the top saved graphics state (Q). The current mask dies
// when it was built above the new stack length and the restored state does
// not hold it.
func (in *interp) restoreState() {
	n := len(in.stack)
	next := in.stack[n-1]
	if m := in.gs.clip.mask; m != nil && m.depth > n-1 && m != next.clip.mask {
		in.b.releaseClipMask(m.bytes)
	}
	in.gs = next
	in.stack = in.stack[:n-1]
	in.syncClip()
}

// formPinned reports whether m is the clip mask of an active form's saved
// state: a form whose stray Q popped its implicit frame (form.go) may replace
// the caller's mask at the caller's own depth, yet the form restores it on
// exit.
func (in *interp) formPinned(m *clipMask) bool {
	for _, p := range in.formClips {
		if p == m {
			return true
		}
	}
	return false
}

// syncClip mirrors the graphics-state clip onto the raster, which enforces it
// per pixel. Called whenever the clip changes: on form entry and exit, after
// any Q, and when a W/W* clip is applied.
func (in *interp) syncClip() {
	in.rs.clip = in.gs.clip.rect
	in.rs.mask = in.gs.clip.mask
}

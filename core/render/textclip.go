package render

import "image/color"

// This file implements text clipping, text rendering modes 4–7 (ISO 32000-1
// §9.3.6, Table 106; design docs/design/clipping-paths.md §5). Each glyph a
// text object shows in a clip mode adds its outline to a clip accumulated
// for the object; at ET the clip becomes that union intersected with the
// current clip. Modes 4–6 also paint as modes 0–2 do; mode 7 only clips.
//
// The union is accumulated in a mask (clip.go), never as a path: the first
// clip-mode glyph allocates a mask over the current clip's box (nothing
// outside it can survive the intersection), and each clip-mode glyph's fill
// spans are ORed into it by the same scan that paints the glyph, so the
// region is exactly the pixels a fill of each glyph covers (§2.1). A path
// would be bounded only by maxPathPoints, and nonzero winding over
// overlapping glyphs of opposite orientation is not their union.
//
// Lenient cases (§5; ISO forbids or does not settle them), all fail-visible:
// when in doubt the object narrows nothing, which shows too much rather than
// hiding content.
//
//   - A text object that shows no glyph in a clip mode changes nothing; one
//     whose clip-mode glyphs are all empty (spaces) clips everything (the
//     union rule, design Q4).
//   - A clip-mode glyph whose outline is unavailable (no font, a font that
//     did not load, a Type 3 font, a code with no glyph, an outline that does
//     not build) discards the object's accumulation: ET narrows nothing.
//   - Q inside the object discards it too; q does not.
//   - A Form XObject invoked inside the object discards an accumulation
//     already started (with none started, the object's later clip-mode
//     glyphs start one as usual); the form's own text objects accumulate
//     separately, and one the form leaves open is discarded at its end.
//   - BT inside an open object, or the end of the content without ET,
//     discards it.
//
// The accumulation mask counts toward the live clip-mask bytes from its
// allocation. At ET its bits are copied into a mask over the new clip's
// tight box, which becomes the clip's mask, and it is released; an object
// that narrows nothing just releases it.

// textClip is the clip accumulation of the open text object. The zero
// interp.textClip (nil) means no glyph has been shown in a clip mode yet.
type textClip struct {
	box     clipRect  // the clip's box when the first clip-mode glyph was shown
	mask    *clipMask // the union of the clip-mode glyphs within box; nil when box is empty or discarded
	discard bool      // ET narrows nothing (an outline was unavailable, Q, a form)
}

// clipModes reports whether text rendering mode mode adds glyphs to the clip
// (4 fill and clip, 5 stroke and clip, 6 both and clip, 7 clip only).
func clipModes(mode int) bool { return mode >= 4 && mode <= 7 }

// openTextClip starts the accumulation of the open text object at its first
// clip-mode glyph: an empty mask over the current clip's box. The mask is
// charged as a mask build (newMask: its rows, the zeroing and the extent
// pass at ET) and its bytes to the live clip-mask counter. A trip leaves a
// discarded accumulation; the render stops anyway.
func (in *interp) openTextClip() *textClip {
	tc := &textClip{box: in.gs.clip.rect}
	if !tc.box.empty() {
		// The depth is set at ET, when the mask becomes a clip.
		if tc.mask, _ = in.rs.newMask(tc.box, textClipPasses, 0); tc.mask == nil {
			tc.discard = true
		}
	}
	in.textClip = tc
	return tc
}

// spoilTextClip discards the open text object's accumulation, so its ET
// narrows nothing, and releases its mask. Called at a clip-mode glyph it
// records the discard even before the first allocation.
func (in *interp) spoilTextClip() {
	tc := in.textClip
	if tc == nil {
		in.textClip = &textClip{discard: true}
		return
	}
	if tc.mask != nil {
		in.b.releaseClipMask(tc.mask.bytes)
		tc.mask = nil
	}
	tc.discard = true
}

// dropTextClip ends the open text object's accumulation without narrowing
// the clip (BT, the end of a content stream without ET) and releases its
// mask.
func (in *interp) dropTextClip() {
	if tc := in.textClip; tc != nil && tc.mask != nil {
		in.b.releaseClipMask(tc.mask.bytes)
	}
	in.textClip = nil
}

// endTextClip applies the open text object's accumulation at ET: the clip
// becomes the union of its clip-mode glyphs intersected with the current
// clip, with the accumulation mask, shrunk to the union's tight box
// (shrinkMask), as the new clip's mask (at the current stack depth, for
// liveness). A discarded accumulation, or none,
// changes nothing. The AND with an enclosing mask is charged as in
// buildClip.
//
// The current clip lies inside the accumulation's box: since its first
// glyph the clip can only have narrowed (W), as Q and forms discard the
// accumulation. The check stays as a guard: a wider clip would need glyph
// pixels the mask never held, so the object then narrows nothing.
func (in *interp) endTextClip() {
	tc := in.textClip
	if tc == nil {
		return
	}
	in.textClip = nil
	cur := in.gs.clip
	if tc.discard || in.b.stopped() || !cur.rect.empty() && !tc.box.containsRect(cur.rect) {
		if tc.mask != nil {
			in.b.releaseClipMask(tc.mask.bytes)
		}
		return
	}
	m := tc.mask
	if m == nil || cur.rect.empty() {
		// Nothing passed the clip when the accumulation started, or passes
		// it now: the intersection is empty.
		if m != nil {
			in.b.releaseClipMask(m.bytes)
		}
		in.setClip(clipState{})
		return
	}
	if cur.mask != nil && !in.b.chargePaint(int64(m.y1-m.y0)*clipWords(m.stride)) {
		in.b.releaseClipMask(m.bytes)
		return
	}
	tight := m.finish(cur.mask, &cur.rect)
	if tight.empty() {
		in.b.releaseClipMask(m.bytes)
		in.setClip(clipState{})
		return
	}
	m.depth = len(in.stack)
	if m = in.rs.shrinkMask(m, tight); m == nil {
		return
	}
	in.setClip(clipState{rect: tight, mask: m})
}

// shrinkMask returns the finished mask m reduced to the rows and word columns
// of tight, the box finish returned for it: the accumulation covers the whole
// clip box, so a word's clip would otherwise hold a page-sized mask for as
// long as it lives. The copy is a new mask at m's depth, charged as a
// one-pass mask build and to the live clip-mask counter before m is
// released; no bit changes, as finish left none outside tight. m itself is
// returned when it already spans exactly those rows and words. A trip
// releases m and returns nil; the render stops.
func (r *raster) shrinkMask(m *clipMask, tight clipRect) *clipMask {
	x0w, x1w := tight.x0>>6, (tight.x1+63)>>6
	if x0w == m.x0w && x1w == m.x1w && tight.y0 == m.y0 && tight.y1 == m.y1 {
		return m
	}
	s, ok := r.newMask(tight, 1, m.depth)
	if !ok {
		r.b.releaseClipMask(m.bytes)
		return nil
	}
	for y := tight.y0; y < tight.y1; y++ {
		src := m.bits[(y-m.y0)*m.stride+x0w-m.x0w:]
		copy(s.bits[(y-s.y0)*s.stride:(y-s.y0+1)*s.stride], src[:s.stride])
	}
	copy(s.rowX, m.rowX[2*(tight.y0-m.y0):2*(tight.y1-m.y0)])
	r.b.releaseClipMask(m.bytes)
	return s
}

// showClipGlyph shows one glyph in a clip mode: it paints the glyph when
// paints (modes 4–6) and adds its outline to the open text object's
// accumulation, starting one at the object's first clip-mode glyph. ok and
// gid are the font's mapping of the code; a code with no glyph, or an
// outline that cannot be built, discards the accumulation (fail-visible).
// An empty outline (a space) adds nothing.
func (in *interp) showClipGlyph(gf *glyphFont, gid uint32, ok, paints bool) {
	tc := in.textClip
	if tc != nil && tc.discard {
		if ok && paints {
			in.drawGlyph(gf, gid)
		}
		return
	}
	var subpaths [][]point
	if ok {
		subpaths, ok = in.glyphOutline(gf, gid)
	}
	if !ok {
		in.spoilTextClip()
		return
	}
	if tc == nil {
		tc = in.openTextClip()
	}
	if len(subpaths) == 0 {
		return
	}
	if tc.mask == nil { // an empty box, or the allocation tripped
		if paints {
			in.rs.fillPath(subpaths, in.gs.fill, false)
		}
		return
	}
	in.rs.fillAccumulate(subpaths, in.gs.fill, paints, tc.mask, tc.box)
}

// fillAccumulate is fillPath (nonzero) for a clip-mode glyph: one scan paints
// the spans with col when paint is true and sets them, within box, in the
// accumulation mask acc, which covers box. It charges what fillPath charges
// for the rows it scans (all of them when painting, else box's) plus
// clipWords per span, as a clip mask build does.
func (r *raster) fillAccumulate(subpaths [][]point, col color.RGBA, paint bool, acc *clipMask, box clipRect) {
	lo, hi := box.y0, box.y1
	if paint {
		lo, hi = 0, r.h
	}
	r.scanPath(subpaths, false, lo, hi, func(x0, x1 float64, y int) {
		if paint {
			r.span(x0, x1, y, col)
		}
		if y < box.y0 || y >= box.y1 {
			return
		}
		ix0, ix1, ok := spanPixels(x0, x1, box.x0, box.x1)
		if !ok || !r.b.chargePaint(clipWords((ix1-1)>>6-ix0>>6+1)) {
			return
		}
		acc.setRun(y, ix0, ix1)
	})
}

// containsRect reports whether every pixel of o lies inside c; an empty o
// lies inside any rectangle.
func (c clipRect) containsRect(o clipRect) bool {
	return o.empty() || o.x0 >= c.x0 && o.y0 >= c.y0 && o.x1 <= c.x1 && o.y1 <= c.y1
}

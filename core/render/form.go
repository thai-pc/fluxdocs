package render

import (
	"math"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// Form XObject recursion limits (SECURITY.md §2–3). A Form XObject may Do other
// forms, so a hostile or accidentally cyclic file could drive unbounded or
// exponential work. Every limit results in a graceful skip of the offending
// form, never a panic or a render error.
const (
	// maxFormDepth bounds form nesting. Real files nest 1–3 deep; 12 is generous
	// headroom while still stopping runaway recursion.
	maxFormDepth = 12
	// maxFormBudget caps the total number of form executions per page render,
	// bounding breadth/fan-out that the depth limit alone does not.
	maxFormBudget = 4096
)

// execForm interprets a Form XObject (§8.10.1) into the same raster surface. The
// PDF definition of Do on a form is: save state (implicit q), concatenate the
// form's /Matrix onto the CTM, clip to the form's /BBox, paint the form's content
// stream against the form's /Resources, then restore (implicit Q).
//
// ref is the XObject's indirect reference and hasRef reports whether the entry
// was reached through one (rather than being a direct/inline stream); together
// they key the font cache and the cycle guard. Any tripwire — depth, cycle,
// budget, an undecodable content stream — is a silent no-op for this Do, leaving
// the rest of the page intact.
func (in *interp) execForm(st *parse.Stream, ref parse.Reference, hasRef bool) {
	// --- security guards (§6): depth, cycle, budget ---
	if in.formBudget <= 0 || in.formDepth >= maxFormDepth {
		return
	}
	if hasRef && in.formPath[ref] {
		return // direct or indirect self-reference (cycle)
	}

	// Decode the content stream up front; an unsupported filter or decode error
	// is a graceful skip that consumes no budget and touches no state.
	data, err := parse.DecodeStream(st)
	if err != nil {
		return
	}

	// Commit to running this form: charge the budget.
	in.formBudget--
	in.formDepth++

	// --- snapshot everything the form must not perturb ---
	// gstate (CTM, colours, line width, clip) is captured in savedGS and restored
	// verbatim; the fields below live on interp and are saved/restored manually.
	// baseLen and stackFloor make the form's q/Q self-contained: truncating to
	// baseLen undoes any q/Q imbalance, and the floor stops a stray Q from popping
	// a caller frame mid-stream.
	baseLen := len(in.stack)
	savedGS := in.gs
	savedFloor := in.stackFloor
	savedResources := in.resources
	savedFonts := in.fonts
	savedTM := in.tm
	savedTLM := in.tlm
	savedSubpaths := in.subpaths
	savedClosed := in.closed
	savedCur := in.cur
	savedCurX, savedCurY := in.curX, in.curY
	savedStartX, savedStartY := in.startX, in.startY

	if hasRef {
		in.formPath[ref] = true
	}

	// Single deferred cleanup: every exit path (the empty-clip skip and any future
	// early return included) restores symmetrically. The stack truncation to
	// baseLen leaves the caller's saved frames untouched regardless of the form's
	// q/Q balance.
	defer func() {
		in.formDepth--
		if hasRef {
			delete(in.formPath, ref)
		}
		in.stack = in.stack[:baseLen]
		in.gs = savedGS
		in.stackFloor = savedFloor
		in.resources = savedResources
		in.fonts = savedFonts
		in.tm = savedTM
		in.tlm = savedTLM
		in.subpaths = savedSubpaths
		in.closed = savedClosed
		in.cur = savedCur
		in.curX, in.curY = savedCurX, savedCurY
		in.startX, in.startY = savedStartX, savedStartY
		in.syncClip()
	}()

	// A conforming form has no pending path at Do time; clear defensively so a
	// malformed form cannot leak marks into the caller's path.
	in.subpaths = nil
	in.closed = nil
	in.cur = nil

	// --- implicit q: push graphics state; floor Q at the form's own frames ---
	in.stack = append(in.stack, in.gs)
	in.stackFloor = baseLen

	// --- concatenate the form /Matrix onto the CTM (§3.2) ---
	in.gs.ctm = formMatrix(in.res, st.Dict).mul(in.gs.ctm)

	// --- intersect the clip with the form /BBox device rectangle (§5) ---
	if bbox, ok := formBBox(in.res, st.Dict); ok {
		in.gs.clip = in.gs.clip.intersect(deviceBounds(bbox, in.gs.ctm))
	}
	in.syncClip()

	// --- swap to the form's resource + font scope (§4) ---
	// Per §8.10.1 a form without /Resources inherits the caller's environment, so
	// only swap when the form declares its own.
	if formRes, ok, err := in.res.ResolveDict(st.Dict["Resources"]); err == nil && ok {
		in.resources = formRes
		in.fonts = in.formFonts(ref, hasRef, formRes)
	}

	// --- paint the form content (skip a degenerate, zero-area clip) ---
	if !in.gs.clip.empty() {
		in.interpret(parse.ScanContent(data))
	}
}

// syncClip mirrors the graphics-state clip onto the raster, which enforces it per
// pixel. Called whenever the clip changes: on form entry and after any Q.
func (in *interp) syncClip() {
	in.rs.clip = in.gs.clip
}

// formFonts returns the fonts for a form's /Resources, cached by the form's
// XObject reference so a form stamped repeatedly parses its embedded fonts once.
// parse.Reference is a comparable struct and is a safe map key; parse.Dict is
// not. An inline (reference-less) form reloads its fonts without caching.
func (in *interp) formFonts(ref parse.Reference, hasRef bool, formRes parse.Dict) map[parse.Name]*glyphFont {
	if !hasRef {
		return loadFonts(in.res, formRes)
	}
	if cached, ok := in.fontCache[ref]; ok {
		return cached
	}
	fonts := loadFonts(in.res, formRes)
	in.fontCache[ref] = fonts
	return fonts
}

// formMatrix reads a Form XObject's /Matrix (six numbers, same layout as cm).
// A missing or malformed matrix defaults to identity, so the form is placed at
// the caller's CTM unchanged.
func formMatrix(res *parse.Resolver, d parse.Dict) matrix {
	v, err := res.Resolve(d["Matrix"])
	if err != nil {
		return identity()
	}
	arr, ok := v.(parse.Array)
	if !ok || len(arr) < 6 {
		return identity()
	}
	return matrix{num(arr[0]), num(arr[1]), num(arr[2]), num(arr[3]), num(arr[4]), num(arr[5])}
}

// formBBox reads and normalises a Form XObject's /BBox. /BBox is spec-required,
// but parse.resolveRectangle is unexported, so the render layer resolves it here:
// a four-number array, normalised to min/max corners. ok is false when the key is
// missing or not a four-number array — the caller then renders the form unclipped
// (preserving its marks) rather than dropping content (design O2).
func formBBox(res *parse.Resolver, d parse.Dict) (parse.Rectangle, bool) {
	v, err := res.Resolve(d["BBox"])
	if err != nil {
		return parse.Rectangle{}, false
	}
	arr, ok := v.(parse.Array)
	if !ok || len(arr) < 4 {
		return parse.Rectangle{}, false
	}
	x0, y0 := num(arr[0]), num(arr[1])
	x1, y1 := num(arr[2]), num(arr[3])
	return parse.Rectangle{
		LLX: math.Min(x0, x1), LLY: math.Min(y0, y1),
		URX: math.Max(x0, x1), URY: math.Max(y0, y1),
	}, true
}

// deviceBounds transforms the four corners of a user-space rectangle by ctm and
// returns their axis-aligned device-pixel bounding box (floor/ceil to integers,
// half-open). For a rotated or skewed /Matrix this box is a superset of the true
// clip region, so a rotated form may mildly overpaint but never loses content
// (design O3).
func deviceBounds(r parse.Rectangle, ctm matrix) clipRect {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{r.LLX, r.LLY}, {r.URX, r.LLY}, {r.URX, r.URY}, {r.LLX, r.URY}} {
		dx, dy := ctm.apply(c[0], c[1])
		minX, maxX = math.Min(minX, dx), math.Max(maxX, dx)
		minY, maxY = math.Min(minY, dy), math.Max(maxY, dy)
	}
	return clipRect{
		x0: int(math.Floor(minX)),
		y0: int(math.Floor(minY)),
		x1: int(math.Ceil(maxX)),
		y1: int(math.Ceil(maxY)),
	}
}

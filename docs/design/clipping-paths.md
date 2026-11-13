# Design: clipping paths (`W`, `W*`, text clipping) in the raster renderer

Status: P0 and Phase A implemented (results in §14 and §15). Maintainer
decisions on §12: Q1 (a), Q2 (a), Q3 (a), Q5 deferred; Q4 and Phase B (text
clipping) open. Tier: **Core (MIT)**.
Package: `core/render` only. Pure Go, no CGO, no build tag. Both targets
(native, `GOOS=js GOARCH=wasm`). No public API change: no new `RenderOptions`
field, no new sentinel.
Basis: ISO 32000-1:2008 §8.2 (graphics objects, Figure 9), §8.4.1 (graphics
state, Table 52: clipping path), §8.4.2 (graphics state stack), §8.5.3.3
(nonzero and even-odd rules), §8.5.4 (clipping path operators, Table 61),
§8.7.4 (shadings, `sh`), §8.9.5 (image placement), §8.10.1 (Form XObjects),
§9.3.6 (text rendering mode, Table 106), §12.5.5 (appearance streams).
SECURITY.md §1 (redaction), §2 (untrusted input), §3 (WASM). Clean-room:
nothing here comes from another renderer's source. Ghostscript is used only as
an external black-box oracle (§9).

---

## 1. Current state (HEAD `5828c31`)

| Item | Where | Behavior today |
|---|---|---|
| `W`, `W*` | `interpret.go:219–221` | No-op. The path stays and is consumed by the next painting operator (usually `n`), so nothing is clipped. |
| Clip state | `gstate.clip clipRect` (`interpret.go:23`), mirrored to `raster.clip` by `syncClip` (`form.go:147`) | An integer, half-open, axis-aligned device rectangle. Saved by `q`, restored by `Q` (which calls `syncClip`). |
| Who narrows it | `execForm` (`form.go:126–129`) | Only a Form XObject's `/BBox`, as the outer-rounded (floor/ceil) device bounding box of the transformed `/BBox` (`deviceBounds`). A rotated or skewed form gets its bounding box, a superset (form-xobject.md O3). |
| Who enforces it | `raster.blend` (`raster.go:170`), the single per-pixel choke point; `span` clamps to the x-range and skips rows outside; `line` clips its segment to the clip box; `drawImage` clamps its device box (`image.go:504–507`) | Fills, strokes, glyphs and images all honour the rectangle. |
| Text modes | `text.go:20` | 3 and 7 are invisible; 0–2 and 4–6 are filled (stroking text is not modelled). Modes 4–7 never clip. `Tr` is read as `int(lastNum(o))` (`interpret.go:269`), which truncates `4.5` to 4. |
| Shadings (`sh`), patterns | — | Not rendered. |
| PDF annotation appearances (`/AP`) | — | Not rendered by `core/render`. The overlay (`drawAnnotations`) draws FluxDocs JSON annotations after the content, through the same raster. |
| SVG output | `render.go:29` | Does not exist: `"svg"` returns `ErrUnsupportedFormat`. Spec §4.1/§11.1 plan a `svg.go`. |
| Coverage model | `fillPath`, `span` | One sample per pixel (row centre `y+0.5`, span ends rounded with `math.Round`). Coverage is binary: a pixel is painted or not. |

Visible consequences: images cropped by a clip (very common in Word, InDesign
and browser-printed PDFs) paint their full rectangle; chart plot areas
overflow their axes; a gradient or image used as "text fill" via `7 Tr` paints
as a solid block. Shadings cannot be added without clipping, because `sh`
paints the whole current clip region (§8.7.4).

---

## 2. Semantics (ISO 32000-1 §8.5.4)

1. **Deferred effect.** `W` (nonzero) and `W*` (even-odd) do not clip at once.
   They mark the current path. The next path-painting operator (`S s f F f* B
   B* b b* n`) first paints with the **old** clip, then the clip becomes
   old ∩ region(path, rule). `n` paints nothing, so `… W n` is the usual
   clip-only idiom, and `… W f` fills under the old clip and then narrows.
2. **Region.** The clip region is the inside of the path under the operator's
   winding rule (§8.5.3.3), independent of the painting operator: `W S`
   narrows to the fill region of the path, not to the stroke outline. Every
   open subpath is implicitly closed, as for fill.
3. **Monotone.** The new clip is always the intersection with the current
   one. Nothing enlarges the clip except restoring an earlier state with `Q`.
4. **Saved and restored.** The clip is part of the graphics state (§8.4.1,
   Table 52), so `q` saves it, `Q` restores it, and a Form XObject's implicit
   `q`/`Q` (§8.10.1) restores it on exit. The current path is **not** part of
   the graphics state, so the pending `W` mark is not either (§4.4).
5. **Initial clip.** The whole page. Here: the full canvas.

### 2.1 Pixel rule (definition)

**A pixel lies inside a clip path exactly when `fillPath` would paint it for
that path and winding rule.** The clip uses the same sampling as fill: row
centres, half-open `[ymin, ymax)` edge membership, the same crossing formula,
`math.Round` on span ends. Consequences:

- `0 0 100 100 re W n 0 0 100 100 re f` paints exactly the pixels of
  `0 0 100 100 re f`. Clip and fill edges line up.
- The rectangle fast path (§3.2) must give the same pixels as the general path.
  That makes it an optimization, never a semantic choice, and a property test
  checks it (§10.1).
- A path that fill would paint nothing for (zero area, a NaN point) clips
  everything (§4.3).

### 2.2 Lenient edge cases (ISO does not settle them)

| Input | Decision | Why |
|---|---|---|
| `W n` with no current path (no point at all), `h W n` | Ignore the `W`: no narrowing | ISO allows `W` only after path construction (Figure 9). Fail-visible: a malformed clip should not blank the page. Matches the redaction rule C46 ("a no-point clip never narrows"). Check against the oracle (§9). |
| `W` then more path construction, then a painting op (`re W re f`) | The mark stays; the clip uses the path as it is at the painting op | A simple state machine; Figure 9 forbids the input, so any choice is lenient. See Q3. |
| `W W* n` | The last of `W`/`W*` sets the rule | Same. |
| `W` then `Q` (or `Do`, `BT`) before the painting op | The mark survives `Q` and applies to the restored state at the next painting op | Follows from rule 4 (the path and the mark are not graphics state). Form entry saves and clears it (§4.4). |
| `W` inside `BT … ET` | Applied as outside | Lenient; no reason to special-case. |
| Clip set inside a `q` ignored past `maxSaveDepth` (1024) | Not undone by the swallowed `Q` | Existing lenient rule (render-budget.md §10). A clip makes it more visible than a color leak; real files stay far below the cap. |

---

## 3. Representation

### 3.1 Options

| | Option | Memory | Paint cost | Hostile `q W n` flood | Verdict |
|---|---|---|---|---|---|
| A | A full-canvas coverage mask (8-bit) per graphics state, copied on `q` | 64 MiB per state at the 64 Mi px cap; × 1024 saved states | O(1) per pixel | A copy per `q`: 1024 × 64 MiB | Rejected |
| B | A list of clip paths per state, intersected lazily at paint time (point-in-path per pixel, or materialized at first paint) | The paths: up to `maxPathPoints` (4 Mi points, 64 MiB) each, held as long as the state lives; × 1024 | O(k) paths per pixel, or one materialization per state at first paint | Holding 1024 max-size paths is 64 GiB | Rejected: the held paths are the memory problem, and per-pixel cost grows with nesting |
| **C** | **A rectangle plus an optional immutable 1-bit mask over the clip's bounding box, shared by pointer** | 1 bit per pixel of the bounding box only (≤ 8 MiB at the cap); none for rectangles | O(1) per pixel (one bit test) | `q` copies a pointer; a rectangle clip allocates nothing | **Recommended** |
| D | Rectangle plus per-row interval lists | O(intervals); up to ≈ 4 bytes per pixel in the worst case (alternating pixels) | O(intervals) per span | Same as C | Alternative. Faster spans for few intervals, worse bound. Not needed while coverage is binary. |

The mask can be 1-bit because coverage is binary (§1). If anti-aliasing lands
later (type1-std14-fonts.md F3), the mask must carry 8-bit coverage, 8× the
memory. The live-mask cap (§3.4) then applies unchanged, in bytes.

### 3.2 Option C in detail

```go
// clipState lives in gstate in place of today's clipRect. The value is copied by
// q (and by execForm's savedGS) at O(1): the mask is shared, never mutated.
type clipState struct {
	rect clipRect  // device rectangle; the effective clip is rect ∩ mask
	mask *clipMask // nil = no path clip beyond rect
}

// clipMask is immutable after construction. Bits are aligned to canvas-global
// 64-bit columns so two masks AND word by word without shifting.
type clipMask struct {
	x0w, x1w int     // word columns [x0w, x1w) (pixels x0w*64 … x1w*64-1)
	y0, y1   int     // rows [y0, y1)
	bits     []uint64 // (x1w-x0w) words per row
	rowX     []int32  // per row: [min, max) x of set bits, for span clamping (2 per row)
	depth    int      // len(stack) when built (liveness, §3.4)
	bytes    int64    // charged to the live-mask counter
}
```

- **Rectangle fast path.** At the painting op, the device path is a rectangle
  when it is one closed subpath of four points whose edges are exactly
  axis-aligned (point coordinates compared with `==`). `re`, and `m l l l h`
  forms of one, qualify under any CTM that keeps axes (including /Rotate 90,
  180, 270). The new clip is `rect ∩ rectPixels(path)`, where `rectPixels`
  applies the §2.1 rule: rows `y` with `ymin ≤ y+0.5 < ymax` (computed with
  `fillStartRow`'s exact predicate) and columns `[round(xmin), round(xmax))`.
  The winding rule does not matter for one rectangle. **No allocation, O(1).**
  A mask, if any, is kept as is: narrowing `rect` narrows the effective clip.
- **General path.** Build a new mask over `bbox = rows/cols the path touches ∩
  effective bbox of the current clip`. Fill the path into it with the shared
  scanline (§3.3), then AND it word by word with the old mask over the
  overlap. Empty result: set `rect` to empty and drop the mask. Recompute
  `rect` as the bounding box of the set bits, so an image or line clamps to
  the tight box.
- **Effective test.** `rect.contains(x, y) && (mask == nil || mask.bit(x, y))`.
  `blend` does it, as today, and stays the single choke point. `span` clamps
  to `rect` and then to `rowX[y]`. `line` and `drawImage` clamp to `rect`,
  which is the mask's tight box.
- **Size arithmetic.** `bbox` is inside the canvas, and the canvas is capped at
  `maxCanvasPixels` (64 Mi) after `deviceMatrix`. The words per row and rows
  are ints from clamped geometry (`clampToInt`). The product is formed in
  int64 and is ≤ 1 Mi words (8 MiB) plus one word of padding per row. It is
  checked against the budget **before** `make` (SECURITY.md §2: no allocation
  sized by untrusted input without a bound).

### 3.3 One scanline for fill and clip

Factor `fillPath` into a scan that emits `(y, ix0, ix1)` spans to a sink.
`fillPath` sinks into `span`, and the clip builder sinks into the mask (set
bits `[ix0, ix1)` on row `y`). The two cannot drift apart, so §2.1 holds by
construction. The refactor must leave every existing render byte-identical
(gate, §9). The active-edge table, the budget charges and the scratch buffers
(`fillScratch`) are shared.

### 3.4 Memory bounds and copy-on-write

- **`q` is O(1).** `gstate` is copied by value. `clipState` is a rect plus a
  pointer, so `q W n` ×10^6 copies pointers: rectangles allocate nothing, and
  `q` past `maxSaveDepth` saves nothing at all.
- **A mask is built only by a painting op that ends a `W` path.** That is
  one allocation per non-rectangular clip, charged to raster work (§6), so the
  total built is bounded by `MaxPaintPixels`.
- **Live masks are capped.** Nested non-rectangular clips keep every level's
  mask alive: 1024 levels of near-full-canvas masks is 8 GiB at the canvas
  cap, fatal under js/wasm (4 GiB). A new internal counter, **live clip-mask
  bytes**, is limited by `maxClipMaskBytes` = 128 MiB per render (16 masks at
  the canvas cap, ≈ 500 at Letter 150 DPI). Past it the render trips
  `ErrBudgetExceeded` with resource `clip masks` (decision D1 of
  render-budget.md: an error, not a silent degradation; see Q2).
- **Liveness rule (exact, no reference counts).** A mask built when
  `len(stack) == d` can be referenced only by the current state and by stack
  entries at index ≥ d: entries below d were saved before it existed. So:
  - when a new clip replaces the current mask m and `m.depth == len(stack)`, m
    is dead;
  - when `Q` pops to length d and the current mask m (if any) has
    `m.depth > d` and differs from the restored mask, m is dead;
  - `execForm` exit applies the `Q` rule against `baseLen`.

  A dead mask's bytes leave the live counter. Its buffer may go to a small
  free list for reuse (optional, only if benchmarks show GC pressure). The
  rule is deterministic, so a trip happens at the same operator on every
  target.
- **WASM.** No filesystem, no network. Peak extra memory is bounded by
  `maxClipMaskBytes` plus one mask under construction.

---

## 4. Coverage: what the clip applies to

| Painting | Today (rect clip) | With this design | Note |
|---|---|---|---|
| Fills (`f F f* B B* b b*`) | `span` + `blend` | Same, plus mask | Phase A |
| Strokes (`S s B b …`) | `line` clamps to the clip box, `blend` per pixel | Clamp to the tight box, `blend` tests the mask | Phase A |
| Glyphs (modes 0–2, 4–6) | `fillPath` | Same as fills | Phase A |
| Images, image masks, `/SMask` | `drawImage` clamps its box, `blend` per pixel | Same, plus mask | Phase A. The common "cropped photo" case. |
| Form XObjects | Inherit the clip; `/BBox` intersected on entry; restored on exit | Unchanged: the inherited clip may now carry a mask. `/BBox` stays the outer-rounded rectangle (Q1). A `W` inside the form is undone by the existing `savedGS` restore. The empty-clip skip (`form.go:140`) also covers an empty mask. | Phase A |
| Shadings (`sh`), shading patterns | Not rendered | When added, `sh` must paint the effective clip region, iterating only over `rect` (the mask's tight box), never the canvas | Prerequisite for any shading design |
| Annotation appearances (`/AP`, §12.5.5) | Not rendered | When added, they go through `execForm`, so the form rules apply. Each appearance starts from the **full-canvas** clip, not the page content's final clip. | Requirement on the future design |
| FluxDocs overlay (`drawAnnotations`) | Drawn after content with `rs.clip`, always full canvas today (forms restore) | **Must reset the clip to the full canvas before the overlay.** An unbalanced page (`… W n` with no `Q`) would otherwise clip the overlay, including redaction preview boxes. | Phase A; mandatory test |
| Text clipping, modes 4–7 | Ignored (7 invisible) | Phase B (§5) | Deferred, with reasons |

### 4.1 Interpreter changes (Phase A)

- `interp` gains `pendingClip int8` (0 none, 1 nonzero, 2 even-odd). `W` and
  `W*` set it and nothing else.
- Every painting op paints first. Then, if `pendingClip != 0`, it applies the
  clip with the current path, before `clearPath`, and resets the mark. `n`
  becomes "apply the pending clip, then clear". `fillKeep`/`strokeCurrent`
  ordering keeps the painted pixels under the old clip (rule 1).
- `execForm` saves and clears `pendingClip` with the path fields and restores
  it on exit.
- `syncClip` copies `clipState` to the raster. `Q` already calls it.
  `render.Page` calls a reset before `drawAnnotations`.
- Update the stale comments: `gstate` ("Clipping … not yet modeled"), the
  `clipRect` and `raster.clip` docs, and the package doc in `render.go`
  ("path clipping … not yet rendered").

---

## 5. Text clipping (§9.3.6, modes 4–7): deferred to Phase B

ISO: in modes 4–7 each glyph's outline is added to a clip accumulated for the
text object; at `ET` the accumulated region (the union of the glyph shapes) is
intersected with the current clip. Modes 4–6 also fill and/or stroke; mode 7
only clips.

**Recommendation: defer to Phase B, same design, separate commit.** Reasons:

1. **Its hashes depend on glyph shapes that are still moving.** The clip
   region is the glyph outline. Standard-14 substitution (type1-std14-fonts.md
   P3, `substitute.go`, being edited now) and F1/F3 (flattening, dropout)
   change those outlines. Landing text clips now churns the same pages' hashes
   twice.
2. **It needs its own rule change.** `Tr` must count as a clip mode only when
   it is exactly an integer 4–7, and any non-integer must render as mode 0
   (redaction-hardening C54; today `int()` truncates `4.5` to 4, and `3.5` to
   invisible). That changes hashes of malformed pages on its own and belongs
   in its own commit.
3. **Phase A does not regress it.** Today mode 7 text is invisible and
   whatever it should clip paints unclipped. Phase A leaves that exactly as
   is.
4. **Lower frequency.** `W n` appears on most generated pages; modes 4–7 are
   rare outside design tools (measured by the census, §9).

**Phase B design (for the record).**

- At the first glyph shown in modes 4–7 inside a text object, allocate an
  accumulation mask over the current effective clip box. The result is
  intersected with that clip anyway, so nothing outside it matters. Each glyph
  (visible or not) is filled into it with OR (nonzero, the same scan), which
  gives the exact union with no path accumulation. Path accumulation is
  rejected: it is bounded only by `maxPathPoints`, and nonzero over
  overlapping glyphs is not their union.
- At `ET`: the new clip is acc ∧ current clip (new mask, liveness as §3.4),
  and the accumulation mask is freed. A text object with no clip-mode glyph
  changes nothing.
- A text object that reaches the end of its content stream (or form) without
  `ET`: discard the accumulation, no narrowing (lenient, fail-visible).
- Charges: glyphs pay their usual fill charges. The accumulation mask is
  charged like any mask build and counts toward live clip-mask bytes.
- Open: a clip-mode text object that showed glyphs that are all empty
  (spaces). By the union rule the region is empty and everything is clipped.
  Confirm against the oracle (Q4).

---

## 6. Budget (extends render-budget.md)

Principle unchanged: charge what the loop iterates, per row or per call, never
per pixel.

| Work | Counter | Charge | Initial constant (calibrate) |
|---|---|---|---|
| `W`/`W*` operator | O | 1 step (already) | — |
| Rectangle clip | — | nothing beyond the painting op's step | — |
| Mask build: scan | W | the same as `fillPath`: `fillPointCost` per point, `fillRowCost(k)` per row | shared |
| Mask build: bits | W | `clipRowCost` per mask row + `clipWordCost` per 64-bit word written (zeroing, span OR, AND with the parent), charged per row **before** the row is written | `clipRowCost` ≈ 4, 1 unit per 4 words (BenchmarkCalibration/clip-mask) |
| Mask allocation | live clip-mask bytes | `bytes` added before `make`, removed when dead (§3.4) | `maxClipMaskBytes` = 128 MiB |
| Per-pixel mask test in `blend` | W | none: it is part of a pixel visit | re-measure the pixel-visit reference |
| Phase B accumulation | W, live bytes | as a mask build; glyph fills as today | — |

- **Calibration gate (implementation PR).** Add `clip-mask` (a full-canvas
  non-rectangular clip built repeatedly under `q … Q`), `clip-and` (nested
  intersection) and `blend-masked` (fill under a mask) to
  `BenchmarkCalibration`. Every `px/unit` must land in ≈ 0.9–1.15, native,
  `-cpu=1`, measured as in render-budget.md §10 (process CPU time interleaved
  with the pixel-visit reference).
- **Worst cases this bounds.** `q <non-rect> W n Q` ×N builds N masks, each
  charged about rows × (fill row + `clipRowCost` + words/4). A full-canvas
  triangle mask at Letter 150 DPI (1275 × 1650 px, 20 words per row) costs
  ≈ 1650 × (9 + 4 + 5) ≈ 30 k units, so the 67 M default stops after
  ≈ 2,200 of them: ≈ 0.6 GB of short-lived allocation (264 KB each) in total,
  one mask live at a time. Nested non-rectangular clips stop at the live cap.
  `re W n` ×10^6 costs 10^6 steps and zero allocations.
- **No new public knob.** The live cap is internal, like `maxPathPoints`
  (render-budget.md R-c). The resource name `clip masks` joins the list in
  render-budget.md §4.1 and spec §13.2 (follow-up edits, not made here).
- **Default budget unchanged.** No corpus page may newly trip it (§9).

---

## 7. Determinism (native and wasm byte-identical)

- All new clip code is integer after the existing span rounding: rectangle
  detection compares coordinates with `==`; `rectPixels` uses `fillStartRow`
  and `math.Round`; mask bits, AND and the row extents are integer operations.
  No new float→int conversion bypasses `clampToInt`.
- The scan is shared with `fillPath` (§3.3), so the clip has exactly fill's
  float behavior. Two existing expressions can fuse into a multiply-add on
  arm64 (amd64 and wasm do not fuse): the crossing `a.X + t*(b.X-a.X)`
  (`raster.go:396`) and `matrix.apply`/`mul`, which path construction uses.
  The clip inherits them and adds none. Recommended in the refactor: write the
  crossing as `a.X + float64(t*(b.X-a.X))`. That changes nothing on amd64 or
  wasm and removes one arm64 divergence. Moving path construction to
  `applyExact`/`mulExact` is the broader follow-up already tracked as F5 in
  type1-std14-fonts.md. It is not part of this design, because it changes
  arm64 renders only and needs its own parity check.
- Liveness and the live-mask counter depend only on the operator sequence, so
  a `clip masks` trip happens at the same operator on both targets.
- Gate: the clip fixtures (§10.1) hash-equal under `GOOS=js GOARCH=wasm` and
  native amd64.

---

## 8. Redaction

**Redaction does not rely on the renderer and must not start relying on
clipping.** `core/internal/redact` has its own content model and never calls
`core/render`. Decision C46 (redaction-hardening.md) states the rule this
design keeps: no clip narrows what a region removes; paint items are bounded
only by their own geometry and a form's `/BBox`; `sh` is bounded by the page
box.

What changes once `W` renders:

- **Clipped-away content becomes invisible but stays present.** Text drawn
  entirely outside a `W` clip is not shown by the viewer, yet it remains in
  the content stream, `ExtractText` returns it (extraction ignores clips,
  unchanged), and copy/paste or another tool reveals it. It is the same class
  as `3 Tr` hidden text. Redaction must keep treating it as present:
  - `RedactArea` removes every text operator, image and path meeting the
    region, **including ones the viewer shows nothing for because they are
    clipped**. A user who boxes an area that looks empty still removes what is
    there.
  - `RedactText` matches clipped-away text like any other text.
  - D4 extension and Stage G verification search what the content holds, not
    what is rendered. Unchanged.
- **C46 prerequisite 1 ("core/render implements W/W\* and text clipping
  first") becomes half met after Phase A.** That does **not** make narrowing
  advisable. Narrowing can only under-remove, and the benefit is less
  over-removal. Any future narrowing needs its own redaction design review and
  the full C46 list (integer-only clip modes, empty clips never narrow, widened
  glyph bounds). This design proposes none.
- **Display.** Redaction preview boxes are drawn by the overlay, which must
  never be clipped by page content (§4, mandatory test).
- **Gates.** Phase A touches no redaction code, but it changes the shared
  render package, so the PR runs `testing/security/redaction/` (100%) and the
  corpus no-regression check. Add one leak-corpus row if it is missing: text
  drawn entirely outside a `0 0 1 1 re W n` clip, inside a `RedactArea`
  region, must be removed (by `ExtractText`, the output bytes and Ghostscript's
  text output). `d5_clip_re_W_n` covers the image case.
- **SVG.** An SVG page keeps clipped-away elements in its markup (§11). It is
  a display format, not a sanitizer.

---

## 9. Expected corpus impact and measurement

**Which hashes change.** Only pages whose content (at any form depth) runs a
painting op after `W`/`W*` with a clip that excludes some ink. Expected:

- **Unchanged:** pages with no `W`/`W*`, and pages whose clips contain all
  their ink (the page-sized `re W n` many generators emit first).
- **Changed:** images cropped by a clip (Word, InDesign, browser print), chart
  plot areas (matplotlib, Excel), maps and CAD with clipped viewports,
  rounded-corner and non-rectangular frames (Office, Keynote, Pages), and
  forms whose content clips inside its `/BBox`.
- Ink can also appear where it did not before: an opaque shape that used to
  overpaint and is now clipped reveals what lies under it. Changes are not
  monotone, so "ink only shrinks" is **not** an assertion.

**Census first (Phase 0).** `TestClipCensus` in `testing/corpus/`, gated by
`FLUXDOCS_EXTRA_CORPUS` like `TestFontCensus`. Per page it counts: `W`/`W*`
painting ops, how many are rectangles vs general paths, the largest live mask
count and bytes, the maximum clip nesting, and text objects with modes 4–7
(integer and non-integer). Output: a TSV of counts only, with no text
(SECURITY.md §4 spirit). Results go in a new "Census results" section
appended to this note.

**Hashes.** SHA-256 of decoded pixels per page (72 and 150 DPI, ≤ 8 pages per
file), recorded before Phase A, as in type1-std14-fonts.md §7.2.

- Pages with no `W`/`W*`: byte-identical. Any diff is a bug, typically in the
  §3.3 refactor.
- Pages where every clip is a rectangle containing all ink: byte-identical.
- Other pages: expected to change, reviewed as below.
- `ExtractText`/`ExtractTextByPage` hashes: byte-identical over the whole
  corpus.

**Ghostscript as oracle.** Run `gs` as an external process only, as
`type1_gs_test.go` already does (`-dSAFER -dNOPAUSE -dBATCH
-sDEVICE=pnggray -r72`/`-r150`). Never link it, vendor it, read its source, or
commit its output images. Tests skip without `gs` on `PATH` unless
`FLUXDOCS_REQUIRE_GS=1`.

- **Metric:** ink F1, as in type1-std14-fonts.md §7.2/§7.2.1. Binarize both
  renders (gray < 128 is ink), then precision = |ours ∩ gs| / |ours|, recall
  = |ours ∩ gs| / |gs|, F1 their harmonic mean. Report over the changed pages,
  before and after, at 72 and 150 DPI.
- **The F1 tool is not in the repo** (no committed code computes it). The
  implementation PR adds it, e.g. `testing/corpus/inkf1_test.go`, gated like
  the census, so this and later render notes share one measurement.
- **Exit:** mean F1 over changed pages rises at both DPIs. No changed page
  drops by more than 0.02, among pages without ExtGState alpha (`/CA`, `/ca`)
  or shadings, which the renderer does not model; drops on such pages, and
  those §15 traces to Ghostscript's any-touch fill of tiny shapes, are
  classified rather than counted (measured exceptions: 65 pages at 72 DPI,
  23 at 150 DPI; see §15). Every page below 0.6 IoU, and a stratified sample
  of ≥ 20 changed pages per generator family, gets a manual side-by-side look.
  The §2.2 lenient cases each get a synthetic fixture compared to the oracle,
  and any disagreement is recorded in this note with a decision.
- **Budget and time:** no corpus page newly trips the default budget. p95
  render time regresses < 5% on pages without `W` (the per-pixel test only)
  and < 25% corpus-wide.

---

## 10. Testing

Unit tests live in `core/render/clip_test.go`, DoS gates in
`testing/security/dos/clip_dos_test.go`, and fuzz seeds in
`testing/security/fuzz/`. Everything runs under `-race`; the deterministic
tables also run under `GOOS=js GOARCH=wasm`.

### 10.1 Table-driven semantics (small canvases, geometric assertions)

Each case renders a content stream and asserts ink or no ink at named pixels
or boxes, not binary goldens (`core/render/testdata/golden/` is empty, and
type1-std14-fonts.md Q9 chose geometric assertions):

1. `re W n` then a full-canvas `f`: ink only inside the rect.
2. `re W f`: the fill paints under the **old** clip; a second full-canvas fill
   is clipped (deferred effect).
3. `W S`: the clip is the fill region, not the stroke outline.
4. Nonzero vs even-odd on a ring (two nested rects, same direction): `W`
   keeps the hole filled, `W*` keeps it empty.
5. Monotone: a second, larger clip does not widen the first.
6. `q … W n … Q`: the clip is restored; ink outside after `Q`.
7. Form: a `W` inside a form is undone at exit; an outer `W` clips the form's
   content; `/BBox` ∩ `W`.
8. Images, image masks and `/SMask` images under a non-rectangular clip.
9. Glyphs under a clip (embedded TrueType and Type 1 fixtures).
10. §2.2 cases: `W n` with no path (no narrowing), a zero-area path (all
    clipped), `W` then `Q` then `n`, `W W* n`, a NaN point (all clipped).
11. Overlay: page content ends with an unbalanced `0 0 1 1 re W n`; an overlay
    highlight still paints in full.
12. Rect fast path ≡ mask path: randomized rects (fractional, negative
    width/height, off-canvas, all four `/Rotate` values) built both ways give
    identical masks. Same style as the existing `raster_test.go` oracles.
13. Shared scan: `fillPath` output is byte-identical before and after the
    §3.3 refactor on the existing fill tests.
14. Liveness: a scripted `q`/`W`/`Q`/form sequence asserts the live-byte
    counter after every operator, including `maxSaveDepth` overflow.
15. Phase B: modes 4–7 (union of glyphs, `ET` intersection, `4.5 Tr` renders
    as mode 0, missing `ET`, empty glyphs).

### 10.2 DoS gates (`testing/security/dos/`)

- `q <triangle> W n Q` ×10^6 on a Letter page: trips `paint work` with
  `ErrBudgetExceeded`, within a time bound, RSS below a fixed ceiling.
- 1024 nested non-rectangular full-canvas clips, each painted at the canvas
  cap: trips `clip masks` before 128 MiB of live masks; RSS bounded.
- `0 0 W H re W n` ×10^6: no trip from clipping, `testing.AllocsPerRun` shows
  no per-operator allocation.
- A clip path at `maxPathPoints`: bounded like the equivalent fill.
- Phase B: one `Tj` of a huge string in `7 Tr`: bounded by O and W.

### 10.3 Fuzz

Extend `exercise` seeds with the §2.2 cases, deep `q`/`W` nests, clips with
NaN/Inf and 1e300 coordinates, clip-only forms, `W` inside `BT`, and
`Tr 4.5`/`Tr 7`. Invariant: no panic, return within deadline plus slack.
Each target runs ≥ 10 min with no finding before merge.

### 10.4 CI gates (phase exit)

- `go test -race ./...`; `CGO_ENABLED=0 go build ./...`; `GOOS=js GOARCH=wasm
  go build ./core/... ./wasm/...`; the wasm smoke test renders one clip fixture
  with a hash equal to native amd64.
- `testing/security/redaction/` 100%, plus the redaction corpus no-regression
  check.
- Corpus rules of §9; WASM size delta in the PR description (expected
  < 10 KB gzip).

---

## 11. SVG renderer

There is no SVG renderer yet (`"svg"` → `ErrUnsupportedFormat`). When it lands
(spec §4.1, §11.1 `svg.go`), clipping is a requirement, not an option:

- Each clip becomes a `<clipPath>` holding the device- or user-space path,
  with `clip-rule="nonzero"` or `"evenodd"`. Content after it is wrapped in a
  `<g clip-path="url(#…)">`. An intersection nests groups, and `Q` closes
  them, so `q`/`Q` map to `<g>` nesting.
- A rectangle clip may use a `<rect>` in the `<clipPath>`. The pixel rule
  (§2.1) does not apply: SVG is anti-aliased by the consumer.
- Text clipping (Phase B) becomes a `<clipPath>` of glyph outlines as
  `<path>`, never `<text>`, so the result does not depend on host fonts.
- IDs are deterministic and content-free (`fd-clip-1`, `fd-clip-2`, …), so the
  same page gives the same bytes on both targets and no document text leaks
  into IDs.
- **Clipped-away content is still in the markup.** An SVG export is not
  redaction (§8). Optional later: cull elements whose bounds miss the clip.
  That is only a size optimization and must not be presented as removal.

---

## 12. Open questions (maintainer)

- **Q1. Form `/BBox` rounding.** Today `/BBox` uses outer rounding
  (floor/ceil, a superset); `W` uses the fill rule (§2.1). They can differ by
  one pixel at fractional edges. Options:
  - (a) Keep `/BBox` as is. No form hash changes. **Recommended for Phase A.**
  - (b) Move `/BBox` to the fill rule. Consistent, but changes form pages by
    ≤ 1 px.
  - (c) Additionally clip a rotated or skewed `/BBox` exactly, as a
    four-point path through the mask (fixes form-xobject.md O3 overpaint).
    Small, but it changes rotated-form hashes; a candidate Phase C.
- **Q2. Live-mask cap behavior.** (a) Trip `ErrBudgetExceeded` (`clip
  masks`). **Recommended,** consistent with render-budget D1. (b) Stop
  narrowing past the cap and overpaint silently. (c) Fall back to the mask's
  bounding rectangle. (b) and (c) render hostile pages "successfully" but
  wrongly, and (c) still hides a degradation.
- **Q3. `W` followed by more path construction before the painting op.**
  (a) Keep the mark; clip with the final path (recommended, simplest).
  (b) Drop the mark at the next construction op. (c) Clip with the path as it
  was at `W`. ISO forbids the input; pick after checking the oracle and the
  census for real occurrences.
- **Q4. Phase B edge cases.** A clip-mode text object whose glyphs are all
  empty: clip everything (the union rule) or no narrowing? And whether Phase B
  waits for type1-std14-fonts.md P3 to land (recommended, §5 reason 1).
- **Q5 (non-blocking).** Per-row interval lists (option D) instead of bits, if
  benchmarks on chart- or CAD-heavy pages show span-level clipping dominating.

---

## 13. Effort and phasing

| Phase | Content | Estimate |
|---|---|---|
| P0 | `TestClipCensus`; baseline pixel hashes; ink-F1 tool (§9) | 1.5–2 days |
| **PA** | §3.3 scan refactor (own commit, zero hash changes); `clipState`, rect fast path, masks, liveness and live cap; `pendingClip` in the interpreter and forms; overlay reset; budget charges and calibration; §10.1 items 1–14, §10.2, §10.3; corpus review | 5–7 days |
| PB | Exact-integer `Tr` (own commit); text-clip accumulation; §10.1 item 15; corpus review | 2–3 days, after P3 of type1-std14-fonts.md |
| PC (optional) | Q1 (c): exact rotated `/BBox` | 1 day |
| Review rounds | go-reviewer passes and recalibration, as past render notes needed | 2–4 days |

Total for P0 + PA + PB: about 2–3 weeks of focused work.

Follow-up edits once this is approved (not made by this note): spec §13.2
(clipping in the pipeline, the `clip masks` resource), spec §9.1 (Weeks 3–4
"clipping" status), render-budget.md §4.1 (resource list), form-xobject.md
(general clipping no longer out of scope), redaction-hardening.md C46 (mark
prerequisite 1 as half met, with no change to the rule).

---

## 14. Census results (P0)

`TestClipCensus` (`testing/corpus/clipcensus_test.go`) over the
no-regression corpus: 1,524 files (`testing/corpus` plus 1,491 PDFs found
on a macOS developer machine: system and application artwork, Xcode and
simulator device chrome, Homebrew documentation), first 8 pages each, 1,965
pages walked (14 files encrypted, 5 do not open). Masks are sized at
150 DPI. Counts only; the per-page TSV is not committed.

| Count | Total / max | Pages | Files |
|---|---|---|---|
| painting ops applying a `W`/`W*` | 12,176 | 1,360 | 1,248 |
| of which rectangles (fast path) | 6,523 | 1,197 | 1,085 |
| of which general paths (masks) | 5,534 | 672 | 652 |
| of which `W*` | 3,979 | 474 | 474 |
| of which empty (zero area) | 119 | 3 | 3 |
| `W` with no current point | 0 | 0 | 0 |
| `W` followed by more path construction (Q3) | 0 | 0 | 0 |
| `W` pending across `Q`, `Do`, `BT`/`ET` | 0 | 0 | 0 |
| `W` inside `BT` | 0 | 0 | 0 |
| `W` never painted | 0 | 0 | 0 |
| max live masks / max live bytes | 3 / 7.3 MiB | | |
| max clip nesting | 5 (p50 1, p99 4) | | |
| text objects in `Tr` 4–7 (integer / non-integer) | 0 / 0 | | |

- 69% of the pages clip, about half of them only with rectangles. The live
  cap (128 MiB) is 17× the largest page; p99 live bytes are 1.6 MiB.
- **Q3:** no real occurrence of path construction after `W`, nor of any
  other §2.2 lenient case. Ghostscript agrees with (a) on the synthetic
  fixture (`TestClip_GhostscriptOracle`: `re W re n` clips to both
  rectangles, 800 px in both renderers), so (a) stands.
- **Phase B:** no text clipping in this corpus; the design-tool PDFs it
  targets are absent from it.

---

## 15. Phase A results

**Change split.** The §3.3 scan refactor (`scanPath`, `spanPixels`, the
crossing written as `a.X + float64(t*(b.X-a.X))`, and the same rounding in
the `fill_test.go` oracle) changed 0 of 3,940 page renders (1,970 pages at 72
and 150 DPI) and 0 of 1,510 extraction hashes against the baseline. Clipping
then landed on top: `clip.go` (state, masks, rectangle fast path, liveness),
the interpreter's `pendingClip`, form save/restore, the overlay reset and the
budget counters.

**Corpus.** 670 of 1,970 pages changed (648 at 72 DPI, 670 at 150). Every one
of them clips (558 with a general clip, 112 only with rectangles whose region
excludes ink). No page without `W` changed, no extraction hash changed, and no
page newly trips the budget. `TestOpenManifest` over the extra corpus: 648
rows differ, all only in the page-0 72-DPI render hash, all on clipping pages.

**Ink F1 against Ghostscript** (changed pages; `testing/corpus/inkf1_test.go`):

| DPI | Pages | Mean F1 before → after | Up / down | Drops > 0.02 | IoU < 0.6 before → after |
|---|---|---|---|---|---|
| 72 | 669 | 0.804 → 0.852 | 395 / 92 | 65 | 272 → 208 |
| 150 | 670 | 0.803 → 0.889 | 428 / 48 | 23 | 205 → 49 |

The §9 exit rule "no changed page drops by more than 0.02" is **not** met
literally (65 pages at 72 DPI = 23 alpha + 41 any-touch + 1 shading; 23 at
150 DPI, all alpha). §9 now scopes the rule to pages without unmodelled
alpha or shadings. Every drop was classified, and none is a clip error:

- 46 pages (23 per DPI): stroke or fill alpha from an ExtGState (`/CA`,
  `/ca`), which the renderer does not model. The clip is right (one stroke
  clipped to the inside of a rounded rectangle, one to the outside). The
  strokes now paint opaque where Ghostscript blends them; before, the
  second opaque stroke hid the first everywhere.
- 41 pages at 72 DPI only: precision ≥ 0.95 and less ink than Ghostscript.
  These are tiny shapes (glyph-shaped clips on 38-px badges), and Ghostscript
  fills every pixel a shape touches while we sample pixel centres. They do
  not drop at 150 DPI.
- 1 page: a shading painted under the clip, which the renderer does not draw.

Before, unclipped stand-in paint covered whole boxes and overlapped
Ghostscript's dark ink by accident. Contact sheets (before / after /
Ghostscript) of a stratified sample (up to 20 changed pages per source
family) and of the 49 pages below 0.6 IoU at 150 DPI show the clipped shapes
matching Ghostscript's geometry. What remains is unrendered shadings and
alpha.

**Oracle on §2.2** (`TestClip_GhostscriptOracle`): Ghostscript agrees with
every lenient decision (no-point `W` and `h W n` do not narrow; zero-area
rectangles and one-point paths clip everything; the mark survives `Q`; the
last of `W`/`W*` wins; Q3 (a); `W` inside `BT`; `W` never painted). One
recorded disagreement follows from §2.1: a line path (`m l W n`) keeps a
1-px line in Ghostscript and clips everything here, because a fill of a line
paints no pixel.

**Budget and calibration** (`BenchmarkCalibration`, native, `-cpu=1`):
`clip-mask` 0.92–0.95 px/unit, `clip-and` 1.03–1.11, `blend-masked`
0.94–0.98. Two changes from §6:

- `clipRowCost` = 2, not 4.
- The AND pass is charged only when there is an enclosing mask.

The per-pixel mask test also turned out **not** free: it cost ≈ 30% of a
pixel visit (1.30 px/unit uncharged), so pixels visited under a mask pay
one extra unit per 3 (`maskedPixelsPerUnit`). Unmasked paint is charged
exactly as before, and the existing calibrations are unchanged within noise.
Live cap: Q2 (a), `ErrBudgetExceeded` naming `clip masks`.

**Time.** A sequential render of all 605 no-`W` pages and 400 `W` pages,
minimum of two interleaved rounds: no-`W` p95 +5.8% raw, but CPU profiles
show the PNG encoder (unchanged code) slowing by the same ≈ 9% between the
runs. Normalized to it, the interpreter's share is identical (0.428 both).
Pages with `W`: total +1.6%.

**WASM.** The parity fixture's pixel hash is pinned in `core/render`
(native and `GOOS=js GOARCH=wasm`) and checked by `wasm/smoke_test.mjs`
inside the module. The size delta is +26 KB raw, +9.3 KB gzip.

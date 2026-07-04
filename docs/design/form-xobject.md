# Design: Form XObject rendering (Wave 1)

Status: design (docs only). Tier: **Core (MIT)**. Package: `core/render`.
Spec basis: ISO 32000-1:2008 §8.10.1 (Form XObjects), §8.10 (external objects),
§8.8 (`Do` / external objects), §7.8.3 (resource dictionaries). Clean-room —
derived from the public ISO spec only.

This note tells the implementer exactly what to build. It does not contain Go
code. Hand off to `go-implementer`. It closes open question **O5** of
`docs/design/image-xobject.md` (Form XObjects were deferred there).

---

## 1. Scope

Render **Form XObjects** (`/Subtype /Form`) invoked by the `Do` operator: a
self-contained content stream, with its own `/Matrix`, `/BBox`, and `/Resources`,
interpreted recursively into the same raster surface. This covers the common
cases — headers/footers stamped once, repeated logos/stamps, and PDFs that wrap
page marking content inside a form.

In scope this increment:

- `Do` dispatch to a form when the XObject stream's `/Subtype` is `/Form` (or is
  untyped but carries a `/BBox` and is not an image).
- Implicit `q … Q` around the form (§8.10.1): save state, concatenate `/Matrix`
  onto the CTM, clip to `/BBox`, interpret the form's content stream with the
  form's resources, restore.
- The form's own `/Resources` scope — its own `/Font` and nested `/XObject`
  entries (a form may `Do` an image, or another form).
- A cheap **axis-aligned device-rectangle clip** for `/BBox` (see §5).
- Recursion-depth bound, cycle detection, and a total-invocation budget
  (SECURITY.md §2–3, see §6).

Explicitly **out of scope** (defer; must degrade gracefully — no crash, rest of
page still renders):

- General clipping via `W` / `W*` (arbitrary clip paths). Still ignored, exactly
  as today. Only the form `/BBox` produces a clip, and only an axis-aligned one.
- `/Group` transparency groups (soft masks, isolated/knockout, blend modes).
  A form with a `/Group` is rendered as an ordinary form (group semantics
  ignored). Acceptable Wave-1 fidelity.
- `/BBox` treated as a rotated/skewed clip region. Only its device-space
  bounding box is honoured (see §5, open question O3).
- Reference XObjects (`/Subtype /Form` with `/RefXObject` / `/PtData`), PostScript
  XObjects (`/Subtype /PS`, deprecated). Skip.

Any form the interpreter cannot handle is a **graceful no-op** for that `Do`; the
page still renders.

---

## 2. Dispatch in the existing `Do` path

`Do` already resolves `/Resources /XObject /<name>` to a `*parse.Stream` in
`image.go` `doXObject`. Today, when `/Subtype` is `/Form` it returns early
("nested content streams are out of scope in Wave 1"). This increment replaces
that early return with a branch to the form path.

Revised `doXObject` decision order (keep the existing structure):

1. Resolve `resources["XObject"]` → the XObject sub-dictionary. Absent → no-op.
2. Resolve `xobjDict[name]` → a `*parse.Stream`. Not a stream → no-op.
   **Keep the raw `xobjDict[name]` value too**: if it is a `parse.Reference`, it
   is the cache key and the cycle-detection key (§4, §6).
3. Read `st.Dict.GetName("Subtype")`:
   - `/Form` → form path (this note).
   - `/Image` → existing image path (unchanged).
   - **absent**: disambiguate leniently — if the dict has `/Width`+`/Height` it
     is an image (existing behaviour); else if it has a `/BBox`, treat it as a
     form; otherwise no-op.
4. Form path: hand `st`, the `parse.Reference` (or a sentinel "inline" marker),
   and the current recursion depth to the form interpreter (§3, §7).

---

## 3. Form semantics (§8.10.1)

Executing `Do` on a form is defined as being equivalent to:

1. Save the current graphics state (implicit `q`).
2. Concatenate the form's `/Matrix` onto the CTM.
3. Intersect the current clip with the form's `/BBox` (transformed by the
   now-updated CTM).
4. Paint the form's content stream, resolving names against the form's
   `/Resources`.
5. Restore the graphics state (implicit `Q`).

### 3.1 Form dictionary keys (§8.10.1, Table 95)

| Key | Wave 1 use |
|---|---|
| `/Subtype` (`/Form`, required) | dispatch (see §2) |
| `/BBox` (rectangle, required) | clip region (see §5). Read as a 4-number array and normalise |
| `/Matrix` (array of 6, optional, default identity) | concat onto CTM (§3.2) |
| `/Resources` (dict, optional) | the form's resource scope; fall back to the caller's resources when absent (§4) |
| `/FormType` | should be `1`; ignore the value, do not reject |
| `/Group`, `/BBox` clipping subtleties, `/Matte`, `/OPI`, `/Metadata` | ignored in Wave 1 |

`/BBox` is *required* by the spec. If it is missing or not a 4-number array,
**render the form unclipped** rather than dropping its content (preserves marks;
see open question O2). `parse.resolveRectangle` is unexported, so the render
layer resolves `st.Dict["BBox"]`, expects a `parse.Array` of four numerics, and
normalises min/max itself (reuse the `num()` helper in `interpret.go`).

### 3.2 CTM update — exact math

The form `/Matrix` is a 6-number array `[a b c d e f]`, the same layout as the
`cm` operator. It **pre-multiplies** onto the current CTM, identically to how the
`cm` case already composes in `interpret.go`:

```
formCTM = formMatrix.mul(in.gs.ctm)
```

using the existing `matrix` type and its `mul` (which documents "apply m first,
then n"). This is the same composition order as `case "cm"`. If `/Matrix` is
absent or malformed, use `identity()` (i.e. `formCTM = in.gs.ctm`).

The implicit `q` is realised by pushing `in.gs` onto `in.stack` (exactly like
`case "q"`); the implicit `Q` pops it. Set `in.gs.ctm = formCTM` after the push.
Because CTM, colours, line width, and the clip (§5) all live in `gstate`, the
implicit `q`/`Q` restores them for free.

### 3.3 State that is NOT in `gstate` — save/restore explicitly

The interpreter keeps several fields **outside** `gstate` that the implicit
`q`/`Q` does *not* cover. The form path must save and restore these manually
around the recursion:

- **`in.resources`** and the active **fonts** map — swapped to the form's scope
  (§4) and restored after.
- **`in.tm` / `in.tlm`** (text matrices). A well-formed form wraps text in
  `BT … ET`, which resets `tm`/`tlm`; but a defensive save/restore prevents a
  malformed form from corrupting the caller's text position. Save both, restore
  both.
- **Current path** (`in.subpaths`, `in.closed`, `in.cur`, `curX/curY`,
  `startX/startY`). A conforming stream has no pending path at `Do` time, but to
  be robust, snapshot-and-clear the path state on entry and restore on exit so a
  form that leaves a dangling subpath cannot leak marks into the caller.

The cleanest implementation is a small helper that snapshots these non-`gstate`
fields into a local value, runs the form, then restores them — paired with the
`in.stack` push/pop for `gs`.

---

## 4. Resources and font threading (the central design question)

A form has its **own** `/Resources`. `Tf <name>` inside the form must resolve
`<name>` against the *form's* `/Font`, not the page's; a nested `Do` must resolve
against the form's `/XObject`. Today fonts are loaded **once** in `render.go`
`Page` via `loadFonts(r, pi.Resources)` and passed into `run` as a fixed map, and
`in.resources` is fixed to the page resources. Both must become **stack-scoped**.

### 4.1 Threading model

Two interpreter fields become the "current scope", swapped on form entry and
restored on exit:

- `in.resources parse.Dict` — already exists; set to the form's `/Resources`.
- the active fonts map (today `in.fonts map[parse.Name]*glyphFont`) — set to the
  fonts of the form's `/Resources`.

**Fallback (spec §8.10.1):** if the form has no `/Resources`, it inherits the
resources of the environment it is used in. Implement this as: when the form's
`/Resources` is absent, **do not swap** — keep the caller's `in.resources` and
fonts unchanged. (This also matches real files that omit `/Resources` and rely on
the page's.)

### 4.2 Font cache — recommended: cache keyed by the form's XObject reference

`loadFonts` parses every embedded font program with `sfnt.Parse`, which is not
free. A form stamped N times (common for headers/logos) must not re-parse its
fonts N times. Recommended cache:

- Add `in.fontCache map[parse.Reference]map[parse.Name]*glyphFont`.
- On form entry, if the XObject was reached through a `parse.Reference` (the
  usual case) and the form has its own `/Resources`:
  - cache hit → reuse the fonts map;
  - miss → `loadFonts(in.res, formResources)`, store under the reference, use it.
- If the XObject entry is **inline** (a direct stream, no `Reference`), reload
  fonts for that invocation without caching (rare; acceptable).

Keying by the form's XObject `Reference` (not by the `/Resources` dict) is
correct because `parse.Dict` is a Go map and is neither comparable nor hashable,
whereas `parse.Reference{Number, Generation}` is a comparable struct. Two forms
that happen to share one `/Resources` object will parse it twice — a negligible
cost that keeps the key simple and safe.

**Trade-off / simpler alternative:** drop the cache entirely and call
`loadFonts` on every form `Do`. This is *correct* and much simpler, but a form
invoked in a loop re-parses its fonts each time — potentially the dominant cost
on stamp-heavy PDFs. Recommendation: ship the reference-keyed cache; it is a few
lines and removes a real performance cliff. (Open question O1.)

### 4.3 Refactor `run` into a shared, reusable entry point

`run` currently constructs the `interp` and loops `in.apply(op)`. Split it so the
page and a form share one interpretation core:

- Keep `run(rs, base, ops, fonts, res, resources)` as the **page** entry point:
  construct the `interp` (now also initialising `fontCache`, `formDepth = 0`, the
  clip = full canvas, and the cycle-path set), then call the new core.
- Add an interpreter method — sketch `in.interpret(ops []parse.ContentOp)` — that
  just runs the `for _, op := range ops { in.apply(op) }` loop. Both the page and
  every form call this.
- Add the form runner — sketch
  `in.execForm(st *parse.Stream, ref parse.Reference, hasRef bool)`:
  1. depth/cycle/budget guard (§6); on any tripwire, return (no-op).
  2. snapshot non-`gstate` fields (§3.3); push `in.gs`.
  3. resolve `/Matrix`, set `in.gs.ctm = formMatrix.mul(savedCTM)`.
  4. resolve `/BBox`, intersect `in.gs.clip` (§5).
  5. swap `in.resources` + active fonts to the form scope (§4), or keep caller's
     on fallback.
  6. decode the form content stream (`parse.DecodeStream(st)`), `parse.ScanContent`,
     then `in.interpret(ops)`.
  7. restore fonts/resources/non-`gstate` fields; pop `in.gs`; decrement depth;
     remove `ref` from the cycle-path set.

`apply`'s `case "Do":` is unchanged; `doXObject` gains the form branch that calls
`execForm`. One interpretation core, reused by page and forms — no duplicate
operator switch.

The form's content stream is **decoded with `parse.DecodeStream`** (Flate/raw,
same as the page content path). A form using an unsupported stream filter →
decode error → graceful no-op.

---

## 5. Clipping to `/BBox` — recommended: cheap axis-aligned device rect

Clipping is not modelled today (`W`/`W*` are ignored). Two options for `/BBox`:

- **(a) Skip BBox clipping.** Simplest, but a form whose content draws outside
  its `/BBox` (legal, and relied upon by `/BBox` to crop) will **overpaint** the
  surrounding page. For headers/footers/stamps this is usually benign, but some
  files deliberately draw a large background inside a small `/BBox` — those would
  bleed. Real risk, visible artefacts.
- **(b) Axis-aligned device-rectangle clip for `/BBox`.** Cheap, safe, and
  removes the overpaint risk for the overwhelmingly common case where `/Matrix`
  is axis-aligned (translation/scale, no rotation). **Recommended.**

**Recommendation: (b).** Introduce a minimal device-space clip:

- Add `clip` to `gstate` as an axis-aligned integer device rectangle
  (`x0, y0, x1, y1`, half-open). It is part of `gstate`, so `q`/`Q` (and the
  implicit form `q`/`Q`) save and restore it automatically. Initialise it in
  `run` to the full canvas `(0, 0, rs.w, rs.h)`.
- On form entry, transform the four `/BBox` corners by the form CTM (§3.2), take
  the device-space min/max, floor/ceil to integers, and **intersect** with the
  current `in.gs.clip`. An empty intersection ⇒ the form paints nothing (still
  run it? no — short-circuit and skip; it cannot produce visible marks).
- **Enforcement — single choke point.** Every mark ultimately goes through
  `raster.blend(x, y, …)`. Give `raster` a `clip` rectangle field and have
  `blend` reject pixels outside it (it already does a `[0,w)×[0,h)` bounds test —
  add the clip test alongside). `span` may early-out against the clip's x-range
  for speed, and `drawImage` should clamp its device bounding box to the clip.
  Because fills, strokes, images, and **text glyphs** all funnel through
  `blend`, one check clips them all.
- The interpreter keeps `raster.clip` in sync with `in.gs.clip`: set it whenever
  the clip changes — i.e. on form entry, and after any `Q` (including the form's
  implicit `Q`) restores `in.gs`. Since a page renders sequentially, mutating the
  shared `raster.clip` is safe.

Scope discipline: this clip machinery is introduced **only** for form `/BBox`.
`W`/`W*` general path clipping stays out of scope and continues to be ignored;
do not wire it up here. (Rotated/skewed `/Matrix` makes the axis-aligned box
larger than the true clip → mild overpaint only, never lost content — see O3.)

---

## 6. Security — recursion, cycles, and total work (SECURITY.md §2–3)

Forms may `Do` other forms, so a hostile or accidentally cyclic file can drive
unbounded or exponential recursion. Defences, all resulting in a **graceful
skip** (never panic, never error the render):

- **Depth bound (primary).** Add `in.formDepth int`, incremented on `execForm`
  entry and decremented on exit. Introduce a constant — proposed
  `maxFormDepth = 12` — and when `in.formDepth >= maxFormDepth`, skip the form.
  12 comfortably exceeds real nesting (typically 1–3) while stopping runaway
  recursion.
- **Cycle detection (secondary).** A depth bound alone lets a 2-form cycle
  A→B→A→… burn `maxFormDepth` frames of work on every offending `Do`. Keep a
  set of the XObject references **currently on the recursion path** —
  `in.formPath map[parse.Reference]bool`. On entry, if `ref` is already present,
  skip immediately (self-reference or cycle); otherwise add it, and remove it on
  exit. This makes direct/indirect self-reference O(1) to reject. (Inline,
  reference-less forms cannot form a resolvable cycle, so they need no path
  entry.)
- **Total-invocation budget (breadth / fan-out).** Depth bounds nesting but not
  breadth: a form that sequentially `Do`s many sub-forms, each doing the same,
  is bounded in depth yet can still explode in total invocations. Add a per-page
  counter `in.formBudget int` (proposed cap ~`4096` successful form executions
  per page render); when exhausted, subsequent form `Do`s are skipped. This caps
  total work regardless of the depth/cycle guards.
- **Content-size bound.** The form content stream is decoded via
  `parse.DecodeStream`, which already caps inflate output (decompression-bomb
  guard). No new bound needed for the stream itself; the budget + depth caps
  bound the *number* of decodes.
- **Clip degeneracy.** An empty `/BBox` device intersection short-circuits (no
  paint), so a zero-area form does no work.

Every tripwire (depth, cycle, budget, bad `/BBox`, decode error, non-stream) is a
silent skip, consistent with the renderer's existing "ignore what we can't do"
policy (`render.go` package doc).

WASM: the entire path is pure Go (parse + raster only); no filesystem, no
network. Nothing to add for the privacy path.

---

## 7. Package-internal API sketch (no Go code)

All changes are package-internal to `core/render`; there is **no public API
change** (the exported `Page` signature is unchanged). Touch `interpret.go`,
`image.go`, `raster.go`; no new file strictly required (a `form.go` is optional
for tidiness).

- **`interp` new fields** (`interpret.go`):
  - `formDepth int` — current recursion depth.
  - `formPath map[parse.Reference]bool` — references on the active path (cycle
    guard).
  - `formBudget int` — remaining form-execution budget.
  - `fontCache map[parse.Reference]map[parse.Name]*glyphFont` — per-form fonts.
  - (the active fonts map + `in.resources` already exist; they become the
    swapped "current scope").

- **`gstate` new field** (`interpret.go`): `clip` — an axis-aligned device
  rectangle, saved/restored by `q`/`Q`.

- **`raster` new field** (`raster.go`): `clip` — the active device clip rect;
  `blend` (and optionally `span`, `drawImage`) clamp to it. Initialised to the
  full canvas in `newRaster` or by `run`.

- **`run` refactor** (`interpret.go`): construct `interp` (init the new fields,
  clip = full canvas), then call `in.interpret(ops)`. New method
  `in.interpret([]parse.ContentOp)` holds the apply loop, shared by page and
  forms.

- **`doXObject` branch** (`image.go`): on `/Subtype /Form` (or untyped-with-BBox),
  call `in.execForm(st, ref, hasRef)` instead of returning.

- **`in.execForm(st *parse.Stream, ref parse.Reference, hasRef bool)`**
  (`interpret.go` or `form.go`): guards (§6) → save non-`gstate` + push `gs`
  (§3.3) → CTM concat (§3.2) → BBox clip (§5) → resource/font swap (§4) →
  decode + `ScanContent` + `in.interpret` → restore.

- **Matrix helpers**: none new. `mul`, `apply`, and (from the image increment)
  `invert` already exist; the CTM concat and BBox-corner transform use `mul` /
  `apply`.

---

## 8. Testing strategy

- **Golden-file (render)** — fixtures under `core/render/testdata/`, PNG goldens
  under `.../golden/`:
  - a form invoked once with a translating `/Matrix` (verify placement);
  - the **same** form `Do`'d twice at different `cm` transforms (verify state
    isolation via the implicit `q`/`Q` — the second placement must not inherit
    the first's colour/CTM);
  - a form with its **own `/Font`** drawing text (verify font-scope switch, not
    the page's fonts);
  - a form whose content **overflows its `/BBox`** (verify the device-rect clip
    crops it; compare against the (a) no-clip variant to prove the clip fires);
  - a form that `Do`s an **image XObject** from its own `/Resources` (nested Do);
  - a form with **no `/Resources`** relying on the page's (fallback path).
- **Table-driven (unit)**: `/Matrix` composition equals `formMatrix.mul(ctm)`;
  `/BBox` corner→device min/max intersection with an existing clip; the
  untyped-stream dispatch (`/Width`+`/Height` ⇒ image, `/BBox` ⇒ form).
- **Security / robustness** (must render without panic, simply omitting the
  offending form):
  - **self-referential** form (`A` does `Do A`) → depth+cycle guard;
  - **mutually recursive** forms (`A→B→A`) → cycle guard;
  - deeply nested chain beyond `maxFormDepth`;
  - a fan-out bomb (a form that `Do`s many sub-forms) → budget guard;
  - a form with a malformed/missing `/BBox` (renders unclipped, no crash);
  - a form whose content stream uses an unsupported filter (decode error → skip).
  - Add a fuzz seed (form dict with cyclic `/XObject` + short content) under
    `testing/security/fuzz/` if a render fuzz target exists.
- **CI**: green for **native and WASM** (pure-Go path).

---

## 9. Open questions (maintainer decision)

- **O1 — Font cache vs reload.** Recommended: reference-keyed `fontCache`
  (§4.2). Alternative: no cache, reload per `Do` (simpler, slower on stamp-heavy
  files). Confirm the cache is worth the small extra state.
- **O2 — Missing/invalid `/BBox`.** `/BBox` is spec-required. Recommended:
  render the form **unclipped** (preserve marks) rather than dropping it.
  Alternative: skip the form entirely (safer against overpaint, loses content).
  Confirm the preference.
- **O3 — Non-axis-aligned `/Matrix`.** The axis-aligned device-rect clip is a
  *superset* of a rotated/skewed true `/BBox` clip, so a rotated form can mildly
  overpaint beyond its true bounds (never lose content). Accept for Wave 1, or is
  a true polygon clip required now? (Recommended: accept; document the
  limitation. A real path clip is a larger, separate piece of work covering
  `W`/`W*` too.)
- **O4 — Constants.** `maxFormDepth = 12`, form budget `~4096` per page — confirm
  or tune. These bound worst-case work; they should be generous vs real PDFs.
- **O5 — `/Group` transparency.** Wave 1 ignores group semantics (renders the
  form flat). Confirm that transparency groups / blend modes are firmly a later
  wave.

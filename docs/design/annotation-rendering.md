# Design: Annotation Rendering (raster overlay)

Status: Wave 1 · Tier: Core (MIT) · Package: `core/render` + `core`
Spec basis: ISO 32000-1:2008 §8.4 (annotations), FluxDocs CLAUDE.md §1.4/§9 (the
annotation layer is stored separately and applied at render/flatten time), §5/§8.2
(design tokens: default annotation amber `#f59e0b`).

## Goal

When `RenderPage` rasterizes a page, draw the four Wave-1 annotation types
(highlight, note, draw, shape) on top of the page content — a **non-destructive
overlay**. The source PDF and the JSON annotation layer are unchanged; the
annotations are painted only into the output bitmap. Permanent baking
(`FlattenAnnotations`) is a separate follow-up (sketched in §7).

## 1. Cross-package type (architectural constraint)

`core` imports `core/render`, so `render` **must not** import `core` (import
cycle). The renderer therefore defines its own plain value type — it does not see
`core.Annotation`:

```
render.Annotation{ Type string; X,Y,Width,Height float64; Color string; Opacity float64; Points []render.AnnPoint }
render.AnnPoint{ X, Y float64 }
```

`Type` is `"highlight" | "note" | "draw" | "shape"`. `X,Y,Width,Height` and
`Points` are in **PDF user space** (origin bottom-left, points), exactly as
`core.Annotation.Rect`/`Points`. `core.RenderPage` translates its
`[]core.Annotation` (from `GetAnnotations(pageID)`) into `[]render.Annotation`,
dropping the deferred types (stamp/redact/signature), and passes them to the
renderer.

## 2. Delivery into the rasterizer

Add `Annotations []Annotation` to `render.Options` (NOT a new `Page` parameter —
keeps the existing `Page(r, pi, opts)` signature and every current caller/test
working). `Page` draws the overlay after the page content, before `encode`:

```
run(rs, base, ...)                 // existing page content
if len(opts.Annotations) > 0 {
    drawAnnotations(rs, base, opts.Annotations)
}
return encode(rs, ...)
```

An empty overlay is a no-op, so a page with no annotations renders byte-identical
to today (existing golden/pixel tests unaffected).

Gating at the core level: `RenderPage` draws annotations by default (the product
promise — annotations are applied at render time). Add `RenderOptions.SkipAnnotations bool`
(default false = draw) so a caller can request the clean page without a breaking
default.

## 3. Coordinate mapping

Annotations are transformed by the **same device matrix** the page content uses
(`base` from `deviceMatrix`, PDF→device px incl. `/Rotate` + DPI), so they align
with the page and honour rotation/scale. Reuse `matrix.apply`, `raster.fillPath`,
`raster.strokePath`, `raster.blend`. Drawing after the content places them on top.

## 4. Per-type drawing recipe (device space)

Common: parse `Color` (hex) → `color.RGBA`; alpha `A = round(Opacity*255)`
(clamped [0,1]; Opacity is already screened finite/clamped by the data layer).

- **highlight** — a translucent filled rectangle over the Rect. Transform the four
  Rect corners by `base`, `fillPath` (nonzero) with the color at the annotation's
  alpha. Source-over blend over the text underneath gives the highlighter look
  (the caller chooses a translucent Opacity, e.g. 0.4; the renderer honours
  whatever alpha is set).
- **shape** — a stroked rectangle outline over the Rect (Wave 1 = rectangle;
  ellipse / other shape-kinds carried in `Meta` are deferred). `strokePath` the
  four corners as a closed path, device width ~1.5px, at full/authored alpha.
- **draw** — a freehand polyline through `Points` (stroke). Transform each point
  by `base`, `strokePath` an open polyline, device width ~2px. &lt;2 points → skip.
- **note** — a small solid marker at the Rect (Wave 1: a filled square of a fixed
  device size, ~14px, anchored at the Rect's top-left corner in PDF space =
  (X, Y+Height)). Rendering the note's `Content` text is deferred.

Color parsing: `#rgb` and `#rrggbb` accepted; anything invalid/empty → default
amber `#f59e0b`. This helper lives in the render package.

## 5. Security / robustness (SECURITY.md §2–3)

Annotations may originate from untrusted imported JSON. Defences:
- `raster.blend` already bounds-checks the canvas, so an off-page or huge-coord
  annotation cannot write out of bounds; drawing is clamped to the canvas.
- Cap the points processed per `draw` annotation (`maxAnnPoints`, e.g. 100000) so
  a hostile Points list can't make rendering unbounded.
- Never panic on a malformed annotation: bad hex → default color; empty/short
  Points → skip; non-finite is already rejected at the data layer, but the color
  parser and coordinate transforms must still be total (no panic).
- No new network/filesystem; WASM-safe (pure raster).

## 6. Backward compatibility

`render.Options` gains a field (additive, zero value = no overlay). `RenderPage`
gains behaviour (draws annotations when present) — existing `RenderPage` tests use
docs with no annotations, so output is unchanged. `render.Page` signature is
unchanged.

## 7. FOLLOW-UP (sketch only — separate increment): FlattenAnnotations

Permanent bake into the page **content stream** (no device matrix — PDF user
space directly):
- highlight → set alpha via an `ExtGState` `/ca`, `rg` + `re` + `f`.
- shape → `re` + `S`; draw → `m`/`l`…/`S`; note → a small filled `re` marker.
- Wrap the appended ops in `q … Q` and append to the page `/Contents` (an array,
  or wrap the existing single stream), then rebuild a new `*Document` via the
  writer (`parse.BuildDocument`). Returns a NEW Document (flattened annotations
  can't be edited); the JSON layer for those annotations is cleared on the copy.
- Open questions: ExtGState resource injection into `/Resources`; append-vs-wrap
  of existing content; interaction with `/Rotate`. Deferred to its own note.

## 8. Open questions

- O1 Highlight default alpha when Opacity is the data-layer default 1.0 (opaque
  block hides text). Recommend: renderer honours authored Opacity verbatim; the UX
  layer/default should author highlights at ~0.4. (No renderer special-casing.)
- O2 `note` marker style (solid square vs a drawn "note" glyph). Recommend the
  simple square for Wave 1.
- O3 Stroke widths (shape/draw) — fixed device px vs scaled by DPI. Recommend
  fixed device px for legibility at any DPI.

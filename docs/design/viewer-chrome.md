# Design: Viewer chrome — base stylesheet, zoom, keyboard nav, responsive toolbar

Status: design (docs only); Q1–Q4 maintainer-approved 2026-10-15 (§10).
Tier: **Core (MIT)**. Packages: `packages/core`
(stylesheet only), `packages/react`, `packages/vue`. **No Go / WASM change.**
Spec basis: `docs/spec.md` §8.2 (tokens), §8.3 (toolbar layout), §8.4 (keyboard
map), §8.5 (a11y). Rules: `.claude/CLAUDE.md` §5 (BEM `fd-`, `--fd-*`),
`.claude/SECURITY.md` §2 (canvas-size bounds) and §3 (WASM privacy).

This note tells `ui-implementer` exactly what to build. It contains no
production code. Hand off to `ui-implementer`, then `ui-tester`.

---

## 1. Scope

**In scope**

1. A framework-agnostic base stylesheet for the `fd-viewer` DOM contract, using
   the §8.2 tokens, with a light and a dark theme.
2. Zoom: zoom out / zoom in / fit-to-width buttons, a "100%" readout,
   Ctrl/Cmd `+` `-` `0`, re-rendering at a zoom-appropriate DPI.
3. Arrow Up/Down page navigation (§8.4), scoped to the viewer.
4. A toolbar that wraps cleanly down to a 320 px container and offers 44 px
   touch targets on coarse pointers.
5. React and Vue parity: same DOM, classes, labels, keys, and behavior.

**Out of scope (leave for later notes)**

- Continuous scroll / multi-page layout, thumbnails sidebar, annotation tools
  (§8.3 left/right panes).
- Pinch-zoom and Ctrl+wheel interception (the browser keeps them).
- Moving rendering off the main thread (Web Worker). `renderPage` is
  synchronous today; see §4.6.
- Controlled `page`/`zoom` props (decided out, §10 Q2) and a `theme` prop
  (not needed, §2.4).
- Search (Cmd/Ctrl+F), Save (Cmd/Ctrl+S), Escape, Delete from §8.4 — they need
  features that do not exist yet.

**Tier and build targets.** Pure TS + CSS under `packages/`, MIT. No CGO, no
Go change: `wasm/main.go` `renderPage` already honors `opts.dpi` (read with
`v.Int()`, so it must be an integer) and `core/render` already rejects canvases
over `maxCanvasPixels` (64 Mi px). `pageSize` is already exported. The native
build is unaffected.

---

## 2. Stylesheet: how it ships

### 2.1 Location and export (decided — §10 Q1)

Recommended: **one canonical file `packages/core/styles.css`**, shipped by
`@fluxdocs/core`:

- `package.json`: add `"./styles.css": "./styles.css"` to `exports`, add
  `"styles.css"` to `files`, add `"sideEffects": ["*.css"]` so bundlers never
  tree-shake the import.
- Plain CSS at the package root. No build step, no preprocessor, no CSS-in-JS,
  no runtime dependency. `tsc` does not touch it.
- Hosts import it once:
  `import "@fluxdocs/core/styles.css";` (Vite/webpack/Next) or
  `<link rel="stylesheet" href=".../@fluxdocs/core/styles.css">`.

Why core and not per-wrapper: the DOM contract is identical in React and Vue
(and future web-components), so one file prevents drift. Hosts already depend
on `@fluxdocs/core` directly because they call `loadFluxDocs` (see
`examples/react-vite-demo/src/App.tsx`), so pnpm's strict `node_modules`
does not hide it.

Alternatives considered: (B) a copy in each wrapper — drift risk, two files to
keep in sync; (C) a new `@fluxdocs/styles` package — cleanest separation, but a
new package to publish/version for one file. Revisit (C) when web-components
lands.

The components do **not** import the CSS themselves (no side-effect import in
JS): that would break SSR/Node consumers and test runners that cannot load CSS.
The viewer must remain usable (unstyled but functional) without the sheet.

### 2.2 Rules for the file

- Every selector is a single `fd-` class (specificity 0,1,0) or a class plus an
  attribute/pseudo-class. No element selectors at top level, no IDs, no
  `!important`. Hosts override with a same-specificity rule loaded later, or —
  preferred — by setting tokens.
- Not wrapped in `@layer`: an unlayered host reset (e.g. `button { all: unset }`)
  would otherwise beat every layered rule. See §11 Q6.
- **Privacy (SECURITY.md §3):** no `@import`, no `@font-face`, no `url(...)`.
  The sheet must never cause a network request. `Inter`/`JetBrains Mono` are
  used only if the host already provides them; otherwise `system-ui` applies.
  `ui-tester` asserts this (§9).
- Defend against common host resets: `.fd-viewer__canvas { max-width: none;
  width: 100%; height: 100%; }` (it fills the inline-sized `__page` box) —
  Tailwind preflight sets `img { max-width: 100%; height: auto }`, which would
  otherwise cancel zoom.
  `.fd-viewer__btn` sets `font: inherit; color: inherit;` and its own
  background/border so `button` resets do not leave it invisible.
- `box-sizing: border-box` on `.fd-viewer` and `.fd-viewer *`.

### 2.3 Tokens

`:root` declares the §8.2 tokens verbatim. The viewer uses these **missing**
tokens, now part of spec §8.2 (§10 Q4):

| Token | Light / value | Why |
|---|---|---|
| `--fd-primary` | `#6366f1` | Named in CLAUDE.md §5, absent from §8.2 |
| `--fd-focus-ring` | `var(--fd-primary)` | Focus indicator (§8.5) |
| `--fd-fg-muted-dark` | `#a1a1aa` | §8.2 has no dark muted text |
| `--fd-border-dark` | `#27272a` | §8.2 has no dark border |
| `--fd-page-border-dark` | `#3f3f46` | Page edge on dark canvas |
| `--fd-danger` | `#b91c1c` | Error text (`#ef4444` fails AA, see §7.3) |
| `--fd-danger-dark` | `#f87171` | Error text in dark |
| `--fd-control-size` | `32px` (44px on coarse pointer) | Button size, §6 |

Hover/active backgrounds are derived, not new tokens:
`color-mix(in srgb, var(--fd-fg) 8%, transparent)` (12% active). Where
`color-mix` is unsupported the declaration is dropped and the button simply has
no hover fill — acceptable degradation.

### 2.4 Dark theme activation

Components consume only the semantic names (`--fd-bg`, `--fd-fg`, …). Dark mode
**re-points the semantic names to their `-dark` siblings on `.fd-viewer`**:

```css
/* the remap block (written twice: media query + attribute) */
--fd-bg: var(--fd-bg-dark);
--fd-bg-canvas: var(--fd-bg-canvas-dark);
--fd-fg: var(--fd-fg-dark);
--fd-fg-muted: var(--fd-fg-muted-dark);
--fd-border: var(--fd-border-dark);
--fd-page-border: var(--fd-page-border-dark);
--fd-danger: var(--fd-danger-dark);
color-scheme: dark;
```

Applied by:

1. **Auto (default):** `@media (prefers-color-scheme: dark)` on
   `.fd-viewer:not([data-fd-theme="light"], [data-fd-theme="light"] *)`.
2. **Explicit dark:** `[data-fd-theme="dark"] .fd-viewer,
   .fd-viewer[data-fd-theme="dark"]`.
3. **Explicit light:** carrying `data-fd-theme="light"` on an ancestor or the
   root excludes rule 1; light is the base state. `.fd-viewer` sets
   `color-scheme: light` by default. On the root itself it also beats a dark
   ancestor (e.g. `<html data-fd-theme="dark">`):
   `.fd-viewer[data-fd-theme="light"]` comes after rule 2 with the same
   specificity and sets each remapped token back to `inherit` (the parent
   still holds the light value, since rule 2 only targets `.fd-viewer`).

Hosts switch theme by putting `data-fd-theme="light|dark"` on `<html>` or any
ancestor of the viewer — **no new prop needed**. (Vue also forwards
non-prop attrs to the root, so `<FluxDocsViewer data-fd-theme="dark">` works
there; React does not spread unknown props, so React hosts use an ancestor.)
Overrides: a host changes `--fd-bg` for light and `--fd-bg-dark` for dark, at
`:root` or any ancestor. Limitation (documented, not solved): nested conflicting
`data-fd-theme` ancestors are not supported; set it once.

The rendered PDF page is never inverted or filtered in dark mode — only the
chrome and the canvas surround change.

---

## 3. DOM structure and class list

Ready state (error/empty states unchanged except for §7 additions):

```
div.fd-viewer[.{className}]                       onKeyDown handler (§5)
├─ div.fd-viewer__toolbar
│  ├─ div.fd-viewer__group.fd-viewer__group--nav   role=group aria-label="Page navigation"
│  │  ├─ button.fd-viewer__btn.fd-viewer__nav.fd-viewer__nav--prev   aria-label="Previous page"  ‹
│  │  ├─ span.fd-viewer__page-info                 role=status aria-live=polite aria-atomic=true
│  │  │      "Page N of M"
│  │  └─ button.fd-viewer__btn.fd-viewer__nav.fd-viewer__nav--next   aria-label="Next page"  ›
│  └─ div.fd-viewer__group.fd-viewer__group--zoom  role=group aria-label="Zoom"
│     ├─ button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--out  aria-label="Zoom out"  −
│     ├─ span.fd-viewer__zoom-level                role=status aria-live=polite aria-atomic=true
│     │      "100%"
│     ├─ button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--in   aria-label="Zoom in"  +
│     └─ button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--fit  aria-label="Fit to width"  Fit
└─ div.fd-viewer__viewport                         tabindex=0 role=region aria-label="Document"
   └─ div.fd-viewer__page                          inline style width/height (CSS px)
      └─ img.fd-viewer__canvas                     alt="Page N", width/height 100% of __page
```

Full class list:

| Class | Element | New? |
|---|---|---|
| `fd-viewer` | root | existing |
| `fd-viewer__toolbar` | toolbar | existing |
| `fd-viewer__group`, `--nav`, `--zoom` | button groups (wrap units) | new |
| `fd-viewer__btn` | every toolbar button (shared look) | new |
| `fd-viewer__nav`, `--prev`, `--next` | page buttons | existing |
| `fd-viewer__page-info` | "Page N of M" | existing |
| `fd-viewer__zoom`, `--out`, `--in`, `--fit` | zoom buttons | new |
| `fd-viewer__zoom-level` | "100%" readout | new |
| `fd-viewer__viewport` | scroll container, focus target | new |
| `fd-viewer__page` | sized page box (future annotation overlay host) | new |
| `fd-viewer__canvas` | page `<img>` | existing (moves inside `__page`) |
| `fd-viewer__page--natural` | page box when geometry is unusable: no inline size, `width: fit-content` (§4.3) | new |
| `fd-viewer__canvas--natural` | page `<img>` at its natural bitmap size (paired with `__page--natural`) | new |
| `fd-viewer__error` | error text | existing |
| `fd-viewer__empty` | empty-state text | existing |

Preserved strings (existing tests depend on them): `Page N of M`,
`No document loaded.`, `Failed to display PDF: …`, `aria-label` "Previous
page" / "Next page", `role="alert"` on the error root, `disabled` on the nav
buttons at the ends, and exactly **one** `<img>` in the ready state. Glyphs are
`‹ › − +` (`−` is U+2212). Buttons keep `type="button"`.

`role="toolbar"` is deliberately **not** used: the ARIA toolbar pattern implies a
single tab stop with Left/Right roving focus, which v1 does not implement. Five
plain buttons as individual tab stops is correct without it.

---

## 4. Zoom model

### 4.1 Units

- `zoom` is a number; `1` = 100% = **true physical size**: 1 pt = 96/72 CSS px.
- Display size of the page box (CSS px), from `doc.pageSize(page)`:
  `cssW = W × 96/72 × zoom`, `cssH = H × 96/72 × zoom`, where `(W, H)` are the
  MediaBox width/height **swapped when `rotation` is 90 or 270** (`pageSize`
  returns the unrotated MediaBox; `core/render` swaps when rasterizing).
- Readout: `Math.round(zoom × 100) + "%"`.

### 4.2 Range and steps

- Clamp: **`ZOOM_MIN = 0.25`, `ZOOM_MAX = 4`.**
- Ladder: `[0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4]`.
- Zoom in = smallest ladder value `> zoom + 1e-6`; zoom out = largest
  `< zoom − 1e-6`. This handles off-ladder values produced by fit-to-width
  (e.g. 0.87 → in = 1, out = 0.75).
- Zoom-out button `disabled` at `ZOOM_MIN`, zoom-in at `ZOOM_MAX`.
- Reset (Ctrl/Cmd+0) → `1`.
- Fit to width (one-shot, not a sticky mode — it does not track later
  resizes): `zoom = clamp((viewport.clientWidth − 2 × pagePaddingPx) /
  (W × 96/72), ZOOM_MIN, ZOOM_MAX)` rounded to 2 decimals. If `clientWidth ≤ 0`
  (hidden container, jsdom) it is a no-op. `pagePaddingPx` is the viewport's
  inline padding read via `getComputedStyle` (fallback 16 when unparseable,
  which is what jsdom yields with no stylesheet).

### 4.3 Re-render at DPI vs CSS-scale — decision

**Both, combined:** the page box is always sized by CSS (§4.1), so the visible
size is exactly right regardless of bitmap resolution; the bitmap is
re-rendered at a DPI matched to the zoom, capped by a pixel budget.

```
baseDpi    = renderOptions.dpi ?? 96 × min(devicePixelRatio || 1, 2)
targetDpi  = baseDpi × zoom
budgetDpi  = min(72 × sqrt(RENDER_BUDGET_PX / (W × H)),   // W, H in pt
                 72 × RENDER_BUDGET_PX / max(W, H))        // long side alone
dpi        = max(1, floor(min(targetDpi, budgetDpi)))      // integer, ≥ 1
px(dpi)    = max(1, ceil(W·dpi/72)) × max(1, ceil(H·dpi/72))   // as core/render
if px(dpi) > RENDER_BUDGET_PX: dpi = largest d in [1, dpi] with px(d) ≤ budget
```

`px` counts pixels exactly as `core/render` allocates them (each side rounded
up, minimum 1 px), so an extreme aspect ratio (e.g. 5e8 × 1 pt) cannot slip
past the area-only formula. If even `px(1) > RENDER_BUDGET_PX` (e.g. a
1e6 × 1e6 pt MediaBox), the page is not rendered at all: `fitsBudget` is false,
`renderPage` is never called, and the viewer shows the error state with
"page is too large to render".

- `RENDER_BUDGET_PX = 8 × 2^20` (8 Mi px ≈ 32 MiB RGBA in WASM memory, before
  PNG encode and base64). That is 1/8 of the Go hard cap (`maxCanvasPixels`
  = 64 Mi px, SECURITY.md §2), so the client never trips the server-side
  error for a normal page, and the synchronous main-thread render stays
  bounded. US Letter: 100% @ 2× DPR = 3.4 Mi px (full sharpness); 200% @ 2×
  = 13.7 Mi px wanted → capped at ≈ 300 DPI (slightly soft). Above the cap the
  browser upscales — correct size, reduced sharpness. Tune after `ui-tester`
  measures render time (§11 Q7).
- `dpi` is an integer so equal zooms produce equal render keys: the existing
  `JSON.stringify(opts)` key in `usePageImage` dedupes renders for free. React
renders in a layout effect (not during render) and remembers the last
(doc, page, opts) in a ref, so StrictMode's double render and effect re-run do
not rasterize a page twice; the image still commits before paint.
- **Must never pass `dpi: 0`:** `core/render` treats `≤ 0` as 150 DPI, which
  can bypass the client budget. Hence `max(1, …)`.
- Untrusted geometry: if `pageSize` throws, or `W`/`H` are not finite and
  `> 0`, skip inline sizing (natural image size), use `baseDpi × zoom`
  clamped to `[1, 600]`, and let the Go-side cap reject anything hostile. Do
  not surface a `pageSize` failure as a viewer error.
- Pass the rest of `renderOptions` through unchanged (`format`, `quality`,
  `skipAnnotations`), overriding only `dpi`.

Rejected: (a) CSS-scale only — blurry above 100% on any DPR, and cheap is not
worth unreadable small print in legal/medical docs. (b) DPI-only with natural
image size — display size would depend on DPR and on the host's `dpi`, and zoom
would not be "true 100%".

**Behavior change to note in the changelog:** today `renderOptions.dpi`
also determines the displayed size (`dpi: 150` shows at ~156% of physical
size). After this change it controls **sharpness at 100%** only; the
displayed size is set by `zoom`. Both demos pass `{ dpi: 150 }`; they should
drop it (let DPR decide) once this lands.

### 4.4 State and public API (decided — §10 Q2)

Uncontrolled only in v1, mirroring the existing `initialPage`:

| React prop | Vue prop / event | Type | Default |
|---|---|---|---|
| `initialZoom` | `initialZoom` (`:initial-zoom`) | `number` | `1` (clamped) |
| `onPageChange` | emit `pageChange` (`@page-change`) | `(pageIndex: number) => void` | — |
| `onZoomChange` | emit `zoomChange` (`@zoom-change`) | `(zoom: number) => void` | — |

- Callbacks fire after every change of the effective value **except the initial
  mount**, including programmatic clamps (e.g. a new document with fewer
  pages). They fire once per change, never with an unchanged value.
- Vue: declare `emits: ["pageChange", "zoomChange"]`.
- Add the props to `FluxDocsViewerProps` (React). No new exported types, no new
  exported functions.

### 4.5 Scroll behavior

- Page or document change → `viewport.scrollTop = 0` (keep `scrollLeft`),
  except an ArrowUp page back from inside the viewport (the edge rule, §5),
  which lands at the **bottom** of the previous page
  (`scrollTop = scrollHeight − clientHeight`) so the next ArrowUp keeps
  scrolling up instead of skipping another page. Buttons, ArrowDown, and
  ArrowUp from the toolbar land at the top.
- Zoom change → keep the viewport's center point at the same relative position
  in the page box (store center ratio before, restore after layout: React
  `useLayoutEffect`, Vue `watch(..., { flush: "post" })`).
- Centering: `.fd-viewer__page { margin: 16px auto; }` (block centering).
  **Do not** use `justify-content: center` / `align-items: center` on the
  scroll container — overflow on the start side becomes unreachable when
  zoomed in.

### 4.6 Cost

Each zoom step is one synchronous `renderPage` call on the main thread. Steps
are discrete (≤ 8 from min to max), so key-repeat on Ctrl+`=` is bounded. No
debounce in v1. If `ui-tester` measures > 300 ms per step on the Letter /
2× DPR / 200% case, follow up with a worker-based renderer (separate note).

---

## 5. Keyboard handling

Listener: `onKeyDown` (React) / `onKeydown` (Vue) on the **root `.fd-viewer`
element**. Never on `window`/`document` — the viewer only reacts when focus is
inside it (viewport or a toolbar button), so host shortcuts and browser zoom
elsewhere on the page are untouched. The viewport has `tabindex="0"` so a
keyboard user can focus the document area (this is also required for keyboard
access to a scrollable region, WCAG 2.1.1).

Ignore the event entirely when any of:

- `event.defaultPrevented` or `event.isComposing` (IME);
- the target is editable: `input`, `textarea`, `select`, or
  `isContentEditable` (future note-editing popups live inside the viewer);
- no document is loaded (error/empty states).

| Key | Modifiers | Action | `preventDefault` |
|---|---|---|---|
| ArrowDown | none | next page (rule below) | only if handled |
| ArrowUp | none | previous page (rule below) | only if handled |
| `+` or `=` | Ctrl or Cmd, no Alt | zoom in | **always** |
| `-` or `_` | Ctrl or Cmd, no Alt | zoom out | **always** |
| `0` | Ctrl or Cmd, no Alt | reset to 100% | **always** |

- Match on `event.key` (layout-aware; numpad `+`/`-` report the same keys).
  Ctrl = `ctrlKey`, Cmd = `metaKey`; accept either on every platform.
- Zoom combos always `preventDefault` while focus is in the viewer — even at the
  limit — otherwise hitting the cap would suddenly zoom the whole host page.
- Arrow keys with any modifier (Shift/Ctrl/Alt/Meta) are ignored (left to
  browser/host: text selection, OS shortcuts).

**Arrow edge rule** (now written into spec §8.4 — §11 Q5): when zoomed in, the
viewport scrolls, and stealing Arrow keys would make scrolling by keyboard
impossible. So:

- Target inside `.fd-viewer__viewport`: ArrowDown changes page only if the
  viewport cannot scroll further down
  (`scrollTop + clientHeight ≥ scrollHeight − 1`); ArrowUp only if
  `scrollTop ≤ 0`. Otherwise do nothing and let the native scroll happen.
  ArrowDown lands at the top of the next page, ArrowUp at the bottom of the
  previous page (§4.5), so holding either key reads continuously.
- Target in the toolbar: always change page (buttons do not scroll).
- At the first/last page the key is a no-op (not handled, no `preventDefault`).

jsdom reports all scroll metrics as 0, so in component tests the viewport is
always "at the edge" and Arrow keys always page — convenient and correct.

**Focus retention:** when a focused button becomes `disabled` (Next on the last
page, Zoom in at 400%), browsers drop focus to `<body>`, which also detaches
the keyboard listener. After any state change, if `document.activeElement` is
a now-disabled button inside the viewer, move focus to the viewport with
`focus({ preventScroll: true })`.

---

## 6. Layout and responsive behavior

- Root: `display: flex; flex-direction: column; height: 100%; min-height: 0;
  container: fd-viewer / inline-size; font: var(--fd-font-size-base)/1.4
  var(--fd-font-sans); color: var(--fd-fg); background: var(--fd-bg)`. If the
  host gives no height, `100%` resolves to auto and the viewer grows with the
  page (current behavior); if the host sizes it, the viewport scrolls.
- Root also has `width: 100%; contain-intrinsic-inline-size: auto 320px`.
  Inline-size containment sizes the box as if it were empty. `width: 100%`
  fills a container of definite width, but in a shrink-to-fit host (flex item
  without a width, inline-block, float, absolutely positioned) the percentage
  cannot resolve and the viewer would collapse to 0 px; the intrinsic inline
  size makes it 320 px there instead. Such hosts should give the container an
  explicit width.
- Toolbar: `display: flex; flex-wrap: wrap; align-items: center;
  justify-content: space-between; gap: 4px 12px; padding: 4px 8px;
  border-bottom: 1px solid var(--fd-border)`. Groups are `inline-flex` with
  `gap: 4px` and never wrap internally (`flex-wrap: nowrap`).
- `__page-info` and `__zoom-level`: `font-variant-numeric: tabular-nums`,
  `white-space: nowrap`, `min-width: 4.5ch` on zoom-level so "25%"→"400%"
  does not shift the buttons.
- Viewport: `flex: 1 1 auto; min-height: 0; overflow: auto;
  scrollbar-gutter: stable; padding: 16px; background: var(--fd-bg-canvas)`.
  The stable gutter keeps `clientWidth` constant when a fitted page grows tall
  enough to scroll, so fit-to-width does not overshoot with classic scrollbars.
- Page box: `background: #fff; outline: 1px solid var(--fd-page-border);
  box-shadow: var(--fd-page-shadow)`. An outline, not a border: under
  `border-box` a border would shrink the bitmap to (W−2)×(H−2), and the
  outline takes no layout space. Canvas `display: block; width: 100%;
  height: 100%; max-width: none`.

**Breakpoint — container query, not viewport media query.** The viewer is
embedded in arbitrary layouts; its own width is what matters.

- Base (any width): flex-wrap means the two groups wrap onto two rows when they
  do not fit. No horizontal scrolling of the toolbar, ever.
- `@container fd-viewer (max-width: 479px)`: each group takes a full row
  (`flex: 1 1 100%; justify-content: space-between`), viewport padding drops
  to 8px, page margin to 8px.
- Without container-query support (pre-2023 browsers) the base wrap still
  yields a usable layout.

Budget at 375 px with 44 px targets: nav group = 44 + ~80 + 44 + gaps ≈ 180 px;
zoom group = 44 × 3 + ~48 + gaps ≈ 196 px; total with padding ≈ 400 px, so at
375 px the groups wrap to two rows — the expected layout. At ≥ 480 px desktop
(32 px buttons) they sit on one row.

**Touch targets.** `--fd-control-size: 32px` by default (passes WCAG 2.2
2.5.8 Target Size Minimum, 24 px; 2.1 AA has no size criterion — 2.5.5 is AAA).
Under `@media (pointer: coarse)` it becomes `44px` (Apple HIG / 2.5.5 AAA),
keyed on input type rather than width so a narrow desktop window keeps dense
32 px controls (§8.1 "high density") and a tablet gets 44 px. Buttons are
`min-width/min-height: var(--fd-control-size)`.

---

## 7. Accessibility (WCAG 2.1 AA, §8.5)

### 7.1 Names and live regions

- Every button has an accessible name (table in §3). "Fit to width" contains
  the visible text "Fit" (2.5.3 Label in Name).
- `__page-info` and `__zoom-level` are `role="status"` (implicitly
  `aria-live="polite"`; set `aria-live`/`aria-atomic` explicitly too for older
  AT). They announce "Page 2 of 3" / "125%" on change, not on mount.
- Viewport: `role="region"` + `aria-label="Document"`; the image keeps
  `alt="Page N"`. (The PDF content itself is out of WCAG scope per §8.5.)
- Error root keeps `role="alert"`.

### 7.2 Focus and motion

- `.fd-viewer__btn:focus-visible, .fd-viewer__viewport:focus-visible {
  outline: 2px solid var(--fd-focus-ring); outline-offset: 2px; }`. Viewport
  uses `outline-offset: -2px` (inside, so `overflow` does not clip it). No
  `outline: none` without a replacement.
- Disabled buttons: `opacity: 0.4; cursor: not-allowed` (inactive components
  are exempt from 1.4.3/1.4.11).
- Transitions only on button `background-color` using `--fd-transition-fast`
  (not `color`: a theme switch must not fade the text).
  `@media (prefers-reduced-motion: reduce)` sets `transition-duration: 0s` on
  viewer elements. No `scroll-behavior: smooth`, no zoom/page animation in v1.

### 7.3 Contrast (computed, WCAG relative-luminance formula)

| Pair (fg on bg) | Light | Dark | Requirement |
|---|---|---|---|
| `--fd-fg` on `--fd-bg` (toolbar text, glyphs) | `#18181b`/`#fafafa` **16.97:1** | `#fafafa`/`#0a0a0a` **18.97:1** | 4.5:1 text |
| `--fd-fg-muted` on `--fd-bg` | `#71717a`/`#fafafa` **4.63:1** | `#a1a1aa`/`#0a0a0a` **7.73:1** | 4.5:1 text |
| `--fd-fg-muted` on `--fd-bg-canvas` | `#71717a`/`#e5e7eb` **3.90:1 — FAIL** | `#a1a1aa`/`#18181b` 6.91:1 | do not use muted text on the canvas |
| `--fd-fg` on `--fd-bg-canvas` (empty state) | `#18181b`/`#e5e7eb` **14.31:1** | `#fafafa`/`#18181b` **16.97:1** | 4.5:1 text |
| `--fd-danger` on `--fd-bg` (error) | `#b91c1c`/`#fafafa` **6.20:1** | `#f87171`/`#0a0a0a` **7.15:1** | 4.5:1 text |
| `#ef4444` (`--fd-draw-default`) on `#fafafa` | 3.61:1 — FAIL | — | why `--fd-danger` exists |
| `--fd-focus-ring` on `--fd-bg` | `#6366f1`/`#fafafa` **4.28:1** | `#6366f1`/`#0a0a0a` **4.43:1** | 3:1 non-text |
| `--fd-focus-ring` on `--fd-bg-canvas` | `#6366f1`/`#e5e7eb` **3.61:1** | `#6366f1`/`#18181b` **3.97:1** | 3:1 non-text |
| `--fd-border` on `--fd-bg` | 1.19:1 | — | decorative only |

Consequences: `__page-info`/`__zoom-level` may use `--fd-fg-muted` (toolbar
background) but `__empty` must use `--fd-fg` (it sits on the canvas color).
Button boundaries are not relied on for identification (glyph/text label is,
at ≥ 16:1), so the 1.19:1 border is fine under 1.4.11. One focus color works
in both themes.

---

## 8. React / Vue parity

| Concern | React | Vue |
|---|---|---|
| Component | `FluxDocsViewer` function component | `defineComponent` + render function (no SFC) |
| New props | `initialZoom?: number` | `initialZoom: { type: Number, default: 1 }` |
| Page/zoom events | `onPageChange`, `onZoomChange` props | `emits: ["pageChange", "zoomChange"]` |
| Key handler | `onKeyDown` on root | `onKeydown` on root |
| Viewport ref | `useRef<HTMLDivElement>` | `ref<HTMLDivElement>()` via `ref` vnode prop |
| Post-layout scroll restore | `useLayoutEffect` | `watch(..., { flush: "post" })` |
| Fire callbacks except on mount | compare with previous via ref | `watch` (non-immediate) |
| Theme attr on root | ancestor only (no prop spread) | ancestor, or fallthrough attr on component |
| Pure logic | `src/viewer-model.ts` | `src/viewer-model.ts` (identical copy) |

**Shared logic.** Zoom ladder/clamp, fit computation, DPI computation, display
size, and key classification are pure functions. Following the `dataurl.ts`
precedent, place an identical `viewer-model.ts` in each wrapper (not exported
from the package index), with an identical unit-test file in each. Moving it to
`@fluxdocs/core` would be a new public export — see §11 Q8.

Constants (both copies): `ZOOM_MIN = 0.25`, `ZOOM_MAX = 4`,
`ZOOM_STEPS = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4]`,
`RENDER_BUDGET_PX = 8 * 2 ** 20`, `CSS_PX_PER_PT = 96 / 72`,
`MAX_DPR = 2`.

---

## 9. Testing strategy

### 9.1 Unit (vitest, both wrappers, table-driven) — `test/viewer-model.test.ts`

- `zoomIn`/`zoomOut`: every ladder value, off-ladder (0.87, 0.3, 3.5), limits
  (no change at 0.25/4), clamp of `initialZoom` (0, −1, NaN, 10).
- `fitZoom`: normal; `clientWidth` 0 → no-op; tiny width → `ZOOM_MIN`; huge →
  `ZOOM_MAX`; rotation 90 uses height.
- `renderDpi`: DPR 1/2/3 (3 capped to 2); host `dpi` overrides base; budget
  cap on Letter @ 200% DPR 2 (expect ≈ 300); A0 page; degenerate/NaN/negative
  size → fallback path; result is always an integer ≥ 1 (never 0).
- `displaySize`: rotation 0/90/180/270.
- `classifyKey`: table over key × modifiers × target (input, textarea,
  contenteditable, button, viewport) × `isComposing` → action | none, and the
  `preventDefault` flag.

### 9.2 Component (vitest + jsdom) — extend `test/viewer.test.{tsx,ts}`

Existing five tests must pass **unchanged**. Extend `test/mock.ts` to record
`renderPage` options (test-only change). New cases, mirrored in React and Vue:

1. Readout starts at "100%"; Zoom in → "125%", Zoom out ×5 from 100% →
   "25%" with Zoom out `disabled`; Zoom in to "400%" → Zoom in `disabled`.
2. `renderPage` receives an integer `dpi` that scales with zoom; repeated equal
   zoom does not trigger another render.
3. `.fd-viewer__page` inline width = `200 × 96/72 × zoom` px for the mock's
   200×300 page (266.67px at 100%; tolerance 0.01).
4. Ctrl+`=` / Cmd+`=` / Ctrl+`-` / Ctrl+`0` dispatched on the viewport change
   zoom and are `defaultPrevented`; the same event dispatched on an element
   **outside** the viewer is not prevented and changes nothing.
5. ArrowDown/ArrowUp on the viewport change page; Shift+ArrowDown does not; an
   `<input>` appended inside the viewport swallows nothing (no page change).
6. `onPageChange`/`onZoomChange` (React) and `pageChange`/`zoomChange` emits
   (Vue): not called on mount; called once per change with the new value.
7. Focus retention: focus Next, click to the last page →
   `document.activeElement` is `.fd-viewer__viewport`.
8. A11y attributes: `role="status"` on page-info and zoom-level; groups have
   names "Page navigation"/"Zoom"; viewport `tabindex="0"`, `role="region"`,
   name "Document"; exactly one `img`.
9. Fit to width: stub `clientWidth` on the viewport (e.g. 400) → readout
   `Math.round((400 − 32) / 266.67 × 100)%`; with 0 → unchanged.

### 9.3 Stylesheet lint (node --test in `packages/core`)

- Every `var(--fd-*)` referenced is declared in `:root`.
- Every class selector starts with `fd-`; no `!important`, no `@import`,
  no `@font-face`, no `url(` (privacy gate).
- Contrast gate: parse the token values and assert the §7.3 "required" pairs
  meet their thresholds in light and dark, so a future token edit cannot
  silently regress AA.

### 9.4 Browser (`ui-tester`, Playwright against `examples/react-vite-demo` and `examples/vue-demo`)

- axe-core: 0 serious/critical violations in light and dark
  (`emulateMedia({ colorScheme })`) and with `data-fd-theme` on `<html>`
  overriding the OS preference both ways.
- 375×667 and 320×568 with `hasTouch` (coarse pointer): toolbar
  `scrollWidth ≤ clientWidth`, groups on two rows, every button ≥ 44×44 px;
  desktop 1280 px: one row, 32 px buttons. Screenshots for review.
- Keyboard-only: Tab order prev → next → zoom out → zoom in → fit → viewport;
  focus ring visible on each; Arrow keys page; Ctrl/Cmd `+`/`-` zoom the
  viewer; focus outside the viewer → Ctrl `+` is **not** prevented.
- Zoomed-in page: Arrow keys scroll first, page only at the edge.
- Sharpness: at DPR 2 and 100%, `img.naturalWidth ≈ 2 × cssWidth`.
- Host-reset robustness: inject `img{max-width:100%;height:auto}
  button{all:unset}` — zoom still changes the page box size; buttons stay
  visible and focusable.
- `prefers-reduced-motion: reduce`: computed `transition-duration` is `0s`.
- **Privacy:** record all network requests during load, page flip, zoom, fit,
  theme switch — none beyond the demo's own static assets and the `.wasm`; no
  font requests caused by the stylesheet.
- Perf probe: time per zoom step on a Letter page at DPR 2 (100%→400%); report
  numbers for §11 Q7.

---

## 10. Decisions (maintainer-approved, 2026-10-15)

These are settled. Implement exactly as stated; do not reopen them in review.

- **Q1 — Ship `@fluxdocs/core/styles.css`.** One canonical file in
  `packages/core`, exported as `./styles.css`, listed in `files`, with
  `"sideEffects": ["*.css"]` (§2.1). No per-wrapper copies, no new package.
- **Q2 — Uncontrolled only.** React: `initialZoom`, `onPageChange`,
  `onZoomChange`. Vue: `initialZoom` prop + `pageChange`/`zoomChange` emits
  (§4.4). No controlled `page`/`zoom` props and no `v-model` yet.
- **Q3 — `renderOptions.dpi` = sharpness at 100%.** It is the base DPI that
  zoom multiplies; display size comes from `zoom` only (§4.3). Note the change
  in the changelog. Both demos (`examples/react-vite-demo`,
  `examples/vue-demo`) drop `renderOptions={{ dpi: 150 }}` /
  `:render-options="{ dpi: 150 }"`.
- **Q4 — Spec amended.** `docs/spec.md` §8.2 now lists the eight tokens from
  §2.3 with light/dark values and documents `data-fd-theme` +
  `prefers-color-scheme`; §8.4 now has Ctrl/Cmd+0 and the Arrow-key edge rule.
  The spec and this note must stay in agreement — if the CSS needs a token
  change, update §8.2 in the same PR.

## 11. Open questions

**Resolved here with a recommendation (no decision needed unless the
maintainer disagrees):**

- **Q5 — Arrow keys vs scrolling.** §8.4 says Arrow Up/Down change page; taken
  literally it breaks keyboard scrolling when zoomed. Resolved: edge-aware rule
  (§5).
- **Q6 — `@layer fluxdocs`.** Easier host overrides, but any unlayered host
  reset would beat our rules. Resolved: unlayered, single-class specificity,
  tokens as the main override API.
- **Q7 — Render budget 8 Mi px and zoom max 400%.** Resolved provisionally;
  revisit with `ui-tester` perf numbers (§9.4). Lowering ZOOM_MAX to 300% is
  the fallback if 400% is too soft/slow.
- **Q8 — Duplicated `viewer-model.ts`.** Resolved: duplicate (matches
  `dataurl.ts`); consolidate into a shared module when web-components arrives.
- **Q9 — Ctrl/Cmd+0 reset.** Added because the viewer already intercepts
  Ctrl/Cmd `+`/`-` and leaving `0` to the browser would reset the host page's
  zoom instead. Now in spec §8.4 (via Q4).
- **Q10 — Default zoom on narrow containers.** Resolved: default stays 100%
  (true size; horizontal scroll on phones). A future `initialZoom: "fit-width"`
  value is a candidate for a later increment (Q2 keeps `initialZoom` numeric
  for now).

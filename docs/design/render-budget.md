# Design: per-page render budget and cancellation

Status: design approved (docs only). The public API is settled in §10, so it is
ready for `go-implementer`. Tier: **Core (MIT)**. Packages: `core/render`
(counters, checks), `core` (options, sentinel, context entry points), `wasm`,
`packages/core` (TS mirror), plus the server callers (`examples/go-server-demo`,
`mcp`, future `cloud/api` render route). Pure Go, no CGO, no build tag. Both
targets (native, `GOOS=js GOARCH=wasm`).
Basis: SECURITY.md §2 (untrusted input must not cause a DoS); ISO 32000-1:2008
§7.8.2 (content streams), §8.2 (graphics objects), §8.5 (path painting), §8.9
(images), §8.10.1 (Form XObjects), §9.4 (text-showing operators). Clean-room.

Settled by the maintainer (2026-10-15), not re-opened here:

- **D1.** Exceeding the budget returns an error, sentinel
  `ErrRenderBudgetExceeded`. No partial image.
- **D2.** The budget is **on by default** on every target, with defaults
  adjustable through `RenderOptions`. Server render paths additionally pass a
  deadline through `context.Context`.

---

## 1. Threat model

Commit 7d31ef4 bounded each paint operator by the canvas: one stroke, fill, or
image costs at most O(canvas pixels), and the canvas is capped at
`maxCanvasPixels` = 64 Mi px. Nothing bounds the **number** of such operators.

Measured (go-reviewer): ≈ 4.75 ns per pixel visit native, ≈ 23 ns wasm. One
full-canvas wide stroke at the cap costs ≈ 0.3 s native / 1.5 s wasm. About 1000
of them (≈ 30 KB of content) is minutes native and tens of minutes in a tab. A
content stream may decode to `maxDecoded` = 256 MiB (`core/parse/filter.go`),
so the attacker has room for millions of such operators.

Other amplifiers in the current code:

| Amplifier | Where | Cost driver |
|---|---|---|
| Repeated full-canvas `f`/`S`/`Do` | `raster.go`, `image.go` | ops × canvas pixels |
| `fillPath` scans **every** edge on every row, then sorts | `raster.go` `fillPath` | rows × E (+ crossings log) per fill, independent of painted area |
| Image re-decode on every `Do` (no cache) | `image.go` `decodeImage` | Do count × w×h samples (≤ 64 Mi each), even for a 1-px placement |
| Form content re-decoded + re-scanned on every `Do` | `form.go` `execForm` | up to `maxFormBudget` 4096 × 256 MiB, even if the form has no operators (whitespace/comments) |
| One `Tj`/`TJ` with a huge string | `text.go` | glyph count × (`LoadGlyph` + fill setup); one operator |
| Unbounded current path | `interpret.go` `lineTo`/`curveTo` (16 pts per `c`) | memory, then rows × E at paint |
| Freehand annotations | `annotation.go` (`maxAnnPoints` 100k per annotation) | segments × rows; overlay drawn into the same raster |

Severity: **high** for server-side rendering (go-server-demo, MCP server, the
planned Cloud render route) where one upload pins a CPU core for minutes and
`RenderAllPages` multiplies it by `NumCPU`. **Lower** for WASM (the victim is the
user's own tab), but a hostile document shared with a reviewer still freezes the
viewer, so the budget is on there too (D2).

Goal: every page render finishes, or fails with an error, within a
**deterministic** work bound that scales with the output size. On server paths it
also finishes within a **wall-clock** bound. Results are identical on both
targets.

---

## 2. What is counted

One `budget` value per `render.Page` call. It is owned by the page's single
goroutine, so it needs no atomics. All counters are `int64`. Principle: **charge
what the loop actually iterates**, so the counter stays proportional to real CPU
time whatever the algorithm does.

| Counter | Charged where | Units | Limit |
|---|---|---|---|
| **W: raster work** | `span` (+width after clip clamp), `fillPath` (+edges examined per row; +E setup), `line` (+`lineRowCost` per row + span width), `drawImage` (+box width per row) | ≈ one pixel visit (≈ 4.75 ns native) | `MaxPaintPixels` (public, canvas-scaled default) |
| **O: interpreter steps** | `interpret` (+1 per operator at any form depth), `drawText` (+1 per glyph shown or advanced, invisible modes included), `drawTextArray` (+1 per array element) | steps | `MaxOperators` (public) |
| **S: image samples** | `doXObject` before `decodeImage` (+w×h, + SMask w×h) | samples | internal `maxImageSamples` |
| **C: content bytes** | `render.Page` (+page content bytes, decoded within what is left by `parse.PageContentLimit`), `execForm` (+decoded form length per invocation, charged **before** `ScanContent`) | bytes | internal `maxContentBytes` |
| **D: decoded bytes** (added after review) | every image, image-mask, soft-mask and font-program decode (`budget.decode`): the bytes the filters produce beyond the image's sample bytes; a JPEG's compressed bytes beyond its frame's raw size | bytes | internal `maxDecodedBytes` |
| **P: path points** | `moveTo`/`lineTo`/`curveTo`/`rect` (points in the current path) | points | internal `maxPathPoints` (not cumulative, reset by paint/`n`) |

Notes:

- `blend` is **not** instrumented. Every charge is per row or per call (one add
  and one compare), never per pixel.
- Pixels are charged when **visited**, not when changed. A span fully rejected by
  the clip or by `src.A == 0` still costs its iterations.
- The annotation overlay (`drawAnnotations`) draws through the same raster and is
  charged to the **same** budget. The budget is per `RenderPage` call, not per
  layer.
- Form XObject guards stay as they are: `maxFormDepth` 12 and `maxFormBudget`
  4096, both silent skips. Their content is now also charged to O and C, which
  closes the empty-but-huge form case. Per-image skips (oversize, unsupported)
  stay silent. Only the cumulative budgets produce an error.
- An image skipped as unsupported (filter, color space, bit depth) after its
  size is validated has already been charged its w×h samples to S: the charge
  comes before the decoder decides.
- `lineRowCost` (≈ 8) covers the per-row bisection in `line`, about 2·log2(steps)
  predicate calls. It is an internal constant, calibrated by benchmark (§8).

### 2.1 Companion change: active-edge `fillPath` (required)

As written, `fillPath` costs rows × E even for a thin shape. A legitimate GIS or
CAD polygon with 50k vertices spanning 1650 rows (Letter, 150 DPI) already costs
82 M edge visits, more than the 150-DPI default in §3. Charging the work honestly
would fail such pages. Replace the per-row full scan with an active edge table:
sort edges by `ymin` once, keep only the edges that cross the row, and update
x incrementally. That brings the cost to O(E log E + Σ crossings + pixels).

This must land **before or together with** enabling the default. It is internal
to `core/render` and has no API impact. Golden files must stay byte-identical,
because the sampling rule (row centers, half-open `[min,max)`) does not change.

---

## 3. Defaults and how they scale

Zero value means default, so existing callers get the budget automatically (D2).

**`MaxPaintPixels` (W)** = clamp(32 × canvasPx, 16 Mi, 1 Gi), where canvasPx =
pxW × pxH after `deviceMatrix`.

- 32 is the overdraw allowance. Real pages touch each pixel a few times
  (background, images, text, overlay), and 32× leaves an order of magnitude of
  headroom. With §2.1 in place, edge work for real text and vector pages is a
  small fraction of pixel work.
- The floor keeps tiny thumbnails from failing on edge or row overhead.
- The ceiling caps the worst case at max canvas: 1 Gi × 4.75 ns ≈ **5.1 s
  native**, × 23 ns ≈ **25 s wasm**. Today this is unbounded.

| Letter page (612×792 pt) | canvas px | W budget | worst case native | worst case wasm |
|---|---|---|---|---|
| 72 DPI | 0.48 M | 16 Mi (floor) | 0.08 s | 0.39 s |
| 96 DPI (go-server-demo default) | 0.86 M | 27.6 M | 0.13 s | 0.63 s |
| 150 DPI (default) | 2.1 M | 67.3 M | 0.32 s | 1.55 s |
| 300 DPI (viewer at DPR 2) | 8.4 M | 269 M | 1.28 s | 6.2 s |
| ≥ ~600 DPI / A0 @ 200 DPI | ≥ 32 Mi | 1 Gi (ceiling) | 5.1 s | 24.7 s |

The budget scales linearly with DPI², so worst-case time per output pixel is
constant. A caller that raises DPI gets proportionally more budget.

**`MaxOperators` (O)** = 8 Mi steps, independent of DPI. Dense real-world CAD
pages run to roughly 1–3 M operators, and text pages to ≤ 100 k glyphs. Per-step
overhead without painting is **not measured yet**. The estimate is ≈ 100 ns
native, which would put the 8 Mi worst case at ≈ 0.8 s native and ≈ 4 s wasm. The
implementation PR must benchmark it (§8) and adjust the default to keep the
worst case ≤ ~1 s native.

**Internal caps**, not exposed in this increment:

- `maxImageSamples` = 256 Mi per page. That is four max-size images, or dozens of
  600-DPI scans. It is DPI-independent because a scanned page decodes its full
  sample grid even at a 72-DPI thumbnail. Charging decode to W would fail common
  scanned PDFs at low DPI.
- `maxContentBytes` = 512 MiB per page (2 × `maxDecoded`). It counts the page's
  own content plus every form invocation's decoded bytes. The page's own content
  is further capped at `parse.MaxPageContent` = 256 MiB (§9 R1), so at least
  256 MiB stays for forms.
- `maxPathPoints` = 4 Mi points in the current path (≈ 64 MiB of `point`s).
- `maxDecodedBytes` = 512 MiB per page (added after review, §10). Every decode
  runs through `parse.DecodeStreamLimit`, capped at what is left, so one `Do`
  cannot inflate past the budget.

**Same defaults on both targets.** A page passes or fails identically natively and
in the browser, keeping one core and two targets with identical behavior. WASM
callers that want a tighter wall-clock bound pass `timeoutMs` (§5).

---

## 4. Public API (Go)

> **Public-API change, approved 2026-10-15** (Q1–Q5 in §10). The public surface
> is `core/types.go` and `core/document.go`; there is no `core/fluxdocs.go`.
> Spec §6.1, §7.2, §10.2, §13.2 and §13.3 already reflect it.

```go
// core/types.go — RenderOptions gains two fields (flat, like the existing ones).
type RenderOptions struct {
	DPI             int
	Format          string
	PageRange       []int
	Quality         int
	SkipAnnotations bool

	// MaxPaintPixels bounds the raster work of one page render, in pixel visits
	// (fills, strokes, images, glyphs, and the annotation overlay; overdraw counts
	// every time). 0 = default: 32 × the page's canvas pixels, clamped to
	// [16 Mi, 1 Gi]. < 0 = unlimited (unsafe for untrusted input). A positive
	// value is an absolute limit, not scaled by DPI.
	MaxPaintPixels int64

	// MaxOperators bounds the interpreter steps of one page render: content
	// operators at every Form XObject depth plus every glyph shown. 0 = default
	// (8 Mi); < 0 = unlimited (unsafe for untrusted input).
	MaxOperators int64
}

// core/errors.go
// ErrRenderBudgetExceeded — a page render exceeded a resource limit (raster
// work, interpreter steps, image samples, content bytes, path size, or the
// canvas-size cap). Deterministic for a given document and options: retrying
// does not help; raise the limit in RenderOptions or reject the document.
ErrRenderBudgetExceeded = errors.New("fluxdocs: render budget exceeded")

// core/document.go — context-aware entry points; the existing signatures are
// unchanged and delegate with context.Background().
func (d *Document) RenderPageContext(ctx context.Context, index int, opts RenderOptions) ([]byte, error)
func (d *Document) RenderAllPagesContext(ctx context.Context, opts RenderOptions) ([][]byte, error)

func (d *Document) RenderPage(index int, opts RenderOptions) ([]byte, error) // = RenderPageContext(context.Background(), …)
func (d *Document) RenderAllPages(opts RenderOptions) ([][]byte, error)      // = RenderAllPagesContext(context.Background(), …)
```

```go
// core/render — internal to the module; render cannot import core (cycle).
var ErrBudgetExceeded = errors.New("render: budget exceeded")
// ErrCanvasTooLarge marks the deviceMatrix canvas cap. Its message keeps
// "exceeds"; core wraps it with ErrRenderBudgetExceeded (Q4).
var ErrCanvasTooLarge = errors.New("render: canvas exceeds pixel cap")

type Options struct {
	// ...existing fields...
	MaxPaintPixels int64           // same 0/<0 semantics; default resolved here (needs canvasPx)
	MaxOperators   int64
	Context        context.Context // nil = context.Background(); see note below
}
```

`render.Options.Context` is a field because `render.Page` is an internal
plumbing call, not a public API, and adding a parameter would churn every test
call site. The public `core` API follows Go guidance and takes `ctx` as the first
parameter, never inside an options struct. (Rejected for `core`: a
`RenderOptions.Context` field, because contexts must not be stored in structs and
`RenderOptions` is a value DTO mirrored to TS. A `RenderOptions.Timeout` field
gives no cancellation. It is only used at the WASM boundary, see §5.)

### 4.1 Error contract

- Budget trip: `render.Page` returns `nil, fmt.Errorf("%w: %s %d > %d", ErrBudgetExceeded, resource, used, limit)`,
  where resource is one of `paint work`, `operators`, `image samples`,
  `content bytes`, or `path points`.
  `core` wraps it at the boundary: `fmt.Errorf("%w: page %d: %w",
  ErrRenderBudgetExceeded, index, err)`. Go ≥ 1.20 multi-`%w`, so
  `errors.Is` matches both the core sentinel and `render.ErrBudgetExceeded`.
- Context: `render.Page` returns `context.Cause(ctx)` (wrapped), so
  `errors.Is(err, context.DeadlineExceeded)` / `context.Canceled` work. A deadline
  is **not** `ErrRenderBudgetExceeded`. A deadline depends on load and may
  succeed on retry (HTTP 503/504), while a budget trip is a property of the
  document (HTTP 422).
- Canvas cap (`deviceMatrix`, commit f71ac5a), **decided (Q4)**:
  1. `deviceMatrix` returns `fmt.Errorf("%w: canvas %gx%g …", ErrCanvasTooLarge, …)`.
  2. `core` wraps it exactly like a budget trip, so
     `errors.Is(err, core.ErrRenderBudgetExceeded)` and
     `errors.Is(err, render.ErrCanvasTooLarge)` both hold.
  3. The message still contains `exceeds`, so the existing tests in
     `core/render_test.go` and `testing/security/fuzz` keep passing unchanged.
     Add `errors.Is` assertions next to them.
- In every case: zero-value result (`nil` bytes), no partial image, no encode,
  and `OnPageRendered` does **not** fire for the failed page.

### 4.2 `RenderAllPagesContext`

This stays consistent with the canvas-too-large batch behavior: the whole batch
fails, results are `nil`, and successful pages may already have fired
`OnPageRendered`. One addition: the first failure **cancels the rest of the
batch**. The pool derives `ctx, cancel := context.WithCancelCause(parent)`, and
a page error calls `cancel(err)`. Without this, a hostile N-page document costs
N × budget. With it, the cost is about `NumCPU` × budget.

Returned error, deterministic in content:

1. If the parent ctx is done, return its cause.
2. Otherwise return the lowest-index page error that is **not** the internal
   batch cancellation (the root cause, never a misleading `context.Canceled`).

Pages still waiting on the semaphore check `ctx` before starting.

---

## 5. Where the checks go (hot-path cost)

```
render.Page(opts)
  deviceMatrix → canvasPx → resolve W/O defaults → b := newBudget(ctx, limits)
  b.chargeContent(len(content))            // before ScanContent
  run(...)  — interp holds b; raster holds the same *budget
    interpret: for each op { if b.stopped() {return}; b.step(1); apply(op) }
    execForm:  decode → b.chargeContent(len) → (stopped? return) → ScanContent → interpret
    doXObject(image): b.chargeSamples(w*h [+smask]) before decodeImage
    path build: b.checkPathPoints(n) on append
    raster.fillPath / span / line / drawImage: charge per row; return when stopped
  drawAnnotations (same raster, same budget)
  if err := b.err(); err != nil { return nil, err }   // before encode
  encode
```

- **Sticky stop.** The first trip records the error. Every paint entry point and
  every per-row loop returns early once `b.stopped()`, and `interpret` and
  `execForm` stop at the next operator. The trip point is exact: the limit is
  checked before work where the cost is known up front (image box area, image
  samples, content bytes, path points), and per row otherwise. Overshoot is
  therefore at most one row.
- **`newRaster` keeps an unlimited budget** so existing tests that build a raster
  directly are unchanged. `render.Page` installs the real one.
- **Context polling is amortized.** The budget polls when ≥ 2^20 W units
  (≈ 5 ms native, ≈ 25 ms wasm) or ≥ 4096 O steps have accumulated since the last
  poll. A poll does a non-blocking `ctx.Err()` **and** compares `time.Now()` with
  `ctx.Deadline()`, read once at construction. The explicit clock check is
  required for WASM. A JS→Go export runs synchronously on the single JS thread,
  and Go/wasm has no preemption, so the timer goroutine that would close
  `ctx.Done()` never runs while the render loop is busy. Without the clock check a
  deadline would never fire in the browser. Cancellation latency is ≤ one quantum
  plus one row.
- Expected overhead on legitimate pages: < 2% (benchmark gate, §8).

---

## 6. WASM and TypeScript surface

`wasm/main.go` `renderPage` reads three new optional fields:

| JS field | Go | Conversion |
|---|---|---|
| `maxPaintPixels?: number` | `RenderOptions.MaxPaintPixels` | `v.Float()` → int64 via a checked conversion: NaN/±Inf/out-of-range → ignored (default). Never a raw float→int cast, which is implementation-defined and saturates under js/wasm (the 7d31ef4 bug class). |
| `maxOperators?: number` | `RenderOptions.MaxOperators` | same |
| `timeoutMs?: number` | `context.WithTimeout(context.Background(), …)` → `RenderPageContext` | finite and > 0 only; otherwise no deadline |

`packages/core/src/types.ts` `RenderOptions` gains the same three optional
fields, with JSDoc stating the 0 / negative semantics. The React viewer spreads
`renderOptions` into the call and the Vue viewer keys on the JSON of
`renderOptions`, so the new fields pass through with **no wrapper change**.
React/Vue parity holds automatically.

### 6.1 Error envelope `code` (decided, Q5)

Every WASM export returns `{ error: string, code: string, …result }`.

- On success: `error === ""` and `code === ""`.
- On failure: `code` is always non-empty and comes from a closed set. `error`
  keeps today's human-readable message, `"<export>: <err.Error()>"`, so existing
  consumers do not break.

**Mapping.** One function, `errcode.Of(err error) string`, in the untagged
package `wasm/internal/errcode` (testable natively; see §8). It tests `errors.Is`
**in table order; the first match wins**. Order matters because
one error can match several sentinels: a canvas error also matches
`ErrRenderBudgetExceeded`.

| # | `code` | Matches (`errors.Is`) / origin |
|---|---|---|
| 1 | `canvas_too_large` | `render.ErrCanvasTooLarge` (checked before 2) |
| 2 | `budget_exceeded` | `core.ErrRenderBudgetExceeded` |
| 3 | `timeout` | `context.DeadlineExceeded` |
| 4 | `canceled` | `context.Canceled` |
| 5 | `invalid_pdf` | `core.ErrInvalidPDF` |
| 6 | `encrypted` | `core.ErrEncryptedDocument` |
| 7 | `page_not_found` | `core.ErrPageNotFound` |
| 8 | `unsupported_format` | `core.ErrUnsupportedFormat`, `render.ErrUnsupportedFormat` |
| 9 | `invalid_annotation` | `core.ErrInvalidAnnotation`, `core.ErrAnnotationNotFound` |
| 10 | `invalid_argument` | `core.ErrInvalidArgument`, plus the WASM boundary's own argument checks (missing page index, non-object options, …) |
| 11 | `invalid_handle` | WASM boundary: missing or unknown document handle (never opened, or already closed) |
| 12 | `internal` | the `export()` recover wrapper (a recovered Go panic). This path must set `code` too: `errResult` takes the code, so no envelope can leave without one. |
| 13 | `unknown` | any other non-nil error (e.g. an unmapped decode error). This is the fallback, not `internal`, so a real panic stays distinguishable. |

Pro, Cloud and read-only sentinels (`ErrFeatureRequiresPro`,
`ErrFeatureRequiresCloud`, `ErrReadOnly`, `ErrFormFieldNotFound`,
`ErrInvalidPageOrder`) are unreachable from today's exports. Add a code for each
only when an export can return it; new codes are additive. The Go helpers become
`errResult(code, msg string)` and `errFrom(prefix string, err error)`, where the
latter applies `errcode.Of`.

**TypeScript** (`packages/core/src/index.ts`):

```ts
export type FluxDocsErrorCode =
  | "canvas_too_large" | "budget_exceeded" | "timeout" | "canceled"
  | "invalid_pdf" | "encrypted" | "page_not_found" | "unsupported_format"
  | "invalid_annotation" | "invalid_argument" | "invalid_handle"
  | "internal" | "unknown";

export class FluxDocsError extends Error {
  readonly code: FluxDocsErrorCode;
  constructor(message: string, code: FluxDocsErrorCode = "unknown") { … }
}
```

- `unwrap` passes `res.code`. An unrecognized or missing code, for example from
  an older `.wasm` build, maps to `"unknown"`.
- Errors raised on the TS side use the same set: `check()` on a closed
  document → `invalid_handle`; "Go runtime not found" and "WASM did not register"
  → `internal`.
- The constructor's default argument keeps `new FluxDocsError(msg)` source
  compatible. Export `FluxDocsErrorCode` from `@fluxdocs/core`, and re-export it
  from `@fluxdocs/react` and `@fluxdocs/vue` next to `RenderOptions`.

**Viewer UX** (React and Vue, kept identical). The existing path stays:
`<div class="fd-viewer" role="alert">` with `.fd-viewer__error`. The text is
chosen by `code`:

| `code` | Message |
|---|---|
| `budget_exceeded` | "This page is too complex to display." |
| `canvas_too_large` | "Failed to display PDF: page is too large to render" (the viewer's own pixel-budget pre-check text; changed after review) |
| `timeout` | "Rendering this page took too long." |
| anything else | "Failed to display PDF: {message}" (today's text) |

No new DOM, classes, or `--fd-*` tokens. The a11y behavior (live
`role="alert"`) is unchanged.

**Privacy path unchanged.** No network, no filesystem; the budget is pure
computation.

Out of scope, noted: a long render still blocks the tab's main thread for up to
the budget. Running the core in a Web Worker is the real UX fix and needs a
separate design.

---

## 7. Server paths

All server render paths call `RenderPageContext` with a deadline. Defaults are
per-call-site constants, not core defaults.

| Caller | Change | Error mapping |
|---|---|---|
| `examples/go-server-demo` `/thumbnail` | `ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)`; client disconnect cancels too. Clamp `?dpi` to ≤ 300 (demo hygiene). | `ErrRenderBudgetExceeded` → 422; `context.DeadlineExceeded` → 503; `context.Canceled` → return silently. Message text only, never document content (SECURITY.md §4). |
| `mcp/tools.go` `render_page` | `context.WithTimeout(context.Background(), 10*time.Second)`. `callTool` has no ctx today (stdio, sequential); MCP `notifications/cancelled` wiring is a later follow-up. | `isError: true`, "render failed: …" (already the shape). |
| `cloud/api` | No render route exists yet (spec §7.9 `GET /v1/documents/:id/pages/:n` is planned). **Requirement for it:** `RenderPageContext(r.Context()` + server timeout`)`, batch renders via River jobs with a per-job deadline, plus the existing rate limiter. | 422 / 503 as above. |
| `cmd/fluxdocs render` (CLI) | No change: default budget, no deadline (local, user-driven). Optional `-max-paint` / `-max-ops` flags deferred. | Exit non-zero with the message. |

---

## 8. Testing strategy

Location: unit tests in `core/render/budget_test.go` and `core/render_test.go`.
Security regression and fuzz in `testing/security/fuzz/`, plus a new
`testing/security/dos/` for the slow hostile-document gates. Everything runs
under `go test -race`, and the deterministic tables also run under
`GOOS=js GOARCH=wasm` (wasm smoke in CI).

**Table-driven, budget trips** (`core/render`, small canvases, explicit limits so
tests are fast). Each case asserts `errors.Is(err, ErrBudgetExceeded)`, nil bytes,
and a message naming the resource:

1. N × full-canvas `0 0 W H re f` → W.
2. N × wide full-canvas stroke `S` → W.
3. One `f` on a path with many edges spanning all rows → W (checks that edge work
   is charged, post-§2.1).
4. N × `Do` of a large image placed at 1×1 px → S (decode charged, not box).
5. `q Q` flood > `MaxOperators` → O.
6. `Tj` with a huge string, Tr 3 (invisible) → O (glyphs counted with no paint).
7. Form with whitespace-only multi-MiB content `Do`'d repeatedly → C.
8. Path with > `maxPathPoints` points before `f` → P.
9. Freehand annotation overlay alone exceeding W → W (overlay shares the budget).

**Boundaries and semantics:** an exact-limit case passes and limit+1 fails
(counting is deterministic). `0` resolves the canvas-scaled default (assert the
resolved value for 72/150/300 DPI and the floor/ceiling). `< 0` disables (a page
that trips at default renders when unlimited). **Golden files byte-identical**
with budget on vs unlimited for the whole corpus: counting must never change
output.

**Context:** an already-canceled ctx returns `context.Canceled` before any work.
A deadline mid-render on a heavy page with an unlimited budget returns
`DeadlineExceeded` within deadline + generous CI slack. Nil-ctx safety in
`render.Options`. A wasm smoke test proves the wall-clock check fires inside a
synchronous export (§5).

**`core` level:** `errors.Is(err, core.ErrRenderBudgetExceeded)`, not
`ErrInvalidPDF` or `ErrPageNotFound`, zero-value bytes, `OnPageRendered` not
fired. `RenderAllPagesContext` with one hostile page: batch error is the budget
sentinel (not `context.Canceled`), nil results, the remaining pages are canceled
(the callback count is below the page count on a many-page fixture), and a parent
ctx cancel returns `context.Canceled`. Run with `-race`. Canvas cap (Q4): the
existing hostile-MediaBox tests also assert
`errors.Is(err, ErrRenderBudgetExceeded)` and `errors.Is(err,
render.ErrCanvasTooLarge)`, and still find `exceeds` in the message.

**Error codes (Q5):**

- A table-driven test for the code mapping, run in the native CI too. The mapping
  therefore lives in a small untagged package, `wasm/internal/errcode`, with
  `errcode.Of(err error) string`, and `wasm/main.go` calls it. It must not go in
  an untagged file of `package main` under `wasm/`: a native `go build ./...`
  would then compile a `main` package with no `main` func and fail.
  - One row per §6.1 code.
  - Precedence: a canvas error yields `canvas_too_large`, not `budget_exceeded`.
  - A wrapped `context.DeadlineExceeded` yields `timeout`.
  - An unmapped error yields `unknown`.
- A recovered panic in `export()` yields `code: "internal"`.
- TS unit tests:
  - `unwrap` copies `code`; a missing or unknown code becomes `"unknown"`.
  - `check()` on a closed document yields `invalid_handle`.
  - React and Vue viewers show the friendly message for `budget_exceeded` and
    `timeout`, and the raw message otherwise.

**Fuzz:** extend `exercise` in `testing/security/fuzz/fuzz_test.go` to render
with default budgets plus a per-input `RenderPageContext` deadline (e.g. 2 s).
The invariant is no panic and a return within deadline + slack. New seeds:
many full-canvas ops; deep and wide form fan-out; a huge image tiny-placed and
repeated; a many-edge path; a `/Contents` array repeating one stream (see §9 R1).

**Benchmarks (calibration gate in the implementation PR):** per-pixel rate native
and wasm (refresh §3 table); per-step overhead (sets the `MaxOperators` default);
`lineRowCost`; budget overhead on representative corpus pages (< 2%); `fillPath`
before and after §2.1 on a 50k-edge polygon.

---

## 9. Related findings (out of scope here, flagged)

**Tracked follow-up: R2 is not covered by the budget yet.** It runs before the
first budget check, so neither `MaxOperators` nor a deadline can stop it.
Measured by go-reviewer on the budget PR: 32 MB of `q Q\n` spends ≈ 4.75 s and
≈ 3.0 GB RSS in `ScanContent` before `MaxOperators` acts. Spec §13.2 states the
gap. R1 is resolved (2026-10-16, below).

**Being fixed separately: do not duplicate.** A concurrent fix in `core/render`
adds an unexported float→int clamping helper, `clampToInt`, in `raster.go` and
`form.go`, and fixes two conversion bugs:

- **The image `/Width` × `/Height` overflow** in `decodeImage` / `imageInt`.
  This fix validates w and h before the area product. The budget's
  `chargeSamples(w*h)` must run **after** that validation and use the validated
  values; it must not introduce its own conversion or product.
- **The `/Rotate` float→int conversion.**

The budget implementer rebases on that fix. Any new float→int conversion in the
budget code (for example the wasm option parsing in §6) reuses the same
checked-conversion approach rather than a raw cast.

- **R1 (resolved 2026-10-16): `parse.(*Resolver).PageContent` concatenation
  was unbounded.** Each stream is capped at 256 MiB, but a `/Contents` array
  may list the same stream reference many times, and the buffer grew to
  N × 256 MiB before the renderer, or `ExtractText`, got control. Measured on
  the probe (one 64 MiB Flate bomb, a 64 KB file): 4 refs ≈ 1.36 GB RSS and
  2.9 s CPU, 24 refs ≈ 5.2 GB RSS (fatal under js/wasm's 4 GiB) and 8.5 s.
  Fix: the page's content is capped at `parse.MaxPageContent` = `maxDecoded`
  (256 MiB) in total: every byte the filters output (each stage of a chain),
  an unfiltered stream's length, each repeat counted again, and one separator
  per stream. Each decode runs through `DecodeStreamLimit` against what is left
  of that one allowance, and a stream listed again is decoded once and its
  bytes counted again (ISO 32000-1 §7.7.3.3 concatenates the elements; the
  output is identical). Past the cap `PageContent` returns an error wrapping
  `errLex` and `ErrDecodeLimit`. Why 256 MiB and not `maxContentBytes`: it is
  the plan this note recorded, a page's content then totals no more than its
  largest permitted single stream, it leaves at least 256 MiB of the 512 MiB
  content counter for forms, and it halves what `ScanContent` (R2) can be handed.
  The largest page content in the corpus and the no-regression set is 57 KB.
  Callers:
  - `render.Page` calls `PageContentLimit` with what is left of C, charges
    the returned count once (no second charge), and maps the limit error to a
    `content bytes` trip, so `RenderPage` returns `ErrRenderBudgetExceeded`.
  - `ExtractText`, `ExtractTextByPage` and `ExtractTables` return the
    `parse.ErrDecodeLimit` error.
  - `RedactText` and `AutoRedactPII` fail and mark the plan failed, and
    `RedactAndFlatten` refuses: redaction fails closed.
  - Merge, split and save copy content streams undecoded and are unaffected.
  After the fix the probe fails in ≈ 0.2–0.4 s at ≈ 210 MB RSS for 4, 12 and 24
  refs (one 64 MiB inflate). Gates: `TestDoS_RepeatedContentsBombIsCapped`,
  `TestRedact_OversizedContentFailsClosed`, and the fuzz seed "Flate bomb
  repeated in /Contents".
- **R2: `parse.ScanContent` materializes the whole operator list up front.** A
  256 MiB stream of tiny operators allocates far more than 256 MiB of
  `ContentOp`/operand slices before the O budget is consulted. Options: a
  limit-aware scan (`ScanContentLimit(data, maxOps) ([]ContentOp, bool)`), or an
  iterator the interpreter pulls from. This affects extraction too. Needs its own
  design.
- **R3: per-render decode caches.** Image and form content are re-decoded on
  every `Do`. That work is bounded: form content by C, image samples by S, and
  every byte an image, mask, soft-mask or font decode produces beyond its
  samples by D (before the review fix a 1×1 image whose Flate body inflated to
  64 MiB was charged one sample per `Do`). Caching by `parse.Reference` within
  one render would make legitimate repeated stamps cheap. This is a performance
  follow-up.

---

## 10. Decisions (maintainer-approved, 2026-10-15)

Settled. Implement as written; do not re-open without the maintainer.

- **Q1. Field names and types:** flat `int64` fields `MaxPaintPixels` and
  `MaxOperators` in `RenderOptions`. Rejected:
  - `MaxRasterWork`;
  - a nested `RenderBudget` struct;
  - `int`, which is 32-bit on some native targets.
- **Q2. Disable semantics:** `< 0` = unlimited, documented on both fields as
  unsafe for untrusted input. **No** exported constant.
- **Q3. Context entry points:** `RenderPageContext(ctx, index, opts)` and
  `RenderAllPagesContext(ctx, opts)`. `RenderPage` and `RenderAllPages` keep
  their signatures and delegate with `context.Background()`. A `Context` field
  exists only on the internal `render.Options`.
- **Q4. Canvas cap:** the canvas-too-large error also wraps
  `ErrRenderBudgetExceeded`, through `render.ErrCanvasTooLarge` (§4.1). The word
  `exceeds` stays in the message.
- **Q5. WASM error codes:** a `code` field in the WASM error envelope, exposed as
  `FluxDocsError.code` in TS. The closed code set, its precedence, and the
  recover-wrapper `internal` path are defined in §6.1.

Spec updated to match: §6.1 (`RenderOptions`), §7.2 (two methods and the render
contract), §10.2 (`ErrRenderBudgetExceeded`), §13.2 (render budget), and §13.3
(`RenderAllPagesContext` with batch cancellation).

**Resolved recommendations** (accepted with the design; listed for traceability):

- **R-a.** One budget per `RenderPage` call, covering content and the annotation
  overlay. There is no per-document or per-batch budget; batches are bounded by
  the ctx deadline plus §4.2 early cancellation.
- **R-b.** Same defaults on native and WASM (deterministic parity). WASM gets
  `timeoutMs` for a wall-clock bound, and the viewer sets **no** default timeout.
- **R-c.** Image samples, content bytes, and path points are internal caps in
  this increment. Expose them only if a real caller needs to tune them.
- **R-d.** A deadline is not a budget error (§4.1).
- **R-e.** `fillPath` active-edge rewrite (§2.1) ships with the budget, not
  after.
- **R-f.** `maxFormDepth`/`maxFormBudget` and per-image skips stay silent.
  Only cumulative budgets error.

**Settled by benchmark in the implementation PR (2026-10-15):** these numbers
supersede the estimates in §2 (`lineRowCost` ≈ 8) and §3 (per-step "not measured
yet"). They come from `BenchmarkCalibration` in `core/render/budget_test.go`, on a
6-core/12-thread x86_64 Mac under heavy unrelated load, `-cpu=1`. Each ratio is to
a pixel visit measured in the same run, which cancels most of the load.

| Measure | Native | js/wasm (Node 24) | Ratio to a pixel visit |
|---|---|---|---|
| Pixel visit (wide span), the W unit | ≈ 11.6 ns (go-reviewer's quieter host: 4.75) | ≈ 33 ns | 1 |
| Stroke row (thin, 8192-row segment) | ≈ 630 ns | ≈ 1540 ns | **≈ 54 native, ≈ 47 wasm** |
| Step: `q`/`Q` | ≈ 21 ns | ≈ 75 ns | ≈ 2 |
| Step: glyph advanced, `Tr 3` (the costliest step that paints nothing) | ≈ 87 ns | ≈ 270 ns | ≈ 8 |
| Visible glyph, tiny (outline load + flatten + fill setup), per charged unit | ≈ 11–12 ns | ≈ 32–36 ns | ≈ 1.0 with `glyphPointCost` = 8 |
| `ScanContent`, per operator (runs before the budget, R2) | ≈ 370–410 ns | ≈ 1.2 µs | — |

- **`lineRowCost` = 48** (was ≈ 8). The per-row bisection in `line` costs about
  50 pixel visits. Consequence: thin strokes are the costliest raster work per
  row, so a dense vector/CAD page reaches the default at a lower operator count
  than the §2 estimate implied. A faster thin-line path in `line` would let this
  constant drop; that is a follow-up.
- **`MaxOperators` default stays 8 Mi.** Worst case without painting is
  8 Mi × 87 ns ≈ 0.73 s native (≈ 0.3 s at 4.75 ns/px host speed) and ≈ 2.3 s
  wasm.
- **Added `glyphPointCost` = 8** (W units per point of a visible glyph's
  flattened outline, charged in `drawGlyph`). Without it, the outline load and
  flatten work went uncharged and a visible glyph cost ≈ 10× its charge.
- **`line` charges 1 per call**, so segments clipped away entirely still pay
  their setup.
- **Image and form decodes are poll points.** `chargeSamples`/`chargeContent`
  also poll `ctx`, because one decode can outweigh a whole poll quantum.
- **Overhead: < 1 %.** On 92 representative files (up to 4 pages each) at
  150 DPI, retired instructions are within 0.4 % of a build whose hot-path
  charges are no-ops; cycles agree within noise. Against the pre-budget tree the total is −7 %
  instructions and ≈ −17 % cycles, mostly from the active-edge `fillPath`.
- **`fillPath` §2.1:** on the 50 k-vertex polygon over 1650 rows, the active-edge
  fill takes ≈ 3.5 ms against ≈ 1.5–1.9 s for the full scan. Edges are bucketed by
  their entry row with a counting sort, so there is no comparison sort. Output is
  byte-identical: 0 PNG hash differences over 477 page renders (468 files:
  `testing/corpus` plus macOS system PDFs, up to 8 pages each) at 72 and 150 DPI,
  with the budget on and off. No corpus page trips the default budget.
- Wasm deadline check: `timeoutMs: 200` on an unbounded page returns
  `code: "timeout"` after ≈ 270 ms inside a synchronous export.

**Recalibrated after review (2026-10-15).** go-reviewer found paint work that
cost far more than its charge; these supersede the matching numbers above.
`BenchmarkCalibration` now times each paint benchmark in process CPU time,
interleaved with a pixel-visit reference, and reports `px/unit` (CPU time per
charged unit over a pixel visit), so a loaded host skews the ratio less.
Native, `-cpu=1`; every `px/unit` is ≈ 0.9–1.15.

| Constant | Was | Now | What it covers |
|---|---|---|---|
| `fillRowCost(k)` | k + 1 | 1 + k·(2 + bits.Len(k)) | a fill row: filter + crossing per edge (`fillCrossingCost` = 2) and the O(k log k) sort. A 65,536-vertex zigzag (every edge on every row) ran ≈ 9 s on a Letter page at 150 DPI; now it stops in about the time of the plain fill flood. |
| `fillPointCost` | 1 | 4 | fill setup per path point (edge, entry row, ordering) |
| `lineCallCost` | 1 | 16 | per stroke segment, including segments clipped away |
| `lineRowCost` | 48 | 14 | per stroke row; the row's sample range is now searched from the previous row's result (`firstTrueNear`, galloping), not bisected from scratch. Output is bit-identical (kept as a test oracle). |
| `imagePixelCost` | 1 | 2 | per pixel of an image placement's device box |
| `glyphPointCost` | 8 | 6 | per visible-glyph outline point, on top of `fillPointCost` (total 10, was 9) |

- Effect on dense vector pages: 100k short hairline segments on a Letter page
  at 150 DPI cost ≈ 0.55× the default (they tripped it before); 1M cost ≈ 5.5×
  and need a higher `MaxPaintPixels`.
- Fill scratch memory is ≈ 20 bytes per path point, each buffer sized once
  (edges are 8-byte references into the path, not copies). A fill at the
  `maxPathPoints` cap (262,140 `c`) peaked at ≈ 1.07 GB RSS with the first
  implementation and ≈ 0.38–0.41 GB now, against ≈ 0.36 GB before the budget.
- Output unchanged: 0 PNG hash differences against the pre-budget tree over the
  same corpus, at 72 and 150 DPI, with the budget on and off.

**Second review round (2026-10-16).**

- **Decoded bytes (D).** Each image, mask, soft-mask and font-program decode
  goes through `budget.decode`, which calls `parse.DecodeStreamLimit` with the
  bytes the image's samples already pay for plus what is left of
  `maxDecodedBytes` = 512 MiB, and charges every other byte the filters produce
  (a bomb's excess, a chain's intermediate stage, an inflate that fails
  part-way). Form content uses the same capped decode against C. A JPEG's
  compressed bytes beyond its frame's raw size are charged to D. Before: a 1×1
  image inflating 64 MiB, `Do`'d 1000 times, ran for minutes and rendered;
  now it stops at 512 MiB of inflate (≈ 1 s CPU).
- **`doCost` = 96** paint units per `Do`, charged before the XObject is
  resolved (`BenchmarkCalibration/do-tiny-image`, `px/unit` ≈ 0.97). A `Do`
  of a 1×1 image measured ≈ 0.5–1.3 µs against one interpreter step.
- **`maxSaveDepth` = 1024** (q nesting; ISO 32000-1 Annex C suggests 28).
  Past it a `q` is counted instead of saved and its matching `Q` is swallowed:
  balanced deep nests still pair up, but state set inside an ignored pair is not
  undone (lenient; real files stay far below the cap). A form's own `Q`s cannot
  consume the caller's ignored `q`s. Before: 8 Mi `q` saved 8 Mi graphics
  states (≈ 3 GB natively, fatal under js/wasm).

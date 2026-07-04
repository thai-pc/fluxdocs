# Design: Reorder / Rotate (Wave 1)

Status: design (docs only). Tier: **Core (MIT)**. Package: `core`
(`document.go`), plus one small sentinel addition in `core/errors.go`.
Spec basis: ISO 32000-1:2008 §7.7.3.3 (page objects, Table 30 — `/Rotate` is an
inheritable page attribute; its value "shall be a multiple of 90"), §7.7.3.4
(inheritance of page attributes), §7.7.3.1 (the page tree / `/Kids` order defines
document page order). Clean-room — derived from the public ISO spec only.

This note tells the implementer exactly what to build. It contains **no Go
code**. It follows the writer/copy machinery landed in
`pdf-writer-merge-split.md`; read that first. Hand off to `go-implementer`.

---

## 1. Scope

Implement the two stubbed page-organization methods on `Document`:

- `Rotate(pageIndex int, degrees int) error`
- `Reorder(newOrder []int) error`

Both **mutate the receiver in place** (unlike `Merge`/`Split`, which return new
documents). They change page *geometry/order metadata only*; they do not touch
page content streams. The change must be visible to a later `RenderPage`,
`ExtractText`, and — critically — must persist through a later `SaveTo`/`Save`.

In scope:

- In-memory permutation of the page arrays (`Reorder`).
- In-memory, additive, normalized rotation of one page (`Rotate`), written so it
  survives serialization.
- Full up-front validation; on any bad input, return an error and leave the
  document **completely unmodified** (no partial mutation).
- One new sentinel error for a malformed permutation (see §7).

Out of scope (defer, degrade to an error, never panic):

- Re-serializing / re-opening the document to apply the change (see §2 — we
  reject this "rebuild" strategy).
- Rotating a *range* of pages, or reordering + rotating in one call.
- Content-stream transforms (baking rotation into the page matrix). PDF `/Rotate`
  is a viewer-side display attribute (§7.7.3.3); Wave 1 sets it, it does not
  re-draw pages.

---

## 2. Strategy: in-memory (recommended) vs rebuild

Two ways to apply the change:

**(a) In-memory** — mutate the live handle's parallel arrays (`d.Pages`,
`d.pageInfos`) and the derived scalar fields. No serialize/re-open. Both consumers
of the change already read `d.pageInfos[i]` as their single source of truth:
`RenderPage` → `render.Page(resolver, pageInfos[i], …)`, and `SaveTo` →
`BuildDocument(sources from pageInfos)` → `CopyPage(pi)`. So an edit to
`pageInfos` flows through to *both* render and save with no extra plumbing.

**(b) Rebuild** — hand a reordered / rotation-patched `[]parse.PageSource` to
`BuildDocument`, `OpenBytes` the result, and swap its internals into `d`
(consistent with `Merge`/`Split`).

**Recommendation: (a) in-memory.** Rebuild re-serializes and re-parses the entire
object closure on every rotate-one-page — needlessly expensive for a metadata
edit — and it would silently discard in-flight state that lives only on the live
handle (the annotation layer, callbacks, `d.ID`). Reorder/Rotate are conceptually
mutations of the open document, so mutating the handle is the correct model. The
one subtlety rebuild would sidestep — persisting `/Rotate` through save — is fully
resolved for the in-memory path in §3, so (a) has no correctness disadvantage.

Because `Merge`/`Split`/`Save` continue to rebuild from `d.pageInfos` at call
time, an in-memory Reorder/Rotate composes correctly with them: a later `SaveTo`
serializes the mutated arrays.

---

## 3. Rotate

### 3.1 Semantics: relative (additive)

The method name is "rotate **by**", and the stub doc says "by a multiple of 90
degrees (… or negative)". Treat `degrees` as **relative**: the new rotation is
`currentRotation + degrees`, then normalized. `Rotate(i, 90)` twice yields 180;
`Rotate(i, -90)` backs a page off by a quarter turn. This is the natural reading
and composes intuitively. (An absolute setter can be added later as a distinct
method if a caller needs one — do not overload `degrees` with both meanings.)

The "current rotation" is `d.Pages[pageIndex].Rotation`, which equals
`d.pageInfos[pageIndex].Rotate` (both seeded from the inherited `/Rotate` at open,
open.go:71). Use it as the base.

### 3.2 Validation

In order, before any mutation:

1. `pageIndex < 0 || pageIndex >= len(d.Pages)` → `ErrPageNotFound` (already in
   the stub; keep it).
2. `degrees` must be a multiple of 90 (`degrees % 90 == 0`). ISO 32000-1
   §7.7.3.3 requires `/Rotate` to be a multiple of 90; a non-multiple input is a
   caller error, not document data to snap. **Recommended: reject** a
   non-multiple with a sentinel error (see §7 — reuse `ErrPageNotFound`? no; this
   is not a page-range problem). Do **not** silently snap (e.g. 45→0 via a
   flooring normalizer): a silent no-op hides caller bugs. See §7 for the sentinel
   choice, which is the one open API question here.

Negative and large magnitudes are legal input (they are just relative turns) and
must not error — only the not-a-multiple-of-90 case is rejected.

### 3.3 Normalization

Compute `normalized = normalize(current + degrees)` mapping to exactly one of
{0, 90, 180, 270}: reduce mod 360, add 360 if negative. `parse.normalizeRotate`
already does this but is **unexported**, and it additionally *floors* to a quarter
turn (lenient handling of malformed input on the read path). Core needs its own
tiny normalizer. Because Rotate rejects non-multiples up front (§3.2), inputs are
always exact multiples, so the flooring behavior is irrelevant here; a plain
`((x % 360) + 360) % 360` is sufficient and clearer. Do **not** export and reuse
the parse helper (its flooring is a read-path leniency we do not want to couple
to). Add an unexported `normalizeDegrees(int) int` in `core`.

### 3.4 The persistence subtlety (critical) — the exact state-update recipe

Setting only `d.pageInfos[pageIndex].Rotate` makes **render** correct
(`render.Page` reads `pi.Rotate` directly) but can make **save wrong**.
`CopyPage` (copy.go:96) only materializes the inherited `pi.Rotate` into the
output when the page's own dict does **not** already carry `/Rotate`:

> `if _, has := page["Rotate"]; !has && pi.Rotate != 0 { page["Rotate"] = … }`

So if the source page's *own* dict already has a `/Rotate` entry, `CopyPage`
keeps that original value and ignores our updated `pi.Rotate` — the saved page
reverts to its pre-rotation angle. To make render **and** save agree, Rotate must
update three places so they stay mutually consistent:

1. `d.pageInfos[pageIndex].Rotate = normalized` — the field `render.Page` reads,
   and the fallback `CopyPage` uses when the own dict has no `/Rotate`.
2. `d.pageInfos[pageIndex].Dict["Rotate"] = Integer(normalized)` — the page's
   **own** dict entry, which `CopyPage` copies verbatim and prefers over the
   inherited value. Writing it makes the saved page authoritative regardless of
   the `!has` branch, and normalizes any pre-existing own `/Rotate`. Use
   `parse.Integer` (a normalized angle is always a whole number).
3. `d.Pages[pageIndex].Rotation = normalized` — the exported geometry field
   callers read.

Writing all three keeps the three views (public `Page`, `PageInfo` for
render/save fallback, own-dict for save) identical, so no downstream reader can
observe a stale angle.

### 3.5 Do NOT mutate the resolver's cached dict — shallow-clone first

`PageInfo.Dict` is **not** a private copy: it is the very `Dict` returned by
`Resolver.ResolveDict` during page-tree walking, which the resolver **caches** by
object number (`resolver.go`: `r.cache[num]`). Assigning
`d.pageInfos[i].Dict["Rotate"] = …` would mutate that shared cached map in place,
corrupting parser state for any later `Resolve` of the same page object (and
racing a concurrent render that resolves it). That is a latent, hard-to-find bug.

Fix: **shallow-clone the page's own dict before writing to it.** Build a new
`parse.Dict`, copy every top-level key/value from `d.pageInfos[pageIndex].Dict`
into it, set `"Rotate"` on the clone, and replace
`d.pageInfos[pageIndex].Dict` with the clone. A *shallow* copy is sufficient:
`CopyPage`/`copyValue` only read top-level keys (`Rotate`, `MediaBox`,
`Resources`) and then walk references read-only; nested values are never mutated,
so they may be shared. This detaches our edit from the resolver cache without a
deep copy. Add an unexported `cloneDictShallow(parse.Dict) parse.Dict` helper in
`core` (or place it in `parse` if a shallow-clone primitive is wanted there — a
`core`-local helper keeps the tier boundary simplest and needs no new export).

### 3.6 Idempotence / no-op

`Rotate(i, 0)` and any `degrees` whose normalized result equals the current angle
still perform the full three-way write (harmless, and it *normalizes* a
pre-existing odd own `/Rotate` such as 360 or -90 in the source). Do not special-
case it away; correctness over micro-optimization.

---

## 4. Reorder

### 4.1 Semantics

`newOrder` is a permutation of `[0, PageCount)`: `newOrder[k]` is the *old* index
of the page that shall occupy position `k` after the call. So new position `k`
receives `d.Pages[newOrder[k]]`. `Reorder([]int{2,0,1})` moves old page 2 to the
front.

### 4.2 Validation (full, before any mutation)

Reject, in order:

1. `len(newOrder) != d.PageCount` (equivalently `!= len(d.Pages)`) → malformed
   permutation → new sentinel (§7).
2. Any `newOrder[k] < 0 || >= d.PageCount` → an index referencing a non-existent
   page → `ErrPageNotFound` (consistent with how `Split` treats an out-of-range
   page: "a range referencing a non-existent page returns ErrPageNotFound").
3. Any duplicate index (build a `seen` set of the `PageCount` slots; a repeat, or
   equivalently any missing index once the length and range checks pass) →
   malformed permutation → new sentinel (§7).

Only after all three pass does mutation begin. An empty document
(`PageCount == 0`) with an empty `newOrder` is a valid no-op (length matches,
nothing to move) — return nil.

### 4.3 Apply

Build **new** slices (do not permute in place with swaps — a validated
permutation still makes fresh slices simplest and keeps the operation atomic):

- `newPages := make([]Page, n)`; `newInfos := make([]parse.PageInfo, n)`.
- For each new position `k`: `newPages[k] = d.Pages[newOrder[k]]`;
  `newInfos[k] = d.pageInfos[newOrder[k]]`.
- **Reset `newPages[k].Index = k`** so `Page.Index` again equals slice position
  (its documented invariant, types.go:76). Do **not** touch `Page.ID` (see §5).
- Assign `d.Pages = newPages`, `d.pageInfos = newInfos`. `d.PageCount` is
  unchanged (a permutation preserves count).

`d.Pages[k]` and `d.pageInfos[k]` must remain index-parallel after the swap —
build them in the same loop so they cannot diverge.

---

## 5. PageID stability & annotations (both operations)

`Page.ID` is assigned once at open (`page-<originalIndex>`, open.go:67) and the
annotation layer keys every annotation by `PageID` (`Annotation.PageID`, resolved
via `hasPage`, annotation.go:364). For annotations to keep pointing at the right
page across a page-organization edit, **the ID must travel with its page**:

- **Reorder** moves the whole `Page` value (ID included) to its new slot; it
  only rewrites `Page.Index`, never `Page.ID`. So a page keeps its identity as it
  moves, and every annotation still resolves to the same physical page.
  Consequence to state plainly: after a reorder, `Page.ID`'s numeric suffix (from
  the *original* position) no longer matches `Page.Index`. That is intended — the
  suffix is an opaque identity token, not a position. **Never recompute `Page.ID`
  from the new index**; doing so would silently reassign annotations to different
  pages.
- **Rotate** does not move any page, so all IDs and the whole annotation layer are
  untouched.

No change to `annotation.go` is required. The annotation layer is deliberately
decoupled from page order.

---

## 6. Concurrency & consistency

Unlike the annotation methods, these mutate `d.Pages`/`d.pageInfos`, which
`RenderPage`/`ExtractTextByPage` read **without** taking `d.mu` (open.go builds
them once and the codebase has so far treated them as immutable after open — see
`hasPage`'s "d.Pages is fixed at open time" comment, annotation.go:363).

Requirements:

1. Take `d.mu` for the whole validate-then-mutate critical section in both
   methods, so two concurrent Reorder/Rotate calls serialize and can never
   interleave into a torn state.
2. **Validate fully before mutating** (§3.2, §4.2). On any error, return before
   writing anything — the document is left byte-for-byte as it was.
3. Keep the four views mutually consistent at all times: `d.Pages`,
   `d.pageInfos`, `d.PageCount`, and each `Page.Index`. Reorder rebuilds
   `Pages`/`pageInfos` together and resets `Index`; Rotate touches only the one
   page's three rotation views.

**Known limitation to document (not fix in Wave 1):** because `RenderPage` reads
`d.pageInfos` without `d.mu`, calling `Reorder`/`Rotate` *concurrently with an
in-flight `RenderPage`/`RenderAllPages`* is a data race. Wave 1 stance: these
mutating operations are not safe to interleave with concurrent renders on the
same `*Document`; a caller must quiesce renders first. State this in the method
doc comments. (A later increment can make the render read-path take `d.mu` or
snapshot `pageInfos` under the lock; out of scope here — flag as open question
O2.)

---

## 7. New sentinel error (the one API decision)

There is no existing sentinel for "not a valid permutation" or "not a multiple of
90". Analysis of the options:

- `ErrPageNotFound` fits an out-of-range **index** (and §4.2 step 2 reuses it for
  exactly that), but it does not describe a wrong-length or duplicate permutation,
  nor a bad rotation angle.
- `ErrInvalidPDF` is about damaged file structure, not a bad caller argument —
  wrong meaning.

**Recommendation:** add **one** new sentinel in `core/errors.go`:

```
ErrInvalidPageOrder = errors.New("fluxdocs: invalid page order")
```

Use it for:
- Reorder: wrong length, or a duplicate/missing index (the non-range permutation
  failures). Out-of-range indices stay `ErrPageNotFound` for Split-consistency.
- Rotate: a `degrees` that is not a multiple of 90. The message is a mild stretch
  for the angle case, but a single new sentinel keeps the surface small and both
  are "invalid page-organization argument". Alternative: a second sentinel
  `ErrInvalidRotation` dedicated to the angle. Two sentinels are more precise but
  add API surface for a rarely-hit programmer-error path.

This is the open question that needs a maintainer sign-off (see below): **the
sentinel name(s)** and whether Rotate shares `ErrInvalidPageOrder` or gets its own
`ErrInvalidRotation`.

---

## 8. Helpers to add (all unexported, `core`)

- `normalizeDegrees(x int) int` → `((x % 360) + 360) % 360`, result in
  {0,90,180,270} for multiple-of-90 input. §3.3.
- `cloneDictShallow(d parse.Dict) parse.Dict` → top-level key/value copy, so
  Rotate can write `/Rotate` without mutating the resolver-cached page dict. §3.5.
- (Optional) a small `isPermutation(newOrder []int, n int) (ok bool, outOfRange
  bool)` to keep Reorder's validation readable and let the caller pick the right
  error per §4.2.

No new exported API, no new package, no CGO, no build tag — this stays 100%
MIT/Core and is WASM-safe (pure in-memory, no I/O, no net).

---

## 9. Test plan

Table-driven `core` tests (`document_test.go` or a new `reorder_rotate_test.go`):

**Rotate**
- Relative accumulation: `Rotate(0,90)` then `Rotate(0,90)` → `Pages[0].Rotation
  == 180`.
- Negative: from 0, `Rotate(0,-90)` → 270.
- Wrap: from 270, `Rotate(0,90)` → 0.
- Out-of-range index → `ErrPageNotFound`; document unchanged.
- Non-multiple of 90 (e.g. 45) → the §7 sentinel; document unchanged.
- **Persistence golden**: open a fixture whose page already carries an own
  `/Rotate` (e.g. 90), `Rotate(0,90)`, `SaveTo` a buffer, re-`OpenBytes`, assert
  the reopened `Pages[0].Rotation == 180`. This is the regression test for the
  §3.4 subtlety — it fails if only `pi.Rotate` (not the own-dict entry) is
  updated. Also cover a page with **no** own `/Rotate` (inherited only) to prove
  both `CopyPage` branches.
- **Cache-safety**: after `Rotate`, resolve the page object again via the
  resolver (or render another page) and assert the original cached dict was not
  mutated — i.e. the shallow clone actually detached. (If a direct resolver probe
  is awkward from `core`, assert indirectly: a second independent `SaveTo` of a
  *sibling* untouched page still emits its original rotation.)

**Reorder**
- Valid permutation reverses/rotates order; assert `Pages[k].Index == k` for all
  k, and `pageInfos` reordered in lockstep (e.g. via `ExtractTextByPage` content
  moving with the page).
- Identity permutation → no-op, no error.
- Empty document + empty `newOrder` → nil.
- Errors, each asserting the document is untouched: wrong length → sentinel;
  out-of-range index → `ErrPageNotFound`; duplicate index → sentinel.
- **Annotation linkage**: add an annotation to the page whose ID is `page-0`,
  `Reorder` so that page moves to the back, then `GetAnnotations("page-0")` still
  returns it and it resolves to the moved page (ID stability, §5).
- **Round-trip**: `Reorder` then `SaveTo` → re-open → page order matches the new
  order (content per page verified via `ExtractTextByPage`).

No golden PNGs are strictly required (this is metadata, not pixels), but a
render-after-rotate smoke test that the call path does not error is cheap
insurance.

---

## 10. Open questions for the maintainer

- **O1 (API):** Sentinel naming. Approve `ErrInvalidPageOrder` for the malformed-
  permutation and non-multiple-of-90 cases, or split rotation into a dedicated
  `ErrInvalidRotation`? (§7). This is the only public-API change in the increment.
- **O2 (concurrency):** Accept the Wave-1 limitation that Reorder/Rotate are not
  safe to interleave with concurrent renders (documented, not enforced), or
  require the render read-path to lock/snapshot `pageInfos` under `d.mu` now?
  (§6). Locking the hot render path has a cost; deferring keeps parity with the
  current "immutable after open" assumption.
- **O3 (Rotate strictness):** Confirm reject-on-non-multiple-of-90 over a lenient
  snap. Recommendation is reject (§3.2); a maintainer who prefers maximum
  leniency on all inputs could ask for the floor-snap instead — but that hides
  caller bugs and is inconsistent with "every bad input → error".

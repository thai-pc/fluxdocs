# Design: Annotation layer — data model + JSON export/import (Wave 1)

Status: design (docs only). Tier: **Core (MIT)**. Package: `core`.
Spec basis: `docs/spec.md` §6.2 (annotation layer stored independently of the
PDF), §7.3 (annotation API), §7.7 (event callbacks), Appendix A (JSON schema).
Product rule: `.claude/CLAUDE.md` §1.4 / §9 — annotations live in a **separate
JSON layer**, never baked into the source PDF except on an explicit `Flatten*`.

This note tells the implementer exactly what to build. It contains no Go code.
Hand off to `go-implementer`.

---

## 1. Scope

Build the **in-memory annotation data layer** on `Document` plus its **JSON
export/import**. Concretely, implement the seven stubbed methods in
`core/document.go` §7.3:

- `AddAnnotation(a Annotation) (Annotation, error)`
- `UpdateAnnotation(id AnnotationID, patch AnnotationPatch) (Annotation, error)`
- `RemoveAnnotation(id AnnotationID) error`
- `GetAnnotations(pageID PageID) ([]Annotation, error)`
- `ExportAnnotationLayer() (*AnnotationLayer, error)`
- `ImportAnnotationLayer(layer AnnotationLayer) error`

and wire the existing `OnAnnotationChange(fn)` callback (already implemented as a
setter in `document.go`) so it fires on add/update/remove.

**In scope this increment:**

- The four Wave-1 annotation types for create/update: `highlight`, `note`,
  `draw`, `shape` (spec §8 roadmap; `AnnotationType` values in `types.go`).
- One implicit **current layer** per open `Document`.
- JSON serialization contract (struct tags on `types.go`) + round-trip-stable
  export/import.

**Explicitly out of scope (leave the stubs returning `errNotImplemented`, and
say so in comments):**

- `FlattenAnnotations()` — baking annotations into the content stream is a
  separate render increment. This increment is the **data layer only**; nothing
  is drawn onto a page here.
- Rendering/compositing annotations over the canvas (a `core/render` follow-up).
- The `stamp`, `signature`, and `redact` types as **editable** annotations.
  `redact`/`signature` are Pro-tier (§2) with their own security gates;
  `stamp` is deferred. See §7 for how import preserves them as inert data.
- Multi-layer management (multiple named layers open at once, layer visibility
  toggling as a first-class API). Wave 1 has exactly one current layer.

---

## 2. In-memory model on `Document`

Add unexported fields to the `Document` struct (`types.go`), grouped with the
existing runtime state (`mu`, `closed`, `onAnnotationChange`, …):

- `annotations []Annotation` — the ordered source of truth. Insertion order is
  preserved because export/round-trip stability (§6) depends on a deterministic
  order, and `GetAnnotations` should return page annotations in creation order.
- `annotationSeq uint64` — monotonic counter used to mint IDs (§3).
- Current-layer identity, initialized lazily on first use:
  - `layerID LayerID`
  - `layerName string` (default empty; the spec example uses "Legal team's
    review" — the name is caller-supplied via import, or set to a default)
  - `layerVisible bool` (default `true`)
  - `layerCreatedAt time.Time`

**Why a slice, not a map keyed by `AnnotationID`.** Wave-1 documents carry tens,
not thousands, of annotations; a linear scan for lookup/update/remove is
negligible and keeps ordering trivial and copy-free. A `map[AnnotationID]int`
position index is a valid later optimization but is invalidated on every remove
(needs reindexing) — not worth the complexity now. **Recommendation:** slice
only for Wave 1; revisit if profiling shows it matters.

**Per-page grouping** (`GetAnnotations(pageID)`) is a filter over the slice by
`Annotation.PageID`, preserving order. No separate per-page index is needed.

**The "current layer" concept.** Wave 1 models a single active layer per open
`Document`. `AddAnnotation` appends "to the current layer" = appends to
`d.annotations` and stamps nothing layer-specific onto the annotation itself (an
`Annotation` has no `LayerID` field, by design). `ExportAnnotationLayer` wraps
the whole `d.annotations` slice into one `AnnotationLayer` using the current-layer
identity fields. This keeps the model simple while matching the §6.2 type.

---

## 3. ID and timestamp assignment

### 3.1 AnnotationID scheme

`AddAnnotation` mints the ID; a caller-supplied `a.ID` is **ignored and
overwritten** (documented in the method comment). Proposed scheme, matching the
existing `PageID` convention (`page-%d`, see `open.go`):

- `ann-<n>` where `n` is `annotationSeq`, incremented before use, formatted in
  decimal. First annotation → `ann-1`.

**Trade-off (flag for maintainer, see O1).** A per-document counter is
**deterministic and test-friendly** (golden files and table tests can assert
exact IDs; no randomness, no clock dependence). Its weakness is that IDs are
**only unique within one open Document**: two documents both start at `ann-1`, so
IDs collide across documents and across `Merge` / multi-user sync. For Wave 1
(single-document, single-user editing) this is fine. When merge/collaboration
arrives (Wave 3), switch to a collision-free scheme (ULID or 16-byte
`crypto/rand` hex, e.g. `ann-<ulid>`, matching the `ann-01ARZ3…` form in the spec
Appendix A). Import already handles cross-source collisions by re-keying (§6.3),
so the counter is safe today.

`annotationSeq` is also advanced past any IDs seen during import (§6.3) so
subsequently-added IDs never collide with imported ones.

### 3.2 Timestamps

- `AddAnnotation` sets `CreatedAt = UpdatedAt = now`, where `now` is
  `time.Now().UTC()`. Use UTC so JSON `RFC3339` output is stable across hosts.
- `UpdateAnnotation` sets `UpdatedAt = now` and leaves `CreatedAt` untouched.
- `ImportAnnotationLayer` **preserves** the `CreatedAt`/`UpdatedAt` carried in the
  layer (does not restamp them) — required for round-trip stability (§6.4).

Recommendation: consider truncating to whole seconds or to `time.Millisecond` on
mint so a value re-marshals identically; RFC3339 preserves sub-second digits, so
an untruncated `time.Now()` round-trips fine either way. Not load-bearing —
implementer's choice; note it in the comment.

---

## 4. CRUD semantics and validation

All three mutators and `GetAnnotations` validate at the boundary and fail fast
(CONVENTIONS §2), returning the zero value + error on failure (CONVENTIONS §3.1).

### 4.1 `AddAnnotation(a)` validation, in order

1. **Type** must be one of the four Wave-1 types (`highlight`, `note`, `draw`,
   `shape`). Any other value (including `stamp`/`redact`/`signature` and unknown
   strings) → `ErrInvalidAnnotation` (new sentinel, §8). Rationale: `Add` is the
   editing entry point; deferred/Pro types must not be creatable through Core.
2. **Page** — `a.PageID` must match some `d.Pages[i].ID`. Otherwise
   `ErrPageNotFound` (reuse the existing sentinel; it already means "no such
   page").
3. **Rect** — `Width >= 0` and `Height >= 0`, and all four fields finite (reject
   `NaN`/`Inf`; untrusted numeric input, CONVENTIONS §2). Zero-area rects are
   allowed for `note`/`draw` (a note is a point marker; a freehand stroke lives
   in `Points`). Negative width/height → `ErrInvalidAnnotation`.
4. **Opacity** — clamp to `[0,1]`; treat the zero value as "unset" and default to
   `1.0`. (An amber default color `#f59e0b` per §5 of CLAUDE.md may be applied
   when `Color` is empty — implementer's choice; flag if you skip it.)

On success: assign ID (§3.1) and timestamps (§3.2), deep-copy caller-owned
reference fields (§5), append to `d.annotations`, and return the **stored copy**
(also deep-copied on the way out).

### 4.2 `UpdateAnnotation(id, patch)`

- Find by `id`; miss → `ErrAnnotationNotFound`.
- Apply `AnnotationPatch` — a **nil pointer field means "unchanged"** (the whole
  point of the patch type, `types.go`). Fields: `Rect`, `Color`, `Opacity`,
  `Content` are `*T`; apply only when non-nil. `Points []Point` and
  `Meta map[string]any` are slices/maps — treat **non-nil = replace**,
  **nil = unchanged** (document this asymmetry; there is no way to distinguish
  "clear to empty" from "leave alone" for a nil slice without a pointer, so
  "clear" is out of scope — flag as O3).
- Re-validate any changed field with the §4.1 rules (e.g. a patched `Rect` must
  stay finite and non-negative; a patched `Opacity` is clamped). Invalid patch →
  `ErrInvalidAnnotation`, annotation left unmodified.
- Bump `UpdatedAt`; return the stored copy.

### 4.3 `RemoveAnnotation(id)`

- Find by `id`; miss → `ErrAnnotationNotFound`. On hit, remove from the slice
  (order-preserving delete). Return `nil`.

### 4.4 `GetAnnotations(pageID)`

- Return a **copy** of the annotations whose `PageID == pageID`, in insertion
  order; `nil` if none (CONVENTIONS §3.5 — nil slice for "no results").
- **Do not** error on an unknown `pageID`: the annotation layer is deliberately
  decoupled from the live page set (a layer may be imported before/without the
  matching page). Return empty. (Contrast with `Add`, which *does* validate the
  page, because it creates editable state against the current document. Flag this
  intentional asymmetry — O2.)

---

## 5. Copy discipline (immutability at the boundary)

`Annotation` holds two caller-owned reference fields: `Points []Point` and
`Meta map[string]any`. To keep the internal layer immune to outside mutation and
to keep returned values safe:

- **On ingest** (`AddAnnotation`, `UpdateAnnotation` patch, `ImportAnnotationLayer`):
  deep-copy `Points` (copy the slice) and `Meta` (copy the map; values are
  `any` — a shallow value copy is acceptable for Wave 1, note the limitation).
- **On egress** (`GetAnnotations`, the returned copy from `Add`/`Update`,
  `ExportAnnotationLayer`): deep-copy the same fields so a caller mutating the
  returned data cannot reach into `d.annotations`.

Cost is negligible at Wave-1 volumes. State this in the method comments
("returned annotations are independent copies; mutating them does not affect the
document").

---

## 6. Concurrency (locking discipline)

These methods mutate shared `Document` state and can run concurrently with
`RenderPage`/`RenderAllPages`. Use the existing `d.mu sync.Mutex`.

**Rules:**

1. **Guard the whole read-modify-write** of `d.annotations`, `annotationSeq`, and
   the current-layer fields under `d.mu`. Copy out the result *inside* the lock.
2. **Never invoke the `onAnnotationChange` callback while holding `d.mu`.**
   `sync.Mutex` is non-reentrant, and a callback is user code that may call back
   into `GetAnnotations`/`AddAnnotation` (e.g. a viewer that re-reads on change)
   → self-deadlock. The pattern per mutator:
   - lock → validate + mutate + build the copy to report → snapshot `fn :=
     d.onAnnotationChange` → unlock → if `fn != nil`, call `fn(copy)`.
   Snapshotting `fn` under the lock also makes it race-free against a concurrent
   `OnAnnotationChange` setter (which already locks `d.mu`).
3. `GetAnnotations` returns copies (§5), so no internal slice header escapes
   under no lock.
4. All of this must pass `go test -race` (CONVENTIONS §6, §8).

**Closed document:** decide whether mutating after `Close()` is an error. Simple
Wave-1 choice: mutators check `d.closed` under the lock and return an error if
closed. There is no perfect existing sentinel (`ErrReadOnly` is about
viewer/read-only mode, not lifecycle). **Recommendation:** for Wave 1, do *not*
add a lifecycle error — `Close()` currently only flips a flag and frees nothing
annotation-related, so operating on a closed doc is harmless. Flag as O4 if the
maintainer wants strictness.

---

## 7. The `OnAnnotationChange` callback

- Fires **once per successful** `AddAnnotation`, `UpdateAnnotation`, and
  `RemoveAnnotation`, **after** the mutation commits and **after** the lock is
  released (§6 rule 2).
- The argument is the resulting `Annotation`:
  - add/update → the stored (post-mutation) copy;
  - remove → the removed annotation's last state (so a viewer knows *what* left
    and can drop it by `ID`).
- **Not** fired on a validation error (nothing changed).
- **Not** fired per-annotation by `ImportAnnotationLayer`. Import is a bulk load;
  emitting one event per imported annotation would create a storm and is easy to
  get wrong re: ordering. A viewer refreshes from `GetAnnotations` after an
  import. (Flag O5 if a single "layer changed" signal is wanted — the current
  callback type `func(a Annotation)` can't express "bulk", so this would need a
  new event.)

---

## 8. Export / import JSON

### 8.1 Struct tags on `types.go` (serialization contract)

**Recommendation: add explicit `json` struct tags** to `Annotation`, `Rect`,
`Point`, and `AnnotationLayer`. This is an **API/serialization contract** (the
JS wrappers and any stored `.json` sidecar depend on it), so it must be explicit
and match the camelCase already shown in spec Appendix A — not left to Go's
default field-name casing. Proposed tags:

| Type | Field → JSON key | Notes |
|---|---|---|
| `Annotation` | `ID`→`id`, `PageID`→`pageId`, `Type`→`type`, `Rect`→`rect`, `Color`→`color`, `Opacity`→`opacity`, `Content`→`content`, `Points`→`points`, `AuthorID`→`authorId`, `CreatedAt`→`createdAt`, `UpdatedAt`→`updatedAt`, `Meta`→`meta` | see omitempty below |
| `Rect` | `X`→`x`, `Y`→`y`, `Width`→`width`, `Height`→`height` | never omitempty (a 0 coord is real) |
| `Point` | `X`→`x`, `Y`→`y` | never omitempty |
| `AnnotationLayer` | `ID`→`id`, `DocumentID`→`documentId`, `Name`→`name`, `Annotations`→`annotations`, `Visible`→`visible`, `CreatedAt`→`createdAt` | |

`omitempty` guidance (round-trip safety, §8.4): apply `omitempty` only to fields
that are semantically absent when empty — `content`, `points`, `authorId`,
`meta`, and `updatedAt`. Do **not** put `omitempty` on `rect`, `type`, `color`,
`opacity`, or the `Rect`/`Point` numeric fields: a zero there (e.g. `opacity: 0`,
`x: 0`) is a real value and must survive the round trip. (Marshaling a
`time.Time`/zero — `updatedAt` may equal `createdAt`; `omitempty` on a
`time.Time` does not actually omit the zero value, so if you want it omitted when
unset, either always emit it or make it a pointer. Simplest: always emit
`updatedAt`. Flag as a minor decision.)

### 8.2 `ExportAnnotationLayer() (*AnnotationLayer, error)`

Returns a `*AnnotationLayer` populated from the current layer:

- `ID` = `d.layerID` (mint one lazily on first add/export if empty, form
  `layer-<n>` or a ULID — same trade-off as §3.1; a per-document `layer-1` is
  fine for Wave 1).
- `DocumentID` = `d.ID`.
- `Name` = `d.layerName`.
- `Visible` = `d.layerVisible`.
- `CreatedAt` = `d.layerCreatedAt`.
- `Annotations` = a deep copy of `d.annotations` in order (§5).

The method returns the **struct**; turning it into bytes is `encoding/json`'s job
via the §8.1 tags (done by the CLI, the JS wrapper, or a WASM export shim). The
standalone layer JSON is exactly the `AnnotationLayer` object — i.e. the
`annotationLayer` node of spec Appendix A, without the surrounding
`fluxdocs`/`document` envelope (that envelope belongs to full-document export, a
different feature). If a top-level `schemaVersion` on the byte stream is wanted
for forward-compat, it needs a wrapper type — see O6.

### 8.3 `ImportAnnotationLayer(layer AnnotationLayer) error`

**Semantics: MERGE, not replace.** The name and the §6.2 use case ("import a
reviewer's layer") mean the incoming annotations are added to whatever the
document already holds. (A `replace` variant can come later; flag O7.)

Procedure:

1. For each incoming annotation, validate `Rect` finiteness/non-negativity (§4.1
   rule 3). **Do not** reject by page: an imported layer may reference pages by
   id that are valid in that document; missing-page annotations are still
   preserved as data (they simply won't render). This mirrors `GetAnnotations`
   leniency (§4.4).
2. **Type handling — preserve fidelity.** Accept the four Wave-1 types normally.
   Annotations of deferred types (`stamp`/`redact`/`signature`) that appear in an
   imported layer (the spec Appendix A example literally contains a `redact`
   annotation) are **preserved verbatim as data** so export→import→export is
   loss-free — but they remain non-editable via `Add` and non-rendered in Wave 1.
   Document this clearly. (This is the one place Core tolerates a redact-typed
   record; it is inert data, not a redaction operation — no content is ever
   removed here. Flag O8.)
3. **ID collision handling.** If an incoming `annotation.ID` is empty or already
   exists in `d.annotations`, **re-key** it: mint a fresh `ann-<n>` (§3.1) and use
   that. Otherwise keep the incoming ID as-is. Either way, advance
   `annotationSeq` past any numeric `ann-<n>` IDs kept, so future `Add` calls
   never collide.
4. **Preserve timestamps** (`CreatedAt`/`UpdatedAt`) from the layer; do not
   restamp (§3.2). Deep-copy `Points`/`Meta` on the way in (§5).
5. Optionally adopt the layer's `Name`/`Visible`/`CreatedAt` into the current
   layer identity if the document had none yet; otherwise keep the document's.
   (Minor — flag O9.)
6. Under `d.mu` for the whole operation; no per-annotation callback (§7).

**DoS note:** an imported layer is untrusted input. Consider bounding the number
of annotations and total `Content`/`Points`/`Meta` size accepted per import to
avoid a memory blow-up (SECURITY.md §3 spirit). A concrete cap is a maintainer
decision — O10.

### 8.4 Round-trip stability (a testable invariant)

`export → (marshal) → (unmarshal) → import → export` must yield an
`AnnotationLayer` equal to the first export, given a document that started empty
before import. This holds because: order is preserved (slice), IDs are preserved
(no collision after re-key on a clean doc), timestamps are preserved, and the
tag/omitempty choices (§8.1) don't drop real zero values. Make this a table/unit
test.

---

## 9. Security & privacy

- **No source PDF in the layer.** `AnnotationLayer` carries only annotation
  geometry, colors, user-authored text, author ids, timestamps, and the
  `DocumentID` string. It must **never** embed the original PDF bytes or any
  parsed content-stream data. (There is no field for it, and none must be added.)
- **No network, ever.** Export/import operate on in-memory structs and byte
  buffers via `encoding/json` — pure Go, WASM-safe, no filesystem or socket on
  the path. This upholds the WASM privacy promise (CLAUDE.md §1.3, §7.2).
- **No new logging of content.** Annotation `Content`/`AuthorID` may be
  user-authored PII; do not log annotation bodies.
- **Untrusted import.** Validate rects, re-key IDs, and bound size (§8.3) — treat
  an imported layer like any other untrusted input.
- This increment removes nothing from any PDF, so it is **not** a redaction path
  and carries none of the SECURITY.md §1 redaction gates. (The lone subtlety: a
  `redact`-typed record may be *stored* on import as inert data — §8.3 step 2 —
  which is not a redaction and leaks nothing that wasn't already in the layer.)

---

## 10. Testing strategy

- **Table-driven (unit):**
  - `AddAnnotation`: each of the 4 valid types; rejects `stamp`/`redact`/
    `signature`/unknown (`ErrInvalidAnnotation`); rejects unknown page
    (`ErrPageNotFound`); rejects `NaN`/`Inf`/negative rect; clamps opacity;
    assigns `ann-1`, `ann-2`, … deterministically; sets `CreatedAt==UpdatedAt`.
  - `UpdateAnnotation`: nil patch fields leave values unchanged; each settable
    field updates; `UpdatedAt` bumps and `CreatedAt` doesn't; unknown id →
    `ErrAnnotationNotFound`; invalid patch leaves the annotation untouched.
  - `RemoveAnnotation`: removes; order of the rest preserved; unknown id →
    `ErrAnnotationNotFound`.
  - `GetAnnotations`: filters by page, order preserved, `nil` when none, no error
    on unknown page; returned copies are independent (mutating them doesn't touch
    the doc).
- **Round-trip (unit):** the §8.4 invariant, including a layer that contains a
  deferred-type (`redact`) record to prove verbatim preservation; and a layer
  with `opacity: 0` / zero coords to prove omitempty doesn't drop them.
- **Import collision:** importing a layer whose ids collide with existing ids
  re-keys the incoming ones and leaves existing ones intact; `annotationSeq`
  advances so a subsequent `Add` doesn't collide.
- **Callback:** fires once per successful add/update/remove with the right value;
  never fires on validation error; not fired per-annotation on import; a callback
  that re-enters `GetAnnotations`/`AddAnnotation` does not deadlock.
- **Concurrency:** `go test -race` with concurrent `AddAnnotation` +
  `GetAnnotations` (+ a `RenderPage` if convenient) — no data race, no panic.
- **Security/robustness:** import of a hostile layer (huge annotation count,
  `NaN` rect, empty/duplicate ids) is rejected or safely bounded without panic.
- **CI:** stays green for **native and WASM** (pure-Go path; WASM must build).

---

## 11. Open questions (maintainer decision)

- **O1 — ID scheme:** per-document counter `ann-<n>` (deterministic,
  test-friendly, doc-local) vs ULID/`crypto/rand` hex (globally unique, needed at
  merge/collab). Proposed: counter for Wave 1, switch at Wave 3. Same question
  for `LayerID`.
- **O2 — `GetAnnotations` on unknown page:** return empty (proposed, decoupled
  layer) vs `ErrPageNotFound` (consistent with `Add`). Proposed: empty.
- **O3 — clearing slice/map fields via patch:** `AnnotationPatch` can't express
  "clear `Points`/`Meta` to empty" (nil = unchanged). Accept the limitation, or
  add a sentinel/flag? Proposed: accept for Wave 1.
- **O4 — mutations after `Close()`:** error out, or allow (harmless today)?
  Proposed: allow; no new lifecycle sentinel.
- **O5 — bulk change event:** import fires no per-annotation callback. Add a
  separate "layer changed" event, or leave viewers to refresh via
  `GetAnnotations`? Proposed: leave it (current callback type can't express bulk).
- **O6 — export envelope/versioning:** `ExportAnnotationLayer` returns the bare
  `*AnnotationLayer` (no `schemaVersion`). Add a wrapper type / a
  `…JSON([]byte)` method carrying a version, or defer? Proposed: defer; version
  lives in full-document export (Appendix A envelope).
- **O7 — import merge vs replace:** proposed MERGE. Do we also want an explicit
  replace/clear API? Proposed: defer.
- **O8 — storing deferred-type records on import:** preserve `redact`/`stamp`/
  `signature` verbatim as inert data for round-trip fidelity (proposed) vs drop
  them on import. Proposed: preserve (matches Appendix A example), never act on
  them in Core.
- **O9 — layer identity on import:** adopt the incoming layer's
  `Name`/`Visible`/`CreatedAt` when the document has none, vs always keep the
  document's. Proposed: adopt only when unset.
- **O10 — import size cap:** concrete bound on annotation count / total bytes for
  an imported layer (DoS defense). Needs a number.
- **O11 — new sentinel:** this design adds `ErrInvalidAnnotation` to
  `core/errors.go` (invalid type / bad rect / invalid patch). Confirm the name
  and that it belongs in the public sentinel set (it distinguishes a real caller
  case, per CONVENTIONS §4).

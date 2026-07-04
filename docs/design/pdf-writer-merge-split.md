# Design: PDF writer + Merge / Split / Save (Wave 1)

Status: design (docs only). Tier: **Core (MIT)**. Packages: `core/parse`
(the serializer) + `core` (`document.go` wiring).
Spec basis: ISO 32000-1:2008 §7.5 (file structure), §7.5.2 (header), §7.5.3
(body / indirect objects), §7.5.4 (cross-reference table), §7.5.5 (trailer),
§7.3 (object syntax: strings, names, numbers, arrays, dictionaries, streams),
§7.7.2 (document catalog), §7.7.3 (page tree). Clean-room — derived from the
public ISO spec only.

This note tells the implementer exactly what to build. It contains **no Go
code**. Hand off to `go-implementer`. This is the first **write** capability in
the codebase, which today can only read.

---

## 1. Scope

Build a serializer that turns a set of PDF objects into a valid PDF byte stream,
and use it to implement the four stubbed methods in `core/document.go`:

- `Merge(other *Document) (*Document, error)`
- `Split(pageRanges [][2]int) ([]*Document, error)`
- `Save(path string) error`
- `SaveTo(w io.Writer) error`

`Reorder` and `Rotate` are **out of scope** for this increment (they remain
stubbed). They will reuse the same writer + copy machinery later; keep the API
shaped so they can.

In scope:

- A `parse.Writer` that assigns fresh object numbers and emits a complete file:
  header, body, **classic** cross-reference table, trailer, `startxref`, `%%EOF`.
- Serialization of every `parse.Object` variant back to conforming PDF syntax.
- A deep copy-with-remap of a page's transitive object closure from a source
  `Resolver` into the writer's fresh object-number space, with cycle and DoS
  guards.
- Building a fresh `/Catalog → /Pages → page` tree from copied pages.
- Merge / Split / Save / SaveTo on top of the above.

Explicitly **out of scope** (defer; degrade to an error, never a panic):

- Encrypted input. Already rejected at open (`OpenBytes` returns
  `ErrEncryptedDocument` when the trailer has `/Encrypt`). The writer assumes
  decrypted objects and never emits `/Encrypt`.
- Incremental update (appending a new xref section with `/Prev`). Wave 1 always
  rewrites a whole, self-contained file.
- Writing **xref streams** and **object streams** (ObjStm). Wave 1 output is a
  plain PDF-1.7 file with a classic table only (see §4). The reader already
  handles both forms on input; the writer need not produce them.
- Linearization ("fast web view").
- Annotation flattening. Annotations are a separate JSON layer; Save writes the
  PDF **without** baking them in (see §8).
- Preserving the document `/Info`, `/Metadata` (XMP), `/StructTreeRoot` (tagged
  PDF), `/AcroForm`, outlines, named destinations, and other catalog-level
  features across a copy. Wave 1 copies **pages and their reachable content
  only** and synthesizes a minimal catalog (see §5 and open question O2).

---

## 2. Package placement & public surface

### 2.1 Where the writer lives

Put the serializer in **`core/parse`**, as `core/parse/writer.go`. Reasons:

- It is the exact inverse of the existing read layer (`xref.go`,
  `indirect.go`, `object.go`) and operates purely on `parse.Object` / `Dict` /
  `*Stream`. Co-locating keeps the object model and its serialization in one
  package, and lets the writer reuse the same name-escaping / string-encoding
  rules the lexer already knows.
- It stays MIT: `parse` imports nothing CGO. `core` already imports `parse`, so
  `document.go` can call it directly.
- A separate `core/write` package would force the object model to be exported
  more widely with no benefit; `parse` already owns it.

Do **not** put it in `core` (would mix the public SDK surface with byte-level
serialization) and do **not** create a new top-level module.

### 2.2 `parse.Writer` API (proposed)

A small, allocation-then-emit type. Exact names are the implementer's call;
this is the shape:

- `NewWriter() *Writer` — fresh, empty object space; next object number starts
  at 1.
- `(*Writer) Add(obj Object) Reference` — append `obj` as a new indirect object,
  return its `Reference` (generation 0). This is how a caller registers an
  object it already holds a direct value for (e.g. a synthesized catalog).
  `Add` does **not** rewrite references inside `obj`; the caller is responsible
  for handing in an object whose internal references already point at writer
  numbers (the copier in §4 does this).
- `(*Writer) SetRoot(ref Reference)` — record which object is `/Root` for the
  trailer. Required before `WriteTo`.
- `(*Writer) SetInfo(ref Reference)` — optional; record `/Info` for the trailer.
- `(*Writer) WriteTo(w io.Writer) error` — emit the whole file (§3). Returns a
  wrapped `errLex`-style error on an internal inconsistency (e.g. `SetRoot`
  never called), never panics.

The copier (§4) is a second type, e.g. `Copier`, that owns a `*Writer` plus the
source `*Resolver` and the visited map. Sketch:

- `NewCopier(w *Writer, src *Resolver) *Copier`
- `(*Copier) CopyPage(page Object) (Reference, error)` — deep-copies one page
  Dict and its closure into `w`, returns the new page object's `Reference` (with
  `/Parent` left unset — the caller patches it in §5).

`Merge`/`Split`/`Save` in `core` call **one** high-level helper rather than
driving `Writer`+`Copier` themselves. Add an exported function in `parse`:

- `parse.BuildDocument(sources []PageSource) ([]byte, error)` where a
  `PageSource` is `{Resolver *Resolver, Page Object}` (a source resolver plus one
  page object/reference from it). It runs the full pipeline: new `Writer`, copy
  each page's closure, build the catalog/page tree, serialize, return bytes.
  Merge and Split differ only in the list of `PageSource`s they pass. This keeps
  `core/document.go` thin and testable.

---

## 3. The serializer (ISO §7.5)

`WriteTo` emits, in order:

### 3.1 Header (§7.5.2)

`%PDF-1.7` + EOL, then a comment line with four bytes ≥ 128 so tools treat the
file as binary: `%` followed by e.g. `0xE2 0xE3 0xCF 0xD3` + EOL. Use `\n`
(LF) as the EOL throughout — simplest and conforming.

### 3.2 Body (§7.5.3)

For each object number `n` from 1..N in order, record the **current byte offset**
(this becomes its xref entry), then emit:

```
n 0 obj
<serialized object>
endobj
```

(newline after `obj`, newline before `endobj`). Generation is always `0` — the
writer never reuses numbers, so every object is generation 0.

Serialization of each `Object` type (§7.3). Note these rules differ from the
existing `String()` debug methods in `object.go`, which are **not** valid PDF
and must not be used for output:

- **Null** → `null`.
- **Boolean** → `true` / `false`.
- **Integer** → decimal digits, e.g. `-42`.
- **Real** → a fixed, locale-independent decimal, never scientific notation
  (`%g` is wrong — it can emit `1e+06`, which is not a valid PDF number). Format
  with a bounded number of fractional digits and trim trailing zeros and a
  trailing `.` (e.g. `1000000`, `0.5`, `-3.14`). Non-finite values (NaN/Inf)
  must not occur; if one does, write `0` (defensive, never panic).
- **Name** (§7.3.5) → `/` + the name with every byte outside the regular
  character set, plus `#`, `(`, `)`, `<`, `>`, `[`, `]`, `{`, `}`, `/`, `%`,
  whitespace, and any byte < 0x21 or > 0x7E, escaped as `#xx` (two uppercase hex
  digits). The stored `Name` has `#xx` already decoded, so re-escape on output.
  A `#` in the name value itself becomes `#23`.
- **String** (§7.3.4) → honor the stored `String.Hex` flag so round-trips stay
  faithful:
  - `Hex == false` → literal `( … )`. Escape `(`, `)`, and `\` as `\(`, `\)`,
    `\\`. Emit other bytes as-is; optionally escape non-printables with `\ddd`
    octal, but raw bytes inside `()` are legal as long as parentheses are
    balanced/escaped — escaping `(`/`)`/`\` is sufficient and simplest.
  - `Hex == true` → `< … >` with each byte as two hex digits.
  - When in doubt or when the value contains many unbalanced parens, hex is
    always safe; the implementer may choose to force hex for binary strings.
- **Array** → `[` + space-separated elements + `]`.
- **Dict** → `<<` + `/Key value` pairs + `>>`. **Emit keys in sorted order** so
  output is deterministic (Go map iteration is random) — golden-file tests
  depend on this. Sort by the raw name bytes.
- **Reference** → `n g R` (generation is whatever the object carries; after the
  copy in §4 all references are generation 0).
- **Stream** (§7.3.8) → serialize `st.Dict` first, but **override `/Length`**:
  set it to the exact byte length of `st.Raw` (a direct `Integer`, never an
  indirect reference — that is the whole point of rewriting it). Then:
  ```
  <<dict>>
  stream\n
  <raw bytes verbatim>
  \nendstream
  ```
  The raw bytes are written **unmodified** — the writer does not decode or
  re-encode filters; `/Filter` and `/DecodeParms` are copied through as-is, so
  the already-compressed bytes stay valid. `/Length` is the only stream key the
  writer authoritatively rewrites. Binary-safe: never scan or transform `Raw`.

Strip any stream keys that would make the copied stream inconsistent in a
non-encrypted, non-object-stream file — specifically drop nothing by default
except that `/Length` is replaced. (ObjStm streams are never copied whole; see
§4 on how compressed source objects are handled.)

### 3.3 Cross-reference table (§7.5.4) — classic, and why

Emit a **classic** xref table, not an xref stream. Rationale for Wave 1:

- It is universally readable by every PDF consumer, including old ones and the
  simplest validators. An xref stream requires a PNG-predictor-style encoder and
  a self-referential `/Length`, which is more code and more failure surface.
- The reader already accepts both, so there is no round-trip requirement to
  match the input form.
- Object streams (which motivate xref streams, by shrinking many small objects)
  are out of scope, so the size win does not apply yet.

Format: `xref` + EOL, then a single subsection covering objects `0..N`:

```
xref
0 N+1
0000000000 65535 f 
<offset> 00000 n 
...
```

- Object 0 is the head of the free list: `0000000000 65535 f`.
- Each in-use entry is the byte offset recorded in §3.2, zero-padded to 10
  digits, then ` 00000 n`. **Each entry line is exactly 20 bytes** including a
  2-character EOL (` \r\n`, ` \n`, or `\r\n`) — the fixed-width rule in §7.5.4 is
  mandatory. Use `<10-digit offset><space><5-digit gen><space><n|f><space><LF>`
  (offset+space+gen+space = 18, then `n`/`f` + space + LF = 20). Pick one 20-byte
  layout and keep it exact; validators reject a wrong width.

Because the writer numbers objects contiguously 1..N with none free, a single
subsection is always sufficient.

### 3.4 Trailer + startxref + EOF (§7.5.5)

Record the byte offset where `xref` began (call it `xrefStart`). Then:

```
trailer
<< /Size N+1 /Root <root> 0 R [ /Info <info> 0 R ] >>
startxref
<xrefStart>
%%EOF
```

- `/Size` = highest object number + 1 = N+1.
- `/Root` = the reference set via `SetRoot`. Required.
- `/Info` only if `SetInfo` was called.
- No `/Encrypt`, no `/Prev` (single, complete file).

---

## 4. Object-graph copy with reference remapping (the hard part)

Merge/Split build a new file that contains only the pages they keep, plus the
transitive closure of everything those pages reference. Because the new file has
its own contiguous object-number space, every source `Reference` must be
**remapped** to a new number as it is copied. Shared objects (a font used by two
pages) must be copied **once**; cycles must not loop forever.

### 4.1 State

The `Copier` holds:

- `w *Writer` — destination.
- `src *Resolver` — source; used to resolve references (`src.Resolve`).
- `visited map[int]Reference` — source object number → the `Reference` already
  minted in `w` for it. This both deduplicates shared objects and breaks cycles.
- `budget` counters: number of objects copied and total bytes emitted so far
  (see §4.4).

Note: `visited` is keyed by source **object number** (`Reference.Number`).
Generation is ignored — Wave 1 treats a file as having one generation per number
(true for essentially all real files; incremental-update reuse is out of scope).

### 4.2 Two-phase copy of a value

Copying is a walk over a value tree with reference-following:

`copyValue(obj) → newObj` (a direct value with all inner references remapped):

- `Null / Boolean / Integer / Real / String / Name` → returned unchanged (they
  hold no references).
- `Array` → new array; `copyValue` each element.
- `Dict` → new dict; `copyValue` each value. (Keys are names, unchanged.)
- `*Stream` → new stream: `copyValue` the dict, copy `Raw` verbatim (a fresh
  byte slice), and let the serializer rewrite `/Length` (§3.2).
- `Reference` → `copyRef(ref)` (below), returning the **new** `Reference` to
  splice in place of the old one.

`copyRef(srcRef) → newRef` (copy one indirect object, memoized):

1. If `srcRef.Number` is in `visited`, return the stored new `Reference`
   immediately (dedup + cycle break).
2. **Reserve** the slot *before* recursing: allocate the new object number now
   and put it in `visited`, so a cycle that points back at this object finds the
   reserved reference instead of recursing infinitely. (Implement `Writer` with
   a `Reserve() Reference` that returns a number and a `Fill(ref, obj)` that
   stores the body later — or allocate by adding a placeholder `Null` and
   overwriting. Either way, the number must be claimed before the recursive
   `copyValue`.)
3. Resolve the source object: `resolved, err := src.Resolve(srcRef)`. On error,
   fail the whole copy (propagate) — do not emit a half object. (A missing
   object resolves to `Null` per the resolver; that is fine — it copies as
   `null`.)
4. `body := copyValue(resolved)` — recurses into the object's own references.
   Note that `src.Resolve` transparently pulls objects out of compressed object
   streams (ObjStm) on input, so **compressed source objects are copied as
   ordinary uncompressed objects** in the output — exactly what we want (no
   ObjStm on write).
5. `Fill(newRef, body)` and return `newRef`.

### 4.3 What a page copy touches, and the back-pointer problem

`CopyPage(page)` calls `copyValue` on the **page dictionary**, which pulls in the
whole reachable graph automatically:

- `/Contents` — a stream, an array of streams, or a reference to either. Copied
  as-is (still compressed).
- `/Resources` and everything under it: `/Font` (incl. `/FontFile`,
  `/FontFile2`, `/FontFile3`, `/ToUnicode`, descendant CIDFonts, `/Encoding`),
  `/XObject` (image XObjects and nested form XObjects, which themselves have
  `/Resources` — the recursion handles the nesting), `/ExtGState`,
  `/ColorSpace`, `/Pattern`, `/Shading`, `/Properties`.
- `/Annots` — **see §6 / §8**: for Merge/Split, Wave 1 keeps existing `/Annots`
  that are part of the source page (they are original PDF content, distinct from
  the FluxDocs annotation JSON layer). Copying them is the safe default; they are
  ordinary indirect objects. *Open question O3 — see below.*
- Inheritable attributes: the walker in `pagetree.go` already resolved
  `/MediaBox`, `/Resources`, `/Rotate` from ancestors into `PageInfo`. But
  `copyValue(page)` copies the page **dictionary as it literally is**, which may
  rely on `/Parent` for those inherited values. Therefore **do not copy the page
  dict blindly** — instead build the copied page from the resolved `PageInfo`:
  - Start from a copy of the page's own `Dict`.
  - **Remove `/Parent`** (back-pointer into the old tree; it must not be copied —
    it would drag in the entire old page tree and point at a foreign object).
    The new `/Parent` is set in §5.
  - **Materialize inherited attributes** onto the copied page so nothing is lost
    when `/Parent` is dropped: if the page dict lacks `/MediaBox`, write
    `PageInfo.MediaBox`; if it lacks `/Resources`, write the inherited
    `PageInfo.Resources` (deep-copied); if it lacks `/Rotate` and the inherited
    rotate is non-zero, write it. This makes each output page self-contained.
  - Drop keys that are meaningless in the new document or point back into old
    structure: `/Parent` (removed), and any `/B` (article beads),
    `/StructParents` / `/StructParent` (tagged-PDF back-pointers into a
    `/StructTreeRoot` we do not copy). Dropping `/StructParents` avoids dangling
    references. *Open question O2.*

Concretely: `CopyPage` builds a **new page dict** = `copyValue(page's own Dict
with /Parent and struct back-pointers stripped)`, then patches in the
materialized `/MediaBox` / `/Resources` / `/Rotate`, adds it via the writer, and
returns its reference with `/Parent` still unset.

### 4.4 Security & DoS guards (SECURITY.md §2–3)

Untrusted input reaches this path (the source `Document` came from an untrusted
file). Bound everything:

- **Total copied objects:** a hard cap (e.g. `maxCopyObjects = 1 << 20`, matching
  the `maxPages` scale in `pagetree.go`). `copyRef` increments a counter; exceed
  → error. The `visited` map already bounds re-copies, but a huge legitimate-ish
  graph must still be capped.
- **Total output bytes:** track bytes queued for emit; exceed a cap (e.g. a few
  GB, or a configurable limit) → error. Prevents a decompression-free but
  reference-explosion bomb.
- **Recursion depth:** `copyValue` recursion is bounded by a depth counter
  (e.g. `maxCopyDepth = 256`), mirroring `maxPageTreeDepth`. Deeply nested arrays
  / dicts → error, not a stack overflow.
- **No filter decoding:** the writer copies `Raw` verbatim, so a decompression
  bomb in the source is never expanded here (it stays compressed). Rendering /
  extraction remain the places that must bound decompression.
- Every failure returns a wrapped error and aborts the whole build. **Never
  panic**; a malformed source must degrade to `ErrInvalidPDF` at the `core`
  boundary (see §9).

---

## 5. Building the new catalog and page tree (§7.7.2–§7.7.3)

After copying all pages (each returns a page `Reference` with `/Parent` unset),
synthesize a minimal, valid document structure:

1. **Reserve** a `/Pages` reference first (the pages need it as their `/Parent`).
2. For each copied page reference, set its `/Parent` to the reserved `/Pages`
   reference. (This is why the page bodies are filled/patchable — either patch
   the dict before `Add`, or use `Reserve`/`Fill` so `/Parent` can be written
   after the `/Pages` number is known.)
3. Build the `/Pages` node dict: `/Type /Pages`, `/Kids [ page refs… ]`,
   `/Count <number of pages>`. Fill it into its reserved slot.
4. Build the `/Catalog` dict: `/Type /Catalog`, `/Pages <pages ref>`. Add it,
   and call `SetRoot` with its reference.
5. Optionally build a minimal `/Info` (see §6) and `SetInfo`.

Do **not** copy the source catalog wholesale — it may carry `/AcroForm`,
`/Names`, `/Outlines`, `/StructTreeRoot`, `/OpenAction`, viewer prefs, etc., all
of which reference objects we deliberately did not copy and would dangle.
A clean synthesized catalog is correct and safe for Wave 1. *Open question O2
covers what to preserve later.*

---

## 6. Merge semantics

`d.Merge(other)`:

- Returns a **new** `*Document`; `d` and `other` are unchanged (immutable —
  neither resolver nor page set is mutated).
- Page order: all of `d`'s pages (in order) followed by all of `other`'s pages
  (in order).
- Both documents' pages are copied into **one** new object space. The
  `PageSource` list handed to `parse.BuildDocument` is `d`'s pages then `other`'s
  pages, each paired with its own resolver (`d.resolver` / `other.resolver`).
  The copier keys `visited` per source, so use a **separate copier per source
  resolver** (or key `visited` by `(resolver, number)`); objects from `d` and
  `other` must never be conflated even if they share a source number.
- **Metadata / `/Info`:** `d`'s `/Info` wins. For Wave 1, synthesize a minimal
  `/Info` from `d.Metadata` (Producer = "FluxDocs", carry Title/Author/Subject
  if present) rather than copying the source `/Info` object graph. Document this
  so users know merge does not union metadata. *Open question O4.*
- **Guard:** if either document has no `resolver` (e.g. a test-constructed
  `Document` with nil resolver/pageInfos), Merge cannot copy real objects →
  return `errNotImplemented`-style behavior is wrong; return `ErrInvalidPDF` or a
  clear error. State that Merge requires opened documents.

### 6.1 Making the returned Document usable — serialize then re-open

The returned `*Document` must be fully wired: it needs a `resolver` and
`pageInfos` so `RenderPage` / `ExtractText` / a subsequent `Save` all work.

**Chosen approach:** `parse.BuildDocument` returns the serialized `[]byte`;
Merge then calls `core.OpenBytes(bytes)` on the result and returns that
`Document`. This guarantees the returned handle is identical in kind to any
opened document — resolver, pageInfos, Pages, Metadata are all populated by the
existing, tested open path. No second code path to build a Document in memory.

Cost: one extra parse of the just-written bytes. Accept it for Wave 1 — merges
are not hot-loop operations, and correctness/simplicity dominate. If profiling
later shows it matters, add an in-memory constructor that builds `Document`
directly from the writer's object table; defer that.

One subtlety: `OpenBytes` copies `path`-based `ID` only in `OpenDocument`; the
merged Document's `ID` will be empty. Set a synthetic `ID` (e.g. derived from the
sources) after re-open if desired — cosmetic, not required.

---

## 7. Split semantics

`d.Split(pageRanges [][2]int)`:

- Each `[start, end]` is **0-based, inclusive**. Produces one `*Document` per
  range, in the order given.
- **Validation** (before building anything): for every range, require
  `0 <= start <= end < d.PageCount`. On any violation, return `nil` and an error.
  Reuse **`ErrPageNotFound`** (a range referencing a non-existent page is
  conceptually "page not found") — do not invent a new sentinel unless review
  prefers one. *Open question O5.* An empty `pageRanges` → return an empty slice,
  no error (or error; pick one and document — recommend empty slice, no error).
- For each valid range, gather `PageSource`s for pages `start..end` from
  `d.resolver` and call `parse.BuildDocument`, then `OpenBytes` the result
  (same round-trip as §6.1).
- Each output document gets a minimal synthesized catalog and `/Info` derived
  from `d.Metadata`.
- `d` is unchanged.

---

## 8. Save / SaveTo

- `SaveTo(w io.Writer)`: serialize the Document's **current page set** to `w`.
  Implementation: build a `PageSource` list from `d`'s own pages
  (`d.resolver` + each `pageInfos[i]`'s page object) and run the same
  `parse.BuildDocument` pipeline, streaming the bytes to `w`. This effectively
  rewrites the document as a clean, single-file PDF containing exactly its
  current pages (which, after a Merge/Split, are already the right set).
- `Save(path string)`: open/create the file at `path` (`os.Create`), then
  delegate to `SaveTo`, closing the file and returning any error. WASM note:
  `Save` (filesystem) is native-only in practice; `SaveTo` is the WASM-friendly
  entry point (write into a JS buffer) and must not touch the filesystem or the
  network.
- **Annotations are NOT baked in.** The FluxDocs annotation layer is separate
  JSON (`d.annotations`); `Save`/`SaveTo` in Wave 1 write the PDF **without**
  drawing annotations onto pages. The existing `Save` doc comment mentions
  "including flattened annotations, if any" — update that comment to state that
  flattening is a separate future increment (`FlattenAnnotations`) and Save emits
  the un-annotated PDF. Existing source `/Annots` on a page (original PDF
  annotations, not the FluxDocs layer) are carried through by the copy per §4.3.
- A Document with a `nil` resolver (test-constructed, never opened) cannot be
  serialized this way → return a clear error (`ErrInvalidPDF` or a dedicated
  message). State this precondition.

---

## 9. Errors & sentinels

- Reuse existing sentinels. Map any `parse`-layer failure at the `core` boundary
  to `ErrInvalidPDF` (as `OpenBytes` already does: `fmt.Errorf("%w: %v",
  ErrInvalidPDF, err)`), so callers keep using `errors.Is(err, ErrInvalidPDF)`.
- `Split` range validation → `ErrPageNotFound` (O5).
- No new exported sentinel is strictly required. If review wants Save/Merge on an
  unopened Document to be distinguishable, consider reusing `ErrReadOnly` or
  adding one — flag as O6, not decided here.
- Internal `parse.Writer`/`Copier` errors are wrapped `errLex`-style within
  `parse`, then wrapped again to `ErrInvalidPDF` in `core`. Never panic anywhere
  on the write path — a hostile source must fail one call cleanly (SECURITY.md).

---

## 10. Testing strategy

- **Serializer unit tests (table-driven, in `parse`):** each `Object` type →
  expected bytes. Cover Real formatting (no scientific notation; `0.5`,
  `-3.14`, `1000000`), Name escaping (`#`, spaces, high bytes), String literal
  vs hex round-trip (honoring `String.Hex`), deterministic sorted dict keys,
  stream `/Length` override and binary-safe `Raw`, the fixed 20-byte xref line
  width.
- **Round-trip tests:** for each PDF in `testing/corpus/`, `OpenBytes` → `SaveTo`
  → `OpenBytes` again; assert page count is preserved, each page's `ExtractText`
  matches the original, and the re-opened file's `RenderPage` matches a golden
  (or matches the original render pixel-for-pixel within tolerance). This is the
  strongest correctness signal.
- **Merge tests:** merge A (m pages) + B (n pages) → m+n pages; page i of the
  merged doc extracts the same text as the corresponding source page; shared
  objects within one source are deduped (assert the output object count is
  sane / not doubled). Merge is associative-ish: `(A∪B)` page order is A then B.
- **Split tests:** ranges produce the right page counts and content; invalid
  ranges (`start<0`, `end>=count`, `start>end`) → `ErrPageNotFound`; empty
  ranges → empty slice.
- **Security / fuzz:** add the writer to the fuzz surface — `OpenBytes` a fuzz
  seed then `SaveTo` to `io.Discard`; assert no panic and bounded time/memory.
  Add malformed corpus files whose closure has cycles (a page whose `/Resources`
  references back to the page) to prove the `visited` guard and depth/object
  caps hold. Seeds under `testing/security/fuzz/`.
- **Immutability tests:** after `d.Merge(other)` / `d.Split(...)`, assert `d` and
  `other` still open, render, and extract identically (no source mutation).
- **Determinism:** serializing the same Document twice yields byte-identical
  output (sorted keys, stable object numbering, fixed EOL) — needed for golden
  files and for content-hashing in the Cloud tier later.

---

## 11. Open questions (need a maintainer decision)

- **O1 — Real number precision.** How many fractional digits before trimming?
  PDF coordinates rarely need more than ~5 significant digits, but content-stream
  matrices can. Recommend formatting with enough precision to round-trip a
  float64 used in page geometry (e.g. up to 6 fractional digits, trailing zeros
  trimmed). Note: content-stream numbers live inside `Raw` bytes and are copied
  verbatim, so this only affects numbers the writer itself emits (MediaBox,
  Matrix arrays we materialize, etc.). Confirm the digit budget.
- **O2 — Catalog preservation.** Wave 1 synthesizes a bare catalog and drops
  `/AcroForm`, `/StructTreeRoot`, `/Outlines`, `/Names`, viewer prefs. That means
  merge/split lose form fields, tagging, and bookmarks. Acceptable for Wave 1?
  When (which wave) do we selectively carry these across a copy, and with what
  reference-fixup rules? This intersects with the Pro forms feature.
- **O3 — `/Annots` on copied pages.** Carry through original-PDF page annotations
  on Merge/Split (recommended: yes, they are page content), or strip them for a
  "clean" output? If carried, confirm we are not conflating them with the
  FluxDocs JSON annotation layer (we are not — different mechanism).
- **O4 — Merge metadata policy.** Confirm "`d`'s Info wins, synthesized from
  `d.Metadata`, Producer=FluxDocs". Alternative: copy `d`'s source `/Info` object
  graph verbatim. Recommend synthesized (simpler, no dangling refs).
- **O5 — Split invalid-range error.** Reuse `ErrPageNotFound` (recommended) or
  add `ErrInvalidPageRange`? And: empty `pageRanges` → empty slice+nil
  (recommended) or an error?
- **O6 — Save on an unopened / resolver-less Document.** Which error? Reuse
  `ErrInvalidPDF`, `ErrReadOnly`, or add a dedicated sentinel?
- **O7 — In-memory Document vs re-open round-trip.** §6.1 chooses
  serialize-then-`OpenBytes` for simplicity. Confirm we accept the extra parse
  for Wave 1 and defer the direct in-memory constructor.

# Design: redaction hardening (one model instead of per-leak patches)

Status: design (docs only). Base: `master` at 446eaea.
Tier: **Pro** for the redaction engine (`core/docops/redact`, `core/redact.go`,
`core/pii.go`). It also touches **Core (MIT)** code in `core/parse` (strict
scanner, copier) and a new shared font model, which Core callers (render,
extract, merge/split) also see. No CGO. No build tags.
Spec basis: ISO 32000-1:2008 only (section numbers below). Clean-room: viewer
behaviour is treated as black-box output (Ghostscript `txtwrite` repros), never
read from any viewer's source.
Related: `.claude/SECURITY.md` §1–3, spec §7.4, §13.4, §18.1,
`docs/design/form-xobject.md`, `docs/design/pdf-writer-merge-split.md`,
`docs/design/render-budget.md` (budget concept only; this design does not depend
on that feature landing).

**Sequencing.** Phase 1 starts only after the render-budget feature has landed
on `master`. It must rebase on that feature's changes to `core/errors.go` (new
sentinel(s), e.g. `ErrRenderBudgetExceeded`) and `core/document.go`. The new
redaction sentinels go in the same sentinel block, following its conventions.
Do not start Phase 1 on a branch that predates that merge.

---

## Decisions (maintainer-approved, 2026-10-15)

| ID | Decision | Effect in this note |
|---|---|---|
| P1 / D2 | **Approved as proposed.** Add `core.ErrRedactionUnsupported` and `*core.RedactionError{Page, Reason, Detail, Err}` with reason codes. `ErrInvalidPDF` is **dropped** from the redaction error chain (pre-1.0). `redact.ErrFormXObjectText` stays reachable via `Unwrap` for one release | §6.9, §9 P1 |
| P3 / D3 | **Approved as proposed.** Add `core.ErrRedactionVerification`, which may wrap `ErrInternal`. Verification is **always on, with no opt-out**. Cost target: ≤ 50% of `RedactAndFlatten` | §5.6, §9 P3 |
| D1 | **Strict.** No `"EI"` byte pair anywhere in the raw inline-image data (D1-A). Relax later only with differential evidence | §6.1 |
| D4 | **Automatically extend the plan.** When removed text is found where the plan did not reach (other pages, annotations, `/Info`, …), the redaction is extended to those occurrences and redone. Precise semantics are in §6.11 | §5.6, §6.11, §9 P8/P9 |
| D5 | **Phase 1 removes images and paths under regions whole.** A full-page scan under any region is blanked. Pixel-level image redaction comes in Phase 2 | §6.10 |
| D7 | **Strip all `/ActualText`, `/Alt` and `/E` on marked pages in Phase 1** | §6.7 |
| D11 | **The back-reference fix (`BuildOptions.PageRefs`) also applies to Merge/Split** | §5.5 |
| D12 | **N1 matching fix moves into Phase 1:** Unicode via `/ToUnicode`, matched across TJ elements | §5.3, §11 |
| D6, D8, D9, D10, D13 | **Not yet decided; the default applies.** D6: glyph-level granularity once Phase 2 lands, operator-level as fallback, no rounding. D8: keep refusing `/PS` XObjects. D9: accept and document the fidelity side effects. D10: Ghostscript oracle mandatory in CI, no further oracles yet. D13: public `ExtractText` stays page-only; the gate wording names the deep extraction | §13 |

| P8 | **Approved (2026-10-15).** `func (d *Document) RedactionReport() (RedactionReport, bool)` on the `*Document` returned by `RedactAndFlatten`, with no signature change. The cloud `redactPII` response adds `"auto_extended"`; MCP and CLI print a text-free summary | §9 P8 |
| P9 | **Approved (2026-10-15).** Propagation thresholds: 4 for matched strings, 8 for `RedactArea`-removed text. Matching is case-sensitive, after NFKC. `golang.org/x/text` becomes a direct dependency | §6.11, §9 P9 |
| Attachments | **Approved (2026-10-15).** Drop all FileAttachment annotations whenever a redaction runs (non-empty plan), and list them in the report (`Reason "attachment-policy"`) | §6.11 |

**No maintainer decision blocks Phase 1 any longer.** The remaining open items
(D6, D8, D9, D10, D13, WASM cap) have defaults that apply, and the
measurements in §13 do not gate any step. P2 (normalize `RedactArea` rects;
reject NaN/Inf with `ErrInvalidArgument`) is not formally decided; its default
is to implement it as proposed in step 1.5.

---

## 1. Why this note

Every review round has found another leak of the same kind. Our scanner
builds one model of "what the page shows". A mainstream viewer builds a
different model. The difference is text that the viewer draws and we do not
see, or bytes we keep that a reader can recover. Patching each route one at a
time does not converge, so this note fixes the model:

1. One **strict content model** decides, for every byte of every content-bearing
   stream, whether it is an operator, an operand or opaque data. If any
   mainstream reading could disagree, it reports a **loss**.
2. One **resource walker** finds every content-bearing stream that a page can
   paint, by every route ISO 32000-1 defines, using a lookup that matches what
   viewers do.
3. One **rewriter** removes content from each of those streams. Shared
   objects are copy-on-write.
4. A mandatory **post-redaction verification** re-reads the output. It refuses
   to return a document that still holds removed content where we said it would
   be gone.

## 2. Threat model

**Assets.** The content of each marked region (`RedactArea`) and every
occurrence the caller asked us to remove (`RedactText`, `AutoRedactPII`).

**Adversaries.**

- **A1, the crafter.** A1 controls the input bytes. A1 makes text visible in
  common viewers (Acrobat, pdf.js/Firefox, Chrome/PDFium, macOS Preview,
  MuPDF-based readers, Ghostscript) but invisible to our scanner, or visible
  through a route we do not walk. Examples: an inline-image boundary dispute, a
  form reached through an indirect `/Subtype`, a soft-mask group, a Type 3 glyph
  procedure, `/ActualText`. A1 can also be an ordinary but unusual producer.
  The effect is the same.
- **A2, the reader of the output.** A2 has unlimited tooling: every viewer's
  copy/paste and text export, object dumps, `strings`/inflate over every stream,
  diffs against layout, and glyph-position analysis (Bland, Iyer, Levchenko,
  "Story Beyond the Eye: Glyph Positions Break PDF Text Redaction", PETS 2023).

**Failure classes.**

- **F1 Missed visible content.** A viewer shows text inside a marked region,
  or text matching a requested pattern, after redaction.
- **F2 Recoverable bytes.** The removed content survives anywhere in the output
  file: an orphan, a shared object, metadata, `/ActualText`, a thumbnail, an
  annotation dragged in by a back-reference, and so on.
- **F3 Silent no-op.** The caller expects removal, but nothing matched, so
  nothing is removed and no error is returned. Examples: `RedactText` on an
  Identity-H font, or a match split across TJ elements.
- **F4 DoS.** A crafted input makes the redaction walk or verification run
  unbounded work or memory.

**Out of scope (to be documented in the Pro docs disclaimer).**

- Visual–semantic mismatch. A font whose glyph for `X` draws `S`, or a lying
  `/ToUnicode`. `RedactText` matches extracted text. `RedactArea` is the
  geometric guarantee.
- Text that is only pixels inside an image. This needs OCR plus image
  redaction (§6.10).
- The width of a redacted span. The black box and the positions of the text
  after it reveal it inherently (§6.8).

## 3. Guiding principles (invariants)

- **I1 Over-approximate removal.** When unsure whether content is inside a
  region, remove it. We may over-remove. We must never under-remove.
- **I2 Fail closed on model disagreement.** If our reading of a stream could
  differ from any mainstream viewer's, the call fails with
  `ErrRedactionUnsupported` (§9 P1). We do not guess.
- **I3 Every route or refusal.** Every route by which a content stream can be
  painted (§6.3) is walked. A route we cannot rewrite yet is a refusal, not a
  skip.
- **I4 Bounded.** Every walk, decode and verification has a cap. Hitting a cap
  is a refusal (I2), never a silent skip. This is the opposite of the renderer,
  which skips silently.
- **I5 The output is self-checked.** `RedactAndFlatten` never returns bytes that
  fail verification (§5.6).
- **I6 The source is never mutated.** Shared objects are copied on write.
  Unmarked pages keep their originals.
- **I7 Byte-exact splice.** Content we keep is preserved verbatim. Only removed
  spans and rewritten operands change.

## 4. Current state (HEAD 446eaea)

What works today:

- `ScanContentSpansStrict` gives a lossless token and operand check (Annex A
  arity).
- The sticky `redactErr` plan.
- `RedactAndFlatten` refuses unreadable marked pages.
- `CheckFormXObjects` refuses text drawn through forms.
- Original `/Annots` are dropped on marked pages (`parse/copy.go`, the
  `DropAnnots` branch).
- Orphaned original `/Contents` are blanked (`blankOrphanedContent`).
- `BuildDocument` writes a fresh catalog, so `/Outlines`, `/AcroForm`, `/Names`
  (including EmbeddedFiles), `/StructTreeRoot`, catalog `/Metadata` and
  `/OCProperties` are not carried over. `/Info` carries only
  Title/Author/Subject (`buildInfoDict`).

Open leaks. Items 1–9 are from the brief and were confirmed with Ghostscript.
Items N1–N7 are new findings from reading the code. Each N-item needs a repro
before it is fixed (§12.1).

| # | Leak | Class | Where |
|---|---|---|---|
| 1 | Inline image ends at the first whitespace-delimited `EI`; viewers that use the exact data length, or accept `EI` with no leading whitespace, disagree | F1 | `parse/content.go` `skipInlineImage` |
| 2 | `CheckFormXObjects` uses `Dict.GetName("Subtype")`, which does not resolve an indirect `/Subtype`. It also does not fall back to enclosing or page resources when a form's own `/Resources` lacks a `Do` name | F1 | `docops/redact/xobject.go` |
| 3 | Routes not walked: ExtGState `/SMask /G`, tiling patterns, Type 3 `/CharProcs` | F1 | — |
| 4 | Each `Do` re-decodes and re-scans: up to 4096 visits × 256 MiB | F4 | `xobject.go` walk |
| 5 | `intersectsAny` uses strict inequalities, so zero-area runs (`0 Tf`, `0 Tz`, singular `Tm`) match `RedactText` but are never removed | F1/F2 | `redact.go:138` |
| 6 | The run box ignores `Tc`/`Tw` and real widths. The pen advance (0.5 em) is an *under*-estimate, so later runs in the same `BT` are placed too far left | F1 | `redact.go:182–189` |
| 7 | `/ActualText`, `/Alt` and `/E` in `BDC` property lists (inline or via `/Properties`) survive | F2 | — |
| 8 | Partially covered runs: the whole operator is removed (safe). Glyph-level removal is not designed | UX | — |
| 9 | Refusals surface as `ErrInvalidPDF` for files every viewer opens | API | `core/redact.go` `redactionScanError` |
| N1 | `RedactText` matches Latin-1 raw bytes, not `/ToUnicode` text, so Identity-H fonts (Word, Chrome, LaTeX) never match. Each TJ string element is matched separately, so `[(SEC) -20 (RET-1234)] TJ` never matches | F3 | `redact.go` `scanRuns`/`FindTextRects` |
| N2 | Copier back-references: an annotation on an unmarked page with `/P` or `/Dest` pointing to a marked page drags in the *original* page dict. Through its `/Parent` that brings the whole original page tree, with the marked page's original `/Annots` and `/Resources`. Widget `/Parent`→field→`/Kids` can drag in the marked page's widgets (`/V`, `/AP`) | F2 | `parse/copy.go` `copyValue` (no special case for page refs) |
| N3 | The marked page dict is copied except `/Parent`, `/StructParents` and `/B`. `/Thumb` (§12.3.4, a picture of the unredacted page), `/PieceInfo`, page `/Metadata` and `/VP` survive | F2 | `parse/copy.go` `CopyPage` |
| N4 | Unused resources on the marked page (e.g. a form in `/XObject` never `Do`-ed but holding the text) are copied verbatim | F2 | copier closure |
| N5 | A token straddling two `/Contents` streams: `PageContent` inserts `\n`, so `(SECRET-12` + `34) Tj` scans as `SECRET-12\n34` and the regex misses it. A viewer that concatenates without a separator shows `SECRET-1234` | F1 | `parse/content.go` `PageContent` |
| N6 | `gs` with ExtGState `/Font [/F size]` (§8.4.5, Table 58) changes font and size, but the scanner tracks only `Tf`, so boxes are wrong | F1 | `scanRuns` |
| N7 | The mandatory gate is weak: public `ExtractText` reads only the page's own content stream, not forms, patterns or Type 3, so "ExtractText lacks the secret" does not prove removal | test gap | `extract/text.go` |

## 5. Architecture

```
            ┌──────────────────────────────────────────────────────────┐
 page ───▶  │ A. Content model   parse.ScanContentStrict (segments, II, │
            │                    operand types)  → spans | loss        │
            ├──────────────────────────────────────────────────────────┤
            │ B. Resource walker  redact/walk: units (page, form,       │
            │    pattern cell, SMask group, Type3 glyph proc) with      │
            │    candidate scopes, inherited gstate, CTMs; memo+budget  │
            ├──────────────────────────────────────────────────────────┤
            │ C. Text model       per unit: glyph runs (codes, Unicode, │
            │    boxes as intervals), marked-content property lists     │
            ├──────────────────────────────────────────────────────────┤
            │ D. Planner          RedactText/AutoRedactPII/RedactArea → │
            │    plan entries {rects, span marks (unit, span, glyphs)}  │
            ├──────────────────────────────────────────────────────────┤
            │ E. Rewriter         per unit: splice spans / rewrite TJ / │
            │    strip property keys; COW path to page-private          │
            │    /Resources; resource GC; page-dict allow-list          │
            ├──────────────────────────────────────────────────────────┤
            │ F. Builder          parse.BuildDocument (+ replacement    │
            │    resources, denied back-references)                     │
            ├──────────────────────────────────────────────────────────┤
            │ G. Verifier         re-open output; deep re-scan marked   │
            │    pages; needle search in owned/orphan objects + /Info   │
            └──────────────────────────────────────────────────────────┘
```

### 5.1 Stage A: content model (`core/parse`, Core)

`ScanContentSpansStrict` becomes the single entry point for every stream B
visits, not only page content. Phase 1 adds:

- **Segments.** A new `ScanContentStrict(segments [][]byte, opts StrictOptions)`
  (internal-facing, exported from `parse`). It scans the concatenation and
  reports a loss if any single lexical token (string, name, number, keyword,
  hex string, comment) contains a segment boundary. §7.8.2 allows a split only
  at token boundaries. A split *between* operands and their operator stays legal.
  `PageContent` gains a sibling `PageContentSegments` that returns the decoded
  streams without joining them (N5).
- **Inline image boundary.** See §6.1. `StrictOptions.InlineColorSpace` is a
  callback that resolves a named inline-image colour space to its component
  count through the unit's resource scope. If it is nil or cannot resolve the
  name, the result is a loss.
- **Operand types for resource-name operators.** `Do`, `gs`, `sh`, `cs` and
  `CS` take a Name. `Tf` takes a Name and a number. `BMC` and `MP` take a Name.
  `BDC` and `DP` take a Name plus a Dict or Name. Today `operandsFit` checks
  only the count, so `(Fm1) Do` passes.

### 5.2 Stage B: resource walker (`core/docops/redact/walk`, Pro)

A content **unit** is one content-bearing stream as painted in one resource
context. It records:

```
unit {
  kind:       page | form | patternCell | smaskGroup | type3Glyph
  ref:        source object reference (or page id / synthetic for inline)
  scopes:     candidate resource scopes, innermost first (§6.2)
  gsIn:       inherited graphics + text state at invocation
  placements: list of CTMs (user→page) at which it is painted on this page
  spans:      strict spans (memoized by ref)
  children:   (spanIndex → child unit) for Do / gs / scn / Tf routes
}
```

The walker interprets the operators that change state (`q Q cm`, the text-state
operators, `gs`, `cs CS scn SCN`, `Tf`, `BT ET`, the text-positioning and
text-showing operators) and the operators that invoke content (`Do`, `gs`,
`scn`/`SCN` under a Pattern colour space, `sh` is ignored because shadings carry
no content stream, and text-showing operators under a Type 3 font). This
mirrors how `core/render` resolves forms (`render/form.go execForm`), but it
covers routes the renderer does not implement (patterns, soft masks, Type 3),
and it never skips silently.

### 5.3 Stage C: text model (`core/internal/fontmodel`, Core, plus redact)

A shared font model, extracted from `core/extract/font.go`:

- Code segmentation: 1 byte for simple fonts; codespace ranges for Type 0
  (§9.7.6.2).
- Unicode mapping: `/ToUnicode` per §9.10.2, otherwise Latin-1 for simple fonts.
- Advance widths: `/FirstChar`, `/Widths` and `/MissingWidth` for simple fonts
  (§9.6.2.1); `/W` and `/DW` for CID fonts (§9.7.4.3); Type 3 `/Widths` ×
  `/FontMatrix`.
- `/FontBBox` (§9.8.1) and `WMode`.

`extract` and `redact` both use it. Then `RedactText` and `ExtractText` see the
same Unicode (N1). The verifier's deep extraction (N7) uses it too.

### 5.4 Stage D: planner

A plan entry is `{page, rects []Rect, marks []SpanMark}`.

- A `SpanMark` is `{unitPath, spanStart, spanEnd, glyphFrom, glyphTo}`.
- `unitPath` is the chain of `(ref, invocation span)` from the page down to the
  unit. It is deterministic because the document is immutable and the walk is
  deterministic.
- `RedactText` records both the match rects (returned to the caller unchanged)
  and the exact span marks. `RedactArea` records only rects.
- Plan entries stay internal. The public `[]Rect` API does not change (item 5).

### 5.5 Stage E: rewriter and copy-on-write

For each marked page:

1. For every unit reachable from the page, collect the spans to remove:
   - span marks;
   - runs whose box intersects any rect under any placement (closed intervals,
     §6.5);
   - property-list keys to strip (§6.7);
   - inline images or XObjects to remove (§6.10, per decision D5).
2. Rewrite each touched unit's bytes by splice (I7). A TJ that is only partly
   removed is re-emitted (§6.8).
3. **COW path.** A rewritten stream gets a fresh object. Every dictionary on the
   path from the page's `/Resources` to it is cloned page-privately: the
   resources dict, its `/XObject`, `/Pattern`, `/ExtGState` or `/Font` subdict,
   then `/SMask` and `/CharProcs`, then the nested form's `/Resources`, and so
   on. Unchanged leaves stay References, so the copier still deduplicates them.
   One copy is made per (marked page, `ref`, scope chain). When one form is
   painted twice on a page, the removals of both placements are united (I1).
4. **Resource GC (N4).** Resource entries on the marked page that the walk never
   invoked are dropped from the page-private resources.
   - Exception: names reachable through a fallback scope (§6.2) count as
     invoked.
   - An entry that cannot be dropped, because something else references the same
     dict, is not a problem: the page-private clone simply omits it.
5. **Page-dict allow-list (N3).** The marked page keeps only `/Type`,
   `/MediaBox`, `/CropBox`, `/BleedBox`, `/TrimBox`, `/ArtBox`, `/Rotate`,
   `/UserUnit`, `/Group`, `/Resources` (rewritten) and `/Contents` (rewritten).
   Everything else is dropped, including `/Annots` (as today), `/Thumb`,
   `/Metadata`, `/PieceInfo`, `/VP`, `/AA`, `/Trans`, `/SeparationInfo` and
   `/Tabs`.

Builder changes (`parse/copy.go`, Core):

- **`PageSource.ReplaceResources parse.Dict`.** A page-private resource tree. In
  it, a *direct* `*parse.Stream` value is added as a new indirect object
  (today `copyValue` returns a `*Stream` in direct position, which is not
  serializable).
- **`BuildOptions.PageRefs`.** References to any source **page object or
  page-tree node** reached during a copy are remapped to the new page ref, or to
  `null` when that page is not in the output. The copier never follows them.
  This fixes N2 for redaction and for Merge/Split (decision D11).
- **`PageSource.Redacted bool`.** Applies the allow-list above. It supersedes
  `DropAnnots`, which stays for `form.go`.

### 5.6 Stage G: post-redaction verification (mandatory, inside `RedactAndFlatten`)

**Needles.** These are collected while rewriting:

- (a) the Unicode text of every removed run, NFC-normalized, whitespace
  collapsed;
- (b) the raw code bytes of every removed string operand;
- (c) every `RedactText` pattern (the regexp itself);
- (d) the values of stripped `/ActualText`, `/Alt` and `/E` entries.

Needles of type (a), (b) or (d) shorter than `minNeedle` = 4 runes or bytes are
not byte-searched, because short needles produce false positives. Patterns (c)
are always re-run on the marked pages. Two needle sets are used:

- **The owned/orphan search** uses every needle ≥ 4.
- **The `R_u` and non-page search** that drives D4 extension uses only the
  *propagating* needles of §6.11: match needles with a skeleton ≥ 4, area
  needles with a skeleton ≥ 8, both compared on the skeleton. Otherwise a
  non-propagating needle would trigger an extension the thresholds were meant
  to prevent.

**Checks on the re-opened output** (`OpenBytes(built)`):

1. **Deep re-scan of each marked page.** Run A–C (strict) on the output page.
   - It must succeed with no loss. We produced the bytes, so a loss is our bug.
   - No glyph run may intersect a planned rect (closed intervals). The black
     cover rectangles are paths, not runs.
   - No unit's text (glyph runs plus surviving property-list text) may match any
     pattern (c) used on that page.
2. **Object classification.**
   - `R_u` = objects reachable from unmarked pages.
   - `R_m` = objects reachable from marked pages.
   - *owned* = `R_m \ R_u`.
   - *orphan* = all objects minus (`R_u ∪ R_m ∪ {catalog, pages tree, /Info}`).
3. **Needle search.** Search every owned and orphan object, plus `/Info`.
   - Strings are decoded as PDF text strings (§7.9.2.2: PDFDocEncoding, or
     UTF-16BE with a BOM).
   - Streams are decoded through the supported filters. Content-like streams
     (forms, patterns, char procs, page contents) are additionally
     lenient-scanned, and the operand strings are decoded through the font
     model.
   - Image-sample streams (`DCT`, `JPX`, `JBIG2`, `CCITT`, or an image
     `/Subtype`) are excluded.
   - Any hit is a verification failure. `RedactAndFlatten` returns no document
     and an error matching `ErrRedactionVerification` (§9 P3).
4. **Residuals drive automatic extension (D4, §6.11).** Hits outside the owned
   and orphan sets are not ignored:
   - hits in `R_u` (pages, annotations or fields the plan did not reach);
   - hits in carried non-page places.

   Each one is attributed to a page occurrence or a non-page location, the plan
   is extended, and the build is redone. This loops to a fixed point with a
   round cap. Hits that cannot be attributed, and residuals that remain after
   the cap, are `ErrRedactionVerification`. Owned and orphan hits are always
   `ErrRedactionVerification` immediately, because they are our bug and are not
   retried.

Verification runs on both build targets. It is pure Go and makes no network
calls, so it is privacy-path safe. Its cost is capped by its own budget (§7).

**Differential oracle (tests only, §12.3).** Ghostscript `txtwrite` on the
output. CI must have `gs` (decision D10). Locally, the oracle subtests skip
when it is absent.

## 6. Algorithms

### 6.1 Inline images (item 1) — §8.9.7, Tables 92–93; §8.9.5 (sample layout); §7.4

The ISO rule is that the `ID` operator is followed by a single white-space
byte, then the data, then `EI`. The spec does not define how a reader finds
the data's end. Viewers either compute it (exact length or filter EOD) or search
for `EI`. Our rule: compute every plausible end. **If they do not all agree,
the result is a loss.**

**Step 1. Parse the dictionary** with the strict token scanner, from `BI` to
`ID`. Expand the abbreviations (Table 93: `/W /H /BPC /CS /IM /F /DP /D /I`) and
normalize abbreviated colour spaces and filters (`/G /RGB /CMYK /I`, `/AHx /A85
/LZW /Fl /RL /CCF /DCT`). A repeated key, or a value of the wrong type for a key
we need, is a loss.

**Step 2. Compute the exact end `E`** from the first filter in `/F` (the
outermost filter determines the extent of the encoded bytes):

| First filter | End `E` | Phase |
|---|---|---|
| none | `dataStart + H × ceil(W × BPC × n / 8)` (rows byte-aligned, §8.9.5.2). `n` comes from `/CS`: G=1, RGB=3, CMYK=4, I (Indexed)=1, or a named entry in `/ColorSpace` resolved through the scope (`ICCBased` → `/N`; `Indexed` → 1; `Separation` → 1; `DeviceN` → array length). `/IM true` ⇒ n=1, BPC=1. Overflow-checked int64; `E > len` is a loss | 1 |
| `AHx` | first `>` (EOD, §7.4.2). A byte that is neither hex nor white-space before it is a loss | 1 |
| `A85` | first `~>` (EOD, §7.4.3). An invalid byte is a loss | 1 |
| `Fl` | decode with a `bytes.Reader` handed directly to `compress/zlib` / `compress/flate` (it implements `io.ByteReader`, so the decompressor does not read ahead). `E = dataStart + consumed`. The stream must reach a real end of stream (zlib Adler-32 checked, or a final deflate block). Truncation or an error is a loss. **Do not** use the lenient `DecodeStream` | 1 |
| `DCT` | JPEG marker walk (no pixel decode): length-prefixed segments until SOS, then entropy data where `FF00` and `FFD0–D7` are not markers, repeated over several SOS for progressive JPEGs, until EOI `FFD9`. `E` is just after EOI. Anything malformed is a loss | 1 |
| `LZW` | EOD code 257 (§7.4.4). Needs a consuming decoder | 2 (loss in 1) |
| `RL` | EOD byte 128 (§7.4.5) | 2 (loss in 1) |
| `CCF`, other, unknown | — | loss |

Every computed size is checked against the remaining bytes before use (no
allocation from untrusted `W × H`).

**Step 3. Terminator agreement.** All of the following must hold:

- (T1) `data[E:]` is `ws* "EI"` followed by white-space, a delimiter, or end of
  stream.
- (T2) The byte immediately before that `E` of `EI` is white-space. This can
  be the last data byte or a separator. Without it, a reader that searches for a
  white-space-preceded `EI` runs past the end, as in repro `ii_tight_EI` and the
  `/AHx` variant `ID 41>EI`.
- (T3) No other `EI` that some reader could accept lies in
  `[dataStart, E)`. This catches repro `ii_early_swallow`. Which `EI`
  occurrences count is decision **D1**:
  - **D1-A, the reviewer's strict rule.** No `"EI"` byte pair at all inside the
    data. Simple and certainly safe. AHx data can never contain `I`, so it
    never trips. Unfiltered or A85 binary data trips with probability ≈
    `len/65536`, about 6% for a 4 KB image (§8.9.7 recommends at most 4 KB
    inline).
  - **D1-B, delimited only.** Forbid `EI` that is preceded by white-space (or
    `dataStart`) and followed by white-space, a delimiter or the end. The refusal
    rate is close to zero. It is safe only if no mainstream reader accepts an
    undelimited `EI` before the computed end, which the oracle tests must show.
  - **Decided (2026-10-15): D1-A.** Measure refusals on the corpus. Relax to
    D1-B only with differential evidence (pdf.js, PDFium, Preview and gs repros
    in the leak corpus) and a new maintainer decision.
- (T4) If the dictionary also carries a length entry (`/L` or `/Length`), it
  must equal `E − dataStart`. ISO 32000-2 defines one; verify Table 91 before
  relying on it. ISO 32000-1 does not.

**Considered and rejected:** "remove the ambiguous region". When readers
disagree about where the image ends, the later candidates are unbounded (the
next `EI` anywhere in the stream), so no finite span is safe to cut. Deleting
or rewriting an inline image only helps once its extent is unambiguous. That is
§6.10 (images under regions) and output normalization (Phase 2: convert each
surviving inline image on a marked page to an Image XObject with an explicit
`/Length`, so the output no longer depends on any `EI` heuristic).

### 6.2 Resolver-aware, viewer-compatible resource lookup (item 2) — §7.3.5, §7.7.3.4, §7.8.3, §8.8, §8.10.1

- **Resolve everything.** `/Resources`, each subdictionary (`/XObject`,
  `/Font`, …), each entry, and keys such as `/Subtype`, `/PatternType`, `/S` and
  `/G` are resolved through `Resolver.Resolve` (indirect chain) before they are
  tested. `Dict.GetName` alone is never used on untrusted dictionaries in this
  package. Add `parse.(*Resolver).ResolveName(obj) (Name, bool, error)`.
- **Names.** Operand names and dictionary keys are compared after §7.3.5 `#xx`
  decoding. The lexer already decodes `#xx`; repro `form_name_escape` pins it.
- **XObject classification.** Use the resolved `/Subtype`:

  | Resolved `/Subtype` | Treated as |
  |---|---|
  | `/Form` | form |
  | `/Image` | image |
  | `/PS` | §8.8.2 says readers shall ignore it. Phase 1 keeps refusing it (decision D8) |
  | any other Name | refusal |
  | missing, non-Name, or unresolvable | ambiguous: walked as a form if it has `/BBox`, or has neither `/Width` nor `/Height`; an image otherwise. When walked as a form, a strict-scan loss is a refusal |

  Repros `form_indirect_subtype` and `form_indirect_subtype_noWH` pin this.
- **Candidate scopes (fallback).** ISO says a form without `/Resources`
  inherits the caller's (§8.10.1). Ghostscript also falls back to the page
  resources when the form's own `/Resources` lacks a name (repro
  `form_res_fallback`). A name used in unit *u* is therefore resolved in *every*
  scope of `u.scopes`, innermost first:
  - the unit's own `/Resources`;
  - the enclosing units' scopes, outward;
  - the page's effective `/Resources`;
  - each `/Pages` ancestor's `/Resources`, because some readers merge the
    inherited and own dicts rather than replacing them (§7.7.3.4 says
    "replace").

  **Every distinct object any scope binds the name to is a child to walk** (I1).
  For Type 3 the order is the font's `/Resources`, then the page's (§9.6.5,
  `/Resources` entry of Table 112). The rewriter handles each candidate through
  COW (§5.5).
- **Inherited resources.** `pageInfos[i].Resources` already resolves `/Pages`
  inheritance. Repros `form_pages_inherited` and `form_indirect_res` pin it.

### 6.3 Content-bearing routes (item 3)

| Route | Trigger | Child unit | Space mapping | Phase 1 | Phase 2 |
|---|---|---|---|---|---|
| Form XObject (§8.10.1) | `Do` | form content, own `/Resources` + fallbacks, inherits gstate including text state | `/Matrix` × CTM at `Do` | refuse if it shows text | rewrite (COW) |
| Transparency group form used as a soft mask (§11.6.5.2, Table 144 `/G`; §11.6.6) | `gs` whose ExtGState `/SMask` is a dict with `/G` | group form | group `/Matrix` × CTM at the `gs` | refuse if text | rewrite (COW ExtGState → SMask dict → `/G`) |
| Tiling pattern (§8.7.3, Table 75; Pattern colour space §8.6.6.2) | `scn`/`SCN` with a name while the colour space is `/Pattern` or `[/Pattern base]` (directly or via a `/ColorSpace` resource). **Also every PatternType 1 entry in scope (reachability), not only those selected** | pattern cell, own `/Resources` (required) | pattern `/Matrix` × the *default* space of the parent stream (page default space, or form space at `Do`) — not the CTM at `scn` | refuse if text | rewrite: remove matched spans; if the painted area's bbox meets a rect, remove all text runs in the cell (tiling makes per-tile geometry not worth it) |
| Shading pattern / `sh` (§8.7.4) | — | no content stream | — | ignore | ignore |
| Type 3 glyph procedure (§9.6.5) | any text-showing op while the current font is Type 3 (set by `Tf` *or* by `gs` `/Font`, N6) | `/CharProcs` entry for each code shown; font `/Resources` then page | `/FontMatrix` × Trm per glyph | refuse if any char proc of a used Type 3 font shows text | rewrite char procs whose text matches a needle or whose glyph instance meets a rect (COW font → `/CharProcs`) |
| Annotation appearance (§12.5.5) | page `/Annots` | `/AP` `/N /R /D` streams | `/Rect` ∘ `/Matrix` (§12.5.5) | dropped on marked pages (confirmed: `copy.go` `DropAnnots` branch) | unchanged; option to flatten-then-redact (decision, see §13 D9) |
| Image XObject `/SMask` or `/Mask` (§8.9.5.4, §11.6.5.3) | `Do` image | image data | — | no text; see §6.10 | §6.10 |
| Marked content (§14.6) | `BDC`/`DP` with a property list | not a unit; carries text (§6.7) | — | see §6.7 | strip/match |
| Optional content (§8.11) | `/OC` on `BDC`, or on an XObject or annotation | — | — | **treated as visible** (I1): hidden content is walked and removed like visible content | same |
| Reference XObject (§8.10.4) | form `/Ref` | external file | — | ignored (we draw the form's own content; the target file is not ours) | same |

**Text-state inheritance.** A form, pattern cell or soft-mask group inherits
the graphics state at invocation, including the text state (font, size, `Tc`,
`Tw`, `Tz`, `TL`, `Ts`, `Tr`). The walker passes it in as `gsIn`.

"Shows text" in Phase 1 means the unit or any descendant has a text-showing
operator (`Tj`, `TJ`, `'`, `"`) or a `BDC`/`DP` property list carrying
`/ActualText`, `/Alt` or `/E`.

### 6.4 Memoization and the decode budget (item 4)

- **Decode + strict-scan cache.** Key: source object number. Value:
  `(spans, loss)`. Each stream is decoded and scanned once per
  `RedactText`/`RedactAndFlatten` call.
- **Walk cache.** Key: `(ref, scopeChainKey, gsInKey)`.
  - `scopeChainKey` is the sequence of object numbers that own the scopes, so it
    is comparable without hashing dictionaries.
  - `gsInKey` is the inherited font object number plus the numeric text-state
    fields.
  - Value: the unit's runs in unit space, its children, and its loss.
  - A cache hit only transforms the cached runs by the new placement CTM. That
    costs O(runs), charged to the op budget.
- **Budgets per page call, all refusals (I4).** `budget_exceeded` reason:

  | Cap | Value | Rationale |
  |---|---|---|
  | `maxRedactDecodedBytes` | 512 MiB, charged on each cache *miss* before scanning | same figure as render-budget's planned `maxContentBytes` (2 × `maxDecoded`); independent constant here |
  | `maxRedactOpVisits` | 64 Mi operator visits, including cache-hit replays and Type 3 glyph-procedure invocations | bounds fan-out such as 4096 forms × a large cached unit |
  | `maxUnitDepth` | 12 | matches `render/form.go` |
  | `maxUnitInvocations` | 4096 forms + groups + patterns, plus 1 Mi Type 3 glyph executions | separate counters, so glyph-heavy Type 3 pages do not exhaust the form budget |
  | `maxPlacementsPerUnit` | 4096 | above this, the unit's runs are treated as intersecting every rect whose bbox meets the union of placements (I1, no refusal) |

- **Cross-call reuse.** `AutoRedactPII` calls `RedactText` once per kind and
  page. Phase 2 caches the per-page text model (C output) on the `Document`.
  The document is immutable, so there is no invalidation. The cache is bounded
  to 128 MiB retained, with LRU eviction.

### 6.5 Matched runs removed by span; closed intervals (item 5)

- **Span marks.** `RedactText` records the span mark of each match (§5.4).
  `RedactAndFlatten` removes span marks **unconditionally**, whatever the
  geometry. A `0 Tf` run that matched is removed even though its box has no area.
- **Closed intervals.** Area intersection uses closed intervals:
  `r.minX ≤ rc.X+rc.W && r.maxX ≥ rc.X && …`.
- **Degenerate boxes.** A box that collapses to a point or a segment intersects
  when it touches the region.
- **Non-finite boxes.** A box with NaN or ±Inf in any coordinate (from an
  overflowing `Tm`/`cm` chain) **intersects every rect on its page**.
- **Region normalization.** Region rects are normalized first (negative
  width/height flipped). A non-finite rect is rejected at `RedactArea` with
  `ErrInvalidArgument`. That is a public behaviour change (P6).

### 6.6 Text boxes: `Tc`, `Tw` and real widths (item 6) — §9.4.4, §9.3, §9.6.2.1, §9.7.4.3, §9.8.1

Per glyph *i* with code *c*, ISO §9.4.4 gives the displacement
`tx = ((w0 − Tj/1000) × Tfs + Tc + Tw·[c is single-byte 32]) × Th`, where `Th`
is `Tz/100`. `Tw` applies only to the single-byte code 32 (§9.3.3).

- **Pen as an interval `[lo, hi]` in text space.** When `w0` is known (from
  `/Widths`, `/W`/`/DW`, or Type 3 widths × FontMatrix), `lo = hi`. When it is
  unknown, `w0 ∈ [0, 1.1 em]`. Negative `Tc` or TJ adjustments move both ends.
  Phase 2 can add standard-14 metrics from the freely distributed Adobe AFM
  data to make more widths exact.
- **Glyph box.** x from `lo_i + min(0, bbox.llx)` to
  `hi_i + max(w0_hi, bbox.urx)`; y from `min(bbox.lly, −0.25) + Ts` to
  `max(bbox.ury, 0.9) + Ts`. Here `bbox` is `/FontBBox` (glyph units ÷ 1000, or
  × FontMatrix for Type 3). If it is missing or degenerate, use the em defaults.
- **Run box.** The union of its glyph boxes, mapped by `Trm` (§9.4.2) and then
  the placement CTM. Mapping uses all four corners.
- **Pen after the run.** It keeps the interval, so later runs in the same `BT`
  are over-approximated rather than placed too far left (the root cause of
  repros `tc_spread` and `wide_glyphs`).
- **Long lines.** The interval grows along a line with unknown widths. That
  over-removes, which is accepted under I1.
- **`WMode` 1 (vertical, §9.7.4.3 `/W2` `/DW2`).** Phase 1 uses the union of the
  horizontal and vertical estimates. Phase 2 uses exact vertical metrics.
- **`"` operator.** It sets `Tw` and `Tc` (§9.4.3) before the show. The current
  scanner ignores both.
- **Phase 1 stopgap (recommended, small).** Add `|Tc| + |Tw|` per glyph to
  `bboxCharWidth`, raise the per-glyph bound to 1.1 em, and advance the pen by
  the same upper bound plus a lower bound of 0. This closes `tc_spread` and
  `wide_glyphs` before the font model lands.

### 6.7 Hidden marked-content text and document-level text (item 7) — §14.6.2, §14.9.3–5, §14.3, §12.3.3, §12.5, §12.7, §7.11.4

**Property lists.** For each `BDC` or `DP`, the property list is inline or a
`/Properties` resource (resolved through the candidate scopes).

- **Matching.** `/ActualText`, `/Alt` and `/E` are decoded as text strings
  (§7.9.2.2), then:
  - **Text matching.** For `RedactText`, if the pattern matches the property
    text, mark the whole `BDC…EMC` sequence: every run inside it becomes a span
    mark, and the property key is stripped. Repros `actualtext` and
    `actualtext_props` show the visible glyphs `XXXXXXXXXXX` while
    copy/paste yields the secret.
  - **Area matching.** If any run inside the sequence is removed, or its box
    meets a rect, strip all three keys from that property list.
- **Rewrite.**
  - Inline dict: re-emit the `BDC` operand dict without the keys. This is a
    byte splice of the `BDC` span with a serialized dict; the other keys are
    preserved.
  - Named entry: COW a page-private `/Properties` entry.
- **Phase 1 (decided, D7).** On a marked page, strip `/ActualText`, `/Alt` and
  `/E` from every property list:
  - inline `BDC`/`DP` dicts in the page content are re-emitted without the keys;
  - named `/Properties` entries get page-private clones.

  This also applies to pages that become marked through automatic extension
  (§6.11). It over-removes accessibility text only. Forms that show text are
  refused in Phase 1 anyway; a form that carries only these keys counts as
  "shows text" (§6.3) and is refused too. The stripped values become needles
  (§5.6 (d)).

**Document- and object-level text.** Policy per location:

| Location | Can hold the string? | Current output | Policy |
|---|---|---|---|
| `/Info` Title/Author/Subject | yes | carried (`buildInfoDict`) | Phase 1: drop any field that contains a propagating needle (§6.11) or matches a `RedactText` pattern. Phase 2: `RedactOptions.Metadata` (P5) |
| `/Info` Keywords/Creator/custom, catalog XMP `/Metadata` (§14.3) | yes | not carried (fresh catalog) | keep dropping; verifier checks `/Info` |
| Page `/Metadata`, `/PieceInfo`, `/Thumb` (§12.3.4) | yes; `/Thumb` is a picture | **carried (N3)** | allow-list (§5.5), Phase 1 |
| Form, image or font `/Metadata` streams inside a rewritten unit | yes | carried | Phase 2: drop `/Metadata` and `/PieceInfo` on every COW'd object; verifier catches owned hits in Phase 1 |
| Outlines (§12.3.3) | yes | not carried | — |
| Annotations on marked pages: `/Contents`, `/RC`, `/T`, `/AP`, popups | yes | dropped | — |
| Annotations on unmarked pages | yes | carried | D4: an annotation whose text or appearance contains a needle is removed (§6.11). N2 back-references fixed by `PageRefs` |
| AcroForm field values `/V`, `/TU` (§12.7) | yes | catalog not carried, but widgets on unmarked pages drag in `/Parent` fields (N2) | Phase 1: `PageRefs` plus a deny on field `/Kids` entries that are widgets of marked pages. D4: a widget whose field values or appearance contain a needle is removed (§6.11) |
| Embedded files: catalog `/Names` (§7.11.4) | yes | not carried | — |
| Embedded files: FileAttachment annots on unmarked pages | yes, possibly inside nested formats we cannot search | carried | D4: removed on every page whenever the plan is non-empty (§6.11) |
| Structure tree `/Alt`, `/ActualText` (§14.9) | yes | not carried (`/StructParents` stripped, fresh catalog) | — |

### 6.8 Partially covered runs (item 8) — §9.4.3, §9.4.4

- **Current (keep as the fallback).** Remove the whole operator. For `TJ`, the
  whole array goes when any element meets a rect.
- **Phase 2: glyph-level removal.**
  - **Segmentation.** Segment each shown string into codes with the font model.
    If segmentation is not certain (Type 0 without a usable codespace, or a
    hostile CMap), fall back to whole-operator removal.
  - **Removal.** Remove the codes whose glyph box meets a rect, or which lie in
    a span mark's `[glyphFrom, glyphTo)`.
  - **Re-emit as one `TJ`.** For example `[(kept) -N (kept)] TJ`. The removed
    run of glyphs, **including every kerning number inside it**, becomes a
    single adjustment `−N`. `N` is the removed displacement (using the interval
    `hi` when widths are unknown), in thousandths of `Tfs × Th`. Text after the
    removal keeps its position, so the layout does not reflow under the black
    box.
  - **Quote operators.** `'` and `"` keep their line move. They are rewritten as
    `T*` (and `Tw`/`Tc` for `"`) followed by `TJ`.
- **Width leak (threat A2, Bland et al.).**
  - Keeping later glyphs in place reveals the removed span's total width. The
    original layout and the black box already reveal that, and we accept it.
  - What we must not leave is *per-glyph* information: individual kerning
    numbers or per-glyph `Td` moves inside the removed span. They narrow a
    dictionary attack. Collapsing them into one adjustment is required.
  - Optional hardening (decision D6): round `N` to whole points.
- **Default granularity** (decision D6):
  - glyph-level for `RedactText` (the user asked for exactly the match);
  - glyph-level for `RedactArea`, keeping glyphs whose whole box is outside;
  - operator-level available via `RedactOptions.Granularity` (P5).

### 6.9 Error surface (item 9)

See P1 and P3. `redactionScanError` stops wrapping `ErrInvalidPDF` for
viewer-readable files that are only too complex to redact safely.

### 6.10 Non-text content under regions (new; not in items 1–9)

The package comment says vector and image content inside a region "is covered
by the black rectangle but not stripped". **For images that is a paint-over**,
the exact failure class SECURITY.md §1 forbids. On a scanned page, `RedactArea`
leaves the scan bytes intact. Outlined text (glyphs converted to paths) is the
same for vectors.

Options (decision **D5**):

- **(a) Remove whole.** Remove every image `Do`, inline image and `sh` whose
  placed bbox meets a rect. Path objects (construction ops through the painting
  op) are removed whole when their bbox meets a rect.
  - Safe and simple.
  - Destructive: a full-page scan or background rect disappears entirely.
- **(b) Pixel-level image redaction.** Decode (the supported decoders from
  `render/image.go`, moved to a shared internal package), set the covered
  samples to the fill, and re-encode as Flate. An `/SMask` gets the same
  treatment. Undecodable images fall back to (a).
  - Paths are split by clipping only for display, so they still need (a) or a
    refusal.
- **(c) Keep the paint-over and document it.** Unsafe. Not recommended.
- **Decided (2026-10-15).** Option (a) in Phase 1 for images *and* paths. A
  full-page scan under any region is blanked, and this must be stated in the
  `RedactArea` docs. Option (b) for images in Phase 2. Paths stay at (a).

**Phase 1 rules for (a).** All geometry is closed-interval and
over-approximated (I1):

- **Image XObject `Do` / inline image `BI…EI`.** The placed bbox is the unit
  square × CTM. If it meets a rect, the operator span (or the whole `BI…EI`)
  is removed. The XObject then becomes unused, and resource GC drops it with
  its `/SMask` and `/Mask`.
- **Form XObject without text, whose placed `/BBox` × `/Matrix` × CTM meets a
  rect.** The whole `Do` is removed. Forms *with* text on a marked page are
  refused in Phase 1, as before.
- **Painted path object.** This is the span from the first construction
  operator (`m`, `re`) through its painting operator (`S s f F f* B B* b b*`).
  The bbox is the control-point hull × CTM, widened by
  `lineWidth × max(1, miterLimit)` (scaled by the CTM) for stroking operators.
  If it meets a rect:
  - a pure painting op is removed with its construction operators;
  - a path that also clips (`W`/`W*` before the painting op) keeps its clip:
    the painting op is replaced by `n`.
  - Clip-only paths (`… W n`) are kept.
- **`sh`.** It is bounded by a conservative clip bbox: the intersection of the
  `W` path bboxes in effect. When no clip is known, the clip is the whole
  page. If that bbox meets a rect, the `sh` is removed.
- **Type 3 glyphs that paint paths or images.** These are covered by the text
  rules: the glyph run is removed.

### 6.11 Automatic plan extension (D4, decided 2026-10-15)

**Goal.** Text the caller asked us to remove must not survive anywhere else in
the output: other pages, annotations, field values, `/Info` or attachments.
When we find it, we extend the redaction to it rather than only reporting it.
The semantics below keep that from turning into surprising over-removal.

**Needles that propagate.** These are fixed once, from the caller's plan only.

- **Match needles (`source = "text-match"`).** Every substring matched by
  `RedactText` or `AutoRedactPII` on the page where it was asked for. The
  needle is the matched text, not the pattern: re-applying a page-scoped
  pattern to other pages would remove values the caller never saw.
  `AutoRedactPII` already runs on every page, so for it extension only adds
  non-page locations and occurrences its per-page matching could not see.
  - A needle propagates only if its skeleton (below) has **≥ 4** characters.
    Shorter matches (e.g. a pattern `\d`) are removed on the requested page and
    nowhere else.
- **Area needles (`source = "area-text"`).** Recommended: yes, but narrowly.
  The text of each glyph run removed *only because it lay under a
  `RedactArea` rect* propagates if its skeleton has **≥ 8** characters.
  - In Phase 1 removal is whole-operator, so the needle is the whole removed
    run's text. That can include glyphs outside the rect, so some neighbouring
    words may also be redacted elsewhere. This is accepted under I1 and
    reported (P8).
  - In Phase 2, with glyph-level removal (§6.8), the needle is only the glyphs
    inside the rect.
  - Area needles are whole removed strings, not split into words. Redacting a
    line "Patient: Jane Q. Doe" propagates that string, not "Jane".
- **Not needles.** Text removed by the extension itself. This is what
  guarantees termination (below). Also excluded: text removed as collateral of
  D5 image/path removal (it has none), and stripped property-list values (they
  are checked for, §5.6 (d), but do not propagate).

**Skeleton (normalization for matching).**

1. NFKC (folds ligatures such as U+FB01 to "fi", and compatibility forms).
   This uses `golang.org/x/text/unicode/norm`, already in `go.mod` as an
   indirect dependency (BSD-3). It becomes a direct dependency of `core`.
2. Remove all white space and default-ignorable code points (soft hyphen,
   ZWSP, ZWJ, BOM). Extraction spacing is heuristic (`tjSpaceThreshold`), so
   white space must not decide a match.
3. Keep case: matching is **case-sensitive**. Case-insensitive matching would
   over-remove; the pattern author controls case via the regexp.

A match is an exact substring match of the needle skeleton in the haystack
skeleton. An index map from skeleton positions back to (unit, span, glyph)
turns each hit into span marks and rects. The same text model and font model
serve `RedactText` (§5.3), so pre-pass and verification see identical text.

**Where occurrences are looked for, and what happens.**

| Location | Action | Phase 1 limits |
|---|---|---|
| Page content of any page (marked or not, inside or outside the original rects), including forms, patterns, soft-mask groups and Type 3 as walked by B | add span marks + rects (black boxes drawn) to that page's plan. The page becomes marked: D7 strip, page-dict allow-list, resource GC and D5 all apply | an occurrence inside a unit Phase 1 cannot rewrite (form, pattern, soft mask or Type 3 text) is `ErrRedactionUnsupported` with that unit's reason; fail closed |
| `/ActualText`, `/Alt`, `/E` on any page | strip the key (page becomes marked) | as above for forms |
| Annotation on any page whose `/Contents`, `/T`, `/Subj`, `/RC`, `/NM` or appearance streams (`/AP /N /R /D`, deep-scanned) contain a needle | remove the annotation, its `/Popup`, and any annotation whose `/IRT` points to it, from that page's `/Annots`. The page is **not** otherwise marked | — |
| Widget annotation whose field (through `/Parent` chain, resolved) `/V`, `/DV`, `/TU` or `/Opt`, or the widget's `/AP`, contains a needle | remove the widget | — |
| FileAttachment annotations (`/FS` → `/EF`) | **removed on every page whenever the plan is non-empty**, needle or not: content inside nested formats (PDF, DOCX, ZIP) cannot be verified | none |
| `/Info` Title/Author/Subject | remove the field | — |
| Outlines, catalog XMP, structure tree, `/Names`, `/AcroForm`, `/OCProperties` | not carried by `BuildDocument` (fresh catalog). The verifier asserts their absence, so a future change that carries them fails closed until this table covers them | — |
| Anything else: a hit in an object of an unmarked page's closure that no glyph run or listed location explains (e.g. an unused resource, `/PieceInfo`) | apply page hygiene to that page (allow-list + resource GC + D7) and re-verify. If it is still there, `ErrRedactionVerification` | — |

**Algorithm (fixed point with a round cap).**

```
N        := needles(userPlan)             // fixed; computed during planning,
                                          // before any build
plan     := userPlan ∪ prepass(N)         // all pages' text models, annots,
                                          // fields, /Info, attachments policy
for round := 0; round <= maxExtendRounds; round++ {   // maxExtendRounds = 2
    out := build(plan)
    v   := verify(out, N, plan)
    if v.ownedOrOrphanHit || v.rescanFailed   { return ErrRedactionVerification }   // bug
    if v.residuals == ∅                       { return out, report(plan) }
    ext, ok := attribute(v.residuals)         // table above
    if !ok                                    { return ErrRedactionVerification }   // unexplained
    if ext.needsUnsupportedRewrite            { return ErrRedactionUnsupported }    // Phase-1 limit
    plan = plan ∪ ext
}
return ErrRedactionVerification (reason "not_converged")
```

Termination: `N` is fixed, and extension removals never add needles. Each round
removes every attributed occurrence of a fixed, finite set. So the set of
remaining occurrences strictly shrinks or the loop exits with an error. The
pre-pass normally finds everything. Round 1 exists for occurrences only the
byte-level search sees, and round 2 confirms. A run that needs more rounds
indicates a bug, so it fails closed.

**State and reporting.** The source document's plan (`d.redactions`) is never
modified (I6). Extensions live only inside the `RedactAndFlatten` call and are
reported through P8. Reports never contain document text: they carry pages,
rects, locations and counts only, so they are safe to log, and the cloud API
can return them.

**Budget.** The pre-pass costs one text-model pass per page, as
`AutoRedactPII` does, under the per-page budgets of §6.4 and a document-wide
2 GiB decoded cap shared with verification. It is skipped when `N` is empty.
The maintainer's 50% target covers verification and pre-pass together. If
corpus benchmarks exceed it, the pre-pass reuses the per-page text-model cache
(Phase 2, §6.4), pulled forward if needed.

## 7. Performance and budget summary

- Typical text-only page:
  - one strict scan + one walk (no units) + one verification re-scan: about 3×
    the cost of a strict scan;
  - plus the `OpenBytes` of the output and one decode of each owned stream.
- Worst case per page:
  - `maxRedactDecodedBytes` (512 MiB) + `maxRedactOpVisits`;
  - verification has its own 512 MiB decoded and 64 Mi op allowance, plus a
    document-wide cap of 2 GiB decoded for the needle search over owned and
    orphan objects.
  - Exceeding any of these is a refusal (`budget_exceeded`).
- Memory: the decode cache holds decoded bytes for the duration of one page call,
  at most `maxRedactDecodedBytes`.
- WASM: the same caps apply. A 512 MiB page allowance is high for a
  4 GiB address space. Phase 2 may lower the WASM default (decision D12),
  consistent with render-budget's per-target defaults.
- Benchmarks to add (`testing/benchmark`): `RedactText` on a Word-like page with
  Identity-H and kerned TJ; `RedactAndFlatten` on 100 pages with a shared
  header form, comparing Phase 1 refusal against the Phase 2 COW rewrite;
  verification overhead (target < 50% of `RedactAndFlatten`).

## 8. Tier, packages, WASM

| Package | Tier | Change |
|---|---|---|
| `core/parse` | Core (MIT) | strict scanner (segments, inline images, operand types), `ResolveName`, consuming Flate/AHx/A85 and a JPEG marker walker, copier `ReplaceResources` / `PageRefs` / `Redacted` |
| `core/internal/fontmodel` (new) | Core (MIT) | v1 (Phase 1, D12): code segmentation, ToUnicode. v2 (Phase 2): widths, FontBBox, WMode, Type 3. Used by `extract` and `redact`. Skeleton normalization uses `golang.org/x/text/unicode/norm` (BSD-3; already an indirect dependency, now direct) |
| `core/extract` | Core (MIT) | switch to `fontmodel`. Phase 2: optional deep extraction (forms, Type 3), shared with the verifier. The public `ExtractText` keeps page-only behaviour unless decision D13 says otherwise |
| `core/docops/redact` (+ `walk/`) | Pro | walker, text model, planner, rewriter, verifier |
| `core/redact.go`, `core/pii.go` | Pro surface in `core` | plan with span marks, sentinels, options/report |
| `cloud/api` | Cloud | map `ErrRedactionUnsupported` to HTTP 422 `redaction_unsupported` (today it is a 500 "redaction failed", `handlers.go` `redactPII`) |
| `mcp`, `cmd/fluxdocs` | — | surface the reason code |

Core must not import Pro: `fontmodel` and `parse` stay free of `docops`
imports.

All of this is pure Go with no CGO and no build tag. Redaction is not exported
from `wasm/` today. When it is, the whole pipeline, including verification,
runs in the browser with no filesystem or network access (SECURITY.md §3). The
Ghostscript oracle is test-only and native-only.

## 9. Public API proposals

Status per item: **approved** (2026-10-15), or **proposed** (P2 for Phase 1;
P4/P5 for Phase 2, decide later).

- **P1, `core.ErrRedactionUnsupported`: approved.** `errors.New("fluxdocs:
  content cannot be redacted safely")`. Returned (wrapped) by `RedactText`,
  `AutoRedactPII` and `RedactAndFlatten` for loss, unsupported routes, budget
  exhaustion and Phase-1-unsupported extension targets (§6.11).
  - Concrete type `*core.RedactionError{Page int; Reason RedactionReason;
    Detail string; Err error}`.
  - `RedactionReason` values: `"content_ambiguous"`, `"inline_image"`,
    `"form_text"`, `"pattern_text"`, `"softmask_text"`, `"type3_text"`,
    `"budget_exceeded"`, `"unsupported_xobject"`.
  - `Error()` never includes document bytes.
  - `redact.ErrFormXObjectText` stays reachable through `Unwrap` for one
    release, then is deleted.
  - **Compatibility (D2, approved option A).** `ErrInvalidPDF` is no longer in
    the redaction error chain. It stays for files that are genuinely broken
    (e.g. a stream that will not decode). Callers to update in the same PR:
    `cloud/api/handlers.go` (`redactPII` → 422 `redaction_unsupported`),
    `mcp/tools_pro.go`, `cmd/fluxdocs`, `testing/security/redaction/*`.
- **P2, the `RedactArea` contract: proposed for Phase 1.** Rects are normalized
  (negative size is flipped). A NaN or Inf rect returns `ErrInvalidArgument`
  and is not added (today it is silently accepted). This is a behaviour change.
  The doc comment must also state that images and paths under a region are
  removed whole (D5).
- **P3, `core.ErrRedactionVerification`: approved.** `"fluxdocs: redaction
  verification failed"`. The output would still contain removed content, or
  automatic extension did not converge (§6.11).
  - It wraps `ErrInternal`, so existing "report a bug" handling applies.
  - No document is returned.
  - Verification is always on. There is **no** `SkipVerify` option, now or in
    `RedactOptions`.
- **P4 (Phase 2), proposed.** `RedactAndFlattenWithOptions(opts RedactOptions)
  (*Document, *RedactionReport, error)`. `RedactAndFlatten()` is equivalent to
  the defaults. It returns the same `RedactionReport` as P8, extended with
  `Pages []PageReport` (`PageReport{Page int; RemovedRuns, RemovedImages,
  RemovedPaths, StrippedProperties int}`).
- **P5, `RedactOptions` (Phase 2), proposed:**
  - `Granularity` (`Glyph` | `Operator`), default per D6
  - `Metadata` (`ScrubMatches` | `DropAll`)
  - `Images` (`RemoveWhole` | `Pixel`)

  `FailOnResiduals` is dropped: D4 made extension the behaviour. There is
  deliberately no option to disable extension or verification.
- **P8, minimal `RedactionReport` in Phase 1: approved (2026-10-15, access
  option (a)).** D4 can change
  more than the caller asked for, so Phase 1 must tell the caller without an
  error and without logging. Proposed shape (text-free by construction):

  ```go
  type RedactionReport struct {
      Extensions         []RedactionExtension // added by D4 (§6.11)
      RemovedAnnotations []RemovedAnnotation  // D4 + attachments policy
      RemovedInfoFields  []string             // e.g. "Title"
  }
  type RedactionExtension struct {
      Page   int
      Rects  []Rect
      Source string // "text-match" | "area-text"
      Where  string // "content" | "marked-content-property"
  }
  type RemovedAnnotation struct {
      Page    int
      Subtype string // e.g. "Text", "Widget", "FileAttachment"
      Reason  string // "needle" | "attachment-policy"
  }
  ```

  Access options:
  - **(a), recommended:** `func (d *Document) RedactionReport()
    (RedactionReport, bool)` on the `*Document` returned by `RedactAndFlatten`.
    `ok` is false for any document not produced by redaction. No signature
    change, and P4 later returns the same value.
  - **(b):** pull P4 `RedactAndFlattenWithOptions` forward into Phase 1, with an
    empty `RedactOptions`.

  Surfacing: the cloud `redactPII` response adds `"auto_extended": <count>`;
  the MCP `redact` tool and the CLI print a one-line summary (counts and page
  numbers, no text).
- **P9, `RedactText` scope: approved (2026-10-15; behaviour change from D4).** A
  string matched on page *p* is now also removed wherever it occurs elsewhere
  in the document (skeleton ≥ 4 characters, §6.11). Text under a `RedactArea`
  rect (skeleton ≥ 8) propagates the same way. Both doc comments, spec §7.4,
  and the Pro docs must say so.
- **P6, documentation of `RedactText` (Phase 1 per D12).** Matching is on
  extracted Unicode (`/ToUnicode`), across the elements of a TJ (Phase 1), and
  (Phase 2) across consecutive
  text-showing operators on the same line. Visual–semantic mismatch is out of
  scope (§2).
- **P7, `parse` API.** This is Core and lower stakes, but importable:
  `ScanContentStrict(segments, StrictOptions)`,
  `(*Resolver).PageContentSegments`, `(*Resolver).ResolveName`,
  `PageSource.ReplaceResources`, `PageSource.Redacted`, `BuildOptions.PageRefs`.
  `ScanContentSpansStrict(data)` stays as a one-segment wrapper.

## 10. Spec and SECURITY.md changes to apply (not applied by this note)

`docs/spec.md` has uncommitted render-budget edits, so these are listed here
for whoever lands Phase 1:

- **§7.4.** Document:
  - P1, P3 and the P2 behaviour;
  - D5 (images and paths under a region are removed whole);
  - D4 automatic extension and the P9 scope change;
  - the P8 report accessor;
  - that `RedactText` matches extracted Unicode across TJ elements.

  Add the Phase 2 P4/P5 signatures once decided.
- **§10.2.** Add `ErrRedactionUnsupported` and `ErrRedactionVerification` to the
  sentinel list.
- **§13.4.** Replace the pseudocode with the A–G pipeline (§5):
  - strict model;
  - walker over every route;
  - span marks + closed-interval geometry;
  - COW rewrite;
  - page-dict allow-list;
  - verification.
  Replace `removeMatchingAnnotations` with "drop all annotations on marked pages;
  strip property-list text; deny back-references".
- **§18.1 (redaction risk).** Add mitigations: the verification stage, the leak
  corpus with a differential oracle, and fail-closed on model disagreement.
- **`.claude/SECURITY.md` §1.**
  - Replace the "Known limitation" sentence with the route table status per
    phase.
  - Add "verification is mandatory and not optional".
  - Add "removed text found elsewhere is removed too (D4); attachments are
    dropped whenever a redaction runs".
  - Add "non-text content under a region is removed (D5)".
  - Add "a refusal is `ErrRedactionUnsupported`".
  - Add the leak-corpus gate to the test gate (§12).

## 11. Phased plan

### Phase 1: fail closed on every known route, plus the decided fixes

**Precondition.** The render-budget feature has landed on `master`. Phase 1
branches from that merge and rebases on its `core/errors.go` (sentinel block)
and `core/document.go` changes. A go-implementer is changing `core/` for
render-budget concurrently, so do not start Phase 1 steps that touch `core/`
before then. Steps 1.0–1.2, which touch only `testing/` and `core/parse`, may
start earlier only if the render-budget branch does not touch those files.
Check the diff first.

| Step | Content | Items / decisions |
|---|---|---|
| 1.0 | Record the HEAD baseline (§12.4). Write repros for N1–N6 and the D4 extension cases. Add the leak-corpus table with every rv7 case (expected to fail or refuse before the fix) | N1–N6, D4 |
| 1.1 | `parse`: inline-image end rule (§6.1: none/AHx/A85/Fl/DCT; LZW/RL/CCF are a loss; T1–T4 with the strict no-`EI`-pair rule), with `StrictOptions.InlineColorSpace` | 1, D1 |
| 1.2 | `parse`: segment-boundary token check (`PageContentSegments`), operand types for resource-name operators | N5, 2 |
| 1.3 | `core/internal/fontmodel` v1: code segmentation and `/ToUnicode` (widths can wait for 2.2). Migrate `extract` to it with no behaviour change, confirmed by the existing `extract` tests | D12, N1 |
| 1.4 | `redact/walk` v1 (check only for text-bearing units): resolved classification, decoded names, candidate scopes, routes Form / SMask group / tiling pattern (selected + reachable) / Type 3 char procs / `gs /Font`; refuse when any shows text; placements recorded for D5; memoized; budgets (§6.4) | 2, 3, 4, N6 |
| 1.5 | Text model and matching: Unicode via the font model, one run per TJ (elements concatenated, glyph index map kept), skeleton normalization (§6.11); span marks; closed intervals; non-finite boxes intersect everything; rect validation | N1, 5, D12, P2, P6 |
| 1.6 | `Tc`/`Tw`/1.1 em stopgap boxes and the interval pen (§6.6, last bullet) | 6 (stopgap) |
| 1.7 | `ErrRedactionUnsupported` + `RedactionError` + `ErrRedactionVerification` (wraps `ErrInternal`) in `core/errors.go`, rebased on render-budget's sentinels. Drop `ErrInvalidPDF` from the redaction chain; keep `redact.ErrFormXObjectText` via `Unwrap`; update the cloud (422), MCP and CLI callers | 9, P1, P3, D2, D3 |
| 1.8 | Builder (`core/parse/copy.go`): `Redacted` page-dict allow-list; `PageRefs` back-reference remap, **also for Merge/Split**; resource GC on marked pages (the page's own resources only; text-bearing forms are refused in Phase 1, so no COW yet) | N2, N3, N4, D11 |
| 1.9 | D7: strip `/ActualText`, `/Alt` and `/E` on every marked page (inline and `/Properties`); collect the stripped values as needles | 7, D7 |
| 1.10 | D5: remove image `Do`, inline images, text-free forms, painted paths and `sh` whose bbox meets a rect (§6.10 Phase 1 rules) | D5 |
| 1.11 | Verifier (§5.6 checks 1–3): needle search, object classification, `/Info` | P3, N7, D3 |
| 1.12 | D4 automatic extension (§6.11): needles from the user plan; pre-pass over all pages, annotations, fields and `/Info`; attachment policy; fixed-point loop with `maxExtendRounds = 2`; minimal report via `(*Document).RedactionReport()` (P8, P9 approved) | D4, P8, P9 |
| 1.13 | Benchmarks for the ≤ 50% verification + pre-pass target. Corpus no-regression (§12.4). Oracle CI job with `FLUXDOCS_REQUIRE_GS=1`. Apply the §10 spec and SECURITY.md changes | D3, D10 |

Exit criteria for Phase 1:

- Every leak-corpus case either produces output that passes all three oracles
  (§12.1) or is refused with `ErrRedactionUnsupported` and the expected reason.
- Every D4 extension case either removes all occurrences and reports them (P8),
  or fails closed with the expected error.
- The corpus no-regression check (§12.4) passes.
- Verification plus pre-pass costs ≤ 50% of `RedactAndFlatten` on the benchmark
  set, or the overrun is reported to the maintainer with numbers before merge.

### Phase 2: rewrite instead of refuse

| Step | Content | Items |
|---|---|---|
| 2.1 | `fontmodel` v2: widths, FontBBox, WMode, Type 3 metrics; deep extraction for forms and Type 3 shared with the verifier | 6, N7 |
| 2.2 | Full width model: interval pen with known widths, FontBBox, WMode | 6 |
| 2.3 | COW rewrite of forms, soft-mask groups, tiling patterns and Type 3 char procs (§5.5, §6.3); `ReplaceResources`. This makes D4 extensions into such units possible, where Phase 1 refuses them | 3, D4 |
| 2.4 | Property-list *matching* (§6.7: a pattern matching `/ActualText` marks the sequence); metadata policy option; COW'd `/Metadata` dropped | 7 |
| 2.5 | Glyph-level partial removal with collapsed adjustments (§6.8); area needles shrink to the glyphs inside the rect | 8, D6, D4 |
| 2.6 | Matching across operators on a line (page-level glyph stream with index → (unit, span, glyph) map) | N1 |
| 2.7 | LZW and RL consuming decoders for inline images; inline image → XObject normalization on marked pages | 1 |
| 2.8 | Pixel-level image redaction (D5 phase 2) | D5 |
| 2.9 | `RedactAndFlattenWithOptions` (P4, P5), returning the P8 report extended with per-page counts | P4, P5 |
| 2.10 | Per-document text-model cache (pulled into Phase 1 if the 1.13 benchmark misses its target) | 4 |

Exit criteria for Phase 2:

- Zero refusals of reason `form_text`, `pattern_text`, `softmask_text` or
  `type3_text` on `testing/corpus/clean` and the macOS list.
- The leak corpus is 100% green with the oracle mandatory in CI.

## 12. Testing strategy

### 12.1 Leak corpus — `testing/security/redaction/leak_corpus_test.go`

There is one table. Every repro from every review round is a row. Start with
all cases in the reviewer's harness (`rv7/main.go`):

- the control case;
- `ii_tight_EI`, `ii_tight_EI_hex`, `ii_early_swallow`;
- `form_indirect_subtype`, `form_indirect_subtype_noWH`, `form_res_fallback`,
  `form_pages_inherited`, `form_indirect_res`, `form_name_escape`;
- `pattern_text`, `smask_text`, `type3_text`;
- `actualtext`, `actualtext_props`;
- `tj_nested`, `bx_known`, `quote_dq`, `quote_sq`, `comment_split`;
- `tc_spread`, `split_string`, `split_keyword`;
- `zero_tf`, `zero_tz`, `degenerate_tm`, `wide_glyphs`.

Add every case from `malformed_content_test.go` and `deep_nesting_test.go`, and
new rows for N1–N6. Fixtures are generated in Go (the
`buildPDF` + object-map pattern already in `redaction_test.go`), not committed
binaries, and contain only synthetic secrets (SECURITY.md §7).

Each row declares:

- the content and objects;
- the operation (`RedactText` with a pattern, a full-page `RedactArea`, or a
  partial-area rect);
- the expectation: `removed`, or `refused(reason)` per phase. The expectation
  flips from refused to removed when Phase 2 lands. A row whose expectation is
  `removed` must never regress to refused without review.

For `removed`, the assertions are:

1. **Our extraction.** `ExtractText` on the output lacks the secret, and so does
   the deep extraction from the verifier. N7: the public `ExtractText` alone is
   not enough.
2. **Bytes and decoded streams.** The verbatim `out.Bytes()` and a re-save lack
   the secret's bytes. Every stream, decoded through Flate/AHx/A85, lacks it. So
   does every string object decoded as a text string (Latin-1, UTF-16BE) or as a
   content operand. This assertion reuses the verifier's needle search
   (exported for tests through an `internal` test helper).
3. **Ghostscript oracle.**
   `gs -q -dNOPAUSE -dBATCH -dSAFER -sDEVICE=txtwrite -sOutputFile=- out.pdf`
   output lacks the secret.
   - Sanity check: gs on the *input* must show the secret. If not, the fixture is
     broken and the test fails.
   - Optionally, non-redacted control text must survive (catches over-removal
     regressions).

For `refused`: `errors.Is(err, core.ErrRedactionUnsupported)`, the reason
matches, the returned document is nil, and the plan is sticky (later
`RedactAndFlatten` calls also fail).

### 12.2 Unit tests (table-driven)

- **`parse`, inline-image boundary.** Every filter row of §6.1, crossed with
  T1–T4 pass and fail cases: exact length with W/H/BPC/n combinations; named
  `/CS` resolved and unresolved; `/IM`; AHx with white-space and with an invalid
  byte; A85 `z` and `~>`; Fl truncated, with trailing junk, and with an
  embedded `EI`; DCT with an EXIF thumbnail (inner `FFD9`), progressive, and
  truncated; LZW/RL/CCF (loss in Phase 1); size overflow.
- **`parse`, segments.** Splits between tokens (allowed), inside a string,
  inside a name, inside a number, inside a keyword, and inside a comment
  (each a loss).
- **`parse`, operand types.** `(x) Do`, `1 gs`, `/F1 /F2 Tf`, and so on.
- **`redact/walk`.** Each route × {direct, indirect, own scope, fallback scope,
  `/Pages`-inherited, `#xx` name} × {text, no text}. Cycles; depth 12/13; budget
  trips (each cap); memo hit rates (a decode counter proves one decode per ref).
- **Geometry.** Closed intervals on edges; zero-area runs; NaN/Inf CTM; `Tc`
  and `Tw` spread; negative `Tc`; TJ adjustments; `Tz` 0; `Ts`; WMode 1;
  unknown widths giving an interval pen.
- **Rewriter (Phase 2).** COW: a form shared by a marked and an unmarked page.
  The unmarked page keeps the original bytes and the marked page gets a copy.
  The same form twice on one page gives the union of removals. Glyph-level TJ
  re-emission keeps the positions of later glyphs (compare against glyph
  positions in the deep model) and leaves no kerning numbers from inside the
  removed span.
- **Verifier.** Seeded leaks it must catch, as `ErrRedactionVerification`: an
  orphan stream with the secret; an owned form; a `/Thumb` stand-in stream; an
  `/ActualText` value that survived; an unexplained hit that survives page
  hygiene.
- **Automatic extension (D4).** One row per §6.11 location and rule:
  - match needle on page 0, also on page 2 in plain text: page 2 gets a rect,
    the secret is gone, and the report lists page 2 `text-match`;
  - same, but on page 2 inside a form: `ErrRedactionUnsupported` `form_text`
    (Phase 1) or removed (Phase 2);
  - match needle of skeleton length 3: not propagated;
  - area needle of skeleton ≥ 8: propagated; < 8: not propagated;
  - case differences: not matched;
  - NFKC ligature `ﬁ` vs `fi`: matched;
  - white-space and soft-hyphen differences: matched;
  - secret in a Text annotation `/Contents` on an unmarked page: the
    annotation, its popup and its replies are removed and reported;
  - secret in a widget `/V`: the widget is removed;
  - any FileAttachment: removed whenever the plan is non-empty;
  - secret in `/Info` Title: the field is removed and reported;
  - a seeded case where the pre-pass is disabled (test hook), proving the
    verification loop extends in round 1 and converges;
  - a seeded non-converging case (test hook that makes removal a no-op):
    `ErrRedactionVerification` `not_converged` after 2 rounds;
  - the source document's plan is unchanged after the call;
  - the report contains no document text (a test asserts that no report
    field contains the needle).

### 12.3 Ghostscript availability

- Detect with `exec.LookPath("gs")`.
- Absent: the oracle assertion calls `t.Skip` for that subtest only, with the
  message "gs not found; oracle skipped". Assertions 1 and 2 still run.
- With `FLUXDOCS_REQUIRE_GS=1` (set in CI), absence is a hard failure.
- CI: the Linux native job installs `ghostscript` from the distro package and
  sets `FLUXDOCS_REQUIRE_GS=1`. The WASM job runs assertions 1 and 2 only.
  `exec` is not available under js/wasm, so the oracle file carries
  `//go:build !js`.
- Licensing note: Ghostscript is AGPL. Running it as a separate process in tests
  links nothing and ships nothing, so the MIT build is unaffected. State this in
  `testing/README.md`.
- Additional oracles (pdftotext/poppler, pdf.js via Node in `testing/e2e`) are
  decision D10.

### 12.4 Corpus no-regression

- **Inputs.** `testing/corpus/**` plus the macOS list
  (`scratchpad/r2/list.txt`, about 300 system PDFs). The list is not committed.
  A `testing/corpus/redaction_regress_test.go` reads `FLUXDOCS_EXTRA_CORPUS`
  (a file of paths) when it is set, and skips when it is not.
- **Operations per page.** `RedactText(page, ".")` (match everything), a
  full-page `RedactArea`, and a centred half-page `RedactArea`. Then
  `RedactAndFlatten`.
- **Metrics.**
  - refusals by reason;
  - verification failures, including `not_converged` (must be **0**, always);
  - output re-open failures (must be 0);
  - full-page area with an output deep extraction that is non-empty (must be 0);
  - D4 extensions per operation (pages, rects, annotations, `/Info` fields),
    recorded so that surprising over-removal is visible in review.

  `RedactText(page, ".")` matches single characters, which are below the
  4-character propagation threshold. So it exercises matching without
  extension, while the full-page area operation exercises extension heavily.
- **Baseline.** Record the HEAD 446eaea refusal counts per reason first.
  - Phase 1 may add refusals only of reasons `pattern_text`, `softmask_text`,
    `type3_text` and `inline_image`, plus `form_text` refusals caused by D4
    extending into a form on another page. Each must be listed with its file in
    the PR description.
  - `content_ambiguous` refusals on `testing/corpus/clean` must be 0.
  - Phase 2 target: 0 refusals of the four rewrite reasons.
- **Gate.** This joins the `testing/security/redaction/` 100% gate. A PR touching
  redaction, `core/parse` content scanning, or the copier must pass both.

### 12.5 Fuzzing

Extend `testing/security/redaction/fuzz_test.go`. The invariant is: for any
input on which a full-page `RedactArea` + `RedactAndFlatten` succeeds, the
deep extraction of the output page is empty and the verifier passes. Any
`ErrInternal` or `ErrRedactionVerification` is a fuzz failure. Seed it with the
leak-corpus fixtures. Add a parse-level fuzz target for the inline-image end
finder (`E` is always in `[dataStart, len]`; no panic; no allocation beyond the
input size).

## 13. Open questions

D1, D2, D3, D4, D5, D7, D11, D12, P8, P9 and the attachments policy were
decided on 2026-10-15; see "Decisions (maintainer-approved)" at the top. They
are not repeated here. **No open item below blocks Phase 1.**

**Not yet decided; the default applies** (implement the default unless the
maintainer says otherwise):

- **D6 (before 2.5).** Default granularity (glyph vs operator). Whether to round
  the collapsed adjustment to whole points (width-leak hardening, Bland et al.).
  *Default:* glyph-level once Phase 2 lands, operator-level as fallback, no
  rounding.
- **D8.** `/Subtype /PS` XObjects: keep refusing, or ignore per §8.8.2?
  *Default:* keep refusing until the corpus shows real files with them.
- **D9.** Output fidelity side effects, already true at HEAD (*default:* accept
  and document):
  - `/OCProperties` is not carried, so optional-content layers that were OFF
    become visible in the output.
  - The structure tree is dropped, so tagged-PDF accessibility is lost.
  - Annotations on marked pages are dropped (links and comments vanish rather
    than being redacted).

  Options: accept and document; carry `/OCProperties`; or offer "flatten
  annotations, then redact" so their appearance is redacted rather than
  dropped.
- **D10.** Is the Ghostscript oracle mandatory in CI? Are pdftotext/poppler
  (GPL, process-only) and pdf.js (Apache-2.0, Node in e2e) also wanted as
  oracles? Avoid MuPDF tools to keep the clean-room story simple. *Default:*
  gs mandatory in the Linux native CI job; no further oracles yet.
- **D13.** Should the public `ExtractText` descend into forms (and Type 3)? This
  is a Core change that improves fidelity and makes the SECURITY.md gate
  meaningful. *Default:* `ExtractText` stays page-only, and the gate wording
  names the deep extraction used by the verifier.
- **WASM decode cap.** Lower `maxRedactDecodedBytes` for js/wasm in Phase 2? It
  should align with render-budget's per-target defaults once that has landed.
  *Default:* same caps on both targets.
- **Measure.** Do readers fall back to page resources for Type 3 `/CharProcs`
  and pattern cells the way gs does for forms? (§6.2 already over-approximates;
  measure for documentation only.)
- **Measure.** Refusal rate of D1-A on the corpus and the macOS list.
- **Measure.** Cost of verification plus the D4 pre-pass on large documents,
  against the approved ≤ 50% target. D4 means the needle search now also covers
  `R_u`, so the cost scales with the document, not only the marked pages. If
  it exceeds the target: cache decoded streams between build and
  verification, skip the pre-pass when there are no propagating needles
  (already the design), and pull the per-document text-model cache (2.10)
  forward.
- **Measure.** How often D4 extension fires on the corpus, and how large it is
  (pages and rects per operation). This is input for revisiting the 4/8
  thresholds.
- **Confirm.** Does ISO 32000-2 define an inline-image length entry (`/L`)? It
  only affects T4.

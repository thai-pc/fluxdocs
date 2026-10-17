# Design: redaction hardening (one model instead of per-leak patches)

Status: design; Phase 1 batches A, B and C landed (cbf7348, 13121d9,
f325e25), batch D (steps 1.12–1.14, the end of Phase 1) in review (review
rounds 1 and 2 applied: C71–C84). Base:
`master` at 446eaea (design). Batch A baselines: 71de860; batch B baselines:
cbf7348; batch C baselines: 13121d9; batch D baselines: f325e25.
Tier: **Pro** for the redaction engine (`core/internal/redact` since step 1.8,
C25; `core/redact.go`, `core/pii.go`). It also touches **Core (MIT)** code in `core/parse` (strict
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
| P8 | **Approved (2026-10-15).** `func (d *Document) RedactionReport() (RedactionReport, bool)` on the `*Document` returned by `RedactAndFlatten`, with no signature change. The cloud `redactPII` response adds `"auto_extended"`; MCP and CLI print a text-free summary | §9 P8 |
| P9 | **Approved (2026-10-15).** Propagation thresholds: 4 for matched strings, 8 for `RedactArea`-removed text. Matching is case-sensitive, after NFKC. `golang.org/x/text` becomes a direct dependency | §6.11, §9 P9 |
| Attachments | **Approved (2026-10-15).** Drop all FileAttachment annotations whenever a redaction runs (non-empty plan), and list them in the report (`Reason "attachment-policy"`) | §6.11 |
| D14 | **Approved (2026-10-16). Property-list matching moves from Phase 2 (old step 2.4) into Phase 1 (step 1.6).** `RedactText`/`AutoRedactPII` also match `/ActualText`, `/Alt` and `/E`, both in inline `BDC`/`DP` dicts and via `/Properties`. A match marks the page, removes the whole `BDC…EMC` sequence's runs, and strips the key. Reason (batch A): D7 strips only on marked pages, so a `RedactText` whose pattern matches only the `/ActualText` left the page unmarked. The `actualtext` repro stayed open, contradicting the Phase 1 exit criterion | §6.7, §11 step 1.6, §12 |
| D15 | **Approved (2026-10-16). Narrow inline-image acceptance in Phase 1.** On pages being redacted, an inline image is accepted only if all of these hold. Anything else is an `inline_image` refusal. Phase 2 may widen this with full decoding. Reason: go-reviewer found 15 batch A leaks where gs rejects or under-reads an inline image and then parses its raw bytes as content (T5, classes B1–B4). The conditions: | §6.1, §11 step 1.1, §12 |
| | • **Filters:** unfiltered, `AHx`, or zlib-headered `Fl`. Refused: DCT, A85, raw DEFLATE, LZW, RL, CCF, JBIG2, JPX, Crypt. | |
| | • **Dictionary:** fully allow-listed. Every key and value is checked; unknown keys are refused; `/DP` predictors are refused. | |
| | • **Colour space:** DeviceGray, DeviceRGB, DeviceCMYK, Indexed over a device base, or a name resolved via `InlineColorSpace` to one of these. Refused: CalGray, CalRGB, Lab, ICCBased, Separation, DeviceN. | |
| | • **Decoded length:** exactly `H × rowBytes`. The whole filter chain is decoded within budget, with nothing left over. | |
| D16 | **Approved (2026-10-16). Defence in depth.** Refuse (`inline_image`) when the image's raw data contains a text-show candidate: the byte pair `Tj` or `TJ`, or `)` or `>`, then optional white-space or comments, then `'` or `"` | §6.1 T6 |
| D6, D8, D9, D10, D13 | **Not yet decided; the default applies.** D6: glyph-level granularity once Phase 2 lands, operator-level as fallback, no rounding. D8: keep refusing `/PS` XObjects. D9: accept and document the fidelity side effects. D10: Ghostscript oracle mandatory in CI, no further oracles yet. D13: public `ExtractText` stays page-only; the gate wording names the deep extraction | §13 |

**No maintainer decision blocks Phase 1 any longer.** The remaining open items
have defaults that apply:
- D6, D8, D9, D10, D13;
- the WASM cap;
- the lenient-scanner question Q1 (§13).

The measurements in §13 do not gate any step. P2 (normalize `RedactArea`
rects; reject NaN/Inf with `ErrInvalidArgument`) is not formally decided; its
default is to implement it as proposed in step 1.5.

## Design corrections from batch A implementation (2026-10-16)

The batch A implementer found places where this design was wrong or said
nothing. Each correction is applied in the section named. Batch A's baselines
were recorded at **71de860** (HEAD at the time), not 446eaea.

| # | Correction | Applied in |
|---|---|---|
| C1 | The gs sanity check ("gs on the input shows the secret") cannot apply to every row. gs `txtwrite` shows nothing for: a string or keyword split across streams, `0 Tf`, `0 Tz`, a singular `Tm`, tiling-pattern text, `/ActualText`, strings nested inside a TJ array, and some over-deep TJ fixtures. Rows get a per-row `gsBlind` flag. It is allowed only on rows whose expectation is `clean` (output must pass the oracles) | §12.1 |
| C2 | N5 premise refined. gs does not show a string split across streams; it appears to tokenize each stream separately. But gs *does* end a comment at a stream boundary: in `split_comment`, the text after a comment that runs to the end of stream 1 is shown from stream 2. Readers therefore disagree three ways: join with no separator, join with white-space (us), or per-stream tokenization. Refusing a token or comment that straddles a boundary remains correct | §4 N5, §5.1 |
| C3 | API shape. `ScanContentStrict(segments [][]byte, …)` cannot report offsets into the joined content that redaction splices. `PageContent` appends `\n` after *every* stream of an array, but adds nothing for a single stream. Implemented instead: `parse.ContentSegments{Data, Joins}`, where `Data` is byte-identical to `PageContent`'s output and `Joins` are the offsets of the inserted separators; plus `(*Resolver).PageContentSegments` | §5.1, §9 P7 |
| C4 | T1 tightened: after `EI`, require white-space or end of content. A delimiter is not enough | §6.1 |
| C5 | CR LF after `ID`: data may start after the CR or after the CR LF. Both starts are plausible and must agree. Unfiltered, Flate and DCT refuse after CR LF (their ends differ by one). AHx and A85 ignore white-space, so they are accepted unless a length entry (`/L`) is present | §6.1 |
| C6 | `tj_nested` (a string inside a nested array inside a TJ array) had no step. It is assigned to 1.5: the text model reads strings at any nesting depth inside a TJ operand, for both matching and removal | §11 step 1.5 |
| C7 | `StrictOptions.InlineColorSpace` is not yet wired into redaction, which scans without options. Until the 1.4 walker passes resource scopes, inline images with a *named* colour space refuse as `inline_image` | §5.1, §11 step 1.4 |
| C8 | Until `RedactionError` exists (step 1.8), reason codes are classified from the error chain plus the loss text. That classifier is duplicated in the leak corpus and the regression harness, and collapses into `RedactionError.Reason` in 1.8 | §11 step 1.8, §12 |
| C9 | Corpus coverage. `scratchpad/r2/list.txt` is mostly icons: 13 inline images and 21 stream joins in 2,598 pages, all accepted. A text-heavy corpus is needed to judge the D1-A refusal rate | §12.4, §13 |
| C10 | Suggestion, **not decided**: the *lenient* scanner (render, extract) should also use the exact inline-image end where it can be computed. In `ii_tight_EI`, gs renders text that our renderer and extractor hide | §13 Q1, Phase 2 candidates |
| C11 | N5 needed minimal wiring in 1.2, not later: batch A wired `core/redact.go` `redactionContent` to use `PageContentSegments` + `ScanContentStrict` when the page has joins | §11 step 1.2 |

## Design corrections from batch B implementation (2026-10-16)

Batch B (steps 1.3–1.7) and its go-reviewer round found these. Baselines were
recorded at **cbf7348**. Measurements: 3,100 pages (the 735-file macOS list
plus 23 generated text-heavy files) strict-scan the same at cbf7348 and after
C12–C14; the regression outcomes and the extract/render hashes are identical.

| # | Correction | Applied in |
|---|---|---|
| C12 | **Numeric operand types.** Strict scanning requires numbers for every operator whose operands are numbers (`cm`, `Tm`, `Td`, `TD`, `Tc`, `Tw`, `Tz`, `TL`, `Ts`, `Tr`, `w`, `J`, `j`, `M`, `i`, `d0`, `d1`, path construction, `g G rg RG k K`). Ghostscript ignores an operator with a wrong-type operand; reading it as 0 placed text elsewhere (`(x) 0 0 1 200 100 Tm`). A mismatch is `content_ambiguous` | §5.1 |
| C13 | **Text-object rules (ISO 32000-1 Figure 9).** Strict scanning refuses `q`, `Q`, `cm` and `BT` inside BT…ET, and the text-showing and text-positioning operators outside it (`content_ambiguous`). Ghostscript saves and restores the text matrices with `q`/`Q`, ignores `cm` until ET, and starts text shown outside a text object from the identity matrix; our model did none of these | §5.1 |
| C14 | **Font candidates are capped.** When scopes bind a `Tf`/`gs` name differently, the text model keeps every font a reader may select (readings and boxes cover each). More than 8 candidates is `budget_exceeded`, not a collapse to "no font", which would make `RedactText` silently miss text (F3). Building the readings is charged to `maxRedactOpVisits` | §6.2, §6.4 |
| C15 | **Readings.** A run matches if any reading matches: per candidate font the `/ToUnicode` text as `ExtractText` reads it (strings at any TJ depth), the `/Differences` glyph-name reading, and for simple fonts a one-byte-per-code reading (`/ToUnicode` lookup of the single byte, else glyph name, else Latin-1); extraction's own reading with TJ word gaps; and the raw bytes as Latin-1 (the behaviour before 1.5). A simple font whose `/ToUnicode` declares two-byte codes otherwise read as nothing | §5.3, §6.11 |
| C16 | **NFKC and the skeleton move out of 1.5 into 1.13.** Matching in 1.5 does not need them; `golang.org/x/text` is not yet a direct dependency. Glyph-name ligatures (`/fi`) read as their letters in the `/Differences` reading; a `/ToUnicode` mapping to U+FB01 still needs NFKC (`lig_fi`) | §6.11, §11 steps 1.5, 1.13 |
| C17 | **Match rects are display only (S1).** `RedactText` rects are the em vertical extent around the run's pen positions and are only painted; matched runs are removed by their span marks. Only `RedactArea` rects are removal regions, intersected with boxes that use `/FontBBox` too. Using the `/FontBBox` match box as a region removed the lines above and below | §5.4, §5.5 |
| C18 | **Quote operators.** Removing `'` or `"` keeps their side effects: `T*`, and `aw Tw ac Tc T*` for `"` (operand bytes copied), so later text keeps its line | §6.8 |
| C19 | **Widths (1.7 stopgap).** fontmodel v1 already carries the §8 "v2" metrics as over-approximations: `/Widths`, `/W`, `/DW`, `/FontBBox`, the Type 3 `/FontMatrix`, WMode. A Type 0 font whose CIDs are unknown (non-Identity encoding) advances by the hull of 0, 1.1 em, `/DW` and every `/W`; a simple font with an embedded program and no `/Widths`, a Type 0 font whose `/W` cannot be read to its end, and a Type 3 font without `/FontMatrix`/`/FontBBox` are unbounded (their runs meet every region). Type 3 advances also cover each glyph procedure's `d0`/`d1` width, which Ghostscript uses instead of `/Widths` | §5.3, §6.6, §8 |
| C20 | **`PageSource.ReplaceResources` is pulled forward from 2.3 into 1.6**: stripping a named `/Properties` entry needs a page-private resource copy, and the original must not be written. A matched entry still reachable through another reference (an alias, a form's resources) survives until resource GC (1.9) and the verifier (1.12): rows `actualtext_alias`, `actualtext_shared_form` | §5.5, §6.7 |
| C21 | **Inline colour-space scopes.** `InlineColorSpace` resolves a name only in the unit's innermost scope; a name only an outer scope defines refuses (readers disagree on the fallback). An abbreviation (G, RGB, CMYK) defined anywhere refuses; a family name (`I`, `Indexed`, …) never resolves | §5.1, §6.1 |
| C22 | **Non-finite match rects.** A matched run whose box overflowed is returned (and painted) as the page's MediaBox, never as NaN/Inf | §6.5 |
| C23 | **Walker.** It lives in `core/docops/redact` (`walk.go`), one interpreter with the page's text model. A memoized revisit costs one operator visit (units are check-only in Phase 1). Tiling patterns are reached from the page's scopes and each unit's *own* `/Resources`. A refusal names the innermost unit that shows text. An untyped XObject is a form unless it has `/Width`, `/Height` and no `/BBox` | §5.2, §6.2–6.4 |
| C24 | **Interim refusal type.** Until 1.8, refusals are `*redact.RefusalError{Reason, Detail}` with a typed `redact.Reason`; `form_text` and `unsupported_xobject` match `redact.ErrFormXObjectText`. Decision D14 strips only the matched key; stripping on area redaction is D7 (1.10) | §9 P1, §11 step 1.8 |

## Design corrections from batch C implementation (2026-10-16)

Batch C (steps 1.8–1.11, plus batch B review follow-ups S-1, S-2 and N-2).
Baselines were recorded at **13121d9**. On the 735-file macOS list plus the 24
generated text-heavy files (759 files, 3,198 redaction outcomes, 3,100 pages),
the redaction outcomes are identical to 13121d9 (no new refusal of any
reason), and the render and extract hashes are byte-identical.

| # | Correction | Applied in |
|---|---|---|
| C25 | **Engine location and error API.** The engine is `core/internal/redact`; only `core` imports it. Refusals are an internal `*redact.Refusal{Reason, Detail, Err}`, which `core` reports as `*core.RedactionError{Page, Reason, Detail, Err}` (P1); `Unwrap() []error` gives `ErrRedactionUnsupported` and `Err`. `Err` is `ErrFormXObjectText` for `form_text` and `unsupported_xobject`, and the strict scanner's loss for `content_ambiguous` and `inline_image`. Reason constants are `core.Redaction*` (`RedactionFormText`, …). `core/docops/redact` is a deprecated shim that only re-exports `ErrFormXObjectText` for one release. `RedactContent`, `FindTextRects` and `CheckFormXObjects` had no callers outside the engine and are test helpers now | §9 P1, §11 step 1.8 |
| C26 | **Error text never holds document bytes, and the reason comes from the error, not its text.** A strict-scan loss message no longer appends the lexer error, which can quote content (`[ (A) SECRET-1234 ] TJ` was reported as `unexpected keyword "SECRET-1234"`, and callers print and log redaction errors); an unsupported inline-image filter is no longer named. `parse.ErrInlineImageLoss` marks inline-image losses (same rule as the C8 classifier: a loss described as "inline image …", so the corpus outcomes did not move). Page content past the decode limit is `budget_exceeded`; content that does not decode or resolve stays `ErrInvalidPDF` (D2: genuinely broken). The cloud maps a refusal to HTTP 422 `{"error": "redaction_unsupported", "reason", "page"}`; MCP, the CLI and the examples print the reason code | §6.9, §8 |
| C27 | **Operator budget and memory (S-1).** `maxRedactOpVisits` is **8 Mi**, the renderer's default operator budget (was 64 Mi). `StrictOptions.MaxOps` stops a strict scan before it materializes more operators than the budget can pay for, at two visits per operator (one to scan, one to interpret), so at most 4 Mi spans per page. Measured: the largest page of the 3,100-page corpus needs 0.56 Mi visits (35 k operators), 14× headroom. Perf probe (one page, `RedactText`/`RedactArea`+flatten, native): 4 M `(x)Tj` took 9.3 s and 2.97 GB RSS before (review), 1.6 s and 0.96 GB now; 17 M `(x)Tj` (97 MB content) 1.5 s and 1.3 GB, against 8.6 s and 4.3 GB for `RenderPage` of the same page at its default budget; 17 M `n` 1.0 s and 0.9 GB (render 3.3 s, 3.3 GB). Cost is now bounded by the budget, not the content (`TestDoS_RedactOperatorFloodIsBounded`: doubling the operators past the cap does not grow the allocation). Rewrite indexes runs and lists by span (a removal bitset, no per-span maps); a run's reading buffers are reused and deduplicated by comparison | §6.4, §7 |
| C28 | **Inline images are spans.** On a strict scan, each accepted inline image is a span with `Op "BI"` (through `EI`) whose operand is its dictionary, so D5 can remove it whole and resource GC can keep its named colour space. The lenient scan is unchanged | §5.1, §6.10 |
| C29 | **`PageRefs` is the copier's default, not a `BuildOptions` field** (there is no `BuildOptions`). Every build — `SaveTo`, Merge, Split, watermark, flatten, form flatten, PDF/A and redaction — maps a reference to a source page in the build to that page's copy, and any other page or page-tree node to null; the original page is never copied through a back-reference. Page identity is an unexported object number in `PageInfo` (it survives `Rotate`'s page-dict clone). A page's number is reserved only when a reference reaches it before it is copied, so documents without such references keep their object numbering. **Effect on Merge/Split:** a split-off page whose link pointed at another page used to carry that page's content, annotations and the whole original page tree (`TestSplit_PageReferencesDoNotDragInPages`, which fails at 13121d9); now the destination is null. Outputs of `SaveTo`/Split/Merge are not byte-deterministic even at 13121d9 (object numbers follow map order), so the 2,478 corpus outputs were compared as canonical object graphs: all identical (no corpus file has a page back-reference in a copied closure) | §5.5, D11 |
| C30 | **`PageSource.Redacted`.** The allow-list of §5.5 is applied before the copy, so dropped entries are never written (the old `DropAnnots` deleted `/Annots` after copying, leaving the annotation objects as orphans; form flattening still uses it — follow-up). The page's annotations and their popups are denied for the whole build: null in a dictionary, dropped from an array (a widget field's `/Kids`). With `ReplaceContent`, the original `/Contents` is not copied for the page; a copy another route makes is still blanked if no page uses it | §5.5, N2, N3 |
| C31 | **Resource GC (N4).** Each page operator's lookups are recorded, including those of the units it invokes (memoized walks keep and replay their names; walks reached only by tiling-pattern reachability count only when `scn` selects them). `Do`, `BI` and `sh` keep theirs apart, so D5 removal drops their resources. `cs`/`CS` names are recorded (the walker never looked them up). Kept: used names of the seven Table 33 categories, `DefaultGray/RGB/CMYK`, a `/ProcSet` array of names. Dropped: everything else, including unknown resource keys | §5.5 |
| C32 | **D7 lives in `Rewrite`.** Every property list on a rewritten (marked) page loses `/ActualText`, `/Alt` and `/E`; a D14 strip mark is now only checked to name a list of the page. The stripped values are returned for the verifier (§5.6 (d), step 1.12) | §6.7 |
| C33 | **D5 details.** Removal regions are `RedactArea` rects only (C17: `RedactText` rects are display boxes). Stroke widening is `max(w, 1) × max(1, M)` in user space. A path whose CTM changes during construction, and a form whose `/BBox` or `/Matrix` cannot be read, meet every region. `sh` is bounded by the intersection of the `W`/`W*` path hulls in effect, else meets every region; text clipping modes are ignored (the clip only shrinks, so this over-approximates). A painting operator with no path paints nothing and is kept | §6.10 |
| C34 | **Rows.** `actualtext_shared_form` stays open: the text-free form is painted, so its `/Resources` (sharing the `/Properties` dictionary) are kept; it closes in 1.12 (owned hit) or 2.3 (COW). N-2: a match split across operators in one text object (`Tj`, `TJ`, per character) → 2.6; a `/Properties` dictionary shared with an unredacted page (used there or not) → 1.13, since GC applies to marked pages only | §12.1 |
| C35 | **S-2.** The deadline halves of `mcp/render_budget_test.go` and `examples/go-server-demo/main_test.go` use a deadline that has already passed (1 ns), so the render's first poll reports it whatever the host speed; the 10 s bound stays as a sanity check | — |

### Batch C review round (2026-10-16)

| # | Correction | Applied in |
|---|---|---|
| C36 | **B3: removed content must not survive through another route.** Page-private resources (C31) are not enough: a resource dictionary shared with a page that is not redacted, a form's or tiling pattern's resources, or any other route still reached the removed image. Two rules: (1) a state-setting operator that names a resource (`cs`/`CS`, `scn`/`SCN` with a pattern, `gs`) is kept only if a surviving paint consumes it — fill, stroke and ExtGState are tracked per paint (paths by operator, text by rendering mode, `Do` all, `BI` fill, `sh` ExtGState) — so a pattern or soft mask used only by removed paint goes with it; (2) **a build-wide deny set D \ P** (option b, chosen over per-page GC because it covers every depth, e.g. an unused entry in a shared form's resources): D is the closure of the objects only removed content looked up (`Output.Removed`); P is the closure of what kept content paints — the rewritten pages' private resources and page-box/`/Group` entries, and for every page that is not redacted but can reach D, the objects its content paints (`redact.PaintedObjects`, a lenient walk: every used name in every candidate scope, through forms, selected patterns, soft masks and Type 3) plus its annotations and `/Group`. Complete: a denied object is never written (null, or dropped from an array); a kept object of D is painted by an output page, where it is shown anyway. Safe: P is closed under reachability, so nothing painted refers to a denied object. Limits: inline-image colour spaces of an unredacted page are not in P (over-removal only); such a page past the budget refuses the redaction (`budget_exceeded` naming that page) | §6.10, §5.5 |
| C37 | **B1/B2/N1: D5 geometry.** A path whose CTM changes after its last point, before the painting operator, meets every region, and so does its clip (Ghostscript maps points with the CTM at painting, other readers at each point). ExtGState `/LW` and `/ML` set the widening; when scopes disagree or only a fallback binds the name, the larger value is kept; a non-number is unbounded. Every painted path is widened by 1 pt on the page (a hairline still paints a device pixel) | §6.10 |
| C38 | **S1/N5: D11 complete.** Any object whose resolved `/Type` is `/Page` or `/Pages` is a page-tree object for the copier (`parse.IsPageObject`), so an orphan page a link targets is never copied. The annotations of source pages outside the build (and their popups) are denied unless a page of the build lists them, so a reply's `/IRT` cannot pull another page's annotation into a split. On a Redacted page, the popup of an annotation written directly in `/Annots` is denied too (N4) | §5.5, D11 |
| C39 | **S3: page identity.** Marks identify their page by document and object number (`PageInfo.ObjectNumber`), so `Rotate` (which clones the page dictionary) between `RedactText` and `RedactAndFlatten` no longer fails as an internal error | §5.4 |
| C40 | **Text and docs.** `ErrInvalidPDF` redaction errors no longer quote resolver or decode errors (N2); a refusal's detail drops the parser's `parse: lexer error:` prefix (N3); `ErrInlineImageLoss` does not cover an image split across streams (that is a join loss, `content_ambiguous`), and `ErrOpLimit` is reported instead of a loss met past the cap (S2); exported `CopyPage` documents that a page copied by a later call is null (S4); the cloud's `page` is 0-based (N7); a mid-render deadline test runs without `-race` (N6). Lenient spans are checked with the strict operand rule (`parse.OperandsFit`) before the lenient walk interprets them | — |

### Batch C review round 3 (2026-10-17)

| # | Correction | Applied in |
|---|---|---|
| C41 | **The deny list's closures stop at `/Resources` (BL-1, BL-2).** C36 followed every reference of kept and removed units, `/Resources` included: an image merely *listed* in a resource dictionary a kept form, Type 3 font or annotation appearance shares landed in P and was written (BL-1: rows `d5_kept_form_shared_res`, `d5_unredacted_form_shared_res`, `d5_unredacted_annot_ap_shared_res`, `d5_kept_type3_shared_res`); and a removed form's closure reached the page's resource dictionary or an indirect subdictionary, which P (leaf values only) never held, so a page that is not redacted was written with `/Resources null` (BL-2: rows Q1, Q2, Q5, Q6 of `TestRedactionKeepsUnredactedPages`). Now D = closure(`Output.Removed`) and P = closure(leaf values kept content looks up) both skip every `/Resources` entry; for a rewritten page P starts from `Output.Used` (its surviving lookups at every depth) plus its private resources' values; for a page that is not redacted, from what `Painter` finds its content and each annotation appearance stream (`/AP /N /R /D`, every state) look up, its `/Contents`, its annotations and `/Group`. **Invariant:** the objects denied are exactly those only removed content used; nothing a kept lookup resolves is denied; containers are never denied — a shared resource dictionary is copied and its denied entries become null | §6.10 |
| C42 | **One walker per call (SF-1).** `redact.Painter` walks every page that is not redacted with one walker: one memo, one 8 Mi budget. Page scope keys name the chain's dictionaries, so memoized units of pages with different resources stay apart. 40 pages sharing one 3 M-operator form: 47 s → 1.4 s; with a resource dictionary per page the form is re-walked per page and the shared budget refuses (`TestDoS_RedactSharedUnitWalkedOnce`) | §6.4 |
| C43 | **What a page paints, completed (SF-2, SF-3, N-a).** The default colour spaces (`DefaultGray/RGB/CMYK`) of every scope a unit walks are looked up; inline images' named colour spaces count (`parse.ScanContentSpansWithInlineImages`, a lenient scan that also reports the dictionaries); an unredacted page's `/Contents` are kept (rows O1–O3) | §6.10 |
| C44 | **Reverted by C46.** *(Was: text shown in a clipping mode narrows the clip box at `ET`, and every paint item's box is intersected with the clip in effect.)* | §6.10 |
| C45 | **Errors (N-b, N-c).** A page that is not redacted whose walk fails on an object that does not resolve keeps everything it can reach (`/Resources` included); no resolver text is reported. A deny-list refusal for the document as a whole has `Page -1` (documented on `RedactionError.Page` and the cloud `page`). `parse.IsPageObject` is `parse.IsPageTreeObject`. `parse.OperandsFit` stays exported (the lenient walk needs the strict operand rule; an internal package cannot hold it without an import cycle) | §6.9 |

### Batch C review round 4 (2026-10-17)

| # | Correction | Applied in |
|---|---|---|
| C46 | **No clip narrows a D5 paint (C44 reverted).** Narrowing under-removed: FluxDocs' own renderer ignores W/W* and text clips (it clips only to a form's `/BBox`; `Tr 7` text is invisible text), so a user redacts what our viewer shows and the image survived under the black box; `4.5 Tr`/`7.9 Tr` were truncated to clip modes that Ghostscript does not apply; `h W n` (no point) narrowed to the origin; and glyphs of substituted fonts overhang their text-clip box. Paint items are bounded only by their own geometry (and a form's `/BBox`); `sh` is bounded by the page box. Rows: `d5_clip_re_W_n`, `d5_clip_text_Tr7`, `d5_clip_text_Tr4.5`, `d5_clip_no_point`, `d5_sh_under_W_clip`. **Prerequisites for any future narrowing:** `core/render` implements W/W* and text clipping first; `Tr` counts as a clip mode only when it is exactly an integer 4–7; an empty or no-point clip never narrows; text-clip boxes use widened glyph bounds (C49) | §6.10 |
| C47 | **Painter budgets (SF-1).** The call walks pages that are not redacted only while some removed object is still undecided, and only pages that can still reach one (reachability recomputed when the undecided set shrinks). Each page gets the per-page budgets (8 Mi visits, 512 MiB decoded); the call is capped at 32 Mi visits and 1 GiB of unit decoding (C56). Scope ids are content fingerprints of the resource dictionaries, so per-page dictionaries with equal contents share memo entries — rather than recording and re-resolving a unit's bindings per new scope chain, which equal dictionaries make unnecessary. Measured (native, `rev3/h` `B`): 300–2,000 pages with 3 k–50 k operators each and shared or per-page dictionaries all complete (0.3–5.7 s); a hostile page set with a distinct 2 M-operator form per page refuses after 15.5 s at 32 Mi (44 s at 64 Mi, which is why 64 Mi was not taken) | §6.4 |
| C48 | **Conforming bindings for kept content (BL-B).** `walker.lookup` still walks every candidate binding (refusals) and records every candidate's objects for the removed side (D), but kept content (`Output.Used`, `Painter`, resource GC) records only the conforming binding — the innermost scope that defines the name, falling back outward only when it lacks it. A tiling pattern a child unit only lists is reached for refusal checks with no collector, as on the page (an `scn` that selects it replays its lookups). Rows: `d5_kept_form_own_same_name` (E4), `d5_unredacted_form_own_same_name` (E5), `d5_kept_form_lists_pattern` (E1). Known over-removal (never a leak, since D keeps every candidate): a nested form without `/Resources` binds through its parent form's scope, while a literal reading of §7.8.3 binds through the page's; and a name the inner scope binds to `null` conforms there, so the outer binding a falling-back reader may use is not kept. Either reader can then miss an object only removed content used | §6.2, §6.10 |
| C49 | **Substituted fonts' ink (SF-2; corrected by C53).** *(Was: the standard 14 font's Adobe AFM `/FontBBox` for a standard 14 `/BaseFont`, else the union of the 14 widened. The AFM boxes are Adobe's faces; readers draw their own clones, which reach past them — see C53.)* A font without an embedded program is painted with a reader's substitute, so its ink bound covers one substitute box, whatever the `/BaseFont`. Redaction boxes only; extraction is unchanged | §6.6 |
| C50 | **Nits.** A page that is not redacted and cannot be read (an object does not resolve) now refuses (`content_ambiguous`, no file text) instead of keeping all it reaches (C45); P does not follow `/Alternates`, `/OPI`, `/Metadata`, `/PieceInfo` or `/AF` of a kept object; `RedactionError.Error()` says "document" for `Page -1`, and the examples print "for the document" | §6.9 |

### Batch C review round 5 (2026-10-17, final)

| # | Correction | Applied in |
|---|---|---|
| C51 | **Vertical metrics are refused, not modelled (B-1).** The text model places vertical glyphs within ±1.1 em of advance and ±`verticalInkEm` of ink and never read `/DW2` or `/W2`, which Ghostscript honours, so a region over the glyphs it drew left the `Tj`. fontmodel now reads `/DW2` and `/W2` (both forms, within the `/W` item bound) into hulls of vx, vy and w1y; a vertical font whose metrics would shift a glyph's ink out of the model's box (the ink hull mirrored and widened to `verticalInkEm`), or advance it by more than 1.1 em, or that cannot be read, refuses `content_ambiguous` ("a vertical font's /DW2 or /W2 places glyphs outside the bounds redaction models"). `verticalInkEm` is 1.5 (was 1.2) so the default `/DW2 [880 −1000]` with a substituted glyph box (C53) still redacts. Rows: `d6_vertical_standard` (clean), `d6_vertical_dw2_offset`, `d6_vertical_w2_advance` (refuse) | §6.6 |
| C52 | **Reachability recomputed by halving (S-1).** Round 4 recomputed which pages can reach an undecided object each time the set shrank (quadratic: 2,000 removed images, 100 k filler objects took 63 s). It is now recomputed only when the undecided set has halved since the last pass — a stale set is a superset, so a page it lets through is only walked — and the passes are charged to the 32 Mi call budget. Inherited resources are expanded into a page's roots only when the page has no `/Resources` entry, and the graph walk does not re-queue seen roots. The same case takes 0.8 s (`TestDoS_RedactReachabilityBounded`) | §6.4 |
| C53 | **One substitute box (S-2).** Ghostscript draws non-embedded fonts with URW clones whose glyphs reach past Adobe's AFM boxes (WinAnsi Helvetica Å/Ê/Ò/Š/Ž/Î to 0.953 em against 0.931; Times Å 0.915 against 0.898), and `/Differences` can name glyphs outside the Adobe set (Aringacute 1.17 em high, Scommaaccent −0.372 em deep, Bookman-LightItalic AE 1.269 em wide); measured substitutes span about [−231 −393 1346 1170]. Every font without an embedded program — standard 14 names included — now covers [−300 −450 1450 1250]. The `rev4` SUB/SUB2/SUB3 probes keep no run whose ink a region touches | §6.6 |
| C54 | **Rendering mode must be an exact integer 0–7 (S-3).** `int(...)` truncated `1.5 Tr` to stroke-only, so a fill pattern kept text renders with was removed; Ghostscript renders any non-integer mode as 0. Any other operand is undefined, and text then consumes every state | §6.10 |
| C55 | **Exact scope fingerprints.** Scope ids compare an exact, type-tagged encoding (integers apart from reals, reals by bits, strings and names by bytes) hashed with SHA-256, confirmed by a full comparison on a hash match (the encodings are not kept) | §6.4 |
| C56 | **Painter memory.** Decoded unit content is cached for the memo up to 256 MiB, then decoded again (charged); all unit decoding in a call is capped at 1 GiB, checked before and after each page (`rev4` MEM: 1.64 GB → 0.77 GB RSS) | §6.4 |

## Design corrections from batch D implementation (2026-10-17)

Batch D: steps 1.12 (verifier), 1.13 (automatic extension, report), 1.14
(benchmarks, corpus, CI oracle job, spec and SECURITY.md) and follow-up F1.
Baselines were recorded at **f325e25** on a rebuilt corpus list (the earlier
scratch lists were lost with a machine restart): 719 system PDFs (every PDF
under /Applications, /Library, /usr/local/Cellar and /usr/local/share, plus
one in five under /Library/Developer and /System/Library) and **53 generated
text-heavy files** (24 man pages via `man -t | ps2pdf`, 4 Chrome
print-to-PDF, 5 Quartz via `cupsfilter`, 14 Ghostscript `pdfwrite` rewrites at
PDF 1.3, 6 synthetic PostScript documents with inline images at PDF 1.2–1.4):
772 files, 3,337 redaction outcomes. Render and extract hashes of every
file (7,400 lines) and the canonical object graphs of 2,623 SaveTo, Split and
Merge outputs are identical to f325e25 (the review round's parser fix, C73,
changes three generated files; nothing else).

| # | Correction | Applied in |
|---|---|---|
| C57 | **Verifier shape (1.12).** Check 1 re-reads each redacted page of the output strictly and also asserts that no property list keeps `/ActualText`, `/Alt` or `/E` (D7) and that no run holds a propagating needle. Removal regions are the `RedactArea` rects only (RedactText rects are display boxes, C17). Every needle type is compared on the skeleton (§6.11), not NFC with collapsed white-space. A budget refusal while re-reading is `budget_exceeded`, not a verification failure | §5.6 |
| C58 | **What the object search reads (1.12).** Objects are classified by route from the catalog and `/Info` (`redact.ObjectRoles`). Not searched: image samples (image `/Subtype` or an image-only filter, as §5.6 says — a thumbnail without them *is* searched), fonts and font structures (programs, CMaps, `/ToUnicode`, `CIDSystemInfo`, descriptors), ICC profiles, functions, halftones and Indexed lookups — their bytes match short needles by chance (an Indexed ramp or a sampled gradient holds "1234" and "ABCD"; a font names "Adobe"). Content streams are read by the strings their text operators show (TJ joined) and their inline property lists, never by raw bytes (operands such as `1000 0 0 1000 0 0 cm` hold digit needles). `/Info` is searched with the propagating needles only (a residual removes the field): with every needle, a removed run "Report" fails any document titled "Annual Report" | §5.6 |
| C59 | **Settle: removed runs move later text (1.12).** Removing a run removes its pen advance, so later runs of the same text line (an "epoch": between BT, Td, TD, Tm, T*, ', ") are shown further back — possibly inside a region, where copy-paste would then find kept text under the black box and the check "no glyph run meets a planned rect" would fail real files. When a region is planned and a removed run precedes a kept run of its epoch, Rewrite interprets the page again as written and also removes every run the output places in a region. Text removed only this way is collateral: not an area needle | §5.5, §6.8 |
| C60 | **Comments.** On a rewritten page, comments before each operator (a span starts where the scanner began looking for its first token, so its leading white-space and comments are inside it) and after the last are replaced by a space: they are white-space to every reader but could hold the removed text. Row `comment_holds_secret` | §5.5 |
| C61 | **Stripped `/Properties` entries are removed objects.** A named property list whose entry has a text key is written as a page-private stripped copy, so the entry's object is no longer the page's: it goes to `Output.Removed`, and the deny set keeps it out of the output unless kept content uses it. Row `actualtext_shared_form` is therefore **clean** in Phase 1 (the form keeps a null), not refused as the row's note predicted | §6.7, §6.10 |
| C62 | **Pre-pass (1.13).** Each page is first read for its text only, leniently, through every unit (`redact.TextScanner`: no glyph geometry, fonts loaded once per call); only a page that shows a needle is read with the strict model, which places the runs to remove. `FindNeedles` on a caller's page skips only the runs removed by a mark or by a region where the source places them — not settle removals (C59), which depend on the whole plan: with the extension's marks a run settle had removed can be kept again (seven `License.pdf` area-half files failed verification before this) | §6.11 |
| C63 | **Verifier and pre-pass share the work (1.12, 1.14).** When the pre-pass ran, the verifier does not read the other pages again with fonts: their copies show what the pre-pass read (content, resources and annotations are copied as they are, and what the deny set removes no kept content paints). It still searches every object those pages reach by bytes, reading shown strings as Latin-1 only — a reading the text model also matches, so a hit is attributable — and skips content streams whose bytes the pre-pass read without a hit (same filters and raw bytes, SHA-256). Without the pre-pass (test hook) the font-aware read runs, and round 1 extends | §5.6 |
| C64 | **Attribution of residuals (1.13).** A hit in an object only pages that are not redacted reach is attributed to each such page that reaches it other than through `/Annots` (page hygiene: the page is redacted with no region; the hit may be in the page object itself, e.g. `/PieceInfo`) and to each annotation that reaches it (removed, with its popup and replies; the output's `/Annots` index is mapped back to the source's). A residual that changes nothing is a verification failure | §6.11 |
| C65 | **Report details (P8).** `RedactionExtension.Where` gains **`"page-hygiene"`** (the third location of the §6.11 table; not in the approved shape — flagged). `RemovedAnnotations` lists the annotations removed from pages that are not otherwise redacted (a redacted page drops all) and every file attachment; a subtype outside ISO 32000-1 Table 169 is reported as `"Unknown"` (a name can carry text). The cloud's `auto_extended` is the number of extensions, removed annotations and removed information fields | §9 P8 |
| C66 | **Refusals extension causes** have a detail starting `"automatic extension: "`; the regression harness classifies them as `<reason>/d4`, and allows `form_text/d4` and the four Phase 1 reasons with `/d4` | §12.4 |
| C67 | **`/Info` and RedactText patterns.** Following §6.7, a field that matches a RedactText pattern is removed too; the regression's `RedactText(page, ".")` therefore removes every non-empty Title, Author and Subject (61 files) | §6.7 |
| C68 | **Budgets.** Pre-pass and verification share one 2 GiB decoded cap per call (`budget_exceeded`, page −1); each page walk keeps the per-page budgets | §6.11, §7 |
| C69 | **A document with no page** builds an empty page tree, which the lenient reader re-opens as one empty page: the page-count check skips that case (`malformed/cyclic_pages.pdf`) | — |
| C70 | **F1 (batch C review).** The deny-set loop deleted from the undecided set by iterating all of it once per walked page (O(pages × \|D\|); 60,000 pages × 10 filler objects took 66 s). `closeInto` now returns the objects it added, and only those leave the undecided set. `TestDoS_RedactUndecidedPerPage` (30,000 pages): 68.4 s with the old loop, 6.5 s now | §6.4 |

### Batch D measurements (1.14)

**Leak corpus.** 330 rows (`FLUXDOCS_REQUIRE_GS=1`, gs 10.07.1): 326 pass;
the 4 rows tagged 2.6 (`split_bt`, `split_same_bt_tj`, `split_same_bt_TJ`,
`split_same_bt_per_char`) stay `knownLeak` and still reproduce. Closed: the
1.12 row `actualtext_shared_form` (clean, C61) and the 1.13 rows
`props_shared_unused`, `props_shared_used`, `d4_other_page` (text, area),
`d4_text_annotation`, `d4_widget_value`, `d4_attachment`, `d4_info_title`
(text, area). New rows: `d4_other_page_form` (refuse `form_text`, both ops),
`d4_annotation_appearance` (popup and reply go too), `d4_skeleton_softhyphen`,
`comment_holds_secret`.

**Corpus no-regression** (772 files, first 3 pages): the only changed outcomes
are three `form_text/d4` refusals — extension into a form on another page:
`argon2-specs.pdf` (area-full) and `GS9_Color_Management.pdf` (area-full,
area-half). Verification failures, internal errors, `nonempty_text`: 0.
Automatic extension fired on 111 of 761 successful area-full and 101 of 762 area-half
operations (212 operations, 4,291 pages extended in total; boilerplate such as
license headers recurs on every page) and on 61 text/flatten operations
(document information fields only, C67).

**Inline images (C9).** In the generated set, Ghostscript `pdfwrite` writes
small images inline, Flate-compressed **with a PNG predictor** (`/DP`): all 54
inline images of the six synthetic files are refused by D15's predictor rule
(`inline_image`, every operation on those files, at f325e25 too). The PDF 1.3
`pdfwrite` rewrites of system files and the other producers wrote no inline
image. Predictors are therefore the first candidate for the step 2.7
widening.

**Cost (verification + pre-pass, target ≤ 50% of RedactAndFlatten): not met.**
`BenchmarkRedactVerify` (synthetic 40-line pages; "base" is RedactAndFlatten
with pre-pass and verification off by test hooks; process CPU time per
operation, which was stable while the machine's load average stayed between
30 and 500):

| Case | base | full | overhead |
|---|---|---|---|
| region on page 0 of 1 page | 0.76 ms | 1.07 ms | +41% |
| region on page 0 of 20 pages, no extension | 0.93 ms | 5.83 ms | +527% |
| RedactText on page 0 of 20 pages, no extension | 1.33 ms | 8.20 ms | +517% |
| region on every page of 20 (all text removed) | 12.5 ms | 26.0 ms | +108% |
| AutoRedactPII, 20 pages (4 with PII) | 2.7 ms | 14.3 ms | (wall 42 → 55 ms, +29%) |
| region on page 0 of 100 pages, no extension | 2.6 ms | 31.7 ms | +1,100% |
| region over the header every page shows, 100 pages | 3.7 ms | 126 ms | extension rewrites 99 pages |

On the corpus (first 3 pages redacted): area-half CPU 4.9 s → 37.2 s
(+654%), area-full 5.0 s → 42.9 s (+764%); this includes rewriting the 4,291
pages extension reached, and the slowest file takes about 1 s. The fixed
cost is one text-only read of every page that is not redacted (about 0.25 ms
for a 40-line page; profile: the text model's readings 55%, content scanning
25%) plus the byte-level search of what those pages reach; base
RedactAndFlatten only copies such pages (about 0.025 ms each). D4 requires
the search, so the overhead scales with the document while the base cost
scales with the redacted pages. Done in batch D: text-only reading, fonts
loaded once per call, shared work between pre-pass and verifier (C63), an
allocation-free ASCII skeleton, an O(n²) loop over object numbers removed.
**Reported to the maintainer per the exit criterion.** Remaining options:
the per-document text-model cache (2.10), so the pre-pass reuses the models
`RedactText`/`AutoRedactPII` built (AutoRedactPII already reads every page
four times while marking); and skipping the search of a page whose content
streams and fonts are byte-identical to one already searched.

### Batch D review round (2026-10-17)

go-reviewer (rvD probes, now leak-corpus rows `rvd_*`, 43 operation rows) and
maintainer decisions: optimize before commit; `Where = "page-hygiene"`
approved; the CI oracle job non-blocking until green and stable.

| # | Correction | Applied in |
|---|---|---|
| C71 | **B1: nothing hidden stays on a rewritten page.** Comments are blanked anywhere in the kept content — before an operator, between its operands, in an inline image's dictionary (lexer-aware: strings, hex strings and names skipped; the end of line kept). Every property list (inline, or the page's named entry) loses every string value but a language tag (`/Lang`, BCP 47 shape), not only `/ActualText`, `/Alt`, `/E`. Check 1 also searches what the rewritten content holds but does not show — the strings of every operator but text-showing ones and inline images, and every comment — for every needle | §5.5, §5.6, §6.7 |
| C72 | **B2: a search never reads "nothing" into what it cannot read.** The text scanner reads each content stream strictly first; a strict loss (read leniently after all) or content that does not decode marks the page *disputed*, and the pre-pass refuses a disputed page it must search (`content_ambiguous` or the loss's own reason, "automatic extension: …"): a lenient reading may hide text Ghostscript shows (`rvd_lenient_hidden_other_page`, `rvd_corrupt_flate_other_page`). An annotation appearance that is disputed counts as a hit (the annotation is removed). `parse.DecodeStream` now decodes ASCIIHex, ASCII85, LZW (EarlyChange, predictors) and RunLength, within the same budgets (cross-checked against `compress/lzw`); a stream the object search must read but cannot decode is a residual in R_u (page hygiene or annotation removal) and a `content_ambiguous` refusal when only redacted pages reach it — never "its dictionary's strings only". Hitting the per-stream decode cap is a budget refusal (S6). The leak oracle decodes the same filters and reads a Flate stream to where it fails (S7) | §5.6, §6.11 |
| C73 | **A parser bug B2 exposed (Core, `parse`).** A stream whose `/Length` is an indirect reference was cut by the `endstream` scan, which takes a final CR or LF of the data for the end of line: one Flate stream in 256 written by Ghostscript (which always writes indirect lengths) lost its last byte and did not decode, so its page rendered and extracted nothing, and B2 refused to search it (3 man pages of the generated set). The parser now resolves an indirect integer length directly from its xref offset (no recursion into the resolver; object-stream lengths still use the scan) and uses it when `endstream` follows. **Corpus effect, the only render/extract/graph change of batch D:** `gs-dig.pdf` (page 5), `gs-git.pdf` (page 12), `gs-tar.pdf` (page 14) now extract text on those pages (text and table hashes change), and their SaveTo and Merge outputs carry the whole stream; every other hash and graph of the 772 files is identical to f325e25 | `core/parse` |
| C74 | **B3: removed annotations paint nothing.** The deny set sees each page that is not redacted as the build writes it — without the annotations the plan removes (and references to removed annotation objects) — so an image removed under a region does not survive through the appearance of an annotation automatic extension removed (`rvd_removed_annot_ap_keeps_removed_image`) | §6.10 |
| C75 | **S1: roles by route.** Whether an object is a font, binary data or content is decided by the route that reaches it (a resource dictionary's `/Font` and `/Shading`, an ExtGState's `/Font`, a font's parts, function keys, colour space slots, `/CharProcs` entries, `/AP` streams); keys an object carries (`/N`, `/Length1`, `/Registry`+`/Ordering`, `/FunctionType`) classify only objects nothing reaches. Image samples stay excluded by their own `/Subtype` or filter (§5.6). An unused form with `/N` or `/Length1`, a `/PieceInfo` with `/Registry`, and an unused Type 3 font's glyph procedures are now searched (`rvd_unused_*`, `rvd_pieceinfo_*`) | §5.6 |
| C76 | **S2: what an extended page loses is reported.** A page automatic extension redacts keeps no annotation; each is listed in `RemovedAnnotations` with Reason `"page-hygiene"` or `"page-redacted"` (text the page shows), and `RedactionExtension.Where` documents what a redacted page loses | §9 P8 |
| C77 | **Cost (maintainer: before commit).** (a) Per-byte reading tables for fonts read one byte per code (`fontmodel.ByteTables`; readings that cannot differ from Text are not built), checked equal to the slow path; the text-only search checks each reading as it is built and keeps no run. (b) Step 2.10 brought forward: a per-document cache of strict page models (`core/redact_cache.go`, keyed by page object, 128 MiB estimated, LRU), so marking reads a page once (AutoRedactPII read each page once per PII kind), and the pre-pass and rounds reuse it. `TestRedactVerifyCostGuard` fails past 15× (measured about 3×). Not reached: ≤ 1.5× for 20 and 100 pages; see the table below | §6.4, §7 |
| C78 | **Nits.** CI job non-blocking with a Ghostscript ≥ 9.50 check (S3); `RedactionReport.Summary()` (reviewer request; capped page list; "automatic extension:" only when something but the attachments policy happened) replaces the CLI and MCP helpers, MCP field `auto_extended_summary` (cloud keeps the integer `auto_extended`); core's `golang.org/x/text` aligned with the workspace (v0.21.0); fixed verification messages; `cputime` helpers `unix`/`!unix` | — |

**Measurements after the review round.** Leak corpus: 373 operation rows with
`FLUXDOCS_REQUIRE_GS=1`; 369 pass, the 4 rows tagged 2.6 stay `knownLeak` and
still reproduce. Corpus no-regression (772 files): outcomes as in batch D — 3
`form_text/d4` refusals, 0 verification failures; extension on 212 area
operations (4,294 pages) and 3,043 annotations now reported as dropped on
extended pages. Cost, process CPU per operation (`BenchmarkRedactVerify`):

| Case | base | full | full/base |
|---|---|---|---|
| region, 1 page | 0.46 ms | 0.54 ms | 1.17 |
| region on page 0 of 20, no extension | 0.79 ms | 3.88 ms | 4.9 |
| RedactText on page 0 of 20 | 0.68 ms | 3.29 ms | 4.8 |
| region on every page of 20 | 8.90 ms | 9.16 ms | 1.03 |
| AutoRedactPII, 20 pages | 1.54 ms | 6.02 ms | 3.9 (wall incl. marking 13.8 → 20.4 ms, 1.48) |
| RedactText on every page of 100, match on page 0 (cache) | 4.18 ms | 7.73 ms | 1.85 (wall incl. marking 26.8 → 28.7 ms, 1.07) |
| region on page 0 of 100, no extension | 2.28 ms | 17.3 ms | 7.6 |
| region over a header on every page, 100 pages | 2.22 ms | 81.0 ms | extension rewrites 99 pages |

Corpus (first 3 pages, CPU): area-half 3.4 s → 23.5 s (+587%), area-full
3.3 s → 23.3 s (+610%), including the extension work. What remains: the
pre-pass reads every page the caller did not mark (about 0.15 ms of a 40-line
page, of which the strict scan's operator spans are half the allocation) —
D4 requires that read, and no byte-level pre-filter can skip a page soundly
(TJ splits, white-space removal, font remapping). Further options: a streaming
strict scan in `parse` that does not materialize spans (a Core API addition),
and reading pages in parallel (bounded by `runtime.NumCPU()`).

### Batch D review round 2 (2026-10-17)

go-reviewer round 2 (decoders checked against ISO 32000-1 and Ghostscript;
the page-model cache, keyed by page object, safe under mutation; F1 correct)
and maintainer decisions (cost accepted as measured; `"page-redacted"`
approved). Rows `r2_*` (24 operation rows).

| # | Correction | Applied in |
|---|---|---|
| C79 | **BL1: an indirect /Length costs a few tokens, once.** C73's resolver parsed the whole target object, uncached, for every stream: each page's `/Length 4 0 R` naming a 500,000-integer array (about 1 MB) took 35.8 s for 200 pages and 7 min 37 s for 2,000. It now reads only `N G obj <integer> endobj` at the object's offset, requires the reference's number and generation, rejects anything else at once (so a stream or an array target costs a few tokens, never a scan to EOF), and caches each result per object number, failures included, under the resolver's lock. `TestDoS_IndirectStreamLengthIsCheap` (500 pages, 1 MB target) | `core/parse` |
| C80 | **BL2: hidden copies on pages that are not redacted.** The pre-pass now also reads what each page holds but does not show — every comment (between operators and operands, in inline-image dictionaries), the strings of every operator but the text-showing ones and inline images (property lists, marked-content tags) — and a hit is page hygiene, whose rewrite blanks comments and scrubs property lists (C71). Units record their hidden text while the page is read (strict model or text scanner, so a cached model has it); a hit there refuses with the unit's reason, since Phase 1 does not rewrite forms (`r2_other_form_comment` refuses `form_text`). The verifier's byte reading of content streams reads the same hidden content, so its skip of streams the pre-pass read is sound | §6.11, §5.6 |
| C81 | **BL3: attributing a residual is one pass.** Every page and annotation used to get its own closure (up to 1 Mi objects each): 4,000 pages sharing 40,000 objects took 23.2 s. The graph of what the pages that are not redacted reach is now built once and walked backwards from the hits (one walk per needle source, as the deny set does), within 32 Mi visits; a graph or walk past its bound is `budget_exceeded`, never a verification failure. `TestDoS_RedactAttributionIsOnePass` (4,000 pages: 0.5 s) | §6.11 |
| C82 | **S1: no property-list string survives a rewrite, `/Lang` included.** `(SECRET-1234)` has a valid BCP 47 shape, so the C71 allow-list kept it and verification failed as a bug. Rewritten pages drop every string of their property lists (the catalog's `/Lang` is not carried anyway) | §6.7 |
| C83 | **S2: the model cache counts what it holds.** `Page.Size` counts every element of array and dictionary operands (a TJ-heavy page: 28.2 MB estimated, 27.9 MB live; it was about half), and `Close` drops a document's models | §6.4 |
| C84 | **Cost decision.** The §11 cost exit criterion is rewritten to the measured scaling (see §11) | §11 |

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
| N5 | A token or comment straddling two `/Contents` streams. `PageContent` inserts `\n`, so `(SECRET-12` + `34) Tj` scans as `SECRET-12\n34` and the regex misses it. Readers disagree three ways (C2): join with no separator (shows `SECRET-1234`), join with white-space (us), or tokenize per stream. gs tokenizes per stream: it does not show the split string, but it ends a comment at the boundary (`split_comment` shows the secret) | F1 | `parse/content.go` `PageContent` |
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

- **Segments (as implemented in batch A, C3).**
  - `parse.ContentSegments{Data []byte; Joins []int}`. `Data` is
    byte-identical to `PageContent`'s output: for an array, each stream is
    followed by `\n`; a single stream gets no separator. `Joins` are the offsets
    of the inserted separators.
  - `(*Resolver).PageContentSegments(pi)` returns it. Spans are offsets into
    `Data`, so the byte splice works unchanged.
  - `ScanContentStrict(seg ContentSegments, opts StrictOptions)` reports a loss
    if any single lexical token (string, name, number, keyword, hex string,
    inline-image data) or comment contains a join. A comment that runs to the
    end of a stream is such a straddle (C2).
  - §7.8.2 allows a split only at token boundaries. A split *between* operands
    and their operator stays legal.
  - Readers disagree three ways about straddles (C2), so refusing is the only
    safe answer (N5).
  - `core/redact.go` `redactionContent` uses this path whenever the page has
    joins (C11, step 1.2).
- **Inline image boundary.** See §6.1. `StrictOptions.InlineColorSpace` is a
  callback that resolves a named inline-image colour space to its component
  count through the unit's resource scope. If it is nil or cannot resolve the
  name, the result is a loss. **Not yet wired into redaction (C7):** redaction
  scans without options, so named colour spaces refuse as `inline_image` until
  the 1.4 walker passes scopes.
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

**Step 1a. Phase 1 dictionary allow-list (D15).** On pages being redacted
(marked, or reached by the D4 pre-pass), every key must be in this table, with
a value of the stated form. Any other key, value, or combination is an
`inline_image` refusal.

| Key (full / abbreviation, Table 93) | Accepted value |
|---|---|
| `/Width` `/W`, `/Height` `/H` | Integer ≥ 1. `W × H` within the inline cap (§6.4 budget) |
| `/BitsPerComponent` `/BPC` | 1, 2, 4 or 8. Must be absent or 1 when `/IM true`. Required otherwise |
| `/ImageMask` `/IM` | Boolean |
| `/ColorSpace` `/CS` | (absent only when `/IM true`) **one of:** `/G` `/DeviceGray`, `/RGB` `/DeviceRGB`, `/CMYK` `/DeviceCMYK`; `[/I base hival lookup]` or `[/Indexed …]`, where `base` is one of those three, `hival` is an integer in `0 … min(255, 2^BPC − 1)`, and `lookup` is a string of exactly `(hival+1) × n(base)` bytes; or a Name resolved through `InlineColorSpace` (the unit's scopes) to one of the above. Refused: CalGray, CalRGB, Lab, ICCBased, Separation, DeviceN, Pattern, an incomplete array, an unresolvable name (B1) |
| `/Decode` `/D` | Array of exactly `2 × n` numbers (`2` for `/IM` and for Indexed). A wrong length is a refusal (B1) |
| `/Interpolate` `/I` | Boolean. `/I 5` is a refusal (B1) |
| `/Intent` | Name |
| `/Filter` `/F` | A Name or an array of Names from {`/AHx` `/ASCIIHexDecode`, `/Fl` `/FlateDecode`}. **Every** element is checked, not just the first (B1 "unchecked second filter"). At most one `Fl`, and `AHx` may only come first |
| `/DecodeParms` `/DP` | Absent, `null`, or a dict (or array aligned with `/F`) whose `/Predictor` is absent or 1 and which has no other keys. Any predictor is a refusal |
| `/L` `/Length` | Integer; T4 applies |

Duplicate keys (full and abbreviated count as the same key) are a refusal.
`n` is 1 for Gray, Indexed and `/IM`; 3 for RGB; 4 for CMYK.
`rowBytes = ceil(W × BPC × n / 8)`, or `ceil(W / 8)` for `/IM`.

**Step 1b. Data start (C5).** §8.9.7 puts exactly one white-space byte after
`ID`. When `ID` is followed by CR LF, two starts are plausible: after the CR
(the data begins with LF) and after the CR LF (the end-of-line taken as one
separator). Steps 2–3 run for **both** starts, and the results must agree.

- Unfiltered and Flate: the two starts give different ends, or one of them
  fails. These **refuse** after CR LF.
- AHx ignores white-space, so both starts reach the same EOD. It is
  **accepted**, unless a length entry (`/L`, T4) is present. The two starts
  then imply lengths that differ by one, so the result is a loss.
- DCT and A85 are refused in Phase 1 regardless (D15).

**Step 2. Compute the exact end `E` and decode the whole chain (D15).** `E`
comes from the first filter in `/F` (the outermost filter determines the extent
of the encoded bytes). Then the **whole chain** is decoded:

- within the inline budget;
- with a limit reader capped at `expected + 1` bytes;
- requiring that each filter consumes **all** of its input, with nothing left
  over;
- requiring that the final output is **exactly** `expected = H × rowBytes`
  bytes. More or fewer is a refusal (B2).

| First filter | End `E` and checks | Phase |
|---|---|---|
| none | `E = dataStart + H × rowBytes` (rows byte-aligned, §8.9.5.2). Overflow-checked int64; `E > len` is a loss | 1 |
| `AHx` | first `>` (EOD, §7.4.2). A byte that is neither hex nor white-space before it is a loss. The decoded byte count (an odd final digit is padded with 0) must equal `expected`, or for `[/AHx /Fl]` it is the exact input to the Flate step | 1 |
| `Fl` | Requires a **zlib header**: valid CMF/FLG, CM = 8, `(CMF<<8 \| FLG) % 31 == 0`, no preset dictionary. Raw DEFLATE without the header is a refusal (B3); the `flateDecode` raw-DEFLATE fallback is never used here. Decode with a `bytes.Reader` handed directly to `compress/zlib`, which implements `io.ByteReader`, so the decompressor does not read ahead. `E = dataStart + consumed`. The stream must reach its end, including the Adler-32 check. Truncation, an error, or decoded output ≠ `expected` (an over-long stream, B2) is a refusal. **Do not** use the lenient `DecodeStream` | 1 |
| `A85` | first `~>` (EOD, §7.4.3), plus exact decoded length | 2 (refused in 1, D15; B2) |
| `DCT` | JPEG marker walk to EOI **and** a full decode whose dimensions and components match `W`, `H` and `n` (B4) | 2 (refused in 1, D15) |
| `LZW` | EOD code 257 (§7.4.4). Needs a consuming decoder | 2 (refused in 1) |
| `RL` | EOD byte 128 (§7.4.5) | 2 (refused in 1) |
| `CCF`, `JBIG2`, `JPX`, `Crypt`, other, unknown | — | refused |

Every computed size is checked against the remaining bytes and the budget
before use. Nothing is allocated from untrusted `W × H` beyond the cap.

**Step 3. Terminator agreement.** All of the following must hold:

- (T1) `data[E:]` is `ws* "EI"` followed by **white-space or the end of the
  content**. A delimiter alone is not enough (C4): `EI(` or `EI/` is a loss.
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
- (T5) **A reader that rejects or under-reads the image parses its raw bytes
  as content.** Observed with gs (go-reviewer, batch A, 15 repros). When gs
  rejects an inline image, or stops reading it early, it resumes content
  parsing inside the image data. Text placed there is shown, while we treat it
  as image data.
  - Repro classes:
    - **B1, bad dictionary values:** `/I 5`; a `/D` of the wrong length; an
      incomplete CalGray, Lab, Separation or DeviceN array; an unchecked second
      filter.
    - **B2, over-long data:** Flate or A85 carrying more encoded data than the
      samples need, so the reader stops early.
    - **B3, raw DEFLATE** without a zlib header.
    - **B4, DCT that does not decode.**
  - Rule: we accept an image only when we are confident that every mainstream
    reader decodes exactly `[dataStart, E)` as image data. That is the D15
    allow-list (Step 1a) plus the full-chain exact decode (Step 2). Anything
    a reader might reject or under-read is refused. No allow-list can be
    *proven* complete across readers, so T6 adds defence in depth.
- (T6) **Defence in depth (D16).** The raw bytes `[dataStart, E)` must not
  contain a text-show candidate. Any of the following is an `inline_image`
  refusal:
  - the byte pair `Tj` or `TJ`;
  - `)` or `>`, then zero or more white-space bytes or comments (a comment
    is white-space, ISO 32000-1 §7.2.3), then `'` or `"`.

  This holds even if the image passed T1–T5. If some reader still falls back to
  parsing the data as content, it cannot reach a text-showing operator inside
  the data. The trailing text a fall-back reader would see lies after `E` and
  is scanned by us anyway.
  - AHx data can contain neither pattern, because `j`, `J`, `'` and `"` are
    not hex digits.
  - Unfiltered and Flate binary data trip T6 with probability about
    `2·len/65536` (the pair test dominates). Measured on the text-heavy corpus
    (§12.4, §13).

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
  | `maxRedactOpVisits` | 8 Mi operator visits (C27; was 64 Mi), two per scanned operator, including cache-hit replays and Type 3 glyph-procedure invocations | bounds fan-out such as 4096 forms × a large cached unit, and the spans a scan materializes |
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
  - **Text matching (Phase 1, D14, step 1.6).** `RedactText` and
    `AutoRedactPII` run their regexp over each property text, the same way they
    run it over glyph text (§6.11 skeleton rules apply only to D4
    propagation). On a match:
    - the whole `BDC…EMC` sequence is marked: every run inside it, at any
      nesting depth of marked content, becomes a span mark;
    - the matching key is stripped;
    - **the page is marked even when the sequence contains no runs.** The plan
      entry carries a property mark, so D7 and the rest of the marked-page
      pipeline apply. `RedactText` returns the union box of the sequence's runs
      (none if it has no runs).
    - The matched substring is a match needle for D4 (≥ 4 skeleton characters).

    This closes repros `actualtext` and `actualtext_props` for `RedactText`.
    Their visible glyphs are `XXXXXXXXXXX`, which never match, while copy/paste
    yields the secret. Before D14, D7 never ran because the page stayed
    unmarked. Scope in Phase 1: page content (inline dicts and page
    `/Properties`). Property lists inside forms make the form "show text"
    (§6.3), so it is refused.
  - **Area matching.** If any run inside the sequence is removed, or its box
    meets a rect, strip all three keys from that property list. On marked pages
    D7 strips them all anyway.
- **Rewrite.**
  - Inline dict: re-emit the `BDC` operand dict without the keys. This is a
    byte splice of the `BDC` span with a serialized dict; the other keys are
    preserved.
  - Named entry: COW a page-private `/Properties` entry.
- **Phase 1 (decided, D7).** The rewriter is built in step 1.6 (D14) and reused
  here. On a marked page, strip `/ActualText`, `/Alt` and `/E` from every
  property list:
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
| `core/internal/redact` (C25; `core/docops/redact` is a deprecated shim) | Pro | walker, text model, planner, rewriter, verifier |
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
      Where  string // "content" | "marked-content-property" | "page-hygiene"
  }
  type RemovedAnnotation struct {
      Page    int
      Subtype string // e.g. "Text", "Widget", "FileAttachment"; "Unknown" outside Table 169
      Reason  string // "needle" | "attachment-policy" | "page-hygiene" | "page-redacted"
  }
  func (r RedactionReport) Summary() string // one line, no document text
  ```

  As implemented (batch D, C65, C76, C78; `"page-hygiene"` and
  `"page-redacted"` approved 2026-10-17): `"page-hygiene"` and
  `"page-redacted"` list the annotations of a page that automatic extension
  redacted (a redacted page keeps none) by page hygiene or by text it shows.

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
- **P7, `parse` API.** This is Core and lower stakes, but importable. The shape
  was corrected in batch A (C3):
  - `ContentSegments{Data []byte; Joins []int}`;
  - `ScanContentStrict(seg ContentSegments, opts StrictOptions)`;
  - `(*Resolver).PageContentSegments`, `(*Resolver).ResolveName`;
  - `PageSource.ReplaceResources`, `PageSource.Redacted`,
    `BuildOptions.PageRefs`.

  `ScanContentSpansStrict(data)` stays as a wrapper with no joins.

## 10. Spec and SECURITY.md changes to apply (not applied by this note)

`docs/spec.md` has uncommitted render-budget edits, so these are listed here
for whoever lands Phase 1:

- **§7.4.** Document:
  - P1, P3 and the P2 behaviour;
  - D5 (images and paths under a region are removed whole);
  - D4 automatic extension and the P9 scope change;
  - the P8 report accessor;
  - that `RedactText` matches extracted Unicode across TJ elements, and also
    matches `/ActualText`, `/Alt` and `/E` (D14).

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
Batch A (in review) covers 1.0–1.2, with corrections C1–C11.

| Step | Content | Items / decisions |
|---|---|---|
| 1.0 | Record the baseline (§12.4; batch A recorded it at 71de860). Write repros for N1–N6 and the D4 extension cases. Add the leak-corpus table with every rv7 case, plus `split_comment`, with `gsBlind` flags (C1) and a temporary reason classifier (C8) | N1–N6, D4 |
| 1.1 | `parse`: inline-image rules (§6.1). The D15 dictionary allow-list (every key and value; unknown keys, predictors and non-device colour spaces refused). Filters: unfiltered, AHx and zlib-headered Flate only (A85, DCT, raw DEFLATE, LZW, RL, CCF, JBIG2, JPX and Crypt refused). Whole-chain exact decode to `H × rowBytes` with nothing left over. Both starts after CR LF (C5). T1 white-space-or-end (C4). T1–T4 with the strict no-`EI`-pair rule. T5. T6 text-show-candidate scan (D16). `StrictOptions.InlineColorSpace` is not yet wired (C7). **Batch A fix-up:** close the 15 go-reviewer repros (B1–B4) | 1, D1, D15, D16 |
| 1.2 | `parse`: `ContentSegments` / `PageContentSegments` / `ScanContentStrict` (C3); straddling-token and straddling-comment check (C2); operand types for resource-name operators. **Minimal wiring:** `core/redact.go` `redactionContent` uses the segment scan when the page has joins (C11), because N5 is not closed otherwise | N5, 2 |
| 1.3 | `core/internal/fontmodel` v1: code segmentation and `/ToUnicode` (widths can wait for 2.2). Migrate `extract` to it with no behaviour change, confirmed by the existing `extract` tests | D12, N1 |
| 1.4 | `redact/walk` v1 (check only for text-bearing units): resolved classification, decoded names, candidate scopes, routes Form / SMask group / tiling pattern (selected + reachable) / Type 3 char procs / `gs /Font`; refuse when any shows text; placements recorded for D5; memoized; budgets (§6.4). **Wires `InlineColorSpace`** with the unit's scopes, so named inline colour spaces stop refusing (C7) | 2, 3, 4, N6 |
| 1.5 | Text model and matching: Unicode via the font model, one run per TJ (elements concatenated, glyph index map kept, **strings read at any nesting depth inside the TJ operand**, `tj_nested`, C6), skeleton normalization (§6.11); span marks; closed intervals; non-finite boxes intersect everything; rect validation | N1, 5, D12, P2, P6 |
| 1.6 | **Property-list matching (D14, moved from Phase 2):** `RedactText`/`AutoRedactPII` match `/ActualText`, `/Alt` and `/E` (inline `BDC`/`DP` dicts and page `/Properties`). A match marks the page (a property mark, even with no runs), span-marks the `BDC…EMC` runs, and strips the key. Builds the property-list rewriter (inline re-emit and page-private `/Properties` clone) | 7, D14 |
| 1.7 | `Tc`/`Tw`/1.1 em stopgap boxes and the interval pen (§6.6, last bullet) | 6 (stopgap) |
| 1.8 | `ErrRedactionUnsupported` + `RedactionError` + `ErrRedactionVerification` (wraps `ErrInternal`) in `core/errors.go`, rebased on render-budget's sentinels. Drop `ErrInvalidPDF` from the redaction chain; keep `redact.ErrFormXObjectText` via `Unwrap`; update the cloud (422), MCP and CLI callers. **Collapse the duplicated reason classifiers** in the leak corpus and the regression harness into `RedactionError.Reason` (C8). **Engine API (batch B review):** move the engine to `core/internal/redact`; replace `redact.RefusalError`/`redact.Reason*` with `core.RedactionError` (`ErrFormXObjectText` stays reachable one more release); unexport or delete `RedactContent`, `FindTextRects` and `CheckFormXObjects` | 9, P1, P3, D2, D3 |
| 1.9 | Builder (`core/parse/copy.go`): `Redacted` page-dict allow-list; `PageRefs` back-reference remap, **also for Merge/Split**; resource GC on marked pages (the page's own resources only; text-bearing forms are refused in Phase 1, so no COW yet) | N2, N3, N4, D11 |
| 1.10 | D7: strip `/ActualText`, `/Alt` and `/E` on every marked page, reusing the 1.6 rewriter; collect the stripped values as needles | 7, D7 |
| 1.11 | D5: remove image `Do`, inline images, text-free forms, painted paths and `sh` whose bbox meets a rect (§6.10 Phase 1 rules) | D5 |
| 1.12 | Verifier (§5.6 checks 1–3): needle search, object classification, `/Info` | P3, N7, D3 |
| 1.13 | D4 automatic extension (§6.11): needles from the user plan; pre-pass over all pages, annotations, fields and `/Info`; attachment policy; fixed-point loop with `maxExtendRounds = 2`; minimal report via `(*Document).RedactionReport()` (P8, P9 approved) | D4, P8, P9 |
| 1.14 | Benchmarks for the ≤ 50% verification + pre-pass target. Corpus no-regression (§12.4), including a **text-heavy corpus** (C9). Oracle CI job with `FLUXDOCS_REQUIRE_GS=1`. Apply the §10 spec and SECURITY.md changes | D3, D10 |

**Why D14 is step 1.6.** Property-list matching is a *matching* feature. It
needs the 1.5 machinery: Unicode decoding, regexp over decoded text, span marks
for the `BDC…EMC` runs, and plan entries that can mark a page. So it comes
directly after 1.5.

It also has to remove what it matched. That is why it builds the property-list
rewriter itself, rather than waiting for D7. D7 (1.10) then becomes a small
policy step: "strip all keys on every marked page", reusing that rewriter.

Placing it later, next to D7, would leave the `actualtext` repros open through
four more steps. Merging the two into one step would couple a matching change
with a page-policy change in one review.

Exit criteria for Phase 1:

- Every leak-corpus case either produces output that passes all three oracles
  (§12.1, with the gs input sanity check skipped only on `gsBlind` rows) or is
  refused with `ErrRedactionUnsupported` and the expected reason.
- `actualtext` and `actualtext_props` are `clean` under **both** `RedactText`
  (pattern matching only the `/ActualText`) and full-page `RedactArea` (D14 +
  D7).
- Every D4 extension case either removes all occurrences and reports them (P8),
  or fails closed with the expected error.
- The corpus no-regression check (§12.4) passes.
- Cost (rewritten 2026-10-17, maintainer decision: cost accepted as measured;
  was "verification plus pre-pass ≤ 50% of `RedactAndFlatten`"). Automatic
  extension must read every page the caller did not redact, so the cost
  scales with the document: about 0.12–0.17 ms per unredacted 40-line page,
  and a whole `RedactAndFlatten` (open, region on page 0, flatten) costs about
  1.8× (20 pages) and 2.2× (100 pages) of opening the document and running a
  whole-document `ExtractText` (process CPU, `core/redact_bench_test.go`
  documents; measured at a load average of 3). `TestRedactVerifyCostGuard`
  keeps the ratio to the redaction without pre-pass and verification within
  15× (measured about 3×). A streaming strict scan and reading pages in
  parallel are deferred to Phase 2.

### Phase 2: rewrite instead of refuse

| Step | Content | Items |
|---|---|---|
| 2.1 | `fontmodel` v2: widths, FontBBox, WMode, Type 3 metrics; deep extraction for forms and Type 3 shared with the verifier | 6, N7 |
| 2.2 | Full width model: interval pen with known widths, FontBBox, WMode | 6 |
| 2.3 | COW rewrite of forms, soft-mask groups, tiling patterns and Type 3 char procs (§5.5, §6.3); `ReplaceResources`. This makes D4 extensions into such units possible, where Phase 1 refuses them | 3, D4 |
| 2.4 | Property-list matching *inside rewritten units* (forms, patterns, groups; the page-content part moved to Phase 1 step 1.6 by D14); metadata policy option; COW'd `/Metadata` dropped | 7 |
| 2.5 | Glyph-level partial removal with collapsed adjustments (§6.8); area needles shrink to the glyphs inside the rect | 8, D6, D4 |
| 2.6 | Matching across operators on a line (page-level glyph stream with index → (unit, span, glyph) map) | N1 |
| 2.7 | Widen D15 with full decoding: A85, DCT (decode and check dimensions), LZW and RL consuming decoders, and Cal*/Lab/ICCBased/Separation/DeviceN colour spaces fully validated. Each widening needs gs + differential repros showing no T5 fall-back. Inline image → XObject normalization on marked pages | 1, D15 |
| 2.8 | Pixel-level image redaction (D5 phase 2) | D5 |
| 2.9 | `RedactAndFlattenWithOptions` (P4, P5), returning the P8 report extended with per-page counts | P4, P5 |
| 2.10 | Per-document text-model cache (pulled into Phase 1 if the 1.14 benchmark misses its target) | 4 |
| 2.11 | *Candidate, needs a decision (Q1):* the lenient scanner (render, extract) uses the exact inline-image end when it can be computed (C10) | 1 |

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
- `tc_spread`, `split_string`, `split_keyword`, `split_comment` (C2);
- `zero_tf`, `zero_tz`, `degenerate_tm`, `wide_glyphs`.

Add every case from `malformed_content_test.go` and `deep_nesting_test.go`, and
new rows for N1–N6. Fixtures are generated in Go (the
`buildPDF` + object-map pattern already in `redaction_test.go`), not committed
binaries, and contain only synthetic secrets (SECURITY.md §7).

Each row declares:

- the content and objects;
- the operation (`RedactText` with a pattern, a full-page `RedactArea`, or a
  partial-area rect). `actualtext` and `actualtext_props` have both a
  `RedactText` row (pattern matches only the `/ActualText`; D14) and a
  full-page `RedactArea` row (D7);
- the expectation: `clean` (output produced and passes the assertions below),
  or `refused(reason)` per phase. The expectation flips from refused to clean
  when the step that closes it lands. A `clean` row must never regress to
  refused without review;
- `gsBlind bool` (C1). This is allowed **only on `clean` rows**: refused rows
  never run gs on output. Set it when gs `txtwrite` does not show the secret
  even on the *input*. Batch A found this for: strings or keywords split
  across streams, `0 Tf`, `0 Tz`, a singular `Tm`, tiling-pattern text,
  `/ActualText`, strings nested inside a TJ array, and some over-deep TJ
  fixtures. On such a row:
  - the gs input sanity check is skipped;
  - the fixture's validity is instead shown by our own deep extraction of the
    input containing the secret (or, for `/ActualText`, the decoded property
    value);
  - the row carries a comment naming the reader behaviour it models;
  - the gs output assertion still runs; it passes trivially but guards against
    regressions.
- until step 1.8: the reason is classified from the error chain plus the loss
  text (C8); afterwards from `RedactionError.Reason`.

For `clean`, the assertions are:

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
   - Sanity check, rows without `gsBlind`: gs on the *input* must show the
     secret. If not, the fixture is broken (or the row needs `gsBlind`, with a
     justification) and the test fails.
   - Optionally, non-redacted control text must survive (catches over-removal
     regressions).

For `refused`: `errors.Is(err, core.ErrRedactionUnsupported)`, the reason
matches, the returned document is nil, and the plan is sticky (later
`RedactAndFlatten` calls also fail).

### 12.2 Unit tests (table-driven)

- **Inline images, leak corpus rows (D15/D16, T5).** Each row is followed by
  `BT … (SECRET-1234) Tj ET` and a final `EI`, or has the text placed so that a
  fall-back reader would show it.
  - **`clean` rows**, where the image is accepted and the trailing text is
    redacted normally:
    - a valid unfiltered DeviceGray image and a valid unfiltered DeviceRGB
      image;
    - `/IM true`;
    - Indexed over DeviceRGB with an exact lookup;
    - a valid AHx image (with and without CR LF after `ID`);
    - a valid zlib Flate image;
    - `[/AHx /Fl]`;
    - a named colour space resolved via `InlineColorSpace`, once 1.4 has wired
      it.
  - **`refused(inline_image)` rows**, one per go-reviewer repro and at least
    one per class:
    - **B1:** `/I 5`; `/D` of length ≠ `2n`; incomplete
      `[/CalGray]`, `[/Lab]`, `[/Separation /X]`, `[/DeviceN [/A]]`; a
      complete ICCBased/CalRGB/Lab colour space (D15 non-device); an unknown
      key; a duplicate key (`/W` and `/Width`); `/DP << /Predictor 12 >>`;
      `/F [/Fl /DCT]` and `/F [/Fl /LZW]` (second filter); `/F [/Fl /AHx]`
      (`AHx` not first); Indexed with a short lookup or `hival` out of range.
    - **B2:** Flate whose decoded output exceeds `H × rowBytes`; Flate shorter
      than needed; any A85.
    - **B3:** raw DEFLATE (no zlib header).
    - **B4:** DCT that does not decode, and a valid DCT (D15 refuses all DCT
      in Phase 1).
    - **D16:** an otherwise valid unfiltered image whose bytes contain `Tj`,
      `TJ`, `) '`, `>"`.
  - Rows where gs shows nothing on the input carry `gsBlind`. Most B rows are
    *not* gs-blind: that is how they were found.
- **`parse`, inline-image boundary.** Every filter row of §6.1, crossed with
  T1–T6 pass and fail cases:
  - `EI` followed by a delimiter (loss, C4) and by end of content (pass);
  - single white-space vs CR LF after `ID` per filter: unfiltered, Flate and
    DCT refuse after CR LF; AHx and A85 pass, and refuse when `/L` is present
    (C5);
  - exact length with W/H/BPC/n combinations; named
  `/CS` resolved and unresolved; `/IM`; AHx with white-space and with an invalid
  byte; A85 `z` and `~>`; Fl truncated, with trailing junk, and with an
  embedded `EI`; DCT with an EXIF thumbnail (inner `FFD9`), progressive, and
  truncated; LZW/RL/CCF (loss in Phase 1); size overflow.
- **`parse`, segments.**
  - Splits between tokens are allowed, including between operands and their
    operator.
  - Each of these is a loss: a split inside a string, a name, a number, a
    keyword, inside inline-image data, and a comment running to the end of a
    stream (C2).
  - `Data` is byte-identical to `PageContent` for a single stream (no joins)
    and for arrays (a `\n` after every stream) (C3).
- **Property-list matching (D14, step 1.6).**
  - Inline dict and `/Properties` match, each with the key stripped and the
    page marked.
  - A match in a `BDC…EMC` sequence with no runs still marks the page.
  - Nested marked content.
  - `/Alt` and `/E` as well as `/ActualText`.
  - UTF-16BE and PDFDocEncoding values.
  - A non-matching property list leaves an unmarked page untouched.
  - `AutoRedactPII` with a PII value only in `/ActualText`.
- **Text model, nested TJ (C6, step 1.5).** `[ (A) [ (SECRET) ] ] TJ`, and
  deeper nesting within the depth bound: the nested strings are matched and
  their operator is removed.
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
- **Coverage gap (C9).** The macOS list is mostly icons. Batch A saw 13 inline
  images and 21 stream joins in 2,598 pages, all accepted. That is too little
  to judge the D1-A refusal rate or the straddle rule.
  - Step 1.14 adds a **text-heavy corpus**, passed through the same
    `FLUXDOCS_EXTRA_CORPUS` mechanism and not committed. It should cover:
    Word/LibreOffice exports, Chrome print, LaTeX, scanned+OCR, government
    forms, and older PDF 1.2–1.4 producers that emit inline images.
  - Its source list is recorded in the PR. The D15, D16 and D1-A refusal counts
    (and, for comparison, the D1-B counts) are reported per producer.
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
- **Baseline.** Refusal counts per reason, recorded by batch A at **71de860**
  (not 446eaea).
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
decided on 2026-10-15, and D14, D15 and D16 on 2026-10-16; see "Decisions
(maintainer-approved)" at the top. They are not repeated here. **No open item
below blocks Phase 1.** Batch D measured the inline-image, cost and
extension-frequency items below ("Batch D measurements"); the ≤ 50% cost
target is not met and is reported there with numbers.

**For the maintainer, not decided (from batch A, C10):**

- **Q1. Should the lenient scanner use the exact inline-image end?** The
  lenient scanner (used by `RenderPage` and `ExtractText`) would compute the
  exact end (§6.1 step 2) whenever it can, and fall back to today's
  white-space-delimited `EI` search only when it cannot. In `ii_tight_EI`, gs
  renders text that our renderer and extractor hide. Redaction is already safe
  here, because the strict scan refuses.
  - **For:** our viewer and extraction agree with exact-length readers. Users
    see what gs and Acrobat-class readers see.
  - **Against:** it changes Core render and extract output on malformed files,
    and needs golden updates. It would also make the lenient and strict paths
    share more code, which is a risk to the "lenient never fails" contract.
  - *Default until decided:* no change (Phase 2 candidate 2.11).

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
- **Measure.** Inline-image refusal rates on the text-heavy corpus (C9),
  broken down by rule: D15 (by filter, by colour space, by dictionary key), D16
  (T6 pattern), D1-A (`EI` pair), CR LF, and the straddle rule. The macOS list
  is not representative: 13 inline images and 21 joins in 2,598 pages. These
  numbers are the evidence a Phase 2 widening (2.7) or a D1-B relaxation needs.
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

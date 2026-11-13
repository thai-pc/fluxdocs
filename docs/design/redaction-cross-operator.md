# Design: redaction step 2.6 — matching across operators on a line

Status: design (not implemented). Base: `master` at f7fc4f5.
Parent: `docs/design/redaction-hardening.md` (Phase 2 table, step 2.6; C34
N-2; §5.4, §5.6, §6.5, §6.8, §6.11). This note is separate because the
parent is organized by Phase 1 items and is already ~1,800 lines; the parent's
step 2.6 row links here.
Tier: **Core (MIT)**, in this repository (`.claude/CLAUDE.md` §2: everything
here is MIT, `core/internal/redact` included; the parent note's "Pro" label
predates that decision). Packages: `core/internal/redact` (engine), `core`
(`redact.go`, `redact_extend.go`, `redact_verify.go`), and a small shared
`core/internal/textflow` extracted from `core/extract` (§3.2). No CGO, no
build tag, no public API change (doc-comment wording only, §9).
Targets: native and `GOOS=js GOARCH=wasm`, same code and the same caps. No
filesystem, no network: privacy path unaffected.
Spec basis: ISO 32000-1:2008 §9.3 (text state: `Tc`, `Tw`, `Tz`, `Tf`),
§9.4.1 (text objects), §9.4.2 (text-positioning operators), §9.4.3
(text-showing operators, `TJ`), §9.4.4 (text space details), §9.10
(extraction of text content; §9.10.2 `/ToUnicode`), §14.6 (marked content),
§14.8.2.3 (page content order: content order need not be reading order),
§14.9.4 (`/ActualText`). Clean-room: Ghostscript `txtwrite` is a black-box
oracle only.

---

## 0. Decisions in this note (summary)

| # | Decision | Section |
|---|---|---|
| X1 | 2.6 depends on **neither 2.2 (width model) nor 2.5 (glyph-level removal)**. It removes **every operator a match touches, whole** (Phase 1 granularity). | §2, §6 |
| X2 | The matchable stream is the **page's text in content order, joined exactly as `ExtractText` joins it** (rule L1): no separator between operators on one line, `\n` where extraction starts a new line, a space where extraction reads a `TJ` gap. Geometric reading order (L2) is a follow-up step, 2.6b. | §3 |
| X3 | The extraction stream is produced by **extraction's own state machine**, moved to `core/internal/textflow` and shared by `extract` and `redact`. The superset rule ("every pattern match in `ExtractTextByPage` is found") then holds by construction; a property test pins it. | §3.2, §3.4 |
| X4 | Besides the extraction stream, one stream per **reading slot** (the per-run readings of C15, joined with the same line separators). Today's per-run matching is kept, so every run matched today is still matched. | §3.3, §5.1 |
| X5 | Removing runs can join their neighbours into a **new** match. Matching runs to a **fixed point** over the stream with the removed runs left out, capped at 4 rounds (`budget_exceeded` past it). | §5.3 |
| X6 | D4 needles are searched across runs too: in strict page models (`FindNeedles`), in the pre-pass (`TextScanner`, per-slot page streams) and in the verifier's byte search of content streams (all shown strings of a stream joined in order). | §5.2, §8.1 |
| X7 | Forms, patterns, soft-mask groups and Type 3 text stay **refused** (Phase 1 reasons, unchanged). A match that would span page content and such a unit is never reached: the page is refused first. | §3.5 |

---

## 1. Problem and current state

Pattern `SECRET-\d+` (the leak corpus's `textPattern`). Four `knownLeak` rows
(`testing/security/redaction/leakcorpus_cases_test.go`, rows after
`actualtext_shared_form`):

| Row | Content | Why it is missed today |
|---|---|---|
| `split_bt` | `BT … 72 700 Td (SECRET-) Tj ET BT … 177.4 700 Td (1234) Tj ET` | two runs, `SECRET-` alone does not match `\d+` |
| `split_same_bt_tj` | `(SECRET-) Tj (1234) Tj` in one `BT` | same |
| `split_same_bt_TJ` | `[(SECRET-)] TJ [(1234)] TJ` | same |
| `split_same_bt_per_char` | `(S) Tj (E) Tj … (4) Tj` | eleven runs, none matches |

All four reproduce in **our** `ExtractText` (extraction joins the two pieces:
same text-space baseline, no separator) and in Ghostscript.

Code today:

- `interp.show` (`core/internal/redact/textmodel.go`) builds one `textRun`
  per text-showing operator, with `texts`: the distinct readings (C15: per
  font candidate `Text`, `AltText`, `ByteText`; the extraction reading with
  `TJ` gaps as spaces; raw Latin-1). The readings are deduplicated per run, so
  which slot a text came from is lost.
- `Page.Match` (`page.go`) runs the regexp over each run's readings
  separately. `FindNeedles` does the same for D4 needles; `TextScanner.Page`
  (`scan.go`) feeds each run's readings to `pageText` separately; the
  verifier's byte search (`objsearch.go` `contentStrings`) joins the strings
  of **one** operator only.
- `extract.PageText` (`core/extract/text.go`) interprets the lenient operator
  list in content order. It writes `\n` when the text line matrix's `f` moves
  by more than `0.5 × Tfs` (2.0 when `Tfs` is 0) after the first glyph was
  written; a `TJ` number ≤ −100 writes a space unless the output is empty or
  ends in space, `\n` or `\t`. It inserts **nothing** between operators
  otherwise. It ignores the CTM, `q`/`Q` (its font, size and leading are not
  restored), `gs /Font`, `Tc`, `Tw`, `Tz` and `Ts`, and does not descend into
  forms (D13). Pages are joined with `\f` by `Document.ExtractText`.

## 2. Dependencies (decided)

**Not on 2.2 (width model).** L1 uses no geometry: line breaks come from the
text-positioning operators exactly as extraction reads them. Widths matter
only for the geometric rule L2 (2.6b, §3.6), and even there run-level
interval positions suffice for a first version.

**Not on 2.5 (glyph-level removal).** A cross-operator match removes every
operator it touches, whole (X1). This is the Phase 1 rule (§6.8 "current,
keep as the fallback") applied to a set of operators instead of one.
Over-removal is bounded by what one operator shows, exactly as today: when a
producer writes a whole line with one `TJ`, a match touching it removes the
line. When 2.5 lands, the index map's reserved glyph column (§4) narrows
removal to the matched glyphs; nothing in 2.6 has to change for that except
filling the column.

**Not on 2.3 (COW of forms).** Text in forms is refused before any matching
(X7). When 2.3 lands, a form's runs are spliced into the page stream at its
`Do`, in invocation order (this is what the step title's "(unit, span,
glyph)" anticipates). The map stores a run index; the run carries its unit
path once units have runs.

**Uses 2.10 (page-model cache, already landed as C77).** Streams are built
with the model and cached with it; `Page.Size` counts them (§7.3).

Smallest safe step: X1–X7 close the four rows and the latent D4 variant
(§10.1, `d4_split_other_page`) with no geometry and no new removal mechanism.

## 3. What forms one matchable stream

### 3.1 Options

| Option | Stream | For | Against |
|---|---|---|---|
| **L1 (chosen)** | Content order, extraction's separators | The superset rule holds by construction (identical haystack). No geometry. Closes all four rows. Deterministic, cheap | Misses text a geometric reader joins but extraction does not: content order reversed against reading order (§14.8.2.3 allows it), a CTM that puts two text objects on one visual baseline with different text-space `f`, rotated per-glyph `Tm` |
| L2 | Runs clustered by page-space baseline (after CTM), sorted by x within a line | Models Ghostscript/viewer copy order; catches the L1 misses | Needs run baselines and directions in page space; heuristics (baseline tolerance, column gaps) that readers do not share; more streams |
| L3 = L1 ∪ L2 | Both streams, matches united | Strictly more than either; L2 can only add removals, so a rough L2 is safe (I1) | Cost of both; L2 heuristics still need gs rows to justify them |

L1 is the minimum: it is what `ExtractText` (the mandatory gate) shows, and
the leak harness compares against it. L3 is the target, as step 2.6b
(open question Q1), after the gs measurements of §10.1's 2.6b rows.

### 3.2 The extraction stream: shared state machine (X3)

`extract.textState` moves, unchanged, to `core/internal/textflow`:

```go
// Flow is text extraction's reading of one content stream: shown text in
// content order, a newline when the text line matrix moves to a new line
// (|Δf| > 0.5×Tfs), a space for a TJ adjustment ≤ −100 (§9.4.3).
type Flow struct { /* the fields of extract.textState */ }

// Fonts resolves a Tf name the way extraction does (the page's /Font
// dictionary; nil for a missing font, which decodes as Latin-1).
type Fonts func(name parse.Name) *fontmodel.Font

func New(fonts Fonts) *Flow
// Apply interprets one operator; owner is attributed every byte the
// operator writes (Owned reports the pieces).
func (f *Flow) Apply(op string, operands []parse.Object, owner int32)
func (f *Flow) String() string
```

`extract.PageText` becomes `textflow.New(buildFonts…)` + `Apply` over
`parse.ScanContent` with `owner = -1` — **no behaviour change**, confirmed by
the existing `extract` tests and by byte-identical extract hashes on the
corpus (the same gate as step 1.3). The redaction interpreter feeds the same
`Flow` the strict spans, with `owner` = the run index for text-showing
operators and −1 for every other operator. On strict-accepted content the
strict and lenient scanners are expected to yield the same operators with the
same operands (a strict scan is lossless, and C40 already checks lenient
spans with the strict operand rule), so the extraction stream equals
`PageText` by construction; property P1 (§10.3) turns that expectation into
a gate, and any difference it finds is fixed in the wiring, not tolerated.

The `Flow` mirrors extraction **including its non-conformances** (no `q`/`Q`
restore of font and leading, `gs /Font` ignored, CTM ignored). That is the
point: the superset rule is about what `ExtractText` shows. Fixing extraction
(D13, `q`/`Q`, `gs /Font`) is a separate Core change; it then changes both
sides at once.

Fonts: `Flow` resolves fonts as `extract.buildFonts` does
(`fontmodel.Load(r, ref, fontmodel.Options{})` on the page's `/Font`
entries). The redaction walker reuses its own loaded font for the same object
when the options are equal, otherwise loads it separately, charged to the
page's decode budget.

Why not duplicate extraction's rules inside `redact` and rely on the property
test alone: two copies drift, and the test only sees the corpus.

### 3.3 Reading slots (X4)

A reader picks one decoding policy and applies it to the whole page. Each
policy is a **slot**; a slot's stream is every run's reading in that slot, in
content order, joined with the extraction stream's newlines (the same number
of `\n` before each run) and **no** `TJ`-gap spaces (as the per-run readings
of C15 have none: kerning never splits a match, 1.5).

| Slot | Per run | Fallback when the run has no such reading |
|---|---|---|
| `extract` | the `Flow` bytes (§3.2) | — |
| `text[k]` | candidate font *k*'s `Text` (strings at any `TJ` depth) | `text[0]` |
| `alt[k]` | candidate *k*'s `/Differences` glyph-name reading | `text[k]` |
| `byte[k]` | candidate *k*'s one-byte-per-code reading | `text[k]` |
| `latin1` | raw bytes as Latin-1 | — |

*k* ranges over candidate rank (C14: innermost scope first, then fallbacks,
then the nil font), at most `maxFontSet` = 8. A run's per-slot reading
already exists in `interp.show`'s buffers (the fallback copies are already
written there); 2.6 records, per run, the slot → `texts` index (`slot
[]uint8`, ≤ 26 entries) instead of losing it in the deduplication.

**Materialization.** A slot stream is built only if it differs from every
stream built before it in the order above: assembly keeps a slot as an alias
of an earlier stream while their pieces are equal and materializes it at the
first difference. A typical simple-font page needs `extract` and `text[0]`
(they differ by `TJ` spaces and nested strings); `latin1` is the same as
`text[0]` for WinAnsi ASCII; an Identity-H page adds `latin1`.

**Not modelled (documented assumption).** A reader that uses glyph names for
one font and `/ToUnicode` for another, both fonts having both. Each slot
applies one policy everywhere; per-font mixing of policies is not a coherent
reader and is out of scope like a lying `/ToUnicode` (parent §2).

### 3.4 Superset rule and its proof

Claim: for every page `RedactText` accepts and every regexp `re`, every
non-empty match of `re` in `ExtractTextByPage(page)` is found by `Match(re)`,
and every run owning a byte of that match is marked.

Proof obligation, in two parts:

1. **Identity.** The `extract` slot stream equals `ExtractTextByPage` byte
   for byte. By construction (§3.2); pinned by property test P1 (§10.3).
2. **Mapping.** `FindAllStringIndex` over identical strings gives identical
   matches (same engine, same input); the index map (§4) maps each match's
   bytes to their owner runs. Pinned by P2.

A match made only of separator bytes (pattern `\n`) owns no run and marks
nothing; it marks nothing today either (no run reading holds an
extraction-inserted newline). Such a match survives in the output's
`ExtractText` too, which is correct: no shown text produced it.

Scope: per page. A match across the `\f` page separator of
`Document.ExtractText` is out of scope: `RedactText` is page-scoped (Q5).

### 3.5 Line grouping, case by case (L1)

| Case | L1 result | Notes |
|---|---|---|
| Same text object, several `Tj`/`TJ`/per character | joined | the four rows |
| Adjacent `BT…ET` on the same text-space `f` (± 0.5 Tfs) | joined | `BT` resets `tlm` but extraction keeps `lastLineY`; `split_bt` |
| `Td` with `ty` = 0 (or small) between pieces | joined | sub/superscript moves stay on the line |
| New line (`T*`, `'`, `"`, `Td` with \|Δf\| > 0.5 Tfs) | `\n` between | a pattern matches across it only if it can match `\n` (e.g. `SECRET-\s*\d+`) |
| Content order ≠ reading order | content order | **L1 miss**; 2.6b |
| CTM puts pieces on one visual line, different text-space `f` | `\n` between | **L1 miss** (extraction ignores the CTM); 2.6b |
| Two columns at the same `f` | joined | over-removal possible (I1); `ExtractText` shows them joined too |
| `TJ` gap ≤ −100 between pieces | `extract`: space; `text[k]`: joined | `[(SECRET-) -250] TJ [(1234)] TJ` matches in `text[0]` |
| `Tc`, `Tw`, `Tz`, `Ts` | no effect | extraction ignores them; L2 would use them for gaps |
| Different fonts (`Tf` between pieces) | joined | each piece decoded with its own font in every slot |
| Rotated text (`Tm` with rotation), pieces in one text object | joined | no line move |
| Rotated, one `Tm` per glyph along the line | `\n` when \|Δf\| > 0.5 Tfs | follows extraction; gs measurement decides a 2.6b row |
| Marked content `BMC`/`BDC`…`EMC` between pieces | joined | extraction ignores marked content |
| `/ActualText` replacing glyphs | not substituted | extraction ignores it; an "ActualText-substituted" stream is Q4 |
| Content array, pieces in different streams (split between tokens) | joined | `PageContent` inserts `\n` into the *content bytes*, not into the text; a token straddle is refused (N5) |
| Inline image, path, `Do` of a text-free form between pieces | joined | they show no text |
| Form / pattern / soft mask / Type 3 showing a piece | page refused | `form_text`, `pattern_text`, `softmask_text`, `type3_text` (X7) |
| Hyphenation: `SEC-` end of line, `RET-1234` next | not matched by `SECRET-\d+` | the text is `SEC-\nRET-1234`; Q9 |
| Soft hyphen (U+00AD from `/ToUnicode`) at a line end | not matched by `SECRET-\d+` | the regexp sees the soft hyphen; D4 needles fold it (skeleton) |

### 3.6 Step 2.6b (proposed, not designed here in full)

L2 stream: each run's baseline origin and direction in page space
(`tm × CTM` at its first glyph; `penX/penY` intervals already exist), runs
clustered into lines by direction and baseline distance ≤ 0.5 × the run's
vertical em, ordered by the lower end of their x interval along the
direction, joined with no separator (a gap larger than one em could become a
space; joining without one over-matches, which is safe). Matches are added to
L1's. Inputs needed: the gs measurements of the 2.6b rows (§10.1).

## 4. Index map

```go
// stream is one reading of the page's text in content order (§3).
type stream struct {
	slot  slotID
	text  string
	offs  []int32 // offs[i]: where piece i starts in text; pieces are contiguous
	owner []int32 // owner[i]: index into Page.runs, or -1 for a separator
	// glyph is reserved for step 2.5: the first glyph index of piece i in its
	// run, per slot (nil in 2.6: matches remove whole runs).
	glyph []int32
}
```

- One piece per run with a non-empty reading in the slot, and one per
  separator sequence. For the `extract` slot, every byte `Flow` writes while
  applying a run's operator (its `TJ` spaces, including a leading one that
  depends on the preceding output) belongs to that run's piece.
- **Lookup.** Match `[a, b)` → pieces `sort.Search(offs, a)` … the last piece
  starting before `b`: O(log P + k) for k pieces. Owners ≥ 0 are set in a
  bitset over runs (one bit per run, as `Rewrite`'s removal bitset), so a run
  touched by many matches is marked once.
- **Size.** P ≤ runs + positioning operators. Two `int32` per piece: at the
  C27 bound of 4 Mi spans, at most 32 MiB per materialized stream; a
  realistic page has a few thousand pieces.
- **Unit.** In 2.6 every run is in the page unit. 2.3 adds a unit path to
  `textRun`; the map keeps a run index.
- **Determinism.** Streams are built in content order and slot order; marks
  are emitted in run order and deduplicated; rects in run order. No map
  iteration is involved.

**Assembly is a function, not a byte copy:** `assemble(keep func(run int)
bool) []stream`. The `Flow` records, per run, its piece bytes and whether it
started with a pending `TJ` gap, and, per positioning operator, the newline
decision inputs (`f`, `Tfs`). Assembly with some runs dropped recomputes the
`emitted` state (a newline is written only after the first kept glyph) and
the pending-gap space against the preceding kept byte, so
`assemble(keep)` equals what extraction reads from the rewritten page
(property P3). Closure (§5.3) and `FindNeedles(done)` use it.

## 5. Matching

### 5.1 `Page.Match(re)` (RedactText, AutoRedactPII)

1. Per-run matching, unchanged (today's code): keeps every match today
   finds, including those that depend on run-local context (`^`, `$`, `\b`
   at a run's edges).
2. For each materialized stream: `re.FindAllStringIndex(text,
   maxStreamMatches+1)`. More than `maxStreamMatches` (4 Mi, the span cap)
   is `budget_exceeded` (§7.1). Each non-empty match marks its owner runs;
   the matched substring is a match needle (§6.11 rules unchanged: ≥ 4
   skeleton characters propagate).
3. Closure (§5.3) with this pattern alone.
4. `Rects`: each marked run's display box (`textRun.disp`, C17), once per
   run. `Marks`: one span mark per run, in run order.

Needles shorter than `MinMatchNeedle` skeleton runes are dropped in `Match`
itself (today `appendNeedles` keeps every non-empty match and the threshold
applies downstream), so a `.` pattern over a large page does not hold
millions of one-character strings.

### 5.2 Needles (D4) across runs

- **`FindNeedles(n, done)`** (strict models: caller pages, extended pages,
  verifier check 1): per-run search as today, plus one `Needles.Find` over
  the skeleton of each stream assembled with `done`'s planned runs dropped.
  The skeleton removes white space, so a needle matches across line breaks
  (Q3). The skeleton is built in chunks that end on NFKC boundaries
  (`norm.NFKC` `LastBoundary`), each chunk keeping its owner list, so a hit
  maps to every run of the chunks it touches (over-removal at a boundary
  that composes, never under).
- **`TextScanner.Page`** (pre-pass, text only, no runs kept): `pageText`
  receives `(slot, reading)` instead of distinct readings. The scanner
  appends each slot's skeleton to a per-slot buffer and searches it in chunks
  of `max(1 MiB, 4·L)` with an overlap of L−1 runes (L = longest needle
  skeleton): O(n·(1 + L/chunk)) ≤ 1.25 n per slot. A hit sets
  `PageHits.Content` as today; the strict model then places the runs
  (`FindNeedles`). Units read for text (`w.texts`) get the same per-unit
  stream, so a needle split across two operators of a form on another page is
  that unit's refusal, as a whole needle in it is today.
- **Verifier byte search** (`contentStrings`): besides each operator's
  strings, the strings of all text-showing operators of the stream joined in
  order are searched (`FindLatin1`/raw, or `FindBytes`). This keeps C63's
  argument sound: the pre-pass's `latin1` slot stream is exactly that joined
  string read as Latin-1, so a stream the pre-pass read without a hit has no
  joined Latin-1 hit either.

Longest needle: a matched substring can be long (`.*` over a line). Needles
longer than `maxNeedleRunes` = 1024 skeleton runes are truncated to their
first 1024 runes. Any occurrence of the full needle contains the prefix, so
truncation can only add hits (over-removal), and it bounds L.

### 5.3 Closure: removal must not create a match (X5)

Removing a run makes its neighbours adjacent. With `SECRET-\d+|X` and runs
`SECRET-`, `X`, `1234`, the source stream `SECRET-X1234` matches only `X`;
the output shows `SECRET-1234`, a match of the caller's pattern. Without a
fix, verification check 1 (`Match` on the output page) fails the call with
`ErrRedactionVerification` — safe, but a bug report for a legitimate request.
The same happens when a region (`RedactArea`) or settle (C59) removes the
middle run, and for D4 needles.

```
removed := marks ∪ runs meeting regions
for round := 0; ; round++ {
    removed ∪= settle(regions, removed)            // C59, when regions exist
    streams := assemble(keep = not removed)
    new := runs touched by matches of every page pattern and every needle in streams
    if new ⊆ removed { break }
    if round == maxMatchRounds { refuse budget_exceeded ("text matching") }
    removed ∪= new
}
```

- **Where.** Once inside `Match` (its own pattern, no regions: rounds only
  when step 2 removed something); and once inside `Rewrite`, which gets the
  page's patterns and propagating needles in addition to regions, boxes and
  marks (an internal `RewritePlan` struct; `core/redact.go rewritePage`
  passes `plan.patterns[i]` and `ns.prop`). The black boxes of runs closure
  adds are painted (their `disp`).
- **Termination.** `removed` grows by at least one run per round or the loop
  ends; `maxMatchRounds` = 4 bounds the work. Real content needs a crafted
  alternation for a second round; the corpus regression records the round
  histogram (expected: all pages ≤ 1 round).
- **Needles.** Matches found only by closure do **not** become match needles:
  the source never showed that text contiguously, and keeping N fixed before
  the first build keeps §6.11's termination argument intact (Q2).
- **Soundness link.** `assemble(keep)` equals what extraction reads from the
  rewritten page (P3), so after closure the output's `Match` and
  `FindNeedles` find nothing: verification check 1 passes for the reason it
  checks.

## 6. Removal (unchanged mechanism)

Marks are span marks (§5.4, §6.5): each run touched by a match is removed
whole, whatever its geometry. Quote operators keep their line moves (C18).
Later runs of the same epoch shift back exactly as for a per-run match today
(width leak: §6.8; 2.5 collapses it). D6 (glyph granularity) does not apply
until 2.5.

Over-removal in practice: a match touching a run removes everything that run
shows. Per-character producers lose exactly the matched characters; line-`TJ`
producers lose the lines the match touches (today: the line holding a
complete match). The `RedactText` doc comment already states per-operator
removal.

## 7. Budgets, complexity, cost

### 7.1 Bounds

| Quantity | Bound | How |
|---|---|---|
| Stream bytes, all slots | ≤ the reading bytes `show` already charges to `maxOpVisits` (every slot buffer, fallbacks included, is charged today) + one separator per positioning operator per stream | no new charge needed for building |
| Materialized streams | ≤ 3·8 + 2 = 26; typically 1–3 | aliasing (§3.3) |
| Index map | 8 bytes per piece per materialized stream | ≤ 32 MiB per stream at the span cap |
| Regexp work | RE2: linear in stream length per pattern per round | new per-page **match budget** `maxMatchWork` = 64 Mi bytes scanned (streams × patterns × rounds); past it `budget_exceeded` |
| Matches | `maxStreamMatches` = 4 Mi per stream per round (≈ 170 MB of `[][]int` at the cap) | `FindAllStringIndex(…, n+1)` |
| Closure rounds | `maxMatchRounds` = 4 | §5.3 |
| Needle search | O(n·(1 + L/chunk)) per slot; L ≤ 1024 | §5.2 |

The match budget is separate from `maxOpVisits` so that pages `RedactArea`
accepts today are not refused because of matching they do not do. The
existing worst case of `Needles.Find` (candidate verification on a
repetitive haystack, O(n·L)) is not new: today a single large `TJ` run gives
the same haystack; truncation to 1024 runes now bounds it.

### 7.2 Estimated cost (to be measured in the implementation PR)

Not measured in this note. Estimates from the C77/C27 measurements:

- Typical 40-line page (~3 KB of text): `Flow` pass +10–20% of the strict
  model build (one more decode per operator, cheap for one-byte fonts with
  `ByteTables`; can reuse the run's extraction reading when `Flow`'s font is
  the run's primary font); assembly and 1–3 regexp passes of a few KB: tens of
  µs. `RedactText` per page: +10–25%. `RedactAndFlatten`: closure pass on
  redacted pages only, < +5% on 20-page documents.
- Pre-pass: one skeleton buffer per slot and one chunked `Find`; ≤ +5% of the
  0.15 ms per page measured in C77.
- Hostile per-character page at the span cap (C27 fixture): ~4 MiB per
  stream; `SECRET-\d+` scans at memchr-like speed on its literal prefix;
  `.` produces 4 Mi matches. Target: ≤ 1.5× time and allocation of the same
  call with stream matching disabled (§10.6).

### 7.3 Memory and the cache

Per-run readings are kept (they serve step 1 of §5.1), so the model holds
roughly twice the reading bytes. `Page.Size` (C83) adds each materialized
stream's text and map; the 128 MiB LRU holds fewer models of text-heavy pages
in exchange. WASM: same caps; the worst case is bounded by the same budgets.

## 8. Interactions

### 8.1 Verification (§5.6)

- Check 1 (`Match` per page pattern, `FindNeedles` on each redacted output
  page) now sees cross-operator text, so it is stronger with no change of its
  own. Closure (§5.3) is what keeps it from failing legitimate requests.
- Check 3b (byte search): joined strings per content stream (§5.2).
- No new verification failure mode. A cross-operator residual in `R_u` is
  attributed like any other (page text → extension; unit → its refusal).

### 8.2 Refusal reasons

No new reason. New details under `budget_exceeded`: "text matching" (match
budget, match cap, closure rounds). Details never quote document text.

### 8.3 `RedactArea`

Geometry unchanged: regions remove runs whose box meets them (closed
intervals), D5 removes paint items. Two indirect effects: closure re-matches
the page's `RedactText` patterns and the needles after region and settle
removals; area needles (≥ 8 skeleton runes) are found across runs on other
pages (D4). Both only add removals.

### 8.4 D-rules and corrections

| Rule | Effect |
|---|---|
| D4 / §6.11 | needles searched across runs everywhere (§5.2); closure matches add no needles; termination unchanged |
| D5, C33, C46 | unchanged (paint items) |
| D6 | not applicable until 2.5 |
| D7, D14 | unchanged; a cross-operator match marks the page, so D7 strips every property list |
| C15 | readings unchanged; slot identity recorded |
| C17 | rects stay display boxes, one per run |
| C59 settle | runs inside closure (§5.3) |
| C62, C63 | pre-pass reads per-slot streams; the verifier's skip of streams the pre-pass read stays sound (§5.2) |
| C77, C83 | streams cached with the model and counted |

## 9. API and documentation

- No exported Go API changes. Internal: `Page.Rewrite` takes a
  `RewritePlan{Regions, Boxes, Marks, Patterns, Needles}`;
  `TextScanner`'s `pageText` callback gains a slot argument;
  `core/internal/textflow` is new (internal).
- `RedactText` doc comment: "one text-showing operator at a time" becomes
  "over the page's text as `ExtractTextByPage` reads it — content order, with
  no separator between operators on one line — and over each run alone; a
  match removes every operator it touches, whole".
- `.claude/SECURITY.md` §1 "Known limitations": drop the cross-operator
  limitation; add "geometric reading order is not modelled (2.6b): content
  shown out of reading order, or placed on one line by the CTM, is matched in
  content order". Maintainer edit (this note does not touch `.claude/`).
- `docs/spec.md` §13.4: same wording as the doc comment.

## 10. Test plan

### 10.1 Leak corpus rows (`leakcorpus_cases_test.go`)

Flip to `clean` (our checks and Ghostscript): `split_bt`,
`split_same_bt_tj`, `split_same_bt_TJ`, `split_same_bt_per_char`.

New rows, all `ops: opText` unless noted, Helvetica `/F1 24`:

| Row | Content | Expect |
|---|---|---|
| `split_three_tj` | `(SEC) Tj (RET-) Tj (1234) Tj` | clean |
| `split_three_bt` | three `BT…ET` on `f` = 700, x by Helvetica widths | clean |
| `split_same_line_td` | `(SECRET-) Tj 105.3 0 Td (1234) Tj` | clean |
| `split_mixed_fonts` | `(SECRET-) Tj /F2 24 Tf <0031003200330034> Tj` (Identity-H with `/ToUnicode`, `identityHFont`) | clean |
| `split_TJ_gap_space` | `[(SECRET-) -250] TJ [(1234)] TJ` | clean (`text[0]` joins; `extract` has a space) |
| `split_tc_tw_tz` | `(SECRET-) Tj 5 Tc 10 Tw 150 Tz (1234) Tj` | clean |
| `split_across_bdc` | `/Span <<>> BDC (SECRET-) Tj EMC (1234) Tj` | clean |
| `split_rotated_same_bt` | `0 1 -1 0 200 100 Tm (SECRET-) Tj (1234) Tj` | clean |
| `split_content_array` | `/Contents [a b]`, `a` ends after `(SECRET-) Tj`, `b` starts `(1234) Tj ET` | clean |
| `split_per_char_TJ` | `[(S)] TJ [(E)] TJ …` | clean |
| `split_partial_line` | `(Code: SECRET-) Tj (1234 end) Tj` | clean (whole operators go, control text with them; documented over-removal) |
| `split_line_break_ws` | `(SECRET-) Tj 0 -30 Td (1234) Tj`, row pattern `SECRET-\s*\d+` | clean |
| `split_page_and_form` | page `(SECRET-) Tj`, then `/Fm Do` showing `(1234)` | refuse `form_text` |
| `d4_split_other_page` | page 0 shows `S` whole, page 1 `(SECRET-) Tj (1234) Tj` (`twoPages`), ops text and area | clean (page 1 extended) |
| `d4_split_other_page_form` | page 1's form shows the two pieces | refuse `form_text` (detail "automatic extension: …") |

`split_line_break_ws` needs a per-row `pattern` field on `leakCase` (test
change only; default `textPattern`). The plain line-break case
(`SECRET-\n1234` with `SECRET-\d+`) is **not** a leak-corpus row: the harness
compares with white space stripped, so it would report a leak although no
reader shows a match of the pattern. It is a unit test (§10.2).

Rows for **2.6b**, added only after measuring Ghostscript on the input (a
row whose input gs does not show joined is dropped, not marked `gsBlind`):
`split_reversed_order` (`1234` at x 177.4 written before `SECRET-` at x 72,
same `f`), `split_cm_same_baseline` (second text object at `f` 1000 under
`1 0 0 1 0 -300 cm`), `split_rotated_per_tm` (one rotated `Tm` per glyph).
Expected `knownLeak`, `closesIn: "2.6b …"`.

### 10.2 Unit tests (`core/internal/redact`, `core/internal/textflow`; table-driven)

- `textflow`: every rule of §1 against `extract`'s existing cases, plus
  ownership: `TJ` spaces owned by their run, a leading pending gap, empty
  strings, newline counts with several moves between runs, `q`/`Q` not
  restoring (mirrors extraction).
- Assembly: `assemble(keep)` with the first run dropped (no leading `\n`),
  with a run between two gaps dropped (one space, not two), aliasing of equal
  slots, divergence materializes.
- Index map: match on a piece boundary, inside a piece, separator-only,
  spanning a separator, last piece; bitset deduplication.
- Matching: per-run anchors still match (`^SECRET` on a later run, `\bSEC`);
  Identity-H `latin1` slot across runs; `/Differences` `alt[0]` across runs;
  font candidates (`text[1]` from a fallback scope).
- Not matched: `SECRET-` / new line / `1234` with `SECRET-\d+`; `SEC-` /
  new line / `RET-1234`; soft hyphen at a line end (regexp does not match;
  `FindNeedles` with the needle `SECRET-1234` does).
- Closure: `SECRET-\d+|X` over `SECRET-`, `X`, `1234` (all three removed);
  region removes the middle run (`core` test: `RedactText` + `RedactArea`,
  `RedactAndFlatten` succeeds, the secret is gone); settle-moved run;
  a crafted chain (pattern `ab`, runs `a`×k then `b`×k) refuses after 4
  rounds.
- Determinism: `Match` twice gives identical marks and rects.
- `Page.Size` grows by the streams.

### 10.3 Property tests (superset rule)

In package `core` (`core/redact_stream_test.go`; `testing/` cannot import
`core/internal`), over `testing/corpus/**` and `FLUXDOCS_EXTRA_CORPUS` when
set, every page on which the strict model builds, and every leak-corpus
fixture (always run):

- **P1 identity:** the `extract` stream == `ExtractTextByPage`, byte for
  byte.
- **P2 mapping:** for patterns `.`, `\d{4}`, `[A-Za-z]{5,}`, `\S+ \S+`,
  `(?s).{3}`, `^.`, `.$`: every non-empty match in `ExtractTextByPage` has
  all its owner runs in `Match(re).Marks`.
- **P3 output model:** after `RedactText(p, `\d`)` and a half-page
  `RedactArea`, `ExtractTextByPage(output)` == `assemble(keep)` of the
  source, and neither holds a match of the patterns.

Any P1 difference is a bug in the `Flow` wiring, not a tolerated deviation.

### 10.4 Corpus no-regression (`testing/corpus/redaction_regress_test.go`)

- Outcomes per operation unchanged for `text/p*` and `text/flatten`
  (`RedactText(page, ".")` already marks every run with a non-newline
  character, so the removal set is the same) — this must be exactly true.
- `area-full`/`area-half`: extension counts may rise (area needles found
  across runs and lines); report the delta per file. New refusals allowed:
  `form_text/d4` only, each listed. Verification failures, `not_converged`,
  internal errors, `nonempty_text`: 0.
- New diagnostic op `text-digits` (`RedactText(page, `\d{4,}`)`): report the
  runs marked only by streams, and the closure-round histogram.
- Render and extract hashes byte-identical (the `textflow` move).

### 10.5 Fuzzing (`testing/security/redaction/fuzz_test.go`)

New invariant: for any input on which `RedactText(0, p)` for `p` in
{`SECRET-\d+`, `\d{3,}`, `(?s)E.C`} and `RedactAndFlatten` succeed,
`ExtractTextByPage(output, 0)` has no match of `p`. Seeds: every row of
§10.1 and a small per-character flood. P1 runs in a core fuzz target on the
same seeds.

### 10.6 DoS gates (relative to a reference measured in the same test run)

- `TestDoS_RedactCrossOperatorFlood`: the C27 per-character page at the span
  cap, `RedactText` with `SECRET-\d+` and with `.`, then `RedactAndFlatten`:
  time and allocation ≤ 1.5× the same call with stream matching disabled by a
  test hook; doubling the operators past the cap does not grow allocation.
- `TestDoS_RedactStreamMatchCap`: a page with more than 4 Mi digit matches,
  pattern `\d`: `budget_exceeded`, allocation bounded.
- `TestDoS_RedactClosureRounds`: the crafted chain: `budget_exceeded` after
  4 rounds, time ≤ 5× one round.
- `TestDoS_RedactSlotStreams`: 8 font candidates per `Tf` on a large page:
  materialized streams and memory within §7.1.
- `TestDoS_PrepassStreams`: 2,000 per-character pages with propagating
  needles: pre-pass time ≤ 1.3× the per-run reference.

## 11. Implementation steps and effort

| # | Step | Estimate |
|---|---|---|
| a | `core/internal/textflow` from `extract.textState`; `extract` migrated with no behaviour change (corpus extract hashes) | 1 day |
| b | Slot identity per run; `Flow` in `interp`; `stream`, `assemble`, index map, `Page.Size` | 1.5 days |
| c | `Match` streams + closure; `RewritePlan` with closure; `core` wiring | 1.5 days |
| d | `FindNeedles` streams, `TextScanner` per-slot buffers, unit streams, `contentStrings` joined search, needle truncation | 1 day |
| e | Tests: rows, unit, P1–P3, fuzz, DoS gates | 2 days |
| f | gs measurements for 2.6b rows, corpus regression run, doc updates | 0.5–1 day |
| | **Total** | **7.5–8 days** |

Step a lands first and alone (Core, no behaviour change); b–d need the
redaction 100% gate.

## 12. Open questions (recommendation each)

- **Q1. Geometric reading order (L2).** Ship 2.6 with L1 only and design 2.6b
  after measuring gs on the 2.6b rows? *Recommendation:* yes. L1 is what the
  mandatory gate reads; L2 needs measured reader behaviour, and producers
  that write text out of reading order (tables, some typesetters writing by
  font) are real, so 2.6b should follow soon after.
- **Q2. Do matches found only by closure propagate as needles?**
  *Recommendation:* no. The source never showed that text contiguously, and
  a fixed needle set before the first build is what makes extension
  terminate.
- **Q3. Needles across line breaks.** The skeleton drops `\n`, so a needle
  wrapped onto the next line on another page matches. *Recommendation:*
  accept (I1, "white space must not decide a match"); report the extension
  count change on the corpus.
- **Q4. `/ActualText`-substituted stream** (a reader that copies the
  property text in place of the glyphs of a marked sequence; a match may span
  an `/ActualText` value and neighbouring glyphs). *Recommendation:* add it to
  2.6b as one more slot; gs does not show `/ActualText`, so its rows are
  checked by our own extraction only.
- **Q5. Matches across pages** (`\f` in `Document.ExtractText`).
  *Recommendation:* out of scope; `RedactText` is page-scoped. Document it.
- **Q6. Shared `textflow` vs. duplicated rules.** Sharing touches Core
  `extract` (no behaviour change). *Recommendation:* share (X3); identity by
  construction is worth one refactor.
- **Q7. Over the match cap: refuse, or remove every run of the page?**
  *Recommendation:* refuse (`budget_exceeded`). Only crafted or absurd input
  reaches 4 Mi matches on one page, and a refusal is explicit.
- **Q8. `RedactText` return value for a cross-operator match:** one rect per
  run (C17, today's shape) or one union per match and line?
  *Recommendation:* one per run; a per-character match then returns eleven
  rects, which callers already handle.
- **Q9. Hyphenation and soft hyphens at line ends.** Should `RedactText`
  fold `-\n` or U+00AD + `\n`? *Recommendation:* no; the pattern author
  controls it (`SEC-?\s*RET-\d+`). Document an example. D4 needles already
  fold soft hyphens and white space.

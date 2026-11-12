# Design: cross-reference reconstruction (brute-force object scan)

Status: **Phases 0–2 implemented** (manifest gate and census, repair ladder,
object scan); Phase 3 (ObjStm expansion) and Phase 4 (full corpus, fuzz and
DoS sweep) are not. The maintainer's §11 decisions and the deviations found
while implementing are in §12.
Tier: **Core (MIT)**. Packages: `core/parse` (new `repair.go`, plus hooks in
`resolver.go`), `core` (`open.go` repair ladder, `RepairReport`), `wasm`
(one additive result field), `packages/core` (TS mirror). Pure Go, no CGO, no
build tag. Both targets (native, `GOOS=js GOARCH=wasm`).

Basis (clean-room, public specifications only):

- ISO 32000-1:2008 §7.3.10 (indirect objects; a reference to an undefined
  object is null), §7.5.2 (file header), §7.5.3 (file body), §7.5.4
  (cross-reference table), §7.5.5 (file trailer, `startxref`), §7.5.6
  (incremental updates), §7.5.7 (object streams, including the list of objects
  that shall not be stored in one), §7.5.8 (cross-reference streams; §7.5.8.4
  hybrid files), §7.6.1 (which strings and streams are encrypted), Annex C
  (implementation limits: largest indirect object number 8,388,607).
- Adobe *PDF Reference*, sixth edition (1.7), Appendix H implementation notes:
  a reader accepts the header anywhere in the first 1024 bytes. This is
  already the rule in `core/open.go` (`headerScanLimit`).
- `.claude/SECURITY.md` §2 ("safe brute-force fallback ... stay within resource
  limits"), `.claude/CLAUDE.md` §6 ("lenient parsing + brute-force scan
  fallback"), `.claude/CONVENTIONS.md` §4 ("recovery has limits; those limits
  are hard").

**Never** read or paraphrase the source of pdf.js, MuPDF, PDFium, poppler, qpdf,
pdfcpu, Ghostscript, iText, Nutrient or Apryse (SECURITY.md §6). The algorithm
below is derived from the file structure ISO 32000-1 §7.5 defines.

---

## 1. Problem and measured state

`parse.NewResolver` builds the object table only from the cross-reference data:
`FindStartXref` → `ParseXref` → the `/Prev` chain (`core/parse/xref.go`). Any
failure on that path makes `core.OpenBytes` return `ErrInvalidPDF`
(`core/open.go:52-55`). There is no fallback. Objects whose bytes are intact
are unreachable when the table that locates them is broken.

### 1.1 The malformed corpus at HEAD `5828c31`

An audit reported that 5 of the 6 files in `testing/corpus/malformed/` fail to
open. **Confirmed: 5 of 6 fail.** This was established by tracing
`core.OpenBytes` over each file's bytes and `testing/corpus/generate.go`. The
committed redaction baseline `testing/corpus/testdata/redaction_baseline.tsv`
(lines 27-34), which `TestRedactionCorpusRegression` records from a real run,
shows the same result: `open_failed` for exactly these five files.
`TestCorpusMalformed` passes either way, because it accepts a clean rejection,
so it cannot detect this gap.

| File (bytes) | Generator mutation | Today | Where it fails | Expected after this design |
|---|---|---|---|---|
| `truncated.pdf` (296) | `text.pdf` cut at half its length, inside object 4's stream | `ErrInvalidPDF` | `FindStartXref`: no `startxref` | Object scan (§3). Objects 1–3 and object 4's header are recovered, and the catalog is found by `/Type /Catalog` (no trailer survives). 1 page. Object 4 has no `endstream`, and its `/Length 49` exceeds the 23 bytes left, so the page content fails to load as it does today for any such stream. Object 5 (the font) is gone. |
| `bad_xref.pdf` (593) | every `0000000` in the xref became `9999999` | `ErrInvalidPDF` | `ParseXref` succeeds. `Pages()` → `Catalog()`: object 1's offset 9,999,999,009 is past the end of the file (`resolver.go:178`) | Object scan. All 5 objects are found, `/Root` comes from the surviving trailer, and the file is fully readable. |
| `missing_endobj.pdf` (586) | the first `endobj\n` removed; xref and `startxref` not updated (7 bytes stale) | `ErrInvalidPDF` | `startxref 410` lands on the `6` of the `0 6` subsection header (the table now starts at 403). `parseXrefStreamAt` → "missing 'obj' keyword at offset 410" | Object scan. Object 1's span ends at object 2's header (`endobj` is already optional, `indirect.go:351`). Fully readable. |
| `cyclic_pages.pdf` (238) | `/Kids [2 0 R]` on node 2 | **opens**, `PageCount` 0 | — (the cycle guard yields no leaf) | Unchanged. This is not a repair trigger (§2.1). |
| `junk_prefix.pdf` (635) | 42 bytes (`%!PS …\n\x00\x01\x02`) before `%PDF-` | `ErrInvalidPDF` | `startxref 410` is 42 bytes before the real table and lands inside the name `/Subtype` (token `ype`). → "missing object number at offset 410" | Header-offset retry (§2.2, step 1): `startxref` + 42 = 452 is `xref`, and every entry + 42 holds its object. Fully readable, with no scan. |
| `no_trailer.pdf` (539) | cut at `trailer`; the xref table survives, the trailer and `startxref` do not | `ErrInvalidPDF` | `FindStartXref`: no `startxref` | Object scan. All 5 objects are found, and the catalog is found by `/Type /Catalog`. Fully readable. |

Re-measure after implementation by asserting outcomes per file in
`TestCorpusMalformed` (§8.2), not by its pass signal.

### 1.2 Failure classes to cover

| Class | Today | Repair route |
|---|---|---|
| `startxref` missing (truncation, cut trailer) | `errLex` from `FindStartXref` | scan |
| `startxref` wrong (stale after an edit, or junk prefix) | parse error at the offset | header-offset retry, then scan |
| Offsets wrong, table parses | `Pages()` fails on the first bad offset | scan |
| Corrupt xref stream (bad `/W`, Flate error, `/Size` missing) | `errLex` from `parseXrefStreamAt` | scan |
| Broken `/Prev` (offset into garbage) | `ParseXref` fails the whole open, even when the newest section is intact (`xref.go:72-75`) | scan (§3.4 keeps the newest definitions) |
| `/Prev` out of range or looping | silently treated as the end of the chain (`xref.go:67-69`); the file opens with the older objects missing | unchanged (§2.1, §11 Q4) |
| Truncated file | `errLex` from `FindStartXref` | scan, with truncated objects failing individually |

---

## 2. When reconstruction runs

### 2.1 Trigger: only when today's open fails

Reconstruction is attempted **only when the strict open fails structurally**:

1. `parse.NewResolver` returns an error, or
2. it succeeds but `resolver.Pages()` returns an error. That covers a missing
   or unresolvable `/Root`, a catalog without `/Pages`, and an offset pointing
   at the wrong bytes on the catalog or page-tree path.

These do **not** trigger reconstruction:

- An `ErrEncryptedDocument` outcome (unsupported handler or wrong password).
  That is not a structural fault.
- A page tree that walks without error but yields 0 pages (`cyclic_pages.pdf`).
- A broken object that the page tree does not need (a bad font or image
  offset). It fails individually, as today.

**Consequence: every file that opens today opens identically.** It takes the
same code path, with the same object table, the same page list, and the same
render, extract and save bytes. This is stronger than "well-formed files are
unaffected", and it is cheap to test: §8.3 gates it with a hash manifest. It
also means a repaired document never mixes a partly valid table with scanned
entries. The whole table comes from one source (§11 Q3 discusses per-object
fallback).

### 2.2 The repair ladder (`core/open.go`)

```
strict:   NewResolver(data) → EnableDecryption → Pages()        ok → done (no repair)
                      │ structural failure (errS)
step 1:   h := headerOffset(data); if h > 0:
            NewResolverShifted(data, h) → EnableDecryption → Pages()   ok → Repaired{header-offset}
step 2:   NewRepairedResolver(data) → EnableDecryption → Pages()       ok → Repaired{object-scan}
otherwise: ErrInvalidPDF wrapping errS (the strict error, with "repair failed: …" appended)
```

- **Step 1, header offset.** ISO 32000-1 measures offsets from the start of
  the file. Writers that prepend bytes (mail and HTTP wrappers, MacBinary)
  leave every offset short by the header's position `h`. Step 1 re-runs
  `ParseXref` with `startxref + h` and every in-use offset `+ h` (one
  `int64` add in `parseXrefAt`/`loadObject`, guarded against overflow). It
  keeps the file's real revision structure (free entries, `/Prev`), so it is
  tried before the scan. It is accepted only if the same `Pages()` check
  passes.
- **Step 2, object scan** (§3).
- An `ErrEncryptedDocument` at any step ends the ladder with that error. A
  repaired file whose password is wrong is not retried as plaintext.
- The returned error stays `ErrInvalidPDF` (`errors.Is` unchanged for callers).

---

## 3. The object scan (`core/parse/repair.go`)

Two linear passes over the file bytes, then a lazy second stage for object
streams.

### 3.1 Pass 1: candidate index

One forward pass records, in file order:

- **Header candidates** `N G obj`: an `obj` keyword whose next byte is
  white-space, a delimiter or end of input. Behind it, within a fixed look-back
  window of 32 bytes, it must have white-space, an unsigned integer `G` ≤
  65535, white-space, and an unsigned integer `N` with 1 ≤ `N` ≤ 8,388,607
  (Annex C). Before `N` there must be the start of file, white-space or a
  delimiter. `endobj` fails the look-back (its `obj` follows `d`). A candidate
  whose line begins with `%` within the window is a comment and is dropped.
  The look-back is constant per hit, so the pass is linear.
- **`stream` keywords** followed by CRLF, LF or a lone CR (lenient), and
  **`endstream`** keywords (§7.3.8.1).
- **`trailer`** keywords.

Each list is a sorted slice of offsets. Headers are packed as
(`N int32`, `G uint16`, `off int64`). Nothing else is stored, and no object is
parsed in pass 1.

### 3.2 Pass 2: object spans and stream masking

Walk the header candidates in order, with one "mask end" cursor. A candidate
whose offset lies before the mask end is inside stream data and is
**discarded**. For each kept header at `off`, with `next` the offset of the
following raw header candidate:

1. Parse the object's value **dictionary-only**, on `data[:next]`. This is a
   new `iparser` mode that returns at the `stream` keyword without copying
   stream data. It keeps `maxNestingDepth`, so an unterminated dictionary
   costs at most its span. Spans `[off, next)` are disjoint, so the total
   parse work is O(file size). If the bounded parse fails, the header is kept
   with its offset only: the resolver parses it lazily with the full data,
   exactly as for an xref offset.
2. If the dictionary is followed by `stream`, find the data end, in order:
   - **(a)** a direct `/Length` such that `endstream` follows after at most one
     EOL (the existing `maybeStream` validation);
   - **(b)** an indirect `/Length` whose target is, in the pass-1 header
     index, the last header with that number whose body is exactly
     `N G obj <integer> endobj` (the `readDirectLength` check), validated the
     same way;
   - **(c)** the first `endstream` candidate after the data start (a pointer
     into the sorted list that only moves forward).

   Header candidates before that end are masked. If no `endstream` exists
   after the data start, **nothing is masked**: the object is recorded as
   truncated, and the headers after it stay candidates. Compressed data almost
   never contains the `N G obj` pattern, so keeping them loses less than
   masking to the end of the file would.
3. Record the dictionary facts that later steps need: `/Type` (`/ObjStm`,
   `/XRef`, `/Catalog`), the encryption-dictionary shape (§3.6), and for
   `/Type /XRef` streams the trailer keys.

Masking with (a)/(b) is what stops an uncompressed embedded PDF (an attachment
stream) from planting `1 0 obj << /Type /Catalog …` headers that would win by
§3.3. Rule (c) can still be fooled by an embedded `endstream`; that limitation
is listed in §10.

### 3.3 Duplicate object numbers: the last definition in file order wins

When the same object number is defined more than once, the definition that
comes **last in file order wins**, whatever its generation number. Under
§7.5.6, an incremental update appends its changed objects after the earlier
revision, so the last definition is the newest one. A redefinition after the
number was freed carries a higher generation and is also later in the file.
The resolver already looks objects up by number only (`object(ref.Number)`).
`RepairReport.Duplicates` counts the shadowed definitions. A definition whose
value does not parse on its span (a write cut short, garbage after a header)
wins its number only when no definition of it parses: it never replaces a good
earlier one. A load through a repaired table (scan or shifted) also checks that
the header at the offset names the requested object.

Shadowed definitions and objects that a later revision freed stay in the index
but are unreachable from the winning graph. They are never resolved, so they
never reach a render, an extract or a build (§5).

### 3.4 Objects inside object streams

The winners whose dictionary has `/Type /ObjStm` are expanded. Their contents
may be encrypted, and the decryption state is known only after
`EnableDecryption`, so expansion is **stage 2**. It runs once, on the first
`object()` call for any number other than the `/Encrypt` object, and on the
first `Catalog()`/`RootRef()` call. Under §7.5.7, the encryption dictionary
cannot itself be in an object stream, so `EnableDecryption` never needs
stage 2. Invariant: **nothing inside an ObjStm is read before the decryption
state is final.**

For each winning ObjStm, in file order:

- It is decoded through the existing `decodeObjStm` path (decryption, the
  `/N` and `/First` bounds, header validation), with `DecodeStreamLimit`
  capped at what is left of the repair decode budget (§4). The decoded stream
  is inserted into the resolver's LRU cache (`cacheObjStm`), so a later object
  load does not decode it again.
- Each header pair `(objNum, offset)` at index `i` becomes an `InObjStm`
  entry `{StreamNum, StreamIdx: i}`, with **file position** equal to the
  ObjStm's own header offset. Duplicates are then settled by §3.3 on file
  position. A compressed object that appears later than a plain definition
  wins, and vice versa. Inside one ObjStm, the later index wins; ISO does not
  settle this case.
- A compressed entry never replaces the ObjStm that holds it, or any other
  expanded ObjStm. A hostile header cannot redirect an ObjStm's number to a
  compressed object, which `decodeObjStm` would then reject as "not a plain
  object".
- **Only winning ObjStms are expanded.** An ObjStm whose number a later
  definition reuses belongs to a superseded revision. Its contents are stale,
  and its number no longer resolves to it.
- A failed expansion (decode error, budget spent, bad header) drops that
  ObjStm's entries and is recorded. The other ObjStms are unaffected.

`/Extends` chains are not followed. They only group streams, and every
winning ObjStm is expanded anyway.

### 3.5 The synthesized trailer, and finding `/Root`

Trailer candidates are the dictionaries after each kept `trailer` keyword
(parsed bounded, like §3.2 step 1) and the dictionaries of winning
`/Type /XRef` streams. Under §7.5.8, cross-reference streams are not
encrypted, and the parser already reads them without decryption. At most the
last 64 candidates in file order are considered.

| Key | Rule |
|---|---|
| `/Encrypt` | the last candidate that has one; otherwise the shape scan (§3.6) |
| `/ID` | the last candidate with a two-element array of strings |
| `/Root` | stage 2: the last candidate whose `/Root` resolves to a dictionary with `/Type /Catalog` (or with no `/Type` but a `/Pages` that resolves to a dictionary). If none does: **catalog scan** |
| `/Info` | the last candidate whose `/Info` resolves to a dictionary; otherwise none |

**Catalog scan.** Byte-search `/Catalog` inside each winner's span (plain
objects) and inside each expanded ObjStm's decoded bytes. Parse only those
candidates, the last in file order first, and accept the first whose `/Type`
is `/Catalog` and whose `/Pages` resolves to a dictionary. At most 64
candidates are parsed. If none is accepted, the repair fails, and the open
returns `ErrInvalidPDF`. Synthesizing a page list from stray `/Type /Page`
objects is out of scope (§11 Q5).

`Trailer()` returns the stage-1 dictionary (`/Encrypt`, `/ID`) until stage 2
has run. `OpenBytes` reads only `/Encrypt` before `EnableDecryption`, and
`Catalog()` forces stage 2 before it reads `/Root`, so the order is safe.

### 3.6 Recovering `/Encrypt`: fail closed

Opening an encrypted file as plaintext would render ciphertext. Worse, it would
hand redaction a model that contains none of the real text. So:

- `/Encrypt` from any trailer candidate means the file is encrypted.
- If no candidate has `/Encrypt`, every winning plain object is tested for the
  shape of an encryption dictionary: a top-level dictionary with a `/Filter`
  name and either `/O`, `/U` and `/P` (standard handler, ISO 32000-1 Table 21)
  or `/Recipients` (public-key handler). Objects are pre-filtered by byte
  search within their span. The last match becomes `/Encrypt <n> 0 R`. Under
  §7.5.7 it cannot be in an ObjStm, so plain objects suffice.
- If neither finds one, the scan still fails closed with
  `parse.ErrEncryptionLost`, which `core` reports as `ErrEncryptedDocument`,
  when the file shows that an encryption dictionary was lost:
  - a kept header that does not parse whose dictionary bytes (its span up to
    the first `stream` or `endobj`, so it cannot borrow the next object's
    names) hold `/Encrypt` (a cross-reference stream dictionary cut short),
    or match `lostShaped`, in this order: `/Recipients`, `/Filter /Standard`,
    `/Filter /Adobe.PubSec` or a public-key `/SubFilter` (`adbe.pkcs7.s`
    and a digit: s3, s4, s5, §7.6.5; not the signature `adbe.pkcs7.sha1`),
    never excluded; `/Filter` with one of `/O`, `/U`, `/CF`, `/StmF`,
    `/StrF`; then a signature dictionary is excluded (`/ByteRange`, `/Type
    /Sig`, `/Type /DocTimeStamp`; a `/SubFilter` alone is not a signature
    marker); then `/Filter` with `/P`; then a dictionary with `/Subtype` (an
    image or font file) is excluded when its `/Filter` value is missing, not
    a name, a stream filter, or a cut-short prefix of one (`/JBIG2`);
    finally `/Filter` with a value that is not a stream filter (Table 6,
    Table 94);
  - any other header (masked as stream data, shadowed, or a winner) whose
    dictionary bytes have the shape and that parses to an encryption
    dictionary it cannot be (masked or shadowed), does not parse, or has the
    keys but cannot be read as one (an indirect `/Filter`, or a stream
    dictionary whose `/Filter` is not a stream filter);
  - a trailer keyword followed by bytes naming `/Encrypt` that do not parse.

  Names match as whole tokens outside literal and hexadecimal strings. A
  string not terminated in the bytes is not trusted to be one: from a stray
  `(` or `<` on, the rest is searched plainly, so damage cannot hide the
  names after it. The search is one pass (two with that fallback), whatever
  the bytes hold.
- After that the normal path applies: `EnableDecryption` either succeeds or
  ends the ladder with `ErrEncryptedDocument`. Handlers R2–R4 derive the key
  from `/ID[0]`. If `/ID` is lost, the key is wrong, which only a password
  check detects (§10 F2). R6 does not use `/ID` (`aes256-decryption.md`).

---

## 4. Bounds (SECURITY.md §2)

All values are proposed for review. Hitting a cap fails the repair: the open
returns `ErrInvalidPDF` and the error names the cap. Nothing is skipped
silently, because a partial table would make a partial document look
complete.

| Bound | Value | Why |
|---|---|---|
| Passes over the file | 2 linear passes plus disjoint-span parses | O(file size) time; no per-candidate rescans (the `endstream` pointer only moves forward) |
| `maxRepairCandidates` (raw pass-1 header candidates) | 4 Mi (≈ 64 MiB of index at 16 B) | `1 0 obj\n` repeated is 8 B per candidate; caps the index before the map is built |
| `maxRepairObjects` (distinct numbers, plain + compressed) | 1 Mi (= `maxPages`) | the `map[int]XrefEntry` costs ≈ 65 B/entry, so ≈ 68 MiB at the cap, tolerable under js/wasm |
| Object number / generation | ≤ 8,388,607 (Annex C) / ≤ 65535 | larger values are not candidates |
| `maxRepairObjStmBytes` (stage 2 decode, total) | 256 MiB (= `maxObjStmCache`) | N ObjStm bombs without a trailer would otherwise inflate N × 256 MiB at open |
| Trailer and catalog candidates parsed | 64 each, last first | bounded parse count |
| Look-back window | 32 bytes | constant work per `obj` hit |

The **render budget** (`render-budget.md`) is not involved. The repair runs
inside `OpenBytes`, before any render, and renders of a repaired document load
objects through the same resolver paths, caches and caps as today. Open itself
has no budget or context today. The repair adds at most two linear passes plus
256 MiB of decode to it. §8.5 gates the time and memory this takes.

Peak extra memory for a hostile 64 MiB file at the caps is estimated at about
130–150 MiB (index plus map), released once the table is built. The
implementation PR must measure it natively and under js/wasm.

Measured natively (§12): the header index is sized once (16 B per `obj`
keyword, capped at 64 MiB), a header that fails to parse costs no formatted
error, and an indirect `/Length` object is read once on its own span. Dense
8-byte headers grow the heap by at most 2 MB per input MB (16 B each) up to
the 64 MiB index cap (+64 MB measured on 48 MiB); a 36 MB file at the caps
(1 Mi objects, 3 Mi duplicates) by about 4 MB per input MB (+147 MB, the
retained table map included), with 324 MB allocated in total.

---

## 5. Redaction, saves and incremental updates

`docs/design/redaction-hardening.md` defines how redaction writes its output.
`RedactAndFlatten` builds it with `parse.BuildDocument` (§5 stage F, §4
"`BuildDocument` writes a fresh catalog"). `SaveTo`, `Merge`, `Split`,
`FlattenAnnotations`, `FlattenForm` and `ConvertToPDFA` build the same way. The
result is a **single new revision**: a fresh object numbering, a classic xref,
and no `/Prev` (`writer.go:134`). It contains only the closure of the current
pages. The verifier then re-opens that output (§5.6, invariant I5).

**Rule R (normative).**

1. **A document that was repaired is only ever written as a full rebuild.** No
   code path may append to `Resolver.Bytes()` of a repaired document.
   Shadowed duplicates, freed objects and superseded ObjStms are not in any
   page's closure, so they never reach the output. Saving a repaired document
   with `SaveTo` therefore also "cleans" it.
2. **Incremental writers refuse repaired documents.** Today the only one is
   `sign.Sign` (`core/sign/sign.go:121-125`). It takes `/Prev` from
   `FindStartXref(original)`, which on a repaired file is absent (the error is
   ignored, so `/Prev 0`) or points into broken data. Its output would also
   keep every old revision, and another reader's own reconstruction might
   treat one of those as current. `SignDocument` on a repaired document
   returns an error (§11 Q2 chooses which). The caller must `SaveTo`,
   re-open, then sign. Any future incremental save inherits this rule.
3. **The redaction verifier re-opens strictly.** The output of
   `BuildDocument` is always well-formed. If verifying it ever needed a repair,
   the writer would be broken, and a scan might resolve objects the xref does
   not name. The verifier's re-open uses an internal strict open
   (repair disabled), so a repair-needing output fails as
   `ErrRedactionVerification`. That is a bug signal, never a silent re-scan.
4. **Redaction is allowed on a repaired document.** Invariant I2 ("fail closed
   on model disagreement") is about how other readers will read *the output*.
   The output is a clean rebuild: what FluxDocs shows, renders and verifies is
   exactly what it writes. Content another reader might pick from a shadowed
   revision is not in the output at all, so nothing outside the user's view is
   kept. `RedactionReport` records that the source was repaired (§6), so a
   reviewer knows the version redacted is FluxDocs' reading of a damaged file
   (§11 Q1 offers the stricter alternatives).

The corpus no-regression check is unaffected in kind. The five `open_failed`
rows become per-operation rows. `compareBaseline` logs them as "not in the
baseline" and does not fail on them; every `mustNotOccur` outcome still fails.
Re-record the baseline (`FLUXDOCS_REGRESS_WRITE`) in the implementation PR,
and review each new row.

---

## 6. API

**Reconstruction is on by default** (CLAUDE.md §6). It never changes a file
that opens today (§2.1), so the default carries no regression risk. It is
reported, never silent. Success is not an error (CONVENTIONS.md §3: "a nil
error means success"), so there is **no warning sentinel**. Repair is reported
through a report accessor in the style of `RedactionReport()`:

```go
// core/types.go
// RepairMethod names how OpenBytes rebuilt a document whose cross-reference
// data could not be used.
type RepairMethod string

const (
	RepairHeaderOffset RepairMethod = "header-offset" // offsets shifted by the bytes before %PDF-
	RepairObjectScan   RepairMethod = "object-scan"   // object table rebuilt by scanning for "N G obj"
)

// RepairReport describes a repaired open. It never holds document text.
type RepairReport struct {
	Method          RepairMethod
	HeaderOffset    int    // bytes before %PDF- (RepairHeaderOffset)
	Objects         int    // objects in the rebuilt table, plain and in object streams
	Duplicates      int    // shadowed definitions; the last in file order was kept
	CatalogFromScan bool   // no trailer named a usable /Root
	Truncated       bool   // the file ends inside an object or a stream
	StrictError     string // why the strict open failed (offsets and keywords, no content)
}

// core/document.go
// RepairReport returns how the document was repaired at open, and false when
// it opened from its own cross-reference data.
func (d *Document) RepairReport() (RepairReport, bool)
```

- The output of `RedactAndFlatten`, `SaveTo` and the other builders is not
  repaired: it re-opens strictly. `RedactionReport` gains
  `SourceRepaired bool` (additive).
- `parse` side: `NewRepairedResolver(data []byte) (*Resolver, error)`, plus an
  unexported shifted variant. `(*Resolver).Repair() (RepairInfo, bool)` feeds
  `core`. `NewResolver` is unchanged.
- **Opt-out.** There is no `OpenOptions` today. A server that wants to reject
  damaged uploads can check `RepairReport` after the open. A public strict
  flag belongs in an `OpenOptions` struct, which the AES-256 note also needs
  for a password (§11 Q6). Internally, the strict open exists from day one,
  for the verifier (§5 R.3).
- **WASM / TS.** `open()` adds `repaired: boolean` to its result (additive;
  `wasm/main.go:187-190`), and `packages/core` mirrors it. The React and Vue
  viewers make no UI change in this increment (a banner is a later UX
  decision), so parity holds.
- **Spec.** Once approved: spec §7.1 (`RepairReport`), §13.2 (parser
  robustness), and a line in `testing/README.md`.

---

## 7. WASM and tiers

- Pure computation over the in-memory bytes: no filesystem, no network, so the
  privacy path is unchanged.
- The caps in §4 are the same on both targets. Results are identical natively
  and in the browser, because the scan has no concurrency and no map-order
  dependence (candidates are walked in file order).
- Core only. Nothing here imports `form`, `sign` or `internal/redact`. The
  `SignDocument` refusal (§5 R.2) is a check in Pro code that reads a Core
  accessor.

---

## 8. Testing

### 8.1 Table-driven unit tests (`core/parse/repair_test.go`)

These use the existing in-package builders (`buildPDF`, `writeStreamObj`,
`packXref`). Each case asserts: the resolved catalog and the page count; the
resolved bodies of the objects named; the `RepairInfo` fields; and no
`errLex` where success is expected.

1. `startxref` missing, past the end of the file, inside an object, and
   pointing at a stale table.
2. All offsets shifted by +k and by −k; a junk prefix (step 1), and a junk
   prefix plus a broken table (falls through to step 2).
3. Corrupt xref stream: bad `/W`, truncated Flate, missing `/Size`, and a
   valid stream with garbage rows.
4. `/Prev` into garbage, with an intact newest section. The newest definitions
   must win, and objects that exist only in the old revision must resolve.
   `/Prev` loop and out-of-range `/Prev` stay on today's lenient path; assert
   no repair.
5. Truncation mid-dictionary, mid-stream, and mid-table.
6. No trailer: catalog by scan; catalog inside an ObjStm; trailer `/Root`
   naming a non-catalog → scan.
7. Duplicates: the same number with different bodies (last wins); a different
   generation; an incremental update whose newest table is broken.
8. ObjStm: shadowed by a later plain object (not expanded); a compressed entry
   claiming its own ObjStm's number (ignored); a bad header (only that ObjStm
   dropped); repair decode budget spent (error, not panic).
9. Stream masking: stream data containing `1 0 obj` with a direct `/Length`,
   with an indirect `/Length` written after the stream, and with no
   `/Length` (first `endstream`); an uncompressed embedded PDF with a valid
   `/Length` (its catalog must not win); a stream with no `endstream` (later
   objects kept).
10. Lexical edge cases: `endobj`, `% 1 0 obj` in a comment, `01 0 obj`,
    `8388608 0 obj` (ignored), and `1 65536 obj` (ignored).
11. Encryption: trailer lost, `/Encrypt` found by shape; an ObjStm in an RC4
    file expanded only after `EnableDecryption` (extends
    `TestResolver_DecryptionClearsFailureCaches`); an encryption-shaped
    object with no trailer never opens as plaintext.
12. No catalog anywhere → error. Caps: candidates, objects, ObjStm bytes, each
    one past its limit → error naming the cap.

### 8.2 Corpus (`testing/corpus`)

- `TestCorpusMalformed` gains a table of expected outcomes per file (opens or
  not, page count, `RepairReport.Method`, and whether the text contains
  `Corpus text sample`), matching §1.1. Its pass signal alone hid the 5/6 gap.
- New `generate.go` variants: `broken_prev.pdf`, `corrupt_xrefstm.pdf`,
  `objstm_no_trailer.pdf`, `duplicate_objects.pdf`,
  `stream_with_headers.pdf`, `encrypted_no_trailer.pdf` (RC4, synthetic).
- A **repaired `SaveTo` output re-opens strictly**: `RepairReport` reports
  false, and the page count and text are unchanged.
- The redaction baseline is re-recorded (§5).

### 8.3 Byte-identity gate (the §2.1 promise)

Record a manifest at the base commit: for every file under `testing/corpus`
and in the extra corpus (`FLUXDOCS_EXTRA_CORPUS`), the open outcome, the page
count, the `ExtractText` hash, the 72-DPI page-0 PNG hash and the `SaveTo`
hash. After the change, every file that opened at the base must match its row
exactly. Only rows that were `open_failed` may change.

### 8.4 Fuzz (`testing/security/fuzz/repair_seeds_test.go`)

Seeds: every §8.1 fixture; `minimalPDF`, which has no xref or `startxref` and
which repair will now open (wider coverage of the downstream code); 1 Mi
headers; `stream\n` repeated without `endstream`; ObjStm bombs without a
trailer; headers with huge numbers; and an unterminated `<<` after every
header. `FuzzOpenSaveReopen` adds an invariant: a repaired input's saved
output must open **without** repair.

### 8.5 DoS gates (`testing/security/dos/repair_dos_test.go`)

These use the `cputime` helpers, with bounds scaled under `-race` and js/wasm.

- `TestDoS_RepairDenseHeaders`: 64 MiB of `N 0 obj\n`. Fails at the cap,
  linear time, bounded RSS.
- `TestDoS_RepairStreamsWithoutEndstream`: 1 MiB of `1 0 obj<<>>stream\n`
  repeated. Linear, which catches a rescan per stream.
- `TestDoS_RepairUnterminatedDicts`: headers each followed by deep, open
  `<<`. The span-bounded parse keeps it linear.
- `TestDoS_RepairObjStmBombs`: k × 192 MiB ObjStms and no trailer. At most
  256 MiB decoded in total.
- `TestDoS_StrictFilesSkipRepair`: a large well-formed file never enters the
  scan (instrumented counter).

All §8.1 tables also run under `GOOS=js GOARCH=wasm`.

---

## 9. Phasing and effort

| Phase | Content | Estimate |
|---|---|---|
| 0 | Manifest at base (§8.3); object-count census of the extra corpus to confirm the §4 caps | 0.5 d |
| 1 | Ladder in `open.go`, header-offset retry, `RepairReport`, strict internal open, `SignDocument` refusal | 1–1.5 d |
| 2 | Passes 1–2, duplicates, trailer synthesis, catalog scan, `/Encrypt` recovery | 3–4 d |
| 3 | Stage 2 ObjStm expansion (lazy, after decryption) with the decode budget | 1.5–2 d |
| 4 | Corpus table, new corpus files, fuzz seeds, DoS gates, wasm runs, baseline re-record | 2–3 d |
| — | Review round (go-reviewer) | 1–2 d |

**Total ≈ 9–13 engineer-days.** Phases 1–2 alone fix all 5 failing corpus
files: none of them uses object streams.

---

## 10. Related findings (out of scope here, flagged)

- **F1. Xref-stream entry count is bounded only by the decode cap.**
  `parseXrefStreamAt` inserts one map entry per row. With `/W [1 0 0]` a
  256 MiB decoded stream yields ≈ 268 M entries, which is far beyond any memory
  bound (≈ 17 GB at ≈ 65 B/entry). The strict path needs the same
  `maxRepairObjects`-style cap. Proposed: reuse that constant for both paths.
- **F2. R2–R4 never verify the password.** `newDecryptor` derives the key and
  never compares `/U` (`crypt.go:86`). A file that needs a user password, or a
  repaired R2–R4 file whose `/ID` was lost, decrypts to garbage instead of
  returning `ErrEncryptedDocument`, contrary to `EnableDecryption`'s
  documentation. This is covered in `aes256-decryption.md` §12 F2.
- **F3. Endstream fallback rescans.** `maybeStream`'s fallback
  `bytes.Index(data[start:], "endstream")` costs O(rest of file) per object.
  k distinct objects that lack `endstream` cost O(k·n) on the strict path too
  (each failure is cached, but distinct objects are not). The pass-2 forward
  pointer could be reused there.
- **F4. Masking rule (c) is defeatable** by an uncompressed embedded file that
  contains `endstream`, when the outer stream's `/Length` is unusable. The
  effect is a wrong object choice, not a leak (§5 R.4). Documented limitation.
- **F5. A wrong but parseable `startxref`** that points at an older intact
  section opens that older revision silently (no trigger). Detecting it means
  comparing against later `trailer`/`xref` keywords, which changes files that
  open today. Deferred (§11 Q4).

---

## 11. Open questions (maintainer)

- **Q1. Redaction on repaired documents.** Options:
  - **(a)** allow, and report `SourceRepaired` (recommended, §5 R.4: the
    output is a clean rebuild of exactly what was shown and verified);
  - **(b)** refuse with a new `RedactionError` reason `repaired_xref`, which
    matches `ErrRedactionUnsupported`;
  - **(c)** allow unless an object in a redacted page's closure had shadowed
    duplicates with different bytes, and refuse those pages as `repaired_xref`.

  (b) and (c) add a reason to `allowedNewRefusals`.
- **Q2. `SignDocument` on a repaired document.** Return `ErrInvalidPDF`,
  wrapped with "document was repaired; save and re-open it before signing"
  (recommended), or add a sentinel `ErrRepairedDocument`. Add the sentinel
  only if a caller needs to branch on it.
- **Q3. Per-object fallback.** When an xref offset does not hold
  `N G obj` for its number, look the object up in a lazily built scan index.
  This would rescue files that open today with broken fonts or images. But it
  changes files that open today (breaking §2.1), and it mixes two table
  sources. Not recommended for this increment.
- **Q4. Lenient `/Prev` handling.** Should an out-of-range or looping `/Prev`
  (silently accepted today), or a parseable but stale `startxref` (F5),
  become a trigger? Each changes the result for files that open today.
- **Q5. Orphan pages.** When no catalog survives, synthesize a page list from
  `/Type /Page` objects in file order? This would recover more truncated
  files, but the page order is a guess and the inherited attributes are lost.
  Recommended: no.
- **Q6. `OpenOptions`.** Introduce `OpenOptions{Strict bool, Password string}`
  with `OpenBytesWithOptions`/`OpenDocumentWithOptions`, shared with the
  AES-256 note, or keep strictness internal and let servers check
  `RepairReport`. One decision for both notes.
- **Q7. Cap values.** Confirm 1 Mi objects, 4 Mi candidates, 256 MiB stage-2
  decode and 64 trailer/catalog candidates after the Phase 0 census. If F1 is
  fixed, the strict path takes the same object cap.

---

## 12. Implementation notes (Phases 0–2)

**Decisions (§11), as implemented.** Q1 (a): redaction runs on a repaired
document; `RedactionReport.SourceRepaired` says so, and the verifier re-opens
the output strictly (`openStrict`). Q2: `SignDocument` returns `ErrInvalidPDF`
wrapped with "document was repaired; save and re-open it before signing" (no
new sentinel); `sign.Sign` also refuses a repaired resolver. Q3, Q4, Q5: no.
Q6: no `OpenOptions`; `Document.RepairReport()` is the opt-out check. Q7: the
§4 caps stand (census below). Alongside: `Document.Encrypted` is now set for an
encrypted source, and `Save`/`SaveTo` document that the output is decrypted.

**Phase 0 census** (`TestRepairCensus`, the 1,491-file extra corpus): file
size p50 9.8 KB, p99 420 KB, max 6.6 MB; xref entries p50 13, p99 248, max
8,371; scan candidates and objects p50 12, p99 143, max 974; duplicates max
128; slowest scan 62 ms. The caps (4 Mi candidates, 1 Mi objects) are more
than 100× the observed maxima. On all 1,450 strict-opening files where the
scan applies, it gives the same page count as the strict open; 41 files
(2.7%) keep objects in object streams, where the scan refuses (below). The
corpus is small-file-skewed, so the caps rest on the §4 reasoning, not on it.

**Byte-identity gate (§8.3).** `TestOpenManifest`; manifest recorded at
`4381a44`. After the change, the 13 `testing/corpus` rows that were not
`open_failed` there (10 opened, 3 needed a user password) and all 1,491
extra-corpus rows are identical; only the 5 `open_failed` rows changed. The `SaveTo` column is a
canonical hash (DFS from the trailer, sorted keys, objects renumbered):
`BuildDocument` numbers objects in Go map order, so its bytes already differ
from run to run at the base commit (finding F6).

**Deviations from the design.**

- *No lazy stage 2 for /Root.* With no ObjStm expansion, /Root, /Info and the
  catalog scan are settled at construction by a bounded, memoized,
  dictionary-only read (`repairScan.peek`) that never touches the Resolver's
  caches, so nothing is cached before decryption. Phase 3 needs stage 2 when
  the catalog may be in an object stream.
- *A winning `/Type /ObjStm` fails the scan* until Phase 3. Its objects would
  resolve to null, and the document would open with pages missing: exactly
  the partial table §4 forbids.
- *A strict-path resource limit ends the ladder.* `errXrefLimit`,
  `ErrDecodeLimit`, scan-limit errors, and the failures that `limitf` marks
  (the `/Prev` chain length, the nesting depth, the page tree's depth and page
  count, the ObjStm re-decode budget) are not retried by repair
  (`parse.IsLimitError`, CONVENTIONS.md §4). The mark leaves each error's
  message and `errors.Is` matches as they were. Damage (a `/W` field wider than 8, an
  overflowing `/Index` range) is repaired as §1.2 says. The DoS and fuzz
  gates that pinned `ErrInvalidPDF` for the damage cases now pin a repaired
  open, with the same allocation and time bounds.
- *A usable /Root needs `/Pages` to resolve to a dictionary* also when `/Type
  /Catalog` is present (§3.5 required it only without `/Type`). Otherwise a
  catalog whose page tree is gone would beat a later usable one.
- *Indirect `/Length` (rule (b))* checks only the last header with that
  number, reads its tokens on that header's span only, and memoizes the
  result per number (failures included), so each lookup is O(log n) and N
  streams naming one huge object read it once: a hostile file cannot make it
  walk back through many same-numbered headers, nor re-lex one object per
  stream (400 streams naming a 4 MB unterminated string took 14.7 s and 8 GB
  before; 0.1 s and 60 MB now).
- *Scan memory.* `headers` is allocated once from a count of `obj` keywords
  (capped), and `iparser` in dictionary-only mode returns one static error for
  every syntax failure. On 1 Mi headers plus 3 Mi duplicates (36 MB) total
  allocation fell from 994 MB to 324 MB and heap growth from 227 MB to
  147–191 MB. The `TestDoS_RepairScanIsBounded` cases bound heap growth
  (`HeapSys`) as well as allocation.
- *Scan parsing cost.* Every dictionary-only parse of the scan (`head`, the
  indirect `/Length` read) lexes quietly (`nextTokenQuiet`,
  `nextTokenInterned`): a lexer error — an unterminated string, an invalid
  character or real, a stray `>`, a bad hex digit — is the static
  `errTruncated` or `errScanSyntax`, never a formatted message; the strict
  path's messages and `errors.Is` matches are unchanged. The scan also
  collects a dictionary's entries on a scratch stack and builds its map only
  when it closes, interns repeated names (at most 4096 per scan), sizes the
  object table once (by the kept count up to 16 Ki headers, past that by a
  1 MiB bitset count of the distinct numbers), and skips
  the reference lookahead for a value that cannot start with an integer.
  Two lexer changes apply to every path and return the same tokens: a
  name without `#` escapes is copied once, and a short integer is parsed
  without a string. On 1,000,000 headers each followed by an unterminated
  `(` (the lin probe, 64 MB) the open fell from 4.4 s to 1.2 s; the
  `TestDoS_RepairScanIsBounded` case of that shape (1 Mi headers, 66 MB)
  takes about 2 s CPU on Go 1.23.4 and on Go 1.27, under its 5 s bound, with
  213–219 MB allocated.
- *Pass 1 indexes no `stream` keywords.* Pass 2 learns each stream's start
  from the dictionary-only parse. The endstream and trailer lists share the
  candidate cap with the headers.
- `NewResolverShifted` is exported (core calls it); the design called it
  unexported.
- `RepairReport.StrictError` elides every double-quoted run and every
  single-quoted rune literal in the parse error (`elideQuoted`), since a
  quoted keyword or character can be document text.
- Every internal re-open of a file FluxDocs wrote (the redaction verifier and
  its test hook, Merge, Split, the flatten, form, watermark and PDF/A writers,
  SignDocument's signed output) uses `openStrict`.
- *Fail-closed name search.* `scanNames` drives every name test of the
  encryption checks in one pass; `TestDoS_RepairScanIsBounded` pins it on
  4 Mi `(` and on 1 Mi `(/Type`. A damaged dictionary of a third-party
  handler (`/Filter /FOPN_foweb`) fails closed even when its span runs into
  an image dictionary's `/Subtype`.
- *Fail-closed false positives are accepted.* An uncompressed encrypted PDF
  embedded in a damaged, unencrypted outer file shows a masked encryption
  dictionary, and the outer file then fails as `ErrEncryptedDocument`
  instead of opening. That trade is deliberate: the alternative risks
  opening ciphertext as plaintext.

**F6 (new).** `parse.BuildDocument` output is not deterministic: object
numbers follow Go map iteration, so the same document saves to different bytes
from run to run. That does not affect correctness, but it is not
reproducible. Out of scope here.

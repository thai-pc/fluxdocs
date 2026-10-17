# SECURITY.md — FluxDocs

> Mandatory security constraints for everyone (and every AI agent) contributing
> code.
> Context: FluxDocs processes sensitive documents (contracts, medical records,
> finance). A single public vulnerability = permanent loss of product trust.
> This is not an optional checklist.

Spec references: §13.4 (Redaction), §18 (Risk Assessment), §5.2 (Design
Principles).

---

## 1. Redaction — risk #1, hard constraint

**The most common flaw:** a weak redaction tool just paints a black rectangle
over content, but the original text remains in the content stream → extractable
and copyable. Many governments and organizations have leaked information this way.

**FluxDocs MUST do it correctly** (design and decisions:
`docs/design/redaction-hardening.md`):
- **Remove, never paint over.** `RedactAndFlatten()` deletes from the content
  stream every text-showing operator (`Tj`, `TJ`, `'`, `"`) that `RedactText`
  matched or that meets a `RedactArea` region (closed intervals; a box that is
  not finite meets every region). Non-text content that meets a region is
  removed whole too (decision D5): images, inline images, text-free forms,
  painted paths and shadings — a full-page scan under any region is blanked.
  Text that removing earlier text on its line moves into a region is removed
  too. The black rectangle (`--fd-redact-fill: #18181b`, solid,
  non-transparent) is for display only — NOT the security mechanism.
- **Hidden copies go too.** On every redacted page, every marked-content
  property list loses `/ActualText`, `/Alt` and `/E` (D7) and every other
  string value, `/Lang` included; every comment in the content
  (between operators, between operands, in an inline image's dictionary) is
  blanked; and the page keeps only the resources its remaining content uses
  and the page-dictionary entries needed to paint it (no annotations,
  `/Thumb`, `/Metadata`, `/PieceInfo`, …). Objects only removed content used
  are written nowhere in the output (a build-wide deny list that also ignores
  what annotations removed by the redaction paint), and references to a
  redacted page from other pages point at its redacted copy.
- **Removed text found elsewhere is removed too** (automatic extension, D4).
  Every string `RedactText`/`AutoRedactPII` matched (4 or more characters) and
  the text removed under a region (8 or more) — compared after NFKC with
  white-space and invisible characters removed, case-sensitive — is searched
  for on every page (what it shows, and what it holds but does not show:
  comments, property lists, other operators' strings), in every annotation (its text, its field's values, its
  appearance) and in the document information, and removed there; a redaction
  that cannot remove an occurrence (text in a form on another page) fails, and
  so does one that cannot search a page for certain (its content or a unit it
  paints does not decode, or the strict scanner reports a loss in it, behind
  which a lenient reading may hide text). File attachment annotations are
  dropped whenever a redaction runs. What was extended — including the
  annotations of every page the extension redacts — is reported by
  `(*Document).RedactionReport()`: pages, rects and locations, never
  document text.
- **Verification is mandatory and not optional.** `RedactAndFlatten()`
  re-opens its own output and re-reads it before returning it: every redacted
  page strictly (no glyph may meet a region, no `RedactText` pattern may still
  match, no property-list text may remain, and no removed text may remain in
  what its content holds but does not show: other operators' strings,
  comments), every object only redacted pages reach and every orphan for any
  removed text, and the rest of the document for the text that propagates —
  objects classified by the route that reaches them, not by keys they carry;
  streams decoded through Flate, LZW, ASCIIHex, ASCII85 and RunLength. A
  stream that must be searched but does not decode is never treated as
  holding nothing. A hit that extension cannot remove fails the call with
  `ErrRedactionVerification` (which matches `ErrInternal`: it is a bug) and no
  document. There is no option to skip it.
- `RedactAndFlatten()` is an **irreversible** operation — document this clearly
  in the API.
- **Fail closed:** if a marking call (`RedactText`, `AutoRedactPII`) fails after
  accepting its arguments, the plan is sticky-failed and `RedactAndFlatten()`
  refuses to run, so a page is never saved without the regions asked for.
- **Strict content model:** redaction reads content with the strict scanner
  (`parse.ScanContentStrict`) and fails the call instead of guessing what a
  viewer draws: a syntax error, dropped operands, operands that do not match
  their operator (ISO 32000-1 Annex A) or have the wrong type, an operator out
  of place in a text object, a token or comment split across content streams,
  an inline image whose end or decoding readers could disagree on, a vertical
  font whose metrics it does not model. Every route by which a page paints
  another content stream (forms, tiling patterns, soft-mask groups, Type 3
  glyph procedures) is walked under a budget. A refusal is a
  `*core.RedactionError` (`Page`, `Reason`, `Detail`) that matches
  `ErrRedactionUnsupported` and never `ErrInvalidPDF`; its message never holds
  document text. Reasons: `content_ambiguous`, `inline_image`, `form_text`,
  `pattern_text`, `softmask_text`, `type3_text`, `budget_exceeded`,
  `unsupported_xobject`.
- **Known limitations (Phase 1; their leak-corpus rows stay `knownLeak` until
  Phase 2):** `RedactText` matches one text-showing operator at a time, so a
  string split across operators (two `Tj`, one character per `Tj`, two text
  objects) is not matched — `RedactArea` has no such limit (Phase 2 step 2.6).
  Text shown through a form, pattern, soft-mask group or Type 3 glyph
  procedure is refused, not rewritten (2.3). Removal is per operator, not per
  glyph (2.5), and per image, not per pixel (2.8). Inline images are accepted
  only unfiltered, ASCIIHex or zlib Flate, in device colour spaces (2.7). A
  font whose glyphs draw something other than its `/ToUnicode` says is out of
  scope for `RedactText`: use `RedactArea`.

**Test gate (non-negotiable):**
- After any redaction, the removed text must not be found by `ExtractText()`,
  by a deep extraction (forms, patterns, Type 3), in the output bytes, any
  decoded stream or any decoded string, nor by Ghostscript's text output.
- The living test suite is in `testing/security/redaction/`: the leak corpus,
  one row per repro from every review, with the Ghostscript oracle required
  (`FLUXDOCS_REQUIRE_GS=1`) in the `redaction-oracle` CI job — non-blocking
  until it has run green and stable, then blocking. **A PR touching redaction does not merge
  unless the pass rate is 100%**, and the corpus no-regression check
  (`testing/corpus/redaction_regress_test.go`) passes: no new refusal except
  the reasons the design allows, and zero verification failures.
- Consider an independent security audit before heavily promoting the redaction
  feature (§18.4).

## 2. Parser receives untrusted input

Every PDF file is potentially hostile input. Mandatory defenses:

- **Decompression bomb:** bound the decompressed size (`/Filter` FlateDecode/LZW).
  Enforce a threshold and a maximum compression ratio.
- **Xref loop:** the `/Prev` chain in an incremental update can form an infinite
  loop → bound the depth, detect cycles.
- **Nested object streams:** bound recursion depth when resolving compressed
  objects.
- **Integer overflow:** when computing `width = MediaBox.width * DPI/72`, check
  bounds before allocating a canvas (prevent giant allocations causing DoS).
- **Safe brute-force fallback:** scan for `obj`/`endobj` on a corrupt file, but
  stay within resource limits.
- **Fuzzing:** seeds in `testing/security/fuzz/`. Run `go test -fuzz` for the
  parser/tokenizer in CI periodically.

- **Redaction is bounded too:** the resource walk (8 Mi operator visits and
  512 MiB decoded per page), the deny-list walk of other pages (32 Mi visits
  and 1 GiB decoded per call), and automatic extension plus verification
  (2 GiB decoded per call) — exceeding any is a `budget_exceeded` refusal,
  never a silent skip. Gates in `testing/security/dos/`.

- **Recover-to-error is the last line of defense:** public entry points turn a
  recoverable panic into `ErrInternal` (an `*InternalError` with `Op`, `Page`,
  `Value`, `Stack`; its message never includes document bytes). It is still a
  bug — fuzz targets fail on it — and fatal runtime errors (stack overflow, OOM)
  cannot be recovered, so recursion and allocation must be bounded up front.

Goal: a malicious file fails a single parse; it must NOT crash the process, OOM,
or leak memory.

## 3. Privacy-first via WASM

- In client-side mode the whole pipeline (parse → render → annotate → redact →
  extract) runs entirely in the browser.
- **Do NOT add any network request** that sends PDF content / annotations /
  extraction results out on the WASM path. This is the core promise to
  privacy-sensitive customers.
- Telemetry (if any) may only be anonymous, opt-in metrics, NEVER including
  document content.

## 4. Cloud tier (Go backend)

- **Secrets & PII:** never log document content, PII, or secrets. Scrub logs.
- **API keys:** store `key_hash` (hashed, not plaintext) + a `prefix` for
  display. Support `revoked_at`. Scope by `scopes[]`.
- **Share links:** `token` with enough entropy (≥ 64 chars), `password_hash`
  (not plaintext), mandatory `expires_at` check, track `view_count`.
- **Multi-tenant isolation:** EVERY query must be scoped by `org_id`. No
  cross-org queries. Check membership + role before any operation.
- **Auth:** JWT + OAuth (Better-Auth or hand-written). Verify signatures, check
  token expiry.
- **Storage:** original files in R2 with an unguessable `storage_key`; `sha256`
  for dedupe + integrity check.
- **Object storage access:** use time-limited signed URLs, no public buckets.
- **Rate limiting** on upload/extract/ask endpoints to prevent abuse and protect
  LLM costs.

## 5. Digital signatures & crypto (Pro)

- PKCS#7 per spec; verify the chain and check cert expiry when verifying an
  existing signature.
- Use Go's standard crypto libraries (`crypto/*`); never roll your own
  algorithms.
- Never embed a private key in a client/WASM bundle.

## 6. Legal / License (see §18.4)

- **Clean-room:** implement the parser/renderer based solely on the **public ISO
  32000-1:2008**. Do NOT read/copy source from Nutrient, Apryse, or MuPDF.
- **MuPDF (AGPL):** only via the `mupdf` build tag. Document clearly in the
  README: a binary built with this tag is subject to AGPL; the default build
  (pure Go) stays MIT. Consult IP counsel before shipping this feature.
- **Tesseract OCR:** via the `ocr` build tag, with its dependent license stated
  clearly.
- **Redaction disclaimer:** the Pro docs must include a disclaimer + a public
  test suite proving safety (reducing legal risk for customers).

## 7. Test data

- Do NOT commit PDFs containing real PII / sensitive data into
  `testing/corpus/`. Use synthetic or anonymized documents.
- Do NOT commit secrets, real API keys, or private cert keys into the repo.

## Reporting a security issue

Report security vulnerabilities privately to security@fluxdocs.dev (do not open a
public issue). Coordinated disclosure before going public.

---

**Golden rule:** if a change weakens any item above to "go faster" or to "make a
test pass" — STOP and ask. In FluxDocs, being security-correct matters more than
being fast.

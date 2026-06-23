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

**FluxDocs MUST do it correctly:**
- When redacting, **completely remove** the text-drawing operators (`Tj`/`TJ`)
  that intersect the redaction region from the content stream, then re-encode
  the stream. Do NOT just paint over.
- Also remove metadata, annotations, alt-text, and any hidden objects that may
  contain the same content in that region.
- The black rectangle (`--fd-redact-fill: #18181b`, solid, non-transparent) is
  for display only — NOT the primary security mechanism.
- `RedactAndFlatten()` is an **irreversible** operation — document this clearly
  in the API.

**Test gate (non-negotiable):**
- After any redaction, re-run `ExtractText()` and attempt copy-paste over the
  redacted region → it must be empty / not match the original content.
- The living test suite is in `testing/security/redaction/`. **A PR touching
  redaction does not merge unless the pass rate is 100%.**
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

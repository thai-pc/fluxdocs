# FluxDocs

> **View. Annotate. Extract. All in Go, all in MIT.**

FluxDocs is an SDK for viewing and annotating PDFs, with a core written
**entirely in Go**, released under the MIT license and compilable to **WASM** so
it runs directly in the browser — no need to send documents to a server. It
fills the gap between two extremes: expensive commercial SDKs (Nutrient/PSPDFKit)
and scattered, feature-thin open-source libraries (pdf.js, pdf-lib).

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go core](https://img.shields.io/badge/core-100%25%20Go-00ADD8)
![Status](https://img.shields.io/badge/status-alpha-yellow)

> **Status:** Alpha (v0.1.0) — the Wave-1 Core is functional end to end (parse →
> render → annotate → merge/split → save → WASM → CLI), and Wave-2 Pro features
> have started (safe redaction, watermark, form read/fill). See
> [Progress](#progress).

## Why FluxDocs

- **A real Go core** — all PDF parsing and rendering runs in Go, not a thin
  binding calling down into a C/C++ library through CGO.
- **One codebase, two build targets** — the same source produces a native binary
  (server, CLI) and WASM (browser).
- **Privacy-first** — sensitive documents (contracts, medical records, finance)
  are processed right in the browser; not a single byte leaves the user's
  machine.
- **MIT-licensed** — self-host the core for free, with transparent pricing and no
  sales-gated quotes like Nutrient ($25k–220k/year).

## Three product tiers

| Tier | License | Features |
|---|---|---|
| **Core** | MIT, free | rendering, basic annotation, text extraction, merge/split, WASM build |
| **Pro** | One-time | form fill, e-signature, safe redaction, OCR, document compare, watermark |
| **Cloud** | Subscription | AI document Q&A, structured extraction, automatic PII redaction, collaboration, hosted API |

## Repository layout

```
core/        Go engine (parse, render, annotation, docops, form, sign, extract, ocr)
cmd/         the `fluxdocs` command-line tool
cloud/       Go backend for the Cloud tier
wasm/        entry point for the WASM build
packages/    JS/TS wrappers (@fluxdocs/react, /vue, …)
examples/    runnable demos: go-server-demo (HTTP), cli-batch-redact-demo (+ react/vue/ai)
apps/        docs site + landing page
testing/     PDF corpus, golden files, security tests, benchmarks
docs/        spec.md — the full technical specification (reference document)
.claude/     guidance for AI: CLAUDE.md, AGENTS.md, CONVENTIONS.md, SECURITY.md
```

## Progress

**Wave 1 — Core (MIT), functional:**
- **Parser** — object model, lexer, indirect objects, streams, classic + stream
  xref, compressed object streams, page-tree flattening, lenient recovery.
- **Text extraction** — content-stream interpreter with `/ToUnicode` CMaps.
- **Rendering** to PNG/JPEG — vector paths, embedded TrueType/CFF glyphs, image
  XObjects (JPEG/raw/ImageMask/SMask), form XObjects, and an annotation overlay.
- **Annotation layer** — CRUD kept separate from the PDF, JSON export/import, and
  permanent flattening into the content stream.
- **PDF writer** — merge, split, save, reorder, rotate (a real object-graph
  copier with cycle/DoS guards).
- **WASM entry point** (`globalThis.fluxdocs`) and the **`fluxdocs` CLI**
  (info/extract/render/merge/split/rotate/watermark/redact/autoredact/sign/
  compare/pdfa).
- **Client SDKs** — `@fluxdocs/core` (typed WASM wrapper), `@fluxdocs/react`,
  `@fluxdocs/vue`.
- **Hardening** — fuzz targets (millions of executions, no crashes) and a
  synthetic clean/malformed PDF corpus round-trip.

**Wave 2 — Pro, in progress:**
- **Safe redaction** — text-showing operators are removed from the content
  stream (not painted over); the leak gate passes 100%.
- **Watermark** — translucent, rotated text stamp.
- **Forms** — AcroForm read, fill, and flatten.
- **Document compare** — page-by-page word diff of two documents' text.
- **Digital signatures** — a detached PKCS#7/CMS signature (pure-Go, RSA +
  SHA-256) embedded via an incremental update, so the original bytes stay
  verbatim and the signature validates.
- **OCR** — Tesseract via CGO behind the `ocr` build tag, so the default build
  stays 100% pure Go (`go test -tags ocr ./core/ocr/...`).
- **PDF/A-1b tagging** — adds the XMP conformance metadata, an sRGB OutputIntent
  with an embedded ICC profile, and a file identifier, plus a structural
  validator (font embedding and certification are follow-ups).
- **Agent-native MCP server** (`fluxdocs-mcp`) — a self-hostable Model Context
  Protocol server exposing open/extract/render/redact/auto_redact_pii/watermark/
  compare/pdfa/forms/sign — plus the Cloud AI tools extract_structured and
  ask_document — as tools an AI agent can call; documents stay on the user's own
  machine (the AI tools call out only to the configured LLM backend).

Wave 2 is feature-complete.

**Wave 3 — Cloud, started:**
- **AI structured extraction & Q&A** — `Document.ExtractStructured(schema)` and
  `Document.AskDocument(question)`. The pure-Go core reads the text locally and
  delegates to an injected `core.AIClient`; the concrete backend
  (`cloud/ai`) is a Claude Messages API client written on a standard
  `net/http` client (no SDK), using a forced tool call for schema-shaped JSON.
  The core makes **no** network call itself — install a backend with
  `SetAIClient`, keeping the MIT/WASM core network-free. See
  `examples/ai-extraction-demo`.
- **Cloud HTTP backend** (`cloud/api`, binary `fluxdocs-api`) — a Chi-routed REST
  API over the core: upload, metadata, text extraction, auto-redact PII, and AI
  structured extraction. Authenticated by API key (**stored only as a hash**) and
  **org-scoped** — every document read/write is keyed by `org_id`, so one tenant
  can never read another's documents (SECURITY.md §4). Requests authenticate by
  API key **or** a short-lived HS256 **JWT** (hand-written, stdlib-only) obtained
  from `POST /v1/token`. Persistence sits behind
  `DocumentStore`/`KeyStore` interfaces with two implementations: **PostgreSQL**
  (`database/sql`, org-scoped parameterized queries, embedded schema, keys stored
  as hashes) selected via `DATABASE_URL`, and in-memory for tests/local dev. The
  document blob store is chosen independently: **Cloudflare R2** (S3 REST + a
  hand-written AWS SigV4 signer, no SDK, objects under an `org_id/` prefix) when
  R2 is configured, else the database, else in-memory. It also mints **share
  links** — a
  public `GET /shared/{token}` that serves one document without an API key,
  gated by an unguessable token plus an optional **bcrypt-hashed password** and
  server-enforced **expiry** (SECURITY.md §4); the token is redacted from
  request logs. Long-running work runs as **async jobs** —
  `POST /documents/{id}/redact-pii-async` returns a job id (202) and
  `GET /jobs/{id}` polls it — on a bounded worker pool behind a `JobQueue`
  interface (River, a PostgreSQL-backed queue, is the durable production
  backend). Requests are throttled per org with a **token-bucket rate limiter**
  (429 + `Retry-After`), so one tenant can't starve another. The whole surface is
  testable with no external services (httptest + sqlmock).

## Developing

Build/test commands and the definition of done are in
[`.claude/AGENTS.md`](.claude/AGENTS.md); coding conventions are in
[`.claude/CONVENTIONS.md`](.claude/CONVENTIONS.md). In short:

```bash
go build ./...                                    # native build
GOOS=js GOARCH=wasm go build -o /dev/null ./wasm  # WASM build (must stay green)
go test -race ./...                               # tests with the race detector
```

> The redaction security gate (`go test ./testing/security/redaction/...`) runs
> in CI once real tests exist; see [`.claude/SECURITY.md`](.claude/SECURITY.md).

## Security

Redaction must **permanently** remove content from the document (not just paint a
black rectangle over it), the parser always treats input files as untrusted, and
the WASM path sends no data anywhere. Details: [`.claude/SECURITY.md`](.claude/SECURITY.md).
Report vulnerabilities to security@fluxdocs.dev.

## License

Core: [MIT](LICENSE). Optional features enabled via build tags (`mupdf` — AGPL,
`ocr` — Apache-2.0) are governed by their own licenses — see the note in
[`LICENSE`](LICENSE).

# FluxDocs

> **View. Annotate. Extract. All in Go, all in MIT.**

FluxDocs is an SDK for viewing and annotating PDFs, with a core written
**entirely in Go**, released under the MIT license and compilable to **WASM** so
it runs directly in the browser — no need to send documents to a server. It
fills the gap between two extremes: expensive commercial SDKs (Nutrient/PSPDFKit)
and scattered, feature-thin open-source libraries (pdf.js, pdf-lib).

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go core](https://img.shields.io/badge/core-100%25%20Go-00ADD8)
![Status](https://img.shields.io/badge/status-pre--build-orange)

> **Status:** Pre-build / Planning (v0.1.0) — laying the foundations. See
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
examples/    demos: go-server, react, vue, cli-batch-redact, ai-extraction
apps/        docs site + landing page
testing/     PDF corpus, golden files, security tests, benchmarks
docs/        spec.md — the full technical specification (reference document)
.claude/     guidance for AI: CLAUDE.md, AGENTS.md, CONVENTIONS.md, SECURITY.md
```

## Progress

Done:
- Core type system and the full `Document` API surface (per spec §6–§7).
- PDF parsing layer: object model, lexer, indirect objects, streams, the
  (classic) xref table, and a reference resolver.
- Continuous integration (GitHub Actions): native + WASM builds, `go test -race`,
  and a redaction security gate.

Next: turn the resolver into a flattened page tree, then render the first page to
a raster image.

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

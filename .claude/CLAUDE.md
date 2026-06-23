# FluxDocs — Project Context & Rules for AI

> An orientation summary so any AI assistant (Claude Code, Copilot, etc.)
> working in this repo understands **what the project is, how to write code, and
> which boundaries must not be crossed**.
> Full source of truth: `docs/spec.md` (Technical Specification v0.1.0).
> Code conventions (comments, inputs, returns, errors): `.claude/CONVENTIONS.md`.

---

## 1. What the project is

**FluxDocs** = a PDF processing & annotation SDK, with a **core written 100% in
Go**, MIT-licensed, part of the Flux family (FluxFiles, FluxGantt).

Goal: fill the gap between expensive enterprise SDKs (Nutrient/PSPDFKit
$25k–220k/year) and scattered OSS libraries (pdf.js renders only, pdf-lib does
basic editing only).

Core technical differentiators — **always keep these true when coding**:
1. **A real Go core**, not a thin binding over CGO. The heavy work (parse +
   render PDF) runs entirely in Go.
2. **One core, two build targets**: native binary (server/CLI) + WASM
   (`GOOS=js GOARCH=wasm`) running client-side.
3. **Privacy-first via WASM**: sensitive documents are processed entirely in the
   browser; not a single byte is sent to a server.
4. **Annotations kept separate from the original PDF**: stored as a separate JSON
   layer, applied at render time or on flatten.

## 2. Three product tiers (this drives where code goes)

| Tier | License | Features | Code location |
|---|---|---|---|
| **Core** | MIT, free | render, basic annotation, text extraction, merge/split, WASM | `core/` (except `ocr/`, `sign/`) |
| **Pro** | One-time | form fill, e-signature, redaction, OCR, compare, watermark, PDF/A | `core/form`, `core/sign`, `core/docops/redact`, `core/ocr` |
| **Cloud** | Subscription | AI Q&A, structured extraction, auto-redact PII, collaboration, hosted API | `cloud/` |

**License-tier separation rule:** pure-MIT code must not `import` a
CGO-dependent package (Tesseract OCR, MuPDF). CGO parts MUST sit behind a **build
tag** (`//go:build ocr` / `//go:build mupdf`) so the default build stays 100%
MIT. This is a legal constraint, not an option.

## 3. Tech stack

- **Core language:** Go 1.23+. Plus a WASM build target.
- **PDF parsing:** a hand-written parser following **PDF 32000-1:2008 (ISO)**.
  Clean-room — do NOT consult Nutrient/Apryse source (see `SECURITY.md`, Legal).
- **Rendering:** raster PNG/JPEG (server thumbnails) + SVG (vector-accurate
  client) + Canvas API (WASM).
- **Concurrency:** a goroutine worker pool, bounded by `runtime.NumCPU()`.
- **OCR:** Tesseract via CGO, build tag `ocr` (Pro).
- **AI:** call an LLM API (Claude) over a standard Go HTTP client — no other
  language SDKs.
- **Cloud backend:** Go + Chi/Echo + PostgreSQL 16 + sqlc + Cloudflare R2 + River
  queue + Stripe + Fly.io.
- **Client wrappers:** `@fluxdocs/react`, `@fluxdocs/vue` (Wave 1); web-components,
  cloud-sdk later.
- **Monorepo:** `go.work` (Go multi-module) + `pnpm-workspace.yaml` (JS side).
- **Module path (reality):** the repo currently lives at
  `github.com/thai-pc/fluxdocs` → imports are `github.com/thai-pc/fluxdocs/core`.
  The spec (`docs/spec.md`) still names the target brand `fluxtoolkit`; use the
  `thai-pc` path for all imports/`go get` until we move to an org.

## 4. Directory structure (see the real tree in the repo)

```
core/        # main Go engine: parse, render, annotation, docops, form, sign, extract, ocr
cmd/         # the `fluxdocs` CLI tool
cloud/       # Go backend (Cloud tier): api, queue, db
wasm/        # WASM build entry point, exporting funcs via syscall/js
packages/    # JS/TS wrappers (@fluxdocs/*)
examples/    # demos: go-server, react, vue, cli-batch-redact, ai-extraction
apps/        # docs site + landing page
testing/     # PDF corpus, golden files, security tests, e2e, benchmarks, fixtures
.claude/     # this file + AGENTS.md + CONVENTIONS.md + SECURITY.md
```

Place code in the right package per spec §10.4: `core`, `render`, `annotation`,
`extract`, `ocr`, `sign`, `cloud`.

## 5. Coding conventions

The full rulebook lives in **`.claude/CONVENTIONS.md`** (comments, inputs,
outputs/returns, errors, naming, concurrency, tests). Highlights:

### Go
- **Naming:** PascalCase for exported, verb-first. No `Get` prefix for simple
  fields (per Effective Go) — but follow the spec for the documented §7 API
  surface. Package names: short, lowercase, no underscores.
  - Good: `doc.RenderPage(0, opts)`, `doc.AddAnnotation(a)`, `doc.ExtractText(opts)`
  - Bad: `doc.GetRenderedPageAsImage(0)`, `doc.render_page(...)`, `doc.DoOperation("render", 0)`
- **Error handling:** return `error` as the last value. Do NOT `panic` for normal
  runtime errors (only for unrecoverable logic bugs). Use sentinel errors +
  `errors.Is`: `ErrPageNotFound`, `ErrEncryptedDocument`, `ErrInvalidPDF`
  (defined in `core/errors.go`).
- **ID types:** use controlled type aliases (`DocumentID`, `PageID`,
  `AnnotationID`, `LayerID`) — avoid mixing IDs; never pass bare `string`.
- **Concurrency:** a worker pool bounded by a semaphore
  `make(chan struct{}, runtime.NumCPU())`. Each page renders independently
  (pages parallel; within a page sequential, since the content stream is
  stateful).
- **Build tags:** anything CGO/license-dependent sits behind its own build tag.

### Language
- **All code, comments, identifiers, commits, and docs are in English.**
  Vietnamese is for maintainer chat only.

### CSS / Client UI (BEM, `fd-` prefix)
- Classes: `.fd-viewer`, `.fd-viewer__canvas`, `.fd-annotation--highlight`,
  `.fd-annotation--selected`.
- CSS custom property prefix: `--fd-*`.
- Avoid collisions with the host application — always prefix `fd-`.

### Design tokens (see spec §8.2)
- Primary `#6366f1` (indigo), default annotation amber `#f59e0b`, redact fill
  `#18181b` (solid black, NOT transparent).
- Fonts: Inter (UI), JetBrains Mono (code). Base size 13px (high density).
- Respect `prefers-reduced-motion`. WCAG 2.1 AA for toolbar/sidebar.

## 6. Testing rules (see `testing/README.md` for details)

- **Parser:** table-driven tests. Real PDFs in `testing/corpus/` — including
  **malformed** files (most real-world PDFs violate the spec). Lenient parsing +
  brute-force scan fallback.
- **Render:** golden-file tests — compare PNG output against references in
  `*/testdata/golden/`. When rendering changes, regenerate goldens deliberately
  and review the diff carefully.
- **CI (GitHub Actions):** must be green for both **native** and **WASM** targets
  before merge.
- **Redaction:** see section 7 — mandatory tests, non-negotiable.
- **Coverage grows incrementally:** Wave 1 only needs to handle the common ~90%
  of real PDFs (from Word, LaTeX, Chrome print, Adobe). Do not force 100% spec
  coverage on day one.

## 7. SECURITY — read carefully, this is reputation risk #1

Full detail in `.claude/SECURITY.md`. Hard constraints in brief:

1. **Redaction must PERMANENTLY remove content from the content stream**, not
   just paint a black rectangle over it. The "paint-over" bug is a common flaw
   that has destroyed organizations' reputations.
   - After any redaction, a mandatory test re-runs `ExtractText()` over the
     redacted region → it must return empty / not match the original content.
   - A PR touching redaction does NOT merge unless `testing/security/redaction/`
     passes 100%.
   - Hidden metadata/annotations that may hold the same content must also be
     removed.
2. **WASM privacy:** in client-side mode, do NOT add any network call that sends
   PDF content out. This is the product's core promise.
3. **Safe parser:** input is an untrusted file. Defend against decompression
   bombs, infinite xref loops (incremental-update `/Prev`), nested object
   streams, integer overflow when computing canvas size. Fuzz seeds in
   `testing/security/fuzz/`.
4. **Cloud:** never log secrets/PII. `api_keys` store a **hash** (not plaintext);
   share links have `password_hash` + `expires_at`. Every query is scoped by
   `org_id` (multi-tenant isolation). Authenticate via JWT/OAuth.
5. **License/legal:** clean-room implementation from the public ISO 32000. MuPDF
   (AGPL) only via the `mupdf` build tag; document clearly that binaries with
   that tag are subject to AGPL.

## 8. Roadmap (know where we are)

- **Wave 1 (Weeks 1–8, Core MIT):** parser, raster+WASM rendering, 4 annotation
  types, React/Vue wrappers, merge/split, text extraction, docs + launch.
- **Wave 2 (Weeks 11–18, Pro):** forms, PKCS#7 e-signature, **safe redaction**,
  OCR, compare, watermark, PDF/A.
- **Wave 3 (Month 6+, Cloud):** backend API, AI extraction/Q&A, auto-redact PII,
  CRDT-lite collaboration, webhook/Zapier.

## 9. When an AI works in this repo

- Default to **pure-Go, MIT-safe**. If a change needs CGO → put it behind a build
  tag and say so explicitly.
- Keep annotations **separate** from the original PDF (JSON layer); don't bake
  them in unless calling `Flatten*`.
- Before writing redaction/parser/crypto code → read `.claude/SECURITY.md`.
- Respect tier boundaries: don't let Core code import Pro/Cloud code.
- When unsure about correct PDF behavior → consult ISO 32000-1:2008, don't guess.
- Brand voice in docs/comments: direct, technical, concrete benchmarks. Avoid
  hollow "revolutionary" / "enterprise-grade" filler.

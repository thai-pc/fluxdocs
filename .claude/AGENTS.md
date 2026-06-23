# AGENTS.md — FluxDocs

> Operating guide for AI coding agents (Claude Code, Cursor, Copilot Agent,
> Aider…) working in the FluxDocs repo.
> Read alongside `.claude/CLAUDE.md` (project rules), `.claude/CONVENTIONS.md`
> (code conventions), and `.claude/SECURITY.md` (security constraints).
> This is a greenfield repo (pre-build): many directories are still `.gitkeep`
> placeholders — you will be the one creating real files.

---

## TL;DR for agents

- **Primary language:** Go 1.23+. The core must be pure-Go, MIT-safe.
- **Two build targets:** native + WASM. Don't break the WASM build.
- **Tier-separated license:** Core (MIT) ⊄ Pro/Cloud. CGO only behind a build tag.
- **Safe redaction & parser = top priority.** Read SECURITY.md before touching.
- **Annotations are separate** from the original PDF (JSON layer); don't bake
  them in unless `Flatten*`.
- **All committed text is English.** Vietnamese only in maintainer chat.

## How to organize work

1. **Identify the tier** of the change (Core / Pro / Cloud) → decide which
   package the code goes in (see the table in CLAUDE.md §2).
2. **Plan small, commit per package.** Don't mix parse + render + cloud in one
   change.
3. **Write tests alongside code** (table-driven for parsers, golden-file for
   rendering).
4. **Run the local gates** (below) before reporting done.

## Build & test commands (use / create once `go.work` exists)

```bash
# Native build
go build ./...

# WASM build (MUST stay green — the product's core differentiator)
GOOS=js GOARCH=wasm go build -o /dev/null ./wasm

# Full test suite (race detector on for concurrency code)
go test -race ./...

# Test with the Pro (CGO) build tag
go test -tags ocr ./core/ocr/...

# Security gate: redaction leak test — MUST pass 100% to merge
go test ./testing/security/redaction/...

# Lint / vet
go vet ./...
gofmt -l .          # must print nothing (everything formatted)

# Benchmark
go test -bench=. ./testing/benchmark/...
```

> JS wrappers: `pnpm install && pnpm -r build && pnpm -r test` under `packages/`.

## Definition of Done (agent self-verifies before reporting done)

- [ ] `go build ./...` green
- [ ] `GOOS=js GOARCH=wasm go build ./wasm` green (if touching core/render/annotation)
- [ ] `go test -race ./...` green
- [ ] `gofmt -l .` prints nothing; `go vet ./...` clean
- [ ] If touching redaction → `testing/security/redaction/` passes 100%
- [ ] No Core code imports a Pro/Cloud package or a CGO package (except behind a build tag)
- [ ] No network call sending PDF content out on the WASM/client path
- [ ] New tests included; goldens (if rendering changed) regenerated deliberately and reviewed
- [ ] No Vietnamese in committed files

## Boundaries — what an agent must NOT do on its own

- ❌ Bypass / weaken / skip redaction tests to make them "pass".
- ❌ Add a CGO dependency to the default build path (breaks MIT).
- ❌ Copy code from Nutrient/Apryse/MuPDF source (clean-room — only the public
  ISO 32000).
- ❌ Send PDF content / PII to an external service in client-side privacy mode.
- ❌ Commit secrets, real keys, or PDFs with real sensitive data into
  `testing/corpus/`.
- ❌ Add cross-tier `import`s that drag Pro/Cloud into Core.
- ❌ `git push` / publish npm / cut a release without being clearly asked.

## When to stop and ask the maintainer

- A change needs a license trade-off (e.g. using MuPDF via CGO for better
  rendering) → ask first.
- Ambiguous PDF behavior that ISO 32000 doesn't settle → propose options, ask.
- Changing the public API (`core/fluxdocs.go`, types in `core/types.go`) →
  confirm, since it affects consumers.
- Cloud DB schema decisions (`cloud/db/schema.sql`) → affects migrations, confirm.

## Quick style

- Go: follow `gofmt` + Effective Go. Sentinel errors + `errors.Is`. No redundant
  `Get` prefix. Full conventions: `.claude/CONVENTIONS.md`.
- Comments & docs: direct, technical, concrete benchmarks. No marketing filler.
- CSS: BEM, prefix `fd-` / `--fd-*`.
- Commit messages: short, describe the change per package, e.g.
  `core/parse: handle compressed xref streams`.

## References

- Full spec: `docs/spec.md`
- Project rules: `.claude/CLAUDE.md`
- Code conventions: `.claude/CONVENTIONS.md`
- Security constraints: `.claude/SECURITY.md`
- Testing strategy: `testing/README.md`
- Reference algorithms (xref resolution, content stream, redaction, parallel
  render): spec §13 + Appendix B.

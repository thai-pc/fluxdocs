---
description: Run the FluxDocs Definition-of-Done gate suite and report pass/fail
argument-hint: (none)
---

Run the local "Definition of Done" gates for FluxDocs (see `.claude/AGENTS.md`)
from the repo root `/usr/local/var/www/fluxdocs` and report a crisp pass/fail for
each. Do NOT fix anything — just run and report. Run these:

1. `go build ./...` — native build.
2. `GOOS=js GOARCH=wasm go build ./core/...` — WASM build (must stay green).
3. `go test -race ./core/...` — full test suite with the race detector.
4. `gofmt -l core/` — must print nothing (everything formatted).
5. `go vet ./core/...` — must be clean.
6. Only if the diff touches redaction (`git -C /usr/local/var/www/fluxdocs diff
   --name-only` includes `redact` or `testing/security/redaction`):
   `go test ./testing/security/redaction/...` — the leak gate, must pass 100%.
7. Only if the diff touches `packages/` or `examples/`:
   `pnpm -r typecheck` and `pnpm -r test` — client wrappers + demos.

Report a checklist with ✅/❌ per gate and the exact error output for any failure.
End with an overall verdict: **all gates green** or **N failing** (list them). Do
not commit.

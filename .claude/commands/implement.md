---
description: Implement a feature with go-implementer (Go) or ui-implementer (React/Vue/TS), from its design note
argument-hint: <feature to implement, e.g. "Form XObject rendering">
---

Implement this feature:

**$ARGUMENTS**

**Pick the coder by area:** Go (`core/`, `cmd/`, `cloud/`, `mcp/`, `wasm/`) →
**go-implementer**. Client UI (`packages/`, `examples/`: TS wrapper, React/Vue
components, CSS, demos) → **ui-implementer**, whose gates are
`pnpm -r typecheck && pnpm -r test`. If the feature needs both (e.g. a new WASM
export + a viewer control), run go-implementer first, then ui-implementer with
its report. The rest of this file describes the Go path; give ui-implementer the
same design-note / no-commit instructions.

Invocation: use the named subagent type if available; if it is "not found"
(added mid-session), fall back to `general-purpose` and tell it to FIRST read
`.claude/agents/<name>.md` and fully adopt that role.

Instruct the implementer to:
- Read the design note under `docs/design/` for this feature first (if none
  exists, say so and recommend running `/spec` first), then the current code in
  the target package and a sibling file to match house style.
- Implement per the design. Respect the hard constraints: pure-Go MIT-safe,
  CGO only behind a build tag, no cross-tier imports, don't break WASM, keep
  untrusted input safe (no panic), English only, sentinel errors + `errors.Is`.
- Do NOT write the test suite (that's `/test`). Do NOT commit.
- Pass the build gates before reporting: `go build ./...`,
  `GOOS=js GOARCH=wasm go build ./core/...`, `gofmt -l core/` (empty),
  `go vet ./core/...`.

Report back: files added/changed, signature/API changes, what's supported vs
deferred, any deviation from the design, and the exact gate results. This is
pipeline step 2 — offer to continue with `/review` → `/fix` → `/test` (Go) /
`/ui-test` (UI).

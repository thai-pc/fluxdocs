---
description: Run the spec → code → review → fix → test (Go + UI/UX) → final review subagent pipeline for a feature
argument-hint: <feature description, e.g. "render Form XObjects" or "zoom controls in the viewer">
---

Run the full FluxDocs feature pipeline for this feature:

**$ARGUMENTS**

```
spec-writer → go-implementer / ui-implementer → go-reviewer → fixer ⟲ go-reviewer
 (plan)        (code: Go core / client UI)      (review)      (fix, ≤2 rounds)
            → go-tester + ui-tester → fixer (if bugs) → go-reviewer (final)
              (tests: Go / UI-UX)
```

Orchestrate the project subagents **sequentially** (each step depends on the
previous — never run two steps that edit files in parallel). Use the named
subagent types (`spec-writer`, `go-implementer`, `ui-implementer`,
`go-reviewer`, `fixer`, `go-tester`, `ui-tester`); if a name is "not found"
(agents added mid-session), fall back to `general-purpose` and tell that agent to
FIRST read the matching `.claude/agents/<name>.md` and fully adopt that role.

Each subagent starts cold: give it the repo root `/usr/local/var/www/fluxdocs`,
the relevant packages, the current state, the design-note path, and the previous
step's report. Pass each step's output to the next.

**Route by area.** Go side = `core/`, `cmd/`, `cloud/`, `mcp/`, `wasm/`. UI side =
`packages/`, `examples/`. A feature can need both (e.g. a new WASM export plus a
viewer control): then run the Go coder first, the UI coder second.

1. **spec-writer (plan)** — design note at `docs/design/<feature>.md`: scope +
   tier, algorithm with ISO 32000 references, Wave-1 support matrix, security
   bounds, Go API sketch; for UI work also the props/events, DOM + `fd-` classes,
   keyboard map (spec §8.4) and a11y requirements (§8.5). List the open
   questions. Skip this step only for a small, well-understood change.
   **If the note has open questions that need a maintainer decision (public API,
   license, schema), stop and ask the user before coding.**
2. **Code** — `go-implementer` for the Go side, `ui-implementer` for the UI side,
   per the design note. Gates: Go `go build ./...`,
   `GOOS=js GOARCH=wasm go build ./core/... ./wasm/...`, `gofmt -l .` (empty),
   `go vet ./...`; UI `pnpm -r typecheck && pnpm -r test`. No commit.
3. **go-reviewer (review)** — read-only review of the whole diff, Go and TS
   (security → correctness → tier → conventions → UI rules). Verdict +
   Blocker/Should-fix/Nit with file:line.
4. **fixer (fix)** — only if step 3 reported Blockers or Should-fix items. Pass
   the findings verbatim. After it fixes any Blocker, re-run `go-reviewer`.
   Loop fixer ⟲ reviewer at most **2 rounds**; if Blockers remain, stop and report
   to the user. Nits may be batched into step 5.
5. **Test** —
   - `go-tester` if Go changed: table-driven / pixel assertions / robustness /
     redaction gate / `-race`. No weakened security tests.
   - `ui-tester` if UI changed: component tests for React + Vue, a11y audit,
     privacy check, browser E2E when Chrome tools are available.
   If either reports a product bug, send that list to **fixer** (one round),
   then re-run the affected tester's gates.
6. **go-reviewer (final)** — confirm tests are real (not tautological), findings
   closed, gates green. Final verdict.

After the pipeline, **independently re-run the gates yourself** (trust but
verify): `go test -race ./...`, `go build ./...`,
`GOOS=js GOARCH=wasm go build ./core/... ./wasm/...`, `gofmt -l .`,
`go vet ./...`, and if UI changed `pnpm -r typecheck && pnpm -r test`. Then
summarize per step: what it produced, findings opened/closed, tests added,
layers skipped. **Commit only if the user asks**; if you do, use per-package
messages (`core/render: …`, `packages/react: …`) with the required
Co-Authored-By line, and list any follow-ups the reviewer deferred.

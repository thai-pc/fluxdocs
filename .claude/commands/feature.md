---
description: Run the spec→implement→review→test→review subagent pipeline for a feature
argument-hint: <feature description, e.g. "render Form XObjects">
---

Run the full FluxDocs feature pipeline for this feature:

**$ARGUMENTS**

Orchestrate the four project subagents **sequentially** (each step depends on the
previous — do NOT run them in parallel). Use the named subagent types
(`spec-writer`, `go-implementer`, `go-reviewer`, `go-tester`) if they are
available; if a name is "not found" (e.g. the agents were added mid-session),
fall back to `general-purpose` and tell that agent to FIRST read the matching
`.claude/agents/<name>.md` file and fully adopt that role before doing the work.

Give each subagent enough context to start cold: repo root
`/usr/local/var/www/fluxdocs`, the relevant package, the current state, and where
to read/write artifacts. Pass each step's output to the next.

1. **spec-writer** — design the feature (docs only). Write a concise design note
   under `docs/design/<feature>.md`: scope, algorithm with ISO 32000 references,
   the Wave-1 support matrix, security bounds, and the package API sketch. Report
   key decisions and any question the implementer must resolve.
2. **go-implementer** — implement it in Go per the design note. Match house style,
   respect tier/CGO/WASM rules, keep untrusted input safe. Must pass the gates:
   `go build ./...`, `GOOS=js GOARCH=wasm go build ./core/...`, `gofmt -l core/`
   (empty), `go vet ./core/...`. Do not write the test suite; do not commit.
3. **go-reviewer** — read-only review of the diff (security → correctness → tier →
   conventions). Report Blocker/Should-fix/Nit + verdict. **If there are any
   Blockers, send them back to go-implementer to fix, then re-review** before
   continuing.
4. **go-tester** — write meaningful tests (table-driven / deterministic pixel
   assertions / security-skip cases; no weakened security tests) and apply any
   small hardening nits. Run the gates including `go test -race ./...`. Do not
   commit.
5. **go-reviewer** — final read-only pass: confirm tests are real (not
   tautological), nits applied, gates green. Give the final verdict.

After the pipeline: **independently re-run the gates yourself** (trust but
verify) — `go test -race ./core/...`, native + WASM build, `gofmt -l core/`,
`go vet ./core/...`. Then summarize each step's outcome. **Commit only if the
user asks**; if you do, end the message with the required Co-Authored-By line and
list any Wave-2 follow-ups the reviewer flagged.

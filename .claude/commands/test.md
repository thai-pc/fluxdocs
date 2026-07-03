---
description: Add/extend tests for the current change with the go-tester subagent and run the gates
argument-hint: [optional target, e.g. "core/render image XObjects"]
---

Run the **go-tester** subagent to cover the current change (default) or the
target named here: **$ARGUMENTS**

Invocation: use the `go-tester` subagent type if available; if it is "not found"
(added mid-session), fall back to `general-purpose` and tell it to FIRST read
`.claude/agents/go-tester.md` and fully adopt that test-engineer role.

Instruct the tester to:
- Start from `git -C /usr/local/var/www/fluxdocs status`/`diff` to see what changed,
  read the code under test and a sibling `_test.go` first to match house style,
  and any design note under `docs/design/`.
- Add **meaningful** tests, not tautological ones: table-driven for parsing;
  deterministic pixel/dimension assertions for render (decode the PNG, assert
  specific pixels/orientation — do NOT commit binary goldens); and untrusted-input
  **robustness cases** that assert graceful skip / no panic. If redaction is
  touched, the mandatory leak test (re-extract the redacted region → empty) is
  required. Never weaken or skip a security test to go green.
- It may apply small hardening nits to production code, but must not paper over a
  real bug — if a test reveals one, describe it precisely and hand it back.
- Run the gates and make them green:
  `go test -race ./core/...`, `go build ./...`,
  `GOOS=js GOARCH=wasm go build ./core/...`, `gofmt -l core/`, `go vet ./core/...`.
- Do NOT commit.

Report back: tests added (file + what each asserts), any nit edits, the exact
gate output, and remaining coverage gaps. After it finishes, independently re-run
`go test -race ./core/...` yourself to confirm before summarizing.

---
description: Review the current diff with the go-reviewer subagent (security/correctness/tier/conventions)
argument-hint: [optional focus, e.g. "core/render" or a PR number]
---

Run the **go-reviewer** subagent to review the current uncommitted change
(default) or the target named here: **$ARGUMENTS**

Invocation: use the `go-reviewer` subagent type if available; if it is "not
found" (added mid-session), fall back to `general-purpose` and tell it to FIRST
read `.claude/agents/go-reviewer.md` and fully adopt that read-only reviewer role
before reviewing.

Instruct the reviewer to:
- Start from `git -C /usr/local/var/www/fluxdocs status` and `git ... diff` to see
  exactly what changed (or the given path/PR), and read the matching design note
  under `docs/design/` if one exists.
- Review in priority order: **security** (SECURITY.md §2–3: bound untrusted
  input, no panic/overflow/OOB, redaction removes content) → **correctness** →
  **tier/license** (pure-Go MIT, no cross-tier/CGO on the default path, WASM-safe,
  clean-room) → **conventions** (sentinel errors, house style, English only).
- Actually run the gates and report their real output:
  `go build ./...`, `GOOS=js GOARCH=wasm go build ./core/...`, `gofmt -l core/`,
  `go vet ./core/...`.
- It is READ-ONLY: report findings, do NOT edit or commit.

Report back grouped by severity **Blocker / Should-fix / Nit** (each with
file:line, why it matters, a concrete suggested fix), the gate results, and a
final verdict: approve / approve with nits / request changes. Relay the summary;
if there are Blockers, offer to send them to go-implementer to fix.

---
description: Apply review findings with the fixer subagent (minimal fixes + regression tests), then re-review
argument-hint: [findings to fix — paste a report, or leave empty to use the last review in this chat]
---

Run the **fixer** subagent on these findings:

**$ARGUMENTS**

If no findings were given, use the most recent `go-reviewer` / `ui-tester`
report from this conversation. If there is none, say so and offer `/review`
first — don't invent findings.

Invocation: use the `fixer` subagent type if available; if it is "not found"
(added mid-session), fall back to `general-purpose` and tell it to FIRST read
`.claude/agents/fixer.md` and fully adopt that role.

Pass the fixer the **full findings list verbatim** (severity, file:line, problem,
suggested fix) plus the repo root `/usr/local/var/www/fluxdocs` and the design
note under `docs/design/` if one exists. Instruct it to:
- Triage each finding as fix / dispute (with evidence) / escalate (maintainer
  decision) / unresolved.
- Make the smallest correct change per finding; add a regression test for every
  Blocker it fixes; never weaken or skip a test.
- Run the gates for what it touched (Go: build, WASM build, `go test -race`,
  gofmt, vet, redaction gate if relevant; TS: `pnpm -r typecheck && pnpm -r test`).
- Not commit.

When it returns:
1. Relay the per-finding status table and the gate output.
2. If any **Blocker** was fixed, run the `go-reviewer` subagent again on the
   current diff (read-only) to confirm it is closed. Repeat fixer → reviewer at
   most **2 rounds**; if Blockers remain after that, stop and hand the open items
   to the user.
3. Surface every **escalated** item as a question for the user.

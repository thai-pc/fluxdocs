# FluxDocs subagents — how to run them

Seven project subagents live in this folder (one `.md` file each). Each file has
YAML frontmatter (`name`, `description`, `tools`, `model`) followed by the
system prompt that steers that agent.

| Stage | Agent | Use it to… | Edits files? |
|---|---|---|---|
| Plan | `spec-writer` | Write the design note / spec before coding | Docs only |
| Code | `go-implementer` | Implement Go (core, cloud, cmd, wasm) on native + WASM | Yes |
| Code | `ui-implementer` | Implement the TS wrapper, React/Vue components, CSS, demos | Yes |
| Review | `go-reviewer` | Review the diff — Go and UI (security, correctness, tier, conventions) | No (read-only) |
| Fix | `fixer` | Apply review/test findings with minimal changes + regression tests | Yes |
| Test | `go-tester` | Table-driven / pixel / redaction-gate / race tests; run Go gates | Tests (+ tiny nits) |
| Test | `ui-tester` | Component tests, a11y/UX audit, privacy check, browser E2E | Tests only |

## The pipeline

```
spec-writer → go-implementer / ui-implementer → go-reviewer → fixer ⟲ go-reviewer
 (plan)        (code: Go core / client UI)      (review)      (fix, ≤2 rounds)
            → go-tester + ui-tester → fixer (if bugs) → go-reviewer (final)
              (tests: Go / UI-UX)
```

- **Route by area.** Go side = `core/`, `cmd/`, `cloud/`, `mcp/`, `wasm/`.
  UI side = `packages/`, `examples/`. A feature that needs a new WASM export plus
  a viewer control runs go-implementer first, then ui-implementer.
- **Reviewer never edits; fixer never adds features.** The reviewer's
  Blocker/Should-fix/Nit list (each with `file:line` + suggested fix) is the
  fixer's input. The fixer marks each item fixed / disputed / escalated, adds a
  regression test for every Blocker, and the reviewer re-checks. Max 2 rounds,
  then a human decides.
- **Testers report, fixer repairs.** A product bug found by go-tester or
  ui-tester goes back through the fixer, not patched inside the test.
- Skip `spec-writer` for a small, well-understood change.

## One-shot: `/feature`

```
/feature render Form XObjects
/feature zoom in/out controls in the viewer (React + Vue)
```

Runs the whole pipeline (`.claude/commands/feature.md`), re-runs the gates
itself, and summarizes. It does not commit unless you ask.

## Per-stage commands

```
/spec      <feature>   # spec-writer: design note in docs/design/ (plan)
/implement <feature>   # go-implementer or ui-implementer, routed by area (code)
/review    [focus]     # go-reviewer on the current diff, read-only (review)
/fix       [findings]  # fixer applies findings, then re-review (fix)
/test      [target]    # go-tester: Go tests + gates (test)
/ui-test   [target]    # ui-tester: React/Vue tests, a11y, privacy, browser E2E (test)
/gate                  # Definition-of-Done gates (Go + pnpm if UI touched)
```

(Note: the built-in `/code-review` reviews the working diff with a generic
recipe; these project commands drive the FluxDocs subagents with the project's
security/tier/UI checklist.)

## Invoking a subagent directly

1. **Automatic delegation.** Describe the task; Claude routes on each agent's
   `description` — "fix the review findings" → `fixer`, "check the viewer's
   accessibility" → `ui-tester`.
2. **Explicit, by name.** `Use the fixer subagent to apply these findings: …`
3. **Manage them.** `/agents` lists, creates, edits, and inspects subagents.
   Agents added mid-session may need a new session (or the commands fall back to
   `general-purpose` + "read `.claude/agents/<name>.md` first").

## Notes

- **Fresh context.** Each subagent starts cold (it does not see this chat's
  history); the orchestrator must pass paths, the design note, and the previous
  step's report. Intermediate tool output stays out of the main thread.
- **Tool limits are deliberate.** `go-reviewer` has no Write/Edit so it can't
  "fix" while reviewing; `ui-tester` writes tests but reports product bugs
  instead of fixing them. `ui-tester` also gets the Chrome browser tools for
  real E2E runs against `examples/react-vite-demo` / `examples/vue-demo`.
- **Model.** All use `model: inherit`. For a cheaper pass, set `model: sonnet`
  (e.g. on `fixer` for nit-only rounds) in the frontmatter.
- **Editing an agent.** Change the `.md` file directly (or via `/agents`). The
  body is the system prompt; the frontmatter controls routing and permissions.

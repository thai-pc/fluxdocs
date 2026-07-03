# FluxDocs subagents — how to run them

Four project subagents live in this folder (one `.md` file each). Each file has
YAML frontmatter (`name`, `description`, `tools`, `model`) followed by the
system prompt that steers that agent.

| Agent | Use it to… |
|---|---|
| `spec-writer` | Write/update design & spec docs before coding |
| `go-implementer` | Implement a feature in Go (native + WASM) |
| `go-reviewer` | Review the current diff (read-only, reports findings) |
| `go-tester` | Write tests and run the full gate suite |

## Three ways to invoke a subagent

1. **Automatic delegation.** Just describe the task in plain language. Claude
   matches it against each agent's `description` and routes to the best fit —
   e.g. "review my changes" tends to pick `go-reviewer`. Say *"use subagents"* to
   nudge it to delegate.

2. **Explicit, by name.** Name the agent so there's no ambiguity:
   - `Use the go-reviewer subagent to review the current diff.`
   - `Have go-implementer add image-XObject rendering, then go-tester cover it.`
   - `Ask spec-writer to draft the design for the annotation layer.`

3. **Manage them.** Run the `/agents` slash command to list, create, edit, or
   inspect available subagents (project + personal).

## One-shot: the `/feature` slash command

Instead of chaining by hand, run the whole pipeline with one command:

```
/feature render Form XObjects
```

It orchestrates spec → implement → review → test → review for the given feature
(defined in `.claude/commands/feature.md`), re-runs the gates, and summarizes.
It does not commit unless you ask.

Two step commands run a single stage on the current change:

```
/review [focus]     # go-reviewer on the current diff (read-only)
/test   [target]    # go-tester adds tests + runs the gates
```

(Note: the built-in `/code-review` reviews the working diff and `/review` a
GitHub PR; these project commands drive the FluxDocs subagents with the
project's security/tier checklist.)

## Recommended pipeline for a feature

Mirrors the repo's code → review → test → review rule:

```
spec-writer  →  go-implementer  →  go-reviewer  →  go-tester  →  go-reviewer
 (design)        (code)            (review)        (tests)       (final review)
```

Chain it in one request:

> Build image-XObject rendering: spec-writer designs it, go-implementer codes it,
> go-reviewer reviews, go-tester adds tests, then go-reviewer does a final pass.

Skip `spec-writer` for a small, well-understood change. Feed any **Blocker**
findings from `go-reviewer` back to `go-implementer` before continuing.

## Notes

- **Fresh context.** Each subagent starts cold (it does not see this chat's
  history); give it enough detail to work from. It reports a summary back; its
  intermediate tool output stays out of the main thread.
- **Tools.** `go-reviewer` is intentionally read-only (no Write/Edit) so it
  can't "fix" code while reviewing. The others can edit.
- **Model.** All use `model: inherit` (same model as the main session). To make a
  cheaper pass, edit an agent's frontmatter to `model: sonnet` or `model: haiku`.
- **Editing an agent.** Change the `.md` file directly (or via `/agents`). The
  body is the system prompt; the frontmatter controls routing and permissions.

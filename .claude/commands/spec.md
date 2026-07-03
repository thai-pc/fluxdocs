---
description: Design a feature with the spec-writer subagent (writes a design note, no code)
argument-hint: <feature to design, e.g. "Form XObject rendering">
---

Run the **spec-writer** subagent to design this feature (docs only, no Go code):

**$ARGUMENTS**

Invocation: use the `spec-writer` subagent type if available; if it is "not
found" (added mid-session), fall back to `general-purpose` and tell it to FIRST
read `.claude/agents/spec-writer.md` and fully adopt that role (clean-room, cite
ISO 32000, English only, no production code).

Instruct spec-writer to:
- Read the relevant current code (name the package, e.g. `core/render` or
  `core/parse`) and the ground-truth docs (`.claude/CLAUDE.md`,
  `.claude/CONVENTIONS.md`, `.claude/SECURITY.md`, `docs/spec.md`).
- Write a concise, implementation-guiding design note at
  `docs/design/<feature>.md`: scope + tier, the algorithm with ISO 32000
  references, the Wave-1 support matrix (what's in vs deferred), security bounds
  (SECURITY.md §2–3), and the package API sketch. English only, no marketing.
- Not write Go code or touch `core/`.

Report back: the file written, key decisions, and any open question the
implementer/maintainer must resolve. This is pipeline step 1 — offer to continue
with `/implement` (or `/feature` for the whole chain).

---
description: UI/UX test the client-side change with the ui-tester subagent (component tests, a11y, privacy, browser E2E)
argument-hint: [optional target, e.g. "react viewer zoom" or "vue-demo"]
---

Run the **ui-tester** subagent on the current client-side change (default) or on
the target named here: **$ARGUMENTS**

Invocation: use the `ui-tester` subagent type if available; if it is "not found"
(added mid-session), fall back to `general-purpose` and tell it to FIRST read
`.claude/agents/ui-tester.md` and fully adopt that role (it writes tests, never
production code).

Give it: repo root `/usr/local/var/www/fluxdocs`, the output of
`git status` / `git diff --name-only` limited to `packages/`, `examples/` and
`wasm/`, the design note under `docs/design/` if any, and whether Chrome browser
tools are available in this session. Instruct it to:
- Layer 1: add vitest component tests for React **and** Vue (states, pointer +
  keyboard interaction per spec §8.4, lifecycle, parity).
- Layer 2: static a11y/UX audit against spec §8 (WCAG 2.1 AA with contrast
  numbers, focus-visible, reduced motion, `fd-` BEM, `--fd-*` tokens).
- Layer 3: privacy check — no network call carrying PDF content.
- Layer 4: if browser tools are available, run the demo and drive it in a new
  tab with a synthetic PDF from `testing/corpus/clean/`; check console, network,
  mobile + desktop widths; record a GIF for user-visible changes. Otherwise
  report Layer 4 as skipped.
- Gates: `pnpm -r typecheck && pnpm -r test`. Do not commit.

When it returns, independently re-run `pnpm -r test` to confirm, then relay:
tests added, layers run/skipped, and the UX/a11y findings list. If it found any
bugs, offer `/fix` with that list.

# @fluxdocs/vue

Vue 3 components and composables for FluxDocs — a **privacy-first, client-side
PDF viewer** powered by WebAssembly. Pages are rendered in the browser; no
document is sent to a server.

## Install

```bash
pnpm add @fluxdocs/vue @fluxdocs/core vue
```

## Usage

Load the WASM runtime once (see `@fluxdocs/core`), then drop in the viewer:

```vue
<script setup lang="ts">
import { ref, onMounted } from "vue";
import { loadFluxDocs, type FluxDocs } from "@fluxdocs/core";
import { FluxDocsViewer } from "@fluxdocs/vue";
import "@fluxdocs/core/styles.css"; // once, anywhere in the app

const props = defineProps<{ pdf: Uint8Array }>();
const runtime = ref<FluxDocs | null>(null);

onMounted(async () => {
  const buf = await fetch("fluxdocs.wasm").then((r) => r.arrayBuffer());
  runtime.value = await loadFluxDocs(buf); // needs wasm_exec.js loaded first
});
</script>

<template>
  <FluxDocsViewer :runtime="runtime" :data="pdf" @page-change="(i) => console.log(i)" />
</template>
```

### Viewer

`FluxDocsViewer` renders one page at a time with page navigation and zoom
(25%–400%, 100% = true physical size; zoom out / zoom in / fit to width).

| Prop / event | Type | Default | |
|---|---|---|---|
| `runtime` | `FluxDocs \| null` | `null` | From `loadFluxDocs` |
| `data` | `Uint8Array \| null` | `null` | The PDF bytes |
| `initial-page` | `number` | `0` | 0-based, clamped |
| `initial-zoom` | `number` | `1` | Clamped to 0.25–4 |
| `render-options` | `RenderOptions` | — | `dpi` = sharpness at 100% (see below) |
| `class-name` | `string` | `""` | Extra class on the `.fd-viewer` root |
| `@page-change` | `(pageIndex: number)` | — | After every page change, not on mount |
| `@zoom-change` | `(zoom: number)` | — | After every zoom change, not on mount |

Page and zoom are uncontrolled (no `v-model`): set the starting values with
`initial-page` / `initial-zoom` and listen to the events.

Keyboard (only while focus is inside the viewer; never in text inputs):

| Key | Action |
|---|---|
| Arrow Down / Arrow Up | Next / previous page. Inside the document area, when the page is zoomed and scrolls, the arrows scroll first and change page only at the bottom / top edge. |
| Ctrl/Cmd + `+` / `-` | Zoom in / out (the browser's page zoom is suppressed while focus is in the viewer) |
| Ctrl/Cmd + `0` | Reset zoom to 100% |

**Behavior change (unreleased):** `renderOptions.dpi` now sets the sharpness at
100% zoom only; the displayed size comes from the zoom. Previously `dpi: 150`
also displayed the page at ~156% of its physical size. Omit `dpi` to let the
viewer pick `96 × devicePixelRatio` (capped at 2×); each zoom step re-renders at
a matching DPI, capped at an 8 Mi px bitmap.

`renderOptions` also passes the render limits through to the core
(`maxPaintPixels`, `maxOperators`, `timeoutMs`; see the `@fluxdocs/core`
README). The viewer sets no timeout of its own. On failure the viewer shows a
`role="alert"` message chosen by the error's `FluxDocsError.code`:

| `code` | Message |
|---|---|
| `budget_exceeded` | This page is too complex to display. |
| `canvas_too_large` | Failed to display PDF: page is too large to render |
| `timeout` | Rendering this page took too long. |
| anything else | Failed to display PDF: *message* |

### Composables

- `useFluxDoc(runtime, data)` → `{ doc, error }` — opens a PDF (inputs may be
  refs, getters, or plain values) and closes it when they change or the scope is
  disposed.
- `usePageImage(doc, pageIndex, opts?)` → computed `{ src, error }` — renders a
  page to a `data:` URL for an `<img>`.

### Styling

Import the base stylesheet once (it ships with `@fluxdocs/core`; the component
does not import it, so it renders unstyled without it):

```ts
import "@fluxdocs/core/styles.css";
```

The DOM uses `fd-` BEM classes (`.fd-viewer`, `.fd-viewer__toolbar`,
`.fd-viewer__btn`, `.fd-viewer__viewport`, `.fd-viewer__page`,
`.fd-viewer__canvas`, …) and `--fd-*` tokens for theming. Dark mode follows
`prefers-color-scheme`; force it with `data-fd-theme="dark"` (or `"light"`) on
`<html>`, any ancestor, or the component itself (non-prop attributes fall
through to the root). See `@fluxdocs/core`'s README.

## Develop

```bash
pnpm build   # tsc -> dist/
pnpm test    # vitest (jsdom) against a mock runtime — no WASM build needed
```

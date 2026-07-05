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

const props = defineProps<{ pdf: Uint8Array }>();
const runtime = ref<FluxDocs | null>(null);

onMounted(async () => {
  const buf = await fetch("fluxdocs.wasm").then((r) => r.arrayBuffer());
  runtime.value = await loadFluxDocs(buf); // needs wasm_exec.js loaded first
});
</script>

<template>
  <FluxDocsViewer :runtime="runtime" :data="pdf" :render-options="{ dpi: 150 }" />
</template>
```

### Composables

- `useFluxDoc(runtime, data)` → `{ doc, error }` — opens a PDF (inputs may be
  refs, getters, or plain values) and closes it when they change or the scope is
  disposed.
- `usePageImage(doc, pageIndex, opts?)` → computed `{ src, error }` — renders a
  page to a `data:` URL for an `<img>`.

### Styling

The viewer uses `fd-` BEM class hooks (`.fd-viewer`, `.fd-viewer__toolbar`,
`.fd-viewer__canvas`, `.fd-viewer__nav`, `.fd-viewer__page-info`). No styles are
bundled.

## Develop

```bash
pnpm build   # tsc -> dist/
pnpm test    # vitest (jsdom) against a mock runtime — no WASM build needed
```

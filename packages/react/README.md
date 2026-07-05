# @fluxdocs/react

React components and hooks for FluxDocs — a **privacy-first, client-side PDF
viewer** powered by WebAssembly. Pages are rendered in the browser; no document
is sent to a server.

## Install

```bash
pnpm add @fluxdocs/react @fluxdocs/core react react-dom
```

## Usage

Load the WASM runtime once (see `@fluxdocs/core`), then drop in the viewer:

```tsx
import { useEffect, useState } from "react";
import { loadFluxDocs, type FluxDocs } from "@fluxdocs/core";
import { FluxDocsViewer } from "@fluxdocs/react";

export function App({ pdf }: { pdf: Uint8Array }) {
  const [runtime, setRuntime] = useState<FluxDocs | null>(null);

  useEffect(() => {
    fetch("fluxdocs.wasm")
      .then((r) => r.arrayBuffer())
      .then((buf) => loadFluxDocs(buf)) // needs wasm_exec.js loaded first
      .then(setRuntime);
  }, []);

  return <FluxDocsViewer runtime={runtime} data={pdf} renderOptions={{ dpi: 150 }} />;
}
```

### Hooks

- `useFluxDoc(runtime, data)` → `{ doc, error }` — opens a PDF and closes it
  automatically when inputs change or the component unmounts.
- `usePageImage(doc, pageIndex, opts?)` → `{ src, error }` — renders a page to a
  `data:` URL for an `<img>`.

### Styling

The viewer uses `fd-` BEM class hooks (`.fd-viewer`, `.fd-viewer__toolbar`,
`.fd-viewer__canvas`, `.fd-viewer__nav`, `.fd-viewer__page-info`) so a host app
can theme it without collisions. No styles are bundled.

## Develop

```bash
pnpm build   # tsc -> dist/
pnpm test    # vitest (jsdom) against a mock runtime — no WASM build needed
```

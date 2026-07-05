# @fluxdocs/core

Typed client SDK for FluxDocs, running the pure-Go PDF core as WebAssembly.
**Privacy-first: documents are processed entirely in the browser and never sent
to a server.**

## Install

```bash
pnpm add @fluxdocs/core
```

You also need the compiled `fluxdocs.wasm` and the Go runtime shim `wasm_exec.js`
(both produced by the Go toolchain: `GOOS=js GOARCH=wasm go build -o fluxdocs.wasm ./wasm`,
and `$(go env GOROOT)/lib/wasm/wasm_exec.js`).

## Usage

```html
<script src="wasm_exec.js"></script>
<script type="module">
  import { loadFluxDocs } from "@fluxdocs/core";

  const wasm = await fetch("fluxdocs.wasm").then((r) => r.arrayBuffer());
  const fluxdocs = await loadFluxDocs(wasm); // uses globalThis.Go from wasm_exec.js

  const bytes = new Uint8Array(await file.arrayBuffer());
  const doc = fluxdocs.open(bytes);

  console.log(doc.pageCount, doc.extractText());

  const png = doc.renderPage(0, { dpi: 150 }); // Uint8Array
  doc.addAnnotation({
    pageIndex: 0,
    type: "highlight",
    rect: { x: 72, y: 700, width: 120, height: 16 },
    color: "#f59e0b",
    opacity: 0.4,
  });
  const withHighlight = doc.renderPage(0); // overlay drawn by default
  const saved = doc.save(); // Uint8Array (PDF, annotations kept as a layer)
  doc.close();
</script>
```

Every method throws `FluxDocsError` on failure (the core reports errors via a
result envelope, which the wrapper unwraps).

## API

- `loadFluxDocs(wasm, opts?) => Promise<FluxDocs>` — instantiate the core.
- `FluxDocs.open(bytes) => FluxDoc`, `FluxDocs.version()`.
- `FluxDoc`: `pageCount`, `pageSize(i)`, `extractText()`, `extractTextByPage(i)`,
  `renderPage(i, opts?)`, `addAnnotation(a)`, `getAnnotations(i)`,
  `exportAnnotations()`, `save()`, `close()`.

## Develop

```bash
pnpm build   # tsc -> dist/
pnpm test    # build, then drive the real WASM core under Node (needs a Go toolchain)
```

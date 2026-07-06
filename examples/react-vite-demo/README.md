# react-vite-demo

A client-side PDF viewer built with Vite + React and `@fluxdocs/react`. The PDF
is rendered entirely in the browser via WebAssembly — nothing is uploaded.

```bash
pnpm install
pnpm --filter react-vite-demo build   # builds the WASM core + the Vite bundle
pnpm --filter react-vite-demo dev     # dev server
```

`build:wasm` compiles `./wasm` for `js/wasm` into `public/fluxdocs.wasm` and
copies the Go runtime's `wasm_exec.js`; `App.tsx` loads it with
`loadFluxDocs` and shows the picked file in a `<FluxDocsViewer>`.

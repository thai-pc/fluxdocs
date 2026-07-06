# vue-demo

A client-side PDF viewer built with Vite + Vue 3 and `@fluxdocs/vue`. The PDF is
rendered entirely in the browser via WebAssembly — nothing is uploaded.

```bash
pnpm install
pnpm --filter vue-demo build   # builds the WASM core + the Vite bundle
pnpm --filter vue-demo dev     # dev server
```

Note: hold the runtime in a **`shallowRef`** (not `ref`) — Vue's deep reactivity
would otherwise strip the private state off the `FluxDocs` class instance.

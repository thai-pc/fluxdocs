# FluxDocs WASM entry point

Compiles the pure-Go core to WebAssembly and exposes it to JavaScript as
`globalThis.fluxdocs`. Everything runs client-side — **no document byte is sent
to a server** (the privacy-first promise, CLAUDE.md §1.3). This package uses no
network or filesystem access.

## Build

```bash
GOOS=js GOARCH=wasm go build -o fluxdocs.wasm ./wasm
```

Load it in a page with the Go runtime shim `wasm_exec.js` (ships with the Go
toolchain under `$(go env GOROOT)/lib/wasm/wasm_exec.js`):

```html
<script src="wasm_exec.js"></script>
<script>
  const go = new Go();
  const { instance } = await WebAssembly.instantiateStreaming(
    fetch("fluxdocs.wasm"), go.importObject);
  go.run(instance); // registers globalThis.fluxdocs
</script>
```

## API

Every function returns a plain object with an `error` string field (`""` on
success) plus result fields — no exceptions to catch.

| Function | Returns |
|---|---|
| `version()` | `{error, version}` |
| `open(bytes: Uint8Array)` | `{error, handle, pageCount}` |
| `close(handle)` | `{error}` |
| `pageCount(handle)` | `{error, count}` |
| `pageSize(handle, index)` | `{error, width, height, rotation}` |
| `extractText(handle)` | `{error, text}` |
| `extractTextByPage(handle, index)` | `{error, text}` |
| `renderPage(handle, index, opts?)` | `{error, image: Uint8Array, format}` |
| `save(handle)` | `{error, bytes: Uint8Array}` |
| `addAnnotation(handle, ann)` | `{error, annotation}` |
| `getAnnotations(handle, pageIndex)` | `{error, annotations: []}` |
| `exportAnnotations(handle)` | `{error, json}` |

`renderPage` opts: `{ dpi?, format?: "png"|"jpeg", quality?, skipAnnotations?: bool }`.
`addAnnotation` ann: `{ pageIndex, type, rect:{x,y,width,height}, color?, opacity?, content?, points?:[{x,y}] }`.

Documents are referenced by an integer `handle`; call `close(handle)` to release
one.

## Smoke test

`smoke_test.mjs` builds the module and drives the real compiled core in a WASM
VM under Node (not just a compile check):

```bash
node wasm/smoke_test.mjs   # requires Node + a Go toolchain
```

It is intentionally a standalone script (not a `go test`), so CI's `go build
./core/... ./wasm/...` under `GOOS=js GOARCH=wasm` remains the compile gate.

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

Every function returns a plain object with an `error` string field and a `code`
string field (both `""` on success) plus result fields — no exceptions to catch.
On failure `error` is a human-readable `"<function>: <message>"` and `code` is
one of a closed set, for programmatic handling:

| `code` | Meaning |
|---|---|
| `canvas_too_large` | the page's canvas exceeds the pixel cap at this DPI |
| `budget_exceeded` | the page exceeded its render budget (`maxPaintPixels` / `maxOperators`, or an internal cap); retrying does not help |
| `timeout` | the render passed `timeoutMs` |
| `canceled` | the render was canceled |
| `invalid_pdf` | not a valid PDF |
| `encrypted` | the document is encrypted |
| `page_not_found` | page index out of range |
| `unsupported_format` | unsupported `format` |
| `invalid_annotation` | annotation failed validation, or not found |
| `invalid_argument` | a malformed argument (missing page index, non-object options, a page index, `dpi` or `quality` that is NaN, infinite or out of the int range, …) |
| `invalid_handle` | missing, unknown, or closed document handle |
| `internal` | a recovered Go panic (a FluxDocs bug) |
| `unknown` | any other error |

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

`renderPage` opts: `{ dpi?, format?: "png"|"jpeg", quality?, skipAnnotations?: bool,
maxPaintPixels?, maxOperators?, timeoutMs? }`. Every page render runs under a
per-page budget (SECURITY.md §2): `maxPaintPixels` (raster work in pixel visits;
default 32 × the canvas pixels, clamped to [16 Mi, 1 Gi]) and `maxOperators`
(content operators plus glyphs; default 8 Mi). `0` or absent = default, negative
= unlimited (unsafe for untrusted input), positive = absolute; NaN, infinite or
out-of-range values are ignored. `timeoutMs` (finite, > 0) bounds the render's
wall clock; the renderer checks the clock itself, because a synchronous call
never yields to Go's timer.
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
./core/... ./wasm/...` under `GOOS=js GOARCH=wasm` remains the compile gate. It
builds with `-tags smoketest`, which adds one test-only export, `_testPanic`
(`testexport_smoketest.go`), to exercise the panic-recovery envelope; a normal
build does not contain it.

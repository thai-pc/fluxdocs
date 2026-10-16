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
result envelope, which the wrapper unwraps). Branch on `err.code`, not on the
message text:

```ts
import { FluxDocsError } from "@fluxdocs/core";

try {
  doc.renderPage(0, { dpi: 150, timeoutMs: 2000 });
} catch (err) {
  if (err instanceof FluxDocsError && err.code === "budget_exceeded") {
    // the page is too costly to render with these limits; retrying won't help
  }
}
```

`code` is a `FluxDocsErrorCode`: `canvas_too_large`, `budget_exceeded`,
`timeout`, `canceled`, `invalid_pdf`, `encrypted`, `page_not_found`,
`unsupported_format`, `invalid_annotation`, `invalid_argument`,
`invalid_handle` (also: any call on a closed `FluxDoc`), `internal` (a FluxDocs
bug, or the runtime failed to load), or `unknown` (anything else, including a
code from a mismatched `.wasm` build). New codes may be added.

### Render limits

Every `renderPage` call runs under a resource budget, so a hostile or
pathological PDF fails fast with a `FluxDocsError` instead of freezing the tab.
The defaults suit untrusted input; tune them per call:

| Option | Unit | Default | |
|---|---|---|---|
| `maxPaintPixels` | pixel visits (overdraw counts each time) | 32 × the page's canvas pixels, clamped to [16 Mi, 1 Gi] | Over it: `budget_exceeded`. A positive value is absolute, not scaled by DPI. |
| `maxOperators` | interpreter steps (content operators at every Form XObject depth, plus glyphs) | 8 Mi | Over it: `budget_exceeded`. |
| `timeoutMs` | milliseconds of wall clock | none | Applied only when finite and > 0. Past it: `timeout`. |

For `maxPaintPixels` and `maxOperators`, absent or `0` means the default and
`< 0` means unlimited, which is unsafe for untrusted input. NaN, ±Infinity, and
out-of-range values are ignored. A page too large for the canvas cap at the
requested DPI fails with `canvas_too_large`. Rendering is synchronous, so a
render keeps the main thread busy until it finishes or hits a limit.

## API

- `loadFluxDocs(wasm, opts?) => Promise<FluxDocs>` — instantiate the core.
- `FluxDocs.open(bytes) => FluxDoc`, `FluxDocs.version()`.
- `FluxDoc`: `pageCount`, `pageSize(i)`, `extractText()`, `extractTextByPage(i)`,
  `renderPage(i, opts?)`, `addAnnotation(a)`, `getAnnotations(i)`,
  `exportAnnotations()`, `save()`, `close()`.

## Styles

`@fluxdocs/core/styles.css` is the base stylesheet for the viewer DOM shared by
`@fluxdocs/react` and `@fluxdocs/vue` (`.fd-viewer`, `.fd-viewer__toolbar`, …).
Import it once in the host app; the components never import it themselves, so
they stay usable (unstyled) under SSR and in test runners:

```ts
import "@fluxdocs/core/styles.css";
```

- Plain CSS, no build step. It never triggers a network request: no `@import`,
  no `@font-face`, no `url()`. Inter / JetBrains Mono are used only if the host
  already loads them; otherwise `system-ui`.
- Theming is done with the `--fd-*` tokens (spec §8.2): override the base name
  for light (`--fd-bg`) and the `-dark` sibling for dark (`--fd-bg-dark`) at
  `:root` or any ancestor of the viewer.
- Light by default, dark under `prefers-color-scheme: dark`. Force a theme with
  `data-fd-theme="light"` or `data-fd-theme="dark"` on `<html>` or any ancestor.
  The rendered PDF page itself is never inverted.
- Selectors are single `fd-` classes (no `@layer`, no `!important`), so common
  host resets such as `button { all: unset }` or `img { max-width: 100% }` do
  not break the viewer.
- The viewer fills its container's width. In a shrink-to-fit container (a flex
  item without a width, `inline-block`, a float, an absolutely positioned box)
  it falls back to 320 px wide; give such containers an explicit width.

## Develop

```bash
pnpm build   # tsc -> dist/
pnpm test    # build, lint styles.css, then drive the real WASM core under Node (needs a Go toolchain)
```

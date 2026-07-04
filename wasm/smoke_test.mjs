// Smoke test for the FluxDocs WASM entry point. It builds wasm/main.go for
// js/wasm, instantiates it under Node with the Go runtime's wasm_exec.js, and
// exercises globalThis.fluxdocs end to end (open, pageCount, extractText,
// renderPage, annotations, save). This runs the REAL compiled core in a WASM VM,
// so it verifies the syscall/js glue, not just that it compiles.
//
// Usage: node wasm/smoke_test.mjs   (requires Node and a Go toolchain)
// Exit code 0 = all assertions passed.

import { readFileSync, mkdtempSync } from "node:fs";
import { execSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..");

let passed = 0;
function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL:", msg);
    process.exit(1);
  }
  passed++;
}

// Locate the Go toolchain's wasm_exec.js and build the module.
const goroot = execSync("go env GOROOT").toString().trim();
let wasmExecPath = join(goroot, "lib", "wasm", "wasm_exec.js");
try {
  readFileSync(wasmExecPath);
} catch {
  wasmExecPath = join(goroot, "misc", "wasm", "wasm_exec.js"); // older Go layout
}

const work = mkdtempSync(join(tmpdir(), "fluxdocs-wasm-"));
const wasmPath = join(work, "fluxdocs.wasm");
execSync(`go build -o ${wasmPath} ./wasm`, {
  cwd: repo,
  env: { ...process.env, GOOS: "js", GOARCH: "wasm" },
  stdio: "inherit",
});

// Load the Go WASM runtime shim (defines globalThis.Go).
(0, eval)(readFileSync(wasmExecPath, "utf8"));

const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(readFileSync(wasmPath), go.importObject);
go.run(instance); // registers globalThis.fluxdocs, then blocks in select{}

const fluxdocs = globalThis.fluxdocs;
assert(fluxdocs, "globalThis.fluxdocs registered");
assert(fluxdocs.version().version === "0.1.0", "version");

// Open the sample PDF.
const pdf = new Uint8Array(readFileSync(join(here, "testdata", "sample.pdf")));
const opened = fluxdocs.open(pdf);
assert(opened.error === "", `open: ${opened.error}`);
assert(opened.pageCount === 1, `open pageCount = ${opened.pageCount}`);
const h = opened.handle;

// Page count + size.
assert(fluxdocs.pageCount(h).count === 1, "pageCount");
const size = fluxdocs.pageSize(h, 0);
assert(size.width === 200 && size.height === 200, `pageSize ${size.width}x${size.height}`);

// Text extraction.
const text = fluxdocs.extractText(h);
assert(text.error === "" && text.text.includes("Hello WASM"), `extractText: ${JSON.stringify(text)}`);
assert(fluxdocs.extractTextByPage(h, 0).text.includes("Hello WASM"), "extractTextByPage");

// Render to PNG.
const png = fluxdocs.renderPage(h, 0, { dpi: 72, format: "png" });
assert(png.error === "" && png.image.length > 100, `renderPage: ${png.error}`);
assert(png.image[0] === 0x89 && png.image[1] === 0x50, "renderPage returns PNG magic");

// Annotations.
const add = fluxdocs.addAnnotation(h, {
  pageIndex: 0, type: "highlight",
  rect: { x: 10, y: 10, width: 50, height: 50 }, color: "#f59e0b", opacity: 0.4,
});
assert(add.error === "" && add.annotation.id === "ann-1", `addAnnotation: ${JSON.stringify(add)}`);
const got = fluxdocs.getAnnotations(h, 0);
assert(got.error === "" && got.annotations.length === 1, "getAnnotations");
assert(got.annotations[0].type === "highlight", "annotation type round-trips");

const exp = fluxdocs.exportAnnotations(h);
assert(exp.error === "", `exportAnnotations: ${exp.error}`);
assert(JSON.parse(exp.json).annotations.length === 1, "exportAnnotations JSON");

// Rendering with the annotation now draws more ink than the bare page.
const withAnn = fluxdocs.renderPage(h, 0, { dpi: 72, format: "png" });
assert(withAnn.error === "" && withAnn.image.length > 0, "renderPage with annotation");

// Save re-serializes a valid PDF.
const saved = fluxdocs.save(h);
assert(saved.error === "" && saved.bytes.length > 0, `save: ${saved.error}`);
assert(saved.bytes[0] === 0x25 && saved.bytes[1] === 0x50, "save returns %PDF header");

// Error envelope on a bad handle.
assert(fluxdocs.pageCount(9999).error !== "", "unknown handle -> error");

fluxdocs.close(h);
assert(fluxdocs.pageCount(h).error !== "", "closed handle -> error");

console.log(`OK — ${passed} assertions passed`);

// End-to-end test for @fluxdocs/core: it builds the WASM core, loads it with the
// Go runtime shim under Node, and drives the built TypeScript wrapper
// (dist/index.js) against the real core. Run: `node --test` (after `tsc`), or
// `npm test` from packages/core.
//
// Requires a Go toolchain (to build the wasm) and Node. It exercises the same
// surface as wasm/smoke_test.mjs but through the typed wrapper, verifying the
// envelope-unwrapping and error handling.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, mkdtempSync } from "node:fs";
import { execSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { loadFluxDocs, FluxDocsError } from "../dist/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..", "..");

// Build the WASM module and load the Go runtime shim once for all tests.
function buildAndLoad() {
  const goroot = execSync("go env GOROOT").toString().trim();
  let wasmExec = join(goroot, "lib", "wasm", "wasm_exec.js");
  try {
    readFileSync(wasmExec);
  } catch {
    wasmExec = join(goroot, "misc", "wasm", "wasm_exec.js");
  }
  const work = mkdtempSync(join(tmpdir(), "fluxdocs-core-"));
  const wasmPath = join(work, "fluxdocs.wasm");
  execSync(`go build -o ${wasmPath} ./wasm`, {
    cwd: repo,
    env: { ...process.env, GOOS: "js", GOARCH: "wasm" },
  });
  (0, eval)(readFileSync(wasmExec, "utf8")); // defines globalThis.Go
  return readFileSync(wasmPath);
}

const wasmBytes = buildAndLoad();
const runtime = await loadFluxDocs(wasmBytes);
const sample = new Uint8Array(readFileSync(join(repo, "wasm", "testdata", "sample.pdf")));

test("version", () => {
  assert.equal(runtime.version(), "0.1.0");
});

test("open + metadata", () => {
  const doc = runtime.open(sample);
  assert.equal(doc.pageCount, 1);
  const size = doc.pageSize(0);
  assert.equal(size.width, 200);
  assert.equal(size.height, 200);
  doc.close();
});

test("extractText", () => {
  const doc = runtime.open(sample);
  assert.ok(doc.extractText().includes("Hello WASM"));
  assert.ok(doc.extractTextByPage(0).includes("Hello WASM"));
  doc.close();
});

test("renderPage returns a PNG", () => {
  const doc = runtime.open(sample);
  const png = doc.renderPage(0, { dpi: 72, format: "png" });
  assert.ok(png.length > 100);
  assert.equal(png[0], 0x89);
  assert.equal(png[1], 0x50); // 'P'
  doc.close();
});

test("annotations round-trip", () => {
  const doc = runtime.open(sample);
  const ann = doc.addAnnotation({
    pageIndex: 0,
    type: "highlight",
    rect: { x: 10, y: 10, width: 50, height: 50 },
    color: "#f59e0b",
    opacity: 0.4,
  });
  assert.equal(ann.id, "ann-1");
  assert.equal(ann.type, "highlight");

  const list = doc.getAnnotations(0);
  assert.equal(list.length, 1);
  assert.equal(list[0].id, "ann-1");

  const layer = JSON.parse(doc.exportAnnotations());
  assert.equal(layer.annotations.length, 1);
  doc.close();
});

test("save returns a PDF", () => {
  const doc = runtime.open(sample);
  const bytes = doc.save();
  assert.ok(bytes.length > 0);
  assert.equal(bytes[0], 0x25); // '%'
  assert.equal(bytes[1], 0x50); // 'P'
  doc.close();
});

test("open failure throws FluxDocsError", () => {
  assert.throws(() => runtime.open(new Uint8Array([1, 2, 3])), FluxDocsError);
});

test("use after close throws", () => {
  const doc = runtime.open(sample);
  doc.close();
  assert.throws(() => doc.extractText(), FluxDocsError);
  doc.close(); // idempotent, no throw
});

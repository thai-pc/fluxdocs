// Unit tests for FluxDocsError.code propagation, driven by a mocked
// globalThis.fluxdocs envelope API (no Go toolchain or .wasm needed). The
// real-core counterpart lives in wrapper.test.mjs. Run: `node --test` after
// `tsc` (packages/core `npm test`).

import { test } from "node:test";
import assert from "node:assert/strict";

import { FluxDoc, FluxDocs, FluxDocsError, loadFluxDocs } from "../dist/index.js";

const ALL_CODES = [
  "canvas_too_large",
  "budget_exceeded",
  "timeout",
  "canceled",
  "invalid_pdf",
  "encrypted",
  "page_not_found",
  "unsupported_format",
  "invalid_annotation",
  "invalid_argument",
  "invalid_handle",
  "internal",
  "unknown",
];

const OK = { error: "", code: "" };

// mockAPI returns a raw API whose every export answers with `envelope` merged
// over a minimal successful result. open() always succeeds unless openEnvelope
// is given, so per-document calls can be exercised.
function mockAPI(envelope, openEnvelope = OK) {
  return {
    version: () => ({ ...OK, version: "mock", ...envelope }),
    open: () => ({ ...OK, handle: 1, pageCount: 1, ...openEnvelope }),
    close: () => ({ ...OK }),
    pageCount: () => ({ ...OK, count: 1, ...envelope }),
    pageSize: () => ({ ...OK, width: 1, height: 1, rotation: 0, ...envelope }),
    extractText: () => ({ ...OK, text: "", ...envelope }),
    extractTextByPage: () => ({ ...OK, text: "", ...envelope }),
    renderPage: () => ({ ...OK, image: new Uint8Array([0x89]), format: "png", ...envelope }),
    save: () => ({ ...OK, bytes: new Uint8Array([0x25]), ...envelope }),
    addAnnotation: () => ({ ...OK, annotation: {}, ...envelope }),
    getAnnotations: () => ({ ...OK, annotations: [], ...envelope }),
    exportAnnotations: () => ({ ...OK, json: "{}", ...envelope }),
  };
}

// caught runs fn and returns the FluxDocsError it throws.
function caught(fn) {
  try {
    fn();
  } catch (err) {
    assert.ok(err instanceof FluxDocsError, `not a FluxDocsError: ${err}`);
    return err;
  }
  assert.fail("expected a FluxDocsError");
}

test("FluxDocsError defaults code to unknown", () => {
  const err = new FluxDocsError("boom");
  assert.equal(err.code, "unknown");
  assert.equal(err.message, "boom");
  assert.equal(err.name, "FluxDocsError");
  assert.ok(err instanceof Error);
  assert.equal(new FluxDocsError("x", "timeout").code, "timeout");
});

test("every known envelope code reaches FluxDocsError.code", () => {
  for (const code of ALL_CODES) {
    const api = mockAPI({ error: `renderPage: failed with ${code}`, code });
    const doc = new FluxDocs(api).open(new Uint8Array([1]));
    const err = caught(() => doc.renderPage(0));
    assert.equal(err.code, code);
    assert.equal(err.message, `renderPage: failed with ${code}`);
    doc.close();
  }
});

test("open failure carries the envelope code", () => {
  const rt = new FluxDocs(mockAPI({}, { error: "open: not a PDF", code: "invalid_pdf" }));
  const err = caught(() => rt.open(new Uint8Array([1])));
  assert.equal(err.code, "invalid_pdf");
  assert.equal(err.message, "open: not a PDF");
});

test("every document method propagates the code", () => {
  const env = { error: "x: budget", code: "budget_exceeded" };
  const doc = new FluxDoc(mockAPI(env), 1, 1);
  const calls = [
    () => doc.pageSize(0),
    () => doc.extractText(),
    () => doc.extractTextByPage(0),
    () => doc.renderPage(0),
    () => doc.addAnnotation({ pageIndex: 0, type: "note", rect: { x: 0, y: 0, width: 1, height: 1 } }),
    () => doc.getAnnotations(0),
    () => doc.exportAnnotations(),
    () => doc.save(),
  ];
  for (const call of calls) {
    assert.equal(caught(call).code, "budget_exceeded");
  }
  assert.equal(caught(() => new FluxDocs(mockAPI(env)).version()).code, "budget_exceeded");
});

test("missing, empty, unrecognized, or non-string code maps to unknown", () => {
  for (const env of [
    { error: "old wasm: no code field" }, // .wasm built before the code field
    { error: "x", code: "" },
    { error: "x", code: "brand_new_code" },
    { error: "x", code: 42 },
    { error: "x", code: null },
  ]) {
    const err = caught(() => new FluxDoc(mockAPI(env), 1, 1).renderPage(0));
    assert.equal(err.code, "unknown", JSON.stringify(env));
    assert.equal(err.message, env.error);
  }
});

test("a code with an empty error still throws (fail closed)", () => {
  const err = caught(() => new FluxDoc(mockAPI({ error: "", code: "timeout" }), 1, 1).renderPage(0));
  assert.equal(err.code, "timeout");
  assert.ok(err.message.includes("timeout"));
});

test("a successful envelope without a code field returns the result", () => {
  const api = mockAPI({});
  api.renderPage = () => ({ error: "", image: new Uint8Array([7]), format: "png" });
  assert.deepEqual([...new FluxDoc(api, 1, 1).renderPage(0)], [7]);
});

test("the render options are passed to the core unchanged", () => {
  const seen = [];
  const api = mockAPI({});
  api.renderPage = (_h, _i, opts) => {
    seen.push(opts);
    return { ...OK, image: new Uint8Array([1]), format: "png" };
  };
  const opts = { dpi: 72, maxPaintPixels: 1000, maxOperators: -1, timeoutMs: 250 };
  new FluxDoc(api, 1, 1).renderPage(0, opts);
  assert.deepEqual(seen, [opts]);
});

test("a closed document throws invalid_handle without calling the core", () => {
  let calls = 0;
  const api = mockAPI({});
  api.extractText = () => {
    calls++;
    return { ...OK, text: "" };
  };
  const doc = new FluxDoc(api, 1, 1);
  doc.close();
  const err = caught(() => doc.extractText());
  assert.equal(err.code, "invalid_handle");
  assert.equal(err.message, "document is closed");
  assert.equal(calls, 0);
});

test("loadFluxDocs without a Go runtime throws internal", async () => {
  assert.equal(globalThis.Go, undefined);
  await assert.rejects(loadFluxDocs(new Uint8Array(0)), (err) => {
    assert.ok(err instanceof FluxDocsError);
    assert.equal(err.code, "internal");
    assert.match(err.message, /Go runtime not found/);
    return true;
  });
});

test("loadFluxDocs throws internal when the WASM does not register its API", async () => {
  // The smallest valid module: magic + version, no imports or exports.
  const empty = new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]);
  class FakeGo {
    importObject = {};
    run() {
      return new Promise(() => {});
    }
  }
  assert.equal(globalThis.fluxdocs, undefined);
  await assert.rejects(loadFluxDocs(empty, { Go: FakeGo }), (err) => {
    assert.ok(err instanceof FluxDocsError);
    assert.equal(err.code, "internal");
    assert.match(err.message, /did not register/);
    return true;
  });
});

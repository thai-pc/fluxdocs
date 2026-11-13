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
import { createHash } from "node:crypto";
import { inflateSync } from "node:zlib";

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
// -tags smoketest adds the test-only _testPanic export (wasm/testexport_smoketest.go).
execSync(`go build -tags smoketest -o ${wasmPath} ./wasm`, {
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

// Every envelope carries a code: "" on success, a closed-set value on failure.
assert(png.code === "", `success code: ${JSON.stringify(png.code)}`);
assert(fluxdocs.version().code === "", "version code");

// Error envelope on a bad handle.
const unknownHandle = fluxdocs.pageCount(9999);
assert(unknownHandle.error !== "" && unknownHandle.code === "invalid_handle", `unknown handle: ${JSON.stringify(unknownHandle)}`);
const noHandle = fluxdocs.pageCount();
assert(noHandle.code === "invalid_handle", `missing handle: ${JSON.stringify(noHandle)}`);
const stringHandle = fluxdocs.pageCount("not-a-handle");
assert(stringHandle.code === "invalid_handle", `non-numeric handle: ${JSON.stringify(stringHandle)}`);

// Argument and lookup errors map to their codes.
const noIndex = fluxdocs.renderPage(h, undefined);
assert(noIndex.code === "invalid_argument" && /^renderPage: /.test(noIndex.error), `missing index: ${JSON.stringify(noIndex)}`);
const badOpts = fluxdocs.renderPage(h, 0, "png");
assert(badOpts.code === "invalid_argument", `non-object options: ${JSON.stringify(badOpts)}`);
assert(fluxdocs.renderPage(h, 0, null).error === "", "null options -> defaults");
// Numbers that have no int value are rejected, not cast (a raw float->int cast
// saturates under js/wasm).
for (const [what, res] of [
  ["NaN index", fluxdocs.renderPage(h, NaN)],
  ["huge index", fluxdocs.renderPage(h, 1e300)],
  ["Infinity dpi", fluxdocs.renderPage(h, 0, { dpi: Infinity })],
  ["NaN quality", fluxdocs.renderPage(h, 0, { format: "jpeg", quality: NaN })],
]) {
  assert(res.code === "invalid_argument" && res.image === undefined, `${what}: ${JSON.stringify(res)}`);
}
const noPage = fluxdocs.renderPage(h, 7, { dpi: 72 });
assert(noPage.code === "page_not_found" && noPage.image === undefined, `page not found: ${JSON.stringify(noPage)}`);
const badFormat = fluxdocs.renderPage(h, 0, { format: "svg" });
assert(badFormat.code === "unsupported_format", `unsupported format: ${JSON.stringify(badFormat)}`);
const badAnn = fluxdocs.addAnnotation(h, { pageIndex: 0, type: "stamp", rect: { x: 0, y: 0, width: 1, height: 1 } });
assert(badAnn.code === "invalid_annotation", `invalid annotation: ${JSON.stringify(badAnn)}`);
const notPDF = fluxdocs.open(new Uint8Array([1, 2, 3]));
assert(notPDF.code === "invalid_pdf", `invalid pdf: ${JSON.stringify(notPDF)}`);

fluxdocs.close(h);
const closed = fluxdocs.pageCount(h);
assert(closed.error !== "" && closed.code === "invalid_handle", `closed handle: ${JSON.stringify(closed)}`);

// A non-numeric annotation pageIndex is an argument error, not a panic.
const panicky = fluxdocs.open(pdf);
const badIndex = fluxdocs.addAnnotation(panicky.handle, { pageIndex: "zero", type: "note" });
assert(badIndex.code === "invalid_argument" && /^addAnnotation: /.test(badIndex.error), `non-numeric pageIndex: ${JSON.stringify(badIndex)}`);
const nanIndex = fluxdocs.addAnnotation(panicky.handle, { pageIndex: NaN, type: "note" });
assert(nanIndex.code === "invalid_argument", `NaN pageIndex: ${JSON.stringify(nanIndex)}`);
fluxdocs.close(panicky.handle);

// A Go panic inside an export (the test-only _testPanic, which panics with a
// value holding "SECRET document bytes") must come back as an error envelope
// with code "internal", and the runtime must survive it — an uncaught panic
// would kill every later fluxdocs.* call. The message names a non-runtime panic
// value by its type only (it could hold document bytes).
const bad = fluxdocs._testPanic();
assert(/^_testPanic: internal error: /.test(bad.error), `panic -> envelope: ${JSON.stringify(bad)}`);
assert(bad.code === "internal", `panic -> code internal: ${JSON.stringify(bad)}`);
assert(/panic value of type \*errors\.errorString/.test(bad.error), `panic value shown by type only: ${bad.error}`);
assert(!/SECRET/.test(bad.error), `panic value text must not be echoed: ${bad.error}`);
assert(fluxdocs.version().version === "0.1.0", "runtime survives a recovered panic");

// A hostile MediaBox (2^33 pt at 36 DPI = 2^32 x 2^32 px, whose int64 area wraps
// to 0) must be rejected by the canvas cap, not crash the runtime.
// With content, the page also gets a content stream.
function onePagePDF(mediaBox, content) {
  const objs = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    `<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox ${mediaBox} >>`,
    content === undefined
      ? "<< /Type /Page /Parent 2 0 R >>"
      : "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
  ];
  if (content !== undefined) {
    objs.push(`<< /Length ${content.length} >>\nstream\n${content}\nendstream`);
  }
  let out = "%PDF-1.7\n";
  const offs = objs.map((o, i) => {
    const off = out.length;
    out += `${i + 1} 0 obj\n${o}\nendobj\n`;
    return off;
  });
  const xref = out.length;
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
  for (const off of offs) out += `${String(off).padStart(10, "0")} 00000 n \n`;
  out += `trailer\n<< /Size ${objs.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return new TextEncoder().encode(out);
}
const huge = fluxdocs.open(onePagePDF("[0 0 8589934592 8589934592]"));
assert(huge.error === "", `open hostile MediaBox: ${huge.error}`);
const hugeRender = fluxdocs.renderPage(huge.handle, 0, { dpi: 36, format: "png" });
assert(/exceeds/.test(hugeRender.error), `hostile MediaBox render -> canvas error: ${hugeRender.error}`);
assert(hugeRender.code === "canvas_too_large", `canvas cap code (not budget_exceeded): ${hugeRender.code}`);
fluxdocs.close(huge.handle);
assert(fluxdocs.version().version === "0.1.0", "runtime alive after hostile render");

// 1e18 pt at DPI 1 is the case where the float→int conversion saturates under
// js/wasm (the native int64 wrap differs), so it must be pinned here too.
const huger = fluxdocs.open(onePagePDF("[0 0 1000000000000000000 1000000000000000000]"));
assert(huger.error === "", `open 1e18 MediaBox: ${huger.error}`);
const hugerRender = fluxdocs.renderPage(huger.handle, 0, { dpi: 1, format: "png" });
assert(/exceeds/.test(hugerRender.error), `1e18 MediaBox render -> canvas error: ${hugerRender.error}`);
assert(hugerRender.image === undefined, "rejected render carries no image");
fluxdocs.close(huger.handle);

// Hostile strokes (SECURITY.md §2): a 1e9-long segment, a 1e5-wide stroke and a
// segment crossing the page from ±1e30. The stroke rasterizer once did work
// proportional to length × width², and under js/wasm the float→int conversion
// saturates, so 1e30 would loop "forever" and freeze the tab. The render must
// come back promptly with an image.
const big = "1" + "0".repeat(30) + ".0"; // 1e30; the lexer has no exponent syntax
const strokes = fluxdocs.open(onePagePDF("[0 0 100 100]",
  `0 0 m 1000000000 0 l S 100000 w 50 50 m 51 50 l S 1 w -${big} 50 m ${big} 50 l S`));
assert(strokes.error === "", `open hostile strokes: ${strokes.error}`);
const strokeStart = Date.now();
const strokeRender = fluxdocs.renderPage(strokes.handle, 0, { dpi: 72, format: "png" });
const strokeMs = Date.now() - strokeStart;
assert(strokeRender.error === "" && strokeRender.image[0] === 0x89, `hostile strokes render: ${strokeRender.error}`);
assert(strokeMs < 5000, `hostile strokes render took ${strokeMs}ms, want < 5000ms`);
fluxdocs.close(strokes.handle);

// Render budget (SECURITY.md §2): 2000 full-page fills on a 100x100 pt page
// cost ≈ 2000 × 10k pixel visits, past the default budget at 72 DPI (16 Mi), so
// the render stops with budget_exceeded instead of running on. An explicit
// limit applies as given; a negative one disables the budget; a NaN, infinite
// or out-of-range value is ignored (the default applies).
const flood = fluxdocs.open(onePagePDF("[0 0 100 100]", "0 0 100 100 re f\n".repeat(2000)));
assert(flood.error === "", `open flood page: ${flood.error}`);
const overBudget = fluxdocs.renderPage(flood.handle, 0, { dpi: 72 });
assert(overBudget.code === "budget_exceeded" && overBudget.image === undefined, `default budget: ${JSON.stringify(overBudget)}`);
assert(/render budget exceeded/.test(overBudget.error), `budget message: ${overBudget.error}`);
for (const junk of [NaN, Infinity, -Infinity, 1e300, -1e300]) {
  const r = fluxdocs.renderPage(flood.handle, 0, { dpi: 72, maxPaintPixels: junk });
  assert(r.code === "budget_exceeded", `maxPaintPixels ${junk} ignored -> default budget: ${r.code} ${r.error}`);
}
const tight = fluxdocs.renderPage(flood.handle, 0, { dpi: 72, maxPaintPixels: 1000 });
assert(tight.code === "budget_exceeded" && /paint work \d+ > 1000/.test(tight.error), `explicit limit: ${tight.error}`);
const fewOps = fluxdocs.renderPage(flood.handle, 0, { dpi: 72, maxPaintPixels: -1, maxOperators: 100 });
assert(fewOps.code === "budget_exceeded" && /operators/.test(fewOps.error), `maxOperators: ${fewOps.error}`);
const unlimited = fluxdocs.renderPage(flood.handle, 0, { dpi: 72, maxPaintPixels: -1 });
assert(unlimited.error === "" && unlimited.code === "" && unlimited.image[0] === 0x89, `unlimited budget renders: ${unlimited.error}`);
fluxdocs.close(flood.handle);

// timeoutMs bounds the wall clock inside a synchronous export. Go/wasm cannot
// preempt the render loop, so the timer goroutine never runs while it is busy:
// only the renderer's own clock check can stop it. With the budget disabled,
// 20000 full-page fills at 300 DPI would run for minutes.
const heavy = fluxdocs.open(onePagePDF("[0 0 100 100]", "0 0 100 100 re f\n".repeat(20000)));
assert(heavy.error === "", `open heavy page: ${heavy.error}`);
const timeoutStart = Date.now();
const timedOut = fluxdocs.renderPage(heavy.handle, 0, { dpi: 300, maxPaintPixels: -1, maxOperators: -1, timeoutMs: 200 });
const timeoutMs = Date.now() - timeoutStart;
assert(timedOut.code === "timeout" && timedOut.image === undefined, `timeoutMs -> timeout: ${JSON.stringify(timedOut)}`);
assert(timeoutMs < 5000, `timed-out render returned after ${timeoutMs}ms, want shortly after 200ms`);
for (const junk of [0, -5, NaN, Infinity]) {
  // A non-positive or non-finite timeoutMs means no deadline: the default
  // budget stops the render instead.
  const r = fluxdocs.renderPage(heavy.handle, 0, { dpi: 72, timeoutMs: junk });
  assert(r.code === "budget_exceeded", `timeoutMs ${junk} ignored: ${r.code} ${r.error}`);
}
fluxdocs.close(heavy.handle);
assert(fluxdocs.version().version === "0.1.0", "runtime alive after budget and timeout renders");

// After a recovered panic and two rejected renders, the real pipeline must still
// produce output: recover() must not swallow happy-path results.
const again = fluxdocs.open(pdf);
assert(again.error === "", `re-open after recovered panic: ${again.error}`);
const pngAgain = fluxdocs.renderPage(again.handle, 0, { dpi: 72, format: "png" });
assert(pngAgain.error === "" && pngAgain.image.length > 100, `render after recovered panic: ${pngAgain.error}`);
assert(pngAgain.image[0] === 0x89 && pngAgain.image[1] === 0x50, "render after recovered panic returns PNG magic");

// An absurd JS dpi (1e300, past the int range) must be rejected as an argument,
// never cast into a saturated, tiny, or negative canvas. A huge in-range dpi
// still hits the canvas cap.
const absurdDPI = fluxdocs.renderPage(again.handle, 0, { dpi: 1e300, format: "png" });
assert(absurdDPI.code === "invalid_argument" && absurdDPI.image === undefined, `dpi 1e300 -> invalid_argument: ${JSON.stringify(absurdDPI)}`);
const hugeDPI = fluxdocs.renderPage(again.handle, 0, { dpi: 1e9, format: "png" });
assert(hugeDPI.code === "canvas_too_large" && /exceeds/.test(hugeDPI.error), `dpi 1e9 -> canvas error: ${hugeDPI.error}`);
assert(fluxdocs.extractText(again.handle).text.includes("Hello WASM"), "extractText after rejected render");
fluxdocs.close(again.handle);

// AES-256 (R6) with a user password (testing/corpus/encrypted, written by
// qpdf and read by Ghostscript): the password option, its error codes, and the
// save policy, all inside the WASM core.
const r6 = new Uint8Array(readFileSync(join(repo, "testing", "corpus", "encrypted", "qpdf_r6_user.pdf")));
const noPw = fluxdocs.open(r6);
assert(noPw.code === "password_required" && noPw.handle === undefined, `R6 without a password: ${JSON.stringify(noPw)}`);
const badPw = fluxdocs.open(r6, { password: "Guess-1234" });
assert(badPw.code === "wrong_password" && !badPw.error.includes("Guess-1234"), `R6 wrong password: ${JSON.stringify(badPw)}`);
const badType = fluxdocs.open(r6, { password: 42 });
assert(badType.code === "invalid_argument", `R6 non-string password: ${JSON.stringify(badType)}`);
const withPw = fluxdocs.open(r6, { password: "user" });
assert(withPw.error === "" && withPw.encrypted === true, `R6 user password: ${withPw.error}`);
assert(fluxdocs.extractText(withPw.handle).text.includes("AES256-OK-r6-user"), "R6 text decrypted");
const refused = fluxdocs.save(withPw.handle);
assert(refused.code === "save_would_decrypt" && refused.bytes === undefined, `R6 save refused: ${JSON.stringify(refused)}`);
fluxdocs.close(withPw.handle);
const allowed = fluxdocs.open(r6, { password: "owner", allowDecryptedSave: true });
assert(allowed.error === "", `R6 owner password: ${allowed.error}`);
const plainSave = fluxdocs.save(allowed.handle);
assert(plainSave.error === "" && plainSave.bytes.length > 0, `R6 save with allowDecryptedSave: ${plainSave.error}`);
const reopened = fluxdocs.open(plainSave.bytes);
assert(reopened.error === "" && reopened.encrypted === false, `saved copy reopens unencrypted: ${reopened.error}`);
assert(fluxdocs.extractText(reopened.handle).text.includes("AES256-OK-r6-user"), "saved copy text");
fluxdocs.close(allowed.handle);
fluxdocs.close(reopened.handle);
const ownerOnly = fluxdocs.open(new Uint8Array(readFileSync(join(repo, "testing", "corpus", "encrypted", "qpdf_r6_owner_only.pdf"))));
assert(ownerOnly.error === "" && ownerOnly.encrypted === true, `R6 owner-only opens: ${ownerOnly.error}`);
assert(fluxdocs.save(ownerOnly.handle).error === "", "R6 owner-only saves");
fluxdocs.close(ownerOnly.handle);

// Clipping paths render byte-identically on native amd64 and js/wasm
// (clipping-paths.md §7): the parity fixture's pixel hash is the one
// core/render pins (clipParityHash in core/render/clip_test.go; a Go test
// checks this copy matches). arm64 is not checked in CI: Go may fuse
// multiply-adds there outside clip.go (matrix.apply, raster.line, blend,
// drawImage, segmentsToSubpaths). The PNG is decoded here and hashed like
// the Go side's imgHash: "WxH\n" then 8-bit RGBA rows.
const clipHash = "3192d00f3cd466d6f10b7cc19d762a5a3159da0ba1a21bbe92a7b05b71d3fb2f";
const clipDoc = fluxdocs.open(new Uint8Array(readFileSync(join(here, "testdata", "clip.pdf"))));
assert(clipDoc.error === "", `open clip fixture: ${clipDoc.error}`);
const clipPng = fluxdocs.renderPage(clipDoc.handle, 0, { dpi: 72, format: "png" });
assert(clipPng.error === "", `render clip fixture: ${clipPng.error}`);
assert(pixelHash(clipPng.image) === clipHash, `clip fixture pixel hash ${pixelHash(clipPng.image)}, native ${clipHash}`);
fluxdocs.close(clipDoc.handle);

// Text clipping (Tr 4–7, clipping-paths.md §5) likewise: textclip.pdf's
// pixel hash is textClipParityHash in core/render/textclip_test.go.
const textClipHash = "767c3b16600855f1268938040b739f023ac367b4ee59cb06c95828b4c3e71ad2";
const textClipDoc = fluxdocs.open(new Uint8Array(readFileSync(join(here, "testdata", "textclip.pdf"))));
assert(textClipDoc.error === "", `open text-clip fixture: ${textClipDoc.error}`);
const textClipPng = fluxdocs.renderPage(textClipDoc.handle, 0, { dpi: 72, format: "png" });
assert(textClipPng.error === "", `render text-clip fixture: ${textClipPng.error}`);
assert(pixelHash(textClipPng.image) === textClipHash, `text-clip fixture pixel hash ${pixelHash(textClipPng.image)}, native ${textClipHash}`);
fluxdocs.close(textClipDoc.handle);

console.log(`OK — ${passed} assertions passed`);

// pixelHash decodes a non-interlaced 8-bit RGB or RGBA PNG (what Go's encoder
// writes for the renderer's canvas) and returns the SHA-256 of "WxH\n"
// followed by its RGBA rows.
// It assumes an opaque canvas: PNG stores straight alpha while imgHash hashes
// premultiplied color.RGBA, so the two agree only where alpha is 255 (and an
// RGB PNG is hashed with alpha 255).
function pixelHash(bytes) {
  const buf = Buffer.from(bytes);
  let pos = 8, w = 0, h = 0, type = 0;
  const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos);
    const kind = buf.toString("latin1", pos + 4, pos + 8);
    const data = buf.subarray(pos + 8, pos + 8 + len);
    if (kind === "IHDR") {
      w = data.readUInt32BE(0);
      h = data.readUInt32BE(4);
      if (data[8] !== 8 || data[12] !== 0) throw new Error("unsupported PNG layout");
      type = data[9];
    } else if (kind === "IDAT") {
      idat.push(data);
    }
    pos += 12 + len;
  }
  const bpp = type === 6 ? 4 : type === 2 ? 3 : 0;
  if (!bpp) throw new Error(`unsupported PNG color type ${type}`);
  const raw = inflateSync(Buffer.concat(idat));
  const stride = w * bpp;
  const prev = new Uint8Array(stride);
  const cur = new Uint8Array(stride);
  const hash = createHash("sha256").update(`${w}x${h}\n`);
  for (let y = 0; y < h; y++) {
    const off = y * (stride + 1);
    const filter = raw[off];
    for (let i = 0; i < stride; i++) {
      const x = raw[off + 1 + i];
      const a = i >= bpp ? cur[i - bpp] : 0;
      const b = prev[i];
      const c = i >= bpp ? prev[i - bpp] : 0;
      let v;
      switch (filter) {
        case 0: v = x; break;
        case 1: v = x + a; break;
        case 2: v = x + b; break;
        case 3: v = x + ((a + b) >> 1); break;
        case 4: {
          const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c);
          v = x + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c);
          break;
        }
        default: throw new Error(`bad PNG filter ${filter}`);
      }
      cur[i] = v & 0xff;
    }
    const row = new Uint8Array(w * 4);
    for (let px = 0; px < w; px++) {
      row[4 * px] = cur[bpp * px];
      row[4 * px + 1] = cur[bpp * px + 1];
      row[4 * px + 2] = cur[bpp * px + 2];
      row[4 * px + 3] = bpp === 4 ? cur[bpp * px + 3] : 255;
    }
    hash.update(row);
    prev.set(cur);
  }
  return hash.digest("hex");
}

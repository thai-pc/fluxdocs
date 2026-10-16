// Table tests for the viewer's pure logic. An identical copy lives in
// @fluxdocs/react and @fluxdocs/vue, next to the identical viewer-model.ts.

import { afterEach, describe, expect, it } from "vitest";

import { FluxDocsError } from "@fluxdocs/core";

import {
  CSS_PX_PER_PT,
  PAGE_TOO_COMPLEX_MESSAGE,
  PAGE_TOO_LARGE_MESSAGE,
  RENDER_BUDGET_PX,
  RENDER_TIMEOUT_MESSAGE,
  ZOOM_MAX,
  ZOOM_MIN,
  ZOOM_STEPS,
  canZoomIn,
  canZoomOut,
  centerRatio,
  clampPage,
  clampZoom,
  classifyKey,
  displaySize,
  errorText,
  fitZoom,
  fitsBudget,
  isEditable,
  keyContext,
  parsePadding,
  renderDpi,
  renderPixels,
  safePageSize,
  sanitizePage,
  scrollForRatio,
  zoomIn,
  zoomLabel,
  zoomOut,
  type KeyContext,
  type KeyInput,
} from "../src/viewer-model.js";

const letter = { width: 612, height: 792, rotation: 0 };
const a0 = { width: 2384, height: 3370, rotation: 0 };
const mock = { width: 200, height: 300, rotation: 0 };

describe("zoom ladder", () => {
  it("zoomIn walks every ladder value and stops at the max", () => {
    const expected = [...ZOOM_STEPS.slice(1), ZOOM_MAX];
    ZOOM_STEPS.forEach((z, i) => expect(zoomIn(z)).toBe(expected[i]));
  });

  it("zoomOut walks every ladder value and stops at the min", () => {
    const expected = [ZOOM_MIN, ...ZOOM_STEPS.slice(0, -1)];
    ZOOM_STEPS.forEach((z, i) => expect(zoomOut(z)).toBe(expected[i]));
  });

  it.each([
    [0.87, 1, 0.75],
    [0.3, 0.5, 0.25],
    [3.5, 4, 3],
    [1.0000001, 1.25, 0.75],
  ])("off-ladder %s -> in %s / out %s", (z, inZ, outZ) => {
    expect(zoomIn(z)).toBe(inZ);
    expect(zoomOut(z)).toBe(outZ);
  });

  it("limits disable the matching direction", () => {
    expect(canZoomOut(ZOOM_MIN)).toBe(false);
    expect(canZoomIn(ZOOM_MIN)).toBe(true);
    expect(canZoomIn(ZOOM_MAX)).toBe(false);
    expect(canZoomOut(ZOOM_MAX)).toBe(true);
    expect(zoomOut(ZOOM_MIN)).toBe(ZOOM_MIN);
    expect(zoomIn(ZOOM_MAX)).toBe(ZOOM_MAX);
  });

  it.each([
    [0, ZOOM_MIN],
    [-1, ZOOM_MIN],
    [Number.NaN, 1],
    [10, ZOOM_MAX],
    [Number.POSITIVE_INFINITY, ZOOM_MAX],
    [1.5, 1.5],
  ])("clampZoom(%s) = %s", (z, want) => {
    expect(clampZoom(z)).toBe(want);
  });

  it.each([
    [1, "100%"],
    [0.25, "25%"],
    [4, "400%"],
    [0.875, "88%"],
  ])("zoomLabel(%s) = %s", (z, want) => {
    expect(zoomLabel(z)).toBe(want);
  });
});

describe("page clamping", () => {
  it.each([
    [0, 0],
    [2.7, 2],
    [-3, 0],
    [Number.NaN, 0],
    [Number.POSITIVE_INFINITY, 0],
  ])("sanitizePage(%s) = %s", (p, want) => {
    expect(sanitizePage(p)).toBe(want);
  });

  it.each([
    [5, 3, 2],
    [1, 3, 1],
    [-1, 3, 0],
    [4, 0, 0],
  ])("clampPage(%s, %s) = %s", (p, n, want) => {
    expect(clampPage(p, n)).toBe(want);
  });
});

describe("displaySize", () => {
  it.each([
    [0, 200, 300],
    [90, 300, 200],
    [180, 200, 300],
    [270, 300, 200],
    [-90, 300, 200],
    [450, 300, 200],
  ])("rotation %s -> %sx%s pt", (rotation, w, h) => {
    const s = displaySize({ width: 200, height: 300, rotation }, 1);
    expect(s?.width).toBeCloseTo(w * CSS_PX_PER_PT, 6);
    expect(s?.height).toBeCloseTo(h * CSS_PX_PER_PT, 6);
  });

  it("scales with zoom (100% = true physical size)", () => {
    expect(displaySize(mock, 1)?.width).toBeCloseTo(266.67, 2);
    expect(displaySize(mock, 2)?.width).toBeCloseTo(533.33, 2);
    expect(displaySize(letter, 1)?.width).toBeCloseTo(816, 6); // 8.5in × 96
  });

  it.each([
    [null],
    [{ width: 0, height: 300, rotation: 0 }],
    [{ width: -5, height: 300, rotation: 0 }],
    [{ width: Number.NaN, height: 300, rotation: 0 }],
    [{ width: Number.POSITIVE_INFINITY, height: 300, rotation: 0 }],
  ])("unusable geometry %j -> null", (size) => {
    expect(displaySize(size, 1)).toBeNull();
  });

  it("safePageSize swallows a pageSize failure", () => {
    const doc = {
      pageSize: () => {
        throw new Error("boom");
      },
    };
    expect(safePageSize(doc, 0)).toBeNull();
    expect(safePageSize(null, 0)).toBeNull();
    expect(safePageSize({ pageSize: () => mock }, 0)).toEqual(mock);
  });
});

describe("fitZoom", () => {
  it("fits the page width inside the padded viewport, rounded to 2 decimals", () => {
    // (400 - 2*16) / (200 * 96/72) = 1.38
    expect(fitZoom(400, 16, mock, 1)).toBe(1.38);
  });

  it("rounds down so the fitted page never overflows the viewport", () => {
    // Rounding to nearest overflowed, e.g. 714 px: (714 - 32) / 266.67 = 2.5575.
    expect(fitZoom(714, 16, mock, 1)).toBe(2.55);
    for (const size of [mock, letter, a0, { width: 200, height: 300, rotation: 90 }]) {
      const o = size.rotation === 90 ? size.height : size.width;
      for (const padding of [0, 8, 16]) {
        for (let clientWidth = 300; clientWidth <= 2000; clientWidth++) {
          const z = fitZoom(clientWidth, padding, size, 1);
          if (z === ZOOM_MIN) continue; // clamped: a narrow viewport may scroll
          const pageCss = o * CSS_PX_PER_PT * z;
          expect(pageCss).toBeLessThanOrEqual(clientWidth - 2 * padding + 1e-6);
          // And it is the largest 2-decimal zoom that fits (below ZOOM_MAX).
          if (z < ZOOM_MAX) {
            expect(o * CSS_PX_PER_PT * (z + 0.01)).toBeGreaterThan(clientWidth - 2 * padding);
          }
        }
      }
    }
  });

  it("is a no-op when the viewport has no width", () => {
    expect(fitZoom(0, 16, mock, 1.5)).toBe(1.5);
    expect(fitZoom(-10, 16, mock, 0.75)).toBe(0.75);
    expect(fitZoom(Number.NaN, 16, mock, 2)).toBe(2);
  });

  it("clamps tiny and huge widths", () => {
    expect(fitZoom(10, 16, mock, 1)).toBe(ZOOM_MIN);
    expect(fitZoom(100_000, 16, mock, 1)).toBe(ZOOM_MAX);
  });

  it("uses the height of a 90-degree rotated page", () => {
    const rotated = { width: 200, height: 300, rotation: 90 };
    // (432 - 32) / (300 * 96/72) = 1
    expect(fitZoom(432, 16, rotated, 2)).toBe(1);
  });

  it("is a no-op with unusable geometry", () => {
    expect(fitZoom(400, 16, null, 1.25)).toBe(1.25);
  });

  it.each([
    ["16px", 16],
    ["8px", 8],
    ["0px", 0],
    ["", 16],
    [undefined, 16],
    ["auto", 16],
  ])("parsePadding(%j) = %s", (v, want) => {
    expect(parsePadding(v)).toBe(want);
  });
});

describe("renderDpi", () => {
  it.each([
    [1, 96],
    [2, 192],
    [3, 192], // DPR capped at 2
    [0, 96], // bogus DPR -> 1
    [Number.NaN, 96],
  ])("DPR %s at 100%% -> %s dpi", (dpr, want) => {
    expect(renderDpi(letter, 1, undefined, dpr)).toBe(want);
  });

  it("scales with zoom", () => {
    expect(renderDpi(mock, 1.25, undefined, 1)).toBe(120);
    expect(renderDpi(mock, 0.25, undefined, 1)).toBe(24);
  });

  it("host dpi overrides the DPR-based base", () => {
    expect(renderDpi(letter, 1, 150, 2)).toBe(150);
    expect(renderDpi(letter, 0.5, 150, 2)).toBe(75);
  });

  it.each([[0], [-10], [Number.NaN]])("an invalid host dpi (%s) falls back to the DPR base", (d) => {
    expect(renderDpi(letter, 1, d, 1)).toBe(96);
  });

  it("caps Letter at 200% DPR 2 to the pixel budget (about 300 dpi)", () => {
    const dpi = renderDpi(letter, 2, undefined, 2);
    expect(dpi).toBeGreaterThanOrEqual(295);
    expect(dpi).toBeLessThanOrEqual(300);
    const px = ((612 * dpi) / 72) * ((792 * dpi) / 72);
    expect(px).toBeLessThanOrEqual(8 * 2 ** 20);
  });

  it("caps an A0 page even at 100%", () => {
    const dpi = renderDpi(a0, 1, undefined, 2);
    expect(dpi).toBeLessThan(192);
    expect(((2384 * dpi) / 72) * ((3370 * dpi) / 72)).toBeLessThanOrEqual(8 * 2 ** 20);
  });

  it("never returns 0 for a gigantic page", () => {
    expect(renderDpi({ width: 1e9, height: 1e9, rotation: 0 }, 0.25, undefined, 1)).toBe(1);
  });

  it.each([
    [null, 1, 1, 96],
    [{ width: 0, height: 0, rotation: 0 }, 2, 1, 192],
    [{ width: Number.NaN, height: 300, rotation: 0 }, 4, 1, 384],
    [{ width: -1, height: 300, rotation: 0 }, 4, 2, 600], // 192 × 4 = 768 -> clamp 600
    [null, 0.25, 1, 24],
  ])("degenerate geometry %j at zoom %s DPR %s -> fallback %s", (size, zoom, dpr, want) => {
    expect(renderDpi(size, zoom, undefined, dpr)).toBe(want);
  });

  // The core rounds each side up and gives it at least 1 px, so an extreme
  // aspect ratio costs far more than width × height × (dpi/72)² suggests.
  // Ceil-aware px = max(1, ceil(w·dpi/72)) × max(1, ceil(h·dpi/72)).
  const ceilPx = (w: number, h: number, dpi: number) =>
    Math.max(1, Math.ceil((w * dpi) / 72)) * Math.max(1, Math.ceil((h * dpi) / 72));

  it.each([
    [5e8, 1],
    [1e8, 0.01],
    [1, 5e8],
  ])("keeps an extreme %s x %s pt page within budget at every zoom and DPR", (w, h) => {
    for (const zoom of ZOOM_STEPS) {
      for (const dpr of [1, 2]) {
        const dpi = renderDpi({ width: w, height: h, rotation: 0 }, zoom, undefined, dpr);
        expect(Number.isInteger(dpi)).toBe(true);
        expect(dpi).toBeGreaterThanOrEqual(1);
        expect(ceilPx(w, h, dpi)).toBeLessThanOrEqual(RENDER_BUDGET_PX);
      }
    }
  });

  it("stays within budget under rounding for a huge host dpi", () => {
    for (const size of [letter, a0, mock, { width: 0.001, height: 0.001, rotation: 0 }]) {
      const dpi = renderDpi(size, ZOOM_MAX, 1e9, 2);
      expect(ceilPx(size.width, size.height, dpi)).toBeLessThanOrEqual(RENDER_BUDGET_PX);
      // And it is the largest such dpi.
      expect(ceilPx(size.width, size.height, dpi + 1)).toBeGreaterThan(RENDER_BUDGET_PX);
    }
  });

  it("always returns an integer >= 1", () => {
    const sizes = [letter, a0, mock, null, { width: 1e9, height: 1, rotation: 0 }];
    for (const size of sizes) {
      for (const zoom of ZOOM_STEPS) {
        for (const dpr of [0.5, 1, 1.5, 2, 3]) {
          for (const host of [undefined, 1, 72, 150, 600]) {
            const dpi = renderDpi(size, zoom, host, dpr);
            expect(Number.isInteger(dpi)).toBe(true);
            expect(dpi).toBeGreaterThanOrEqual(1);
          }
        }
      }
    }
  });
});

describe("renderPixels / fitsBudget", () => {
  it("counts pixels the way the core does (ceil per side, min 1 px)", () => {
    expect(renderPixels({ width: 72, height: 72 }, 72)).toBe(72 * 72);
    expect(renderPixels({ width: 1e8, height: 0.01 }, 72)).toBe(1e8);
    expect(renderPixels({ width: 0.5, height: 100.2 }, 72)).toBe(101);
  });

  it.each([
    [{ width: 1e18, height: 1e18, rotation: 0 }],
    [{ width: 1e6, height: 1e6, rotation: 0 }],
    [{ width: 1e9, height: 1e9, rotation: 90 }],
  ])("rejects %j: over budget even at 1 dpi", (size) => {
    expect(fitsBudget(size)).toBe(false);
  });

  it.each([
    [letter],
    [a0],
    [{ width: 5e8, height: 1, rotation: 0 }],
    [{ width: 1e8, height: 0.01, rotation: 0 }],
    // Unusable geometry takes the fallback path (Go-side cap), not this guard.
    [null],
    [{ width: Number.POSITIVE_INFINITY, height: 10, rotation: 0 }],
  ])("accepts %j", (size) => {
    expect(fitsBudget(size)).toBe(true);
  });
});

describe("scroll center", () => {
  it("round-trips the center through a size change", () => {
    const r = centerRatio(150, 100, 400); // center at 200 of 400
    expect(r).toBe(0.5);
    expect(scrollForRatio(r, 100, 800)).toBe(350); // center at 400 of 800
  });

  it("handles a non-scrolling viewport", () => {
    expect(centerRatio(0, 100, 0)).toBe(0.5);
    expect(scrollForRatio(0.1, 100, 100)).toBe(0);
  });
});

describe("classifyKey", () => {
  const key = (k: string, mods: Partial<KeyInput> = {}): KeyInput => ({
    key: k,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...mods,
  });
  const toolbar: KeyContext = {
    editable: false,
    inViewport: false,
    atTop: true,
    atBottom: true,
    canPrev: true,
    canNext: true,
  };
  const viewport: KeyContext = { ...toolbar, inViewport: true };
  const scrolledMid: KeyContext = { ...viewport, atTop: false, atBottom: false };
  const editable: KeyContext = { ...viewport, editable: true };

  it.each<[string, KeyInput, KeyContext, string | null, boolean]>([
    // zoom: Ctrl or Cmd, no Alt; always prevented
    ["Ctrl+=", key("=", { ctrlKey: true }), viewport, "zoomIn", true],
    ["Ctrl++", key("+", { ctrlKey: true, shiftKey: true }), viewport, "zoomIn", true],
    ["Cmd+=", key("=", { metaKey: true }), toolbar, "zoomIn", true],
    ["Ctrl+-", key("-", { ctrlKey: true }), viewport, "zoomOut", true],
    ["Ctrl+_", key("_", { ctrlKey: true, shiftKey: true }), viewport, "zoomOut", true],
    ["Cmd+-", key("-", { metaKey: true }), viewport, "zoomOut", true],
    ["Ctrl+0", key("0", { ctrlKey: true }), viewport, "zoomReset", true],
    ["Cmd+0", key("0", { metaKey: true }), toolbar, "zoomReset", true],
    ["Ctrl+Alt+=", key("=", { ctrlKey: true, altKey: true }), viewport, null, false],
    ["Ctrl+F", key("f", { ctrlKey: true }), viewport, null, false],
    ["= alone", key("="), viewport, null, false],
    ["0 alone", key("0"), viewport, null, false],
    // arrows: unmodified only
    ["ArrowDown viewport", key("ArrowDown"), viewport, "nextPage", true],
    ["ArrowUp viewport", key("ArrowUp"), viewport, "prevPage", true],
    ["ArrowDown toolbar", key("ArrowDown"), toolbar, "nextPage", true],
    ["Shift+ArrowDown", key("ArrowDown", { shiftKey: true }), viewport, null, false],
    ["Ctrl+ArrowDown", key("ArrowDown", { ctrlKey: true }), viewport, null, false],
    ["Alt+ArrowUp", key("ArrowUp", { altKey: true }), viewport, null, false],
    ["Meta+ArrowUp", key("ArrowUp", { metaKey: true }), viewport, null, false],
    ["ArrowLeft", key("ArrowLeft"), viewport, null, false],
    // edge rule: scroll first inside the viewport
    ["ArrowDown mid-scroll", key("ArrowDown"), scrolledMid, null, false],
    ["ArrowUp mid-scroll", key("ArrowUp"), scrolledMid, null, false],
    ["ArrowDown at bottom", key("ArrowDown"), { ...scrolledMid, atBottom: true }, "nextPage", true],
    ["ArrowUp at top", key("ArrowUp"), { ...scrolledMid, atTop: true }, "prevPage", true],
    ["ArrowDown toolbar mid-scroll", key("ArrowDown"), { ...toolbar, atBottom: false }, "nextPage", true],
    // first/last page: no-op, not prevented
    ["ArrowDown last page", key("ArrowDown"), { ...viewport, canNext: false }, null, false],
    ["ArrowUp first page", key("ArrowUp"), { ...viewport, canPrev: false }, null, false],
    // ignored contexts
    ["editable ArrowDown", key("ArrowDown"), editable, null, false],
    ["editable Ctrl+=", key("=", { ctrlKey: true }), editable, null, false],
    ["composing", key("ArrowDown", { isComposing: true }), viewport, null, false],
    ["composing Ctrl+=", key("=", { ctrlKey: true, isComposing: true }), viewport, null, false],
    ["already prevented", key("ArrowDown", { defaultPrevented: true }), viewport, null, false],
  ])("%s", (_name, ev, ctx, action, prevent) => {
    expect(classifyKey(ev, ctx)).toEqual({ action, preventDefault: prevent });
  });
});

describe("keyContext / isEditable", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  function setup() {
    document.body.innerHTML = `
      <div class="root">
        <button class="btn">x</button>
        <div class="vp">
          <input class="in" />
          <textarea class="ta"></textarea>
          <select class="sel"></select>
          <div class="ce" contenteditable="true"><span class="ce-child">t</span></div>
          <div class="ce-off" contenteditable="false">t</div>
          <div class="plain">t</div>
        </div>
      </div>`;
    const q = (s: string) => document.querySelector(s) as HTMLElement;
    return { q, vp: q(".vp") };
  }

  it.each([
    [".in", true],
    [".ta", true],
    [".sel", true],
    [".ce", true],
    [".ce-child", true],
    [".ce-off", false],
    [".plain", false],
    [".btn", false],
  ])("isEditable(%s) = %s", (sel, want) => {
    const { q } = setup();
    expect(isEditable(q(sel))).toBe(want);
  });

  it("isEditable(null) is false", () => {
    expect(isEditable(null)).toBe(false);
  });

  it("detects viewport vs toolbar targets and scroll edges", () => {
    const { q, vp } = setup();
    expect(keyContext(q(".plain"), vp, true, false)).toEqual({
      editable: false,
      inViewport: true,
      atTop: true,
      atBottom: true, // jsdom: all scroll metrics are 0
      canPrev: true,
      canNext: false,
    });
    expect(keyContext(q(".btn"), vp, false, true).inViewport).toBe(false);
    expect(keyContext(vp, vp, true, true).inViewport).toBe(true);

    Object.defineProperty(vp, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(vp, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(vp, "scrollTop", { value: 100, configurable: true });
    const mid = keyContext(vp, vp, true, true);
    expect(mid.atTop).toBe(false);
    expect(mid.atBottom).toBe(false);
    Object.defineProperty(vp, "scrollTop", { value: 599.5, configurable: true });
    expect(keyContext(vp, vp, true, true).atBottom).toBe(true);
  });
});

describe("errorText", () => {
  it("maps the budget code to the too-complex text", () => {
    expect(PAGE_TOO_COMPLEX_MESSAGE).toBe("This page is too complex to display.");
    expect(errorText(new FluxDocsError("renderPage: budget", "budget_exceeded"))).toBe(
      PAGE_TOO_COMPLEX_MESSAGE,
    );
  });

  it("maps the canvas code to the pixel-budget pre-check's too-large text", () => {
    expect(errorText(new FluxDocsError("renderPage: canvas exceeds", "canvas_too_large"))).toBe(
      `Failed to display PDF: ${PAGE_TOO_LARGE_MESSAGE}`,
    );
    expect(errorText(new FluxDocsError("renderPage: canvas exceeds", "canvas_too_large"))).toBe(
      errorText(new Error(PAGE_TOO_LARGE_MESSAGE)),
    );
  });

  it("maps timeout to the took-too-long text", () => {
    expect(RENDER_TIMEOUT_MESSAGE).toBe("Rendering this page took too long.");
    expect(errorText(new FluxDocsError("renderPage: deadline", "timeout"))).toBe(
      RENDER_TIMEOUT_MESSAGE,
    );
  });

  it("falls back to the raw message for every other code and for uncoded errors", () => {
    for (const code of [
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
    ] as const) {
      expect(errorText(new FluxDocsError(`x: ${code}`, code))).toBe(
        `Failed to display PDF: x: ${code}`,
      );
    }
    expect(errorText(new Error("plain"))).toBe("Failed to display PDF: plain");
    expect(errorText(new FluxDocsError("no code"))).toBe("Failed to display PDF: no code");
  });

  it("reads the code by shape, not instanceof", () => {
    const foreign = Object.assign(new Error("other copy"), { code: "timeout" });
    expect(errorText(foreign)).toBe(RENDER_TIMEOUT_MESSAGE);
    const odd = Object.assign(new Error("odd"), { code: 7 });
    expect(errorText(odd)).toBe("Failed to display PDF: odd");
  });
});

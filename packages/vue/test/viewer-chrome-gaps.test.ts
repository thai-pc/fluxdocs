import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";
import type { FluxDoc, FluxDocs } from "@fluxdocs/core";

import { FluxDocsViewer } from "../src/index.js";
import { mockRuntime, type MockOptions } from "./mock.js";

// Gap tests for the viewer chrome (ui-tester). The same scenarios, with the
// same expected strings, live in packages/react/test/viewer-chrome-gaps.test.tsx;
// a divergence between the two files is a parity bug.

let w: VueWrapper | null = null;
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
});

const data = new Uint8Array([1, 2, 3]);

function mountViewer(props: Record<string, unknown> = {}): VueWrapper {
  w?.unmount();
  w = mount(FluxDocsViewer, {
    props: { runtime: mockRuntime(), data, ...props },
    attachTo: document.body,
  });
  return w;
}

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector(sel) as T;
const all = (sel: string) => Array.from(document.querySelectorAll<HTMLElement>(sel));
const byText = (re: RegExp) => all(".fd-viewer *").find((el) => re.test(el.textContent ?? "") && el.children.length === 0);
const level = () => byText(/^\d+%$/)?.textContent;
const info = () => byText(/^Page \d+ of \d+$/)?.textContent;
const viewport = () => q<HTMLElement>('[role="region"][aria-label="Document"]');
const btn = (name: string) =>
  all("button").find((b) => b.getAttribute("aria-label") === name) as HTMLButtonElement;

async function click(el: HTMLElement): Promise<void> {
  el.click();
  await nextTick();
}

// press dispatches a keydown like fireEvent: returns false when prevented.
async function press(el: Element, init: KeyboardEventInit): Promise<boolean> {
  const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  el.dispatchEvent(ev);
  await nextTick();
  return !ev.defaultPrevented;
}

/** A runtime whose opened docs record close() calls, in open order. */
function trackingRuntime(opts: MockOptions = {}): { runtime: FluxDocs; docs: { closed: number }[] } {
  const base = mockRuntime(opts);
  const docs: { closed: number }[] = [];
  const runtime = {
    version: () => base.version(),
    open: (d: Uint8Array) => {
      const doc = base.open(d) as FluxDoc;
      const rec = { closed: 0 };
      docs.push(rec);
      return new Proxy(doc, {
        get(target, prop, recv) {
          if (prop === "close") {
            return () => {
              rec.closed++;
            };
          }
          return Reflect.get(target, prop, recv);
        },
      });
    },
  } as unknown as FluxDocs;
  return { runtime, docs };
}

/**
 * Stubs the viewport's scroll metrics. scrollHeight/scrollWidth follow the
 * page box's inline size (+64 px of padding and margin) so a zoom changes them.
 */
function stubScroll(vp: HTMLElement, m: { clientWidth: number; clientHeight: number }) {
  const page = () => q<HTMLElement>(".fd-viewer__page");
  let top = 0;
  let left = 0;
  Object.defineProperties(vp, {
    clientWidth: { configurable: true, get: () => m.clientWidth },
    clientHeight: { configurable: true, get: () => m.clientHeight },
    scrollHeight: { configurable: true, get: () => parseFloat(page().style.height) + 64 },
    scrollWidth: {
      configurable: true,
      get: () => Math.max(m.clientWidth, parseFloat(page().style.width) + 64),
    },
    scrollTop: {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    },
    scrollLeft: {
      configurable: true,
      get: () => left,
      set: (v: number) => {
        left = v;
      },
    },
  });
}

/**
 * signature serializes the accessible/BEM shape of the viewer DOM: tag,
 * classes, role, aria-*, tabindex, disabled, alt, and direct text. Both
 * wrappers must produce the same lines for the same scenario.
 */
function signature(root: Element): string[] {
  const out: string[] = [];
  const walk = (el: Element, depth: number) => {
    const parts = [el.tagName.toLowerCase()];
    const cls = el.getAttribute("class");
    if (cls) parts.push("." + cls.trim().split(/\s+/).join("."));
    for (const a of ["type", "role", "aria-label", "aria-live", "aria-atomic", "tabindex", "alt"]) {
      const v = el.getAttribute(a);
      if (v !== null) parts.push(`[${a}=${v}]`);
    }
    if (el.hasAttribute("disabled")) parts.push("[disabled]");
    const text = Array.from(el.childNodes)
      .filter((n) => n.nodeType === Node.TEXT_NODE)
      .map((n) => n.textContent)
      .join("")
      .trim();
    if (text) parts.push(JSON.stringify(text));
    out.push("  ".repeat(depth) + parts.join(""));
    for (const c of Array.from(el.children)) walk(c, depth + 1);
  };
  walk(root, 0);
  return out;
}

// Shared expectations (copied verbatim from the React file).
const READY_SIGNATURE = [
  "div.fd-viewer.host-class",
  '  div.fd-viewer__toolbar',
  '    div.fd-viewer__group.fd-viewer__group--nav[role=group][aria-label=Page navigation]',
  '      button.fd-viewer__btn.fd-viewer__nav.fd-viewer__nav--prev[type=button][aria-label=Previous page][disabled]"‹"',
  '      span.fd-viewer__page-info[role=status][aria-live=polite][aria-atomic=true]"Page 1 of 3"',
  '      button.fd-viewer__btn.fd-viewer__nav.fd-viewer__nav--next[type=button][aria-label=Next page]"›"',
  '    div.fd-viewer__group.fd-viewer__group--zoom[role=group][aria-label=Zoom]',
  '      button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--out[type=button][aria-label=Zoom out]"−"',
  '      span.fd-viewer__zoom-level[role=status][aria-live=polite][aria-atomic=true]"100%"',
  '      button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--in[type=button][aria-label=Zoom in]"+"',
  '      button.fd-viewer__btn.fd-viewer__zoom.fd-viewer__zoom--fit[type=button][aria-label=Fit to width]"Fit"',
  '  div.fd-viewer__viewport[role=region][aria-label=Document][tabindex=0]',
  '    div.fd-viewer__page',
  '      img.fd-viewer__canvas[alt=Page 1]',
];
const EMPTY_SIGNATURE = ["div.fd-viewer", '  p.fd-viewer__empty"No document loaded."'];
const ERROR_SIGNATURE = [
  "div.fd-viewer[role=alert]",
  '  p.fd-viewer__error"Failed to display PDF: bad pdf"',
];

describe("viewer chrome gaps: parity signature", () => {
  it("ready state matches the shared DOM signature", () => {
    mountViewer({ className: "host-class" });
    expect(signature(q(".fd-viewer"))).toEqual(READY_SIGNATURE);
  });

  it("empty and error states match the shared DOM signature", () => {
    mountViewer({ runtime: null, data: null });
    expect(signature(q(".fd-viewer"))).toEqual(EMPTY_SIGNATURE);
    mountViewer({ runtime: mockRuntime({ openError: "bad pdf" }) });
    expect(signature(q(".fd-viewer"))).toEqual(ERROR_SIGNATURE);
  });
});

describe("viewer chrome gaps: Arrow edge rule", () => {
  it("scrolls first inside a zoomed viewport and pages only at the edge", async () => {
    mountViewer({ initialZoom: 2 });
    const vp = viewport();
    // 200x300 pt at 200% = 533x800 CSS px; scrollHeight 864, clientHeight 300.
    stubScroll(vp, { clientWidth: 400, clientHeight: 300 });

    vp.scrollTop = 100; // mid-page: not handled, native scroll proceeds
    expect(await press(vp, { key: "ArrowDown" })).toBe(true);
    expect(await press(vp, { key: "ArrowUp" })).toBe(true);
    expect(info()).toBe("Page 1 of 3");

    vp.scrollTop = 864 - 300; // at the bottom edge
    expect(await press(vp, { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
    expect(vp.scrollTop).toBe(0); // a page change starts at the top

    // At the top, ArrowUp pages back.
    expect(await press(vp, { key: "ArrowUp" })).toBe(false);
    expect(info()).toBe("Page 1 of 3");
  });

  it("lands at the bottom of the previous page after an ArrowUp page back", async () => {
    mountViewer({ initialZoom: 2, initialPage: 1 });
    const vp = viewport();
    stubScroll(vp, { clientWidth: 400, clientHeight: 300 });

    // At the top of page 2, ArrowUp pages back and lands at the bottom of page 1
    // (scrollHeight 864 - clientHeight 300), so reading continues upward.
    expect(await press(vp, { key: "ArrowUp" })).toBe(false);
    expect(info()).toBe("Page 1 of 3");
    expect(vp.scrollTop).toBe(564);
    // The next ArrowUp scrolls natively instead of skipping another page.
    expect(await press(vp, { key: "ArrowUp" })).toBe(true);
    expect(info()).toBe("Page 1 of 3");

    // ArrowDown, the buttons, and ArrowUp from the toolbar still land at the top.
    expect(await press(vp, { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
    expect(vp.scrollTop).toBe(0);
    btn("Previous page").click();
    await nextTick();
    expect(info()).toBe("Page 1 of 3");
    expect(vp.scrollTop).toBe(0);
    btn("Next page").click();
    await nextTick();
    expect(await press(btn("Zoom in"), { key: "ArrowUp" })).toBe(false);
    expect(info()).toBe("Page 1 of 3");
    expect(vp.scrollTop).toBe(0);
  });

  it("tolerates a fractional bottom position within 1px", async () => {
    mountViewer({ initialZoom: 2 });
    const vp = viewport();
    stubScroll(vp, { clientWidth: 400, clientHeight: 300 });
    vp.scrollTop = 864 - 300 - 0.5;
    expect(await press(vp, { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
  });

  it("always pages from a toolbar button, even mid-scroll", async () => {
    mountViewer({ initialZoom: 2 });
    const vp = viewport();
    stubScroll(vp, { clientWidth: 400, clientHeight: 300 });
    vp.scrollTop = 100;
    expect(await press(btn("Zoom in"), { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
    expect(await press(btn("Fit to width"), { key: "ArrowUp" })).toBe(false);
    expect(info()).toBe("Page 1 of 3");
  });

  it("does not handle ArrowDown on the last page from a button", async () => {
    mountViewer({ initialPage: 2 });
    expect(await press(btn("Zoom in"), { key: "ArrowDown" })).toBe(true);
    expect(info()).toBe("Page 3 of 3");
  });
});

describe("viewer chrome gaps: focus rescue", () => {
  it("rescues focus when Zoom in reaches 400% by click", async () => {
    mountViewer({ initialZoom: 3 });
    const zin = btn("Zoom in");
    zin.focus();
    await click(zin);
    expect(level()).toBe("400%");
    expect(zin.disabled).toBe(true);
    expect(document.activeElement).toBe(viewport());
  });

  it("rescues focus when Zoom out reaches 25% by keyboard on the button", async () => {
    mountViewer({ initialZoom: 0.5 });
    const zout = btn("Zoom out");
    zout.focus();
    expect(await press(zout, { key: "-", ctrlKey: true })).toBe(false);
    expect(level()).toBe("25%");
    expect(zout.disabled).toBe(true);
    expect(document.activeElement).toBe(viewport());
  });

  it("rescues focus when ArrowDown on the focused Next button reaches the last page", async () => {
    mountViewer({ initialPage: 1 });
    const next = btn("Next page");
    next.focus();
    await press(next, { key: "ArrowDown" });
    expect(info()).toBe("Page 3 of 3");
    expect(next.disabled).toBe(true);
    expect(document.activeElement).toBe(viewport());
  });

  it("does not steal focus that is outside the viewer", async () => {
    mountViewer({ initialPage: 1 });
    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();
    await click(btn("Next page")); // pointer click without moving focus
    expect(info()).toBe("Page 3 of 3");
    expect(document.activeElement).toBe(outside);
  });

  it("keeps focus on a button that stays enabled", async () => {
    mountViewer();
    const zin = btn("Zoom in");
    zin.focus();
    await click(zin);
    expect(document.activeElement).toBe(zin);
  });
});

describe("viewer chrome gaps: zoom behavior", () => {
  it("keeps the viewport center when zooming", async () => {
    mountViewer();
    const vp = viewport();
    stubScroll(vp, { clientWidth: 200, clientHeight: 200 });
    // 100%: page 266.67x400 -> scroll 330.67x464. Center y = (100+100)/464.
    vp.scrollTop = 100;
    vp.scrollLeft = 30;
    const ry = (100 + 100) / 464;
    const rx = (30 + 100) / (266.6667 + 64);
    await click(btn("Zoom in"));
    // 125%: page 333.33x500 -> scroll 397.33x564.
    expect(vp.scrollTop).toBeCloseTo(ry * 564 - 100, 1);
    expect(vp.scrollLeft).toBeCloseTo(rx * (333.3333 + 64) - 100, 1);
  });

  it("steps from an off-ladder fit value to the neighbouring ladder values", async () => {
    mountViewer();
    Object.defineProperty(viewport(), "clientWidth", { value: 400, configurable: true });
    await click(btn("Fit to width"));
    expect(level()).toBe("138%");
    await click(btn("Zoom in"));
    expect(level()).toBe("150%");
    await click(btn("Fit to width"));
    await click(btn("Zoom out"));
    expect(level()).toBe("125%");
  });

  it("sizes a 90-degree rotated page with swapped sides", () => {
    mountViewer({ runtime: mockRuntime({ pageSize: { width: 200, height: 300, rotation: 90 } }) });
    const page = q<HTMLDivElement>(".fd-viewer__page");
    expect(parseFloat(page.style.width)).toBeCloseTo(400, 2);
    expect(parseFloat(page.style.height)).toBeCloseTo(266.67, 2);
  });

  it("treats Ctrl+Shift+'+' (shifted =) as zoom in", async () => {
    mountViewer();
    expect(await press(viewport(), { key: "+", ctrlKey: true, shiftKey: true })).toBe(false);
    expect(level()).toBe("125%");
  });
});

describe("viewer chrome gaps: inert states and lifecycle", () => {
  it("does not intercept keys in the empty and error states", async () => {
    mountViewer({ runtime: null, data: null });
    expect(await press(q(".fd-viewer"), { key: "=", ctrlKey: true })).toBe(true);
    expect(await press(q(".fd-viewer"), { key: "ArrowDown" })).toBe(true);
    mountViewer({ runtime: mockRuntime({ openError: "bad pdf" }) });
    expect(await press(q('[role="alert"]'), { key: "=", ctrlKey: true })).toBe(true);
  });

  it("closes the document on unmount", () => {
    const { runtime, docs } = trackingRuntime();
    const v = mountViewer({ runtime });
    expect(docs).toHaveLength(1);
    expect(docs[0].closed).toBe(0);
    v.unmount();
    w = null;
    expect(docs[0].closed).toBe(1);
  });

  it("closes the previous document when data changes and keeps the page in range", async () => {
    const { runtime, docs } = trackingRuntime();
    const v = mountViewer({ runtime, initialPage: 2 });
    expect(info()).toBe("Page 3 of 3");
    await v.setProps({ data: new Uint8Array([4, 5, 6]) });
    expect(docs).toHaveLength(2);
    expect(docs[0].closed).toBe(1);
    expect(docs[1].closed).toBe(0);
    expect(info()).toBe("Page 3 of 3");
  });

  it("returns to the empty state and closes the document when data is cleared", async () => {
    const { runtime, docs } = trackingRuntime();
    const v = mountViewer({ runtime });
    await v.setProps({ data: null });
    expect(docs[0].closed).toBe(1);
    expect(v.text()).toContain("No document loaded.");
    expect(document.querySelector("button")).toBeNull();
  });

  it("keeps the zoom when the document changes", async () => {
    const v = mountViewer();
    await click(btn("Zoom in"));
    await v.setProps({ runtime: mockRuntime(), data: new Uint8Array([9]) });
    expect(level()).toBe("125%");
  });
});

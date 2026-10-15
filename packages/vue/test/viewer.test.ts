import { afterEach, describe, expect, it } from "vitest";
import { mount, type VueWrapper } from "@vue/test-utils";
import { nextTick } from "vue";

import { FluxDocsViewer } from "../src/index.js";
import { mockRuntime, type RenderCall } from "./mock.js";

const data = new Uint8Array([1, 2, 3]);

describe("FluxDocsViewer", () => {
  it("renders the first page as an image with a data URL", () => {
    const w = mount(FluxDocsViewer, {
      props: { runtime: mockRuntime({ pageCount: 3 }), data },
    });
    expect(w.text()).toContain("Page 1 of 3");
    const img = w.find("img.fd-viewer__canvas");
    expect(img.exists()).toBe(true);
    expect(img.attributes("src")?.startsWith("data:image/png;base64,")).toBe(true);
  });

  it("navigates between pages and clamps at the ends", async () => {
    const w = mount(FluxDocsViewer, {
      props: { runtime: mockRuntime({ pageCount: 3 }), data },
    });
    const prev = w.get("button.fd-viewer__nav--prev");
    const next = w.get("button.fd-viewer__nav--next");

    expect((prev.element as HTMLButtonElement).disabled).toBe(true);
    await next.trigger("click");
    expect(w.text()).toContain("Page 2 of 3");
    await next.trigger("click");
    expect(w.text()).toContain("Page 3 of 3");
    expect((next.element as HTMLButtonElement).disabled).toBe(true);
    await prev.trigger("click");
    expect(w.text()).toContain("Page 2 of 3");
  });

  it("shows an empty state without a document", () => {
    const w = mount(FluxDocsViewer, { props: { runtime: null, data: null } });
    expect(w.text()).toContain("No document loaded.");
  });

  it("surfaces an open failure as an alert", () => {
    const w = mount(FluxDocsViewer, {
      props: { runtime: mockRuntime({ openError: "bad pdf" }), data },
    });
    expect(w.get('[role="alert"]').text()).toContain("bad pdf");
  });

  it("surfaces a render failure as an alert", () => {
    const w = mount(FluxDocsViewer, {
      props: { runtime: mockRuntime({ renderError: "render boom" }), data },
    });
    expect(w.get('[role="alert"]').text()).toContain("render boom");
  });
});

describe("FluxDocsViewer chrome (zoom, keyboard, a11y)", () => {
  let w: VueWrapper | null = null;
  afterEach(() => {
    w?.unmount();
    w = null;
    document.body.innerHTML = "";
  });

  function mountViewer(props: Record<string, unknown> = {}): VueWrapper {
    w = mount(FluxDocsViewer, {
      props: { runtime: mockRuntime(), data, ...props },
      attachTo: document.body,
    });
    return w;
  }

  const q = <T extends Element = HTMLElement>(sel: string) =>
    document.querySelector(sel) as T;
  const level = () => q(".fd-viewer__zoom-level").textContent;
  const info = () => q(".fd-viewer__page-info").textContent;
  const viewport = () => q(".fd-viewer__viewport");
  const btn = (name: string) =>
    q<HTMLButtonElement>(`button[aria-label="${name}"]`);
  const click = async (name: string) => {
    btn(name).click();
    await nextTick();
  };

  // press dispatches a keydown like fireEvent: returns false when prevented.
  async function press(el: Element, init: KeyboardEventInit): Promise<boolean> {
    const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
    el.dispatchEvent(ev);
    await nextTick();
    return !ev.defaultPrevented;
  }

  it("zooms through the ladder and disables the buttons at the limits", async () => {
    mountViewer();
    expect(level()).toBe("100%");
    await click("Zoom in");
    expect(level()).toBe("125%");
    await click("Zoom out");
    for (let i = 0; i < 5; i++) await click("Zoom out");
    expect(level()).toBe("25%");
    expect(btn("Zoom out").disabled).toBe(true);
    for (let i = 0; i < 10; i++) await click("Zoom in");
    expect(level()).toBe("400%");
    expect(btn("Zoom in").disabled).toBe(true);
    expect(btn("Zoom out").disabled).toBe(false);
  });

  it("re-renders at an integer DPI that scales with zoom, once per distinct DPI", async () => {
    const renderCalls: RenderCall[] = [];
    mountViewer({
      runtime: mockRuntime({ renderCalls }),
      renderOptions: { format: "png", skipAnnotations: true },
    });
    expect(renderCalls).toHaveLength(1);
    expect(renderCalls[0].opts).toEqual({ format: "png", skipAnnotations: true, dpi: 96 });
    await click("Zoom in");
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([96, 120]);
    // Reset at 100% from 125% renders at 96 again; a second reset is a no-op.
    await press(viewport(), { key: "0", ctrlKey: true });
    await press(viewport(), { key: "0", ctrlKey: true });
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([96, 120, 96]);
    for (const c of renderCalls) expect(Number.isInteger(c.opts?.dpi)).toBe(true);
  });

  it("uses renderOptions.dpi as the 100% sharpness", async () => {
    const renderCalls: RenderCall[] = [];
    mountViewer({ runtime: mockRuntime({ renderCalls }), renderOptions: { dpi: 150 } });
    await click("Zoom out");
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([150, 112]);
  });

  it("sizes the page box in CSS px from the page geometry and zoom", async () => {
    mountViewer();
    const page = q<HTMLDivElement>(".fd-viewer__page");
    expect(parseFloat(page.style.width)).toBeCloseTo(266.67, 2);
    expect(parseFloat(page.style.height)).toBeCloseTo(400, 2);
    await click("Zoom in");
    expect(parseFloat(page.style.width)).toBeCloseTo(333.33, 2);
    expect(page.querySelector("img.fd-viewer__canvas")).not.toBeNull();
  });

  it("falls back to the natural image size when pageSize fails, without an error", () => {
    const renderCalls: RenderCall[] = [];
    mountViewer({ runtime: mockRuntime({ renderCalls, pageSizeError: "bad box" }) });
    expect(q('[role="alert"]')).toBeNull();
    const page = q<HTMLDivElement>(".fd-viewer__page");
    expect(page.classList.contains("fd-viewer__page--natural")).toBe(true);
    expect(page.style.width).toBe("");
    expect(q(".fd-viewer__canvas").classList.contains("fd-viewer__canvas--natural")).toBe(true);
    expect(renderCalls[0].opts?.dpi).toBe(96);
  });

  it("refuses to render a page over the pixel budget even at 1 dpi", () => {
    const renderCalls: RenderCall[] = [];
    mountViewer({
      runtime: mockRuntime({ renderCalls, pageSize: { width: 1e18, height: 1e18, rotation: 0 } }),
    });
    expect(renderCalls).toHaveLength(0);
    expect(q('[role="alert"]').textContent).toContain("page is too large to render");
  });

  it("resets the vertical scroll when the document changes", async () => {
    const v = mountViewer();
    const vp = viewport();
    Object.defineProperty(vp, "scrollTop", { value: 50, writable: true, configurable: true });
    await v.setProps({ runtime: mockRuntime() });
    expect(viewport()).toBe(vp); // same element: the doc swapped in place
    expect(vp.scrollTop).toBe(0);
  });

  it("handles Ctrl/Cmd zoom keys inside the viewer and ignores them outside", async () => {
    mountViewer();
    expect(await press(viewport(), { key: "=", ctrlKey: true })).toBe(false);
    expect(level()).toBe("125%");
    expect(await press(viewport(), { key: "=", metaKey: true })).toBe(false);
    expect(level()).toBe("150%");
    expect(await press(btn("Zoom in"), { key: "-", ctrlKey: true })).toBe(false);
    expect(level()).toBe("125%");
    expect(await press(viewport(), { key: "0", ctrlKey: true })).toBe(false);
    expect(level()).toBe("100%");

    const outside = document.createElement("button");
    document.body.appendChild(outside);
    expect(await press(outside, { key: "=", ctrlKey: true })).toBe(true);
    expect(level()).toBe("100%");
    outside.remove();
  });

  it("still prevents Ctrl+= at the zoom limit", async () => {
    mountViewer({ initialZoom: 4 });
    expect(level()).toBe("400%");
    expect(await press(viewport(), { key: "=", ctrlKey: true })).toBe(false);
    expect(level()).toBe("400%");
  });

  it("pages with ArrowDown/ArrowUp, but not with modifiers or from an input", async () => {
    mountViewer();
    expect(await press(viewport(), { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
    await press(viewport(), { key: "ArrowUp" });
    expect(info()).toBe("Page 1 of 3");
    // At the first page ArrowUp is not handled and not prevented.
    expect(await press(viewport(), { key: "ArrowUp" })).toBe(true);
    expect(await press(viewport(), { key: "ArrowDown", shiftKey: true })).toBe(true);
    expect(info()).toBe("Page 1 of 3");

    const input = document.createElement("input");
    viewport().appendChild(input);
    expect(await press(input, { key: "ArrowDown" })).toBe(true);
    expect(await press(input, { key: "=", ctrlKey: true })).toBe(true);
    expect(info()).toBe("Page 1 of 3");
    expect(level()).toBe("100%");
    input.remove();

    // From a toolbar button, arrows always page.
    await press(btn("Zoom out"), { key: "ArrowDown" });
    expect(info()).toBe("Page 2 of 3");
  });

  it("emits page and zoom changes, not on the initial mount", async () => {
    const v = mountViewer();
    expect(v.emitted("pageChange")).toBeUndefined();
    expect(v.emitted("zoomChange")).toBeUndefined();
    await click("Next page");
    expect(v.emitted("pageChange")).toEqual([[1]]);
    await click("Zoom in");
    expect(v.emitted("zoomChange")).toEqual([[1.25]]);
    // No-ops do not fire.
    await press(viewport(), { key: "=", ctrlKey: true, altKey: true });
    expect(v.emitted("zoomChange")).toHaveLength(1);
    expect(v.emitted("pageChange")).toHaveLength(1);
  });

  it("honors initialPage/initialZoom and reports a clamp from a shorter document", async () => {
    const v = mountViewer({
      runtime: mockRuntime({ pageCount: 5 }),
      initialPage: 3,
      initialZoom: 10,
    });
    expect(info()).toBe("Page 4 of 5");
    expect(level()).toBe("400%");
    expect(v.emitted("pageChange")).toBeUndefined();
    await v.setProps({ runtime: mockRuntime({ pageCount: 2 }) });
    expect(info()).toBe("Page 2 of 2");
    expect(v.emitted("pageChange")).toEqual([[1]]);
  });

  it("clamps an out-of-range initialPage without reporting it", () => {
    const v = mountViewer({ initialPage: 9 });
    expect(info()).toBe("Page 3 of 3");
    expect(v.emitted("pageChange")).toBeUndefined();
  });

  it("moves focus to the viewport when the focused button becomes disabled", async () => {
    mountViewer();
    const next = btn("Next page");
    next.focus();
    await click("Next page");
    expect(document.activeElement).toBe(next);
    await click("Next page");
    expect(next.disabled).toBe(true);
    expect(document.activeElement).toBe(viewport());
  });

  it("exposes groups, live regions, and a focusable document region", () => {
    mountViewer();
    const groups = Array.from(document.querySelectorAll('[role="group"]')).map((g) =>
      g.getAttribute("aria-label"),
    );
    expect(groups).toEqual(["Page navigation", "Zoom"]);
    expect(q('[role="toolbar"]')).toBeNull();
    for (const sel of [".fd-viewer__page-info", ".fd-viewer__zoom-level"]) {
      const el = q(sel);
      expect(el.getAttribute("role")).toBe("status");
      expect(el.getAttribute("aria-live")).toBe("polite");
      expect(el.getAttribute("aria-atomic")).toBe("true");
    }
    const region = q('[role="region"]');
    expect(region).toBe(viewport());
    expect(region.getAttribute("aria-label")).toBe("Document");
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(document.querySelectorAll("img")).toHaveLength(1);
    expect(q("img").getAttribute("alt")).toBe("Page 1");
    const names = Array.from(document.querySelectorAll("button")).map((b) =>
      b.getAttribute("aria-label"),
    );
    expect(names).toEqual([
      "Previous page",
      "Next page",
      "Zoom out",
      "Zoom in",
      "Fit to width",
    ]);
    for (const b of document.querySelectorAll("button")) {
      expect(b.getAttribute("type")).toBe("button");
      expect(b.classList.contains("fd-viewer__btn")).toBe(true);
    }
    expect(btn("Zoom out").textContent).toBe("−");
    expect(btn("Fit to width").textContent).toBe("Fit");
  });

  it("fits the page to the viewport width, and is a no-op without a width", async () => {
    mountViewer();
    await click("Fit to width");
    expect(level()).toBe("100%"); // jsdom clientWidth is 0
    Object.defineProperty(viewport(), "clientWidth", { value: 400, configurable: true });
    await click("Fit to width");
    // (400 − 2 × 16 padding) / (200 pt × 96/72) = 1.38
    expect(level()).toBe("138%");
  });

  it("forwards data-fd-theme to the root element", () => {
    mountViewer({ "data-fd-theme": "dark" });
    expect(q(".fd-viewer").getAttribute("data-fd-theme")).toBe("dark");
  });
});

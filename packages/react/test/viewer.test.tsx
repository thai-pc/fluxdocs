import { StrictMode } from "react";
import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";

import { FluxDocsViewer } from "../src/index.js";
import { mockRuntime, type RenderCall } from "./mock.js";

afterEach(cleanup);

const data = new Uint8Array([1, 2, 3]);

describe("FluxDocsViewer", () => {
  it("renders the first page as an image with a data URL", () => {
    render(<FluxDocsViewer runtime={mockRuntime({ pageCount: 3 })} data={data} />);
    expect(screen.getByText("Page 1 of 3")).toBeDefined();
    const img = screen.getByRole("img") as HTMLImageElement;
    expect(img.src.startsWith("data:image/png;base64,")).toBe(true);
  });

  it("navigates between pages and clamps at the ends", () => {
    render(<FluxDocsViewer runtime={mockRuntime({ pageCount: 3 })} data={data} />);
    const prev = screen.getByLabelText("Previous page") as HTMLButtonElement;
    const next = screen.getByLabelText("Next page") as HTMLButtonElement;

    expect(prev.disabled).toBe(true); // on the first page
    fireEvent.click(next);
    expect(screen.getByText("Page 2 of 3")).toBeDefined();
    fireEvent.click(next);
    expect(screen.getByText("Page 3 of 3")).toBeDefined();
    expect(next.disabled).toBe(true); // on the last page
    fireEvent.click(prev);
    expect(screen.getByText("Page 2 of 3")).toBeDefined();
  });

  it("shows an empty state without a document", () => {
    render(<FluxDocsViewer runtime={null} data={null} />);
    expect(screen.getByText("No document loaded.")).toBeDefined();
  });

  it("surfaces an open failure as an alert", () => {
    render(
      <FluxDocsViewer runtime={mockRuntime({ openError: "bad pdf" })} data={data} />,
    );
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("bad pdf");
  });

  it("surfaces a render failure as an alert", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderError: "render boom" })}
        data={data}
      />,
    );
    expect(screen.getByRole("alert").textContent).toContain("render boom");
  });
});

describe("FluxDocsViewer error codes", () => {
  const alertText = () => screen.getByRole("alert").textContent;
  const errorP = () => document.querySelector(".fd-viewer__error");

  it("shows the too-complex text for budget_exceeded", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderError: "renderPage: over budget", renderErrorCode: "budget_exceeded" })}
        data={data}
      />,
    );
    expect(alertText()).toBe("This page is too complex to display.");
    expect(errorP()?.textContent).toBe("This page is too complex to display.");
    expect(screen.getByRole("alert").classList.contains("fd-viewer")).toBe(true);
    expect(alertText()).not.toContain("over budget");
  });

  it("shows the pixel-budget pre-check's too-large text for canvas_too_large", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderError: "renderPage: canvas exceeds cap", renderErrorCode: "canvas_too_large" })}
        data={data}
      />,
    );
    expect(alertText()).toBe("Failed to display PDF: page is too large to render");
    expect(errorP()?.textContent).toBe("Failed to display PDF: page is too large to render");
    expect(screen.getByRole("alert").classList.contains("fd-viewer")).toBe(true);
    expect(alertText()).not.toContain("exceeds cap");
  });

  it("shows the took-too-long text for timeout", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderError: "renderPage: deadline", renderErrorCode: "timeout" })}
        data={data}
      />,
    );
    expect(alertText()).toBe("Rendering this page took too long.");
    expect(errorP()?.textContent).toBe("Rendering this page took too long.");
  });

  it.each(["unknown", "internal", "page_not_found"] as const)(
    "falls back to the raw message for %s",
    (code) => {
      render(
        <FluxDocsViewer
          runtime={mockRuntime({ renderError: "renderPage: raw text", renderErrorCode: code })}
          data={data}
        />,
      );
      expect(alertText()).toBe("Failed to display PDF: renderPage: raw text");
    },
  );

  it("keeps the raw message for a coded open failure", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ openError: "open: bad xref", openErrorCode: "invalid_pdf" })}
        data={data}
      />,
    );
    expect(alertText()).toBe("Failed to display PDF: open: bad xref");
  });

  it("keeps the client-side pixel-budget message", () => {
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ pageSize: { width: 1e18, height: 1e18, rotation: 0 } })}
        data={data}
      />,
    );
    expect(alertText()).toBe("Failed to display PDF: page is too large to render");
  });
});

describe("FluxDocsViewer chrome (zoom, keyboard, a11y)", () => {
  const q = <T extends Element = HTMLElement>(sel: string) =>
    document.querySelector(sel) as T;
  const level = () => q(".fd-viewer__zoom-level").textContent;
  const info = () => q(".fd-viewer__page-info").textContent;
  const viewport = () => q(".fd-viewer__viewport");
  const btn = (name: string) => screen.getByLabelText(name) as HTMLButtonElement;

  it("zooms through the ladder and disables the buttons at the limits", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    expect(level()).toBe("100%");
    fireEvent.click(btn("Zoom in"));
    expect(level()).toBe("125%");
    fireEvent.click(btn("Zoom out"));
    for (let i = 0; i < 5; i++) fireEvent.click(btn("Zoom out"));
    expect(level()).toBe("25%");
    expect(btn("Zoom out").disabled).toBe(true);
    for (let i = 0; i < 10; i++) fireEvent.click(btn("Zoom in"));
    expect(level()).toBe("400%");
    expect(btn("Zoom in").disabled).toBe(true);
    expect(btn("Zoom out").disabled).toBe(false);
  });

  it("renders each page/zoom exactly once under StrictMode", () => {
    const renderCalls: RenderCall[] = [];
    render(
      <StrictMode>
        <FluxDocsViewer runtime={mockRuntime({ renderCalls })} data={data} />
      </StrictMode>,
    );
    const keys = () => renderCalls.map((c) => `${c.index}@${c.opts?.dpi}`);
    expect(keys()).toEqual(["0@96"]);
    fireEvent.click(btn("Zoom in"));
    expect(keys()).toEqual(["0@96", "0@120"]);
    fireEvent.click(btn("Next page"));
    expect(keys()).toEqual(["0@96", "0@120", "1@120"]);
    expect(q<HTMLImageElement>("img.fd-viewer__canvas").alt).toBe("Page 2");
  });

  it("re-renders at an integer DPI that scales with zoom, once per distinct DPI", () => {
    const renderCalls: RenderCall[] = [];
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderCalls })}
        data={data}
        renderOptions={{ format: "png", skipAnnotations: true }}
      />,
    );
    expect(renderCalls).toHaveLength(1);
    expect(renderCalls[0].opts).toEqual({ format: "png", skipAnnotations: true, dpi: 96 });
    fireEvent.click(btn("Zoom in"));
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([96, 120]);
    // Reset at 100% from 125% renders at 96 again; a second reset is a no-op.
    fireEvent.keyDown(viewport(), { key: "0", ctrlKey: true });
    fireEvent.keyDown(viewport(), { key: "0", ctrlKey: true });
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([96, 120, 96]);
    for (const c of renderCalls) expect(Number.isInteger(c.opts?.dpi)).toBe(true);
  });

  it("uses renderOptions.dpi as the 100% sharpness", () => {
    const renderCalls: RenderCall[] = [];
    render(
      <FluxDocsViewer runtime={mockRuntime({ renderCalls })} data={data} renderOptions={{ dpi: 150 }} />,
    );
    fireEvent.click(btn("Zoom out"));
    expect(renderCalls.map((c) => c.opts?.dpi)).toEqual([150, 112]);
  });

  it("sizes the page box in CSS px from the page geometry and zoom", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    const page = q<HTMLDivElement>(".fd-viewer__page");
    expect(parseFloat(page.style.width)).toBeCloseTo(266.67, 2);
    expect(parseFloat(page.style.height)).toBeCloseTo(400, 2);
    fireEvent.click(btn("Zoom in"));
    expect(parseFloat(page.style.width)).toBeCloseTo(333.33, 2);
    expect(page.querySelector("img.fd-viewer__canvas")).not.toBeNull();
  });

  it("falls back to the natural image size when pageSize fails, without an error", () => {
    const renderCalls: RenderCall[] = [];
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderCalls, pageSizeError: "bad box" })}
        data={data}
      />,
    );
    expect(screen.queryByRole("alert")).toBeNull();
    const page = q<HTMLDivElement>(".fd-viewer__page");
    expect(page.classList.contains("fd-viewer__page--natural")).toBe(true);
    expect(page.style.width).toBe("");
    expect(q(".fd-viewer__canvas").classList.contains("fd-viewer__canvas--natural")).toBe(true);
    expect(renderCalls[0].opts?.dpi).toBe(96);
  });

  it("refuses to render a page over the pixel budget even at 1 dpi", () => {
    const renderCalls: RenderCall[] = [];
    render(
      <FluxDocsViewer
        runtime={mockRuntime({ renderCalls, pageSize: { width: 1e18, height: 1e18, rotation: 0 } })}
        data={data}
      />,
    );
    expect(renderCalls).toHaveLength(0);
    expect(screen.getByRole("alert").textContent).toContain("page is too large to render");
  });

  it("resets the vertical scroll when the document changes", () => {
    const { rerender } = render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    const vp = viewport();
    Object.defineProperty(vp, "scrollTop", { value: 50, writable: true, configurable: true });
    rerender(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    expect(viewport()).toBe(vp); // same element: the doc swapped in place
    expect(vp.scrollTop).toBe(0);
  });

  it("handles Ctrl/Cmd zoom keys inside the viewer and ignores them outside", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    // fireEvent returns false when the event was default-prevented.
    expect(fireEvent.keyDown(viewport(), { key: "=", ctrlKey: true })).toBe(false);
    expect(level()).toBe("125%");
    expect(fireEvent.keyDown(viewport(), { key: "=", metaKey: true })).toBe(false);
    expect(level()).toBe("150%");
    expect(fireEvent.keyDown(btn("Zoom in"), { key: "-", ctrlKey: true })).toBe(false);
    expect(level()).toBe("125%");
    expect(fireEvent.keyDown(viewport(), { key: "0", ctrlKey: true })).toBe(false);
    expect(level()).toBe("100%");

    const outside = document.createElement("button");
    document.body.appendChild(outside);
    expect(fireEvent.keyDown(outside, { key: "=", ctrlKey: true })).toBe(true);
    expect(level()).toBe("100%");
    outside.remove();
  });

  it("still prevents Ctrl+= at the zoom limit", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} initialZoom={4} />);
    expect(level()).toBe("400%");
    expect(fireEvent.keyDown(viewport(), { key: "=", ctrlKey: true })).toBe(false);
    expect(level()).toBe("400%");
  });

  it("pages with ArrowDown/ArrowUp, but not with modifiers or from an input", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    expect(fireEvent.keyDown(viewport(), { key: "ArrowDown" })).toBe(false);
    expect(info()).toBe("Page 2 of 3");
    fireEvent.keyDown(viewport(), { key: "ArrowUp" });
    expect(info()).toBe("Page 1 of 3");
    // At the first page ArrowUp is not handled and not prevented.
    expect(fireEvent.keyDown(viewport(), { key: "ArrowUp" })).toBe(true);
    expect(fireEvent.keyDown(viewport(), { key: "ArrowDown", shiftKey: true })).toBe(true);
    expect(info()).toBe("Page 1 of 3");

    const input = document.createElement("input");
    viewport().appendChild(input);
    expect(fireEvent.keyDown(input, { key: "ArrowDown" })).toBe(true);
    expect(fireEvent.keyDown(input, { key: "=", ctrlKey: true })).toBe(true);
    expect(info()).toBe("Page 1 of 3");
    expect(level()).toBe("100%");
    input.remove();

    // From a toolbar button, arrows always page.
    fireEvent.keyDown(btn("Zoom out"), { key: "ArrowDown" });
    expect(info()).toBe("Page 2 of 3");
  });

  it("reports page and zoom changes, not the initial mount", () => {
    const onPageChange = vi.fn();
    const onZoomChange = vi.fn();
    render(
      <FluxDocsViewer
        runtime={mockRuntime()}
        data={data}
        onPageChange={onPageChange}
        onZoomChange={onZoomChange}
      />,
    );
    expect(onPageChange).not.toHaveBeenCalled();
    expect(onZoomChange).not.toHaveBeenCalled();
    fireEvent.click(btn("Next page"));
    expect(onPageChange).toHaveBeenCalledTimes(1);
    expect(onPageChange).toHaveBeenLastCalledWith(1);
    fireEvent.click(btn("Zoom in"));
    expect(onZoomChange).toHaveBeenCalledTimes(1);
    expect(onZoomChange).toHaveBeenLastCalledWith(1.25);
    // No-ops do not fire.
    fireEvent.keyDown(viewport(), { key: "=", ctrlKey: true, altKey: true });
    expect(onZoomChange).toHaveBeenCalledTimes(1);
    expect(onPageChange).toHaveBeenCalledTimes(1);
  });

  it("honors initialPage/initialZoom and reports a clamp from a shorter document", () => {
    const onPageChange = vi.fn();
    const { rerender } = render(
      <FluxDocsViewer
        runtime={mockRuntime({ pageCount: 5 })}
        data={data}
        initialPage={3}
        initialZoom={10}
        onPageChange={onPageChange}
      />,
    );
    expect(info()).toBe("Page 4 of 5");
    expect(level()).toBe("400%");
    expect(onPageChange).not.toHaveBeenCalled();
    rerender(
      <FluxDocsViewer
        runtime={mockRuntime({ pageCount: 2 })}
        data={data}
        initialPage={3}
        onPageChange={onPageChange}
      />,
    );
    expect(info()).toBe("Page 2 of 2");
    expect(onPageChange).toHaveBeenCalledTimes(1);
    expect(onPageChange).toHaveBeenLastCalledWith(1);
  });

  it("clamps an out-of-range initialPage without reporting it", () => {
    const onPageChange = vi.fn();
    render(
      <FluxDocsViewer runtime={mockRuntime()} data={data} initialPage={9} onPageChange={onPageChange} />,
    );
    expect(info()).toBe("Page 3 of 3");
    expect(onPageChange).not.toHaveBeenCalled();
  });

  it("moves focus to the viewport when the focused button becomes disabled", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    const next = btn("Next page");
    next.focus();
    fireEvent.click(next);
    expect(document.activeElement).toBe(next);
    fireEvent.click(next);
    expect(next.disabled).toBe(true);
    expect(document.activeElement).toBe(viewport());
  });

  it("exposes groups, live regions, and a focusable document region", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    expect(screen.getByRole("group", { name: "Page navigation" })).toBeDefined();
    expect(screen.getByRole("group", { name: "Zoom" })).toBeDefined();
    expect(screen.queryByRole("toolbar")).toBeNull();
    for (const sel of [".fd-viewer__page-info", ".fd-viewer__zoom-level"]) {
      const el = q(sel);
      expect(el.getAttribute("role")).toBe("status");
      expect(el.getAttribute("aria-live")).toBe("polite");
      expect(el.getAttribute("aria-atomic")).toBe("true");
    }
    const region = screen.getByRole("region", { name: "Document" });
    expect(region).toBe(viewport());
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(screen.getAllByRole("img")).toHaveLength(1);
    expect(screen.getByRole("img").getAttribute("alt")).toBe("Page 1");
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

  it("fits the page to the viewport width, and is a no-op without a width", () => {
    render(<FluxDocsViewer runtime={mockRuntime()} data={data} />);
    fireEvent.click(btn("Fit to width"));
    expect(level()).toBe("100%"); // jsdom clientWidth is 0
    Object.defineProperty(viewport(), "clientWidth", { value: 400, configurable: true });
    fireEvent.click(btn("Fit to width"));
    // (400 − 2 × 16 padding) / (200 pt × 96/72) = 1.38
    expect(level()).toBe("138%");
  });
});

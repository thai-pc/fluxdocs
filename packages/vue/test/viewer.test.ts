import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";

import { FluxDocsViewer } from "../src/index.js";
import { mockRuntime } from "./mock.js";

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

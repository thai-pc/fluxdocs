// @vitest-environment node
import { renderToString } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { FluxDoc } from "@fluxdocs/core";

import { usePageImage } from "../src/hooks.js";
import { mockRuntime, type RenderCall } from "./mock.js";

describe("usePageImage (server)", () => {
  it("returns a null src, never rasterizes, and does not warn during SSR", () => {
    const warn = vi.spyOn(console, "error").mockImplementation(() => {});
    const calls: RenderCall[] = [];
    const doc = mockRuntime({ renderCalls: calls }).open(new Uint8Array([1]));
    function Img({ d }: { d: FluxDoc }) {
      const { src } = usePageImage(d, 0, { dpi: 72 });
      return <span>{src === null ? "null" : "set"}</span>;
    }
    expect(renderToString(<Img d={doc} />)).toContain("null");
    expect(calls.length).toBe(0);
    // useLayoutEffect on the server logs a hydration-mismatch warning.
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});

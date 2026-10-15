import { StrictMode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import type { FluxDoc } from "@fluxdocs/core";

import { usePageImage } from "../src/hooks.js";
import { mockRuntime, type RenderCall } from "./mock.js";

afterEach(cleanup);

function Img({ doc }: { doc: FluxDoc }) {
  const { src } = usePageImage(doc, 0, { dpi: 72 });
  return <span>{src ? "y" : "n"}</span>;
}

describe("usePageImage", () => {
  it("renders once under StrictMode when mounted with a doc", () => {
    const calls: RenderCall[] = [];
    const doc = mockRuntime({ renderCalls: calls }).open(new Uint8Array([1]));
    const { container } = render(
      <StrictMode>
        <Img doc={doc} />
      </StrictMode>,
    );
    expect(calls.length).toBe(1);
    expect(container.textContent).toBe("y");
  });

  it("never returns a result from a previous doc", () => {
    const a = mockRuntime().open(new Uint8Array([1]));
    const b = mockRuntime({ renderError: "boom" }).open(new Uint8Array([2]));
    const seen: string[] = [];
    function Probe({ doc }: { doc: FluxDoc }) {
      const r = usePageImage(doc, 0, { dpi: 72 });
      seen.push(r.src ? "src" : r.error ? "err" : "none");
      return null;
    }
    const { rerender } = render(<Probe doc={a} />);
    expect(seen.at(-1)).toBe("src");
    seen.length = 0;
    rerender(<Probe doc={b} />);
    // The first render with doc b must not show doc a's image; the layout
    // effect then surfaces b's own render failure.
    expect(seen[0]).toBe("none");
    expect(seen.at(-1)).toBe("err");
  });
});

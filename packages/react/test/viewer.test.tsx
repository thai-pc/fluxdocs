import { describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";

import { FluxDocsViewer } from "../src/index.js";
import { mockRuntime } from "./mock.js";

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

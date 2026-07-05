import { describe, expect, it } from "vitest";

import { bytesToDataURL, mimeFor } from "../src/dataurl.js";

describe("bytesToDataURL", () => {
  it("encodes bytes as a base64 data URL", () => {
    const url = bytesToDataURL(new Uint8Array([0x89, 0x50, 0x4e, 0x47]));
    expect(url.startsWith("data:image/png;base64,")).toBe(true);
    const b64 = url.split(",")[1];
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    expect(Array.from(bytes)).toEqual([0x89, 0x50, 0x4e, 0x47]);
  });

  it("honors the mime argument", () => {
    expect(bytesToDataURL(new Uint8Array([1]), "image/jpeg")).toContain(
      "data:image/jpeg;base64,",
    );
  });

  it("handles a large buffer without overflowing", () => {
    const big = new Uint8Array(200_000).fill(65);
    expect(() => bytesToDataURL(big)).not.toThrow();
  });
});

describe("mimeFor", () => {
  it("maps format to a MIME type", () => {
    expect(mimeFor({ format: "jpeg" })).toBe("image/jpeg");
    expect(mimeFor({ format: "png" })).toBe("image/png");
    expect(mimeFor()).toBe("image/png");
  });
});

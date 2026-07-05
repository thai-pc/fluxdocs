import type { RenderOptions } from "@fluxdocs/core";

/**
 * bytesToDataURL encodes raw image bytes as a `data:` URL usable as an <img>
 * src. Chunked base64 avoids a call-stack overflow on large buffers. Pure —
 * works in the browser, Node, and jsdom (uses the global btoa).
 */
export function bytesToDataURL(bytes: Uint8Array, mime = "image/png"): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return `data:${mime};base64,${btoa(binary)}`;
}

/** mimeFor maps a render format to its image MIME type (default PNG). */
export function mimeFor(opts?: RenderOptions): string {
  return opts?.format === "jpeg" ? "image/jpeg" : "image/png";
}

import { useEffect, useMemo, useState } from "react";
import type { FluxDoc, FluxDocs, RenderOptions } from "@fluxdocs/core";

import { bytesToDataURL, mimeFor } from "./dataurl.js";

/**
 * useFluxDoc opens a PDF from `data` using `runtime` and manages its lifetime:
 * the document is closed automatically when the inputs change or the component
 * unmounts. Returns null doc until both inputs are provided; surfaces a parse
 * failure via `error`.
 */
export function useFluxDoc(
  runtime: FluxDocs | null | undefined,
  data: Uint8Array | null | undefined,
): { doc: FluxDoc | null; error: Error | null } {
  const [doc, setDoc] = useState<FluxDoc | null>(null);
  const [error, setError] = useState<Error | null>(null);

  useEffect(() => {
    if (!runtime || !data) {
      setDoc(null);
      setError(null);
      return;
    }
    let opened: FluxDoc | null = null;
    try {
      opened = runtime.open(data);
      setDoc(opened);
      setError(null);
    } catch (e) {
      setDoc(null);
      setError(e as Error);
    }
    return () => opened?.close();
  }, [runtime, data]);

  return { doc, error };
}

/**
 * usePageImage renders one page of `doc` to a data URL suitable for an <img>
 * src. It re-renders when the document, page index, or options change. Returns
 * null src when there is no document, and surfaces a render failure via `error`.
 */
export function usePageImage(
  doc: FluxDoc | null,
  pageIndex: number,
  opts?: RenderOptions,
): { src: string | null; error: Error | null } {
  // Serialize opts so the memo is stable across equal-but-new objects.
  const optsKey = JSON.stringify(opts ?? {});
  return useMemo(() => {
    if (!doc) {
      return { src: null, error: null };
    }
    try {
      const bytes = doc.renderPage(pageIndex, opts);
      return { src: bytesToDataURL(bytes, mimeFor(opts)), error: null };
    } catch (e) {
      return { src: null, error: e as Error };
    }
    // opts is captured via optsKey; pageIndex and doc are the other deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, pageIndex, optsKey]);
}

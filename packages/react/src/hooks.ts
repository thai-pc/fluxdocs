import { useEffect, useLayoutEffect, useRef, useState } from "react";
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

// useLayoutEffect warns during server rendering; fall back to useEffect where
// there is no window (no render happens server-side either way).
export const useIsoLayoutEffect = typeof window !== "undefined" ? useLayoutEffect : useEffect;

type PageImage = { src: string | null; error: Error | null };

const NO_IMAGE: PageImage = { src: null, error: null };

/** One completed render: its inputs and its result. */
interface Rendered {
  doc: FluxDoc;
  pageIndex: number;
  optsKey: string;
  result: PageImage;
}

/**
 * usePageImage renders one page of `doc` to a data URL suitable for an <img>
 * src. It re-renders when the document, page index, or options change. Returns
 * null src when there is no document, and surfaces a render failure via `error`.
 *
 * The render runs in a layout effect, not during render, so React StrictMode's
 * double render cannot rasterize a page twice; a ref remembers the last
 * (doc, page, options) rendered so StrictMode's effect re-run reuses it too.
 * The layout effect commits the new image before the browser paints. Until a
 * render for the current document has completed, src is null.
 */
export function usePageImage(
  doc: FluxDoc | null,
  pageIndex: number,
  opts?: RenderOptions,
): PageImage {
  // Serialize opts so equal-but-new objects do not trigger another render.
  const optsKey = JSON.stringify(opts ?? {});
  const last = useRef<Rendered | null>(null);
  const [rendered, setRendered] = useState<Rendered | null>(null);

  useIsoLayoutEffect(() => {
    if (!doc) {
      return;
    }
    const prev = last.current;
    if (prev && prev.doc === doc && prev.pageIndex === pageIndex && prev.optsKey === optsKey) {
      setRendered(prev); // StrictMode effect re-run: same inputs, reuse
      return;
    }
    let result: PageImage;
    try {
      const bytes = doc.renderPage(pageIndex, opts);
      result = { src: bytesToDataURL(bytes, mimeFor(opts)), error: null };
    } catch (e) {
      result = { src: null, error: e as Error };
    }
    const next = { doc, pageIndex, optsKey, result };
    last.current = next;
    setRendered(next);
    // opts is captured via optsKey; pageIndex and doc are the other deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, pageIndex, optsKey]);

  // A result from another document is never shown. A result for an earlier
  // page/options of the same document is kept for the one commit before the
  // layout effect above replaces it (not painted).
  if (!doc || !rendered || rendered.doc !== doc) {
    return NO_IMAGE;
  }
  return rendered.result;
}

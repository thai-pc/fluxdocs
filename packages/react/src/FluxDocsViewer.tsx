import { useEffect, useState } from "react";
import type { FluxDocs, RenderOptions } from "@fluxdocs/core";

import { useFluxDoc, usePageImage } from "./hooks.js";

export interface FluxDocsViewerProps {
  /** A loaded runtime from `loadFluxDocs`. */
  runtime: FluxDocs | null | undefined;
  /** The PDF bytes to view. */
  data: Uint8Array | null | undefined;
  /** The page shown first (0-based, clamped); defaults to 0. */
  initialPage?: number;
  /** Render options passed to each page render. */
  renderOptions?: RenderOptions;
  /** Extra class on the root element (always carries `fd-viewer`). */
  className?: string;
}

/**
 * FluxDocsViewer is a minimal, dependency-light page viewer: it opens the PDF,
 * renders the current page client-side (no server round-trip), and offers
 * prev/next navigation. Styling hooks use the `fd-` BEM prefix so a host app can
 * theme them without collisions.
 */
export function FluxDocsViewer({
  runtime,
  data,
  initialPage = 0,
  renderOptions,
  className,
}: FluxDocsViewerProps): JSX.Element {
  const { doc, error: openError } = useFluxDoc(runtime, data);
  const [page, setPage] = useState(initialPage);

  const pageCount = doc?.pageCount ?? 0;

  // Keep the current page within range as documents load or change.
  useEffect(() => {
    setPage((p) => clamp(p, 0, Math.max(0, pageCount - 1)));
  }, [pageCount]);

  const { src, error: renderError } = usePageImage(doc, page, renderOptions);
  const error = openError ?? renderError;

  const root = className ? `fd-viewer ${className}` : "fd-viewer";

  if (error) {
    return (
      <div className={root} role="alert">
        <p className="fd-viewer__error">Failed to display PDF: {error.message}</p>
      </div>
    );
  }

  if (!doc) {
    return (
      <div className={root}>
        <p className="fd-viewer__empty">No document loaded.</p>
      </div>
    );
  }

  return (
    <div className={root}>
      <div className="fd-viewer__toolbar">
        <button
          type="button"
          className="fd-viewer__nav fd-viewer__nav--prev"
          onClick={() => setPage((p) => clamp(p - 1, 0, pageCount - 1))}
          disabled={page <= 0}
          aria-label="Previous page"
        >
          ‹
        </button>
        <span className="fd-viewer__page-info">
          Page {page + 1} of {pageCount}
        </span>
        <button
          type="button"
          className="fd-viewer__nav fd-viewer__nav--next"
          onClick={() => setPage((p) => clamp(p + 1, 0, pageCount - 1))}
          disabled={page >= pageCount - 1}
          aria-label="Next page"
        >
          ›
        </button>
      </div>
      {src && (
        <img
          className="fd-viewer__canvas"
          src={src}
          alt={`Page ${page + 1}`}
        />
      )}
    </div>
  );
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}

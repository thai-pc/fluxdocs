import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import type { FluxDocs, RenderOptions } from "@fluxdocs/core";

import { useFluxDoc, useIsoLayoutEffect, usePageImage } from "./hooks.js";
import {
  canZoomIn,
  canZoomOut,
  centerRatio,
  clampPage,
  clampZoom,
  classifyKey,
  currentDpr,
  displaySize,
  errorText,
  fitZoom,
  fitsBudget,
  keyContext,
  PAGE_TOO_LARGE_MESSAGE,
  parsePadding,
  renderDpi,
  safePageSize,
  sanitizePage,
  scrollForRatio,
  shouldRescueFocus,
  zoomIn,
  zoomLabel,
  zoomOut,
  type KeyAction,
} from "./viewer-model.js";

export interface FluxDocsViewerProps {
  /** A loaded runtime from `loadFluxDocs`. */
  runtime: FluxDocs | null | undefined;
  /** The PDF bytes to view. */
  data: Uint8Array | null | undefined;
  /** The page shown first (0-based, clamped); defaults to 0. */
  initialPage?: number;
  /**
   * The zoom factor shown first (1 = 100% = true physical size), clamped to
   * 0.25–4; defaults to 1.
   */
  initialZoom?: number;
  /**
   * Render options passed to each page render. `dpi` is the sharpness at 100%
   * (default: 96 × devicePixelRatio, capped at 2×); the displayed size comes
   * from the zoom, and the viewer re-renders at a zoom-matched DPI.
   */
  renderOptions?: RenderOptions;
  /** Extra class on the root element (always carries `fd-viewer`). */
  className?: string;
  /** Called with the new 0-based page index after every page change (not on mount). */
  onPageChange?: (pageIndex: number) => void;
  /** Called with the new zoom factor after every zoom change (not on mount). */
  onZoomChange?: (zoom: number) => void;
}

/**
 * FluxDocsViewer is a dependency-light page viewer: it opens the PDF, renders
 * the current page client-side (no server round-trip), and offers page
 * navigation, zoom (buttons and Ctrl/Cmd + + - 0), and Arrow-key paging while
 * focus is inside the viewer. Styling hooks use the `fd-` BEM prefix; import
 * `@fluxdocs/core/styles.css` for the base look.
 */
export function FluxDocsViewer({
  runtime,
  data,
  initialPage = 0,
  initialZoom = 1,
  renderOptions,
  className,
  onPageChange,
  onZoomChange,
}: FluxDocsViewerProps): JSX.Element {
  const { doc, error: openError } = useFluxDoc(runtime, data);
  const [rawPage, setRawPage] = useState(() => sanitizePage(initialPage));
  const [zoom, setZoom] = useState(() => clampZoom(initialZoom));

  const rootRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  // Viewport center (as content ratios) captured before a zoom change.
  const pendingCenter = useRef<{ x: number; y: number } | null>(null);
  // Whether focus was inside the viewer when a page/zoom change started.
  const focusWasInside = useRef(false);
  // Where the next page change lands: the bottom after an ArrowUp edge-rule
  // page back (keeps reading upward), otherwise the top.
  const landAtBottom = useRef(false);

  const pageCount = doc?.pageCount ?? 0;
  // The effective page is clamped during render so the first frame with a
  // document already shows (and reports) an in-range page.
  const page = pageCount > 0 ? clampPage(rawPage, pageCount) : rawPage;

  // Write the clamp back so a later, longer document does not jump pages.
  useEffect(() => {
    if (pageCount > 0) {
      setRawPage((p) => clampPage(p, pageCount));
    }
  }, [pageCount]);

  const size = useMemo(() => safePageSize(doc, page), [doc, page]);
  const box = displaySize(size, zoom);
  const dpi = renderDpi(size, zoom, renderOptions?.dpi, currentDpr());
  const opts: RenderOptions = { ...renderOptions, dpi };

  // A page over the pixel budget even at 1 dpi is never sent to the core.
  const sizeError = useMemo(
    () => (fitsBudget(size) ? null : new Error(PAGE_TOO_LARGE_MESSAGE)),
    [size],
  );
  const { src, error: renderError } = usePageImage(sizeError ? null : doc, page, opts);
  const error = openError ?? sizeError ?? renderError;

  // Report changes (never the initial mount) through the latest callbacks.
  const onPageChangeRef = useRef(onPageChange);
  const onZoomChangeRef = useRef(onZoomChange);
  onPageChangeRef.current = onPageChange;
  onZoomChangeRef.current = onZoomChange;
  const reportedPage = useRef<number | null>(null);
  const reportedZoom = useRef(zoom);

  useEffect(() => {
    if (!doc) {
      return;
    }
    if (reportedPage.current === null) {
      reportedPage.current = page; // baseline: the first page shown
      return;
    }
    if (reportedPage.current !== page) {
      reportedPage.current = page;
      onPageChangeRef.current?.(page);
    }
  }, [doc, page]);

  useEffect(() => {
    if (reportedZoom.current !== zoom) {
      reportedZoom.current = zoom;
      onZoomChangeRef.current?.(zoom);
    }
  }, [zoom]);

  // Page or document change: start at the top of the page, or at the bottom
  // after an ArrowUp page back from inside the viewport (keep horizontal scroll).
  useIsoLayoutEffect(() => {
    const vp = viewportRef.current;
    const bottom = landAtBottom.current;
    landAtBottom.current = false;
    if (vp) {
      vp.scrollTop = bottom ? Math.max(0, vp.scrollHeight - vp.clientHeight) : 0;
    }
  }, [doc, page]);

  // Zoom change: keep the viewport center at the same relative position.
  useIsoLayoutEffect(() => {
    const vp = viewportRef.current;
    const c = pendingCenter.current;
    pendingCenter.current = null;
    if (!vp || !c) {
      return;
    }
    vp.scrollLeft = scrollForRatio(c.x, vp.clientWidth, vp.scrollWidth);
    vp.scrollTop = scrollForRatio(c.y, vp.clientHeight, vp.scrollHeight);
  }, [zoom]);

  // Focus retention: a focused button that became disabled loses focus to
  // <body>, which would also detach the keyboard handler. Move it to the viewport.
  useIsoLayoutEffect(() => {
    const was = focusWasInside.current;
    focusWasInside.current = false;
    if (shouldRescueFocus(rootRef.current, was)) {
      viewportRef.current?.focus({ preventScroll: true });
    }
  }, [page, zoom]);

  const root = className ? `fd-viewer ${className}` : "fd-viewer";

  if (error) {
    return (
      <div className={root} role="alert">
        <p className="fd-viewer__error">{errorText(error)}</p>
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

  const noteFocus = () => {
    const active = typeof document !== "undefined" ? document.activeElement : null;
    focusWasInside.current = !!active && !!rootRef.current?.contains(active);
  };

  const goToPage = (next: number, atBottom = false) => {
    const p = clampPage(next, pageCount);
    if (p === page) {
      return;
    }
    noteFocus();
    landAtBottom.current = atBottom;
    setRawPage(p);
  };

  const changeZoom = (next: number) => {
    const z = clampZoom(next);
    if (z === zoom) {
      return;
    }
    const vp = viewportRef.current;
    if (vp) {
      pendingCenter.current = {
        x: centerRatio(vp.scrollLeft, vp.clientWidth, vp.scrollWidth),
        y: centerRatio(vp.scrollTop, vp.clientHeight, vp.scrollHeight),
      };
    }
    noteFocus();
    setZoom(z);
  };

  const fitToWidth = () => {
    const vp = viewportRef.current;
    if (!vp) {
      return;
    }
    const padding = parsePadding(getComputedStyle(vp).paddingLeft);
    changeZoom(fitZoom(vp.clientWidth, padding, size, zoom));
  };

  const run = (action: KeyAction, inViewport: boolean) => {
    switch (action) {
      case "nextPage":
        return goToPage(page + 1);
      case "prevPage":
        return goToPage(page - 1, inViewport);
      case "zoomIn":
        return changeZoom(zoomIn(zoom));
      case "zoomOut":
        return changeZoom(zoomOut(zoom));
      case "zoomReset":
        return changeZoom(1);
    }
  };

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const ctx = keyContext(e.target, viewportRef.current, page > 0, page < pageCount - 1);
    const decision = classifyKey(e.nativeEvent, ctx);
    if (decision.preventDefault) {
      e.preventDefault();
    }
    if (decision.action) {
      run(decision.action, ctx.inViewport);
    }
  };

  const pageStyle = box ? { width: `${box.width}px`, height: `${box.height}px` } : undefined;

  return (
    <div className={root} ref={rootRef} onKeyDown={onKeyDown}>
      <div className="fd-viewer__toolbar">
        <div
          className="fd-viewer__group fd-viewer__group--nav"
          role="group"
          aria-label="Page navigation"
        >
          <button
            type="button"
            className="fd-viewer__btn fd-viewer__nav fd-viewer__nav--prev"
            onClick={() => goToPage(page - 1)}
            disabled={page <= 0}
            aria-label="Previous page"
          >
            ‹
          </button>
          <span
            className="fd-viewer__page-info"
            role="status"
            aria-live="polite"
            aria-atomic="true"
          >
            Page {page + 1} of {pageCount}
          </span>
          <button
            type="button"
            className="fd-viewer__btn fd-viewer__nav fd-viewer__nav--next"
            onClick={() => goToPage(page + 1)}
            disabled={page >= pageCount - 1}
            aria-label="Next page"
          >
            ›
          </button>
        </div>
        <div
          className="fd-viewer__group fd-viewer__group--zoom"
          role="group"
          aria-label="Zoom"
        >
          <button
            type="button"
            className="fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--out"
            onClick={() => changeZoom(zoomOut(zoom))}
            disabled={!canZoomOut(zoom)}
            aria-label="Zoom out"
          >
            −
          </button>
          <span
            className="fd-viewer__zoom-level"
            role="status"
            aria-live="polite"
            aria-atomic="true"
          >
            {zoomLabel(zoom)}
          </span>
          <button
            type="button"
            className="fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--in"
            onClick={() => changeZoom(zoomIn(zoom))}
            disabled={!canZoomIn(zoom)}
            aria-label="Zoom in"
          >
            +
          </button>
          <button
            type="button"
            className="fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--fit"
            onClick={fitToWidth}
            aria-label="Fit to width"
          >
            Fit
          </button>
        </div>
      </div>
      <div
        className="fd-viewer__viewport"
        ref={viewportRef}
        tabIndex={0}
        role="region"
        aria-label="Document"
      >
        <div
          className={box ? "fd-viewer__page" : "fd-viewer__page fd-viewer__page--natural"}
          style={pageStyle}
        >
          {src && (
            <img
              className={
                box ? "fd-viewer__canvas" : "fd-viewer__canvas fd-viewer__canvas--natural"
              }
              src={src}
              alt={`Page ${page + 1}`}
            />
          )}
        </div>
      </div>
    </div>
  );
}

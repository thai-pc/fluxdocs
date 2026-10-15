import type { PageSize } from "@fluxdocs/core";

// viewer-model: the pure logic behind the FluxDocsViewer chrome (zoom ladder,
// fit-to-width, render DPI, display size, page clamping, key classification).
// An identical copy lives in @fluxdocs/react and @fluxdocs/vue (like
// dataurl.ts) so both viewers behave the same; it is not exported from either
// package index. Design: docs/design/viewer-chrome.md §4–§5.

/** Smallest zoom factor (25%). */
export const ZOOM_MIN = 0.25;
/** Largest zoom factor (400%). */
export const ZOOM_MAX = 4;
/** The zoom ladder walked by zoom in / zoom out. */
export const ZOOM_STEPS: readonly number[] = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];
/** Pixel budget for one rendered bitmap (8 Mi px, 1/8 of the Go hard cap). */
export const RENDER_BUDGET_PX = 8 * 2 ** 20;
/** CSS pixels per PDF point (96 CSS px per inch / 72 pt per inch). */
export const CSS_PX_PER_PT = 96 / 72;
/** Highest devicePixelRatio honored when choosing the default DPI. */
export const MAX_DPR = 2;
/** Viewport inline padding assumed when the computed value is unparseable. */
export const DEFAULT_PAGE_PADDING_PX = 16;
/** DPI ceiling for the fallback path (page geometry unknown). */
export const FALLBACK_MAX_DPI = 600;

const EPS = 1e-6;

/** clamp bounds `v` to `[lo, hi]`. */
export function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}

/**
 * clampZoom bounds a requested zoom to `[ZOOM_MIN, ZOOM_MAX]`. NaN (e.g. a bad
 * `initialZoom`) falls back to 1 (100%).
 */
export function clampZoom(zoom: number): number {
  if (Number.isNaN(zoom)) {
    return 1;
  }
  return clamp(zoom, ZOOM_MIN, ZOOM_MAX);
}

/**
 * zoomIn returns the smallest ladder step above `zoom`, or `zoom` (clamped)
 * when already at the top. Off-ladder values (from fit-to-width) step to the
 * next ladder value.
 */
export function zoomIn(zoom: number): number {
  const z = clampZoom(zoom);
  for (const step of ZOOM_STEPS) {
    if (step > z + EPS) {
      return step;
    }
  }
  return z;
}

/** zoomOut returns the largest ladder step below `zoom`, or `zoom` (clamped) at the bottom. */
export function zoomOut(zoom: number): number {
  const z = clampZoom(zoom);
  for (let i = ZOOM_STEPS.length - 1; i >= 0; i--) {
    if (ZOOM_STEPS[i] < z - EPS) {
      return ZOOM_STEPS[i];
    }
  }
  return z;
}

/** canZoomIn reports whether zoom in would change the value. */
export function canZoomIn(zoom: number): boolean {
  return zoom < ZOOM_MAX - EPS;
}

/** canZoomOut reports whether zoom out would change the value. */
export function canZoomOut(zoom: number): boolean {
  return zoom > ZOOM_MIN + EPS;
}

/** zoomLabel formats a zoom factor for the readout, e.g. 1.25 -> "125%". */
export function zoomLabel(zoom: number): string {
  return `${Math.round(zoom * 100)}%`;
}

/**
 * sanitizePage turns a host-supplied page index into a non-negative integer
 * (NaN/Infinity -> 0, fractions truncated).
 */
export function sanitizePage(page: number): number {
  return Number.isFinite(page) ? Math.max(0, Math.trunc(page)) : 0;
}

/** clampPage bounds `page` to `[0, pageCount - 1]` (0 for an empty document). */
export function clampPage(page: number, pageCount: number): number {
  return clamp(sanitizePage(page), 0, Math.max(0, pageCount - 1));
}

/** A width/height pair. Units depend on the caller (points or CSS px). */
export interface Size {
  width: number;
  height: number;
}

/**
 * orientedSize returns the page's displayed width/height in points: the
 * MediaBox swapped for a 90/270 rotation. Returns null when the geometry is
 * missing or not finite and positive (untrusted input).
 */
export function orientedSize(size: PageSize | null | undefined): Size | null {
  if (!size) {
    return null;
  }
  const { width, height } = size;
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return null;
  }
  const rot = (((Math.trunc(size.rotation) || 0) % 360) + 360) % 360;
  return rot === 90 || rot === 270 ? { width: height, height: width } : { width, height };
}

/**
 * displaySize returns the page box size in CSS px at `zoom` (100% = true
 * physical size), or null when the geometry is unusable.
 */
export function displaySize(size: PageSize | null | undefined, zoom: number): Size | null {
  const o = orientedSize(size);
  if (!o) {
    return null;
  }
  return {
    width: o.width * CSS_PX_PER_PT * zoom,
    height: o.height * CSS_PX_PER_PT * zoom,
  };
}

/**
 * safePageSize reads a page's geometry, returning null instead of throwing (a
 * pageSize failure must not surface as a viewer error).
 */
export function safePageSize(
  doc: { pageSize(index: number): PageSize } | null | undefined,
  index: number,
): PageSize | null {
  if (!doc) {
    return null;
  }
  try {
    return doc.pageSize(index);
  } catch {
    return null;
  }
}

/** parsePadding reads a computed CSS length in px, falling back to 16. */
export function parsePadding(value: string | null | undefined): number {
  const n = parseFloat(value ?? "");
  return Number.isFinite(n) && n >= 0 ? n : DEFAULT_PAGE_PADDING_PX;
}

/**
 * fitZoom returns the zoom that fits the page width into a viewport of
 * `clientWidth` px with `paddingPx` inline padding per side, clamped and
 * rounded DOWN to 2 decimals so the fitted page never overflows the viewport
 * (rounding to nearest made it up to a few px too wide). It returns `current`
 * unchanged when the viewport has no width (hidden, jsdom) or the page
 * geometry is unusable.
 */
export function fitZoom(
  clientWidth: number,
  paddingPx: number,
  size: PageSize | null | undefined,
  current: number,
): number {
  const o = orientedSize(size);
  if (!o || !Number.isFinite(clientWidth) || clientWidth <= 0) {
    return current;
  }
  const raw = (clientWidth - 2 * paddingPx) / (o.width * CSS_PX_PER_PT);
  // The 1e-9 absorbs float noise (e.g. 137.99999999 for an exact 1.38) and
  // cannot widen the page by a visible amount.
  return clampZoom(Math.floor(clamp(raw, ZOOM_MIN, ZOOM_MAX) * 100 + 1e-9) / 100);
}

/** currentDpr reads window.devicePixelRatio, or 1 outside a browser. */
export function currentDpr(): number {
  return typeof window !== "undefined" ? window.devicePixelRatio : 1;
}

/**
 * baseDpi is the render DPI at 100% zoom: the host's `renderOptions.dpi` when
 * it is a positive number, otherwise 96 × devicePixelRatio (capped at 2).
 */
export function baseDpi(hostDpi: number | undefined, dpr: number): number {
  if (typeof hostDpi === "number" && Number.isFinite(hostDpi) && hostDpi > 0) {
    return hostDpi;
  }
  const d = Number.isFinite(dpr) && dpr > 0 ? dpr : 1;
  return 96 * Math.min(d, MAX_DPR);
}

/**
 * renderPixels is the bitmap area the core allocates for a page of `o` points
 * at `dpi`. It mirrors core/render deviceMatrix: scale = dpi / 72, each side
 * rounded up and at least 1 px — so a sliver page costs a full pixel row.
 */
export function renderPixels(o: Size, dpi: number): number {
  const s = dpi / 72;
  return Math.max(1, Math.ceil(o.width * s)) * Math.max(1, Math.ceil(o.height * s));
}

/** Error message shown when a page exceeds the pixel budget even at 1 dpi. */
export const PAGE_TOO_LARGE_MESSAGE = "page is too large to render";

/**
 * fitsBudget reports whether the page can be rendered within RENDER_BUDGET_PX
 * at all (i.e. at 1 dpi). False means the viewer must not call renderPage and
 * shows PAGE_TOO_LARGE_MESSAGE instead. Unusable geometry returns true: that
 * path renders at the fallback DPI and relies on the Go-side canvas cap.
 */
export function fitsBudget(size: PageSize | null | undefined): boolean {
  const o = orientedSize(size);
  return !o || renderPixels(o, 1) <= RENDER_BUDGET_PX;
}

/**
 * renderDpi is the integer DPI to rasterize a page at for `zoom`: base DPI ×
 * zoom, capped so the bitmap — counted the way the core counts it, see
 * renderPixels — stays within RENDER_BUDGET_PX. Always an integer ≥ 1 (the
 * core treats dpi ≤ 0 as 150, which would bypass the budget); a page that
 * exceeds the budget even at 1 dpi is caught by fitsBudget. With unusable
 * geometry it falls back to base × zoom clamped to [1, 600] and lets the
 * Go-side canvas cap reject anything hostile.
 */
export function renderDpi(
  size: PageSize | null | undefined,
  zoom: number,
  hostDpi: number | undefined,
  dpr: number,
): number {
  const target = baseDpi(hostDpi, dpr) * zoom;
  const o = orientedSize(size);
  if (!o) {
    return fallbackDpi(target);
  }
  // The area bound alone lets an extreme aspect ratio through (the long side
  // times a 1 px short side); the second term bounds the long side by itself.
  const budget = Math.min(
    72 * Math.sqrt(RENDER_BUDGET_PX / (o.width * o.height)),
    (72 * RENDER_BUDGET_PX) / Math.max(o.width, o.height),
  );
  const dpi = Math.floor(Math.min(target, budget));
  if (!Number.isFinite(dpi)) {
    return fallbackDpi(target);
  }
  if (dpi <= 1 || renderPixels(o, dpi) <= RENDER_BUDGET_PX) {
    return Math.max(1, dpi);
  }
  // Per-side rounding pushed the area over budget: binary-search the largest
  // DPI that fits (bounded: at most ~log2(dpi) steps, even for a huge host dpi).
  let lo = 1;
  let hi = dpi;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (renderPixels(o, mid) <= RENDER_BUDGET_PX) {
      lo = mid;
    } else {
      hi = mid - 1;
    }
  }
  return lo;
}

function fallbackDpi(target: number): number {
  if (!Number.isFinite(target)) {
    return 1;
  }
  return clamp(Math.floor(target), 1, FALLBACK_MAX_DPI);
}

/**
 * centerRatio is the position of the viewport's center along one axis as a
 * fraction of the scrollable content (0.5 when there is nothing to scroll).
 */
export function centerRatio(scrollPos: number, clientSize: number, scrollSize: number): number {
  if (!(scrollSize > 0)) {
    return 0.5;
  }
  return (scrollPos + clientSize / 2) / scrollSize;
}

/** scrollForRatio is the scroll offset that puts `ratio` at the viewport center. */
export function scrollForRatio(ratio: number, clientSize: number, scrollSize: number): number {
  return Math.max(0, ratio * scrollSize - clientSize / 2);
}

/** An action the viewer takes for a key press. */
export type KeyAction = "nextPage" | "prevPage" | "zoomIn" | "zoomOut" | "zoomReset";

/** The parts of a KeyboardEvent the classifier reads. */
export interface KeyInput {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  isComposing?: boolean;
  defaultPrevented?: boolean;
}

/** Where the key press happened and what the viewer can currently do. */
export interface KeyContext {
  /** The target is an input, textarea, select, or contenteditable. */
  editable: boolean;
  /** The target is inside the scrolling viewport (vs. the toolbar). */
  inViewport: boolean;
  /** The viewport is scrolled to the top (or cannot scroll). */
  atTop: boolean;
  /** The viewport is scrolled to the bottom (or cannot scroll). */
  atBottom: boolean;
  /** A previous page exists. */
  canPrev: boolean;
  /** A next page exists. */
  canNext: boolean;
}

/** The classifier's verdict: what to do and whether to call preventDefault. */
export interface KeyDecision {
  action: KeyAction | null;
  preventDefault: boolean;
}

const NONE: KeyDecision = { action: null, preventDefault: false };

/**
 * classifyKey maps a key press to a viewer action (spec §8.4):
 *
 * - Ctrl/Cmd (no Alt) + `+`/`=` zoom in, `-`/`_` zoom out, `0` reset — always
 *   preventDefault so the browser never zooms the host page, even at a limit.
 * - Unmodified ArrowDown/ArrowUp change page, but inside the viewport only once
 *   it is scrolled to the bottom/top (the edge rule); a no-op at the first/last
 *   page is not handled and not prevented.
 *
 * Ignored entirely: already-prevented events, IME composition, and editable
 * targets.
 */
export function classifyKey(ev: KeyInput, ctx: KeyContext): KeyDecision {
  if (ev.defaultPrevented || ev.isComposing || ctx.editable) {
    return NONE;
  }
  if ((ev.ctrlKey || ev.metaKey) && !ev.altKey) {
    switch (ev.key) {
      case "+":
      case "=":
        return { action: "zoomIn", preventDefault: true };
      case "-":
      case "_":
        return { action: "zoomOut", preventDefault: true };
      case "0":
        return { action: "zoomReset", preventDefault: true };
      default:
        return NONE;
    }
  }
  if (ev.ctrlKey || ev.metaKey || ev.altKey || ev.shiftKey) {
    return NONE;
  }
  if (ev.key === "ArrowDown") {
    if (!ctx.canNext || (ctx.inViewport && !ctx.atBottom)) {
      return NONE;
    }
    return { action: "nextPage", preventDefault: true };
  }
  if (ev.key === "ArrowUp") {
    if (!ctx.canPrev || (ctx.inViewport && !ctx.atTop)) {
      return NONE;
    }
    return { action: "prevPage", preventDefault: true };
  }
  return NONE;
}

/** isEditable reports whether a key target accepts text input. */
export function isEditable(target: EventTarget | null): boolean {
  if (typeof Element === "undefined" || !(target instanceof Element)) {
    return false;
  }
  const tag = target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") {
    return true;
  }
  if ((target as HTMLElement).isContentEditable) {
    return true;
  }
  // jsdom does not implement isContentEditable; check the attribute too.
  return target.closest('[contenteditable]:not([contenteditable="false"])') !== null;
}

/**
 * keyContext builds a KeyContext from the event target, the viewport element,
 * and the page navigation state. Scroll metrics come from the viewport; a
 * 1px tolerance absorbs fractional scroll positions.
 */
export function keyContext(
  target: EventTarget | null,
  viewport: HTMLElement | null,
  canPrev: boolean,
  canNext: boolean,
): KeyContext {
  const inViewport =
    viewport !== null && typeof Node !== "undefined" && target instanceof Node
      ? viewport.contains(target)
      : false;
  const atTop = viewport ? viewport.scrollTop <= 0 : true;
  const atBottom = viewport
    ? viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 1
    : true;
  return { editable: isEditable(target), inViewport, atTop, atBottom, canPrev, canNext };
}

/**
 * shouldRescueFocus reports whether focus must move to the viewport after a
 * state change: focus was inside the viewer before the change and is now on a
 * disabled control inside it, or was dropped to <body> (browsers drop focus
 * from a button that becomes disabled).
 */
export function shouldRescueFocus(
  root: HTMLElement | null,
  focusWasInside: boolean,
): boolean {
  if (!root || !focusWasInside || typeof document === "undefined") {
    return false;
  }
  const active = document.activeElement;
  if (!active || active === document.body) {
    return true;
  }
  return root.contains(active) && (active as HTMLButtonElement).disabled === true;
}

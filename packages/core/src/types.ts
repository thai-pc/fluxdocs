// Public types for the FluxDocs client SDK. Field names mirror the core's JSON
// contract (spec Appendix A, camelCase).

/** A rectangle in PDF coordinate space (origin bottom-left, units in points). */
export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** A point in PDF coordinate space. */
export interface Point {
  x: number;
  y: number;
}

/** The geometry of one page, in points. */
export interface PageSize {
  width: number;
  height: number;
  /** 0 | 90 | 180 | 270 */
  rotation: number;
}

/** The four Wave-1 editable annotation types. */
export type AnnotationType = "highlight" | "note" | "draw" | "shape";

/** An annotation as stored by the core (returned by add/get). */
export interface Annotation {
  id: string;
  pageId: string;
  type: string;
  rect: Rect;
  color: string;
  opacity: number;
  content?: string;
  points?: Point[];
}

/** The fields needed to create an annotation. */
export interface AnnotationInput {
  pageIndex: number;
  type: AnnotationType;
  rect: Rect;
  color?: string;
  opacity?: number;
  content?: string;
  points?: Point[];
}

/** Options for rendering a page to a raster image. */
export interface RenderOptions {
  /** Dots per inch; defaults to 150. */
  dpi?: number;
  /** Output format; defaults to "png". */
  format?: "png" | "jpeg";
  /** JPEG quality 1-100; defaults to 90. */
  quality?: number;
  /** Render the bare page without the annotation overlay. */
  skipAnnotations?: boolean;
  /**
   * Bounds the raster work of one page render, in pixel visits (fills,
   * strokes, images, glyphs, and the annotation overlay; overdraw counts every
   * time). Absent or 0 = default: 32 × the page's canvas pixels, clamped to
   * [16 Mi, 1 Gi]. A positive value is an absolute limit, not scaled by DPI.
   * < 0 = unlimited (unsafe for untrusted input). Fractions are truncated
   * toward zero; NaN, ±Infinity, or a value outside the int64 range is ignored
   * (default). Exceeding it throws a FluxDocsError with code
   * `"budget_exceeded"`.
   */
  maxPaintPixels?: number;
  /**
   * Bounds the interpreter steps of one page render: content operators at
   * every Form XObject depth plus every glyph shown. Absent or 0 = default
   * (8 Mi steps). < 0 = unlimited (unsafe for untrusted input). Same number
   * handling as `maxPaintPixels`. Exceeding it throws a FluxDocsError with code
   * `"budget_exceeded"`.
   */
  maxOperators?: number;
  /**
   * Wall-clock bound for one page render, in milliseconds. Applied only when
   * finite and > 0; absent, 0, negative, NaN, or ±Infinity = no deadline (the
   * default; the budgets above still apply). Past the deadline the render
   * throws a FluxDocsError with code `"timeout"`. The render is synchronous, so
   * the tab's main thread stays busy until it finishes or times out.
   */
  timeoutMs?: number;
}

/**
 * The machine-readable reason carried by {@link FluxDocsError}. The set is
 * closed; new codes are only ever added.
 *
 * - `canvas_too_large`: the page at the requested DPI exceeds the core's canvas
 *   pixel cap (lower the DPI).
 * - `budget_exceeded`: the render exceeded a resource budget (see
 *   `RenderOptions.maxPaintPixels` / `maxOperators`). Deterministic: retrying
 *   with the same options fails the same way.
 * - `timeout`: the render passed `RenderOptions.timeoutMs`. Load dependent: a
 *   retry may succeed.
 * - `canceled`: the operation was canceled.
 * - `invalid_pdf`: the bytes are not a readable PDF.
 * - `encrypted`: the document is encrypted and cannot be opened.
 * - `page_not_found`: the page index is out of range.
 * - `unsupported_format`: `RenderOptions.format` is not "png" or "jpeg".
 * - `invalid_annotation`: the annotation is invalid or was not found.
 * - `invalid_argument`: a malformed argument (missing page index, non-object
 *   options, ...).
 * - `invalid_handle`: the document was never opened or is already closed.
 * - `internal`: a bug in FluxDocs (a recovered panic in the core) or the
 *   runtime failed to load.
 * - `unknown`: any other failure, including a code this version does not know
 *   (for example from a newer or older `.wasm` build).
 */
export type FluxDocsErrorCode =
  | "canvas_too_large"
  | "budget_exceeded"
  | "timeout"
  | "canceled"
  | "invalid_pdf"
  | "encrypted"
  | "page_not_found"
  | "unsupported_format"
  | "invalid_annotation"
  | "invalid_argument"
  | "invalid_handle"
  | "internal"
  | "unknown";

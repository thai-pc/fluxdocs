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
}

// @fluxdocs/react — React components and hooks for the FluxDocs client SDK.
// Everything renders in the browser via WebAssembly; no document is sent to a
// server (privacy-first). Build on top of @fluxdocs/core's loadFluxDocs.

export { FluxDocsViewer } from "./FluxDocsViewer.js";
export type { FluxDocsViewerProps } from "./FluxDocsViewer.js";
export { useFluxDoc, usePageImage } from "./hooks.js";
export { bytesToDataURL, mimeFor } from "./dataurl.js";

// Re-export the core types React consumers commonly need.
export type {
  Annotation,
  AnnotationInput,
  AnnotationType,
  FluxDoc,
  FluxDocs,
  PageSize,
  Point,
  Rect,
  RenderOptions,
} from "@fluxdocs/core";

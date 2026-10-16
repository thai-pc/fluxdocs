// @fluxdocs/vue — Vue 3 components and composables for the FluxDocs client SDK.
// Everything renders in the browser via WebAssembly; no document is sent to a
// server (privacy-first). Build on top of @fluxdocs/core's loadFluxDocs.

export { FluxDocsViewer } from "./FluxDocsViewer.js";
export { useFluxDoc, usePageImage } from "./composables.js";
export { bytesToDataURL, mimeFor } from "./dataurl.js";

// Re-export the core types Vue consumers commonly need.
export type {
  Annotation,
  AnnotationInput,
  AnnotationType,
  FluxDoc,
  FluxDocs,
  FluxDocsErrorCode,
  PageSize,
  Point,
  Rect,
  RenderOptions,
} from "@fluxdocs/core";

import { FluxDocsError } from "@fluxdocs/core";
import type {
  Annotation,
  FluxDoc,
  FluxDocs,
  FluxDocsErrorCode,
  PageSize,
  RenderOptions,
} from "@fluxdocs/core";

// A minimal in-memory stand-in for the WASM runtime, so the Vue glue can be
// tested without building/loading the core. The real core is tested in
// @fluxdocs/core; here we only verify the component/composable behavior.

export function fakePNG(page: number): Uint8Array {
  return new Uint8Array([0x89, 0x50, 0x4e, 0x47, page & 0xff]);
}

export interface MockOptions {
  pageCount?: number;
  openError?: string;
  renderError?: string;
  /** Every renderPage() call is appended here (page index + options). */
  renderCalls?: RenderCall[];
  /** Overrides the page geometry (default 200x300 pt, no rotation). */
  pageSize?: PageSize;
  /** When set, pageSize() throws this message. */
  pageSizeError?: string;
  /**
   * With openError, open() throws a FluxDocsError with this code (as the
   * wrapper does for a coded WASM envelope) instead of a plain Error.
   */
  openErrorCode?: FluxDocsErrorCode;
  /** With renderError, renderPage() throws a FluxDocsError with this code. */
  renderErrorCode?: FluxDocsErrorCode;
}

/** fail builds the error the mock throws: coded like the wrapper's, or plain. */
function fail(message: string, code: FluxDocsErrorCode | undefined): Error {
  return code ? new FluxDocsError(message, code) : new Error(message);
}

export interface RenderCall {
  index: number;
  opts: RenderOptions | undefined;
}

export function mockRuntime(opts: MockOptions = {}): FluxDocs {
  const pageCount = opts.pageCount ?? 3;

  const doc = {
    pageCount,
    pageSize: (i: number) => {
      if (opts.pageSizeError) throw new Error(opts.pageSizeError);
      return { ...(opts.pageSize ?? { width: 200, height: 300, rotation: 0 }), _i: i };
    },
    extractText: () => "text",
    extractTextByPage: (i: number) => `page ${i}`,
    renderPage: (i: number, o?: RenderOptions) => {
      opts.renderCalls?.push({ index: i, opts: o });
      if (opts.renderError) throw fail(opts.renderError, opts.renderErrorCode);
      return fakePNG(i);
    },
    addAnnotation: (): Annotation => ({
      id: "ann-1",
      pageId: "page-0",
      type: "highlight",
      rect: { x: 0, y: 0, width: 1, height: 1 },
      color: "#f59e0b",
      opacity: 1,
    }),
    getAnnotations: (): Annotation[] => [],
    exportAnnotations: () => "{}",
    save: () => new Uint8Array([0x25, 0x50]),
    close: () => {},
  } as unknown as FluxDoc;

  return {
    version: () => "0.1.0-mock",
    open: () => {
      if (opts.openError) throw fail(opts.openError, opts.openErrorCode);
      return doc;
    },
  } as unknown as FluxDocs;
}

import type { Annotation, FluxDoc, FluxDocs, PageSize, RenderOptions } from "@fluxdocs/core";

// A minimal in-memory stand-in for the WASM runtime, so the React glue can be
// tested without building/loading the core. The real core is tested in
// @fluxdocs/core; here we only verify the component/hook behavior.

// Distinct 1x1 PNG-ish byte stub per page so renders are distinguishable.
export function fakePNG(page: number): Uint8Array {
  return new Uint8Array([0x89, 0x50, 0x4e, 0x47, page & 0xff]);
}

export interface MockOptions {
  pageCount?: number;
  /** When set, open() throws this message. */
  openError?: string;
  /** When set, renderPage() throws this message. */
  renderError?: string;
  /** Every renderPage() call is appended here (page index + options). */
  renderCalls?: RenderCall[];
  /** Overrides the page geometry (default 200x300 pt, no rotation). */
  pageSize?: PageSize;
  /** When set, pageSize() throws this message. */
  pageSizeError?: string;
}

export interface RenderCall {
  index: number;
  opts: RenderOptions | undefined;
}

export function mockRuntime(opts: MockOptions = {}): FluxDocs {
  const pageCount = opts.pageCount ?? 3;
  let closed = false;

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
      if (opts.renderError) throw new Error(opts.renderError);
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
    close: () => {
      closed = true;
    },
    get _closed() {
      return closed;
    },
  } as unknown as FluxDoc;

  return {
    version: () => "0.1.0-mock",
    open: () => {
      if (opts.openError) throw new Error(opts.openError);
      return doc;
    },
  } as unknown as FluxDocs;
}

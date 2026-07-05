// @fluxdocs/core — a typed wrapper over the FluxDocs WebAssembly core. Everything
// runs in the browser; no document byte is sent to a server (privacy-first). This
// package wraps the low-level globalThis.fluxdocs envelope API (see wasm/main.go)
// into ergonomic classes that throw FluxDocsError on failure.

import type {
  Annotation,
  AnnotationInput,
  PageSize,
  RenderOptions,
} from "./types.js";

export type {
  Annotation,
  AnnotationInput,
  AnnotationType,
  PageSize,
  Point,
  Rect,
  RenderOptions,
} from "./types.js";

/** Thrown when a core call reports an error in its result envelope. */
export class FluxDocsError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "FluxDocsError";
  }
}

/** The minimal shape of the Go runtime from wasm_exec.js. */
interface GoRuntime {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}

/** Every result the core returns carries an `error` string ("" on success). */
interface Envelope {
  error: string;
}

/** The raw function surface the WASM registers on globalThis.fluxdocs. */
interface RawAPI {
  version(): Envelope & { version: string };
  open(bytes: Uint8Array): Envelope & { handle: number; pageCount: number };
  close(handle: number): Envelope;
  pageCount(handle: number): Envelope & { count: number };
  pageSize(handle: number, index: number): Envelope & PageSize;
  extractText(handle: number): Envelope & { text: string };
  extractTextByPage(handle: number, index: number): Envelope & { text: string };
  renderPage(
    handle: number,
    index: number,
    opts?: RenderOptions,
  ): Envelope & { image: Uint8Array; format: string };
  save(handle: number): Envelope & { bytes: Uint8Array };
  addAnnotation(
    handle: number,
    ann: AnnotationInput,
  ): Envelope & { annotation: Annotation };
  getAnnotations(
    handle: number,
    pageIndex: number,
  ): Envelope & { annotations: Annotation[] };
  exportAnnotations(handle: number): Envelope & { json: string };
}

/** Options for {@link loadFluxDocs}. */
export interface LoadOptions {
  /** The Go constructor from wasm_exec.js; defaults to globalThis.Go. */
  Go?: new () => GoRuntime;
}

/** unwrap throws on a non-empty error field, otherwise returns the result. */
function unwrap<T extends Envelope>(res: T): T {
  if (res.error) {
    throw new FluxDocsError(res.error);
  }
  return res;
}

/**
 * loadFluxDocs instantiates the WASM core and returns a runtime. `wasm` is the
 * compiled module (fetch it or read the bytes). The Go runtime shim wasm_exec.js
 * must have run first (it defines globalThis.Go), or pass its constructor via
 * `opts.Go`.
 */
export async function loadFluxDocs(
  wasm: BufferSource,
  opts: LoadOptions = {},
): Promise<FluxDocs> {
  const GoCtor = opts.Go ?? (globalThis as unknown as { Go?: new () => GoRuntime }).Go;
  if (!GoCtor) {
    throw new FluxDocsError(
      "Go runtime not found: load wasm_exec.js first, or pass opts.Go",
    );
  }
  const go = new GoCtor();
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  // go.run starts the Go program, which registers globalThis.fluxdocs and then
  // blocks; we intentionally do not await it (it never resolves).
  void go.run(instance);
  const api = (globalThis as unknown as { fluxdocs?: RawAPI }).fluxdocs;
  if (!api) {
    throw new FluxDocsError("WASM did not register globalThis.fluxdocs");
  }
  return new FluxDocs(api);
}

/** A loaded FluxDocs runtime. Open documents with {@link open}. */
export class FluxDocs {
  /** @internal */
  constructor(private readonly api: RawAPI) {}

  /** The core version string. */
  version(): string {
    return unwrap(this.api.version()).version;
  }

  /** Opens a PDF from its bytes. Throws FluxDocsError on a parse failure. */
  open(bytes: Uint8Array): FluxDoc {
    const res = unwrap(this.api.open(bytes));
    return new FluxDoc(this.api, res.handle, res.pageCount);
  }
}

/** An open PDF document, referenced by an opaque handle. Call {@link close}. */
export class FluxDoc {
  #closed = false;

  /** @internal */
  constructor(
    private readonly api: RawAPI,
    private readonly handle: number,
    /** The number of pages, captured at open time. */
    public readonly pageCount: number,
  ) {}

  private check(): void {
    if (this.#closed) {
      throw new FluxDocsError("document is closed");
    }
  }

  /** Returns the geometry of page `index` (0-based). */
  pageSize(index: number): PageSize {
    this.check();
    const r = unwrap(this.api.pageSize(this.handle, index));
    return { width: r.width, height: r.height, rotation: r.rotation };
  }

  /** Extracts all text in reading order (pages separated by a form feed). */
  extractText(): string {
    this.check();
    return unwrap(this.api.extractText(this.handle)).text;
  }

  /** Extracts the text of a single page. */
  extractTextByPage(index: number): string {
    this.check();
    return unwrap(this.api.extractTextByPage(this.handle, index)).text;
  }

  /** Renders a page to PNG or JPEG bytes, with the annotation overlay by default. */
  renderPage(index: number, opts: RenderOptions = {}): Uint8Array {
    this.check();
    return unwrap(this.api.renderPage(this.handle, index, opts)).image;
  }

  /** Adds an annotation and returns the stored copy (with its id/timestamps). */
  addAnnotation(ann: AnnotationInput): Annotation {
    this.check();
    return unwrap(this.api.addAnnotation(this.handle, ann)).annotation;
  }

  /** Lists the annotations on a page. */
  getAnnotations(pageIndex: number): Annotation[] {
    this.check();
    return unwrap(this.api.getAnnotations(this.handle, pageIndex)).annotations;
  }

  /** Exports the annotation layer as a JSON string. */
  exportAnnotations(): string {
    this.check();
    return unwrap(this.api.exportAnnotations(this.handle)).json;
  }

  /** Serializes the document (without baking in annotations) to PDF bytes. */
  save(): Uint8Array {
    this.check();
    return unwrap(this.api.save(this.handle)).bytes;
  }

  /** Releases the document. Safe to call more than once. */
  close(): void {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    unwrap(this.api.close(this.handle));
  }
}

import {
  computed,
  shallowRef,
  toValue,
  watchEffect,
  type ComputedRef,
  type MaybeRefOrGetter,
  type Ref,
} from "vue";
import type { FluxDoc, FluxDocs, RenderOptions } from "@fluxdocs/core";

import { bytesToDataURL, mimeFor } from "./dataurl.js";

/**
 * useFluxDoc opens a PDF from `data` using `runtime` and manages its lifetime:
 * the document is closed automatically when the reactive inputs change or the
 * owning scope is disposed. Inputs may be refs, getters, or plain values. A
 * parse failure is surfaced via `error`.
 *
 * The document is held in a shallowRef so Vue does not deeply proxy the class
 * instance (which would break its private state).
 */
export function useFluxDoc(
  runtime: MaybeRefOrGetter<FluxDocs | null | undefined>,
  data: MaybeRefOrGetter<Uint8Array | null | undefined>,
): { doc: Ref<FluxDoc | null>; error: Ref<Error | null> } {
  const doc = shallowRef<FluxDoc | null>(null);
  const error = shallowRef<Error | null>(null);

  watchEffect((onCleanup) => {
    const rt = toValue(runtime);
    const d = toValue(data);
    if (!rt || !d) {
      doc.value = null;
      error.value = null;
      return;
    }
    let opened: FluxDoc | null = null;
    try {
      opened = rt.open(d);
      doc.value = opened;
      error.value = null;
    } catch (e) {
      doc.value = null;
      error.value = e as Error;
    }
    onCleanup(() => opened?.close());
  });

  return { doc, error };
}

/**
 * usePageImage renders one page of `doc` to a data URL suitable for an <img>
 * src, recomputing when the document, page index, or options change. Returns a
 * computed `{ src, error }`; src is null when there is no document.
 */
export function usePageImage(
  doc: MaybeRefOrGetter<FluxDoc | null>,
  pageIndex: MaybeRefOrGetter<number>,
  opts?: MaybeRefOrGetter<RenderOptions | undefined>,
): ComputedRef<{ src: string | null; error: Error | null }> {
  return computed(() => {
    const d = toValue(doc);
    if (!d) {
      return { src: null, error: null };
    }
    const o = toValue(opts);
    try {
      const bytes = d.renderPage(toValue(pageIndex), o);
      return { src: bytesToDataURL(bytes, mimeFor(o)), error: null };
    } catch (e) {
      return { src: null, error: e as Error };
    }
  });
}

import { computed, defineComponent, h, ref, watch, type PropType } from "vue";
import type { FluxDocs, RenderOptions } from "@fluxdocs/core";

import { useFluxDoc, usePageImage } from "./composables.js";

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi);
}

/**
 * FluxDocsViewer is a minimal, dependency-light page viewer: it opens the PDF,
 * renders the current page client-side (no server round-trip), and offers
 * prev/next navigation. Styling hooks use the `fd-` BEM prefix so a host app can
 * theme them without collisions. Written with a render function (no SFC compiler
 * needed).
 */
export const FluxDocsViewer = defineComponent({
  name: "FluxDocsViewer",
  props: {
    /** A loaded runtime from `loadFluxDocs`. */
    runtime: { type: Object as PropType<FluxDocs | null>, default: null },
    /** The PDF bytes to view. */
    data: { type: Object as PropType<Uint8Array | null>, default: null },
    /** The page shown first (0-based, clamped). */
    initialPage: { type: Number, default: 0 },
    /** Render options passed to each page render. */
    renderOptions: { type: Object as PropType<RenderOptions>, default: undefined },
    /** Extra class on the root element (always carries `fd-viewer`). */
    className: { type: String, default: "" },
  },
  setup(props) {
    const { doc, error: openError } = useFluxDoc(
      () => props.runtime,
      () => props.data,
    );
    const page = ref(props.initialPage);
    const pageCount = computed(() => doc.value?.pageCount ?? 0);

    // Keep the current page in range as documents load or change.
    watch(pageCount, (n) => {
      page.value = clamp(page.value, 0, Math.max(0, n - 1));
    });

    const image = usePageImage(doc, page, () => props.renderOptions);
    const error = computed(() => openError.value ?? image.value.error);

    return () => {
      const rootClass = props.className
        ? `fd-viewer ${props.className}`
        : "fd-viewer";

      if (error.value) {
        return h("div", { class: rootClass, role: "alert" }, [
          h(
            "p",
            { class: "fd-viewer__error" },
            `Failed to display PDF: ${error.value.message}`,
          ),
        ]);
      }
      if (!doc.value) {
        return h("div", { class: rootClass }, [
          h("p", { class: "fd-viewer__empty" }, "No document loaded."),
        ]);
      }

      const n = pageCount.value;
      const children = [
        h("div", { class: "fd-viewer__toolbar" }, [
          h(
            "button",
            {
              type: "button",
              class: "fd-viewer__nav fd-viewer__nav--prev",
              disabled: page.value <= 0,
              "aria-label": "Previous page",
              onClick: () => (page.value = clamp(page.value - 1, 0, n - 1)),
            },
            "‹",
          ),
          h(
            "span",
            { class: "fd-viewer__page-info" },
            `Page ${page.value + 1} of ${n}`,
          ),
          h(
            "button",
            {
              type: "button",
              class: "fd-viewer__nav fd-viewer__nav--next",
              disabled: page.value >= n - 1,
              "aria-label": "Next page",
              onClick: () => (page.value = clamp(page.value + 1, 0, n - 1)),
            },
            "›",
          ),
        ]),
      ];
      if (image.value.src) {
        children.push(
          h("img", {
            class: "fd-viewer__canvas",
            src: image.value.src,
            alt: `Page ${page.value + 1}`,
          }),
        );
      }
      return h("div", { class: rootClass }, children);
    };
  },
});

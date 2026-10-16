import { computed, defineComponent, h, ref, watch, type PropType } from "vue";
import type { FluxDocs, RenderOptions } from "@fluxdocs/core";

import { useFluxDoc, usePageImage } from "./composables.js";
import {
  canZoomIn,
  canZoomOut,
  centerRatio,
  clampPage,
  clampZoom,
  classifyKey,
  currentDpr,
  displaySize,
  errorText,
  fitZoom,
  fitsBudget,
  keyContext,
  PAGE_TOO_LARGE_MESSAGE,
  parsePadding,
  renderDpi,
  safePageSize,
  sanitizePage,
  scrollForRatio,
  shouldRescueFocus,
  zoomIn,
  zoomLabel,
  zoomOut,
  type KeyAction,
} from "./viewer-model.js";

/**
 * FluxDocsViewer is a dependency-light page viewer: it opens the PDF, renders
 * the current page client-side (no server round-trip), and offers page
 * navigation, zoom (buttons and Ctrl/Cmd + + - 0), and Arrow-key paging while
 * focus is inside the viewer. Styling hooks use the `fd-` BEM prefix; import
 * `@fluxdocs/core/styles.css` for the base look. Written with a render function
 * (no SFC compiler needed).
 *
 * Emits `pageChange` (0-based page index) and `zoomChange` (zoom factor) after
 * every change, never on mount.
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
    /**
     * The zoom factor shown first (1 = 100% = true physical size), clamped to
     * 0.25–4.
     */
    initialZoom: { type: Number, default: 1 },
    /**
     * Render options passed to each page render. `dpi` is the sharpness at 100%
     * (default: 96 × devicePixelRatio, capped at 2×); the displayed size comes
     * from the zoom, and the viewer re-renders at a zoom-matched DPI.
     */
    renderOptions: { type: Object as PropType<RenderOptions>, default: undefined },
    /** Extra class on the root element (always carries `fd-viewer`). */
    className: { type: String, default: "" },
  },
  emits: {
    /** The new 0-based page index, after every page change (not on mount). */
    pageChange: (pageIndex: number) => typeof pageIndex === "number",
    /** The new zoom factor, after every zoom change (not on mount). */
    zoomChange: (zoom: number) => typeof zoom === "number",
  },
  setup(props, { emit }) {
    const { doc, error: openError } = useFluxDoc(
      () => props.runtime,
      () => props.data,
    );
    const rawPage = ref(sanitizePage(props.initialPage));
    const zoom = ref(clampZoom(props.initialZoom));

    const rootRef = ref<HTMLDivElement | null>(null);
    const viewportRef = ref<HTMLDivElement | null>(null);
    // Viewport center (as content ratios) captured before a zoom change.
    let pendingCenter: { x: number; y: number } | null = null;
    // Whether focus was inside the viewer when a page/zoom change started.
    let focusWasInside = false;
    // Where the next page change lands: the bottom after an ArrowUp edge-rule
    // page back (keeps reading upward), otherwise the top.
    let landAtBottom = false;

    const pageCount = computed(() => doc.value?.pageCount ?? 0);
    // The effective page is always in range once a document is loaded.
    const page = computed(() =>
      pageCount.value > 0 ? clampPage(rawPage.value, pageCount.value) : rawPage.value,
    );

    // Write the clamp back so a later, longer document does not jump pages.
    watch(pageCount, (n) => {
      if (n > 0) {
        rawPage.value = clampPage(rawPage.value, n);
      }
    });

    const size = computed(() => safePageSize(doc.value, page.value));
    const box = computed(() => displaySize(size.value, zoom.value));
    const dpi = computed(() =>
      renderDpi(size.value, zoom.value, props.renderOptions?.dpi, currentDpr()),
    );
    // Serialize so equal options (same zoom DPI, equal-but-new objects) do not
    // trigger another render — the same dedupe React gets from its render key.
    const optsKey = computed(() =>
      JSON.stringify({ ...props.renderOptions, dpi: dpi.value }),
    );
    const opts = computed(() => JSON.parse(optsKey.value) as RenderOptions);

    // A page over the pixel budget even at 1 dpi is never sent to the core.
    const sizeError = computed(() =>
      fitsBudget(size.value) ? null : new Error(PAGE_TOO_LARGE_MESSAGE),
    );
    const image = usePageImage(() => (sizeError.value ? null : doc.value), page, opts);
    const error = computed(() => openError.value ?? sizeError.value ?? image.value.error);

    // Report changes (never the initial mount).
    let reportedPage: number | null = null;
    watch(
      [doc, page],
      ([d, p]) => {
        if (!d) {
          return;
        }
        if (reportedPage === null) {
          reportedPage = p; // baseline: the first page shown
          return;
        }
        if (reportedPage !== p) {
          reportedPage = p;
          emit("pageChange", p);
        }
      },
      { immediate: true },
    );
    watch(zoom, (z) => emit("zoomChange", z));

    // Page or document change: start at the top of the page, or at the bottom
    // after an ArrowUp page back from inside the viewport (keep horizontal scroll).
    watch(
      [doc, page],
      () => {
        const vp = viewportRef.value;
        const bottom = landAtBottom;
        landAtBottom = false;
        if (vp) {
          vp.scrollTop = bottom ? Math.max(0, vp.scrollHeight - vp.clientHeight) : 0;
        }
      },
      { flush: "post" },
    );

    // Zoom change: keep the viewport center at the same relative position.
    watch(
      zoom,
      () => {
        const vp = viewportRef.value;
        const c = pendingCenter;
        pendingCenter = null;
        if (!vp || !c) {
          return;
        }
        vp.scrollLeft = scrollForRatio(c.x, vp.clientWidth, vp.scrollWidth);
        vp.scrollTop = scrollForRatio(c.y, vp.clientHeight, vp.scrollHeight);
      },
      { flush: "post" },
    );

    // Focus retention: a focused button that became disabled loses focus to
    // <body>, which would also detach the keyboard handler. Move it to the viewport.
    watch(
      [page, zoom],
      () => {
        const was = focusWasInside;
        focusWasInside = false;
        if (shouldRescueFocus(rootRef.value, was)) {
          viewportRef.value?.focus({ preventScroll: true });
        }
      },
      { flush: "post" },
    );

    function noteFocus(): void {
      const active = typeof document !== "undefined" ? document.activeElement : null;
      focusWasInside = !!active && !!rootRef.value?.contains(active);
    }

    function goToPage(next: number, atBottom = false): void {
      const p = clampPage(next, pageCount.value);
      if (p === page.value) {
        return;
      }
      noteFocus();
      landAtBottom = atBottom;
      rawPage.value = p;
    }

    function changeZoom(next: number): void {
      const z = clampZoom(next);
      if (z === zoom.value) {
        return;
      }
      const vp = viewportRef.value;
      if (vp) {
        pendingCenter = {
          x: centerRatio(vp.scrollLeft, vp.clientWidth, vp.scrollWidth),
          y: centerRatio(vp.scrollTop, vp.clientHeight, vp.scrollHeight),
        };
      }
      noteFocus();
      zoom.value = z;
    }

    function fitToWidth(): void {
      const vp = viewportRef.value;
      if (!vp) {
        return;
      }
      const padding = parsePadding(getComputedStyle(vp).paddingLeft);
      changeZoom(fitZoom(vp.clientWidth, padding, size.value, zoom.value));
    }

    function run(action: KeyAction, inViewport: boolean): void {
      switch (action) {
        case "nextPage":
          return goToPage(page.value + 1);
        case "prevPage":
          return goToPage(page.value - 1, inViewport);
        case "zoomIn":
          return changeZoom(zoomIn(zoom.value));
        case "zoomOut":
          return changeZoom(zoomOut(zoom.value));
        case "zoomReset":
          return changeZoom(1);
      }
    }

    function onKeydown(e: KeyboardEvent): void {
      const ctx = keyContext(
        e.target,
        viewportRef.value,
        page.value > 0,
        page.value < pageCount.value - 1,
      );
      const decision = classifyKey(e, ctx);
      if (decision.preventDefault) {
        e.preventDefault();
      }
      if (decision.action) {
        run(decision.action, ctx.inViewport);
      }
    }

    return () => {
      const rootClass = props.className
        ? `fd-viewer ${props.className}`
        : "fd-viewer";

      if (error.value) {
        return h("div", { class: rootClass, role: "alert" }, [
          h("p", { class: "fd-viewer__error" }, errorText(error.value)),
        ]);
      }
      if (!doc.value) {
        return h("div", { class: rootClass }, [
          h("p", { class: "fd-viewer__empty" }, "No document loaded."),
        ]);
      }

      const n = pageCount.value;
      const p = page.value;
      const z = zoom.value;
      const b = box.value;

      const toolbar = h("div", { class: "fd-viewer__toolbar" }, [
        h(
          "div",
          {
            class: "fd-viewer__group fd-viewer__group--nav",
            role: "group",
            "aria-label": "Page navigation",
          },
          [
            h(
              "button",
              {
                type: "button",
                class: "fd-viewer__btn fd-viewer__nav fd-viewer__nav--prev",
                disabled: p <= 0,
                "aria-label": "Previous page",
                onClick: () => goToPage(p - 1),
              },
              "‹",
            ),
            h(
              "span",
              {
                class: "fd-viewer__page-info",
                role: "status",
                "aria-live": "polite",
                "aria-atomic": "true",
              },
              `Page ${p + 1} of ${n}`,
            ),
            h(
              "button",
              {
                type: "button",
                class: "fd-viewer__btn fd-viewer__nav fd-viewer__nav--next",
                disabled: p >= n - 1,
                "aria-label": "Next page",
                onClick: () => goToPage(p + 1),
              },
              "›",
            ),
          ],
        ),
        h(
          "div",
          {
            class: "fd-viewer__group fd-viewer__group--zoom",
            role: "group",
            "aria-label": "Zoom",
          },
          [
            h(
              "button",
              {
                type: "button",
                class: "fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--out",
                disabled: !canZoomOut(z),
                "aria-label": "Zoom out",
                onClick: () => changeZoom(zoomOut(z)),
              },
              "−",
            ),
            h(
              "span",
              {
                class: "fd-viewer__zoom-level",
                role: "status",
                "aria-live": "polite",
                "aria-atomic": "true",
              },
              zoomLabel(z),
            ),
            h(
              "button",
              {
                type: "button",
                class: "fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--in",
                disabled: !canZoomIn(z),
                "aria-label": "Zoom in",
                onClick: () => changeZoom(zoomIn(z)),
              },
              "+",
            ),
            h(
              "button",
              {
                type: "button",
                class: "fd-viewer__btn fd-viewer__zoom fd-viewer__zoom--fit",
                "aria-label": "Fit to width",
                onClick: fitToWidth,
              },
              "Fit",
            ),
          ],
        ),
      ]);

      const img = image.value.src
        ? h("img", {
            class: b ? "fd-viewer__canvas" : "fd-viewer__canvas fd-viewer__canvas--natural",
            src: image.value.src,
            alt: `Page ${p + 1}`,
          })
        : null;

      const viewport = h(
        "div",
        {
          class: "fd-viewer__viewport",
          ref: viewportRef,
          tabindex: 0,
          role: "region",
          "aria-label": "Document",
        },
        [
          h(
            "div",
            {
              class: b ? "fd-viewer__page" : "fd-viewer__page fd-viewer__page--natural",
              style: b ? { width: `${b.width}px`, height: `${b.height}px` } : undefined,
            },
            img ? [img] : [],
          ),
        ],
      );

      return h("div", { class: rootClass, ref: rootRef, onKeydown }, [toolbar, viewport]);
    };
  },
});

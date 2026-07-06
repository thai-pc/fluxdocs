<script setup lang="ts">
import { ref, shallowRef, onMounted } from "vue";
import { loadFluxDocs, type FluxDocs } from "@fluxdocs/core";
import { FluxDocsViewer } from "@fluxdocs/vue";

// A minimal client-side PDF viewer. The WASM core is loaded once; the picked PDF
// is rendered entirely in the browser — never uploaded.
const runtime = shallowRef<FluxDocs | null>(null);
const pdf = ref<Uint8Array | null>(null);
const error = ref("");

onMounted(async () => {
  try {
    const buf = await fetch("/fluxdocs.wasm").then((r) => r.arrayBuffer());
    runtime.value = await loadFluxDocs(buf);
  } catch (e) {
    error.value = String(e);
  }
});

async function onPick(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0];
  if (file) pdf.value = new Uint8Array(await file.arrayBuffer());
}
</script>

<template>
  <main style="font-family: system-ui; max-width: 720px; margin: 2rem auto">
    <h1>FluxDocs — client-side PDF viewer</h1>
    <p>Pick a PDF. It is rendered in your browser via WebAssembly — never uploaded.</p>
    <p v-if="error" style="color: crimson">Failed to load the core: {{ error }}</p>
    <p v-else-if="!runtime">Loading the WASM core…</p>
    <input v-if="runtime" type="file" accept="application/pdf" @change="onPick" />
    <div v-if="runtime && pdf" style="margin-top: 1rem; border: 1px solid #ddd">
      <FluxDocsViewer :runtime="runtime" :data="pdf" :render-options="{ dpi: 150 }" />
    </div>
  </main>
</template>

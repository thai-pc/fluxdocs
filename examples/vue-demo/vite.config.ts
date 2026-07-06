import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// No backend: the PDF is processed entirely in the browser via WebAssembly.
export default defineConfig({ plugins: [vue()] });

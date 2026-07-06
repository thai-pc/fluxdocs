import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The WASM binary and wasm_exec.js are served from public/ (built by
// `pnpm build:wasm`). No backend — the PDF is processed entirely in the browser.
export default defineConfig({
  plugins: [react()],
});

import { useEffect, useState } from "react";
import { loadFluxDocs, type FluxDocs } from "@fluxdocs/core";
import { FluxDocsViewer } from "@fluxdocs/react";

// A minimal client-side PDF viewer: it loads the WASM core once, lets the user
// pick a PDF, and renders it with @fluxdocs/react. Nothing is uploaded — the
// document is processed entirely in the browser.
export function App() {
  const [runtime, setRuntime] = useState<FluxDocs | null>(null);
  const [pdf, setPdf] = useState<Uint8Array | null>(null);
  const [error, setError] = useState<string>("");

  useEffect(() => {
    fetch("/fluxdocs.wasm")
      .then((r) => r.arrayBuffer())
      .then((buf) => loadFluxDocs(buf))
      .then(setRuntime)
      .catch((e) => setError(String(e)));
  }, []);

  async function onPick(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (file) {
      setPdf(new Uint8Array(await file.arrayBuffer()));
    }
  }

  return (
    <main style={{ fontFamily: "system-ui", maxWidth: 720, margin: "2rem auto" }}>
      <h1>FluxDocs — client-side PDF viewer</h1>
      <p>Pick a PDF. It is rendered in your browser via WebAssembly — never uploaded.</p>
      {error && <p style={{ color: "crimson" }}>Failed to load the core: {error}</p>}
      {!runtime && !error && <p>Loading the WASM core…</p>}
      {runtime && <input type="file" accept="application/pdf" onChange={onPick} />}
      {runtime && pdf && (
        <div style={{ marginTop: "1rem", border: "1px solid #ddd", height: "70vh" }}>
          <FluxDocsViewer runtime={runtime} data={pdf} />
        </div>
      )}
    </main>
  );
}

// Command go-server-demo is a tiny HTTP service that embeds the FluxDocs core
// directly (no CGO, no external service): POST a PDF and get its text, a page
// thumbnail, or a redacted copy back. It shows the "real Go core" advantage —
// the same library that runs in the browser via WASM also runs server-side.
//
//	go run ./examples/go-server-demo            # listens on :8080
//	curl --data-binary @in.pdf localhost:8080/extract
//	curl --data-binary @in.pdf localhost:8080/thumbnail?page=0 -o page.png
//	curl --data-binary @in.pdf "localhost:8080/redact?pattern=SECRET" -o out.pdf
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/thai-pc/fluxdocs/core"
)

func main() {
	addr := ":8080"
	log.Printf("go-server-demo listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, NewServer()))
}

// NewServer returns the HTTP handler with the demo routes registered.
func NewServer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/extract", handleExtract)
	mux.HandleFunc("/thumbnail", handleThumbnail)
	mux.HandleFunc("/redact", handleRedact)
	return mux
}

// readDoc reads the request body as PDF bytes and opens it.
func readDoc(w http.ResponseWriter, r *http.Request) (*core.Document, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20)) // 64 MiB cap
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		http.Error(w, "open pdf: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return doc, true
}

// handleExtract returns the document's text as text/plain.
func handleExtract(w http.ResponseWriter, r *http.Request) {
	doc, ok := readDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()
	text, err := doc.ExtractText(core.ExtractOptions{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, text)
}

// handleThumbnail renders a page (?page=N, default 0) to PNG.
func handleThumbnail(w http.ResponseWriter, r *http.Request) {
	doc, ok := readDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()
	page := 0
	if v := r.URL.Query().Get("page"); v != "" {
		page, _ = strconv.Atoi(v)
	}
	dpi := 96
	if v := r.URL.Query().Get("dpi"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			dpi = n
		}
	}
	img, err := doc.RenderPage(page, core.RenderOptions{DPI: dpi, Format: "png"})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(img)
}

// handleRedact removes text matching ?pattern from every page and returns the
// redacted PDF.
func handleRedact(w http.ResponseWriter, r *http.Request) {
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		http.Error(w, "missing ?pattern", http.StatusBadRequest)
		return
	}
	doc, ok := readDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()
	for p := 0; p < doc.PageCount; p++ {
		if _, err := doc.RedactText(p, pattern); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	redacted, err := doc.RedactAndFlatten()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	if err := redacted.SaveTo(w); err != nil {
		log.Printf("write redacted: %v", err)
	}
}

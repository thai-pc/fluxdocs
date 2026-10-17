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
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// thumbnailTimeout bounds one /thumbnail render's wall clock, on top of the
// core's per-page work budget (a variable so tests can shorten it).
var thumbnailTimeout = 10 * time.Second

// maxThumbnailDPI caps ?dpi: a thumbnail endpoint has no use for print
// resolution, and the canvas (and the default budget) grows with DPI².
const maxThumbnailDPI = 300

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

// handleThumbnail renders a page (?page=N, default 0; ?dpi=N, default 96,
// clamped to maxThumbnailDPI) to PNG. The render runs under the core's per-page
// budget and a thumbnailTimeout deadline derived from the request context, so a
// client disconnect cancels it too:
//   - over budget (a page too complex to render): 422, a property of the
//     document, retrying does not help;
//   - past the deadline: 503, load-dependent, a retry may succeed;
//   - client gone: nothing is written.
//
// Error bodies are fixed text, never document content (SECURITY.md §4).
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
	if dpi > maxThumbnailDPI {
		dpi = maxThumbnailDPI
	}
	ctx, cancel := context.WithTimeout(r.Context(), thumbnailTimeout)
	defer cancel()
	img, err := doc.RenderPageContext(ctx, page, core.RenderOptions{DPI: dpi, Format: "png"})
	switch {
	case err == nil:
	case errors.Is(err, core.ErrRenderBudgetExceeded):
		http.Error(w, "page too complex to render", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "render timed out", http.StatusServiceUnavailable)
		return
	case errors.Is(err, context.Canceled):
		return // the client went away: nobody to answer
	case errors.Is(err, core.ErrInternal):
		log.Printf("thumbnail: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	default: // e.g. a page index out of range
		http.Error(w, "cannot render page", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(img)
}

// redactionRefused answers a redaction the core refused as unsafe
// (core.ErrRedactionUnsupported) with 422 and its reason code, and reports
// whether it did. Redaction fails closed on content other viewers could read
// differently; the error never holds document text.
func redactionRefused(w http.ResponseWriter, err error) bool {
	var re *core.RedactionError
	if !errors.As(err, &re) {
		return false
	}
	where := fmt.Sprintf("on page %d", re.Page)
	if re.Page < 0 {
		where = "for the document"
	}
	http.Error(w, fmt.Sprintf("redaction refused %s: %s", where, re.Reason), http.StatusUnprocessableEntity)
	return true
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
			if !redactionRefused(w, err) {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
	}
	redacted, err := doc.RedactAndFlatten()
	if err != nil {
		if !redactionRefused(w, err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	if err := redacted.SaveTo(w); err != nil {
		log.Printf("write redacted: %v", err)
	}
}

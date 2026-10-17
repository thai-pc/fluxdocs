package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/thai-pc/fluxdocs/core"
)

// maxUpload caps the request body for an uploaded PDF (defense against
// resource-exhaustion; the parser itself is also bounded — SECURITY.md §3).
const maxUpload = 50 << 20 // 50 MiB

// uploadDocument validates and stores an uploaded PDF, returning its id.
func (s *Server) uploadDocument(w http.ResponseWriter, r *http.Request) {
	data, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid PDF")
		return
	}
	defer doc.Close()

	id := newID()
	if err := s.docs.Put(r.Context(), orgID(r.Context()), id, data); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store document")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "pageCount": doc.PageCount, "title": doc.Title,
	})
}

// getDocument returns metadata for a stored document.
func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": chi.URLParam(r, "id"), "pageCount": doc.PageCount, "title": doc.Title,
	})
}

// extractText returns the document's extracted text.
func (s *Server) extractText(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()
	text, err := doc.ExtractText(core.ExtractOptions{})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "could not extract text")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// redactPII auto-redacts PII, stores the redacted document under a new id, and
// returns that id, the per-kind counts and auto_extended, the number of
// places the redaction was extended to beyond the matches (occurrences
// elsewhere, annotations, information fields; no document text). The
// optional JSON body {"kinds": [...]} selects kinds; omit for all.
func (s *Server) redactPII(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()

	var body struct {
		Kinds []string `json:"kinds"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body) // body optional

	counts, err := doc.AutoRedactPII(core.PIIKindsFromStrings(body.Kinds)...)
	if err != nil {
		if !writeRedactionUnsupported(w, err) {
			writeError(w, http.StatusUnprocessableEntity, "could not scan for PII")
		}
		return
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		if !writeRedactionUnsupported(w, err) {
			writeError(w, http.StatusInternalServerError, "redaction failed")
		}
		return
	}
	raw, err := out.Bytes() // leak-free verbatim output
	if err != nil {
		writeError(w, http.StatusInternalServerError, "redaction failed")
		return
	}
	id := newID()
	if err := s.docs.Put(r.Context(), orgID(r.Context()), id, raw); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store document")
		return
	}
	byKind := make(map[string]int, len(counts))
	for k, n := range counts {
		byKind[string(k)] = n
	}
	// auto_extended counts what the redaction removed beyond the PII matches
	// (core.RedactionReport, text-free): other places that held the removed
	// text, removed annotations (file attachments always) and document
	// information fields.
	report, _ := out.RedactionReport()
	extended := len(report.Extensions) + len(report.RemovedAnnotations) + len(report.RemovedInfoFields)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "redacted": byKind, "auto_extended": extended})
}

// writeRedactionUnsupported answers a redaction the core refused as unsafe
// (core.ErrRedactionUnsupported: content other readers could read
// differently, text in a route redaction does not rewrite yet, a budget) with
// 422 {"error": "redaction_unsupported", "reason": …, "page": …}, and reports
// whether err was such a refusal. page is the 0-based page index, or -1 when
// the refusal concerns the document as a whole. The reason
// and page carry no document text.
func writeRedactionUnsupported(w http.ResponseWriter, err error) bool {
	var re *core.RedactionError
	if !errors.As(err, &re) {
		return false
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  "redaction_unsupported",
		"reason": string(re.Reason),
		"page":   re.Page,
	})
	return true
}

// extractStructured runs AI structured extraction. It is 501 until an AI backend
// is configured on the Server.
func (s *Server) extractStructured(w http.ResponseWriter, r *http.Request) {
	if s.ai == nil {
		writeError(w, http.StatusNotImplemented, "AI backend not configured")
		return
	}
	var body struct {
		Fields []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Required    bool   `json:"required"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || len(body.Fields) == 0 {
		writeError(w, http.StatusBadRequest, "a non-empty 'fields' array is required")
		return
	}
	doc, ok := s.loadDoc(w, r)
	if !ok {
		return
	}
	defer doc.Close()

	schema := core.Schema{Fields: make([]core.SchemaField, len(body.Fields))}
	for i, f := range body.Fields {
		schema.Fields[i] = core.SchemaField{Name: f.Name, Description: f.Description, Type: f.Type, Required: f.Required}
	}
	doc.SetAIClient(s.ai)
	result, err := doc.ExtractStructured(schema)
	if err != nil {
		writeError(w, http.StatusBadGateway, "extraction failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": result})
}

// loadDoc fetches the org-scoped document named by the {id} path param and opens
// it. It writes a 404 (never leaking whether the id exists for another org) and
// returns ok=false on any failure.
func (s *Server) loadDoc(w http.ResponseWriter, r *http.Request) (*core.Document, bool) {
	id := chi.URLParam(r, "id")
	data, err := s.docs.Get(r.Context(), orgID(r.Context()), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "document not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load document")
		}
		return nil, false
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stored document is corrupt")
		return nil, false
	}
	return doc, true
}

// readBody reads up to maxUpload+1 bytes; it errors if the body exceeds the cap.
func readBody(r *http.Request) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxUpload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUpload {
		return nil, errors.New("body too large")
	}
	return data, nil
}

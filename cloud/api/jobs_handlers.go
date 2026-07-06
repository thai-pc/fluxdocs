package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/thai-pc/fluxdocs/core"
)

// enqueueRedactPII schedules PII auto-redaction as a background job and returns
// its id (202 Accepted). Poll GET /v1/jobs/{jobID} for the result. Optional body
// {"kinds": [...]} selects PII kinds.
func (s *Server) enqueueRedactPII(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		writeError(w, http.StatusNotImplemented, "async jobs are not enabled")
		return
	}
	org := orgID(r.Context())
	id := chi.URLParam(r, "id")
	if _, err := s.docs.Get(r.Context(), org, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "document not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load document")
		}
		return
	}

	var body struct {
		Kinds []string `json:"kinds"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)

	jobID, err := s.queue.Enqueue(r.Context(), org, "redact_pii", map[string]any{
		"document_id": id,
		"kinds":       body.Kinds,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not enqueue job")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "status": string(JobPending)})
}

// getJob returns the status (and result document id, once complete) of a job the
// caller's org owns.
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	job, err := s.queue.Get(r.Context(), orgID(r.Context()), chi.URLParam(r, "jobID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	resp := map[string]any{"job_id": job.ID, "kind": job.Kind, "status": string(job.Status)}
	if job.ResultDocID != "" {
		resp["result_document_id"] = job.ResultDocID
	}
	if job.Error != "" {
		resp["error"] = job.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

// runRedactPIIJob is the handler registered for the "redact_pii" job kind. It
// loads the org's document, auto-redacts PII, and stores the redacted result as
// a new document, returning its id. The redacted bytes are the leak-free
// verbatim output (out.Bytes(), per the v0.3.1 fix).
func (s *Server) runRedactPIIJob(ctx context.Context, org string, args map[string]any) (string, error) {
	docID, _ := args["document_id"].(string)
	var kinds []string
	if ks, ok := args["kinds"].([]string); ok {
		kinds = ks
	}
	data, err := s.docs.Get(ctx, org, docID)
	if err != nil {
		return "", err
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		return "", err
	}
	defer doc.Close()
	if _, err := doc.AutoRedactPII(core.PIIKindsFromStrings(kinds)...); err != nil {
		return "", err
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		return "", err
	}
	raw, err := out.Bytes()
	if err != nil {
		return "", err
	}
	newDocID := newID()
	if err := s.docs.Put(ctx, org, newDocID, raw); err != nil {
		return "", err
	}
	return newDocID, nil
}

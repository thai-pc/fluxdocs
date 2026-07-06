package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/thai-pc/fluxdocs/core"
)

// createShare mints a share link for a document the caller's org owns. Body
// (optional): {"password": "...", "expires_in_seconds": N}. The password is
// stored only as a bcrypt hash; the plaintext is never persisted or logged.
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	if s.shares == nil {
		writeError(w, http.StatusNotImplemented, "share links are not enabled")
		return
	}
	org := orgID(r.Context())
	id := chi.URLParam(r, "id")

	// Confirm the document exists for this org before sharing it.
	if _, err := s.docs.Get(r.Context(), org, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "document not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load document")
		}
		return
	}

	var body struct {
		Password         string `json:"password"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body) // body optional

	link := ShareLink{
		Token:      newToken(),
		OrgID:      org,
		DocumentID: id,
		CreatedAt:  time.Now().UTC(),
	}
	if body.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not secure the password")
			return
		}
		link.PasswordHash = string(hash)
	}
	if body.ExpiresInSeconds > 0 {
		link.ExpiresAt = time.Now().UTC().Add(time.Duration(body.ExpiresInSeconds) * time.Second)
	}

	if err := s.shares.CreateShare(r.Context(), link); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create share link")
		return
	}

	resp := map[string]any{"token": link.Token, "url": "/shared/" + link.Token}
	if !link.ExpiresAt.IsZero() {
		resp["expires_at"] = link.ExpiresAt.Format(time.RFC3339)
	}
	if link.PasswordHash != "" {
		resp["password_protected"] = true
	}
	writeJSON(w, http.StatusCreated, resp)
}

// getShared serves a document to an unauthenticated holder of a valid share
// token, enforcing the optional password and expiry. The password is read from
// the X-Share-Password header or a ?password query parameter.
func (s *Server) getShared(w http.ResponseWriter, r *http.Request) {
	if s.shares == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	token := chi.URLParam(r, "token")
	link, err := s.shares.GetShare(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found") // unknown token — don't distinguish
		return
	}
	if link.Expired(time.Now()) {
		writeError(w, http.StatusGone, "this share link has expired")
		return
	}
	if link.PasswordHash != "" {
		pw := r.Header.Get("X-Share-Password")
		if pw == "" {
			pw = r.URL.Query().Get("password")
		}
		if bcrypt.CompareHashAndPassword([]byte(link.PasswordHash), []byte(pw)) != nil {
			writeError(w, http.StatusUnauthorized, "a valid password is required")
			return
		}
	}

	// Resolve the document via the LINK's org — the anonymous caller never
	// supplies one, so isolation still holds.
	data, err := s.docs.Get(r.Context(), link.OrgID, link.DocumentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "the shared document is no longer available")
		return
	}
	if _, err := core.OpenBytes(data); err != nil {
		writeError(w, http.StatusInternalServerError, "the shared document is corrupt")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

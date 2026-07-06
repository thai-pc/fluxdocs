package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/thai-pc/fluxdocs/core"
)

// ctxKey is an unexported context key type so values can't collide with other
// packages'.
type ctxKey int

const orgIDKey ctxKey = iota

// Server is the Cloud HTTP backend. Construct it with New and serve its Handler.
type Server struct {
	docs   DocumentStore
	keys   KeyStore
	shares ShareStore
	ai     core.AIClient // optional; the AI endpoints 501 when nil
	log    *slog.Logger
	router http.Handler
}

// New builds a Server. shares may be nil (the share endpoints then 501). ai may
// be nil (the AI endpoints report they are unconfigured). log may be nil (a
// discarding logger is used).
func New(docs DocumentStore, keys KeyStore, shares ShareStore, ai core.AIClient, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	s := &Server{docs: docs, keys: keys, shares: shares, ai: ai, log: log}
	s.router = s.routes()
	return s
}

// Handler returns the HTTP handler for the whole API.
func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestLog)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Public: fetch a document by an unguessable share token (no API key). The
	// token, plus an optional password and expiry, is the only credential.
	r.Get("/shared/{token}", s.getShared)

	// Everything under /v1 requires a valid API key and is org-scoped.
	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.Post("/documents", s.uploadDocument)
		r.Get("/documents/{id}", s.getDocument)
		r.Post("/documents/{id}/extract", s.extractText)
		r.Post("/documents/{id}/redact-pii", s.redactPII)
		r.Post("/documents/{id}/extract-structured", s.extractStructured)
		r.Post("/documents/{id}/shares", s.createShare)
	})
	return r
}

// HashKey returns the hex SHA-256 of a raw API key. Only the hash is ever stored
// or compared (SECURITY.md §4).
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// newID returns a random 128-bit hex identifier for a stored document.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newToken returns a random 256-bit hex share token — large enough that it can't
// be guessed, since it is the sole credential for a public share link.
func newToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- middleware ---

// authenticate resolves the org from the request's API key and stashes it in the
// context. A missing or unknown key is a 401. The key itself is never logged.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := apiKeyFromRequest(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key")
			return
		}
		orgID, ok := s.keys.OrgForKeyHash(r.Context(), HashKey(key))
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		ctx := context.WithValue(r.Context(), orgIDKey, orgID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// apiKeyFromRequest reads the key from Authorization: Bearer <key> or X-Api-Key.
func apiKeyFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// orgID returns the authenticated org from the context.
func orgID(ctx context.Context) string {
	org, _ := ctx.Value(orgIDKey).(string)
	return org
}

// requestLog logs method, path, status, and duration — never headers or bodies,
// which may carry API keys or document content (SECURITY.md §4).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method, "path", redactPath(r.URL.Path),
			"status", sw.status, "dur_ms", time.Since(start).Milliseconds())
	})
}

// redactPath hides the secret share token from logs: a share URL is
// /shared/<token>, and the token is a credential (SECURITY.md §4).
func redactPath(path string) string {
	if strings.HasPrefix(path, "/shared/") {
		return "/shared/[token]"
	}
	return path
}

// recoverer turns a handler panic into a 500 instead of crashing the process.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", rec)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- small helpers ---

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// discard is an io.Writer that drops everything (default logger sink).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

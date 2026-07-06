package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
)

// shareServer builds a server with in-memory stores the test can reach directly,
// plus an uploaded document. Returns the server, the share store, and the doc id.
func shareServer(t *testing.T) (*Server, *MemShareStore, string) {
	t.Helper()
	keys := NewMemKeyStore()
	keys.Add(HashKey("k"), "org-a")
	shares := NewMemShareStore()
	s := New(NewMemDocumentStore(), keys, shares, nil, nil)
	_, up := do(t, s, http.MethodPost, "/v1/documents", "k", textPDF("Shared content"))
	return s, shares, up["id"].(string)
}

// doRaw performs a request and returns status, content-type, and raw body.
func doRaw(t *testing.T, s *Server, method, path string, header map[string]string) (int, string, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Code, rr.Header().Get("Content-Type"), rr.Body.Bytes()
}

func TestShareCreateAndFetch(t *testing.T) {
	s, _, id := shareServer(t)
	code, body := do(t, s, http.MethodPost, "/v1/documents/"+id+"/shares", "k", nil)
	if code != http.StatusCreated {
		t.Fatalf("create share = %d %v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("no token returned")
	}

	// Fetch it with no API key — the token is the only credential.
	status, ct, raw := doRaw(t, s, http.MethodGet, "/shared/"+token, nil)
	if status != http.StatusOK {
		t.Fatalf("fetch shared = %d", status)
	}
	if ct != "application/pdf" || !bytes.HasPrefix(raw, []byte("%PDF")) {
		t.Errorf("content-type=%q, prefix=%q", ct, raw[:min(4, len(raw))])
	}
}

func TestSharePasswordProtected(t *testing.T) {
	s, _, id := shareServer(t)
	_, body := do(t, s, http.MethodPost, "/v1/documents/"+id+"/shares", "k",
		[]byte(`{"password":"hunter2"}`))
	token := body["token"].(string)
	if body["password_protected"] != true {
		t.Errorf("expected password_protected=true, got %v", body)
	}

	// No password -> 401.
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/"+token, nil); status != http.StatusUnauthorized {
		t.Errorf("no password: status=%d, want 401", status)
	}
	// Wrong password -> 401.
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/"+token, map[string]string{"X-Share-Password": "wrong"}); status != http.StatusUnauthorized {
		t.Errorf("wrong password: status=%d, want 401", status)
	}
	// Correct password via header -> 200.
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/"+token, map[string]string{"X-Share-Password": "hunter2"}); status != http.StatusOK {
		t.Errorf("header password: status=%d, want 200", status)
	}
	// Correct password via query -> 200.
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/"+token+"?password=hunter2", nil); status != http.StatusOK {
		t.Errorf("query password: status=%d, want 200", status)
	}
}

func TestShareExpired(t *testing.T) {
	s, shares, id := shareServer(t)
	// Inject an already-expired link directly (the API only mints future ones).
	token := "expired-token"
	shares.CreateShare(context.Background(), ShareLink{
		Token: token, OrgID: "org-a", DocumentID: id,
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/"+token, nil); status != http.StatusGone {
		t.Errorf("expired share: status=%d, want 410", status)
	}
}

func TestShareUnknownToken(t *testing.T) {
	s, _, _ := shareServer(t)
	if status, _, _ := doRaw(t, s, http.MethodGet, "/shared/does-not-exist", nil); status != http.StatusNotFound {
		t.Errorf("unknown token: status=%d, want 404", status)
	}
}

func TestCreateShareMissingDoc(t *testing.T) {
	s, _, _ := shareServer(t)
	if code, _ := do(t, s, http.MethodPost, "/v1/documents/nope/shares", "k", nil); code != http.StatusNotFound {
		t.Errorf("share of missing doc: code=%d, want 404", code)
	}
}

func TestRedactPath(t *testing.T) {
	if got := redactPath("/shared/deadbeefsecrettoken"); got != "/shared/[token]" {
		t.Errorf("redactPath = %q, want /shared/[token]", got)
	}
	if got := redactPath("/v1/documents/abc"); got != "/v1/documents/abc" {
		t.Errorf("redactPath mangled a non-share path: %q", got)
	}
}

func TestPostgresShareStore(t *testing.T) {
	db, mock := newMockDB(t)
	s := NewPostgresShareStore(db)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	exp := time.Now().Add(time.Hour).UTC()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO share_links`)).
		WithArgs("tok", "org-a", "doc1", string(hash), exp).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := s.CreateShare(context.Background(), ShareLink{
		Token: "tok", OrgID: "org-a", DocumentID: "doc1",
		PasswordHash: string(hash), ExpiresAt: exp,
	}); err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// GetShare with a NULL expiry maps to a zero ExpiresAt (never expires).
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT org_id, document_id, password_hash, expires_at, created_at`)).
		WithArgs("tok").
		WillReturnRows(sqlmock.NewRows([]string{"org_id", "document_id", "password_hash", "expires_at", "created_at"}).
			AddRow("org-a", "doc1", "", nil, time.Now()))
	link, err := s.GetShare(context.Background(), "tok")
	if err != nil {
		t.Fatalf("GetShare: %v", err)
	}
	if link.OrgID != "org-a" || !link.ExpiresAt.IsZero() {
		t.Errorf("link = %+v", link)
	}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT org_id`)).WithArgs("missing").
		WillReturnError(sql.ErrNoRows)
	if _, err := s.GetShare(context.Background(), "missing"); err != ErrNotFound {
		t.Errorf("missing token err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

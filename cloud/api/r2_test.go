package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockS3 is an in-memory S3-compatible endpoint that also asserts each request
// is SigV4-signed and carries a correct payload hash.
type mockS3 struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
}

func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Assert the request is signed the way an S3 server expects.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
		m.t.Errorf("bad Authorization header: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		m.t.Errorf("SignedHeaders missing/incorrect: %q", auth)
	}
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hex.EncodeToString(sum[:]) {
		m.t.Errorf("payload hash = %q, want %q", got, hex.EncodeToString(sum[:]))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		m.objects[r.URL.Path] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, ok := m.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newR2(t *testing.T) (*R2DocumentStore, *mockS3) {
	t.Helper()
	m := &mockS3{t: t, objects: map[string][]byte{}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	store := NewR2DocumentStore(R2Config{
		Endpoint: srv.URL, Bucket: "docs", Region: "auto",
		AccessKey: "AKIATEST", SecretKey: "secret",
	}, srv.Client())
	return store, m
}

func TestR2PutGetRoundTrip(t *testing.T) {
	store, m := newR2(t)
	ctx := context.Background()

	if err := store.Put(ctx, "org-a", "doc1", []byte("PDF-bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The object must be stored under an org-scoped key.
	if _, ok := m.objects["/docs/org-a/doc1"]; !ok {
		t.Fatalf("object not stored at /docs/org-a/doc1; keys=%v", keysOf(m))
	}

	got, err := store.Get(ctx, "org-a", "doc1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "PDF-bytes" {
		t.Errorf("Get = %q", got)
	}
}

func TestR2GetNotFound(t *testing.T) {
	store, _ := newR2(t)
	if _, err := store.Get(context.Background(), "org-a", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestR2OrgIsolation(t *testing.T) {
	store, _ := newR2(t)
	ctx := context.Background()
	if err := store.Put(ctx, "org-a", "doc1", []byte("a-secret")); err != nil {
		t.Fatal(err)
	}
	// A different org resolves a different key -> not found, never org-a's bytes.
	if _, err := store.Get(ctx, "org-b", "doc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get err = %v, want ErrNotFound", err)
	}
}

func TestAWSEncode(t *testing.T) {
	// Unreserved characters pass through; others are percent-encoded uppercase.
	if got := encodePath("/docs/org a/id+1"); got != "/docs/org%20a/id%2B1" {
		t.Errorf("encodePath = %q", got)
	}
}

func keysOf(m *mockS3) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ks []string
	for k := range m.objects {
		ks = append(ks, k)
	}
	return ks
}

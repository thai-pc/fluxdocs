package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// --- fixtures ---

func textPDF(text string) []byte {
	return contentPDF("BT /F1 12 Tf 72 700 Td (" + text + ") Tj ET")
}

// contentPDF wraps an arbitrary content stream body in a one-page PDF.
func contentPDF(content string) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	off := map[int]int{}
	for i := 1; i <= 5; i++ {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

// stubAI implements core.AIClient without a network.
type stubAI struct{ fields map[string]any }

func (s stubAI) Extract(context.Context, string, core.Schema) (map[string]any, error) {
	return s.fields, nil
}
func (stubAI) Ask(context.Context, string, string) (string, error) { return "", nil }

// testServer wires an in-memory server with one API key for "org-a".
func testServer(t *testing.T, ai core.AIClient) (*Server, string) {
	t.Helper()
	keys := NewMemKeyStore()
	const key = "sk-test-abc"
	keys.Add(HashKey(key), "org-a")
	return New(NewMemDocumentStore(), keys, NewMemShareStore(), ai, nil), key
}

// do performs a request with the given key and returns status + decoded body.
func do(t *testing.T, s *Server, method, path, key string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestHealthz(t *testing.T) {
	s, _ := testServer(t, nil)
	code, body := do(t, s, http.MethodGet, "/healthz", "", nil)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
}

func TestAuthRequired(t *testing.T) {
	s, _ := testServer(t, nil)
	if code, _ := do(t, s, http.MethodPost, "/v1/documents", "", textPDF("x")); code != http.StatusUnauthorized {
		t.Errorf("no key: code = %d, want 401", code)
	}
	if code, _ := do(t, s, http.MethodPost, "/v1/documents", "wrong-key", textPDF("x")); code != http.StatusUnauthorized {
		t.Errorf("bad key: code = %d, want 401", code)
	}
}

func TestUploadGetExtract(t *testing.T) {
	s, key := testServer(t, nil)
	code, body := do(t, s, http.MethodPost, "/v1/documents", key, textPDF("Hello Cloud"))
	if code != http.StatusCreated {
		t.Fatalf("upload = %d %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" || body["pageCount"].(float64) != 1 {
		t.Fatalf("upload body = %v", body)
	}

	code, meta := do(t, s, http.MethodGet, "/v1/documents/"+id, key, nil)
	if code != http.StatusOK || meta["pageCount"].(float64) != 1 {
		t.Fatalf("get = %d %v", code, meta)
	}

	code, ext := do(t, s, http.MethodPost, "/v1/documents/"+id+"/extract", key, nil)
	if code != http.StatusOK || !strings.Contains(ext["text"].(string), "Hello Cloud") {
		t.Fatalf("extract = %d %v", code, ext)
	}
}

func TestOrgIsolation(t *testing.T) {
	// Two keys, two orgs, one shared server.
	keys := NewMemKeyStore()
	keys.Add(HashKey("key-a"), "org-a")
	keys.Add(HashKey("key-b"), "org-b")
	s := New(NewMemDocumentStore(), keys, NewMemShareStore(), nil, nil)

	_, body := do(t, s, http.MethodPost, "/v1/documents", "key-a", textPDF("org-a secret"))
	id := body["id"].(string)

	// org-b must not be able to read org-a's document — 404, not 200.
	if code, _ := do(t, s, http.MethodGet, "/v1/documents/"+id, "key-b", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org read: code = %d, want 404", code)
	}
	// org-a still can.
	if code, _ := do(t, s, http.MethodGet, "/v1/documents/"+id, "key-a", nil); code != http.StatusOK {
		t.Fatalf("same-org read: code = %d, want 200", code)
	}
}

func TestRedactPIIEndpoint(t *testing.T) {
	s, key := testServer(t, nil)
	// SSN and public text in separate text-showing operators so removing the
	// SSN run leaves the public one.
	pdf := contentPDF("BT /F1 12 Tf 72 700 Td (SSN 123-45-6789) Tj 0 -20 Td (public text) Tj ET")
	_, up := do(t, s, http.MethodPost, "/v1/documents", key, pdf)
	id := up["id"].(string)

	code, red := do(t, s, http.MethodPost, "/v1/documents/"+id+"/redact-pii", key, []byte(`{"kinds":["ssn"]}`))
	if code != http.StatusCreated {
		t.Fatalf("redact-pii = %d %v", code, red)
	}
	newID := red["id"].(string)
	if newID == id {
		t.Fatal("redaction must produce a new document id")
	}

	_, ext := do(t, s, http.MethodPost, "/v1/documents/"+newID+"/extract", key, nil)
	text := ext["text"].(string)
	if strings.Contains(text, "123-45-6789") {
		t.Errorf("LEAK: SSN survived redaction via the API: %q", text)
	}
	if !strings.Contains(text, "public text") {
		t.Errorf("over-redacted: %q", text)
	}
}

func TestExtractStructuredEndpoint(t *testing.T) {
	s, key := testServer(t, stubAI{fields: map[string]any{"vendor": "Acme"}})
	_, up := do(t, s, http.MethodPost, "/v1/documents", key, textPDF("Invoice from Acme"))
	id := up["id"].(string)

	code, res := do(t, s, http.MethodPost, "/v1/documents/"+id+"/extract-structured", key,
		[]byte(`{"fields":[{"name":"vendor","description":"seller"}]}`))
	if code != http.StatusOK {
		t.Fatalf("extract-structured = %d %v", code, res)
	}
	fields := res["fields"].(map[string]any)
	if fields["vendor"] != "Acme" {
		t.Errorf("fields = %v", fields)
	}
}

func TestExtractStructuredUnconfigured(t *testing.T) {
	s, key := testServer(t, nil) // no AI backend
	_, up := do(t, s, http.MethodPost, "/v1/documents", key, textPDF("x"))
	id := up["id"].(string)
	if code, _ := do(t, s, http.MethodPost, "/v1/documents/"+id+"/extract-structured", key,
		[]byte(`{"fields":[{"name":"x"}]}`)); code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501", code)
	}
}

func TestUploadRejectsGarbage(t *testing.T) {
	s, key := testServer(t, nil)
	if code, _ := do(t, s, http.MethodPost, "/v1/documents", key, []byte("not a pdf")); code != http.StatusBadRequest {
		t.Fatalf("garbage upload: code = %d, want 400", code)
	}
}

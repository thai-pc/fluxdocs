package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// secretPDF builds a one-page PDF with a secret and public text.
func secretPDF() []byte {
	var b bytes.Buffer
	content := "BT /F1 12 Tf 100 700 Td (SECRET token) Tj 0 -100 Td (public info) Tj ET"
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
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

func post(t *testing.T, srv http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestExtractEndpoint(t *testing.T) {
	srv := NewServer()
	rr := post(t, srv, "/extract", secretPDF())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "SECRET token") {
		t.Errorf("extract body = %q", rr.Body.String())
	}
}

func TestThumbnailEndpoint(t *testing.T) {
	srv := NewServer()
	rr := post(t, srv, "/thumbnail?page=0&dpi=72", secretPDF())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if b := rr.Body.Bytes(); len(b) < 8 || b[0] != 0x89 || b[1] != 'P' {
		t.Errorf("thumbnail is not a PNG")
	}
}

func TestRedactEndpoint(t *testing.T) {
	srv := NewServer()
	rr := post(t, srv, "/redact?pattern=SECRET", secretPDF())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.Bytes()
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Errorf("redact response is not a PDF")
	}
	if bytes.Contains(out, []byte("SECRET token")) {
		t.Errorf("LEAK: redacted PDF still contains the secret")
	}
}

func TestBadInput(t *testing.T) {
	srv := NewServer()
	if rr := post(t, srv, "/extract", []byte("not a pdf")); rr.Code != http.StatusBadRequest {
		t.Errorf("bad pdf status = %d, want 400", rr.Code)
	}
	if rr := post(t, srv, "/redact", secretPDF()); rr.Code != http.StatusBadRequest {
		t.Errorf("missing pattern status = %d, want 400", rr.Code)
	}
}

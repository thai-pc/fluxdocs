package main

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// floodPDF builds a one-page PDF (w x h pt) whose content repeats a full-page
// fill n times: cheap to send, but n canvases of raster work to render.
func floodPDF(w, h, n int) []byte {
	var b bytes.Buffer
	content := strings.Repeat(fmt.Sprintf("0 0 %d %d re f\n", w, h), n)
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 %d %d] >>", w, h),
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	b.WriteString("%PDF-1.7\n")
	off := map[int]int{}
	for i := 1; i <= 4; i++ {
		off[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 5\n0000000000 65535 f \n")
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", off[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

// TestThumbnailBudgetExceeded: a page over the render budget is a property of
// the document (422), with a fixed message, never document content.
func TestThumbnailBudgetExceeded(t *testing.T) {
	// Lift the deadline so the outcome cannot depend on host speed: under -race
	// on a loaded host the 10 s default can fire (503) before the budget trips.
	old := thumbnailTimeout
	thumbnailTimeout = time.Hour
	t.Cleanup(func() { thumbnailTimeout = old })
	rr := post(t, NewServer(), "/thumbnail?page=0&dpi=72", floodPDF(300, 800, 500))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "page too complex to render" {
		t.Errorf("body = %q, want the fixed message", got)
	}
}

// TestThumbnailBadPage: any other render failure (here a page index out of
// range) is a 400 with fixed text, never the error string.
func TestThumbnailBadPage(t *testing.T) {
	rr := post(t, NewServer(), "/thumbnail?page=99&dpi=72", secretPDF())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "cannot render page" {
		t.Errorf("body = %q, want the fixed message", got)
	}
}

// TestThumbnailDeadline: a render past the server deadline is load-dependent
// (503), not a budget error.
func TestThumbnailDeadline(t *testing.T) {
	old := thumbnailTimeout
	// A deadline that has passed before the render starts, so the outcome
	// does not depend on host speed: the render's first poll reports it, long
	// before the default budget of a 2000 pt page at 150 DPI (≈ 556 M pixel
	// visits) could trip. A 50 ms deadline raced the budget under -race on a
	// loaded host.
	thumbnailTimeout = time.Nanosecond
	t.Cleanup(func() { thumbnailTimeout = old })

	start := time.Now()
	rr := post(t, NewServer(), "/thumbnail?page=0&dpi=150", floodPDF(2000, 2000, 100))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rr.Code, rr.Body.String())
	}
	// A generous sanity bound only: the poll interval keeps the abort prompt.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("request took %v after its deadline, want an abort at the first poll", elapsed)
	}
}

// TestThumbnailClientGone: when the client has disconnected, nothing is written.
func TestThumbnailClientGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/thumbnail?page=0&dpi=150", bytes.NewReader(floodPDF(2000, 2000, 100))).WithContext(ctx)
	rr := httptest.NewRecorder()
	NewServer().ServeHTTP(rr, req)
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
		t.Errorf("wrote %q (Content-Type %q) to a gone client, want nothing", rr.Body.String(), rr.Header().Get("Content-Type"))
	}
}

// TestThumbnailClampsDPI: an absurd ?dpi is clamped to maxThumbnailDPI rather
// than hitting the canvas cap.
func TestThumbnailClampsDPI(t *testing.T) {
	rr := post(t, NewServer(), "/thumbnail?page=0&dpi=100000", secretPDF())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	img, err := png.Decode(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 300 x 800 pt at 300 DPI.
	if b := img.Bounds(); b.Dx() != 1250 || b.Dy() != 3334 {
		t.Errorf("thumbnail = %dx%d, want 1250x3334 (dpi clamped to %d)", b.Dx(), b.Dy(), maxThumbnailDPI)
	}
}

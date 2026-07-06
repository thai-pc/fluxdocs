package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// buildTextPDF assembles a one-page PDF whose content stream is `content`.
func buildTextPDF(content string) []byte {
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

// mockServer returns a test server that runs handler, plus a Client pointed at
// it with a dummy key (so no real network or ANTHROPIC_API_KEY is touched).
func mockServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New("test-key")
	c.BaseURL = srv.URL
	return c, srv
}

func TestExtract(t *testing.T) {
	var gotBody request
	c, _ := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("missing/invalid headers: %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		// The model is forced to call the extraction tool.
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"stop_reason":"tool_use","content":[
			{"type":"tool_use","name":"record_extraction","input":{"vendor":"Acme","total":42}}
		]}`)
	})

	out, err := c.Extract(context.Background(), "Invoice from Acme, total 42",
		core.Schema{Fields: []core.SchemaField{
			{Name: "vendor", Description: "the seller", Required: true},
			{Name: "total", Type: "number"},
		}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out["vendor"] != "Acme" || out["total"].(float64) != 42 {
		t.Errorf("result = %v", out)
	}

	// Request shape: forced tool_choice, strict tool, schema translated.
	if gotBody.Model != defaultModel {
		t.Errorf("model = %q, want %q", gotBody.Model, defaultModel)
	}
	if gotBody.ToolChoice == nil || gotBody.ToolChoice.Type != "tool" || gotBody.ToolChoice.Name != extractToolName {
		t.Errorf("tool_choice = %+v", gotBody.ToolChoice)
	}
	if len(gotBody.Tools) != 1 || !gotBody.Tools[0].Strict {
		t.Fatalf("tool not strict: %+v", gotBody.Tools)
	}
	schema := gotBody.Tools[0].InputSchema
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "vendor" {
		t.Errorf("required = %v, want [vendor]", schema["required"])
	}
	if !strings.Contains(gotBody.Messages[0].Content, "Invoice from Acme") {
		t.Errorf("document text not in request: %q", gotBody.Messages[0].Content)
	}
}

func TestAsk(t *testing.T) {
	c, _ := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body request
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if len(body.Tools) != 0 {
			t.Errorf("Ask should not send tools: %+v", body.Tools)
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"stop_reason":"end_turn","content":[
			{"type":"text","text":"The total is 42 dollars."}
		]}`)
	})

	ans, err := c.Ask(context.Background(), "Total: 42 USD", "How much is the total?")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans != "The total is 42 dollars." {
		t.Errorf("answer = %q", ans)
	}
}

func TestErrorStatus(t *testing.T) {
	c, _ := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	_, err := c.Ask(context.Background(), "doc", "q")
	if err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("err = %v, want the API error message", err)
	}
}

func TestNoAPIKey(t *testing.T) {
	c := New("")
	c.APIKey = "" // ensure empty even if the environment set ANTHROPIC_API_KEY
	if _, err := c.Ask(context.Background(), "doc", "q"); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestMissingToolCall(t *testing.T) {
	// If the model answers with text instead of the tool call, Extract errors
	// rather than returning a bogus empty map.
	c, _ := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"I cannot."}]}`)
	})
	if _, err := c.Extract(context.Background(), "doc", core.Schema{Fields: []core.SchemaField{{Name: "x"}}}); err == nil {
		t.Fatal("expected an error when the tool call is missing")
	}
}

// TestEndToEndViaDocument wires the client into a core.Document exactly as a
// Cloud build would, and confirms ExtractStructured flows document text through
// the HTTP client to a structured result.
func TestEndToEndViaDocument(t *testing.T) {
	c, _ := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "CONFIDENTIAL") {
			t.Errorf("document text not forwarded to API: %s", body)
		}
		io.WriteString(w, `{"content":[{"type":"tool_use","name":"record_extraction","input":{"classification":"CONFIDENTIAL"}}]}`)
	})

	content := "BT /F1 12 Tf 72 700 Td (CONFIDENTIAL report) Tj ET"
	pdf := buildTextPDF(content)
	doc, err := core.OpenBytes(pdf)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	doc.SetAIClient(c)

	out, err := doc.ExtractStructured(core.Schema{Fields: []core.SchemaField{{Name: "classification"}}})
	if err != nil {
		t.Fatalf("ExtractStructured: %v", err)
	}
	if out["classification"] != "CONFIDENTIAL" {
		t.Errorf("result = %v", out)
	}
}

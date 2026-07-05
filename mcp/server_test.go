package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// --- fixtures ---

func buildPDF(objects map[int]string, trailer string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.7\n")
	n := len(objects)
	for i := 1; i <= n; i++ {
		off[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i, objects[i]))
	}
	xref := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", n+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n" + trailer + "\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return b.Bytes()
}

func stream(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func writeSecretPDF(t *testing.T) string {
	t.Helper()
	content := "BT /F1 12 Tf 100 700 Td (SECRET-42) Tj 0 -100 Td (public) Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "in.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- JSON-RPC harness ---

// rpc sends a request through Handle and returns the response as a generic map
// (nil when the message is a notification with no response).
func rpc(t *testing.T, s *Server, id int, method string, params any) map[string]any {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if id >= 0 {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	resp, send := s.Handle(b)
	if !send {
		return nil
	}
	rb, _ := json.Marshal(resp)
	var m map[string]any
	if err := json.Unmarshal(rb, &m); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return m
}

// callTool invokes tools/call and returns the first content item and isError.
func callTool(t *testing.T, s *Server, name string, args map[string]any) (item map[string]any, isError bool) {
	t.Helper()
	m := rpc(t, s, 1, "tools/call", map[string]any{"name": name, "arguments": args})
	if m["error"] != nil {
		t.Fatalf("tools/call %s returned protocol error: %v", name, m["error"])
	}
	res := m["result"].(map[string]any)
	isError, _ = res["isError"].(bool)
	content := res["content"].([]any)
	if len(content) == 0 {
		return nil, isError
	}
	return content[0].(map[string]any), isError
}

// --- tests ---

func TestInitialize(t *testing.T) {
	s := NewServer()
	m := rpc(t, s, 1, "initialize", nil)
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

func TestToolsList(t *testing.T) {
	s := NewServer()
	m := rpc(t, s, 2, "tools/list", nil)
	toolsArr := m["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tt := range toolsArr {
		names[tt.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"open_document", "extract_text", "render_page", "redact_text", "redact_and_save"} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
}

func TestNotificationHasNoResponse(t *testing.T) {
	s := NewServer()
	if m := rpc(t, s, -1, "notifications/initialized", nil); m != nil {
		t.Errorf("notification produced a response: %v", m)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	s := NewServer()
	m := rpc(t, s, 3, "bogus/method", nil)
	if m["error"] == nil {
		t.Errorf("unknown method should return an error")
	}
	m2 := rpc(t, s, 4, "tools/call", map[string]any{"name": "no_such_tool", "arguments": map[string]any{}})
	if m2["error"] == nil {
		t.Errorf("unknown tool should return an error")
	}
}

func TestOpenExtractRenderInfo(t *testing.T) {
	s := NewServer()
	path := writeSecretPDF(t)

	item, isErr := callTool(t, s, "open_document", map[string]any{"path": path})
	if isErr {
		t.Fatalf("open_document error: %v", item)
	}
	var opened struct {
		Handle    int `json:"handle"`
		PageCount int `json:"pageCount"`
	}
	json.Unmarshal([]byte(item["text"].(string)), &opened)
	if opened.PageCount != 1 {
		t.Fatalf("pageCount = %d", opened.PageCount)
	}
	h := opened.Handle

	txt, _ := callTool(t, s, "extract_text", map[string]any{"handle": h})
	if !strings.Contains(txt["text"].(string), "SECRET-42") {
		t.Errorf("extract_text = %q", txt["text"])
	}

	img, isErr := callTool(t, s, "render_page", map[string]any{"handle": h, "page": 0, "dpi": 36})
	if isErr || img["type"] != "image" || img["mimeType"] != "image/png" || len(img["data"].(string)) == 0 {
		t.Errorf("render_page = %v (isErr=%v)", img, isErr)
	}

	info, _ := callTool(t, s, "document_info", map[string]any{"handle": h})
	if !strings.Contains(info["text"].(string), "pageCount") {
		t.Errorf("document_info = %v", info["text"])
	}

	if _, isErr := callTool(t, s, "close_document", map[string]any{"handle": h}); isErr {
		t.Errorf("close_document errored")
	}
	// After close, the handle is unknown.
	if _, isErr := callTool(t, s, "extract_text", map[string]any{"handle": h}); !isErr {
		t.Errorf("extract_text after close should error")
	}
}

// TestAgentRedactionFlow is the competitive story: an agent opens a PDF, marks a
// secret, and saves the redacted result locally — and the secret is gone.
func TestAgentRedactionFlow(t *testing.T) {
	s := NewServer()
	path := writeSecretPDF(t)
	out := filepath.Join(t.TempDir(), "redacted.pdf")

	item, _ := callTool(t, s, "open_document", map[string]any{"path": path})
	var opened struct {
		Handle int `json:"handle"`
	}
	json.Unmarshal([]byte(item["text"].(string)), &opened)

	marked, isErr := callTool(t, s, "redact_text", map[string]any{
		"handle": opened.Handle, "page": 0, "pattern": `SECRET-\d+`,
	})
	if isErr {
		t.Fatalf("redact_text error: %v", marked)
	}
	if !strings.Contains(marked["text"].(string), "\"marked\":1") {
		t.Errorf("redact_text marked = %v, want 1", marked["text"])
	}

	saved, isErr := callTool(t, s, "redact_and_save", map[string]any{
		"handle": opened.Handle, "out_path": out,
	})
	if isErr {
		t.Fatalf("redact_and_save error: %v", saved)
	}

	// The saved file must not contain the secret (extract or raw bytes).
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := doc.ExtractText(core.ExtractOptions{})
	if strings.Contains(text, "SECRET") {
		t.Errorf("LEAK: redacted-and-saved file still extracts the secret: %q", text)
	}
	raw, _ := os.ReadFile(out)
	if bytes.Contains(raw, []byte("SECRET-42")) {
		t.Errorf("LEAK: secret bytes remain in the saved file")
	}
}

func TestServe_StdioLoop(t *testing.T) {
	s := NewServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := s.Serve(in, &out); err != nil {
		t.Fatal(err)
	}
	// Two requests with ids -> two responses; the notification -> none.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2:\n%s", len(lines), out.String())
	}
}

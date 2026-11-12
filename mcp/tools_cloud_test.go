package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// stubAI is an injectable core.AIClient that records the document text it saw
// and returns canned results, so the Cloud MCP tools can be tested without a
// network or an API key.
type stubAI struct {
	gotText string
	extract map[string]any
	answer  string
}

func (s *stubAI) Extract(_ context.Context, text string, _ core.Schema) (map[string]any, error) {
	s.gotText = text
	return s.extract, nil
}

func (s *stubAI) Ask(_ context.Context, text, _ string) (string, error) {
	s.gotText = text
	return s.answer, nil
}

func TestExtractStructuredTool(t *testing.T) {
	s := NewServer()
	stub := &stubAI{extract: map[string]any{"vendor": "Acme", "total": "42"}}
	s.EnableAITools(stub, "TestLLM")
	// writeSecretPDF embeds "SECRET-42" and "public" text — enough to ground on.
	h := openHandle(t, s, writeSecretPDF(t))

	item, isErr := callTool(t, s, "extract_structured", map[string]any{
		"handle": h,
		"fields": []map[string]any{
			{"name": "vendor", "description": "the seller", "required": true},
			{"name": "total", "type": "number"},
		},
	})
	if isErr {
		t.Fatalf("extract_structured error: %v", item)
	}
	txt := item["text"].(string)
	if !strings.Contains(txt, "Acme") || !strings.Contains(txt, "\"vendor\"") {
		t.Errorf("result = %q, want the stub's fields", txt)
	}
	// The document text must have reached the backend (grounding).
	if !strings.Contains(stub.gotText, "SECRET-42") {
		t.Errorf("document text not forwarded: %q", stub.gotText)
	}
}

func TestAskDocumentTool(t *testing.T) {
	s := NewServer()
	s.EnableAITools(&stubAI{answer: "The document mentions a public notice."}, "TestLLM")
	h := openHandle(t, s, writeSecretPDF(t))

	item, isErr := callTool(t, s, "ask_document", map[string]any{
		"handle": h, "question": "What does it say?",
	})
	if isErr {
		t.Fatalf("ask_document error: %v", item)
	}
	if !strings.Contains(item["text"].(string), "public notice") {
		t.Errorf("answer = %q", item["text"])
	}
}

func TestCloudToolsBadArgs(t *testing.T) {
	s := NewServer()
	s.EnableAITools(&stubAI{}, "TestLLM")
	h := openHandle(t, s, writeSecretPDF(t))

	// extract_structured requires a non-empty fields array.
	if _, isErr := callTool(t, s, "extract_structured", map[string]any{"handle": h}); !isErr {
		t.Error("extract_structured with no fields should error")
	}
	// ask_document requires a question.
	if _, isErr := callTool(t, s, "ask_document", map[string]any{"handle": h}); !isErr {
		t.Error("ask_document with no question should error")
	}
	// Unknown handle.
	if _, isErr := callTool(t, s, "ask_document", map[string]any{"handle": 999, "question": "x"}); !isErr {
		t.Error("ask_document with unknown handle should error")
	}
}

// toolNames returns the tools a server advertises, keyed by name, with their
// descriptions.
func toolNames(t *testing.T, s *Server) map[string]string {
	t.Helper()
	m := rpc(t, s, 1, "tools/list", nil)
	out := map[string]string{}
	for _, tt := range m["result"].(map[string]any)["tools"].([]any) {
		e := tt.(map[string]any)
		out[e["name"].(string)] = e["description"].(string)
	}
	return out
}

// TestAIToolsOffByDefault pins the privacy promise: a default server never
// lists or accepts a tool that sends document content to an external service,
// even when an API key is present in the environment.
func TestAIToolsOffByDefault(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-not-a-real-key")
	t.Setenv("FLUXDOCS_MCP_ENABLE_AI", "1") // the library ignores env; only the cloud binary reads it
	s := NewServer()

	names := toolNames(t, s)
	for n := range aiTools {
		if _, ok := names[n]; ok {
			t.Errorf("default server lists network-sending tool %q", n)
		}
	}
	for n, desc := range names {
		if strings.Contains(desc, "leaves this machine") {
			t.Errorf("default server lists %q, described as sending content out", n)
		}
	}
	if len(aiTools) == 0 {
		t.Fatal("aiTools registry is empty; the test would pass vacuously")
	}

	h := openHandle(t, s, writeSecretPDF(t))
	for _, name := range []string{"ask_document", "extract_structured"} {
		m := rpc(t, s, 1, "tools/call", map[string]any{
			"name":      name,
			"arguments": map[string]any{"handle": h, "question": "x", "fields": []map[string]any{{"name": "a"}}},
		})
		if m["error"] == nil {
			t.Errorf("default server accepted %s: %v", name, m["result"])
		}
	}
}

// TestAIToolsOptIn checks EnableAITools registers the AI tools with the provider
// named in their descriptions, and that a nil backend turns them off again.
func TestAIToolsOptIn(t *testing.T) {
	s := NewServer()
	s.EnableAITools(&stubAI{answer: "ok"}, "Anthropic (Claude API)")

	names := toolNames(t, s)
	for n := range aiTools {
		desc, ok := names[n]
		if !ok {
			t.Errorf("opted-in server does not list %q", n)
			continue
		}
		if !strings.Contains(desc, "Anthropic (Claude API)") || !strings.Contains(desc, "leaves this machine") {
			t.Errorf("%s description does not disclose the provider: %q", n, desc)
		}
	}
	// Core tools stay listed.
	if _, ok := names["open_document"]; !ok {
		t.Error("opted-in server lost open_document")
	}

	s.EnableAITools(nil, "")
	names = toolNames(t, s)
	for n := range aiTools {
		if _, ok := names[n]; ok {
			t.Errorf("tool %q still listed after disabling", n)
		}
	}
}

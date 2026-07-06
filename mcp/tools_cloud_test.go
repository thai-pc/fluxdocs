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
	s.SetAIClient(stub)
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
	s.SetAIClient(&stubAI{answer: "The document mentions a public notice."})
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
	s.SetAIClient(&stubAI{})
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

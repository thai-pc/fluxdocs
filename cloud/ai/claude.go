// Package ai implements the Cloud-tier LLM backend for FluxDocs: a minimal
// client for the Claude Messages API that satisfies core.AIClient. It talks to
// the API over a standard net/http client with no third-party SDK, per
// CLAUDE.md §3 ("call an LLM API (Claude) over a standard Go HTTP client — no
// other language SDKs"). Living in the cloud module keeps the network dependency
// out of the pure-Go, WASM-safe MIT core.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultModel     = "claude-opus-4-8"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 4096
	extractToolName  = "record_extraction"

	// maxDocChars bounds how much document text is sent in one request. Well
	// within the model's context window, it caps request size and cost; when the
	// text is longer it is truncated with a visible marker rather than silently.
	maxDocChars = 200_000
)

// ErrNoAPIKey is returned when the client has no API key (neither passed to New
// nor present in ANTHROPIC_API_KEY).
var ErrNoAPIKey = errors.New("fluxdocs/ai: no Anthropic API key configured")

// Client is a Claude Messages API client implementing core.AIClient. The zero
// value is not usable; construct one with New.
type Client struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client. If apiKey is empty it falls back to the ANTHROPIC_API_KEY
// environment variable. The model defaults to claude-opus-4-8; override via the
// returned struct's fields before use.
func New(apiKey string) *Client {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	return &Client{
		APIKey:  apiKey,
		Model:   defaultModel,
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// compile-time check that Client satisfies the core contract.
var _ core.AIClient = (*Client)(nil)

// --- wire types (Messages API) ---

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Strict      bool           `json:"strict,omitempty"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type request struct {
	Model      string      `json:"model"`
	MaxTokens  int         `json:"max_tokens"`
	System     string      `json:"system,omitempty"`
	Messages   []message   `json:"messages"`
	Tools      []toolDef   `json:"tools,omitempty"`
	ToolChoice *toolChoice `json:"tool_choice,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type response struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
}

type apiError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Extract implements core.AIClient. It defines a single strict tool whose input
// schema mirrors the requested fields and forces the model to call it, then
// returns the tool input as the structured result.
func (c *Client) Extract(ctx context.Context, documentText string, schema core.Schema) (map[string]any, error) {
	if len(schema.Fields) == 0 {
		return nil, fmt.Errorf("fluxdocs/ai: empty schema")
	}
	req := request{
		Model:     c.Model,
		MaxTokens: defaultMaxTokens,
		System:    "You extract structured data from documents. Use the provided tool to record exactly the requested fields, drawing every value only from the document text. Omit any field the document does not contain.",
		Messages: []message{{
			Role:    "user",
			Content: "Extract the requested fields from this document:\n\n" + clip(documentText),
		}},
		Tools: []toolDef{{
			Name:        extractToolName,
			Description: "Record the fields extracted from the document.",
			InputSchema: schemaToJSON(schema),
			Strict:      true,
		}},
		ToolChoice: &toolChoice{Type: "tool", Name: extractToolName},
	}
	resp, err := c.send(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, block := range resp.Content {
		if block.Type == "tool_use" && block.Name == extractToolName {
			var out map[string]any
			if err := json.Unmarshal(block.Input, &out); err != nil {
				return nil, fmt.Errorf("fluxdocs/ai: decode tool input: %w", err)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("fluxdocs/ai: model did not return the extraction tool call")
}

// Ask implements core.AIClient with a grounded question-answering prompt.
func (c *Client) Ask(ctx context.Context, documentText, question string) (string, error) {
	req := request{
		Model:     c.Model,
		MaxTokens: defaultMaxTokens,
		System:    "Answer the question using only the provided document. If the document does not contain the answer, say so plainly.",
		Messages: []message{{
			Role:    "user",
			Content: "Document:\n\n" + clip(documentText) + "\n\nQuestion: " + question,
		}},
	}
	resp, err := c.send(ctx, req)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// send performs one POST /v1/messages round trip.
func (c *Client) send(ctx context.Context, body request) (*response, error) {
	if c.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("content-type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		var e apiError
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("fluxdocs/ai: %s (%d %s)", e.Error.Message, httpResp.StatusCode, e.Error.Type)
		}
		return nil, fmt.Errorf("fluxdocs/ai: request failed with status %d", httpResp.StatusCode)
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("fluxdocs/ai: decode response: %w", err)
	}
	return &r, nil
}

// schemaToJSON translates a core.Schema into a JSON-Schema object suitable for a
// strict tool input_schema: additionalProperties is false and required lists the
// fields marked Required.
func schemaToJSON(s core.Schema) map[string]any {
	props := make(map[string]any, len(s.Fields))
	var required []string
	for _, f := range s.Fields {
		props[f.Name] = fieldToJSON(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func fieldToJSON(f core.SchemaField) map[string]any {
	prop := map[string]any{}
	if f.Description != "" {
		prop["description"] = f.Description
	}
	switch f.Type {
	case "number":
		prop["type"] = "number"
	case "boolean":
		prop["type"] = "boolean"
	case "array":
		prop["type"] = "array"
		prop["items"] = map[string]any{"type": "string"}
	default:
		prop["type"] = "string"
	}
	return prop
}

// clip bounds document text to maxDocChars, appending a visible marker when it
// truncates so the omission is never silent.
func clip(text string) string {
	if len(text) <= maxDocChars {
		return text
	}
	return text[:maxDocChars] + "\n\n[document truncated at " + fmt.Sprint(maxDocChars) + " characters]"
}

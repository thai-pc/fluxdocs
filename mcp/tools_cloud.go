package mcp

import (
	"encoding/json"

	"github.com/thai-pc/fluxdocs/cloud/ai"
	"github.com/thai-pc/fluxdocs/core"
)

// tools_cloud.go registers the Cloud-tier AI tools: extract_structured and
// ask_document. Both run the document text through an LLM backend. By default
// that is a Claude client reading ANTHROPIC_API_KEY (constructed on demand, so
// the tools stay declared even with no key — they simply error until one is
// set); a test or embedder can override it via Server.SetAIClient. The document
// bytes leave the machine only through this explicitly-configured backend — the
// pure-Go core never makes the call itself.

// aiOrDefault returns the configured AI backend, or a default Claude client.
func (s *Server) aiOrDefault() core.AIClient {
	s.mu.Lock()
	c := s.aiClient
	s.mu.Unlock()
	if c != nil {
		return c
	}
	return ai.New("")
}

func init() {
	register(tool{
		name:        "extract_structured",
		description: "Extract structured fields from the document via an LLM (Cloud). Provide the fields to pull out; returns a JSON object with the requested keys. Requires ANTHROPIC_API_KEY unless a backend is configured.",
		schema: obj(map[string]any{
			"handle": prop("integer", "Document handle"),
			"fields": map[string]any{
				"type":        "array",
				"description": "Fields to extract",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        prop("string", "Field name (the returned JSON key)"),
						"description": prop("string", "What this field is (guides the model)"),
						"type":        map[string]any{"type": "string", "enum": []string{"string", "number", "boolean", "array"}, "description": "Value type (default string)"},
						"required":    prop("boolean", "Whether the field must be present"),
					},
					"required": []string{"name"},
				},
			},
		}, "handle", "fields"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle int `json:"handle"`
				Fields []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Type        string `json:"type"`
					Required    bool   `json:"required"`
				} `json:"fields"`
			}
			if err := json.Unmarshal(args, &a); err != nil || len(a.Fields) == 0 {
				return errText("invalid arguments: 'handle' and a non-empty 'fields' array are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			schema := core.Schema{Fields: make([]core.SchemaField, len(a.Fields))}
			for i, f := range a.Fields {
				schema.Fields[i] = core.SchemaField{Name: f.Name, Description: f.Description, Type: f.Type, Required: f.Required}
			}
			doc.SetAIClient(s.aiOrDefault())
			result, err := doc.ExtractStructured(schema)
			if err != nil {
				return errText("extract_structured failed: %v", err)
			}
			return jsonText(map[string]any{"fields": result})
		},
	})

	register(tool{
		name:        "ask_document",
		description: "Answer a natural-language question grounded in the document via an LLM (Cloud). Requires ANTHROPIC_API_KEY unless a backend is configured.",
		schema: obj(map[string]any{
			"handle":   prop("integer", "Document handle"),
			"question": prop("string", "The question to answer from the document"),
		}, "handle", "question"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle   int    `json:"handle"`
				Question string `json:"question"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.Question == "" {
				return errText("invalid arguments: 'handle' and 'question' are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			doc.SetAIClient(s.aiOrDefault())
			answer, err := doc.AskDocument(a.Question)
			if err != nil {
				return errText("ask_document failed: %v", err)
			}
			return jsonText(map[string]any{"answer": answer})
		},
	})
}

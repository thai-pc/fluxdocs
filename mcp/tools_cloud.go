package mcp

import (
	"encoding/json"

	"github.com/thai-pc/fluxdocs/core"
)

// tools_cloud.go defines the AI tools: extract_structured and ask_document.
// Both send the document's extracted text to an external LLM service, so they
// live in a separate registry (aiTools) that a server exposes only after
// Server.EnableAITools — off by default, even when ANTHROPIC_API_KEY is set.
// This package ships no backend: the concrete Claude client lives in the cloud
// module (cloud/ai) and is wired in by the cloud binary fluxdocs-mcp-cloud, so
// the root module never imports Cloud code and the default build makes no
// network call.

// aiDisclosure is prepended to every AI tool description, so the agent (and the
// user reviewing its tool calls) can see that the call sends document text to
// the named provider.
func aiDisclosure(provider string) string {
	return "Sends the document's extracted text to " + provider + " (external service): the document content leaves this machine. "
}

// aiCall resolves the opted-in backend for one AI tool call. It reports false
// if the AI tools were disabled since the tool was listed.
func (s *Server) aiCall() (core.AIClient, bool) {
	c, _ := s.aiBackend()
	return c, c != nil
}

func init() {
	registerAI(tool{
		name:        "extract_structured",
		description: "Extract structured fields from the document via an LLM (Cloud). Provide the fields to pull out; returns a JSON object with the requested keys.",
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
			backend, ok := s.aiCall()
			if !ok {
				return errText("AI tools are disabled on this server")
			}
			doc.SetAIClient(backend)
			result, err := doc.ExtractStructured(schema)
			if err != nil {
				return errText("extract_structured failed: %v", err)
			}
			return jsonText(map[string]any{"fields": result})
		},
	})

	registerAI(tool{
		name:        "ask_document",
		description: "Answer a natural-language question grounded in the document via an LLM (Cloud).",
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
			backend, ok := s.aiCall()
			if !ok {
				return errText("AI tools are disabled on this server")
			}
			doc.SetAIClient(backend)
			answer, err := doc.AskDocument(a.Question)
			if err != nil {
				return errText("ask_document failed: %v", err)
			}
			return jsonText(map[string]any{"answer": answer})
		},
	})
}

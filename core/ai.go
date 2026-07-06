package core

import "context"

// AIClient bridges the pure-Go core to a Cloud-tier LLM backend. The core never
// makes network calls itself — that would break the privacy-first promise and
// the WASM build (CLAUDE.md §7.2) — so a Cloud build injects a client via
// Document.SetAIClient. The concrete HTTP implementation (a Claude Messages API
// client) lives in the cloud module (cloud/ai), which the MIT core does not
// import, keeping the tier boundary intact.
type AIClient interface {
	// Extract returns structured JSON for the document text, shaped by schema.
	// A field the model cannot fill is omitted from the returned map.
	Extract(ctx context.Context, documentText string, schema Schema) (map[string]any, error)

	// Ask answers a natural-language question grounded in the document text.
	Ask(ctx context.Context, documentText, question string) (string, error)
}

// Schema describes the fields a structured extraction should return. It is a
// deliberately small, provider-agnostic subset of JSON Schema — the cloud/ai
// client translates it into whatever the LLM provider expects (for Claude, a
// tool input_schema). Keeping it here lets the core define the contract without
// depending on any provider.
type Schema struct {
	Fields []SchemaField
}

// SchemaField is one requested value. Type is one of "string", "number",
// "boolean", or "array" (of strings); an empty Type defaults to "string".
type SchemaField struct {
	Name        string
	Description string
	Type        string
	Required    bool
}

// SetAIClient installs the Cloud-tier LLM backend used by ExtractStructured and
// AskDocument. Passing nil disables them (they then return
// ErrFeatureRequiresCloud). Safe to call concurrently with other operations.
func (d *Document) SetAIClient(c AIClient) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ai = c
}

// toSchema normalizes the loosely-typed ExtractStructured argument (kept as
// `any` to match the documented §7 API surface) into a Schema. It accepts a
// Schema, a []SchemaField, or a map[string]string (field name -> description).
func toSchema(v any) (Schema, bool) {
	switch s := v.(type) {
	case Schema:
		return s, true
	case []SchemaField:
		return Schema{Fields: s}, true
	case map[string]string:
		fields := make([]SchemaField, 0, len(s))
		for name, desc := range s {
			fields = append(fields, SchemaField{Name: name, Description: desc})
		}
		return Schema{Fields: fields}, true
	default:
		return Schema{}, false
	}
}

package mcp

import (
	"encoding/json"
	"os"

	"github.com/thai-pc/fluxdocs/core"
)

// tools_pro.go registers the Pro-feature MCP tools: watermark, compare, PDF/A,
// forms, and signing. They reuse the core and, like the base tools, keep every
// document on the user's own machine (local file paths, in-process processing).

// writeExact writes a document's verbatim bytes to path (preserving a signature
// or PDF/A markers that a rebuild via Save would drop).
func writeExact(doc *core.Document, path string) error {
	b, err := doc.Bytes()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func init() {
	register(tool{
		name:        "watermark_and_save",
		description: "Stamp a translucent, rotated text watermark over all pages and write the result to a local path.",
		schema: obj(map[string]any{
			"handle":   prop("integer", "Document handle"),
			"text":     prop("string", "Watermark text"),
			"out_path": prop("string", "Local path to write the watermarked PDF"),
			"opacity":  prop("number", "Opacity 0..1 (default 0.3)"),
		}, "handle", "text", "out_path"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle  int     `json:"handle"`
				Text    string  `json:"text"`
				OutPath string  `json:"out_path"`
				Opacity float64 `json:"opacity"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.Text == "" || a.OutPath == "" {
				return errText("invalid arguments: 'handle', 'text', 'out_path' are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			out, err := doc.Watermark(core.WatermarkOptions{Text: a.Text, Opacity: a.Opacity})
			if err != nil {
				return errText("watermark failed: %v", err)
			}
			if err := out.Save(a.OutPath); err != nil {
				return errText("save failed: %v", err)
			}
			return jsonText(map[string]any{"saved": a.OutPath})
		},
	})

	register(tool{
		name:        "compare_documents",
		description: "Compare two open documents and return the per-page word differences (added/removed text).",
		schema: obj(map[string]any{
			"handle_a": prop("integer", "First document handle (the original)"),
			"handle_b": prop("integer", "Second document handle (the revision)"),
		}, "handle_a", "handle_b"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				A int `json:"handle_a"`
				B int `json:"handle_b"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			da, ok := s.doc(a.A)
			if !ok {
				return errText("unknown document handle %d", a.A)
			}
			db, ok := s.doc(a.B)
			if !ok {
				return errText("unknown document handle %d", a.B)
			}
			res, err := da.Compare(db)
			if err != nil {
				return errText("compare failed: %v", err)
			}
			pages := []map[string]any{}
			for _, pd := range res.Pages {
				if pd.Identical {
					continue
				}
				var added, removed []string
				for _, seg := range pd.Segments {
					switch seg.Op {
					case core.DiffInsert:
						added = append(added, seg.Text)
					case core.DiffDelete:
						removed = append(removed, seg.Text)
					}
				}
				pages = append(pages, map[string]any{"page": pd.PageIndex, "added": added, "removed": removed})
			}
			return jsonText(map[string]any{"identical": res.Identical, "pages": pages})
		},
	})

	register(tool{
		name:        "convert_to_pdfa",
		description: "Tag the document for PDF/A-1b conformance (XMP metadata + sRGB output intent) and write it to a local path.",
		schema: obj(map[string]any{
			"handle":   prop("integer", "Document handle"),
			"out_path": prop("string", "Local path to write the PDF/A file"),
		}, "handle", "out_path"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle  int    `json:"handle"`
				OutPath string `json:"out_path"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.OutPath == "" {
				return errText("invalid arguments: 'handle' and 'out_path' are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			out, err := doc.ConvertToPDFA()
			if err != nil {
				return errText("PDF/A conversion failed: %v", err)
			}
			// Write verbatim: a rebuild would drop the PDF/A markers.
			if err := writeExact(out, a.OutPath); err != nil {
				return errText("save failed: %v", err)
			}
			return jsonText(map[string]any{"saved": a.OutPath})
		},
	})

	register(tool{
		name:        "get_form_fields",
		description: "List the interactive AcroForm fields in the document (name, type, value, options).",
		schema:      obj(map[string]any{"handle": prop("integer", "Document handle")}, "handle"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			doc, errc := s.docArg(args)
			if errc != nil {
				return errText("%s", *errc)
			}
			fields, err := doc.GetFormFields()
			if err != nil {
				return errText("get_form_fields failed: %v", err)
			}
			out := make([]map[string]any, len(fields))
			for i, f := range fields {
				out[i] = map[string]any{
					"name": f.Name, "type": string(f.Type), "value": f.Value,
					"options": f.Options, "readOnly": f.ReadOnly, "required": f.Required,
				}
			}
			return jsonText(map[string]any{"fields": out})
		},
	})

	register(tool{
		name:        "auto_redact_pii",
		description: "Detect and permanently redact personally identifiable information (emails, US SSNs, phone numbers, Luhn-valid credit-card numbers) across all pages, then write the result to a local path. Detection is pattern-based and runs entirely on this machine. Irreversible.",
		schema: obj(map[string]any{
			"handle":   prop("integer", "Document handle"),
			"out_path": prop("string", "Local path to write the redacted PDF"),
			"kinds": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"email", "ssn", "phone", "credit_card"}},
				"description": "PII kinds to redact; omit for all kinds",
			},
		}, "handle", "out_path"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle  int      `json:"handle"`
				OutPath string   `json:"out_path"`
				Kinds   []string `json:"kinds"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.OutPath == "" {
				return errText("invalid arguments: 'handle' and 'out_path' are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			counts, err := doc.AutoRedactPII(core.PIIKindsFromStrings(a.Kinds)...)
			if err != nil {
				return errText("auto_redact_pii failed: %v", err)
			}
			out, err := doc.RedactAndFlatten()
			if err != nil {
				return errText("redaction failed: %v", err)
			}
			// Write verbatim; RedactAndFlatten already produced leak-free bytes.
			if err := writeExact(out, a.OutPath); err != nil {
				return errText("save failed: %v", err)
			}
			byKind := make(map[string]int, len(counts))
			for k, n := range counts {
				byKind[string(k)] = n
			}
			return jsonText(withSummary(map[string]any{"saved": a.OutPath, "redacted": byKind}, out))
		},
	})

	register(tool{
		name:        "sign_and_save",
		description: "Apply a detached PKCS#7 digital signature (RSA) using a local PEM certificate and key, and write the signed PDF to a local path. The key never leaves the machine.",
		schema: obj(map[string]any{
			"handle":    prop("integer", "Document handle"),
			"cert_path": prop("string", "Local path to a PEM certificate"),
			"key_path":  prop("string", "Local path to a PEM RSA private key"),
			"out_path":  prop("string", "Local path to write the signed PDF"),
			"reason":    prop("string", "Signing reason (optional)"),
		}, "handle", "cert_path", "key_path", "out_path"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle   int    `json:"handle"`
				CertPath string `json:"cert_path"`
				KeyPath  string `json:"key_path"`
				OutPath  string `json:"out_path"`
				Reason   string `json:"reason"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.CertPath == "" || a.KeyPath == "" || a.OutPath == "" {
				return errText("invalid arguments: 'handle', 'cert_path', 'key_path', 'out_path' are required")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			cert, err := os.ReadFile(a.CertPath)
			if err != nil {
				return errText("read cert: %v", err)
			}
			key, err := os.ReadFile(a.KeyPath)
			if err != nil {
				return errText("read key: %v", err)
			}
			out, err := doc.SignDocument(core.SignatureOptions{CertPEM: cert, KeyPEM: key, Reason: a.Reason})
			if err != nil {
				return errText("sign failed: %v", err)
			}
			// Write verbatim: a rebuild would invalidate the signature byte range.
			if err := writeExact(out, a.OutPath); err != nil {
				return errText("save failed: %v", err)
			}
			return jsonText(map[string]any{"saved": a.OutPath})
		},
	})
}

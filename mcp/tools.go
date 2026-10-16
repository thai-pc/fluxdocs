package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// renderTimeout bounds one render_page call's wall clock, on top of the core's
// per-page work budget. callTool has no request context yet (stdio, sequential),
// so the deadline starts from context.Background(); wiring MCP
// notifications/cancelled is a later follow-up.
var renderTimeout = 10 * time.Second

// tool is one MCP tool: its schema and a handler that returns result content
// and whether the call errored.
type tool struct {
	name        string
	description string
	schema      map[string]any
	handler     func(s *Server, args json.RawMessage) ([]contentItem, bool)
}

// tools is the registry, keyed by tool name.
var tools = map[string]tool{}

func register(t tool) { tools[t.name] = t }

// toolList returns the advertised tools (for tools/list) in a stable order.
func toolList() []map[string]any {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		t := tools[n]
		out = append(out, map[string]any{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.schema,
		})
	}
	return out
}

// obj / prop build JSON Schema fragments.
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func init() {
	register(tool{
		name:        "open_document",
		description: "Open a PDF from a local file path and return a handle for the other tools. The document is processed in-process; nothing is sent anywhere.",
		schema:      obj(map[string]any{"path": prop("string", "Local filesystem path to the PDF")}, "path"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &a); err != nil || a.Path == "" {
				return errText("invalid arguments: a 'path' string is required")
			}
			doc, err := core.OpenDocument(a.Path)
			if err != nil {
				return errText("open failed: %v", err)
			}
			h := s.addDoc(doc)
			return jsonText(map[string]any{"handle": h, "pageCount": doc.PageCount, "title": doc.Title})
		},
	})

	register(tool{
		name:        "document_info",
		description: "Return a document's page count, title, and per-page size (points) and rotation.",
		schema:      obj(map[string]any{"handle": prop("integer", "Document handle from open_document")}, "handle"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			doc, errc := s.docArg(args)
			if errc != nil {
				return errText("%s", *errc)
			}
			pages := make([]map[string]any, len(doc.Pages))
			for i, p := range doc.Pages {
				pages[i] = map[string]any{"width": p.Width, "height": p.Height, "rotation": p.Rotation}
			}
			return jsonText(map[string]any{"pageCount": doc.PageCount, "title": doc.Title, "pages": pages})
		},
	})

	register(tool{
		name:        "extract_text",
		description: "Extract the text of a document. Pass 'page' (0-based) for a single page, or omit it for all pages.",
		schema: obj(map[string]any{
			"handle": prop("integer", "Document handle"),
			"page":   prop("integer", "Optional 0-based page index; omit for all pages"),
		}, "handle"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle int  `json:"handle"`
				Page   *int `json:"page"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			var text string
			var err error
			if a.Page != nil {
				text, err = doc.ExtractTextByPage(*a.Page, core.ExtractOptions{})
			} else {
				text, err = doc.ExtractText(core.ExtractOptions{})
			}
			if err != nil {
				return errText("extract failed: %v", err)
			}
			return textResult(text), false
		},
	})

	register(tool{
		name:        "extract_tables",
		description: "Detect and extract tabular data on a page as a grid of rows and columns (stream/whitespace heuristic; returns no tables when the page is not tabular).",
		schema: obj(map[string]any{
			"handle": prop("integer", "Document handle"),
			"page":   prop("integer", "0-based page index"),
		}, "handle", "page"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle int `json:"handle"`
				Page   int `json:"page"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			tables, err := doc.ExtractTables(a.Page)
			if err != nil {
				return errText("extract_tables failed: %v", err)
			}
			out := make([]map[string]any, len(tables))
			for i, t := range tables {
				out[i] = map[string]any{"rows": t.Rows}
			}
			return jsonText(map[string]any{"tables": out})
		},
	})

	register(tool{
		name:        "render_page",
		description: "Render a page to a PNG image (base64), including the annotation overlay. Useful for an agent to see a page.",
		schema: obj(map[string]any{
			"handle": prop("integer", "Document handle"),
			"page":   prop("integer", "0-based page index"),
			"dpi":    prop("integer", "Resolution in DPI (default 150)"),
		}, "handle", "page"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle int `json:"handle"`
				Page   int `json:"page"`
				DPI    int `json:"dpi"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
			defer cancel()
			img, err := doc.RenderPageContext(ctx, a.Page, core.RenderOptions{DPI: a.DPI, Format: "png"})
			if err != nil {
				return errText("render failed: %v", err)
			}
			return []contentItem{{
				Type:     "image",
				Data:     base64.StdEncoding.EncodeToString(img),
				MimeType: "image/png",
			}}, false
		},
	})

	register(tool{
		name:        "redact_text",
		description: "Mark text matching a regular expression on a page for redaction. Nothing is changed until redact_and_save. Returns the number of regions marked.",
		schema: obj(map[string]any{
			"handle":  prop("integer", "Document handle"),
			"page":    prop("integer", "0-based page index"),
			"pattern": prop("string", "A Go regular expression, e.g. \\d{3}-\\d{2}-\\d{4}"),
		}, "handle", "page", "pattern"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle  int    `json:"handle"`
				Page    int    `json:"page"`
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			doc, ok := s.doc(a.Handle)
			if !ok {
				return errText("unknown document handle %d", a.Handle)
			}
			rects, err := doc.RedactText(a.Page, a.Pattern)
			if err != nil {
				return errText("redact_text failed: %v", err)
			}
			return jsonText(map[string]any{"marked": len(rects)})
		},
	})

	register(tool{
		name:        "redact_and_save",
		description: "Permanently apply all pending redactions (removing the text from the content stream, not just painting over it) and write the result to a local path. Irreversible.",
		schema: obj(map[string]any{
			"handle":   prop("integer", "Document handle"),
			"out_path": prop("string", "Local path to write the redacted PDF"),
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
			out, err := doc.RedactAndFlatten()
			if err != nil {
				return errText("redaction failed: %v", err)
			}
			if err := out.Save(a.OutPath); err != nil {
				return errText("save failed: %v", err)
			}
			return jsonText(map[string]any{"saved": a.OutPath, "pageCount": out.PageCount})
		},
	})

	register(tool{
		name:        "close_document",
		description: "Release an open document handle.",
		schema:      obj(map[string]any{"handle": prop("integer", "Document handle")}, "handle"),
		handler: func(s *Server, args json.RawMessage) ([]contentItem, bool) {
			var a struct {
				Handle int `json:"handle"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return errText("invalid arguments")
			}
			if !s.removeDoc(a.Handle) {
				return errText("unknown document handle %d", a.Handle)
			}
			return textResult("closed"), false
		},
	})
}

// docArg resolves the {"handle": N} argument common to several tools. On failure
// it returns a message pointer.
func (s *Server) docArg(args json.RawMessage) (*core.Document, *string) {
	var a struct {
		Handle int `json:"handle"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		msg := "invalid arguments: a 'handle' is required"
		return nil, &msg
	}
	doc, ok := s.doc(a.Handle)
	if !ok {
		msg := "unknown document handle"
		return nil, &msg
	}
	return doc, nil
}

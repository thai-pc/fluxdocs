// Package mcp is an agent-native Model Context Protocol server that exposes the
// FluxDocs core as tools an AI agent (Claude, Claude Code, any MCP client) can
// call: open, inspect, extract, render, and redact PDFs. It speaks JSON-RPC 2.0
// over a newline-delimited stdio transport.
//
// It is pure Go, MIT-licensed, and self-hostable: documents are read from and
// written to the user's own filesystem and processed in-process — no byte is
// sent to a third party. That is the defensible angle versus sales-gated,
// cloud-leaning agent assistants (spec §2, revision 0.2.0).
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/thai-pc/fluxdocs/core"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "fluxdocs-mcp"
	serverVersion   = "0.1.0"
)

// Server holds the open documents and dispatches MCP requests. It is safe for
// sequential use over one stdio connection.
type Server struct {
	mu   sync.Mutex
	docs map[int]*core.Document
	next int

	// aiClient is the Cloud-tier LLM backend for the extract_structured and
	// ask_document tools. When nil, those tools construct a default Claude client
	// (reading ANTHROPIC_API_KEY) on demand. Tests inject a stub here. Guarded by
	// mu.
	aiClient core.AIClient
}

// NewServer returns a ready MCP server.
func NewServer() *Server {
	return &Server{docs: map[int]*core.Document{}, next: 1}
}

// SetAIClient installs the LLM backend used by the Cloud AI tools, overriding
// the default Claude client. Passing a client makes the AI tools usable without
// ANTHROPIC_API_KEY (e.g. in tests).
func (s *Server) SetAIClient(c core.AIClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aiClient = c
}

// --- JSON-RPC 2.0 wire types ---

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC standard error codes (subset used here).
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Serve runs the stdio loop: it reads newline-delimited JSON-RPC messages from r
// and writes responses to w until r is exhausted.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // allow large messages
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, send := s.Handle(line)
		if send {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// Handle processes one JSON-RPC message and returns the response object and
// whether it should be sent (notifications produce no response). Exposed so the
// protocol can be driven by an in-process test harness.
func (s *Server) Handle(msg []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(msg, &req); err != nil {
		return errorResponse(nil, codeParseError, "parse error"), true
	}
	// A request without an id is a notification: act on it, send nothing.
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return okResponse(req.ID, s.initialize()), true
	case "notifications/initialized", "initialized":
		return response{}, false
	case "tools/list":
		return okResponse(req.ID, map[string]any{"tools": toolList()}), true
	case "tools/call":
		res, rerr := s.callTool(req.Params)
		if rerr != nil {
			return errorResponse(req.ID, rerr.Code, rerr.Message), true
		}
		return okResponse(req.ID, res), true
	default:
		if notification {
			return response{}, false
		}
		return errorResponse(req.ID, codeMethodNotFound, "unknown method: "+req.Method), true
	}
}

func (s *Server) initialize() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
	}
}

// callTool dispatches a tools/call request to the named tool.
func (s *Server) callTool(params json.RawMessage) (map[string]any, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{codeInvalidParams, "invalid tools/call params"}
	}
	t, ok := tools[call.Name]
	if !ok {
		return nil, &rpcError{codeMethodNotFound, "unknown tool: " + call.Name}
	}
	content, isErr := t.handler(s, call.Arguments)
	return map[string]any{"content": content, "isError": isErr}, nil
}

// --- document registry ---

func (s *Server) addDoc(doc *core.Document) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.next
	s.next++
	s.docs[h] = doc
	return h
}

func (s *Server) doc(handle int) (*core.Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[handle]
	return d, ok
}

func (s *Server) removeDoc(handle int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[handle]
	if ok {
		d.Close()
		delete(s.docs, handle)
	}
	return ok
}

// --- response helpers ---

func okResponse(id json.RawMessage, result interface{}) response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, msg string) response {
	if id == nil {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}}
}

// contentItem is one element of an MCP tool result's content array.
type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// textResult wraps a plain-text tool result.
func textResult(text string) []contentItem {
	return []contentItem{{Type: "text", Text: text}}
}

// errText wraps a tool error message and reports isError=true.
func errText(format string, a ...any) ([]contentItem, bool) {
	return textResult(fmt.Sprintf(format, a...)), true
}

// jsonText marshals v to compact JSON and wraps it as a text result.
func jsonText(v any) ([]contentItem, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return errText("internal error: %v", err)
	}
	return textResult(string(b)), false
}

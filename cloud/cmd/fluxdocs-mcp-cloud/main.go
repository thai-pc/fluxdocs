// Command fluxdocs-mcp-cloud is the FluxDocs MCP server with the Cloud-tier AI
// tools available. It is the root fluxdocs-mcp server (same local tools, same
// stdio transport) plus extract_structured and ask_document, which send the
// document's extracted text to the Anthropic Claude API.
//
// The AI tools are strictly opt-in: they are registered only when
// --enable-cloud-ai is passed or FLUXDOCS_MCP_ENABLE_AI is set to a true value
// (1, true). Without that, this binary behaves exactly like fluxdocs-mcp and
// sends nothing anywhere, even when ANTHROPIC_API_KEY is set. When enabled, the
// key is read from ANTHROPIC_API_KEY; it is never logged.
//
// It lives in the cloud module so the root module (and so the default
// fluxdocs-mcp binary) never depends on Cloud code.
//
//	{ "command": "fluxdocs-mcp-cloud", "args": ["--enable-cloud-ai"] }
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/thai-pc/fluxdocs/cloud/ai"
	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/mcp"
)

// provider is the service name shown in the AI tool descriptions.
const provider = "Anthropic (Claude API)"

// errNoKey is returned when the AI tools are enabled but no key is configured.
var errNoKey = errors.New("AI tools enabled but ANTHROPIC_API_KEY is not set")

func main() {
	s, err := newServer(os.Args[1:], os.Getenv, os.Stderr, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-mcp-cloud:", err)
		os.Exit(2)
	}
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-mcp-cloud:", err)
		os.Exit(1)
	}
}

// newServer parses args and the environment and returns the MCP server. The AI
// tools are registered only on explicit opt-in (flag or FLUXDOCS_MCP_ENABLE_AI);
// an API key alone never enables them. backend overrides the Claude client (for
// tests); nil means a Claude client reading ANTHROPIC_API_KEY. It returns
// errNoKey when opted in without a key, or a flag/env parse error.
func newServer(args []string, getenv func(string) string, stderr io.Writer, backend core.AIClient) (*mcp.Server, error) {
	fs := flag.NewFlagSet("fluxdocs-mcp-cloud", flag.ContinueOnError)
	fs.SetOutput(stderr)
	enableAI := fs.Bool("enable-cloud-ai", false,
		"register the AI tools, which send document text to "+provider+" (also FLUXDOCS_MCP_ENABLE_AI=1)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	enabled := *enableAI
	if v := getenv("FLUXDOCS_MCP_ENABLE_AI"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("FLUXDOCS_MCP_ENABLE_AI: want a boolean (1/0, true/false)")
		}
		enabled = enabled || on
	}

	s := mcp.NewServer()
	if !enabled {
		return s, nil
	}
	if backend == nil {
		key := getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, errNoKey
		}
		backend = ai.New(key)
	}
	s.EnableAITools(backend, provider)
	fmt.Fprintln(stderr, "fluxdocs-mcp-cloud: AI tools enabled; document text sent through them goes to "+provider)
	return s, nil
}

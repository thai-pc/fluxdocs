// Command fluxdocs-mcp runs the FluxDocs Model Context Protocol server over
// stdio, so an AI agent (Claude, Claude Code, any MCP client) can open, inspect,
// extract, render, and redact PDFs through tool calls. Everything runs locally —
// documents are read from and written to the user's own filesystem and never
// sent to a third party.
//
// This build contains no AI backend, so it never registers a tool that sends
// document content to an external service, whatever the environment
// (ANTHROPIC_API_KEY included). The opt-in AI tools (extract_structured,
// ask_document) ship only in the cloud module's fluxdocs-mcp-cloud binary.
//
// Configure it in an MCP client as a stdio server:
//
//	{ "command": "fluxdocs-mcp" }
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/thai-pc/fluxdocs/mcp"
)

// errNoAIBackend is returned when the caller explicitly asks for the AI tools
// in a build that does not include them.
var errNoAIBackend = errors.New("--enable-cloud-ai: this build includes no AI backend; " +
	"use fluxdocs-mcp-cloud, built from a checkout of the repository with " +
	"`go build ./cloud/cmd/fluxdocs-mcp-cloud`")

func main() {
	s, err := newServer(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-mcp:", err)
		os.Exit(2)
	}
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-mcp:", err)
		os.Exit(1)
	}
}

// newServer parses args and returns the local-only MCP server. Passing
// --enable-cloud-ai is an error here (this build cannot honour it); the
// FLUXDOCS_MCP_ENABLE_AI environment variable, which may be set globally for the
// cloud binary, only prints a note to stderr. stderr never receives secrets or
// document content.
func newServer(args []string, getenv func(string) string, stderr io.Writer) (*mcp.Server, error) {
	fs := flag.NewFlagSet("fluxdocs-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	enableAI := fs.Bool("enable-cloud-ai", false, "not supported in this build (see fluxdocs-mcp-cloud)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *enableAI {
		return nil, errNoAIBackend
	}
	if on, _ := strconv.ParseBool(getenv("FLUXDOCS_MCP_ENABLE_AI")); on {
		fmt.Fprintln(stderr, "fluxdocs-mcp: FLUXDOCS_MCP_ENABLE_AI ignored: this build includes no AI tools (see fluxdocs-mcp-cloud)")
	}
	return mcp.NewServer(), nil
}

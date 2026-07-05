// Command fluxdocs-mcp runs the FluxDocs Model Context Protocol server over
// stdio, so an AI agent (Claude, Claude Code, any MCP client) can open, inspect,
// extract, render, and redact PDFs through tool calls. Everything runs locally —
// documents are read from and written to the user's own filesystem and never
// sent to a third party.
//
// Configure it in an MCP client as a stdio server:
//
//	{ "command": "fluxdocs-mcp" }
package main

import (
	"fmt"
	"os"

	"github.com/thai-pc/fluxdocs/mcp"
)

func main() {
	if err := mcp.NewServer().Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fluxdocs-mcp:", err)
		os.Exit(1)
	}
}

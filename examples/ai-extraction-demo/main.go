// Command ai-extraction-demo extracts structured fields from a PDF using the
// FluxDocs Cloud tier: the pure-Go core reads the document text locally, then a
// cloud/ai Claude client turns it into JSON via a single forced tool call.
//
// The document text is sent to the Anthropic API only because a Cloud backend is
// explicitly installed with SetAIClient — the MIT core never makes that call on
// its own. Set ANTHROPIC_API_KEY, then:
//
//	go run ./examples/ai-extraction-demo invoice.pdf
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/thai-pc/fluxdocs/cloud/ai"
	"github.com/thai-pc/fluxdocs/core"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ai-extraction-demo <file.pdf>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		return err
	}
	defer doc.Close()

	// Install the Cloud backend. New reads ANTHROPIC_API_KEY when the argument
	// is empty; the model defaults to claude-opus-4-8.
	doc.SetAIClient(ai.New(""))

	// Describe the fields to pull out. The core normalizes this into the tool
	// input schema the model must fill.
	schema := core.Schema{Fields: []core.SchemaField{
		{Name: "vendor", Description: "the company that issued the document", Required: true},
		{Name: "date", Description: "the document date, ISO-8601 if present"},
		{Name: "total", Description: "the total amount due", Type: "number"},
		{Name: "line_items", Description: "short descriptions of each line item", Type: "array"},
	}}

	fields, err := doc.ExtractStructured(schema)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

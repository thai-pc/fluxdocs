# ai-extraction-demo

Structured data extraction from a PDF using the FluxDocs **Cloud** tier.

The pure-Go core extracts the document text locally, then a `cloud/ai` Claude
client turns it into JSON via a single forced tool call. The text is sent to the
Anthropic API **only** because the program installs a Cloud backend with
`doc.SetAIClient(...)` — the MIT core never makes that network call on its own.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/ai-extraction-demo invoice.pdf
```

Output is a JSON object with the fields declared in `main.go` (`vendor`, `date`,
`total`, `line_items`). Edit the `core.Schema` there to pull different fields.

Uses `claude-opus-4-8` by default; override `ai.New("").Model` to change it.

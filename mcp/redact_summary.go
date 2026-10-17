package mcp

import "github.com/thai-pc/fluxdocs/core"

// withSummary adds a redaction's text-free summary (core.RedactionReport
// Summary: what automatic extension and the attachments policy removed
// beyond the plan) to a tool result as auto_extended_summary, when there is
// one.
func withSummary(res map[string]any, out *core.Document) map[string]any {
	if r, ok := out.RedactionReport(); ok {
		if s := r.Summary(); s != "" {
			res["auto_extended_summary"] = s
		}
	}
	return res
}

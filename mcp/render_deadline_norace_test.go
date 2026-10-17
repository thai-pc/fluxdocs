//go:build !race

package mcp

import (
	"strings"
	"testing"
	"time"
)

// TestRenderPage_DeadlineMidRender: a deadline that fires while the render
// runs (not one already past at its start, as in
// TestRenderPage_BudgetAndDeadline) still aborts it promptly. Without -race
// (which slows the render about tenfold, so on a loaded host the budget
// could trip first) a 2000 pt page at 150 DPI spends its default budget
// (≈ 556 M pixel visits) over seconds, far past 50 ms.
func TestRenderPage_DeadlineMidRender(t *testing.T) {
	s := NewServer()
	old := renderTimeout
	t.Cleanup(func() { renderTimeout = old })
	renderTimeout = 50 * time.Millisecond

	h := openHandle(t, s, writeFloodPDF(t, 2000, 100))
	start := time.Now()
	item, isErr := callTool(t, s, "render_page", map[string]any{"handle": h, "page": 0, "dpi": 150})
	if !isErr || !strings.Contains(item["text"].(string), "deadline exceeded") {
		t.Errorf("slow render_page = %v (isErr=%v), want a deadline tool error", item, isErr)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("render_page took %v, want shortly after the 50ms deadline", elapsed)
	}
}

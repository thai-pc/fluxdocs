package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFloodPDF writes a one-page PDF (size x size pt) that repeats a full-page
// fill n times and returns its path.
func writeFloodPDF(t *testing.T, size, n int) string {
	t.Helper()
	content := strings.Repeat(fmt.Sprintf("0 0 %d %d re f\n", size, size), n)
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 %d %d] >>", size, size),
		3: "<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>",
		4: stream(content),
	}, "<< /Size 5 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "flood.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRenderPage_BudgetAndDeadline: render_page runs under the core's render
// budget and a renderTimeout deadline; both come back as a tool error
// ("render failed: …"), not a hang.
func TestRenderPage_BudgetAndDeadline(t *testing.T) {
	s := NewServer()
	old := renderTimeout
	t.Cleanup(func() { renderTimeout = old })

	// 100 pt at 72 DPI: the 16 Mi floor allows ≈ 1600 fills. The deadline is
	// lifted so the outcome cannot depend on host speed (under -race on a
	// loaded host the 10 s default can fire before the budget trips).
	renderTimeout = time.Hour
	h := openHandle(t, s, writeFloodPDF(t, 100, 3000))
	item, isErr := callTool(t, s, "render_page", map[string]any{"handle": h, "page": 0, "dpi": 72})
	if !isErr || !strings.Contains(item["text"].(string), "render failed: fluxdocs: render budget exceeded") {
		t.Errorf("over-budget render_page = %v (isErr=%v), want a budget tool error", item, isErr)
	}

	// A deadline that has passed before the render starts: the render's first
	// poll reports it, long before the default budget (≈ 556 M pixel visits
	// for 2000 pt at 150 DPI) could trip, however fast or loaded the host is.
	// A 50 ms deadline raced the budget under -race on a loaded host.
	renderTimeout = time.Nanosecond
	h = openHandle(t, s, writeFloodPDF(t, 2000, 100))
	start := time.Now()
	item, isErr = callTool(t, s, "render_page", map[string]any{"handle": h, "page": 0, "dpi": 150})
	if !isErr || !strings.Contains(item["text"].(string), "deadline exceeded") {
		t.Errorf("slow render_page = %v (isErr=%v), want a deadline tool error", item, isErr)
	}
	// A generous sanity bound only: the poll interval keeps the abort prompt.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("render_page took %v after its deadline, want an abort at the first poll", elapsed)
	}
}

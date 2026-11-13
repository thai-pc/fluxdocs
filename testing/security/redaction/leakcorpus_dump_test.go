package redaction

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLeakFixtureDump writes every leak-corpus fixture as a PDF into the
// directory FLUXDOCS_LEAK_DUMP names, for the core package's property tests
// of matching across operators (core/redact_stream_test.go, which read them
// through FLUXDOCS_LEAK_FIXTURES: the fixtures are built in this package,
// which core cannot import). Skipped when the variable is not set;
// TestLeakFixtureProperties runs those tests on them without it.
func TestLeakFixtureDump(t *testing.T) {
	dir := os.Getenv("FLUXDOCS_LEAK_DUMP")
	if dir == "" {
		t.Skip("FLUXDOCS_LEAK_DUMP not set")
	}
	n := writeLeakFixtures(t, dir)
	t.Logf("wrote %d fixtures to %s", n, dir)
}

// writeLeakFixtures writes every leak-corpus fixture as a PDF into dir and
// returns how many it wrote.
func writeLeakFixtures(t *testing.T, dir string) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, lc := range leakCases() {
		// Names repeat across tables (malformed/…, deep/…): prefix the row.
		name := fmt.Sprintf("%03d_%s.pdf", i, strings.NewReplacer("/", "_", " ", "_").Replace(lc.name))
		if err := os.WriteFile(filepath.Join(dir, name), lc.pdf(), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

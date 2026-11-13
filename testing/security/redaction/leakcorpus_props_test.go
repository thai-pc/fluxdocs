//go:build !js

package redaction

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLeakFixtureProperties runs the core package's property tests of
// matching across operators (P1–P3, core/redact_stream_test.go) on every
// leak-corpus fixture: it writes them to a temporary directory and runs
// `go test` on package core with FLUXDOCS_LEAK_FIXTURES pointing there. The
// fixtures are built in this package, which core cannot import, so this is
// how a plain `go test ./...` covers them. Skipped under -short, or when no
// Go toolchain is found to run the tests with.
func TestLeakFixtureProperties(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test on package core: not under -short")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err := os.Stat(goTool); err != nil {
			t.Skip("no go toolchain to run package core's tests with")
		}
	}
	dir := t.TempDir()
	n := writeLeakFixtures(t, dir)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl.Add(-10*time.Second))
		defer cancel()
	}
	// The child is a separate `go test`: this run's -tags, -race and other
	// build flags are not forwarded (it builds package core plainly), and
	// this package's test cache key does not cover core's _test.go files nor
	// the fixtures, so the child always runs (-count=1) and this test's
	// result may be cached while core's tests change: run with -count=1 to
	// re-check.
	cmd := exec.CommandContext(ctx, goTool, "test", "-count=1", "-v",
		"-run", `^TestRedactStream_P(1P2|3)$`, "github.com/thai-pc/fluxdocs/core")
	cmd.Env = append(os.Environ(), "FLUXDOCS_LEAK_FIXTURES="+dir)
	out, err := cmd.CombinedOutput()
	p3 := false
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "P1/P2:") || strings.Contains(l, "P3:") && strings.Contains(l, "documents") {
			t.Log(strings.TrimSpace(l))
			p3 = p3 || strings.Contains(l, "P3:")
		}
	}
	if err != nil {
		t.Fatalf("P1–P3 on %d leak fixtures: %v\n%s", n, err, out)
	}
	// A child that ran nothing (a renamed test, a -run that matches
	// nothing) also exits 0: require both tests to have passed, and P3's
	// summary.
	for _, want := range []string{"--- PASS: TestRedactStream_P1P2", "--- PASS: TestRedactStream_P3"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the child run lacks %q:\n%s", want, out)
		}
	}
	if !p3 {
		t.Errorf("the child run lacks P3's documents line:\n%s", out)
	}
}

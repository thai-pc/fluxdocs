package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

func TestWatermark(t *testing.T) {
	path := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "wm.pdf")
	code, stdout, errs := runCLI("watermark", path, "--text", "DRAFT", "-o", out)
	if code != 0 {
		t.Fatalf("watermark: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "wrote") {
		t.Errorf("stdout=%q", stdout)
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := doc.ExtractText(core.ExtractOptions{}); !strings.Contains(txt, "DRAFT") {
		t.Errorf("watermark text not baked in: %q", txt)
	}
}

func TestWatermark_RequiresText(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, _ := runCLI("watermark", path); code != 1 {
		t.Errorf("watermark without --text: code=%d, want 1", code)
	}
}

func TestRedact(t *testing.T) {
	// The sample PDF shows "Page 0" on its page; redact the digit.
	path := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "red.pdf")
	code, stdout, errs := runCLI("redact", path, "--pattern", `Page \d`, "-o", out)
	if code != 0 {
		t.Fatalf("redact: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "redacted") {
		t.Errorf("stdout=%q", stdout)
	}
	// The redacted text must be gone from both extraction and the raw bytes.
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := doc.ExtractText(core.ExtractOptions{}); strings.Contains(txt, "Page 0") {
		t.Errorf("LEAK: redacted text still extractable: %q", txt)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "Page 0") {
		t.Errorf("LEAK: redacted text remains in the raw bytes")
	}
}

func TestRedact_RequiresPattern(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, _ := runCLI("redact", path); code != 1 {
		t.Errorf("redact without --pattern: code=%d, want 1", code)
	}
}

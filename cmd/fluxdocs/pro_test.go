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

func TestAutoRedact(t *testing.T) {
	content := "BT /F1 12 Tf 72 700 Td (SSN 123-45-6789 card 4111 1111 1111 1111) Tj 0 -20 Td (public line) Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: streamObj(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "pii.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "clean.pdf")

	code, stdout, errs := runCLI("autoredact", path, "--kinds", "ssn,credit_card", "-o", out)
	if code != 0 {
		t.Fatalf("autoredact: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "auto-redacted") {
		t.Errorf("stdout=%q", stdout)
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	txt, _ := doc.ExtractText(core.ExtractOptions{})
	for _, secret := range []string{"123-45-6789", "4111 1111 1111 1111"} {
		if strings.Contains(txt, secret) {
			t.Errorf("LEAK: %q still extractable", secret)
		}
	}
	if !strings.Contains(txt, "public line") {
		t.Errorf("over-redacted: public text lost: %q", txt)
	}
}

func TestAutoRedact_UnknownKinds(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, _ := runCLI("autoredact", path, "--kinds", "bogus"); code != 1 {
		t.Errorf("autoredact with only unknown kinds: code=%d, want 1", code)
	}
}

// TestRedact_RefusalNamesReason: a page the core refuses to redact safely
// fails the command with the reason code in the message, and writes nothing.
func TestRedact_RefusalNamesReason(t *testing.T) {
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >> /Contents 4 0 R >>",
		4: streamObj("/Fm1 Do"),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		6: strings.Replace(streamObj("BT /F1 12 Tf 20 100 Td (Page 0) Tj ET"), "<<",
			"<< /Type /XObject /Subtype /Form /BBox [0 0 200 200] /Resources << /Font << /F1 5 0 R >> >>", 1),
	}, "<< /Size 7 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "form.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.pdf")
	code, _, errs := runCLI("redact", path, "--pattern", `Page \d`, "-o", out)
	if code != 1 || !strings.Contains(errs, "cannot be redacted safely") || !strings.Contains(errs, "form_text") {
		t.Errorf("redact: code=%d errs=%q, want 1 and a form_text refusal", code, errs)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused redaction wrote an output file")
	}
}

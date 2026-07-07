package mcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// openHandle opens a PDF path via the MCP tool and returns its handle.
func openHandle(t *testing.T, s *Server, path string) int {
	t.Helper()
	item, isErr := callTool(t, s, "open_document", map[string]any{"path": path})
	if isErr {
		t.Fatalf("open_document error: %v", item)
	}
	var o struct {
		Handle int `json:"handle"`
	}
	json.Unmarshal([]byte(item["text"].(string)), &o)
	return o.Handle
}

// writeTextPDF writes a one-page PDF showing text and returns the path.
func writeTextPDF(t *testing.T, text string) string {
	t.Helper()
	content := "BT /F1 12 Tf 20 100 Td (" + text + ") Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), strings.ReplaceAll(text, " ", "_")+".pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToolsList_IncludesProTools(t *testing.T) {
	s := NewServer()
	m := rpc(t, s, 1, "tools/list", nil)
	names := map[string]bool{}
	for _, tt := range m["result"].(map[string]any)["tools"].([]any) {
		names[tt.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"watermark_and_save", "compare_documents", "convert_to_pdfa", "get_form_fields", "sign_and_save"} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
}

func TestCompareDocuments(t *testing.T) {
	s := NewServer()
	a := openHandle(t, s, writeTextPDF(t, "the cat sat"))
	b := openHandle(t, s, writeTextPDF(t, "the dog sat"))

	item, isErr := callTool(t, s, "compare_documents", map[string]any{"handle_a": a, "handle_b": b})
	if isErr {
		t.Fatalf("compare error: %v", item)
	}
	var res struct {
		Identical bool `json:"identical"`
		Pages     []struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		} `json:"pages"`
	}
	json.Unmarshal([]byte(item["text"].(string)), &res)
	if res.Identical || len(res.Pages) != 1 {
		t.Fatalf("compare result = %+v", res)
	}
	if !contains(res.Pages[0].Removed, "cat") || !contains(res.Pages[0].Added, "dog") {
		t.Errorf("expected -cat +dog, got %+v", res.Pages[0])
	}
}

func TestWatermarkAndSave(t *testing.T) {
	s := NewServer()
	h := openHandle(t, s, writeTextPDF(t, "body"))
	out := filepath.Join(t.TempDir(), "wm.pdf")
	if _, isErr := callTool(t, s, "watermark_and_save", map[string]any{
		"handle": h, "text": "SECRET", "out_path": out,
	}); isErr {
		t.Fatal("watermark_and_save errored")
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := doc.ExtractText(core.ExtractOptions{}); !strings.Contains(txt, "SECRET") {
		t.Errorf("watermark not applied: %q", txt)
	}
}

func TestConvertToPDFA_Tool(t *testing.T) {
	s := NewServer()
	h := openHandle(t, s, writeTextPDF(t, "archive"))
	out := filepath.Join(t.TempDir(), "pdfa.pdf")
	if _, isErr := callTool(t, s, "convert_to_pdfa", map[string]any{"handle": h, "out_path": out}); isErr {
		t.Fatal("convert_to_pdfa errored")
	}
	// The written file must validate as PDF/A (markers preserved via Bytes()).
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := doc.ValidatePDFA(); !r.Conformant {
		t.Errorf("saved PDF/A file is not conformant: %+v", r.Checks)
	}
}

func TestSignAndSave(t *testing.T) {
	s := NewServer()
	path := writeTextPDF(t, "sign me")
	h := openHandle(t, s, path)

	// Write cert+key.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "MCP"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "c.pem")
	keyPath := filepath.Join(dir, "k.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o644)

	out := filepath.Join(dir, "signed.pdf")
	if _, isErr := callTool(t, s, "sign_and_save", map[string]any{
		"handle": h, "cert_path": certPath, "key_path": keyPath, "out_path": out, "reason": "ok",
	}); isErr {
		t.Fatal("sign_and_save errored")
	}
	// The signed file appends to the original (incremental update preserved).
	orig, _ := os.ReadFile(path)
	signed, _ := os.ReadFile(out)
	if len(signed) <= len(orig) || string(signed[:len(orig)]) != string(orig) {
		t.Errorf("signed file was rebuilt, not appended (signature would be invalid)")
	}
	doc, _ := core.OpenDocument(out)
	fields, _ := doc.GetFormFields()
	has := false
	for _, f := range fields {
		if f.Type == core.FieldSignature {
			has = true
		}
	}
	if !has {
		t.Errorf("no signature field in the signed file")
	}
}

func TestGetFormFields_Tool(t *testing.T) {
	s := NewServer()
	// A doc with an AcroForm text field.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 200] >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< >>",
		5: "<< /Fields [6 0 R] >>",
		6: "<< /FT /Tx /T (name) /V (Alice) >>",
	}, "<< /Size 7 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "form.pdf")
	os.WriteFile(path, pdf, 0o644)
	h := openHandle(t, s, path)

	item, isErr := callTool(t, s, "get_form_fields", map[string]any{"handle": h})
	if isErr {
		t.Fatalf("get_form_fields error: %v", item)
	}
	if !strings.Contains(item["text"].(string), `"name":"name"`) ||
		!strings.Contains(item["text"].(string), `"value":"Alice"`) {
		t.Errorf("form fields JSON = %s", item["text"])
	}
}

func contains(s []string, w string) bool {
	for _, x := range s {
		if x == w {
			return true
		}
	}
	return false
}

// piiSecretPDF writes a one-page PDF containing PII plus a public line.
func piiSecretPDF(t *testing.T) string {
	t.Helper()
	content := "BT /F1 12 Tf 72 700 Td (SSN 123-45-6789 mail a@b.com) Tj 0 -20 Td (public notice) Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "pii.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAutoRedactPIITool(t *testing.T) {
	s := NewServer()
	h := openHandle(t, s, piiSecretPDF(t))
	out := filepath.Join(t.TempDir(), "clean.pdf")

	item, isErr := callTool(t, s, "auto_redact_pii", map[string]any{
		"handle": h, "out_path": out, "kinds": []string{"ssn", "email"},
	})
	if isErr {
		t.Fatalf("auto_redact_pii error: %v", item)
	}
	if !strings.Contains(item["text"].(string), "\"saved\"") {
		t.Errorf("unexpected result: %v", item["text"])
	}

	// The tool writes verbatim (writeExact); the leak fix must hold — the PII
	// must be gone from both the extracted text and the raw bytes.
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := doc.ExtractText(core.ExtractOptions{})
	if strings.Contains(text, "123-45-6789") || strings.Contains(text, "a@b.com") {
		t.Errorf("LEAK: PII still extractable: %q", text)
	}
	if !strings.Contains(text, "public notice") {
		t.Errorf("over-redacted: public text lost: %q", text)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "123-45-6789") {
		t.Errorf("LEAK: SSN bytes remain in the verbatim MCP output")
	}
}

func TestExtractTablesTool(t *testing.T) {
	s := NewServer()
	content := "BT /F1 10 Tf 1 0 0 1 72 700 Tm (Name) Tj 1 0 0 1 300 700 Tm (Age) Tj " +
		"1 0 0 1 72 680 Tm (Alice) Tj 1 0 0 1 300 680 Tm (30) Tj ET"
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: stream(content),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
	path := filepath.Join(t.TempDir(), "grid.pdf")
	os.WriteFile(path, pdf, 0o644)
	h := openHandle(t, s, path)
	item, isErr := callTool(t, s, "extract_tables", map[string]any{"handle": h, "page": 0})
	if isErr {
		t.Fatalf("extract_tables error: %v", item)
	}
	if !strings.Contains(item["text"].(string), "Alice") || !strings.Contains(item["text"].(string), "\"tables\"") {
		t.Errorf("result = %v", item["text"])
	}
}

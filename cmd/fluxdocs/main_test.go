package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// buildPDF assembles a classic-xref PDF from object bodies (1-based) + a trailer,
// computing offsets — mirrors the parse-package test builder.
func buildPDF(objects map[int]string, trailer string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.7\n")
	n := len(objects)
	for i := 1; i <= n; i++ {
		off[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i, objects[i]))
	}
	xref := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", n+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n" + trailer + "\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return b.Bytes()
}

func streamObj(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

// samplePDF builds an n-page PDF where page k shows the text "Page k".
func samplePDF(n int) []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
	}
	fontObj := 3 + 2*n // font after the page/content pairs
	objs[fontObj] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	kids := ""
	obj := 3
	for i := 0; i < n; i++ {
		pageObj, contentObj := obj, obj+1
		obj += 2
		kids += fmt.Sprintf("%d 0 R ", pageObj)
		objs[pageObj] = fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontObj, contentObj)
		objs[contentObj] = streamObj(fmt.Sprintf("BT /F1 12 Tf 20 100 Td (Page %d) Tj ET", i))
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n)
	return buildPDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", fontObj+1))
}

// writePDF writes an n-page sample PDF to a temp file and returns its path.
func writePDF(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.pdf")
	if err := os.WriteFile(path, samplePDF(n), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runCLI invokes run() capturing stdout/stderr.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRun_VersionAndHelp(t *testing.T) {
	if code, out, _ := runCLI("version"); code != 0 || !strings.Contains(out, "fluxdocs "+version) {
		t.Errorf("version: code=%d out=%q", code, out)
	}
	if code, out, _ := runCLI("help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: code=%d out=%q", code, out)
	}
}

func TestRun_NoArgsAndUnknown(t *testing.T) {
	if code, _, _ := runCLI(); code != 2 {
		t.Errorf("no args: code=%d, want 2", code)
	}
	if code, _, errs := runCLI("bogus"); code != 2 || !strings.Contains(errs, "unknown command") {
		t.Errorf("unknown: code=%d errs=%q", code, errs)
	}
}

func TestInfo(t *testing.T) {
	path := writePDF(t, 2)
	code, out, errs := runCLI("info", path)
	if code != 0 {
		t.Fatalf("code=%d errs=%q", code, errs)
	}
	if !strings.Contains(out, "Pages:    2") || !strings.Contains(out, "200 x 200 pt") {
		t.Errorf("info out=%q", out)
	}
}

func TestInfo_MissingFile(t *testing.T) {
	if code, _, errs := runCLI("info", "/no/such/file.pdf"); code != 1 || !strings.Contains(errs, "info:") {
		t.Errorf("missing file: code=%d errs=%q", code, errs)
	}
}

func TestExtract(t *testing.T) {
	path := writePDF(t, 3)
	// All pages.
	code, out, _ := runCLI("extract", path)
	if code != 0 || !strings.Contains(out, "Page 0") || !strings.Contains(out, "Page 2") {
		t.Errorf("extract all: code=%d out=%q", code, out)
	}
	// Single page.
	code, out, _ = runCLI("extract", path, "--page", "1")
	if code != 0 || !strings.Contains(out, "Page 1") || strings.Contains(out, "Page 0") {
		t.Errorf("extract --page 1: code=%d out=%q", code, out)
	}
}

func TestExtract_BadArgs(t *testing.T) {
	if code, _, _ := runCLI("extract"); code != 1 {
		t.Errorf("extract no file: code=%d, want 1", code)
	}
}

func TestRender(t *testing.T) {
	path := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "page.png")
	code, stdout, errs := runCLI("render", path, "--page", "0", "--dpi", "72", "-o", out)
	if code != 0 {
		t.Fatalf("render: code=%d errs=%q", code, errs)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if len(data) < 8 || data[0] != 0x89 || data[1] != 'P' {
		t.Errorf("output is not a PNG (magic %v)", data[:2])
	}
	if !strings.Contains(stdout, "wrote") {
		t.Errorf("stdout=%q", stdout)
	}
}

func TestRender_OutOfRange(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, errs := runCLI("render", path, "--page", "5"); code != 1 || !strings.Contains(errs, "render:") {
		t.Errorf("render bad page: code=%d errs=%q", code, errs)
	}
}

func TestMerge(t *testing.T) {
	a := writePDF(t, 2)
	b := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "merged.pdf")
	code, stdout, errs := runCLI("merge", "-o", out, a, b)
	if code != 0 {
		t.Fatalf("merge: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "3 pages") {
		t.Errorf("merge stdout=%q, want 3 pages", stdout)
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatalf("reopen merged: %v", err)
	}
	if doc.PageCount != 3 {
		t.Errorf("merged PageCount=%d, want 3", doc.PageCount)
	}
}

func TestMerge_RequiresOutputAndInputs(t *testing.T) {
	a := writePDF(t, 1)
	if code, _, _ := runCLI("merge", a, a); code != 1 {
		t.Errorf("merge without -o: code=%d, want 1", code)
	}
	out := filepath.Join(t.TempDir(), "x.pdf")
	if code, _, _ := runCLI("merge", "-o", out, a); code != 1 {
		t.Errorf("merge one input: code=%d, want 1", code)
	}
}

func TestSplit(t *testing.T) {
	path := writePDF(t, 3)
	prefix := filepath.Join(t.TempDir(), "out")
	code, stdout, errs := runCLI("split", path, "--ranges", "0-1,2-2", "-o", prefix)
	if code != 0 {
		t.Fatalf("split: code=%d errs=%q", code, errs)
	}
	if !strings.Contains(stdout, "out-1.pdf") || !strings.Contains(stdout, "out-2.pdf") {
		t.Errorf("split stdout=%q", stdout)
	}
	p1, err := core.OpenDocument(prefix + "-1.pdf")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := core.OpenDocument(prefix + "-2.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if p1.PageCount != 2 || p2.PageCount != 1 {
		t.Errorf("split parts = %d,%d pages, want 2,1", p1.PageCount, p2.PageCount)
	}
}

func TestSplit_InvalidRange(t *testing.T) {
	path := writePDF(t, 2)
	if code, _, errs := runCLI("split", path, "--ranges", "0-9"); code != 1 || !strings.Contains(errs, "split:") {
		t.Errorf("split bad range: code=%d errs=%q", code, errs)
	}
	if code, _, _ := runCLI("split", path); code != 1 {
		t.Errorf("split without --ranges: code=%d, want 1", code)
	}
}

func TestRotate(t *testing.T) {
	path := writePDF(t, 1)
	out := filepath.Join(t.TempDir(), "rot.pdf")
	code, _, errs := runCLI("rotate", path, "--page", "0", "--degrees", "90", "-o", out)
	if code != 0 {
		t.Fatalf("rotate: code=%d errs=%q", code, errs)
	}
	doc, err := core.OpenDocument(out)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pages[0].Rotation != 90 {
		t.Errorf("rotated page Rotation=%d, want 90", doc.Pages[0].Rotation)
	}
}

func TestRotate_BadInput(t *testing.T) {
	path := writePDF(t, 1)
	if code, _, _ := runCLI("rotate", path, "--degrees", "90"); code != 1 {
		t.Errorf("rotate without --page: code=%d, want 1", code)
	}
	if code, _, errs := runCLI("rotate", path, "--page", "0", "--degrees", "45"); code != 1 || !strings.Contains(errs, "rotate:") {
		t.Errorf("rotate non-multiple-of-90: code=%d errs=%q", code, errs)
	}
}

func TestParseRanges(t *testing.T) {
	tests := []struct {
		in   string
		want [][2]int
		err  bool
	}{
		{"0-1,2-3", [][2]int{{0, 1}, {2, 3}}, false},
		{"5", [][2]int{{5, 5}}, false},
		{"0-2, 4", [][2]int{{0, 2}, {4, 4}}, false},
		{"", nil, true},
		{"a-b", nil, true},
		{"1-x", nil, true},
	}
	for _, tt := range tests {
		got, err := parseRanges(tt.in)
		if tt.err {
			if err == nil {
				t.Errorf("parseRanges(%q) = %v, want error", tt.in, got)
			}
			continue
		}
		if err != nil || len(got) != len(tt.want) {
			t.Errorf("parseRanges(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseRanges(%q)[%d] = %v, want %v", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestTrimPDFExt(t *testing.T) {
	for in, want := range map[string]string{
		"a.pdf": "a", "b.PDF": "b", "c": "c", "dir/x.pdf": "dir/x",
	} {
		if got := trimPDFExt(in); got != want {
			t.Errorf("trimPDFExt(%q) = %q, want %q", in, got, want)
		}
	}
}

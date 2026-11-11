//go:build !js

package render

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tb "github.com/thai-pc/fluxdocs/core/internal/psfont/t1build"
)

// TestType1_GhostscriptOracle renders the hand-built Type 1 font pages with
// Ghostscript and compares the ink bounding boxes with ours (±1 px at 72 DPI;
// design docs/design/type1-std14-fonts.md §7.1). It is an independent check
// of the cipher, charstring, closepath, flex and seac semantics: Ghostscript
// is an external black-box tool (AGPL) run as a separate process in tests
// only, nothing is linked or vendored. It skips without gs on PATH unless
// FLUXDOCS_REQUIRE_GS=1.
func TestType1_GhostscriptOracle(t *testing.T) {
	gs, _ := exec.LookPath("gs")
	if gs == "" {
		if os.Getenv("FLUXDOCS_REQUIRE_GS") == "1" {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not installed")
	}
	f := t1TestFont()
	l1, l2, l3 := f.Lengths()
	hex, pfb := f, f
	hex.Hex, pfb.PFB = true, true
	flex := tb.Font{
		Encoding: map[int]string{0x41: "flex"},
		Subrs:    [][]byte{tb.CS(tb.Return), tb.CS(tb.Return), tb.CS(tb.Return), tb.CS(0, 50, tb.HStem, tb.Return)},
		Glyphs: []tb.Glyph{{Name: "flex", CS: tb.CS(0, 1000, tb.HSBW, 100, 100, tb.RMoveTo, 3, 1, 3, tb.CallOtherSubr, tb.Pop, tb.CallSubr,
			100, tb.HLineTo, 0, 1, tb.CallOtherSubr,
			200, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 0, 100, tb.RMoveTo, 0, 2, tb.CallOtherSubr,
			100, 100, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 100, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr,
			100, 0, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 100, -100, tb.RMoveTo, 0, 2, tb.CallOtherSubr,
			0, -100, tb.RMoveTo, 0, 2, tb.CallOtherSubr, 50, 800, 100, 3, 0, tb.CallOtherSubr, tb.Pop, tb.Pop, tb.SetCurrentPoint,
			300, tb.VLineTo, -600, tb.HLineTo, tb.ClosePath, tb.EndChar)}},
	}
	fl1, fl2, fl3 := flex.Lengths()
	for _, tt := range []struct {
		name string
		pdf  []byte
	}{
		{"square", t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"ring", t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 20 30 Td (B) Tj ET")},
		{"seac", t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 20 30 Td (C) Tj ET")},
		{"advance", t1PDF(f.Bytes(), l1, l2, l3, "", "BT /F1 100 Tf 0 30 Td (AB) Tj ET")},
		{"widths", t1PDF(f.Bytes(), l1, l2, l3, "/FirstChar 65 /LastChar 65 /Widths [1000]", "BT /F1 100 Tf 0 30 Td (AB) Tj ET")},
		{"winansi", t1PDF(f.Bytes(), l1, l2, l3, "/Encoding /WinAnsiEncoding", "BT /F1 100 Tf 20 30 Td (A) Tj ET")},
		{"hex", t1PDF(hex.Bytes(), 0, 0, 0, "", "BT /F1 100 Tf 20 30 Td (B) Tj ET")},
		{"pfb", t1PDF(pfb.Bytes(), 0, 0, 0, "", "BT /F1 100 Tf 20 30 Td (C) Tj ET")},
		{"flex", t1PDF(flex.Bytes(), fl1, fl2, fl3, "", "BT /F1 100 Tf 10 30 Td (A) Tj ET")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ours := renderPDF(t, tt.pdf)
			theirs := gsPNG(t, gs, tt.pdf)
			for _, half := range []image.Rectangle{image.Rect(0, 0, 200, 200), image.Rect(0, 0, 200, 135), image.Rect(0, 135, 200, 200), image.Rect(0, 0, 75, 200)} {
				a, okA := inkBox(ours, half)
				b, okB := inkBox(theirs, half)
				if okA != okB || okA && !near(a, b) {
					t.Errorf("region %v: ink box = %v (%v), Ghostscript %v (%v)", half, a, okA, b, okB)
				}
			}
		})
	}
}

// gsPNG renders pdf's first page with Ghostscript at 72 DPI, gray.
func gsPNG(t *testing.T, gs string, pdf []byte) image.Image {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=pnggray", "-r72",
		"-dTextAlphaBits=1", "-dGraphicsAlphaBits=1", "-sOutputFile="+out, in)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gs: %v: %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

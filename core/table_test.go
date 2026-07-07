package core

import (
	"fmt"
	"strings"
	"testing"
)

// gridPDF builds a one-page PDF whose content places each cell at an absolute
// position via Tm, forming rows×cols grid.
func gridPDF(cells [][]string, xs []float64, ys []float64) []byte {
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf ")
	for r, row := range cells {
		for c, text := range row {
			fmt.Fprintf(&content, "1 0 0 1 %g %g Tm (%s) Tj ", xs[c], ys[r], text)
		}
	}
	content.WriteString("ET")
	body := content.String()

	return buildCorePDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		3: "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
		5: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "<< /Size 6 /Root 1 0 R >>")
}

func TestExtractTables_Grid(t *testing.T) {
	cells := [][]string{
		{"Name", "Age", "City"},
		{"Alice", "30", "NYC"},
		{"Bob", "25", "LA"},
	}
	doc, err := OpenBytes(gridPDF(cells, []float64{72, 220, 380}, []float64{700, 680, 660}))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	tables, err := doc.ExtractTables(0)
	if err != nil {
		t.Fatalf("ExtractTables: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(tables))
	}
	got := tables[0].Rows
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3: %v", len(got), got)
	}
	for r := range cells {
		if len(got[r]) != 3 {
			t.Fatalf("row %d has %d cols: %v", r, len(got[r]), got[r])
		}
		for c := range cells[r] {
			if got[r][c] != cells[r][c] {
				t.Errorf("cell [%d][%d] = %q, want %q", r, c, got[r][c], cells[r][c])
			}
		}
	}
	if tables[0].PageIndex != 0 {
		t.Errorf("PageIndex = %d, want 0", tables[0].PageIndex)
	}
	if tables[0].Bounds.Width <= 0 || tables[0].Bounds.Height <= 0 {
		t.Errorf("degenerate bounds: %+v", tables[0].Bounds)
	}
}

func TestExtractTables_NonTabular(t *testing.T) {
	// A single left-aligned column of prose is not a table.
	cells := [][]string{{"Just a paragraph line one"}, {"and line two"}, {"and line three"}}
	doc, err := OpenBytes(gridPDF(cells, []float64{72}, []float64{700, 680, 660}))
	if err != nil {
		t.Fatal(err)
	}
	tables, err := doc.ExtractTables(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 0 {
		t.Errorf("prose page produced %d tables, want 0: %v", len(tables), tables)
	}
}

func TestExtractTables_Errors(t *testing.T) {
	doc, _ := OpenBytes(gridPDF([][]string{{"a", "b"}}, []float64{72, 200}, []float64{700}))
	if _, err := doc.ExtractTables(5); err != ErrPageNotFound {
		t.Errorf("out-of-range err = %v, want ErrPageNotFound", err)
	}
	// A document not opened from bytes has no resolver.
	bare := &Document{Pages: []Page{{Index: 0}}}
	if _, err := bare.ExtractTables(0); err != ErrInvalidPDF {
		t.Errorf("resolver-less err = %v, want ErrInvalidPDF", err)
	}
}

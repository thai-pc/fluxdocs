package parse

import (
	"bytes"
	"fmt"
	"testing"
)

// buildPDF assembles a PDF from a set of indirect-object bodies (keyed by object
// number, 1-based and contiguous) plus a trailer dictionary body, writing a
// classic xref table with dynamically computed offsets.
func buildPDF(objects map[int]string, trailer string) []byte {
	var b bytes.Buffer
	off := map[int]int{}
	b.WriteString("%PDF-1.7\n")

	n := len(objects)
	for i := 1; i <= n; i++ {
		off[i] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i, objects[i]))
	}

	xrefStart := b.Len()
	b.WriteString("xref\n")
	b.WriteString(fmt.Sprintf("0 %d\n", n+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off[i]))
	}
	b.WriteString("trailer\n" + trailer + "\n")
	b.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xrefStart))
	return b.Bytes()
}

func TestPages_SinglePage(t *testing.T) {
	r, err := NewResolver(buildMinimalPDF())
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.MediaBox.Width() != 612 || p.MediaBox.Height() != 792 {
		t.Errorf("MediaBox = %.0fx%.0f, want 612x792", p.MediaBox.Width(), p.MediaBox.Height())
	}
	if p.Rotate != 0 {
		t.Errorf("Rotate = %d, want 0", p.Rotate)
	}
	if ref, ok := p.Contents.(Reference); !ok || ref.Number != 4 {
		t.Errorf("Contents = %v, want Reference{4 0}", p.Contents)
	}
}

func TestPages_InheritedAttributes(t *testing.T) {
	// MediaBox + Rotate defined on the intermediate Pages node; page 3 inherits,
	// page 4 overrides both.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 300] /Rotate 90 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 500] /Rotate 270 >>",
	}, "<< /Size 5 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages) = %d, want 2", len(pages))
	}

	// Page 0 inherits from the Pages node.
	if w, h := pages[0].MediaBox.Width(), pages[0].MediaBox.Height(); w != 200 || h != 300 {
		t.Errorf("page0 MediaBox = %.0fx%.0f, want 200x300 (inherited)", w, h)
	}
	if pages[0].Rotate != 90 {
		t.Errorf("page0 Rotate = %d, want 90 (inherited)", pages[0].Rotate)
	}

	// Page 1 overrides both.
	if w, h := pages[1].MediaBox.Width(), pages[1].MediaBox.Height(); w != 400 || h != 500 {
		t.Errorf("page1 MediaBox = %.0fx%.0f, want 400x500 (own)", w, h)
	}
	if pages[1].Rotate != 270 {
		t.Errorf("page1 Rotate = %d, want 270 (own)", pages[1].Rotate)
	}
}

func TestPages_NestedTreeOrder(t *testing.T) {
	// A two-level tree: root -> [intermediate(3) -> [5,6], leaf(4)].
	// Document order must be 5, 6, 4.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 3 /MediaBox [0 0 100 100] >>",
		3: "<< /Type /Pages /Parent 2 0 R /Kids [5 0 R 6 0 R] /Count 2 >>",
		4: "<< /Type /Page /Parent 2 0 R >>",
		5: "<< /Type /Page /Parent 3 0 R >>",
		6: "<< /Type /Page /Parent 3 0 R >>",
	}, "<< /Size 7 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("len(pages) = %d, want 3", len(pages))
	}
	// All inherit MediaBox 100x100 from the root.
	for i, p := range pages {
		if p.MediaBox.Width() != 100 {
			t.Errorf("page %d MediaBox width = %.0f, want 100 (inherited from root)", i, p.MediaBox.Width())
		}
	}
}

func TestPages_CyclicKidsTerminates(t *testing.T) {
	// The Pages node references itself in /Kids. The walk must terminate with no
	// leaf pages rather than loop forever.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [2 0 R] /Count 1 >>",
	}, "<< /Size 3 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatalf("Pages() on cyclic tree = %v, want nil error", err)
	}
	if len(pages) != 0 {
		t.Errorf("len(pages) = %d, want 0 for a cyclic intermediate node", len(pages))
	}
}

func TestPages_DefaultMediaBox(t *testing.T) {
	// No MediaBox anywhere -> lenient US Letter default.
	pdf := buildPDF(map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R >>",
	}, "<< /Size 4 /Root 1 0 R >>")

	r, err := NewResolver(pdf)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1", len(pages))
	}
	if w, h := pages[0].MediaBox.Width(), pages[0].MediaBox.Height(); w != 612 || h != 792 {
		t.Errorf("MediaBox = %.0fx%.0f, want default 612x792", w, h)
	}
}

func TestNormalizeRotate(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 0}, {90, 90}, {180, 180}, {270, 270},
		{360, 0}, {450, 90}, {-90, 270}, {-360, 0},
	}
	for _, tt := range tests {
		if got := normalizeRotate(tt.in); got != tt.want {
			t.Errorf("normalizeRotate(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

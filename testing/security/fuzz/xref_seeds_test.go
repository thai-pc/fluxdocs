package fuzz

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// xrefBombPDF is a one-page document whose only xref is a stream with
// /W [1 0 0] (one decoded byte per entry), the given /Size or /Index counts in
// xrefDict, and a zlib body of zeros bytes of zeros. With counts (or, without
// /Index, real rows) over the parser's entry cap it must be rejected, having
// inflated at most one row past the cap.
func xrefBombPDF(xrefDict string, zeros int) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, zeros))
	zw.Close()

	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	for i, o := range []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	} {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	off := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Type /XRef /W [1 0 0] %s /Root 1 0 R /Filter /FlateDecode /Length %d >>\nstream\n", xrefDict, z.Len())
	b.Write(z.Bytes())
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", off)
	return b.Bytes()
}

// xrefSeedBytes are the xref-size seeds: a /W [1 0 0] stream declaring far
// more entries than the cap through /Size, /Index (one range, several ranges,
// and an overflowing start+count). The /Index seeds carry a 1 MiB body of
// zeros; the /Size seed one row more than the 2 Mi-row cap, since a stream
// without /Index is charged the rows it really holds.
func xrefSeedBytes() [][]byte {
	return [][]byte{
		xrefBombPDF("/Size 268435456", 1<<21+1),
		xrefBombPDF("/Size 4 /Index [0 268435456]", 1<<20),
		xrefBombPDF("/Size 4 /Index [0 1048576 1048576 1048576 2097152 1]", 1<<20),
		xrefBombPDF("/Size 4 /Index [9223372036854775807 2]", 1<<20),
	}
}

// TestXrefSeedsRejected pins the seeds' contract beyond "no panic": each is an
// ErrInvalidPDF, not a partial table that opens.
func TestXrefSeedsRejected(t *testing.T) {
	for i, s := range xrefSeedBytes() {
		if _, err := core.OpenBytes(s); !errors.Is(err, core.ErrInvalidPDF) {
			t.Errorf("seed %d: OpenBytes err = %v, want ErrInvalidPDF", i, err)
		}
	}
}

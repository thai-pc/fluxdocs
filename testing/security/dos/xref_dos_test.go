package dos

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// hostileXrefZeros is the decoded size of the hostile xref streams' body:
// 32 MiB of zero bytes, which Flate packs into ~32 KB. Read as /W [1 0 0]
// rows it is 32 Mi free entries (≈ 3.7 GB of map before the cap).
const hostileXrefZeros = 32 << 20

// zlibZeros returns n zero bytes, zlib-compressed.
func zlibZeros(t *testing.T, n int64) []byte {
	t.Helper()
	var b bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&b, zlib.BestCompression)
	if _, err := io.CopyN(zw, zeroReader{}, n); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// hostileXrefPDF builds a one-page document whose objects are listed by a
// valid classic table, followed by an incremental update whose xref stream
// (object 4, /W w, body; /Prev to the table) carries the hostile counts
// in xrefDict. With hybrid set, the stream comes first and the classic
// trailer points at it through /XRefStm, so it is the hybrid section instead.
func hostileXrefPDF(w, xrefDict string, body []byte, hybrid bool) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	off := make([]int, 4)
	for i, o := range []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	} {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	writeClassic := func(extra string) int {
		classic := b.Len()
		b.WriteString("xref\n0 4\n0000000000 65535 f \n")
		for _, o := range off[1:] {
			fmt.Fprintf(&b, "%010d 00000 n \n", o)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", extra, classic)
		return classic
	}
	writeStream := func(extra string) int {
		stm := b.Len()
		fmt.Fprintf(&b, "4 0 obj\n<< /Type /XRef /W %s %s /Root 1 0 R%s /Filter /FlateDecode /Length %d >>\nstream\n", w, xrefDict, extra, len(body))
		b.Write(body)
		b.WriteString("\nendstream\nendobj\n")
		return stm
	}
	if hybrid {
		stm := writeStream("")
		writeClassic(fmt.Sprintf(" /XRefStm %d", stm))
		return b.Bytes()
	}
	classic := writeClassic("")
	stm := writeStream(fmt.Sprintf(" /Prev %d", classic))
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", stm)
	return b.Bytes()
}

// TestDoS_XrefStreamEntriesAreCapped: an xref stream with /W [1 0 0] spends one
// decoded byte per entry, so a file whose /Size or /Index declares hundreds of
// millions of rows used to insert one map entry per decoded byte: 32 Mi
// entries (≈ 3.7 GB) for this ~32 KB file, ≈ 268 M at the 256 MiB decode cap
// for a ~260 KB one. /Index counts are charged against maxXrefEntries before
// the stream is decoded; a stream without /Index is charged the rows it
// holds, and only maxXrefEntries+1 of them are decoded to find out. The open
// fails with ErrInvalidPDF promptly — a resource bound is never retried
// through cross-reference repair — and a hybrid /XRefStm over the cap fails
// the open rather than being silently dropped. An /Index whose start+count
// overflows is damage, not a bound: the table is rebuilt by the object scan
// (docs/design/xref-reconstruction.md §1.2) within the same bounds.
func TestDoS_XrefStreamEntriesAreCapped(t *testing.T) {
	body := zlibZeros(t, hostileXrefZeros)
	// The /Size cases decode one /W [1 0 0] row past the 2 Mi-row cap: 2 MiB,
	// plus buffer growth that depends on the Go version (measured 4.7 MB on
	// Go 1.27 and 10.4 MB on the CI toolchain; the race detector roughly
	// doubles it). The bound only has to separate that from decoding the
	// whole 32 MiB body, which allocates several times 32 MiB. The /Index
	// cases decode nothing.
	var sizeAlloc, indexAlloc uint64 = 24 << 20, 4 << 20
	if raceEnabled {
		sizeAlloc = 48 << 20
	}
	for _, tc := range []struct {
		name     string
		dict     string
		hybrid   bool
		maxAlloc uint64
		repaired bool // damage, not a bound: opens through the object scan
	}{
		{"/Size 2^28", "/Size 268435456", false, sizeAlloc, false},
		{"/Size 2^21+1", "/Size 2097153", false, sizeAlloc, false},
		{"/Index one huge range", "/Size 4 /Index [0 268435456]", false, indexAlloc, false},
		{"/Index ranges summing past the cap", "/Size 4 /Index [0 1048576 1048576 1048576 2097152 1]", false, indexAlloc, false},
		{"/Index start+count overflow", "/Size 4 /Index [9223372036854775807 2]", false, indexAlloc, true},
		{"hybrid /XRefStm /Size 2^28", "/Size 268435456", true, sizeAlloc, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := hostileXrefPDF("[1 0 0]", tc.dict, body, tc.hybrid)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			doc, err := core.OpenBytes(pdf)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			alloc := after.TotalAlloc - before.TotalAlloc

			if tc.repaired {
				if err != nil {
					t.Fatalf("OpenBytes: %v, want a repaired open", err)
				}
				if rep, ok := doc.RepairReport(); !ok || rep.Method != core.RepairObjectScan {
					t.Fatalf("RepairReport = %+v, %v; want an object scan", rep, ok)
				}
			} else if !errors.Is(err, core.ErrInvalidPDF) {
				t.Fatalf("OpenBytes err = %v, want ErrInvalidPDF", err)
			}
			if alloc > tc.maxAlloc {
				t.Errorf("OpenBytes allocated %d KB, want ≤ %d KB (at most one row past the cap decoded)", alloc>>10, tc.maxAlloc>>10)
			}
			if !raceEnabled && elapsed > time.Second {
				t.Errorf("OpenBytes took %v, want < 1 s", elapsed)
			}
			t.Logf("%d-byte file: %v, %d KB allocated; err %v", len(pdf), elapsed, alloc>>10, err)
		})
	}

	// Controls: the same documents with honest counts (four free entries for
	// unused numbers 10–13) open, so the gate exercises the xref stream and
	// the chain rather than failing on the builder.
	for _, hybrid := range []bool{false, true} {
		ok := hostileXrefPDF("[1 0 0]", "/Size 14 /Index [10 4]", zlibZeros(t, 4), hybrid)
		if _, err := core.OpenBytes(ok); err != nil {
			t.Fatalf("control document (hybrid %v): OpenBytes: %v", hybrid, err)
		}
	}
}

// TestDoS_XrefStreamShortSizeOverBomb: the rows a stream can use bound its
// decode, not just the 256 MiB per-stream cap. A /Size 1 xref stream (one row
// of /W [1 0 0]) over 256 MiB of zeros — a ~260 KB file — used to inflate all
// 256 MiB before reading its single row; it now decodes one row plus a few KB
// of slack and opens. The bound is rows × the row length, which /W keeps to
// 24 bytes (TestDoS_XrefStreamWideRows), plus at most one /DecodeParms
// predictor row; the chain's xref streams share one maxDecoded decode budget,
// so those costs cannot add up section by section.
func TestDoS_XrefStreamShortSizeOverBomb(t *testing.T) {
	pdf := hostileXrefPDF("[1 0 0]", "/Size 1", zlibZeros(t, 256<<20), false)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err := core.OpenBytes(pdf)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	// Measured ~310 KB: mostly the parser's copy of the ~255 KB stream body.
	// Inflating the whole body allocated ~570 MiB (0.57 s).
	if alloc > 512<<10 {
		t.Errorf("OpenBytes allocated %d KB, want ≤ 512 KB (one row decoded, not 256 MiB)", alloc>>10)
	}
	t.Logf("%d-byte file: %v, %d KB allocated", len(pdf), elapsed, alloc>>10)
}

// Mirrors of the parser's xref caps (core/parse/xref.go: maxXrefEntries,
// maxXrefRows), which are unexported.
const (
	xrefEntryCap = 1 << 21
	xrefRowCap   = 2 * xrefEntryCap
)

// xrefChainPDF builds a one-page document whose objects 1–3 are listed by a
// classic table, followed by one incremental update per entry of indexes
// (oldest first): an xref stream with /W [1 0 0], that /Index, /Prev to the
// previous section, and rows zero bytes (free entries) as its body.
func xrefChainPDF(t *testing.T, indexes []string, rows int64) []byte {
	body := zlibZeros(t, rows)
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	off := make([]int, 4)
	for i, o := range []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R >>",
	} {
		off[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	prev := b.Len()
	b.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, o := range off[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", prev)
	for i, idx := range indexes {
		stm := b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /W [1 0 0] /Size %d /Index %s /Root 1 0 R /Prev %d /Filter /FlateDecode /Length %d >>\nstream\n",
			4+i, xrefEntryCap, idx, prev, len(body))
		b.Write(body)
		b.WriteString("\nendstream\nendobj\n")
		fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", stm)
		prev = stm
	}
	return b.Bytes()
}

// TestDoS_XrefAtTheCap pins the cost of the largest xref table the parser
// accepts, for a ~4 KB file: one section of maxXrefEntries rows, and a chain
// of four sections whose rows add up to maxXrefRows over maxXrefEntries
// distinct objects (the worst a chain can do). Both open.
//
// Measured (Go 1.27, darwin/amd64; ParseXref alone, peak = process memory):
//
//	                        before (per-section maps,   after (one map,
//	                        maxXrefRows 4× entries)     maxXrefRows 2×)
//	single section, native  3.2 s, 901 MiB alloc, 584 MiB   1.05 s, 452 MiB, 282 MiB
//	single section, wasm    3.6 s, 901 MiB alloc, 566 MiB   1.95 s, 452 MiB, 286 MiB
//	4-section chain, native 9.1 s, 2259 MiB alloc, 753 MiB  1.09 s, 456 MiB, 298 MiB
//	4-section chain, wasm   9.6 s, 2259 MiB alloc, 847 MiB  2.44 s, 456 MiB, 298 MiB
//
// (wasm: GOOS=js GOARCH=wasm under node 24; "before" chain = four full
// rewrites of one maximal table, then at maxXrefRows.)
func TestDoS_XrefAtTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a table of maxXrefEntries objects")
	}
	half := int64(xrefEntryCap/2 - 2)
	lo := fmt.Sprintf("[4 %d]", half)
	hi := fmt.Sprintf("[%d %d]", 4+half, half)
	for _, tc := range []struct {
		name    string
		indexes []string
		rows    int64
	}{
		{"one section of maxXrefEntries rows", []string{fmt.Sprintf("[4 %d]", xrefEntryCap-4)}, xrefEntryCap - 4},
		{"four sections, maxXrefRows rows", []string{lo, hi, lo, hi}, half},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := xrefChainPDF(t, tc.indexes, tc.rows)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			doc, err := core.OpenBytes(pdf)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			alloc := after.TotalAlloc - before.TotalAlloc
			if err != nil || doc.PageCount != 1 {
				t.Fatalf("OpenBytes = %v; want the one-page document at the cap", err)
			}
			// One map of 2 Mi entries allocates ~450 MiB while it grows; the
			// per-section maps and merge allocated 900 MiB (one section) to
			// 2.2 GB (a chain).
			if alloc > 600<<20 {
				t.Errorf("OpenBytes allocated %d MiB, want ≤ 600 MiB (one map, no per-section copies)", alloc>>20)
			}
			if !raceEnabled && elapsed > 6*time.Second {
				t.Errorf("OpenBytes took %v, want < 6 s (measured ~1.1 s)", elapsed)
			}
			t.Logf("%d-byte file: %v, %d MiB allocated", len(pdf), elapsed, alloc>>20)
		})
	}
}

// TestDoS_XrefStreamWideRows: /W [1 0 100000] makes every row 100 KB, so a
// /Size 2^28 stream over 255 MiB of zeros (~250 KB of file) decoded the full
// 256 MiB per section — 572 MiB allocated for one section, 2.29 GiB for four,
// ~36 GiB projected for a 64-section chain — and opened. A /W field wider
// than 8 bytes is now damage: the strict parse fails before the stream is
// decoded, and the table is rebuilt by the object scan, which never decodes
// it (docs/design/xref-reconstruction.md §1.2); a hybrid /XRefStm section
// like it is dropped and the document opens from its classic table.
func TestDoS_XrefStreamWideRows(t *testing.T) {
	body := zlibZeros(t, 255<<20)
	for _, hybrid := range []bool{false, true} {
		t.Run(fmt.Sprintf("hybrid %v", hybrid), func(t *testing.T) {
			pdf := hostileXrefPDF("[1 0 100000]", "/Size 268435456", body, hybrid)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			doc, err := core.OpenBytes(pdf)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			alloc := after.TotalAlloc - before.TotalAlloc
			if err != nil {
				t.Fatalf("OpenBytes: %v; want the damaged section dropped (hybrid) or the table rebuilt", err)
			}
			rep, repaired := doc.RepairReport()
			if hybrid == repaired {
				t.Fatalf("hybrid %v: RepairReport = %+v, %v; want a repair only without the classic table", hybrid, rep, repaired)
			}
			// Mostly the parser's copy of the ~250 KB stream body.
			if alloc > 1<<20 {
				t.Errorf("OpenBytes allocated %d KB, want ≤ 1 MB (nothing inflated)", alloc>>10)
			}
			if !raceEnabled && elapsed > time.Second {
				t.Errorf("OpenBytes took %v, want < 1 s", elapsed)
			}
			t.Logf("%d-byte file: %v, %d KB allocated; err %v", len(pdf), elapsed, alloc>>10, err)
		})
	}
}

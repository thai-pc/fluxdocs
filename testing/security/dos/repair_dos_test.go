package dos

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/internal/reltime"
)

// TestDoS_RepairScanIsBounded: a file whose cross-reference data is unusable
// is rebuilt by scanning its body for "N G obj" headers
// (docs/design/xref-reconstruction.md §3), so a hostile body chooses the
// scan's work. Each case must fail (or open) in time linear in its size and
// within a fixed memory bound:
//
//   - dense headers: 48 MiB of "1 0 obj\n" is 6 Mi header candidates, past
//     maxRepairCandidates (4 Mi); the open fails naming the cap, having
//     indexed at most 4 Mi of them (16 bytes each). Its time is measured
//     below the cap, 2 Mi headers against 512 Ki: past it, the scan stops at
//     4 Mi whatever the size, so the ratio would measure nothing;
//   - streams without endstream: 1 MiB of "1 0 obj<<>>stream\n", each a
//     stream with no endstream after it; the endstream lookup is a pointer
//     that only moves forward, so this is not one scan of the rest of the
//     file per stream;
//   - unterminated dictionaries: headers each followed by 300 open "<<";
//     each parse stops at the nesting bound inside its own span;
//   - shared indirect /Length: 400 streams whose /Length is 5 0 R, an object
//     holding a 4 MB unterminated string (rule (b)); its tokens are read on
//     its span once, not once per stream;
//   - unterminated strings: an image dictionary followed by 4 Mi '(', and
//     one followed by 1 Mi "(/Type"; the name search behind the fail-closed
//     encryption check scans an unterminated string's bytes plainly once,
//     instead of restarting at every '(';
//   - unterminated strings after every header: 1 Mi headers (66 MB), each
//     with a few names and an unterminated '(' that ends at the next header
//     (the shape of the lin probe, with /Filter /Fl so that no span looks
//     like a lost encryption dictionary and the fail-closed check walks them
//     all); the scan's lexer reports each failure with a static error and
//     its dictionaries cost no map until they close (about 2 s CPU on Go
//     1.23 and newer);
//   - headers up to the caps: 1 Mi distinct objects (maxRepairObjects) and
//     3 Mi duplicates (36 MB), 998 headers short of maxRepairCandidates,
//     every header failing to parse; it opens, and a failed header costs no
//     formatted error. Its time is measured below the caps (see below).
//
// The memory bounds are generous (CI runs an older Go, whose allocation
// figures differ, and the hosts vary): they only separate the index from a
// per-candidate allocation. maxHeap bounds the growth of the heap the
// runtime holds (HeapSys) across the open, which a per-candidate allocation
// or a doubling index would exceed. Time is bounded relative to the same
// case at a quarter of its size, measured in the same run (reltime): at most
// reltime.LinearBound× (linear: 4×, quadratic: 16×), never by an absolute
// time. The two largest cases that open or fail past the scan are timed at a
// quarter of their size against a sixteenth (16 MB and 4 MB for the 66 MB
// one, which took 5.5 s per run at full size), and dense headers at a third
// against a twelfth, under the cap it stops at; each body is built once.
func TestDoS_RepairScanIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("slow hostile-document gate")
	}
	unterminated := func(n int) []byte {
		var b bytes.Buffer
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%d 0 obj<</Filter/Fl/Subtype/Y/Type/Type/Type(((((((((( ]]\n", i+10)
		}
		return b.Bytes()
	}
	// 400 streams share a 4 MB string: n streams, n × 10,486 bytes.
	shared := func(n int) []byte {
		var b bytes.Buffer
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%d 0 obj<</Length 5 0 R>>stream\nX\nendstream endobj\n", i+10)
		}
		b.WriteString("5 0 obj\n(")
		b.Write(bytes.Repeat([]byte("a"), n*10_486))
		return b.Bytes()
	}
	// n distinct objects, 3n duplicates, then a catalog.
	atCaps := func(n int) []byte {
		var b bytes.Buffer
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d 0 obj\n", i)
		}
		b.Write(bytes.Repeat([]byte("1 0 obj\n"), 3*n-1000))
		b.WriteString("1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj 2 0 obj<</Type/Pages/Kids[]/Count 0>>endobj\n")
		return b.Bytes()
	}
	repeat := func(unit string) func(n int) []byte {
		return func(n int) []byte { return bytes.Repeat([]byte(unit), n) }
	}
	image := func(unit string) func(n int) []byte {
		return func(n int) []byte {
			return append([]byte("1 0 obj<</Subtype/Image/Filter/DCTDecode "), bytes.Repeat([]byte(unit), n)...)
		}
	}

	for _, tc := range []struct {
		name     string
		body     func(n int) []byte // the case's body at n units
		n        int                // units of the gate's body
		wantErr  string             // a substring of the open error; "" for a file that opens
		maxAlloc uint64
		maxHeap  uint64
		linN     int // units of the larger body the linearity check times; 0 is n
	}{
		{"dense headers", repeat("1 0 obj\n"), 6 << 20, "maxRepairCandidates", 768 << 20, 256 << 20, 2 << 20},
		{"streams without endstream", repeat("1 0 obj<<>>stream\n"), (1 << 20) / 18, "no document catalog", 256 << 20, 256 << 20, 0},
		{"unterminated dictionaries", repeat("1 0 obj\n" + strings.Repeat("<<", 300) + "\n"), (1 << 20) / 609, "no document catalog", 256 << 20, 256 << 20, 0},
		{"shared indirect /Length", shared, 400, "no document catalog", 256 << 20, 256 << 20, 0},
		{"unterminated strings, 4 Mi '('", image("("), 4 << 20, "no document catalog", 256 << 20, 256 << 20, 0},
		{"unterminated strings, 1 Mi \"(/Type\"", image("(/Type"), 1 << 20, "no document catalog", 256 << 20, 256 << 20, 0},
		{"unterminated strings after every header", unterminated, 1 << 20, "no document catalog", 768 << 20, 384 << 20, 1 << 18},
		{"headers up to the caps", atCaps, 1 << 20, "", 640 << 20, 384 << 20, 1 << 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := append([]byte("%PDF-1.7\n"), tc.body(tc.n)...)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			doc, err := core.OpenBytes(pdf)
			runtime.ReadMemStats(&after)
			alloc := after.TotalAlloc - before.TotalAlloc

			heap := int64(after.HeapSys) - int64(before.HeapSys)

			var ie *core.InternalError
			if errors.As(err, &ie) {
				t.Fatalf("recovered panic: %v", err)
			}
			if tc.wantErr == "" {
				if err != nil || doc == nil {
					t.Fatalf("OpenBytes = %v, %v; want the file to open", doc, err)
				}
			} else if doc != nil || !errors.Is(err, core.ErrInvalidPDF) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("OpenBytes = %v, %v; want ErrInvalidPDF naming %q", doc, err, tc.wantErr)
			}
			if alloc > tc.maxAlloc {
				t.Errorf("OpenBytes allocated %d MB, want ≤ %d MB", alloc>>20, tc.maxAlloc>>20)
			}
			if heap > int64(tc.maxHeap) {
				t.Errorf("OpenBytes grew the heap by %d MB, want ≤ %d MB", heap>>20, tc.maxHeap>>20)
			}
			t.Logf("%d-byte file: %d MB allocated, heap %+d MB", len(pdf), alloc>>20, heap>>20)

			// Linear, not quadratic: the body at n against n/4, in the same
			// run (reltime): ≈ 4 when linear, 16 when quadratic.
			reltime.Skip(t)
			linN := tc.n
			if tc.linN != 0 {
				linN = tc.linN
			}
			bodies := map[int][]byte{tc.n: pdf}
			open := func(n int) func() {
				b, ok := bodies[n]
				if !ok {
					b = append([]byte("%PDF-1.7\n"), tc.body(n)...)
					bodies[n] = b
				}
				return func() { core.OpenBytes(b) }
			}
			reltime.Check(t, "the body against a quarter of it", reltime.LinearBounded(reltime.CPU, 4, open, linN/4, reltime.LinearBound), reltime.LinearBound)
		})
	}
}

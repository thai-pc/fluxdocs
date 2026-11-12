package dos

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// TestDoS_RepairScanIsBounded: a file whose cross-reference data is unusable
// is rebuilt by scanning its body for "N G obj" headers
// (docs/design/xref-reconstruction.md §3), so a hostile body chooses the
// scan's work. Each case must fail (or open) in time linear in its size and
// within a fixed memory bound:
//
//   - dense headers: 48 MiB of "1 0 obj\n" is 6 Mi header candidates, past
//     maxRepairCandidates (4 Mi); the open fails naming the cap, having
//     indexed at most 4 Mi of them (16 bytes each);
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
//   - headers at the caps: 1 Mi distinct objects and 3 Mi duplicates (36 MB),
//     every header failing to parse; it opens, and a failed header costs no
//     formatted error.
//
// The bounds are generous (CI runs an older Go, whose allocation figures
// differ, and the hosts vary): they only separate linear work from
// quadratic work, and the index from a per-candidate allocation. maxHeap
// bounds the growth of the heap the runtime holds (HeapSys) across the open,
// which a per-candidate allocation or a doubling index would exceed.
func TestDoS_RepairScanIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("slow hostile-document gate")
	}
	scale := time.Duration(1)
	if raceEnabled {
		scale = 10
	}
	var shared, atCaps, unterminated bytes.Buffer
	for i := 0; i < 1<<20; i++ {
		fmt.Fprintf(&unterminated, "%d 0 obj<</Filter/Fl/Subtype/Y/Type/Type/Type(((((((((( ]]\n", i+10)
	}
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&shared, "%d 0 obj<</Length 5 0 R>>stream\nX\nendstream endobj\n", i+10)
	}
	shared.WriteString("5 0 obj\n(")
	shared.Write(bytes.Repeat([]byte("a"), 4<<20))
	for i := 1; i <= 1<<20; i++ {
		fmt.Fprintf(&atCaps, "%d 0 obj\n", i)
	}
	atCaps.Write(bytes.Repeat([]byte("1 0 obj\n"), 3<<20-1000))
	atCaps.WriteString("1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj 2 0 obj<</Type/Pages/Kids[]/Count 0>>endobj\n")

	for _, tc := range []struct {
		name     string
		body     []byte
		wantErr  string // a substring of the open error; "" for a file that opens
		maxCPU   time.Duration
		maxAlloc uint64
		maxHeap  uint64
	}{
		{"dense headers", bytes.Repeat([]byte("1 0 obj\n"), 6<<20), "maxRepairCandidates", 10 * time.Second, 768 << 20, 256 << 20},
		{"streams without endstream", bytes.Repeat([]byte("1 0 obj<<>>stream\n"), (1<<20)/18), "no document catalog", 5 * time.Second, 256 << 20, 256 << 20},
		{"unterminated dictionaries", bytes.Repeat([]byte("1 0 obj\n"+strings.Repeat("<<", 300)+"\n"), (1<<20)/609), "no document catalog", 5 * time.Second, 256 << 20, 256 << 20},
		{"shared indirect /Length", shared.Bytes(), "no document catalog", 5 * time.Second, 256 << 20, 256 << 20},
		{"unterminated strings, 4 Mi '('", append([]byte("1 0 obj<</Subtype/Image/Filter/DCTDecode "), bytes.Repeat([]byte("("), 4<<20)...),
			"no document catalog", 5 * time.Second, 256 << 20, 256 << 20},
		{"unterminated strings, 1 Mi \"(/Type\"", append([]byte("1 0 obj<</Subtype/Image/Filter/DCTDecode "), bytes.Repeat([]byte("(/Type"), 1<<20)...),
			"no document catalog", 5 * time.Second, 256 << 20, 256 << 20},
		{"unterminated strings after every header", unterminated.Bytes(), "no document catalog", 5 * time.Second, 768 << 20, 384 << 20},
		{"headers at the caps", atCaps.Bytes(), "", 30 * time.Second, 640 << 20, 384 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdf := append([]byte("%PDF-1.7\n"), tc.body...)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := cpuTime()
			doc, err := core.OpenBytes(pdf)
			elapsed := cpuTime() - start
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
			if elapsed > tc.maxCPU*scale {
				t.Errorf("OpenBytes took %v CPU, want ≤ %v", elapsed, tc.maxCPU*scale)
			}
			if alloc > tc.maxAlloc {
				t.Errorf("OpenBytes allocated %d MB, want ≤ %d MB", alloc>>20, tc.maxAlloc>>20)
			}
			if heap > int64(tc.maxHeap) {
				t.Errorf("OpenBytes grew the heap by %d MB, want ≤ %d MB", heap>>20, tc.maxHeap>>20)
			}
			t.Logf("%d-byte file: %v CPU, %d MB allocated, heap %+d MB", len(pdf), elapsed, alloc>>20, heap>>20)
		})
	}
}

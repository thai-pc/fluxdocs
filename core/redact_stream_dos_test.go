package core

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
)

// DoS gates of matching across operators (docs/design/redaction-cross-
// operator.md §10.6). Every bound is relative to a reference measured in the
// same run — the same call with step 2.6 turned off by its test hook
// (redact.SetStreamMatchingForTest), which reads pages without extraction's
// reading and matches each run alone — never an absolute time: CI runs on 2
// cores. They live in package core because the hook is internal to the
// engine.

// skipUnlessStreamTimed skips a timing gate under -short, -race or without
// process CPU time.
func skipUnlessStreamTimed(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled || cpuTime() == 0 {
		t.Skip("CPU-time ratio gate: needs CPU time, no -race, no -short")
	}
}

// cost is the median CPU time and allocation of f over runs runs (one run
// when it takes over 2 s of CPU: large jobs are stable, and the gates must
// stay affordable); f gets a fresh document each time (open is not
// measured).
type cost struct {
	cpu   time.Duration
	alloc uint64
	err   error
}

func measure(t *testing.T, runs int, open func() *Document, f func(d *Document) error) cost {
	t.Helper()
	var cpus []time.Duration
	var allocs []uint64
	var last error
	for i := 0; i < runs; i++ {
		d := open()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c0 := cpuTime()
		last = f(d)
		cpus = append(cpus, cpuTime()-c0)
		runtime.ReadMemStats(&after)
		allocs = append(allocs, after.TotalAlloc-before.TotalAlloc)
		d.Close()
		if cpus[0] > 2*time.Second {
			break
		}
	}
	runs = len(cpus)
	sort.Slice(cpus, func(i, j int) bool { return cpus[i] < cpus[j] })
	sort.Slice(allocs, func(i, j int) bool { return allocs[i] < allocs[j] })
	return cost{cpu: cpus[runs/2], alloc: allocs[runs/2], err: last}
}

// withStreams runs f with step 2.6 on or off.
func withStreams(on bool, f func() cost) cost {
	defer redact.SetStreamMatchingForTest(on)()
	return f()
}

// ratios reports on/off for CPU and allocation.
func ratios(on, off cost) (float64, float64) {
	return float64(on.cpu) / float64(max(off.cpu, time.Microsecond)), float64(on.alloc) / float64(max(off.alloc, 1))
}

// perCharDoc is one page showing n one-character strings, one Tj each, on one
// line (the C27 shape).
func perCharDoc(t *testing.T, n int, char string) func() *Document {
	content := "BT /F1 12 Tf 10 700 Td " + strings.Repeat("("+char+")Tj ", n) + "ET"
	pdf := extPDF([]extPage{{content: content}}, nil, "")
	return func() *Document {
		d, err := OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

// TestDoS_RedactCrossOperatorFlood: on a per-character page near the
// operator budget, RedactText with a literal pattern and with `.` (every
// run a match), then RedactAndFlatten, cost at most 1.5× the same calls
// without step 2.6, in CPU time and allocation. Past the budget the page is
// refused with bounded work whatever its size
// (testing/security/dos TestDoS_RedactOperatorFloodIsBounded runs with the
// step on).
func TestDoS_RedactCrossOperatorFlood(t *testing.T) {
	skipUnlessStreamTimed(t)
	const n = 600_000 // about 7 operator visits each: near the 8 Mi budget
	open := perCharDoc(t, n, "x")
	for _, pattern := range []string{`SECRET-\d+`, `.`} {
		t.Run(pattern, func(t *testing.T) {
			run := func(d *Document) error {
				if _, err := d.RedactText(0, pattern); err != nil {
					return err
				}
				_, err := d.RedactAndFlatten()
				return err
			}
			off := withStreams(false, func() cost { return measure(t, 3, open, run) })
			on := withStreams(true, func() cost { return measure(t, 3, open, run) })
			if off.err != nil || on.err != nil {
				t.Fatalf("off: %v, on: %v; want both to succeed (the page is under the budget)", off.err, on.err)
			}
			rc, ra := ratios(on, off)
			t.Logf("%d runs: CPU off %v on %v (%.2f×), alloc off %d MiB on %d MiB (%.2f×)",
				n, off.cpu, on.cpu, rc, off.alloc>>20, on.alloc>>20, ra)
			if rc > 1.5 || ra > 1.5 {
				t.Errorf("matching across operators costs %.2f× CPU and %.2f× allocation, want ≤ 1.5×", rc, ra)
			}
		})
	}
}

// TestDoS_RedactStreamMatchCap: a page with more than 4 Mi digits and the
// pattern `\d`. The operator budget, which charges every reading, refuses it
// before matching (the match cap itself is a backstop, tested in the
// engine): budget_exceeded, with allocation within 1.5× of the same call
// without step 2.6.
func TestDoS_RedactStreamMatchCap(t *testing.T) {
	skipUnlessStreamTimed(t)
	pdf := extPDF([]extPage{{content: "BT /F1 12 Tf 10 700 Td (" + strings.Repeat("7", 4<<20+1) + ") Tj ET"}}, nil, "")
	open := func() *Document {
		d, err := OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	run := func(d *Document) error { _, err := d.RedactText(0, `\d`); return err }
	off := withStreams(false, func() cost { return measure(t, 3, open, run) })
	on := withStreams(true, func() cost { return measure(t, 3, open, run) })
	for _, c := range []cost{off, on} {
		var re *RedactionError
		if !errors.As(c.err, &re) || re.Reason != RedactionBudgetExceeded {
			t.Fatalf("RedactText = %v, want a budget_exceeded refusal", c.err)
		}
	}
	rc, ra := ratios(on, off)
	t.Logf("CPU off %v on %v (%.2f×), alloc off %d MiB on %d MiB (%.2f×)", off.cpu, on.cpu, rc, off.alloc>>20, on.alloc>>20, ra)
	if ra > 1.5 {
		t.Errorf("allocation %.2f× the reference, want ≤ 1.5×", ra)
	}
}

// chainDoc is a page of filler one-character runs, then k runs "a" and k
// runs "b": with the pattern ab, each closure round removes one more pair.
func chainDoc(t *testing.T, filler, k int) func() *Document {
	content := "BT /F1 12 Tf 10 700 Td " + strings.Repeat("(x)Tj ", filler) + strings.Repeat("(a)Tj ", k) +
		strings.Repeat("(b)Tj ", k) + "ET"
	pdf := extPDF([]extPage{{content: content}}, nil, "")
	return func() *Document {
		d, err := OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

// TestDoS_RedactClosureRounds: the crafted chain is refused after 4 closure
// rounds (budget_exceeded), in at most 5× the time of a page that needs one
// round.
func TestDoS_RedactClosureRounds(t *testing.T) {
	skipUnlessStreamTimed(t)
	const filler = 300_000
	run := func(d *Document) error { _, err := d.RedactText(0, `ab`); return err }
	one := measure(t, 3, chainDoc(t, filler, 2), run)
	if one.err != nil {
		t.Fatalf("one round: %v", one.err)
	}
	many := measure(t, 3, chainDoc(t, filler, 6), run)
	var re *RedactionError
	if !errors.As(many.err, &re) || re.Reason != RedactionBudgetExceeded || !strings.Contains(re.Detail, "text matching") {
		t.Fatalf("RedactText = %v, want budget_exceeded (text matching)", many.err)
	}
	ratio := float64(many.cpu) / float64(max(one.cpu, time.Microsecond))
	t.Logf("one round %v, refused after 4 rounds %v (%.2f×)", one.cpu, many.cpu, ratio)
	if ratio > 5 {
		t.Errorf("refusal took %.2f× one round, want ≤ 5×", ratio)
	}
}

// slotDoc is fontCandidatesDoc of testing/security/dos at the font-set cap:
// seven ExtGStates only the /Pages resources define, each with its own
// /Differences font, then m strings: every string is read by 8 candidates.
func slotDoc(t *testing.T, m int) func() *Document {
	var eg, c strings.Builder
	eg.WriteString("<< /ExtGState << ")
	c.WriteString("BT 10 700 Td ")
	for i := 0; i < 7; i++ {
		fmt.Fprintf(&eg, "/G%d << /Font [<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica "+
			"/Encoding << /Differences [120 /uni%04X] >> >> 12] >> ", i, 0x4E00+i)
		fmt.Fprintf(&c, "/G%d gs ", i)
	}
	eg.WriteString(">> >>")
	c.WriteString(strings.Repeat("(x) Tj ", m))
	c.WriteString("ET")
	content := c.String()
	pdf := extPDF([]extPage{{content: content, res: "<< /Font << >> >>"}}, map[int]string{}, "")
	// The ExtGStates go on the /Pages node (a fallback scope).
	pdf = []byte(strings.Replace(string(pdf), "/MediaBox [0 0 400 800]", "/MediaBox [0 0 400 800] /Resources "+eg.String(), 1))
	return func() *Document {
		d, err := OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
}

// TestDoS_RedactSlotStreams: with 8 font candidates per string on a large
// page, the model holds at most numSlots+1 streams, its size stays within 3×
// the model without step 2.6 (§7.1: the streams hold the readings once
// more, plus their index maps), and RedactText within 1.5× its CPU time.
func TestDoS_RedactSlotStreams(t *testing.T) {
	skipUnlessStreamTimed(t)
	open := slotDoc(t, 100_000)
	var size [2]int64
	var streams int
	run := func(i int) func(d *Document) error {
		return func(d *Document) error {
			page, err := d.redactionPage(0)
			if err != nil {
				return err
			}
			size[i], streams = page.Size(), page.StreamCount()
			_, err = d.RedactText(0, `SECRET-\d+`)
			return err
		}
	}
	off := withStreams(false, func() cost { return measure(t, 3, open, run(0)) })
	on := withStreams(true, func() cost { return measure(t, 3, open, run(1)) })
	if off.err != nil || on.err != nil {
		t.Fatalf("off: %v, on: %v", off.err, on.err)
	}
	rc, _ := ratios(on, off)
	t.Logf("%d streams; model size off %d MiB on %d MiB; CPU off %v on %v (%.2f×)", streams, size[0]>>20, size[1]>>20, off.cpu, on.cpu, rc)
	if streams > 3*8+2 {
		t.Errorf("%d streams, want at most %d", streams, 3*8+2)
	}
	if float64(size[1]) > 3*float64(size[0]) {
		t.Errorf("model size %d against %d without streams, want ≤ 3×", size[1], size[0])
	}
	if rc > 1.5 {
		t.Errorf("RedactText %.2f× the reference CPU, want ≤ 1.5×", rc)
	}
}

// measurePair is measure for an on/off pair: runs runs of each, alternated
// (off, on, off, …) so that load on the machine falls on both alike, with
// no early stop. It returns the medians, off then on.
func measurePair(t *testing.T, runs int, open func() *Document, f func(d *Document) error) (off, on cost) {
	t.Helper()
	var cpus [2][]time.Duration
	var allocs [2][]uint64
	var last [2]error
	for i := 0; i < 2*runs; i++ {
		k := i % 2 // 0: off, 1: on
		// The hook is set before open, which may read pages (RedactText).
		restore := redact.SetStreamMatchingForTest(k == 1)
		d := open()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c0 := cpuTime()
		last[k] = f(d)
		cpus[k] = append(cpus[k], cpuTime()-c0)
		runtime.ReadMemStats(&after)
		allocs[k] = append(allocs[k], after.TotalAlloc-before.TotalAlloc)
		restore()
		d.Close()
	}
	var out [2]cost
	for k := range out {
		sort.Slice(cpus[k], func(i, j int) bool { return cpus[k][i] < cpus[k][j] })
		sort.Slice(allocs[k], func(i, j int) bool { return allocs[k][i] < allocs[k][j] })
		out[k] = cost{cpu: cpus[k][runs/2], alloc: allocs[k][runs/2], err: last[k]}
	}
	return out[0], out[1]
}

// TestDoS_PrepassStreams: 16,000 per-character pages searched by the
// pre-pass and verification for propagating needles cost at most 1.5× the
// per-run search (median of 5 alternated runs of each, each over 1 s of CPU
// on a 12-core development machine).
func TestDoS_PrepassStreams(t *testing.T) {
	skipUnlessStreamTimed(t)
	const n = 16_000
	pages := []extPage{{content: showAt("(SECRET-1234)", 700)}}
	var line strings.Builder
	line.WriteString("BT /F1 12 Tf 10 700 Td ")
	for _, c := range "public text shown one character at a time, many times over " {
		fmt.Fprintf(&line, "(%c)Tj ", c)
	}
	line.WriteString("ET")
	for i := 0; i < n; i++ {
		pages = append(pages, extPage{content: line.String()})
	}
	pdf := extPDF(pages, nil, "")
	open := func() *Document {
		d, err := OpenBytes(pdf)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.RedactText(0, `SECRET-\d+`); err != nil {
			t.Fatal(err)
		}
		return d
	}
	run := func(d *Document) error { _, err := d.RedactAndFlatten(); return err }
	off, on := measurePair(t, 5, open, run)
	if off.err != nil || on.err != nil {
		t.Fatalf("off: %v, on: %v", off.err, on.err)
	}
	rc, ra := ratios(on, off)
	t.Logf("%d pages: CPU off %v on %v (%.2f×), alloc off %d MiB on %d MiB (%.2f×)", n+1, off.cpu, on.cpu, rc, off.alloc>>20, on.alloc>>20, ra)
	if rc > 1.5 {
		t.Errorf("pre-pass and verification %.2f× the per-run reference, want ≤ 1.5×", rc)
	}
}

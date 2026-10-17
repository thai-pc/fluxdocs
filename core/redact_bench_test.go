package core

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Benchmarks of the redaction pre-pass and verification (docs/design/
// redaction-hardening.md §7, step 1.14): each case runs RedactAndFlatten as
// shipped ("full") and without automatic extension and verification
// ("base", test hooks), so the overhead is (full − base) / base. The cost
// criterion and the measured scaling are in the design note §11 (C84).
//
//	go test ./core -run '^$' -bench BenchmarkRedactVerify -benchtime 5x

// benchTextDoc is an n-page document of 40 lines of text per page; each page
// also shows a header line common to every page.
func benchTextDoc(n int) []byte {
	pages := make([]extPage, n)
	for i := range pages {
		var c strings.Builder
		c.WriteString("BT /F1 11 Tf 50 760 Td (ACME Corp. quarterly report - confidential) Tj ET\n")
		c.WriteString("BT /F1 11 Tf 50 740 Td 13 TL ")
		for line := 0; line < 40; line++ {
			fmt.Fprintf(&c, "(The quick brown fox jumps over the lazy dog, line %d of page %d.) Tj T* ", line, i)
		}
		if i%5 == 0 {
			c.WriteString("(Contact: jane.doe@example.com, SSN 123-45-6789) Tj T* ")
		}
		c.WriteString("ET")
		pages[i] = extPage{content: c.String()}
	}
	return extPDF(pages, nil, "")
}

// benchCases are the benchmark set.
var benchCases = []struct {
	name  string
	pages int
	mark  func(d *Document)
}{
	// A region over body text: its lines occur nowhere else (no extension).
	{"area-body-page0/1p", 1, redactArea(0, Rect{X: 0, Y: 300, Width: 612, Height: 200})},
	{"area-body-page0/20p", 20, redactArea(0, Rect{X: 0, Y: 300, Width: 612, Height: 200})},
	// A region over the header every page shows: extends to every page.
	{"area-header-page0/20p", 20, redactArea(0, Rect{X: 0, Y: 750, Width: 612, Height: 30})},
	{"text-page0/20p", 20, redactText(0, `line \d+ of page 0`)},
	{"area-full-every-page/20p", 20, func(d *Document) {
		for p := 0; p < d.PageCount; p++ {
			_ = d.RedactArea(p, Rect{X: 0, Y: 0, Width: 612, Height: 792})
		}
	}},
	{"autoredact-pii/20p", 20, func(d *Document) {
		if _, err := d.AutoRedactPII(); err != nil {
			panic(err)
		}
	}},
	{"area-body-page0/100p", 100, redactArea(0, Rect{X: 0, Y: 300, Width: 612, Height: 200})},
	// RedactText on every page, matching on page 0 only: marking has read
	// every page, and the pre-pass reuses those models (redact_cache.go).
	{"text-every-page-match-page0/100p", 100, func(d *Document) {
		for p := 0; p < d.PageCount; p++ {
			if _, err := d.RedactText(p, `line 3 of page 0\.`); err != nil {
				panic(err)
			}
		}
	}},
	{"area-header-page0/100p", 100, redactArea(0, Rect{X: 0, Y: 750, Width: 612, Height: 30})},
}

func BenchmarkRedactVerify(b *testing.B) {
	for _, bc := range benchCases {
		pdf := benchTextDoc(bc.pages)
		for _, mode := range []string{"base", "full"} {
			b.Run(bc.name+"/"+mode, func(b *testing.B) {
				redactHooks.noPrepass, redactHooks.noVerify = mode == "base", mode == "base"
				defer func() { redactHooks.noPrepass, redactHooks.noVerify = false, false }()
				var cpu time.Duration
				for i := 0; i < b.N; i++ {
					d, err := OpenBytes(pdf)
					if err != nil {
						b.Fatal(err)
					}
					bc.mark(d)
					c0 := cpuTime()
					if _, err := d.RedactAndFlatten(); err != nil {
						b.Fatal(err)
					}
					cpu += cpuTime() - c0
				}
				// CPU time of RedactAndFlatten alone, all threads (GC
				// included): steadier than wall time on a loaded machine.
				b.ReportMetric(float64(cpu.Nanoseconds())/float64(b.N), "cpu-ns/op")
			})
		}
	}
}

// TestRedactVerifyCorpusCost measures the overhead on a real corpus: for
// each PDF of the list FLUXDOCS_BENCH_CORPUS names (one path per line), a
// centred half-page region on each of its first 3 pages, flattened with
// and without pre-pass and verification. It logs the total times and the
// ratio; it fails nothing (the machine's load decides wall times).
func TestRedactVerifyCorpusCost(t *testing.T) {
	list := os.Getenv("FLUXDOCS_BENCH_CORPUS")
	if list == "" {
		t.Skip("FLUXDOCS_BENCH_CORPUS not set")
	}
	f, err := os.Open(list)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var paths []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			paths = append(paths, p)
		}
	}
	ops := map[string]func(d *Document){
		"area-half": func(d *Document) {
			for p := 0; p < min(d.PageCount, 3); p++ {
				pg := d.Pages[p]
				_ = d.RedactArea(p, Rect{X: pg.Width / 4, Y: pg.Height / 4, Width: pg.Width / 2, Height: pg.Height / 2})
			}
		},
		"area-full": func(d *Document) {
			for p := 0; p < min(d.PageCount, 3); p++ {
				_ = d.RedactArea(p, Rect{X: -1e6, Y: -1e6, Width: 2e6, Height: 2e6})
			}
		},
	}
	for name, op := range ops {
		var total, cpuTotal [2]time.Duration
		n := 0
		var worst []string
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var dur [2]time.Duration
			ok := true
			for m := 0; m < 2; m++ {
				redactHooks.noPrepass, redactHooks.noVerify = m == 0, m == 0
				d, err := OpenBytes(data)
				if err != nil {
					ok = false
					break
				}
				op(d)
				start, c0 := time.Now(), cpuTime()
				_, err = d.RedactAndFlatten()
				dur[m] = time.Since(start)
				cpuTotal[m] += cpuTime() - c0
				if err != nil {
					ok = false
				}
			}
			redactHooks.noPrepass, redactHooks.noVerify = false, false
			if !ok {
				continue
			}
			n++
			total[0] += dur[0]
			total[1] += dur[1]
			if dur[0] > 50*time.Millisecond && dur[1] > 2*dur[0] {
				worst = append(worst, fmt.Sprintf("%s base %v full %v", p, dur[0], dur[1]))
			}
		}
		t.Logf("%s: %d files, wall base %v full %v (overhead %.0f%%), CPU base %v full %v (overhead %.0f%%)", name, n,
			total[0], total[1], 100*float64(total[1]-total[0])/float64(total[0]),
			cpuTotal[0], cpuTotal[1], 100*float64(cpuTotal[1]-cpuTotal[0])/float64(cpuTotal[0]))
		for _, w := range worst {
			t.Logf("  %s", w)
		}
	}
}

// TestRedactVerifyCostGuard guards the cost of automatic extension and
// verification against regressions (an accidental quadratic, a pass that
// stops skipping work): on 20 pages with a region on page 0 and nothing to
// extend to, RedactAndFlatten with them must stay within a generous multiple
// of RedactAndFlatten without them, in process CPU time (median of 5). The
// ratio measured when this guard was added was about 3.
func TestRedactVerifyCostGuard(t *testing.T) {
	if testing.Short() || raceEnabled || cpuTime() == 0 {
		t.Skip("CPU-time ratio check: needs CPU time, no -race, no -short")
	}
	pdf := benchTextDoc(20)
	mark := redactArea(0, Rect{X: 0, Y: 300, Width: 612, Height: 200})
	measure := func(base bool) time.Duration {
		redactHooks.noPrepass, redactHooks.noVerify = base, base
		defer func() { redactHooks.noPrepass, redactHooks.noVerify = false, false }()
		var runs []time.Duration
		for i := 0; i < 5; i++ {
			d, err := OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			mark(d)
			c0 := cpuTime()
			if _, err := d.RedactAndFlatten(); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, cpuTime()-c0)
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
		return runs[2]
	}
	base, full := measure(true), measure(false)
	ratio := float64(full) / float64(max(base, time.Microsecond))
	t.Logf("CPU base %v, full %v, ratio %.1f", base, full, ratio)
	if ratio > 15 {
		t.Errorf("pre-pass and verification cost %.1f× RedactAndFlatten without them, want ≤ 15× (was about 3×)", ratio)
	}
}

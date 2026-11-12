package corpus

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// TestRepairCensus is the Phase 0 object-count census of
// docs/design/xref-reconstruction.md (§4, §9, §11 Q7): over the extra corpus
// (FLUXDOCS_EXTRA_CORPUS) it measures what the object scan's bounds must
// admit — the file size, the cross-reference entries, and the scan's header
// candidates, distinct objects and duplicates — and logs the maxima and
// percentiles. It also runs the scan on every file that opens strictly and
// compares the page count it gives with the strict one: the scan is only
// used when the strict open fails, but agreeing on intact files is evidence
// it reads the body correctly. A scan that fails because the file keeps
// objects in object streams (not expanded until Phase 3) is counted apart.
// It logs counts only, never document text.
func TestRepairCensus(t *testing.T) {
	paths := extraCorpus(t)
	type row struct {
		size, xref, cands, objs, dups int
		strictPages, scanPages        int // -1: that open failed
		objStm, otherFail             bool
		scanTime                      time.Duration
	}
	rows := make([]row, len(paths))
	parallel(len(paths), func(i int) {
		r := row{strictPages: -1, scanPages: -1}
		data, err := os.ReadFile(paths[i])
		if err != nil {
			r.otherFail = true
			rows[i] = r
			return
		}
		r.size = len(data)
		if x, err := parse.ParseXref(data); err == nil {
			r.xref = len(x.Entries)
		}
		if sr, err := parse.NewResolver(data); err == nil && sr.EnableDecryption(nil) == nil {
			if pages, err := sr.Pages(); err == nil {
				r.strictPages = len(pages)
			}
		}
		start := time.Now()
		rr, err := parse.NewRepairedResolver(data)
		r.scanTime = time.Since(start)
		switch {
		case err != nil && strings.Contains(err.Error(), "object stream"):
			r.objStm = true
		case err != nil:
			r.otherFail = true
		default:
			info, _ := rr.Repair()
			r.cands, r.objs, r.dups = info.Candidates, info.Objects, info.Duplicates
			if rr.EnableDecryption(nil) == nil {
				if pages, err := rr.Pages(); err == nil {
					r.scanPages = len(pages)
				}
			}
		}
		rows[i] = r
	})

	stat := func(name string, get func(row) int) string {
		var v []int
		for _, r := range rows {
			v = append(v, get(r))
		}
		sort.Ints(v)
		at := func(q float64) int { return v[min(len(v)-1, int(q*float64(len(v))))] }
		return fmt.Sprintf("%-11s p50 %9d  p99 %9d  max %9d", name, at(0.5), at(0.99), v[len(v)-1])
	}
	var objStm, other, strictOK, agree, disagree, scanFail int
	var slowest time.Duration
	for i, r := range rows {
		slowest = max(slowest, r.scanTime)
		switch {
		case r.objStm:
			objStm++
		case r.otherFail:
			other++
		}
		if r.strictPages < 0 {
			continue
		}
		strictOK++
		switch {
		case r.objStm:
		case r.scanPages < 0:
			scanFail++
			t.Logf("scan fails where the strict open works: %s", paths[i])
		case r.scanPages == r.strictPages:
			agree++
		default:
			disagree++
			t.Logf("scan gives %d pages, strict %d: %s", r.scanPages, r.strictPages, paths[i])
		}
	}
	t.Logf("repair census over %d files:\n  %s\n  %s\n  %s\n  %s\n  %s\n"+
		"  scan: %d need object streams (Phase 3), %d other failures; slowest scan %v\n"+
		"  of %d strict opens, the scan (where it applies) agrees on the page count for %d, disagrees for %d, fails for %d",
		len(rows),
		stat("bytes", func(r row) int { return r.size }),
		stat("xref", func(r row) int { return r.xref }),
		stat("candidates", func(r row) int { return r.cands }),
		stat("objects", func(r row) int { return r.objs }),
		stat("duplicates", func(r row) int { return r.dups }),
		objStm, other, slowest, strictOK, agree, disagree, scanFail)
}

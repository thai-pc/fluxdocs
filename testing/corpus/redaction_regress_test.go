package corpus

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// This file is the redaction corpus no-regression check
// (docs/design/redaction-hardening.md §12.4). Every hardening step may refuse
// more content, but only for the listed reasons: it runs the redaction
// operations over the corpus, classifies each outcome, and compares the
// result with a recorded baseline.
//
// Inputs: every PDF under testing/corpus (baseline committed in
// testdata/redaction_baseline.tsv), plus, when FLUXDOCS_EXTRA_CORPUS names a
// file with one PDF path per line, those PDFs (baseline in the file named by
// FLUXDOCS_EXTRA_BASELINE; without it the outcomes are only summarized).
// FLUXDOCS_REGRESS_WRITE=path writes the current outcomes there, to record a
// baseline. FLUXDOCS_REGRESS_MAX_PAGES (default 3) bounds the pages redacted
// per file.

// redactionBaseline is the committed baseline for testing/corpus.
const redactionBaseline = "testdata/redaction_baseline.tsv"

// allowedNewRefusals are the reasons a Phase 1 step may newly refuse for
// (§12.4): content Phase 1 cannot rewrite yet, and inline images whose end is
// ambiguous (decision D1).
var allowedNewRefusals = map[string]bool{
	"inline_image": true, "pattern_text": true, "softmask_text": true, "type3_text": true,
}

// mustNotOccur are outcomes that fail the check whatever the baseline says: a
// recovered panic, a whole-page redaction whose output still extracts text,
// and a refusal whose error chain breaks decision D2. (A re-open failure of
// the output is "error", which no step may add.)
var mustNotOccur = map[string]bool{"internal": true, "nonempty_text": true, "bad_chain": true}

// refusalReason classifies a redaction error into the reason codes of the
// design (§9 P1): a *core.RedactionError carries its reason (step 1.8; the
// classifier of C8 is gone).
func refusalReason(err error) string {
	var ie *core.InternalError
	var re *core.RedactionError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &ie):
		return "internal"
	case errors.As(err, &re):
		if !errors.Is(err, core.ErrRedactionUnsupported) || errors.Is(err, core.ErrInvalidPDF) {
			return "bad_chain" // D2: a refusal matches ErrRedactionUnsupported, never ErrInvalidPDF
		}
		return string(re.Reason)
	case errors.Is(err, core.ErrEncryptedDocument):
		return "encrypted"
	}
	return "error"
}

// regressOutcome is one (file, operation) result.
type regressOutcome struct {
	file, op, outcome string
}

// redactOutcomes runs the §12.4 operations on one PDF, on its first maxPages
// pages: RedactText(page, ".") per page and the flatten of all of them; a
// region over everything, flattened; and a centred half-page region,
// flattened. A successful whole-page flatten whose output pages still extract
// text is "nonempty_text".
func redactOutcomes(key string, data []byte, maxPages int) []regressOutcome {
	doc, err := core.OpenBytes(data)
	if err != nil {
		return []regressOutcome{{key, "open", "open_failed"}}
	}
	n := min(doc.PageCount, maxPages)
	var out []regressOutcome
	for p := 0; p < n; p++ {
		_, err := doc.RedactText(p, ".")
		out = append(out, regressOutcome{key, "text/p" + strconv.Itoa(p), refusalReason(err)})
	}
	_, err = doc.RedactAndFlatten()
	out = append(out, regressOutcome{key, "text/flatten", refusalReason(err)})

	for _, op := range []string{"area-full", "area-half"} {
		doc, err := core.OpenBytes(data)
		if err != nil {
			return append(out, regressOutcome{key, op, "open_failed"})
		}
		for p := 0; p < n; p++ {
			r := core.Rect{X: -1e6, Y: -1e6, Width: 2e6, Height: 2e6} // everything, whatever the box origin
			if op == "area-half" {
				pg := doc.Pages[p]
				r = core.Rect{X: pg.Width / 4, Y: pg.Height / 4, Width: pg.Width / 2, Height: pg.Height / 2}
			}
			doc.RedactArea(p, r)
		}
		red, err := doc.RedactAndFlatten()
		outcome := refusalReason(err)
		if err == nil && op == "area-full" {
			for p := 0; p < n; p++ {
				if text, err := red.ExtractTextByPage(p, core.ExtractOptions{}); err == nil && strings.TrimSpace(text) != "" {
					outcome = "nonempty_text"
				}
			}
		}
		out = append(out, regressOutcome{key, op, outcome})
	}
	return out
}

// TestRedactionCorpusRegression is the §12.4 gate.
func TestRedactionCorpusRegression(t *testing.T) {
	maxPages := 3
	if v := os.Getenv("FLUXDOCS_REGRESS_MAX_PAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("FLUXDOCS_REGRESS_MAX_PAGES=%q, want a positive integer", v)
		}
		maxPages = n
	}

	type input struct{ key, path string }
	var local []input
	for _, dir := range []string{"clean", "malformed", "encrypted", "forms", "scanned"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
		for _, m := range matches {
			local = append(local, input{filepath.ToSlash(m), m})
		}
	}
	var extra []input
	if list := os.Getenv("FLUXDOCS_EXTRA_CORPUS"); list != "" {
		paths, err := readLines(list)
		if err != nil {
			t.Fatalf("FLUXDOCS_EXTRA_CORPUS: %v", err)
		}
		for _, p := range paths {
			extra = append(extra, input{p, p})
		}
	}

	run := func(inputs []input) []regressOutcome {
		results := make([][]regressOutcome, len(inputs))
		sem := make(chan struct{}, runtime.NumCPU())
		var wg sync.WaitGroup
		for i, in := range inputs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				data, err := os.ReadFile(in.path)
				if err != nil {
					results[i] = []regressOutcome{{in.key, "open", "read_failed"}}
					return
				}
				results[i] = redactOutcomes(in.key, data, maxPages)
			}()
		}
		wg.Wait()
		var all []regressOutcome
		for _, r := range results {
			all = append(all, r...)
		}
		return all
	}

	localOut := run(local)
	var extraOut []regressOutcome
	if len(extra) > 0 {
		extraOut = run(extra)
	}
	if path := os.Getenv("FLUXDOCS_REGRESS_WRITE"); path != "" {
		if err := writeOutcomes(path, append(append([]regressOutcome{}, localOut...), extraOut...)); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d outcomes to %s", len(localOut)+len(extraOut), path)
	}

	for _, o := range localOut {
		if strings.HasPrefix(o.file, "clean/") && o.outcome == "content_ambiguous" {
			t.Errorf("%s %s: content_ambiguous refusal on the clean corpus (must be 0)", o.file, o.op)
		}
	}
	compareBaseline(t, "testing/corpus", localOut, redactionBaseline)
	if len(extra) > 0 {
		if base := os.Getenv("FLUXDOCS_EXTRA_BASELINE"); base != "" {
			compareBaseline(t, "extra corpus", extraOut, base)
		} else {
			summarize(t, "extra corpus (no baseline)", extraOut)
		}
	}
}

// compareBaseline fails on an outcome that must never occur, and on a refusal
// that is new against the baseline (or has a different reason) unless its
// reason is in allowedNewRefusals. Refusals that went away, and new files, are
// logged.
func compareBaseline(t *testing.T, label string, outcomes []regressOutcome, basePath string) {
	t.Helper()
	summarize(t, label, outcomes)
	base, err := readOutcomes(basePath)
	if err != nil {
		t.Fatalf("%s baseline: %v (record one with FLUXDOCS_REGRESS_WRITE)", label, err)
	}
	var added, removed []string
	for _, o := range outcomes {
		if mustNotOccur[o.outcome] {
			t.Errorf("%s %s: %s", o.file, o.op, o.outcome)
			continue
		}
		was, known := base[o.file+"\t"+o.op]
		switch {
		case !known:
			t.Logf("%s %s: %s (not in the baseline)", o.file, o.op, o.outcome)
		case was == o.outcome:
		case o.outcome == "ok":
			removed = append(removed, fmt.Sprintf("%s %s: %s -> ok", o.file, o.op, was))
		case allowedNewRefusals[o.outcome]:
			added = append(added, fmt.Sprintf("%s %s: %s -> %s (allowed)", o.file, o.op, was, o.outcome))
		default:
			t.Errorf("%s %s: %s -> %s: a new refusal for a reason Phase 1 may not add", o.file, o.op, was, o.outcome)
		}
	}
	for _, s := range added {
		t.Logf("%s: new refusal %s", label, s)
	}
	for _, s := range removed {
		t.Logf("%s: refusal gone %s", label, s)
	}
}

// summarize logs the count of each outcome per operation.
func summarize(t *testing.T, label string, outcomes []regressOutcome) {
	t.Helper()
	counts := map[string]int{}
	for _, o := range outcomes {
		op := o.op
		if strings.HasPrefix(op, "text/p") {
			op = "text/page"
		}
		counts[op+" "+o.outcome]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "\n  %-40s %d", k, counts[k])
	}
	t.Logf("%s: %d outcomes:%s", label, len(outcomes), b.String())
}

// readOutcomes reads a baseline: "file<TAB>op<TAB>outcome" per line.
func readOutcomes(path string) (map[string]string, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(lines))
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			return nil, fmt.Errorf("%s: malformed line %q", path, l)
		}
		m[f[0]+"\t"+f[1]] = f[2]
	}
	return m, nil
}

// writeOutcomes writes outcomes in the baseline format, sorted.
func writeOutcomes(path string, outcomes []regressOutcome) error {
	lines := make([]string, len(outcomes))
	for i, o := range outcomes {
		lines[i] = o.file + "\t" + o.op + "\t" + o.outcome
	}
	sort.Strings(lines)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// readLines returns the non-empty lines of a file.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

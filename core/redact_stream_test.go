package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
)

// Property tests of matching across operators (docs/design/redaction-cross-
// operator.md §10.3, the superset rule), over the always-run fixtures below,
// every PDF under testing/corpus, and, when set, the PDFs listed in
// FLUXDOCS_EXTRA_CORPUS (one path per line) and the leak-corpus fixtures
// dumped to FLUXDOCS_LEAK_FIXTURES (testing/security/redaction
// TestLeakFixtureDump):
//
//   - P1 identity: the page model's extraction stream is ExtractTextByPage,
//     byte for byte, on every page the strict model accepts.
//   - P2 mapping: every non-empty match of each pattern in ExtractTextByPage
//     has all its owner runs among the runs Match marks.
//   - P3 output model: after RedactText(page, `\d`) and a half-page
//     RedactArea, ExtractTextByPage of each rewritten output page is the
//     source model's extraction stream without the removed runs, and the
//     caller's pages hold no digit.
//
// FLUXDOCS_STREAM_MAX_PAGES (default 20 for fixtures and testing/corpus, 3
// for the extra corpus) bounds the pages checked per file.

// streamFixturePages are the always-run fixtures: every construct the step
// 2.6 leak rows use (the leak corpus itself lives in another package).
var streamFixturePages = map[string]string{
	"split_bt":       "BT /F1 24 Tf 72 700 Td (SECRET-) Tj ET BT /F1 24 Tf 177.4 700 Td (1234) Tj ET",
	"same_bt_tj":     "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (1234) Tj ET",
	"same_bt_TJ":     "BT /F1 24 Tf 72 700 Td [(SECRET-)] TJ [(1234)] TJ ET",
	"per_char":       "BT /F1 24 Tf 72 700 Td (S) Tj (E) Tj (C) Tj (R) Tj (E) Tj (T) Tj (-) Tj (1) Tj (2) Tj (3) Tj (4) Tj ET",
	"tj_gap":         "BT /F1 24 Tf 72 700 Td [(SECRET-) -250] TJ [-300 (1234) -300] TJ [(x)] TJ ET",
	"gaps_and_space": "BT /F1 24 Tf 72 700 Td (a ) Tj [-500 (b)] TJ [(c\t)] TJ [-500 (d)] TJ ET",
	"quotes":         "BT /F1 12 Tf 14 TL 72 700 Td (a) Tj (b) ' 1 2 (c) \" T* (d) Tj ET",
	"line_moves":     "BT /F1 12 Tf 72 700 Td (a) Tj 0 -20 Td 0 -20 Td (b) Tj 0 5 Td (sup) Tj 10 -1 TD (c) Tj ET",
	"tm":             "BT /F1 12 Tf 1 0 0 1 72 700 Tm (a) Tj 1 0 0 1 72 680 Tm (b) Tj 0 1 -1 0 200 100 Tm (r) Tj (s) Tj ET",
	"q_Q_size":       "BT /F1 24 Tf 72 700 Td (a) Tj q /F1 2 Tf Q 0 -5 Td (b) Tj ET",
	"fonts":          "BT /F1 24 Tf 72 700 Td (SECRET-) Tj /F2 24 Tf (\x93x) Tj /F9 12 Tf (\xe9) Tj ET",
	"bdc":            "BT /F1 24 Tf 72 700 Td /Span <</ActualText (z)>> BDC (SECRET-) Tj EMC (1234) Tj ET",
	"tc_tw_tz":       "BT /F1 24 Tf 72 700 Td (SECRET-) Tj 5 Tc 10 Tw 150 Tz 3 Ts (1234) Tj ET",
	"empty":          "BT /F1 24 Tf 72 700 Td () Tj [] TJ [-400] TJ (a) Tj ET",
	"no_font":        "BT 72 700 Td (before) Tj ET",
	"paths":          "0 0 10 10 re f BT /F1 24 Tf 72 700 Td (SEC) Tj ET 1 0 0 RG 0 0 m 5 5 l S BT /F1 24 Tf 121.3 700 Td (RET-1234) Tj ET",
	"nested_TJ":      "BT /F1 24 Tf 72 700 Td [(SEC) [(junk)] (RET-1234)] TJ ET",
}

// streamFixtures builds the always-run fixtures: one page each, plus a
// two-page one and a content array.
func streamFixtures() map[string][]byte {
	out := map[string][]byte{}
	for name, c := range streamFixturePages {
		out["fixture/"+name] = extPDF([]extPage{{content: c}}, nil, "")
	}
	out["fixture/two_pages"] = extPDF([]extPage{{content: showAt("(SECRET-1234)", 700)},
		{content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (1234) Tj ET"}}, nil, "")
	return out
}

// streamCorpus returns the inputs: name → loader.
func streamCorpus(t *testing.T) (names []string, load func(string) ([]byte, error), maxPages func(string) int) {
	t.Helper()
	fixtures := streamFixtures()
	paths := map[string]string{}
	for name := range fixtures {
		names = append(names, name)
	}
	for _, dir := range []string{"clean", "malformed", "encrypted", "forms", "scanned"} {
		matches, _ := filepath.Glob(filepath.Join("..", "testing", "corpus", dir, "*.pdf"))
		for _, m := range matches {
			name := "corpus/" + dir + "/" + filepath.Base(m)
			names = append(names, name)
			paths[name] = m
		}
	}
	if dir := os.Getenv("FLUXDOCS_LEAK_FIXTURES"); dir != "" {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
		if len(matches) == 0 {
			t.Fatalf("FLUXDOCS_LEAK_FIXTURES=%s holds no PDF", dir)
		}
		for _, m := range matches {
			name := "leak/" + filepath.Base(m)
			names = append(names, name)
			paths[name] = m
		}
	}
	extra := map[string]bool{}
	if list := os.Getenv("FLUXDOCS_EXTRA_CORPUS"); list != "" {
		data, err := os.ReadFile(list)
		if err != nil {
			t.Fatalf("FLUXDOCS_EXTRA_CORPUS: %v", err)
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				names = append(names, l)
				paths[l] = l
				extra[l] = true
			}
		}
	}
	limit, extraLimit := 20, 3
	if v := os.Getenv("FLUXDOCS_STREAM_MAX_PAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("FLUXDOCS_STREAM_MAX_PAGES=%q", v)
		}
		limit, extraLimit = n, n
	}
	load = func(name string) ([]byte, error) {
		if b, ok := fixtures[name]; ok {
			return b, nil
		}
		return os.ReadFile(paths[name])
	}
	maxPages = func(name string) int {
		if extra[name] {
			return extraLimit
		}
		return limit
	}
	return names, load, maxPages
}

// forEachInput runs f on every input, NumCPU at a time, and reports the
// failures sorted.
func forEachInput(t *testing.T, f func(name string, data []byte, maxPages int) []string) {
	names, load, maxPages := streamCorpus(t)
	var mu sync.Mutex
	var fails []string
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := load(name)
			if err != nil {
				return
			}
			got := f(name, data, maxPages(name))
			mu.Lock()
			fails = append(fails, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Strings(fails)
	for i, f := range fails {
		if i == 50 {
			t.Errorf("... and %d more", len(fails)-50)
			break
		}
		t.Error(f)
	}
}

// p2Patterns are the patterns of P2.
var p2Patterns = []string{`.`, `\d{4}`, `[A-Za-z]{5,}`, `\S+ \S+`, `(?s).{3}`, `^.`, `.$`}

func TestRedactStream_P1P2(t *testing.T) {
	var pages, checked, budget int64
	var mu sync.Mutex
	res := make([]*regexp.Regexp, len(p2Patterns))
	for i, p := range p2Patterns {
		res[i] = regexp.MustCompile(p)
	}
	forEachInput(t, func(name string, data []byte, maxPages int) []string {
		doc, err := OpenBytes(data)
		if err != nil {
			return nil
		}
		defer doc.Close()
		var fails []string
		var np, nc, nb int64
		for i := 0; i < min(doc.PageCount, maxPages); i++ {
			np++
			page, err := doc.redactionPage(i)
			if err != nil {
				continue // the strict model refuses the page: nothing to check
			}
			text, err := doc.ExtractTextByPage(i, ExtractOptions{})
			if err != nil {
				fails = append(fails, fmt.Sprintf("%s page %d: ExtractTextByPage: %v", name, i, err))
				continue
			}
			if got := page.ExtractionText(); got != text {
				fails = append(fails, fmt.Sprintf("P1 %s page %d: extraction stream (%d bytes) != ExtractTextByPage (%d bytes) at byte %d",
					name, i, len(got), len(text), firstDiff(got, text)))
				continue
			}
			nc++
			for _, re := range res {
				m, err := page.Match(re)
				if err != nil {
					nb++ // a budget refusal: the page is refused, nothing is missed
					continue
				}
				marked := map[int]bool{}
				for _, r := range page.MarkedRuns(m.Marks) {
					marked[r] = true
				}
				for _, x := range re.FindAllStringIndex(text, -1) {
					if x[0] == x[1] {
						continue
					}
					for _, r := range page.ExtractionRuns(x[0], x[1]) {
						if !marked[r] {
							fails = append(fails, fmt.Sprintf("P2 %s page %d pattern %s: the match at %d..%d owns run %d, which Match does not mark",
								name, i, re, x[0], x[1], r))
							break
						}
					}
				}
			}
		}
		mu.Lock()
		pages, checked, budget = pages+np, checked+nc, budget+nb
		mu.Unlock()
		return fails
	})
	t.Logf("P1/P2: %d pages, %d read by the strict model and checked, %d Match budget refusals", pages, checked, budget)
	if checked == 0 {
		t.Fatal("no page checked")
	}
}

func TestRedactStream_P3(t *testing.T) {
	var mu sync.Mutex
	captured := map[*Document]struct {
		models map[int]*redact.Page
		outs   map[int]redact.Output
	}{}
	redactHooks.built = func(d *Document, models map[int]*redact.Page, outs map[int]redact.Output) {
		mu.Lock()
		defer mu.Unlock()
		c := captured[d]
		c.models, c.outs = map[int]*redact.Page{}, map[int]redact.Output{}
		for k, v := range models {
			c.models[k] = v
		}
		for k, v := range outs {
			c.outs[k] = v
		}
		captured[d] = c
	}
	defer func() { redactHooks.built = nil }()
	digit := regexp.MustCompile(`\d`)
	var docs, redacted, refused, pagesChecked int64
	forEachInput(t, func(name string, data []byte, maxPages int) []string {
		doc, err := OpenBytes(data)
		if err != nil {
			return nil
		}
		defer doc.Close()
		n := min(doc.PageCount, maxPages)
		for i := 0; i < n; i++ {
			if _, err := doc.RedactText(i, `\d`); err != nil {
				return nil // refused: no output to check
			}
			pg := doc.Pages[i]
			doc.RedactArea(i, Rect{X: pg.Width / 4, Y: pg.Height / 4, Width: pg.Width / 2, Height: pg.Height / 2})
		}
		out, err := doc.RedactAndFlatten()
		mu.Lock()
		docs++
		c := captured[doc]
		delete(captured, doc)
		mu.Unlock()
		if err != nil {
			// Only a refusal is a page this test cannot check; a verification
			// failure or a recovered panic is a redaction bug, and so is any
			// other error once RedactText accepted every page.
			var re *RedactionError
			if errors.As(err, &re) && !errors.Is(err, ErrRedactionVerification) && !errors.Is(err, ErrInternal) {
				mu.Lock()
				refused++
				mu.Unlock()
				return nil
			}
			return []string{fmt.Sprintf("P3 %s: RedactAndFlatten: %v", name, err)}
		}
		defer out.Close()
		var fails []string
		var checkedHere int64
		for _, i := range sortedKeys(c.outs) {
			got, err := out.ExtractTextByPage(i, ExtractOptions{})
			if err != nil {
				fails = append(fails, fmt.Sprintf("P3 %s page %d: output ExtractTextByPage: %v", name, i, err))
				continue
			}
			want, err := c.models[i].ExtractionAfter(c.outs[i])
			if err != nil {
				fails = append(fails, fmt.Sprintf("P3 %s page %d: ExtractionAfter: %v", name, i, err))
				continue
			}
			if got != want {
				fails = append(fails, fmt.Sprintf("P3 %s page %d: output text (%d bytes) != assembled source (%d bytes) at byte %d",
					name, i, len(got), len(want), firstDiff(got, want)))
			}
			if i < n && digit.MatchString(got) {
				fails = append(fails, fmt.Sprintf("P3 %s page %d: the output still holds a digit", name, i))
			}
			checkedHere++
		}
		mu.Lock()
		redacted++
		pagesChecked += checkedHere
		mu.Unlock()
		return fails
	})
	t.Logf("P3: %d documents, %d redacted, %d refused, %d output pages checked", docs, redacted, refused, pagesChecked)
	if pagesChecked == 0 {
		t.Fatal("no page checked")
	}
}

// firstDiff is the first byte index where a and b differ.
func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// FuzzRedactStreamP1 is P1 on fuzzed page content: whenever the strict model
// reads the page, its extraction stream is ExtractTextByPage. Seeds: the
// always-run fixtures (the constructs of the step 2.6 leak rows).
//
// Run: go test ./core -run=xxx -fuzz=FuzzRedactStreamP1 -fuzztime=60s
func FuzzRedactStreamP1(f *testing.F) {
	names := make([]string, 0, len(streamFixturePages))
	for name := range streamFixturePages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f.Add(streamFixturePages[name])
	}
	f.Fuzz(func(t *testing.T, content string) {
		doc, err := OpenBytes(extPDF([]extPage{{content: content}}, nil, ""))
		if err != nil {
			t.Skip()
		}
		page, err := doc.redactionPage(0)
		if err != nil {
			return
		}
		text, err := doc.ExtractTextByPage(0, ExtractOptions{})
		if err != nil {
			t.Fatalf("ExtractTextByPage: %v", err)
		}
		if got := page.ExtractionText(); got != text {
			t.Fatalf("P1: extraction stream %q != ExtractTextByPage %q", got, text)
		}
	})
}

// TestRedactStream_ClosureAfterRegion: a region removes the run between
// "SECRET-" and "1234" on one line; the output would show the RedactText
// match, so closure removes the neighbours too, and verification (which
// matches the output's text) passes.
func TestRedactStream_ClosureAfterRegion(t *testing.T) {
	pdf := extPDF([]extPage{{content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj ET BT /F1 24 Tf 300 700 Td (X) Tj ET " +
		"BT /F1 24 Tf 177.3 700 Td (1234) Tj ET BT /F1 24 Tf 72 600 Td (kept) Tj ET"}}, nil, "")
	doc, err := OpenBytes(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if rects, err := doc.RedactText(0, `SECRET-\d+`); err != nil || len(rects) != 0 {
		t.Fatalf("RedactText = %v, %v; the source shows no match", rects, err)
	}
	if err := doc.RedactArea(0, Rect{X: 299, Y: 690, Width: 10, Height: 30}); err != nil {
		t.Fatal(err)
	}
	out, err := doc.RedactAndFlatten()
	if err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	text, err := out.ExtractTextByPage(0, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "SECRET") || strings.Contains(text, "1234") || !strings.Contains(text, "kept") {
		t.Errorf("output text %q, want only the unrelated line", text)
	}
}

// TestRedactStream_DigitsDiagnostic is the corpus diagnostic of the note's
// §10.4 (op text-digits), run when FLUXDOCS_STREAM_DIGITS=1: Match with
// `\d{4,}` on every page the strict model reads, with stream matching on and
// off. It logs the runs only the streams mark — and of those, the runs that
// own a match in ExtractTextByPage (real in what extraction shows) — and the
// histogram of closure rounds.
func TestRedactStream_DigitsDiagnostic(t *testing.T) {
	if os.Getenv("FLUXDOCS_STREAM_DIGITS") != "1" {
		t.Skip("FLUXDOCS_STREAM_DIGITS not set")
	}
	re := regexp.MustCompile(`\d{4,}`)
	var mu sync.Mutex
	var pages, streamOnly, inExtract int
	rounds := map[int]int{}
	var lines []string
	// Stream matching is toggled per call, so pages are read one at a time.
	names, load, maxPages := streamCorpus(t)
	for _, name := range names {
		data, err := load(name)
		if err != nil {
			continue
		}
		doc, err := OpenBytes(data)
		if err != nil {
			continue
		}
		for i := 0; i < min(doc.PageCount, maxPages(name)); i++ {
			page, err := doc.redactionPage(i)
			if err != nil {
				continue
			}
			on, err := page.Match(re)
			if err != nil {
				lines = append(lines, fmt.Sprintf("%s page %d: refused: %v", name, i, err))
				continue
			}
			restore := redact.SetStreamMatchingForTest(false)
			off, _ := page.Match(re)
			restore()
			was := map[int]bool{}
			for _, r := range page.MarkedRuns(off.Marks) {
				was[r] = true
			}
			text := page.ExtractionText()
			real := map[int]bool{}
			for _, x := range re.FindAllStringIndex(text, -1) {
				for _, r := range page.ExtractionRuns(x[0], x[1]) {
					real[r] = true
				}
			}
			n, nr := 0, 0
			for _, r := range page.MarkedRuns(on.Marks) {
				if !was[r] {
					n++
					if real[r] {
						nr++
					}
				}
			}
			mu.Lock()
			pages++
			rounds[on.Rounds]++
			streamOnly += n
			inExtract += nr
			if n > 0 {
				lines = append(lines, fmt.Sprintf("%s page %d: %d runs marked only by streams, %d of them own a match in ExtractTextByPage", name, i, n, nr))
			}
			mu.Unlock()
		}
		doc.Close()
	}
	sort.Strings(lines)
	for _, l := range lines {
		t.Log(l)
	}
	t.Logf("text-digits: %d pages, %d runs marked only by streams (%d owning a match in ExtractTextByPage), closure rounds %v",
		pages, streamOnly, inExtract, rounds)
}

// TestRedactStream_InternalErrorIsErrInternal: an engine bug reported by
// matching (redact.ErrInternal, e.g. a match that is not a subslice of its
// stream) surfaces as ErrInternal, never as a refusal or ErrInvalidPDF.
func TestRedactStream_InternalErrorIsErrInternal(t *testing.T) {
	err := fmt.Errorf("%w: test", redact.ErrInternal)
	for name, got := range map[string]error{"redactionScanError": redactionScanError(3, err)} {
		var re *RedactionError
		if !errors.Is(got, ErrInternal) || errors.Is(got, ErrInvalidPDF) || errors.As(got, &re) {
			t.Errorf("%s = %v, want ErrInternal", name, got)
		}
	}
}

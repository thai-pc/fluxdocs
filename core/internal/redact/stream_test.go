package redact

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unsafe"

	"github.com/thai-pc/fluxdocs/core/internal/textflow"
	"github.com/thai-pc/fluxdocs/core/parse"
	"golang.org/x/text/unicode/norm"
)

// Tests of matching across operators (stream.go; docs/design/redaction-
// cross-operator.md §10.2).

// runTexts returns the first reading of each run in runs.
func runTexts(p *Page, runs []int) []string {
	var out []string
	for _, i := range runs {
		out = append(out, p.runs[i].texts[0])
	}
	return out
}

// markedTexts returns the first reading of each run m marks, in run order.
func markedTexts(p *Page, m Match) []string { return runTexts(p, p.MarkedRuns(m.Marks)) }

const (
	// fallbackRes binds /F1 on the page to a two-byte font whose /ToUnicode
	// maps every code c to U+E000 + c (its Text reading matches no pattern
	// here) and on the /Pages node to Helvetica: the run reads Latin-1
	// through the fallback candidate.
	fallbackRes = "<< /Font << /F1 6 0 R >> >>"
	// diffFont is a simple font whose /Differences name the glyphs of A, B,
	// C as S, E, C.
	diffFont = "<< /Type /Font /Subtype /Type1 /BaseFont /X /Encoding << /Differences [65 /S /E /C] >> >>"
	// idHNoUnicode is an Identity-H font without /ToUnicode: a reader
	// derives text for its codes that no reading holds, so redaction
	// refuses it (fontmodel Font.TextUnknown).
	idHNoUnicode = "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [7 0 R] >>"
	// idHPUAUnicode is an Identity-H font whose /ToUnicode (object 12,
	// puaUnicode) maps every two-byte code c to U+E000 + c, from the
	// private-use area up: Text reads characters no pattern here matches,
	// and every code is mapped to some text (one mapped to none is refused:
	// fontmodel Font.TextUnknown).
	idHPUAUnicode = "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [7 0 R] /ToUnicode 12 0 R >>"
	cidFont       = "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X >>"
)

// puaUnicode is the /ToUnicode stream of idHPUAUnicode.
var puaUnicode = stm("", "1 begincodespacerange <0000> <FFFF> endcodespacerange 1 beginbfrange <0000> <FFFF> <E000> endbfrange")

// TestAnalyze_CompositeWithoutToUnicodeRefused: a Type 0 font with no
// /ToUnicode shows codes a reader derives text for (an Adobe collection, or
// an Identity font's codes as Unicode) that no reading holds: the page is
// refused, whether the font is the page's or a fallback scope's candidate.
func TestAnalyze_CompositeWithoutToUnicodeRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    fixture
	}{
		{"the page's font", fixture{content: "BT /H 24 Tf 72 700 Td <00530045> Tj ET",
			res: "<< /Font << /H 6 0 R >> >>", extra: map[int]string{6: idHNoUnicode, 7: cidFont}}},
		{"a fallback scope's candidate", fixture{content: "BT /F1 24 Tf 72 700 Td (SECRET-1) Tj ET",
			res: fallbackRes, pagesRes: "<< /Font << /F1 5 0 R >> >>", extra: map[int]string{6: idHNoUnicode, 7: cidFont}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := analyze(t, tc.f, nil)
			var rf *Refusal
			if !errors.As(err, &rf) || rf.Reason != ReasonContentAmbiguous {
				t.Fatalf("err = %v, want a %s refusal", err, ReasonContentAmbiguous)
			}
		})
	}
}

func TestMatch_AcrossOperators(t *testing.T) {
	for _, tc := range []struct {
		name, content, pattern string
		f                      fixture
		want                   []string // marked runs' first readings, in run order
	}{
		{name: "same text object", content: "BT /F1 24 Tf 72 700 Td (SEC) Tj (RET-) Tj (1234) Tj (tail) Tj ET",
			pattern: `SECRET-\d+`, want: []string{"SEC", "RET-", "1234"}},
		{name: "two text objects on one line", content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj ET BT /F1 24 Tf 177.4 700 Td (1234) Tj ET",
			pattern: `SECRET-\d+`, want: []string{"SECRET-", "1234"}},
		{name: "match on a piece boundary", content: "BT /F1 24 Tf 72 700 Td (ab) Tj (cd) Tj ET",
			pattern: `bc`, want: []string{"ab", "cd"}},
		{name: "match inside one piece", content: "BT /F1 24 Tf 72 700 Td (abcd) Tj (ef) Tj ET",
			pattern: `bc`, want: []string{"abcd"}},
		{name: "last piece", content: "BT /F1 24 Tf 72 700 Td (ab) Tj (cd) Tj ET",
			pattern: `d$`, want: []string{"cd"}},
		{name: "separator-only match marks nothing", content: "BT /F1 24 Tf 72 700 Td (a) Tj 0 -30 Td (b) Tj ET",
			pattern: `\n`, want: nil},
		{name: "spanning a separator", content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj 0 -30 Td (1234) Tj ET",
			pattern: `-\s1`, want: []string{"SECRET-", "1234"}},
		{name: "new line is not joined", content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj 0 -30 Td (1234) Tj ET",
			pattern: `SECRET-\d+`, want: nil},
		{name: "hyphenation is not folded (Q9)", content: "BT /F1 24 Tf 72 700 Td (SEC-) Tj 0 -30 Td (RET-1234) Tj ET",
			pattern: `SECRET-\d+`, want: nil},
		{name: "a hyphenation pattern matches", content: "BT /F1 24 Tf 72 700 Td (SEC-) Tj 0 -30 Td (RET-1234) Tj ET",
			pattern: `SEC-?\s*RET-\d+`, want: []string{"SEC-", "RET-1234"}},
		{name: "soft hyphen at a line end", content: "BT /F1 24 Tf 72 700 Td (SEC\xad) Tj 0 -30 Td (RET-1234) Tj ET",
			pattern: `SECRET-\d+`, want: nil},
		{name: "per-run anchor still matches", content: "BT /F1 24 Tf 72 700 Td (xx) Tj (SECRET) Tj ET",
			pattern: `^SECRET`, want: []string{"SECRET"}},
		{name: "per-run word boundary still matches", content: "BT /F1 24 Tf 72 700 Td (A) Tj (SECRET) Tj ET",
			pattern: `\bSEC`, want: []string{"SECRET"}},
		{name: "TJ gap: the Text slot joins", content: "BT /F1 24 Tf 72 700 Td [(SECRET-) -250] TJ [(1234)] TJ ET",
			pattern: `SECRET-\d+`, want: []string{"SECRET-", "1234"}},
		{name: "Differences glyph names across runs (alt slot)", content: "BT /D 24 Tf 72 700 Td (AB) Tj (C) Tj ET",
			f: fixture{res: "<< /Font << /D 6 0 R >> >>", extra: map[int]string{6: diffFont}}, pattern: `SEC`,
			want: []string{"AB", "C"}},
		{name: "Identity-H whose /ToUnicode reads other characters: the Latin-1 slot", content: "BT /H 24 Tf 72 700 Td <00530045> Tj <0043> Tj ET",
			f: fixture{res: "<< /Font << /H 6 0 R >> >>", extra: map[int]string{6: idHPUAUnicode, 7: cidFont, 12: puaUnicode}}, pattern: "S\x00E\x00C",
			want: []string{"\U0000E053\U0000E045", "\U0000E043"}},
		{name: "a fallback scope's candidate font", content: "BT /F1 24 Tf 72 700 Td (SECRET-1) Tj (2345) Tj ET",
			f:       fixture{res: fallbackRes, pagesRes: "<< /Font << /F1 5 0 R >> >>", extra: map[int]string{6: idHPUAUnicode, 7: cidFont, 12: puaUnicode}},
			pattern: `SECRET-\d+`, want: []string{"\U00013345\U00012352\U00012554\U00010D31", "\U00011233\U00011435"}},
		{name: "closure: removal joins the neighbours", content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (X) Tj (1234) Tj ET",
			pattern: `SECRET-\d+|X`, want: []string{"SECRET-", "X", "1234"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.content = tc.content
			p, _, err := analyze(t, f, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := mustMatch(t, p, regexp.MustCompile(tc.pattern))
			if got := markedTexts(p, m); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("marked %q, want %q (extraction %q)", got, tc.want, p.ExtractionText())
			}
			if len(m.Rects) != len(m.Marks) {
				t.Errorf("%d rects for %d marks: want one per run (Q8)", len(m.Rects), len(m.Marks))
			}
			again := mustMatch(t, p, regexp.MustCompile(tc.pattern))
			if !reflect.DeepEqual(m, again) {
				t.Error("Match is not deterministic")
			}
		})
	}
}

// TestMatch_ClosureNeedles: a match needle is the matched stream text; a
// match found only by closure is none (Q2).
func TestMatch_ClosureNeedles(t *testing.T) {
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (XXXX) Tj (1234) Tj (split) Tj (ting) Tj ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mustMatch(t, p, regexp.MustCompile(`SECRET-\d+|XXXX|litt`))
	if !reflect.DeepEqual(m.Needles, []string{"XXXX", "litt"}) {
		t.Errorf("needles %q, want the source matches only", m.Needles)
	}
	if m.Rounds != 1 {
		t.Errorf("closure rounds %d, want 1", m.Rounds)
	}
}

// abChain is the crafted closure chain: pattern ab over k runs "a" then k runs
// "b"; each round removes one more pair.
func abChain(k int) string {
	return "BT /F1 24 Tf 72 700 Td " + strings.Repeat("(a) Tj ", k) + strings.Repeat("(b) Tj ", k) + "ET"
}

func TestMatch_ClosureRoundsBounded(t *testing.T) {
	ab := regexp.MustCompile(`ab`)
	p, _, err := analyze(t, fixture{content: abChain(maxMatchRounds + 1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mustMatch(t, p, ab)
	if len(m.Marks) != 2*(maxMatchRounds+1) || m.Rounds != maxMatchRounds {
		t.Errorf("%d marks after %d rounds, want every run after %d", len(m.Marks), m.Rounds, maxMatchRounds)
	}
	p, _, err = analyze(t, fixture{content: abChain(maxMatchRounds + 2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Match(ab); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("Match = %v, want a budget_exceeded refusal after %d rounds", err, maxMatchRounds)
	}
	if _, err := p.Rewrite(RewritePlan{Patterns: []*regexp.Regexp{regexp.MustCompile(`a`)}}); err != nil {
		t.Errorf("Rewrite with a pattern no removal creates: %v", err)
	}
}

// TestMatch_StreamMatchCap: past maxStreamMatches matches in a stream the
// page is refused. The operator budget (which charges every reading) keeps
// real pages far below the cap — a stream holds at most about 2.8 Mi
// characters under it — so the test raises that budget to reach it.
func TestMatch_StreamMatchCap(t *testing.T) {
	raise := func(l *limits) { l.ops = 64 << 20 }
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (" + strings.Repeat("1", maxStreamMatches+1) + ") Tj ET"}, raise)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Match(regexp.MustCompile(`\d`)); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("Match = %v, want a budget_exceeded refusal past %d matches (Q7)", err, maxStreamMatches)
	}
	if _, err := p.Match(regexp.MustCompile(`\d+`)); err != nil {
		t.Errorf("one match of the whole run: %v", err)
	}
}

// TestRewrite_ClosureAfterRegion: a region removes the middle run; the
// pattern then matches across its neighbours, which go too.
func TestRewrite_ClosureAfterRegion(t *testing.T) {
	content := "BT /F1 24 Tf 72 700 Td (SECRET-) Tj ET BT /F1 24 Tf 300 700 Td (X) Tj ET BT /F1 24 Tf 177.3 700 Td (1234) Tj ET"
	p, _, err := analyze(t, fixture{content: content}, nil)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`SECRET-\d+`)
	if m := mustMatch(t, p, re); len(m.Marks) != 0 {
		t.Fatalf("the source shows no match, got %d marks", len(m.Marks))
	}
	region := []Rect{{X: 299, Y: 690, Width: 10, Height: 30}}
	without, err := p.Rewrite(RewritePlan{Regions: region})
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := p.ExtractionAfter(without); after != "SECRET-1234" {
		t.Fatalf("without closure the output reads %q, want the joined secret (the fixture's point)", after)
	}
	out, err := p.Rewrite(RewritePlan{Regions: region, Patterns: []*regexp.Regexp{re}})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out.Content); strings.Contains(s, "SECRET") || strings.Contains(s, "1234") {
		t.Errorf("closure left the joined match: %q", s)
	}
	if after, _ := p.ExtractionAfter(out); after != "" {
		t.Errorf("output reads %q, want nothing", after)
	}
	// The needle of the joined text closes the same way.
	n := &Needles{}
	n.Add(Skeleton("SECRET-1234"), 1)
	if out, err := p.Rewrite(RewritePlan{Regions: region, Needles: n}); err != nil || strings.Contains(string(out.Content), "1234") {
		t.Errorf("needle closure: %v, %q", err, out.Content)
	}
}

func TestFindNeedles_AcrossOperators(t *testing.T) {
	n := &Needles{}
	n.Add(Skeleton("SECRET-1234"), 1)
	for _, tc := range []struct {
		name, content string
		want          []string
	}{
		{"same line", "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (1234) Tj (x) Tj ET", []string{"SECRET-", "1234"}},
		{"across a line break (Q3)", "BT /F1 24 Tf 72 700 Td (SECRET-) Tj 0 -30 Td (1234) Tj ET", []string{"SECRET-", "1234"}},
		{"soft hyphen at a line end", "BT /F1 24 Tf 72 700 Td (SEC\xad) Tj 0 -30 Td (RET-1234) Tj ET", []string{"SEC­", "RET-1234"}},
		{"per character", "BT /F1 24 Tf 72 700 Td (S) Tj (E) Tj (C) Tj (R) Tj (E) Tj (T) Tj (-) Tj (1) Tj (2) Tj (3) Tj (4) Tj ET",
			strings.Split("SECRET-1234", "")},
		{"not there", "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (123) Tj ET", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := analyze(t, fixture{content: tc.content}, nil)
			if err != nil {
				t.Fatal(err)
			}
			marks, hits, err := p.FindNeedles(n, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := runTexts(p, p.MarkedRuns(marks)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("marked %q, want %q", got, tc.want)
			}
			if len(hits) != len(marks) {
				t.Errorf("%d hits for %d marks", len(hits), len(marks))
			}
		})
	}
}

// TestFindNeedles_Done: with a rewrite given, its planned runs are left out
// of the streams, so their neighbours join.
func TestFindNeedles_Done(t *testing.T) {
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (X) Tj (1234) Tj ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := &Needles{}
	n.Add(Skeleton("SECRET-1234"), 1)
	if marks, _, _ := p.FindNeedles(n, nil); len(marks) != 0 {
		t.Fatalf("the source holds no needle, got %d marks", len(marks))
	}
	x := mustMatch(t, p, regexp.MustCompile(`X`))
	done, err := p.Rewrite(RewritePlan{Marks: x.Marks})
	if err != nil {
		t.Fatal(err)
	}
	marks, _, err := p.FindNeedles(n, &done)
	if err != nil {
		t.Fatal(err)
	}
	if got := runTexts(p, p.MarkedRuns(marks)); !reflect.DeepEqual(got, []string{"SECRET-", "1234"}) {
		t.Errorf("marked %q after X is removed, want its neighbours", got)
	}
}

func TestStreams_Aliasing(t *testing.T) {
	plain, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (ab) Tj (cd) Tj ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := plain.ts.part.n; n != 1 {
		t.Errorf("Helvetica page: %d slot classes, want 1 (every slot reads the same)", n)
	}
	if n := len(plain.ts.base); n != 1 {
		t.Errorf("Helvetica page without gaps: %d streams, want 1 (extraction equals the Text slot)", n)
	}
	gap, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td [(ab) -300 (cd)] TJ ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(gap.ts.base); n != 2 {
		t.Errorf("a TJ gap: %d streams, want 2 (extraction has the space)", n)
	}
	diff, _, err := analyze(t, fixture{content: "BT /D 24 Tf 72 700 Td (AB) Tj (C) Tj ET", res: "<< /Font << /D 6 0 R >> >>",
		extra: map[int]string{6: diffFont}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := diff.ts.part.n; n < 2 {
		t.Errorf("/Differences page: %d slot classes, want the glyph-name slot apart", n)
	}
	var texts []string
	for _, s := range diff.ts.base {
		texts = append(texts, s.text)
	}
	if !reflect.DeepEqual(texts, []string{"ABC", "SEC"}) {
		t.Errorf("streams %q, want [ABC SEC]", texts)
	}
}

func TestStreams_SizeCounted(t *testing.T) {
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (" + strings.Repeat("x", 1000) + ") Tj ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	with := p.Size()
	ts := p.ts
	p.ts = nil
	without := p.Size()
	p.ts = ts
	// The extraction stream is the recorded reading itself: its text is held
	// once.
	if with-without < 1000+eventSize*int64(len(ts.events)) {
		t.Errorf("Size grows by %d bytes for the streams, want at least the extraction text and its events", with-without)
	}
	if got := unsafe.Sizeof(textflow.Event{}); got != eventSize {
		t.Errorf("textflow.Event is %d bytes, eventSize says %d", got, eventSize)
	}
	// The extraction stream's text is the recorded reading: counted once.
	want := int64(len(ts.flowText)) + eventSize*int64(len(ts.events))
	for i, s := range ts.base {
		want += 48 + 8*int64(len(s.pieces))
		if i > 0 {
			want += int64(len(s.text))
		}
	}
	if ts.base[0].text != ts.flowText {
		t.Fatalf("the first stream is not the recorded reading")
	}
	if got := ts.size(); got != want {
		t.Errorf("stream size %d, want %d", got, want)
	}
}

func TestStreams_Off(t *testing.T) {
	defer SetStreamMatchingForTest(false)()
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (SECRET-) Tj (1234) Tj ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := mustMatch(t, p, regexp.MustCompile(`SECRET-\d+`)); len(m.Marks) != 0 {
		t.Errorf("stream matching off: %d marks, want each run alone (none)", len(m.Marks))
	}
}

func TestNeedles_Truncated(t *testing.T) {
	long := strings.Repeat("ab", maxNeedleRunes)
	n := &Needles{}
	n.Add(long, 1)
	if n.list[0] != long[:maxNeedleRunes] {
		t.Errorf("needle of %d runes kept %d bytes, want %d", 2*maxNeedleRunes, len(n.list[0]), maxNeedleRunes)
	}
	if n.Find(long[:maxNeedleRunes]+"zz") == 0 {
		t.Error("the prefix of a truncated needle is not found")
	}
	var spans [][2]int
	n.FindSpans("x"+long[:maxNeedleRunes], func(i, j int, _ uint8) { spans = append(spans, [2]int{i, j}) })
	if !reflect.DeepEqual(spans, [][2]int{{1, 1 + maxNeedleRunes}}) {
		t.Errorf("FindSpans = %v", spans)
	}
}

// TestSlotSearcher_Chunks: a needle across the end of a searched chunk, and
// across a class split, is found.
func TestSlotSearcher_Chunks(t *testing.T) {
	n := &Needles{}
	n.Add("SECRET-1234", 1)
	var same [numSlots]uint8
	ss := newSlotSearcher(n)
	filler := []byte(strings.Repeat("x", searchChunk-4))
	ss.add(&same, func(int) []byte { return filler })
	ss.add(&same, func(int) []byte { return []byte("SECRET-") })
	ss.add(&same, func(int) []byte { return []byte("1234") })
	if !ss.done() {
		t.Error("a needle across a chunk boundary is not found")
	}
	split := [numSlots]uint8{}
	split[latinSlot] = 1
	ss = newSlotSearcher(n)
	ss.add(&same, func(int) []byte { return []byte("SECRET-") })
	ss.add(&split, func(s int) []byte {
		if s == latinSlot {
			return []byte("1234")
		}
		return []byte("zzzz")
	})
	if !ss.done() {
		t.Error("a needle across a class split is not found")
	}
	ss = newSlotSearcher(n)
	ss.add(&same, func(int) []byte { return []byte("SECRET-") })
	ss.add(&same, func(int) []byte { return []byte("123") })
	if ss.done() {
		t.Error("found a needle that is not there")
	}
}

// TestSlotSearcher_NoBoundaryCut: a chunk with no NFKC boundary (invalid
// UTF-8: continuation bytes only) is cut after them, and a needle across that
// cut is found through the overlap.
func TestSlotSearcher_NoBoundaryCut(t *testing.T) {
	filler := []byte(strings.Repeat("\x80", searchChunk))
	if b := norm.NFKC.LastBoundary(filler); b > 0 {
		t.Fatalf("filler has an NFKC boundary at %d: the test does not reach the fallback", b)
	}
	n := &Needles{}
	n.Add(Skeleton("\x80\x80\x80Z"), 1)
	var same [numSlots]uint8
	ss := newSlotSearcher(n)
	ss.add(&same, func(int) []byte { return filler })
	if ss.hit {
		t.Fatal("the filler alone holds the needle")
	}
	if len(ss.bufs[0]) != 0 {
		t.Errorf("%d bytes kept after the cut, want the continuation bytes searched", len(ss.bufs[0]))
	}
	ss.add(&same, func(int) []byte { return []byte("Z") })
	if !ss.done() {
		t.Error("a needle across a cut with no NFKC boundary is not found")
	}
	// Continuation bytes then non-starters: the non-starters wait for more
	// text, the cut is after the continuation bytes.
	ss = newSlotSearcher(n)
	tail := "̖́"
	ss.add(&same, func(int) []byte { return append(append([]byte(nil), filler...), tail...) })
	if got := string(ss.bufs[0]); got != tail {
		t.Errorf("kept %q after the cut, want the trailing non-starters", got)
	}
}

// TestEachMatch_SameAsFindAll: eachMatch finds the matches
// FindAllStringIndex finds, empty ones included, and refuses past its cap.
func TestEachMatch_SameAsFindAll(t *testing.T) {
	texts := []string{"", "a", "abc ab\nab", "xab ab", "aaa", "a\nb\n\nc", "é á ü", "\x80a\x80", strings.Repeat("ab ", 2000)}
	pats := []string{`a`, `a*`, `\b`, `\B`, `^`, `$`, `(?m)^`, `(?m)$`, `x*|b`, ``, `.`, `(?s).`, `\bab`, `ab|`, `\w+`, `[^a]*`, `(?i)AB`}
	for _, p := range pats {
		re := regexp.MustCompile(p)
		for _, s := range texts {
			want := re.FindAllStringIndex(s, -1)
			var got [][]int
			if err := eachMatch(re, s, maxStreamMatches, func(a, b int) { got = append(got, []int{a, b}) }); err != nil {
				t.Fatalf("%q on %.20q: %v", p, s, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q on %.20q: eachMatch %v, FindAllStringIndex %v", p, s, got, want)
			}
		}
	}
	var n int
	if err := eachMatch(regexp.MustCompile(`a`), "aaaa", 3, func(int, int) { n++ }); reasonOf(err) != ReasonBudgetExceeded || n != 3 {
		t.Errorf("past the cap: %v after %d matches, want budget_exceeded after 3", err, n)
	}
	if err := eachMatch(regexp.MustCompile(`a`), "aaa", 3, func(int, int) {}); err != nil {
		t.Errorf("at the cap: %v", err)
	}
}

// TestMatchStreams_ManyMatches: a stream with more matches than one batch
// marks the same runs as matching each match.
func TestMatchStreams_ManyMatches(t *testing.T) {
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td " + strings.Repeat("(ab) Tj (c) Tj ", 3*matchBatch) + "ET"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mustMatch(t, p, regexp.MustCompile(`bc`))
	if got := len(p.MarkedRuns(m.Marks)); got != 2*3*matchBatch {
		t.Errorf("%d runs marked, want every run (each pair holds a match)", got)
	}
}

// TestRewrite_ManyPatternsBudget: each pattern has its own scan budget. A
// page of about 1 MB of text in two streams (TJ gaps), rewritten with 20
// patterns and a removal that joins its neighbours into a match (a closure
// round), is accepted; one shared budget would have run out.
func TestRewrite_ManyPatternsBudget(t *testing.T) {
	var c strings.Builder
	c.WriteString("BT /F1 12 Tf 72 700 Td (SECRET-) Tj (X) Tj (1234) Tj ")
	for text := 0; text < 1<<20; text += 2 * len("public-text-shown-here") {
		c.WriteString("[(public-text-shown-here) -300 (public-text-shown-here)] TJ ")
	}
	c.WriteString("ET")
	p, _, err := analyze(t, fixture{content: c.String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ts.base) < 2 {
		t.Fatalf("%d streams, want 2 (the gaps)", len(p.ts.base))
	}
	var size int
	for _, s := range p.ts.base {
		size += len(s.text)
	}
	patterns := []*regexp.Regexp{regexp.MustCompile(`SECRET-\d+`)}
	for i := len(patterns); i < 20; i++ {
		patterns = append(patterns, regexp.MustCompile(fmt.Sprintf(`absent-%d`, i)))
	}
	x := mustMatch(t, p, regexp.MustCompile(`^X$`))
	if len(x.Marks) != 1 {
		t.Fatalf("%d marks for X", len(x.Marks))
	}
	if shared := int64(size) * int64(len(patterns)) * 2; shared <= maxMatchWork {
		t.Fatalf("20 patterns over two rounds scan %d bytes: a shared budget would not run out", shared)
	}
	out, err := p.Rewrite(RewritePlan{Marks: x.Marks, Patterns: patterns})
	if err != nil {
		t.Fatalf("Rewrite with 20 patterns on a %d-byte page: %v", size, err)
	}
	if after, _ := p.ExtractionAfter(out); strings.Contains(after, "SECRET") || strings.Contains(after, "1234") {
		t.Error("closure did not remove the joined match")
	}
	// The call as a whole stays bounded.
	mt := newMatcher(100)
	for k := 0; ; k++ {
		if err := mt.charge(k%100, maxMatchWork/2); err != nil {
			if mt.total <= maxMatchBudgets*maxMatchWork || mt.total > maxMatchBudgets*maxMatchWork+maxMatchWork/2 {
				t.Errorf("refused after %d bytes, want the overall bound %d", mt.total, maxMatchBudgets*maxMatchWork)
			}
			break
		}
	}
}

// TestFlow_OwnBudget: extraction's reading is charged to its own allowance,
// not the operator budget: a page that needs all of its operator budget
// without step 2.6 is accepted with it, and the reading's bytes are bounded.
func TestFlow_OwnBudget(t *testing.T) {
	f := fixture{content: "BT /F1 12 Tf 10 700 Td " + strings.Repeat("(x)Tj ", 2000) + "0 -20 Td [(ab) -300 (cd)] TJ ET"}
	restore := SetStreamMatchingForTest(false)
	_, off, err := analyze(t, f, nil)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	p, on, err := analyze(t, f, func(l *limits) { l.ops = off.ops })
	if err != nil {
		t.Fatalf("with the operator budget the page needs without step 2.6: %v", err)
	}
	if on.ops != off.ops || on.flow != int64(len(p.ExtractionText())) {
		t.Errorf("operator visits %d (want %d), reading bytes %d (want %d)", on.ops, off.ops, on.flow, len(p.ExtractionText()))
	}
	if _, _, err := analyze(t, f, func(l *limits) { l.flow = 1000 }); reasonOf(err) != ReasonBudgetExceeded {
		t.Errorf("reading past its allowance: %v, want budget_exceeded", err)
	}
}

func TestContentStrings_Joined(t *testing.T) {
	n := &Needles{}
	n.Add(Skeleton("SECRET-1234"), 1)
	data := []byte("BT /F1 24 Tf (SEC) Tj [(RET) -300 (-12)] TJ (34) ' ET")
	if contentStrings(data, n, nil, true) == 0 {
		t.Error("the strings of a stream joined in content order are not searched")
	}
	defer SetStreamMatchingForTest(false)()
	if contentStrings(data, n, nil, true) != 0 {
		t.Error("with stream matching off only each operator's strings are searched")
	}
}

// TestMatch_RefusalIsBudget: closure refusals are budget refusals that hold
// no document text.
func TestMatch_RefusalIsBudget(t *testing.T) {
	p, _, err := analyze(t, fixture{content: abChain(maxMatchRounds + 2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Match(regexp.MustCompile(`ab`))
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Reason != ReasonBudgetExceeded || !strings.Contains(rf.Detail, "text matching") {
		t.Errorf("refusal %v, want budget_exceeded (text matching)", err)
	}
}

// TestRewrite_BoxesAfterOpenTextObject: content that ends inside a text
// object gets an ET before the black boxes, so the output reads strictly.
func TestRewrite_BoxesAfterOpenTextObject(t *testing.T) {
	p, _, err := analyze(t, fixture{content: "BT /F1 24 Tf 72 700 Td (SECRET-1234) Tj"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mustMatch(t, p, regexp.MustCompile(`SECRET`))
	out, err := p.Rewrite(RewritePlan{Boxes: m.Rects, Marks: m.Marks})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzeContent(out.Content); err != nil {
		t.Errorf("rewritten content %q does not read strictly: %v", out.Content, err)
	}
}

func TestSameSkeleton(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"a b\nc", "abc", true},
		{"", "  ", true},
		{"abc", "abd", false},
		{"ab", "abc", false},
		{"é", "é", false}, // not ASCII: composition could differ
	} {
		if got := sameSkeleton(tc.a, []byte(tc.b)); got != tc.want {
			t.Errorf("sameSkeleton(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestTextScanner_ExtractionOnly: the font a Tf inside q…Q selects stays
// selected for extraction (it does not restore it), so only extraction's
// reading shows the needle; the pre-pass finds it there.
func TestTextScanner_ExtractionOnly(t *testing.T) {
	f := fixture{
		content: "BT /F1 24 Tf ET q BT /F2 24 Tf ET Q BT 72 700 Td (b) Tj ET",
		res:     "<< /Font << /F1 5 0 R /F2 6 0 R >> >>",
		extra: map[int]string{
			6: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 7 0 R >>",
			7: stm("", "1 begincodespacerange <00> <FF> endcodespacerange "+
				"1 beginbfchar <62> <005300450043005200450054002D0031003200330034> endbfchar"),
		},
	}
	r, err := parse.NewResolver(testPDF(f.objs()))
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := r.Pages()
	n := &Needles{}
	n.Add(Skeleton("SECRET-1234"), 1)
	hits, err := NewTextScanner(r, n).Page(pages[0])
	if err != nil {
		t.Fatal(err)
	}
	if !hits.Content {
		t.Error("the pre-pass misses a needle only extraction's reading shows")
	}
	p, _, err := analyze(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ExtractionText(); got != "SECRET-1234" {
		t.Fatalf("extraction stream %q", got)
	}
	marks, _, err := p.FindNeedles(n, nil)
	if err != nil || len(marks) != 1 {
		t.Errorf("FindNeedles = %d marks, %v; want the run", len(marks), err)
	}
}

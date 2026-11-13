package redact

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/thai-pc/fluxdocs/core/internal/textflow"
	"golang.org/x/text/unicode/norm"
)

// stream.go is matching across operators (docs/design/redaction-cross-
// operator.md, step 2.6): the page's text is read as one stream per reading
// policy — extraction's own reading (textflow, the same state machine as
// ExtractText), and one per reading slot of the runs (a font candidate's
// /ToUnicode, /Differences or one-byte reading, or the raw bytes as Latin-1)
// joined with extraction's line breaks — in content order, and an index map
// gives the runs each byte belongs to. A match or a needle anywhere in a
// stream marks every run it touches; the runs are removed whole (Phase 1
// granularity). Removing runs joins their neighbours, so the streams are
// assembled again without them and matched again, to a fixed point.

// The reading slots of a run (§3.3 of the note): textSlot+k is candidate font
// k's Text reading (strings at any TJ depth), altSlot+k its /Differences
// glyph-name reading, byteSlot+k its one-byte-per-code reading, latinSlot the
// raw bytes as Latin-1. A run with fewer candidates, or without the reading,
// uses the fallback: text[k] → text[0], alt[k] and byte[k] → text[k].
const (
	textSlot  = 0
	altSlot   = maxFontSet
	byteSlot  = 2 * maxFontSet
	latinSlot = 3 * maxFontSet
	numSlots  = 3*maxFontSet + 1
)

// Stream matching bounds (§7.1 of the note). Past any of them the page is
// refused (budget_exceeded, "text matching"), never matched partly: a match
// that is not found would leave text on the page (Q7).
const (
	// maxStreamMatches is the most matches of one pattern in one stream in
	// one round (the span cap: only crafted pages get near it).
	maxStreamMatches = 4 << 20
	// maxMatchRounds is how many closure rounds may remove more runs (§5.3).
	maxMatchRounds = 4
	// maxMatchWork is the stream bytes one Match, Rewrite or FindNeedles call
	// may scan for one pattern, or for its needles, over all streams and
	// rounds: each pattern and the needle search have their own, so a page
	// redacted with many patterns has room for each. It is separate from the
	// operator budget, so a page RedactArea accepts is not refused for
	// matching it does not do.
	maxMatchWork = 64 << 20
	// maxMatchBudgets bounds the call as a whole: all its patterns and its
	// needles together scan at most maxMatchBudgets × maxMatchWork bytes
	// (1 GiB), however many patterns the page has.
	maxMatchBudgets = 16
	// matchBatch is the most matches matchStreams collects in one slice: a
	// stream with more is matched again by eachMatch, which does not collect
	// them.
	matchBatch = 1024
	// maxNeedleRunes is the longest needle searched for, in runes of its
	// skeleton: a longer one is truncated to its first maxNeedleRunes runes
	// (Needles.Add). Every occurrence of the full needle holds the prefix, so
	// truncation only adds hits; it bounds the search's overlap and its
	// worst case.
	maxNeedleRunes = 1024
)

// testHooks are set by tests only: noStreams turns step 2.6 off — pages are
// read without extraction's reading or streams, and Match, Rewrite,
// FindNeedles, the pre-pass and the verifier's byte search match each run
// alone, as before — for the DoS gates' reference.
var testHooks struct{ noStreams bool }

// SetStreamMatchingForTest turns stream matching off (on false) or back on,
// for the DoS gates that measure its cost against matching each run alone.
// It returns a func that restores the previous setting. Tests only: it is
// not safe to call while redaction runs.
func SetStreamMatchingForTest(on bool) (restore func()) {
	was := testHooks.noStreams
	testHooks.noStreams = !on
	return func() { testHooks.noStreams = was }
}

// piece is a maximal range of a stream's text owned by one run (owner ≥ 0,
// an index into Page.runs) or by none (a line break, -1). Pieces are
// contiguous: piece i covers [start, next start).
type piece struct {
	start, owner int32
}

// stream is one reading of the page's text in content order with its index
// map (§4 of the note).
type stream struct {
	text   string
	pieces []piece
}

// owners calls f with the owner run of every piece the bytes [a, b) touch.
func (s *stream) owners(a, b int, f func(run int)) {
	i := sort.Search(len(s.pieces), func(i int) bool { return int(s.pieces[i].start) > a }) - 1
	if i < 0 {
		i = 0
	}
	for ; i < len(s.pieces) && int(s.pieces[i].start) < b; i++ {
		if o := s.pieces[i].owner; o >= 0 {
			f(int(o))
		}
	}
}

// pieceText is the text of piece i.
func (s *stream) pieceText(i int) string {
	end := len(s.text)
	if i+1 < len(s.pieces) {
		end = int(s.pieces[i+1].start)
	}
	return s.text[s.pieces[i].start:end]
}

// pageStreams is what a page model keeps to read its text as streams: the
// recorded extraction reading, the slot classes, and the streams of the
// whole page.
type pageStreams struct {
	flowText string
	events   []textflow.Event
	part     slotPartition // classes of slots whose streams are equal
	base     []stream      // assemble(keep every run), built once
}

// eventSize is the size of a textflow.Event (TestStreams_SizeCounted checks
// it).
const eventSize = 16

// size is the memory the streams hold (Page.Size): the recorded reading and
// its events, and each stream with its index map. The first base stream is
// the extraction stream, whose text is flowText itself (analyzePage builds
// it from the Flow's own pieces, or it is empty): it is not counted twice.
func (ps *pageStreams) size() int64 {
	n := int64(len(ps.flowText)) + eventSize*int64(len(ps.events))
	for i, s := range ps.base {
		n += 48 + 8*int64(len(s.pieces))
		if i > 0 {
			n += int64(len(s.text))
		}
	}
	return n
}

// slotPartition groups the reading slots of a page (or unit) into classes of
// slots whose streams are equal: every run so far has the same reading in
// each slot of a class. A run whose readings differ splits classes; classes
// never merge.
type slotPartition struct {
	class    [numSlots]uint8
	n        int // number of classes
	last     [numSlots]uint8
	haveLast bool
	repCache []int // reps, until the next split
}

// refine splits the classes by one run's slot values v (equal values: equal
// readings). split, when set, is called for each new class with the class
// it split from, before the run's readings are added.
func (sp *slotPartition) refine(v *[numSlots]uint8, split func(newClass, from int)) {
	if sp.n == 0 {
		sp.n = 1
	}
	if sp.haveLast && *v == sp.last || sp.n == numSlots {
		return
	}
	sp.last, sp.haveLast = *v, true
	// For each class, the first value seen keeps the class; each other value
	// gets a new class.
	type assign struct {
		from  uint8
		val   uint8
		class uint8
	}
	var first [numSlots]int16
	for i := range first {
		first[i] = -1
	}
	var extra [numSlots]assign
	ne := 0
	for s := 0; s < numSlots; s++ {
		c := sp.class[s]
		switch {
		case first[c] < 0:
			first[c] = int16(v[s])
		case uint8(first[c]) == v[s]:
		default:
			found := false
			for _, a := range extra[:ne] {
				if a.from == c && a.val == v[s] {
					sp.class[s] = a.class
					found = true
					break
				}
			}
			if !found {
				nc := uint8(sp.n)
				sp.n++
				sp.repCache = nil
				extra[ne] = assign{from: c, val: v[s], class: nc}
				ne++
				if split != nil {
					split(int(nc), int(c))
				}
				sp.class[s] = nc
			}
		}
	}
}

// reps returns the lowest slot of each class, in class order. The slice is
// shared until the next split: it must not be modified.
func (sp *slotPartition) reps() []int {
	if sp.repCache != nil {
		return sp.repCache
	}
	n := max(sp.n, 1)
	out := make([]int, n)
	for i := range out {
		out[i] = -1
	}
	for s := 0; s < numSlots; s++ {
		if c := sp.class[s]; out[c] < 0 {
			out[c] = s
		}
	}
	sp.repCache = out
	return out
}

// slotBuffer is the reading buffer slot s uses in a run with nf candidate
// fonts (see show's buffer layout: 3 per font, then extraction, then
// Latin-1), after the fallbacks.
func slotBuffer(s, nf, nRead int, used []bool) int {
	switch {
	case s == latinSlot:
		return nRead - 1
	case s >= byteSlot:
		k := s - byteSlot
		if k < nf && used[3*k+2] {
			return 3*k + 2
		}
		return slotBuffer(textSlot+k, nf, nRead, used)
	case s >= altSlot:
		k := s - altSlot
		if k < nf && used[3*k+1] {
			return 3*k + 1
		}
		return slotBuffer(textSlot+k, nf, nRead, used)
	default:
		if s < nf {
			return 3 * s
		}
		return 0
	}
}

// slots returns, for each slot, the first used buffer among the first n
// equal to the buffer the slot reads (readBuffers.first), for a run with nf
// candidate fonts: equal values, equal readings.
func (r *readBuffers) slots(n, nf int) [numSlots]uint8 {
	var v [numSlots]uint8
	// The common run: every reading is the same text, so every slot reads
	// buffer 0.
	same := true
	for i := 1; i < n && same; i++ {
		same = !r.used[i] || r.first(i) == 0
	}
	if same {
		return v
	}
	var canon [3*maxFontSet + 2]int8
	for i := range canon[:n] {
		canon[i] = -1
	}
	for s := 0; s < numSlots; s++ {
		b := slotBuffer(s, nf, n, r.used)
		if canon[b] < 0 {
			canon[b] = int8(r.first(b))
		}
		v[s] = uint8(canon[b])
	}
	return v
}

// textsAndSlots returns the distinct readings among the first n used buffers
// (as texts) and, for each slot, the index in them of the reading it uses.
func (r *readBuffers) textsAndSlots(n, nf int) ([]string, [numSlots]uint8) {
	texts := r.texts(n)
	var pos [3*maxFontSet + 2]uint8
	k := uint8(0)
	for i := 0; i < n; i++ {
		if r.used[i] && r.first(i) == i {
			pos[i] = k
			k++
		}
	}
	v := r.slots(n, nf)
	for s := range v {
		v[s] = pos[v[s]]
	}
	return texts, v
}

// streamsOn reports whether stream matching is on and the page has streams.
func (p *Page) streamsOn() bool { return !testHooks.noStreams && p.ts != nil }

// assemble returns the page's streams with every run keep rejects left out
// (nil keeps every run): what a reader of each policy reads from the page
// once those runs are removed (property P3 for the extraction stream). The
// first stream is extraction's; streams equal to an earlier one (text and
// owners) are not repeated.
func (p *Page) assemble(keep func(run int) bool) ([]stream, error) {
	if keep == nil && p.ts.base != nil {
		return p.ts.base, nil
	}
	return p.ts.assemble(p.runs, keep)
}

// assemble builds the streams of runs (see Page.assemble).
func (ps *pageStreams) assemble(runs []textRun, keep func(run int) bool) ([]stream, error) {
	return ps.build(runs, keep, nil)
}

// build is assemble; flowPieces, when set (keep nil), are the pieces of the
// recorded reading itself, used as the extraction stream instead of a
// replay.
func (ps *pageStreams) build(runs []textRun, keep func(run int) bool, flowPieces []textflow.Piece) ([]stream, error) {
	var k func(int32) bool
	if keep != nil {
		k = func(o int32) bool { return keep(int(o)) }
	}
	var out []stream
	add := func(text string, pieces []textflow.Piece) error {
		if len(text) > math.MaxInt32 {
			return budget("text matching")
		}
		s := stream{text: text, pieces: make([]piece, len(pieces))}
		for i, pc := range pieces {
			s.pieces[i] = piece{start: int32(pc.Start), owner: pc.Owner}
		}
		for _, o := range out {
			if o.equal(&s) {
				return nil
			}
		}
		out = append(out, s)
		return nil
	}
	if flowPieces != nil && keep == nil {
		if err := add(ps.flowText, flowPieces); err != nil {
			return nil, err
		}
	} else if err := add(textflow.Replay(ps.flowText, ps.events, k, nil)); err != nil {
		return nil, err
	}
	for _, rep := range ps.part.reps() {
		shown := func(o int32) string {
			r := &runs[o]
			return r.texts[r.slot[rep]]
		}
		if err := add(textflow.Replay(ps.flowText, ps.events, k, shown)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// equal reports whether two streams have the same text and owners.
func (s *stream) equal(o *stream) bool {
	if s.text != o.text || len(s.pieces) != len(o.pieces) {
		return false
	}
	for i := range s.pieces {
		if s.pieces[i] != o.pieces[i] {
			return false
		}
	}
	return true
}

// bits is a set of run indices.
type bits []uint64

func newBits(n int) bits      { return make(bits, (n+63)/64) }
func (b bits) has(i int) bool { return b[i/64]&(1<<(i%64)) != 0 }
func (b bits) set(i int)      { b[i/64] |= 1 << (i % 64) }
func (b bits) clone() bits    { return append(bits(nil), b...) }

// kept is the keep function of assemble that leaves out the runs in b.
func (b bits) kept() func(int) bool { return func(i int) bool { return !b.has(i) } }

// matcher is the state of one Match, Rewrite or FindNeedles call's stream
// matching: its scan budgets. Pattern i of the call charges budget i, the
// needle search the last one; each holds maxMatchWork bytes and all of them
// together limit (maxMatchWork × min(patterns+1, maxMatchBudgets)).
type matcher struct {
	work  []int64
	total int64
	limit int64
}

// newMatcher returns the matcher of a call that matches patterns patterns
// (and needles).
func newMatcher(patterns int) *matcher {
	n := patterns + 1
	return &matcher{work: make([]int64, n), limit: maxMatchWork * int64(min(n, maxMatchBudgets))}
}

// needleKey is the budget the needle search charges.
func (m *matcher) needleKey() int { return len(m.work) - 1 }

// charge counts n stream bytes scanned for budget key.
func (m *matcher) charge(key, n int) error {
	m.work[key] += int64(n)
	m.total += int64(n)
	if m.work[key] > maxMatchWork || m.total > m.limit {
		return budget("text matching")
	}
	return nil
}

// matchStreams runs pattern (budget key) over each stream and calls hit with
// the owner runs of every non-empty match, and needle (when set) with its
// text. Returns a *Refusal (budget_exceeded) past maxStreamMatches matches in
// a stream or the call's scan budget; hit and needle may then have been
// called for part of the matches, which the caller must discard.
func (m *matcher) matchStreams(key int, streams []stream, pattern *regexp.Regexp, hit func(run int), needle func(s string)) error {
	for i := range streams {
		s := &streams[i]
		if s.text == "" {
			continue
		}
		if err := m.charge(key, len(s.text)); err != nil {
			return err
		}
		found := func(a, b int) {
			if a == b {
				return
			}
			s.owners(a, b, hit)
			if needle != nil {
				needle(s.text[a:b])
			}
		}
		// Most streams have few matches: one small slice holds them all.
		if idx := pattern.FindAllStringIndex(s.text, matchBatch); len(idx) < matchBatch {
			for _, x := range idx {
				found(x[0], x[1])
			}
			continue
		}
		if err := eachMatch(pattern, s.text, maxStreamMatches, found); err != nil {
			return err
		}
	}
	return nil
}

// ErrInternal marks a failure that is a bug in the engine, never a property
// of the file (core reports it as ErrInternal).
var ErrInternal = errors.New("redaction internal error")

// eachMatch calls f with the bounds of every match of re in s, in order —
// the matches FindAllStringIndex(s, -1) returns, empty ones included —
// without collecting them. Past max matches it stops calling f and returns a
// *Refusal (budget_exceeded); the scan still runs to the end of s (its bytes
// are charged), keeping nothing per match. It allocates a copy of s and the
// unmatched text (at most len(s) bytes), which it drops.
//
// The regexp package has no search from an offset that sees the text before
// it (\b, ^ and (?m)^ depend on the preceding rune), so the matches come from
// ReplaceAllFunc, which finds the same ones FindAllStringIndex does (both
// resume at a match's end, step one rune past an empty match, and skip an
// empty match at the end of the previous one) and hands each as a subslice of
// its input: its start is where its capacity says. That is checked by
// address, and a match that is not a subslice is an error (never a guess).
func eachMatch(re *regexp.Regexp, s string, max int, f func(a, b int)) error {
	src := []byte(s)
	n := 0
	foreign := false
	re.ReplaceAllFunc(src, func(m []byte) []byte {
		n++
		if n > max || foreign {
			return nil
		}
		a := cap(src) - cap(m)
		if a < 0 || a+len(m) > len(src) || len(m) > 0 && &src[a] != &m[0] {
			foreign = true
			return nil
		}
		f(a, a+len(m))
		return nil
	})
	switch {
	case foreign:
		return fmt.Errorf("%w: text matching: a match is not a subslice of the stream", ErrInternal)
	case n > max:
		return budget("text matching")
	}
	return nil
}

// needleStreams searches each stream's skeleton for the needles and calls
// hit with every run a needle occurrence touches and the needle's tags. The
// skeleton is built per chunk of pieces that ends on an NFKC boundary, so it
// equals the skeleton of the whole stream; an occurrence maps to the owners
// of every chunk it touches (over-removal where a boundary composes, never
// under). White space is not in the skeleton, so a needle matches across
// line breaks (Q3).
func (m *matcher) needleStreams(streams []stream, n *Needles, hit func(run int, tags uint8)) error {
	if n.Len() == 0 {
		return nil
	}
	for i := range streams {
		s := &streams[i]
		if len(s.text) < MinNeedle {
			continue
		}
		if err := m.charge(m.needleKey(), len(s.text)); err != nil {
			return err
		}
		sk := skeletonChunks(s)
		n.FindSpans(sk.hay, func(a, b int, tags uint8) {
			c := sort.Search(len(sk.start), func(c int) bool { return int(sk.start[c]) > a }) - 1
			for ; c >= 0 && c < len(sk.start) && int(sk.start[c]) < b; c++ {
				for pi := sk.first[c]; pi < sk.first[c+1]; pi++ {
					if o := s.pieces[pi].owner; o >= 0 {
						hit(int(o), tags)
					}
				}
			}
		})
	}
	return nil
}

// chunked is a stream's skeleton with, per chunk, where its skeleton starts
// in hay and its first piece (first has one more entry: the end).
type chunked struct {
	hay   string
	start []int32
	first []int32
}

// skeletonChunks builds the skeleton of s chunk by chunk. A chunk starts at a
// piece whose text starts on an NFKC boundary (it cannot compose with what
// precedes it).
func skeletonChunks(s *stream) chunked {
	var out chunked
	hay := make([]byte, 0, len(s.text))
	from := 0 // first piece of the current chunk
	flush := func(to int) {
		if to <= from {
			return
		}
		a := int(s.pieces[from].start)
		b := len(s.text)
		if to < len(s.pieces) {
			b = int(s.pieces[to].start)
		}
		out.start = append(out.start, int32(len(hay)))
		out.first = append(out.first, int32(from))
		hay = append(hay, Skeleton(s.text[a:b])...)
		from = to
	}
	for i := 1; i < len(s.pieces); i++ {
		if t := s.pieceText(i); t != "" && (t[0] < utf8.RuneSelf || norm.NFKC.FirstBoundaryInString(t) == 0) {
			flush(i)
		}
	}
	flush(len(s.pieces))
	out.first = append(out.first, int32(len(s.pieces)))
	out.hay = string(hay)
	return out
}

// closureRound assembles the streams without the runs removed holds and
// returns the runs that a match of any pattern, or an occurrence of any
// needle, touches in them (all kept by construction): removing runs must not
// join their neighbours into a new match (§5.3).
func (p *Page) closureRound(m *matcher, removed bits, patterns []*regexp.Regexp, needles *Needles) ([]int, error) {
	streams, err := p.assemble(removed.kept())
	if err != nil {
		return nil, err
	}
	found := newBits(len(p.runs))
	var out []int
	add := func(run int) {
		if !found.has(run) && !removed.has(run) {
			found.set(run)
			out = append(out, run)
		}
	}
	for k, re := range patterns {
		if err := m.matchStreams(k, streams, re, add, nil); err != nil {
			return nil, err
		}
	}
	if err := m.needleStreams(streams, needles, func(run int, _ uint8) { add(run) }); err != nil {
		return nil, err
	}
	sort.Ints(out)
	return out, nil
}

// slotSearcher searches per-slot streams for needles without keeping them
// (TextScanner, §5.2 of the note): each class of slots appends its runs'
// readings to a buffer, searched in chunks that end on an NFKC boundary,
// with an overlap of the longest needle's skeleton (less one byte) carried
// into the next chunk. When a class splits, the new class starts from a copy
// of its parent's unsearched buffer and overlap: what precedes it was
// searched as the parent's, which it equals.
type slotSearcher struct {
	n    *Needles
	part slotPartition
	bufs [][]byte // per class: raw text not yet searched
	tail []string // per class: the overlap, in skeleton form
	hit  bool
}

// searchChunk is the raw text a class buffers before it is searched.
const searchChunk = 1 << 20

func newSlotSearcher(n *Needles) *slotSearcher {
	return &slotSearcher{n: n, bufs: make([][]byte, 1), tail: make([]string, 1)}
}

// add appends one run's readings: reading(s) is slot s's (v gives equal
// values for equal readings).
func (ss *slotSearcher) add(v *[numSlots]uint8, reading func(slot int) []byte) {
	if ss.hit {
		return
	}
	ss.part.refine(v, func(nc, from int) {
		for len(ss.bufs) <= nc {
			ss.bufs = append(ss.bufs, nil)
			ss.tail = append(ss.tail, "")
		}
		ss.bufs[nc] = append([]byte(nil), ss.bufs[from]...)
		ss.tail[nc] = ss.tail[from]
	})
	for c, rep := range ss.part.reps() {
		ss.bufs[c] = append(ss.bufs[c], reading(rep)...)
		if len(ss.bufs[c]) >= searchChunk {
			ss.flush(c, false)
		}
	}
}

// flush searches class c's buffer up to its last NFKC boundary (everything
// when final) and keeps the rest and the overlap.
func (ss *slotSearcher) flush(c int, final bool) {
	buf := ss.bufs[c]
	cut := len(buf)
	if !final {
		cut = norm.NFKC.LastBoundary(buf)
		if cut <= 0 {
			// No boundary in a whole chunk. Past 30 non-starters NFKC has one
			// (stream-safe text), so only invalid UTF-8 gets here: continuation
			// bytes, perhaps ending in a few non-starters. Cut after the
			// leading continuation bytes (each its own segment, read as
			// U+FFFD), so the skeletons on either side join to the skeleton of
			// the whole; the overlap carries a needle across the cut, and the
			// rest waits for more text.
			cut = 0
			for cut < len(buf) && !utf8.RuneStart(buf[cut]) {
				cut++
			}
		}
	}
	hay := ss.tail[c] + Skeleton(string(buf[:cut]))
	if ss.n.Find(hay) != 0 {
		ss.hit = true
	}
	keep := max(ss.n.maxLen-1, 0)
	if len(hay) > keep {
		hay = hay[len(hay)-keep:]
	}
	ss.tail[c] = hay
	ss.bufs[c] = append(buf[:0], buf[cut:]...)
}

// done searches what is left and reports whether any slot stream held a
// needle.
func (ss *slotSearcher) done() bool {
	if ss.hit {
		return true
	}
	for c := range ss.bufs {
		if len(ss.bufs[c]) > 0 {
			ss.flush(c, true)
		}
	}
	return ss.hit
}

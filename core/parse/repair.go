package parse

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// repair.go rebuilds the object table of a file whose cross-reference data
// cannot be used, by scanning the file body for "N G obj" headers (ISO
// 32000-1 §7.3.10, §7.5.3) — the object scan of
// docs/design/xref-reconstruction.md §3. It runs only when the strict open
// fails (core/open.go), never for a file that opens from its own xref.
//
// Two linear passes over the bytes: pass 1 indexes the header, endstream and
// trailer keyword candidates; pass 2 walks the headers in file order, parses
// each object's dictionary only (iparser.dictOnly, bounded by the next
// header) and masks the headers inside stream data. The last definition of a
// number in file order wins (§7.5.6). The trailer is synthesized from the
// surviving trailer dictionaries and cross-reference stream dictionaries; a
// catalog no trailer names is found by a bounded search. An encryption
// dictionary is never lost silently (§3.6): a file that had one either
// decrypts or fails as encrypted.
//
// Objects inside object streams are not expanded yet (design §3.4, Phase 3).
// Until they are, a scan whose table holds a winning /Type /ObjStm fails: the
// objects inside would resolve to null, and the document would open with
// pages or resources silently missing.

// Bounds of the object scan (SECURITY.md §2; design §4). Hitting one fails the
// repair, and the error names it: a partial table would make a partial
// document look complete.
const (
	// maxRepairCandidates caps the header, endstream and trailer keyword
	// candidates pass 1 records together, before any object is parsed: at
	// 16 bytes per header that is at most 64 MiB of index. "1 0 obj\n"
	// repeated is 8 bytes per candidate.
	maxRepairCandidates = 4 << 20
	// maxRepairObjects caps the distinct object numbers of the rebuilt table
	// (≈ 65 bytes per map entry, ≈ 68 MiB at the cap; = maxPages).
	maxRepairObjects = 1 << 20
	// maxRepairTrailers caps the trailer candidates considered (the last in
	// file order), and maxRepairCatalogs the catalog candidates parsed.
	maxRepairTrailers = 64
	maxRepairCatalogs = 64
	// repairLookBack is how far behind an "obj" keyword the "N G" digits and
	// white-space are looked for: constant work per hit keeps pass 1 linear.
	repairLookBack = 32
	// maxRepairObjectNumber is the largest object number (ISO 32000-1 Annex
	// C) and maxRepairGeneration the largest generation (§7.5.4) a header
	// may carry; larger values are not candidates.
	maxRepairObjectNumber = 8388607
	maxRepairGeneration   = 65535
)

// errRepairLimit reports an object scan over one of its bounds.
var errRepairLimit = fmt.Errorf("%w: repair limit exceeded", errLex)

// ErrEncryptionLost reports an object scan that found signs of an encryption
// dictionary it cannot use (design §3.6, fail closed): one that does not
// parse, lies inside stream data or is shadowed by a later definition, or a
// trailer naming /Encrypt that does not parse. The file may be encrypted, and
// opening it as plaintext would show ciphertext as content; core.OpenBytes
// reports it as ErrEncryptedDocument. It wraps errLex.
var ErrEncryptionLost = fmt.Errorf("%w: repair cannot read the encryption dictionary", errLex)

// RepairInfo describes how a Resolver's object table was rebuilt (see
// Resolver.Repair). It holds counts and flags only, never document bytes.
type RepairInfo struct {
	// HeaderOffset is the number of bytes before "%PDF-" when the table was
	// read from the file's own cross-reference data with every offset moved
	// by it (NewResolverShifted); 0 for an object scan.
	HeaderOffset int
	// Scanned is set when the table was rebuilt by the object scan
	// (NewRepairedResolver).
	Scanned bool
	// Candidates is the number of "N G obj" headers pass 1 found, including
	// those later masked as stream data (object scan only).
	Candidates int
	// Objects is the number of objects in the rebuilt table.
	Objects int
	// Duplicates is the number of definitions shadowed by a later one of the
	// same number (object scan only).
	Duplicates int
	// CatalogFromScan is set when no trailer named a usable /Root and the
	// catalog was found by searching the objects.
	CatalogFromScan bool
	// Truncated is set when the file ends inside an object or a stream.
	Truncated bool
}

// repairHeader is one "N G obj" candidate: the offset of N, packed in 16
// bytes.
type repairHeader struct {
	off int64
	num int32
	gen uint16
}

// repairCand is one trailer candidate: a dictionary after a "trailer" keyword
// or a cross-reference stream's dictionary, at its file offset.
type repairCand struct {
	off  int
	dict Dict
}

// repairScan is the state of one object scan.
type repairScan struct {
	data       []byte
	headers    []repairHeader // pass 1, in file order
	endstreams []int          // offsets of "endstream", in file order
	trailers   []int          // offsets of "trailer" keywords, in file order
	masked     []bool         // per header: inside the data of an earlier stream
	failed     []bool         // per kept header: its value does not parse on its span
	streamData [][2]int       // [start, end) of each stream whose end was found, in file order
	xrefDicts  []repairCand   // dictionaries of /Type /XRef streams (the last maxRepairTrailers)
	objStms    []int          // header indices of /Type /ObjStm streams
	byNum      []int32        // header indices sorted by (num, off), built on first use
	truncated  bool
	lengths    map[int]int64     // indirectLength results by object number; -1 for none
	pairs      []dictPair        // head's scratch stack of dictionary entries (iparser.pairs)
	names      map[string]string // head's interned names (iparser.names)

	entries map[int]XrefEntry
	winners []int // header index of each winning definition, last in file order first
	peeked  map[int]peekResult
}

// peekResult is a memoized dictionary read of one winning object.
type peekResult struct {
	dict Dict
	ok   bool
}

// NewRepairedResolver rebuilds the object table of data by scanning it for
// indirect objects (see the file comment) and returns a Resolver over it, with
// a synthesized trailer (/Root, /Info, /ID, /Encrypt). The Resolver is
// otherwise an ordinary one: objects are loaded lazily at their scanned
// offsets, through the same parser, caches and caps. Repair reports the scan.
//
// Returns a wrapped errLex when no document catalog can be found, or a
// wrapped errRepairLimit naming the bound a hostile or oversized file hit.
func NewRepairedResolver(data []byte) (*Resolver, error) {
	s := &repairScan{data: data, names: make(map[string]string)}
	if err := s.index(); err != nil {
		return nil, err
	}
	s.objects()
	if err := s.table(); err != nil {
		return nil, err
	}
	trailer, fromScan, err := s.trailer()
	if err != nil {
		return nil, err
	}
	r := newResolver(data, &Xref{Entries: s.entries, Trailer: trailer})
	r.repair = &RepairInfo{
		Scanned:         true,
		Candidates:      len(s.headers),
		Objects:         len(s.entries),
		Duplicates:      s.kept() - len(s.entries),
		CatalogFromScan: fromScan,
		Truncated:       s.truncated,
	}
	return r, nil
}

// index is pass 1: one bytes.Index sweep per keyword ("obj", "stream" for
// endstream, "trailer"), each linear, with constant work per hit.
func (s *repairScan) index() error {
	data := s.data
	// Size the header index once: every header has an "obj" keyword, so this
	// count (which also counts each endobj) bounds it. Growing by appends
	// instead left up to half again its size as garbage at the cap (design §4).
	s.headers = make([]repairHeader, 0, min(bytes.Count(data, []byte("obj")), maxRepairCandidates))
	total := 0
	claim := func() error {
		total++
		if total > maxRepairCandidates {
			return fmt.Errorf("%w: more than %d object, endstream and trailer candidates (maxRepairCandidates)",
				errRepairLimit, maxRepairCandidates)
		}
		return nil
	}
	for i := 0; i < len(data); {
		k := bytes.Index(data[i:], []byte("obj"))
		if k < 0 {
			break
		}
		at := i + k
		i = at + 3
		if i < len(data) && isRegular(data[i]) {
			continue // "objx": another keyword
		}
		h, ok := headerBehind(data, at)
		if !ok {
			continue
		}
		if err := claim(); err != nil {
			return err
		}
		s.headers = append(s.headers, h)
	}
	for i := 0; i < len(data); {
		k := bytes.Index(data[i:], []byte("endstream"))
		if k < 0 {
			break
		}
		if err := claim(); err != nil {
			return err
		}
		s.endstreams = append(s.endstreams, i+k)
		i += k + len("endstream")
	}
	for i := 0; i < len(data); {
		k := bytes.Index(data[i:], []byte("trailer"))
		if k < 0 {
			break
		}
		at := i + k
		i = at + len("trailer")
		// A keyword, not part of a name (/trailer) or of a longer word.
		if at > 0 && (isRegular(data[at-1]) || data[at-1] == '/') {
			continue
		}
		if i < len(data) && isRegular(data[i]) {
			continue
		}
		if err := claim(); err != nil {
			return err
		}
		s.trailers = append(s.trailers, at)
	}
	return nil
}

// headerBehind matches "N G obj" ending at the "obj" keyword at, within
// repairLookBack bytes: white-space, an unsigned integer G ≤ 65535,
// white-space, and an unsigned integer N in [1, 8388607] preceded by the start
// of the data, white-space or a delimiter. A candidate on a line that a '%'
// comments out (within the window) is rejected. "endobj" fails because its
// "obj" follows a 'd'.
//
// Returns the header at N's offset, or false.
func headerBehind(data []byte, at int) (repairHeader, bool) {
	lo := max(at-repairLookBack, 0)
	j := at - 1
	skipWS := func() bool {
		if j < lo || !isWhitespace(data[j]) {
			return false
		}
		for j >= lo && isWhitespace(data[j]) {
			j--
		}
		return true
	}
	digits := func(limit int64) (int64, bool) {
		end := j
		for j >= lo && data[j] >= '0' && data[j] <= '9' {
			j--
		}
		if j == end {
			return 0, false
		}
		var v int64
		for _, c := range data[j+1 : end+1] {
			v = v*10 + int64(c-'0')
			if v > limit {
				return 0, false
			}
		}
		return v, true
	}
	if !skipWS() {
		return repairHeader{}, false
	}
	gen, ok := digits(maxRepairGeneration)
	if !ok || !skipWS() {
		return repairHeader{}, false
	}
	num, ok := digits(maxRepairObjectNumber)
	if !ok || num < 1 {
		return repairHeader{}, false
	}
	// j is the byte before N (one past the window at most: constant work).
	if j >= 0 && !isWhitespace(data[j]) && !isDelimiter(data[j]) {
		return repairHeader{}, false
	}
	for c := j; c >= lo; c-- {
		if data[c] == '\n' || data[c] == '\r' {
			break
		}
		if data[c] == '%' {
			return repairHeader{}, false // a comment (§7.2.3)
		}
	}
	return repairHeader{off: int64(j + 1), num: int32(num), gen: uint16(gen)}, true
}

// spanEnd returns where header k's span ends: the next raw header candidate,
// or the end of the data. Spans are disjoint, so parses bounded by them cost
// O(file size) together.
func (s *repairScan) spanEnd(k int) int {
	if k+1 < len(s.headers) {
		return int(s.headers[k+1].off)
	}
	return len(s.data)
}

// head parses header k's object value on its span only, dictionary only.
//
// Returns the value and the offset of its stream data (-1 for none), or an
// error when the bounded parse fails.
func (s *repairScan) head(k int) (Object, int, error) {
	p := &iparser{data: s.data[:s.spanEnd(k)], pos: int(s.headers[k].off), dictOnly: true, streamAt: -1, pairs: &s.pairs, names: s.names}
	for i := 0; i < 3; i++ { // N G obj, matched by pass 1
		_, ni, err := p.next(p.pos)
		if err != nil {
			return nil, -1, err
		}
		p.pos = ni
	}
	obj, err := p.parseValue()
	if err != nil {
		return nil, -1, err
	}
	return obj, p.streamAt, nil
}

// objects is pass 2: it walks the headers in file order with one mask-end
// cursor. A header before the mask end lies inside an earlier stream's data
// and is masked. For a kept header followed by stream data, the data's end is
// found by (a) a direct /Length that endstream follows, (b) an indirect
// /Length whose object is "N G obj <integer> endobj", or (c) the first
// endstream after the data (a pointer that only moves forward). A stream with
// no endstream after its data masks nothing: the file is truncated there, and
// the headers after it stay candidates.
func (s *repairScan) objects() {
	s.masked = make([]bool, len(s.headers))
	s.failed = make([]bool, len(s.headers))
	maskEnd := 0
	es := 0
	for k, h := range s.headers {
		if int(h.off) < maskEnd {
			s.masked[k] = true
			continue
		}
		obj, start, err := s.head(k)
		if err != nil {
			s.failed[k] = true
			if k == len(s.headers)-1 {
				s.truncated = true // the last object does not parse to its end
			}
			continue
		}
		if start < 0 {
			continue
		}
		d, _ := obj.(Dict)
		t, _ := d.GetName("Type")
		if t == "ObjStm" {
			s.objStms = append(s.objStms, k)
		}
		if t == "XRef" {
			s.xrefDicts = append(s.xrefDicts, repairCand{off: int(h.off), dict: d})
			if len(s.xrefDicts) > 2*maxRepairTrailers {
				s.xrefDicts = append(s.xrefDicts[:0], s.xrefDicts[len(s.xrefDicts)-maxRepairTrailers:]...)
			}
		}
		end := s.lengthEnd(d, start)
		if end < 0 {
			for es < len(s.endstreams) && s.endstreams[es] < start {
				es++
			}
			if es < len(s.endstreams) {
				end = s.endstreams[es] + len("endstream")
			}
		}
		if end < 0 {
			s.truncated = true
			continue
		}
		maskEnd = end
		s.streamData = append(s.streamData, [2]int{int(h.off), end})
	}
}

// lengthEnd returns the end of the endstream keyword when the stream whose
// data starts at start has a usable /Length (rules (a) and (b)), else -1.
func (s *repairScan) lengthEnd(d Dict, start int) int {
	n := int64(-1)
	if v, ok := d.GetInt("Length"); ok {
		n = int64(v)
	} else if ref, ok := d["Length"].(Reference); ok {
		n = s.indirectLength(ref)
	}
	if n < 0 || n > int64(len(s.data)-start) {
		return -1
	}
	probe := skipStreamEOL(s.data, start+int(n))
	if !bytes.HasPrefix(s.data[probe:], []byte("endstream")) {
		return -1
	}
	return probe + len("endstream")
}

// indirectLength resolves an indirect /Length against the pass-1 index: the
// last header with ref's number must be written exactly "N G obj <integer>
// endobj" with ref's generation (the Resolver.readDirectLength check). Only
// that last header is tried, its tokens are read on its span only, and the
// read is memoized per object number, failures included: N streams naming
// one object cost one read of its span, not N (each lookup is then O(log n)).
// Returns -1 for none.
func (s *repairScan) indirectLength(ref Reference) int64 {
	if s.byNum == nil {
		s.byNum = make([]int32, len(s.headers))
		for i := range s.byNum {
			s.byNum[i] = int32(i)
		}
		sort.SliceStable(s.byNum, func(a, b int) bool {
			return s.headers[s.byNum[a]].num < s.headers[s.byNum[b]].num
		})
	}
	i := sort.Search(len(s.byNum), func(i int) bool { return int(s.headers[s.byNum[i]].num) > ref.Number })
	if i == 0 {
		return -1
	}
	k := int(s.byNum[i-1])
	h := s.headers[k]
	if int(h.num) != ref.Number || int(h.gen) != ref.Generation {
		return -1
	}
	if n, ok := s.lengths[ref.Number]; ok {
		return n
	}
	if s.lengths == nil {
		s.lengths = make(map[int]int64)
	}
	n := s.readLength(k)
	s.lengths[ref.Number] = n
	return n
}

// readLength is indirectLength's uncached read of header k: the integer of
// "N G obj <integer> endobj" on k's span, or -1.
func (s *repairScan) readLength(k int) int64 {
	data := s.data[:s.spanEnd(k)]
	pos := int(s.headers[k].off)
	var toks [5]token
	for i := range toks {
		t, ni, err := nextTokenQuiet(data, pos)
		if err != nil {
			return -1
		}
		toks[i], pos = t, ni
	}
	if toks[2].kind != tokKeyword || toks[2].kw != "obj" || toks[3].kind != tokInt ||
		toks[4].kind != tokKeyword || toks[4].kw != "endobj" || toks[3].int < 0 {
		return -1
	}
	return toks[3].int
}

// kept returns the number of headers pass 2 did not mask.
func (s *repairScan) kept() int {
	n := 0
	for _, m := range s.masked {
		if !m {
			n++
		}
	}
	return n
}

// table builds the object table from the kept headers: the last definition
// of each number in file order wins, whatever its generation (§7.5.6: an
// incremental update appends its objects after the revision it changes) —
// among the definitions that parse on their span. A definition that does not
// parse (a write cut short, garbage after a header) wins its number only when
// no definition of it parses: it must not replace a good earlier one.
//
// Returns a wrapped errRepairLimit past maxRepairObjects distinct numbers.
func (s *repairScan) table() error {
	// Size the table once instead of letting the map double its way to 1 Mi
	// entries, which left about twice its size as garbage: by the kept count
	// for a small scan, by an exact count of the distinct kept numbers on a
	// bitset (one bit per possible number, 1 MiB) for a large one.
	hint := s.kept()
	if hint > 1<<14 { // a hint this size costs about what the bitset does
		seen := make([]uint64, maxRepairObjectNumber/64+1)
		hint = 0
		for k, h := range s.headers {
			if w, bit := h.num/64, uint64(1)<<(h.num%64); !s.masked[k] && seen[w]&bit == 0 {
				seen[w] |= bit
				hint++
			}
		}
	}
	s.entries = make(map[int]XrefEntry, min(hint, maxRepairObjects+1))
	for _, failed := range [2]bool{false, true} {
		for k := len(s.headers) - 1; k >= 0; k-- {
			if s.masked[k] || s.failed[k] != failed {
				continue
			}
			h := s.headers[k]
			if _, ok := s.entries[int(h.num)]; ok {
				continue
			}
			if len(s.entries) >= maxRepairObjects {
				return fmt.Errorf("%w: more than %d distinct objects (maxRepairObjects)", errRepairLimit, maxRepairObjects)
			}
			s.entries[int(h.num)] = XrefEntry{Offset: h.off, Gen: int(h.gen)}
		}
	}
	s.winners = make([]int, 0, len(s.entries))
	for k := len(s.headers) - 1; k >= 0; k-- {
		if !s.masked[k] && s.entries[int(s.headers[k].num)].Offset == s.headers[k].off {
			s.winners = append(s.winners, k)
		}
	}
	for _, k := range s.objStms {
		if s.entries[int(s.headers[k].num)].Offset == s.headers[k].off {
			return fmt.Errorf("%w: repair found an object stream (object %d), which it does not expand", errLex, s.headers[k].num)
		}
	}
	return nil
}

// IsLimitError reports whether err is a parse failure on a resource bound
// rather than on damage: an xref over its entry, row or decode caps or a
// /Prev chain over maxXrefChain sections, a stream over its decode limit, a
// value nested past maxNestingDepth, a page tree past maxPageTreeDepth or
// maxPages, a spent ObjStm re-decode budget, or an object scan over its
// bounds. Such a file is hostile or oversized, and core.OpenBytes does not
// retry it through repair (CONVENTIONS.md §4: recovery has limits, and they
// are hard).
func IsLimitError(err error) bool {
	return errors.Is(err, errLimit) || errors.Is(err, errXrefLimit) || errors.Is(err, ErrDecodeLimit) ||
		errors.Is(err, errRepairLimit)
}

// errLimit marks a failure on a resource bound (IsLimitError). It is never
// returned alone: limitf joins it to the error it marks.
var errLimit = errors.New("parse: resource limit")

// limitf is fmt.Errorf for a failure on a resource bound. The error has the
// message and matches what fmt.Errorf's would (errLex, at its sites), and
// matches errLimit too.
func limitf(format string, args ...any) error {
	return limitErr{fmt.Errorf(format, args...)}
}

// limitErr is limitf's error: err, marked with errLimit.
type limitErr struct{ err error }

func (e limitErr) Error() string   { return e.err.Error() }
func (e limitErr) Unwrap() []error { return []error{e.err, errLimit} }

// headerOf returns the header index of the winning definition of num, or -1.
func (s *repairScan) headerOf(num int) int {
	e, ok := s.entries[num]
	if !ok {
		return -1
	}
	k := sort.Search(len(s.headers), func(i int) bool { return s.headers[i].off >= e.Offset })
	if k == len(s.headers) || s.headers[k].off != e.Offset {
		return -1
	}
	return k
}

// peek returns the dictionary of the winning object num (a stream's
// dictionary for a stream), read with the bounded dictionary-only parse and
// memoized, so a check never loads, caches or decrypts an object: the
// Resolver's caches stay empty until the decryption state is final.
func (s *repairScan) peek(num int) (Dict, bool) {
	if r, ok := s.peeked[num]; ok {
		return r.dict, r.ok
	}
	var res peekResult
	if k := s.headerOf(num); k >= 0 {
		if obj, _, err := s.head(k); err == nil {
			res.dict, res.ok = obj.(Dict)
		}
	}
	if s.peeked == nil {
		s.peeked = make(map[int]peekResult)
	}
	s.peeked[num] = res
	return res.dict, res.ok
}

// dictOf resolves a trailer or catalog value to a dictionary: a direct one,
// or a reference to a winning object (one level, peek).
func (s *repairScan) dictOf(v Object) (Dict, bool) {
	switch o := v.(type) {
	case Dict:
		return o, true
	case Reference:
		return s.peek(o.Number)
	}
	return nil, false
}

// isCatalog reports whether v resolves to a usable catalog: a dictionary
// whose /Type, if present, is /Catalog and whose /Pages resolves to a
// dictionary.
func (s *repairScan) isCatalog(v Object) bool {
	d, ok := s.dictOf(v)
	if !ok {
		return false
	}
	if t, has := d["Type"]; has && t != Name("Catalog") {
		return false
	}
	_, ok = s.dictOf(d["Pages"])
	return ok
}

// candidates returns the trailer candidates, in file order, at most the last
// maxRepairTrailers: the dictionaries after the trailer keywords outside
// stream data, each parsed up to the next trailer keyword or header, and the
// cross-reference stream dictionaries pass 2 kept. lost is set when a trailer
// keyword considered is followed by bytes naming /Encrypt that do not parse
// to a dictionary (§3.6).
func (s *repairScan) candidates() (out []repairCand, lost bool) {
	var kw []int
	d := 0
	for _, t := range s.trailers {
		for d < len(s.streamData) && s.streamData[d][1] <= t {
			d++
		}
		if d < len(s.streamData) && s.streamData[d][0] <= t {
			continue // inside a stream object
		}
		kw = append(kw, t)
	}
	if len(kw) > maxRepairTrailers {
		kw = kw[len(kw)-maxRepairTrailers:]
	}
	for i, t := range kw {
		end := len(s.data)
		if k := sort.Search(len(s.headers), func(k int) bool { return int(s.headers[k].off) > t }); k < len(s.headers) {
			end = int(s.headers[k].off)
		}
		if i+1 < len(kw) {
			end = min(end, kw[i+1])
		}
		p := &iparser{data: s.data[:end], pos: t + len("trailer"), dictOnly: true, streamAt: -1}
		if v, err := p.parseValue(); err == nil {
			if dict, ok := v.(Dict); ok {
				out = append(out, repairCand{off: t, dict: dict})
				continue
			}
		}
		if hasName(s.data[t:end], "/Encrypt") {
			lost = true
		}
	}
	out = append(out, s.xrefDicts...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].off < out[b].off })
	if len(out) > maxRepairTrailers {
		out = out[len(out)-maxRepairTrailers:]
	}
	return out, lost
}

// trailer synthesizes the trailer (design §3.5): /Encrypt from the last
// candidate that has one, else from the shape scan (§3.6); /ID from the last
// candidate with a two-string array; /Root from the last candidate whose
// /Root is a usable catalog, else from the catalog scan; /Info from the last
// candidate whose /Info resolves to a dictionary.
//
// Returns the trailer and whether the catalog came from the scan, a wrapped
// errLex when no catalog is found, or ErrEncryptionLost when no candidate
// names /Encrypt, no winner has its shape, and the file shows one was lost.
func (s *repairScan) trailer() (Dict, bool, error) {
	cands, trailerLost := s.candidates()
	tr := Dict{}
	for i := len(cands) - 1; i >= 0; i-- {
		c := cands[i].dict
		if v, ok := c["Encrypt"]; ok && tr["Encrypt"] == nil {
			tr["Encrypt"] = v
		}
		if a, ok := c["ID"].(Array); ok && len(a) == 2 && tr["ID"] == nil {
			_, ok0 := a[0].(String)
			_, ok1 := a[1].(String)
			if ok0 && ok1 {
				tr["ID"] = a
			}
		}
		if v, ok := c["Root"]; ok && tr["Root"] == nil && s.isCatalog(v) {
			tr["Root"] = v
		}
		if v, ok := c["Info"]; ok && tr["Info"] == nil {
			if _, ok := s.dictOf(v); ok {
				tr["Info"] = v
			}
		}
	}
	if tr["Encrypt"] == nil {
		if ref, ok := s.encryptByShape(); ok {
			tr["Encrypt"] = ref
		} else if trailerLost || s.encryptLost() {
			return nil, false, ErrEncryptionLost
		}
	}
	fromScan := false
	if tr["Root"] == nil {
		ref, ok := s.catalogByScan()
		if !ok {
			return nil, false, fmt.Errorf("%w: repair found no document catalog", errLex)
		}
		tr["Root"] = ref
		fromScan = true
	}
	return tr, fromScan, nil
}

// span returns the bytes of winning header k's span.
func (s *repairScan) span(k int) []byte {
	return s.data[s.headers[k].off:s.spanEnd(k)]
}

// ref returns the reference to winning header k.
func (s *repairScan) ref(k int) Reference {
	h := s.headers[k]
	return Reference{Number: int(h.num), Generation: int(h.gen)}
}

// catalogByScan finds the catalog no trailer names: the winners whose span
// holds "/Catalog", the last in file order first, at most maxRepairCatalogs
// of them parsed; the first that isCatalog with /Type /Catalog is taken.
func (s *repairScan) catalogByScan() (Reference, bool) {
	parsed := 0
	for _, k := range s.winners {
		if !bytes.Contains(s.span(k), []byte("/Catalog")) {
			continue
		}
		if parsed == maxRepairCatalogs {
			return Reference{}, false
		}
		parsed++
		ref := s.ref(k)
		if d, ok := s.peek(ref.Number); ok && d["Type"] == Name("Catalog") && s.isCatalog(ref) {
			return ref, true
		}
	}
	return Reference{}, false
}

// encryptByShape finds an encryption dictionary no trailer names (§3.6, fail
// closed): the last winner whose dictionary has a /Filter name and either
// /O, /U and /P (the standard handler, ISO 32000-1 Table 21), /OE or /UE (its
// AES-256 form, ISO 32000-2) or /Recipients (a public-key handler), and is
// not a stream's (§7.6.1: it is a
// dictionary). An encryption dictionary cannot be in an object stream
// (§7.5.7), so plain objects suffice. Dictionary bytes (dictBytes) are
// pre-filtered by name search (encryptShaped); the parse is the bounded
// dictionary-only one, not memoized.
func (s *repairScan) encryptByShape() (Reference, bool) {
	for _, k := range s.winners {
		if !s.failed[k] && encryptShaped(namesOf(s.dictBytes(k))) && s.isEncryptDict(k) == 1 {
			return s.ref(k), true
		}
	}
	return Reference{}, false
}

// encryptLost reports, for a scan whose winners hold no encryption dictionary
// (encryptByShape found none), whether one was lost all the same (§3.6, fail
// closed): a kept header whose value does not parse but whose dictionary
// bytes look like an encryption dictionary (lostShaped) or name /Encrypt (a
// trailer or cross-reference stream dictionary cut short), or any other
// header — masked as stream data, shadowed by a later definition of its
// number, or a winner — whose dictionary bytes have the shape and which
// parses to an encryption dictionary (not a winner's: encryptByShape would
// have taken it), does not parse, or cannot be read as one (isEncryptDict).
// Each span is searched and parsed once at most: linear in the file size.
func (s *repairScan) encryptLost() bool {
	for k := range s.headers {
		n := namesOf(s.dictBytes(k))
		if s.failed[k] {
			if lostShaped(n) || n.has(nEncrypt) {
				return true
			}
			continue
		}
		if encryptShaped(n) && s.isEncryptDict(k) != 0 {
			return true
		}
	}
	return false
}

// isEncryptDict parses header k (bounded, dictionary only) and returns 1 when
// its value is a dictionary, not a stream's, with a /Filter name and either
// /O, /U and /P, /OE or /UE, or /Recipients; -1 when it does not parse, or has those keys
// but cannot be read as one — /Filter is not a name (an indirect handler
// name), or the dictionary is a stream's and its /Filter is not a stream
// filter (an encryption dictionary followed by a stray stream keyword); 0
// otherwise.
func (s *repairScan) isEncryptDict(k int) int {
	obj, start, err := s.head(k)
	if err != nil {
		return -1
	}
	d, ok := obj.(Dict)
	if !ok {
		return 0
	}
	_, o := d["O"]
	_, u := d["U"]
	_, p := d["P"]
	_, rc := d["Recipients"]
	_, oe := d["OE"]
	_, ue := d["UE"]
	if !(o && u && p) && !rc && !oe && !ue {
		return 0
	}
	f, has := d["Filter"]
	if !has {
		return 0
	}
	name, isName := f.(Name)
	switch {
	case !isName:
		return -1
	case start >= 0 && streamFilters[string(name)]:
		return 0 // a stream with its own filter, not an encryption dictionary
	case start >= 0:
		return -1
	}
	return 1
}

// dictBytes returns header k's span up to its first "stream" or "endobj"
// keyword: the bytes that can hold its dictionary, without the stream data,
// whose binary bytes match short names by chance, and without the objects
// after a value that does not parse, whose names are not its own.
func (s *repairScan) dictBytes(k int) []byte {
	sp := s.span(k)
	for _, kw := range [...]string{"stream", "endobj"} {
		if i := bytes.Index(sp, []byte(kw)); i >= 0 {
			sp = sp[:i]
		}
	}
	return sp
}

// encryptShaped is the pre-filter for an encryption dictionary: the names
// /Filter and either /Recipients, all of /O, /U and /P, or one of /OE and /UE
// (keys only the AES-256 standard handler's dictionary has, ISO 32000-2
// Table 21; a V5 dictionary missing /P is still one).
func encryptShaped(n dictNames) bool {
	return n.has(nFilter) && (n.has(nRecipients) || (n.has(nO) && n.has(nU) && n.has(nP)) || n.has(nOE) || n.has(nUE))
}

// lostShaped is the pre-filter for the dictionary bytes of a header whose
// value does not parse: a cut can end them anywhere, so any one sign of an
// encryption dictionary counts. In order:
//
//  1. /Recipients, /OE, /UE (AES-256 standard handler keys, ISO 32000-2
//     Table 21: no other dictionary has them), /Filter /Standard (the
//     standard handler, ISO 32000-1 §7.6.3), /Filter /Adobe.PubSec or a
//     public-key /SubFilter adbe.pkcs7.s3, .s4, .s5 (§7.6.5): always a sign,
//     whatever else the bytes hold;
//  2. /Filter with one of /O, /U, /CF, /StmF, /StrF (Table 20, Table 21);
//  3. otherwise, a signature dictionary (§12.8.1; its /Filter names its
//     handler too) is not one: /ByteRange, /Type /Sig or /Type
//     /DocTimeStamp rules it out (a /SubFilter alone does not: public-key
//     handlers have one);
//  4. /Filter with /P (after 3: a DocMDP /TransformParams has /P);
//  5. a dictionary with /Subtype (an image, form or font file: Table 20 has
//     no /Subtype) is not one, when its /Filter value is missing or not a
//     name, a stream filter, or a cut-short prefix of one (/JBIG2);
//  6. /Filter whose value is not a stream filter (another handler name, or
//     nothing yet).
func lostShaped(n dictNames) bool {
	fv := string(n.filter) // compared and looked up below without allocating
	if n.has(nRecipients) || n.has(nOE) || n.has(nUE) || fv == "Standard" || fv == "Adobe.PubSec" || publicKeySubFilter(n.subFilter) {
		return true
	}
	if !n.has(nFilter) {
		return false
	}
	if n.has(nO) || n.has(nU) || n.has(nCF) || n.has(nStmF) || n.has(nStrF) {
		return true
	}
	if n.has(nByteRange) || n.sigType {
		return false
	}
	if n.has(nP) {
		return true
	}
	if n.has(nSubtype) && (fv == "" || streamFilterPrefix(fv)) {
		return false
	}
	return !n.filterIsStream
}

// publicKeySubFilter reports whether v, a /SubFilter value, is a public-key
// security handler's: adbe.pkcs7.s followed by a digit (s3, s4, s5, §7.6.5),
// not the signature SubFilter adbe.pkcs7.sha1.
func publicKeySubFilter(v []byte) bool {
	const pre = "adbe.pkcs7.s"
	return len(v) > len(pre) && string(v[:len(pre)]) == pre && v[len(pre)] >= '0' && v[len(pre)] <= '9'
}

// streamFilterPrefix reports whether name is a standard stream filter name or
// a non-empty prefix of one (a name cut short).
func streamFilterPrefix(name string) bool {
	for f := range streamFilters {
		if len(name) <= len(f) && f[:len(name)] == name {
			return true
		}
	}
	return false
}

// streamFilters are the standard stream filter names and their inline-image
// abbreviations (ISO 32000-1 Table 6, Table 94).
var streamFilters = map[string]bool{
	"ASCIIHexDecode": true, "AHx": true, "ASCII85Decode": true, "A85": true, "LZWDecode": true, "LZW": true,
	"FlateDecode": true, "Fl": true, "RunLengthDecode": true, "RL": true, "CCITTFaxDecode": true, "CCF": true,
	"JBIG2Decode": true, "DCTDecode": true, "DCT": true, "JPXDecode": true, "Crypt": true,
}

// The names the encryption checks look for (dictNames.bits).
const (
	nFilter = 1 << iota
	nRecipients
	nO
	nU
	nP
	nCF
	nStmF
	nStrF
	nByteRange
	nSubtype
	nEncrypt
	nOE
	nUE
)

// dictNames is what one scanNames pass over dictionary bytes finds: which of
// the names the encryption checks use occur as whole tokens outside strings,
// the values after the first /Filter and /SubFilter, and whether a /Type is
// /Sig or /DocTimeStamp. One pass instead of one per name: a check of every
// span of a hostile file must cost one walk of its bytes.
type dictNames struct {
	bits           int
	filter         []byte // the name after the first /Filter, without its slash; nil if not a name
	filterIsStream bool   // that value is a stream filter: a standard name, an array or a reference
	subFilter      []byte // the name after the first /SubFilter
	sigType        bool   // some /Type is /Sig or /DocTimeStamp
}

func (n dictNames) has(bit int) bool { return n.bits&bit != 0 }

// namesOf scans b once (scanNames) and returns its dictNames.
func namesOf(b []byte) dictNames {
	var n dictNames
	scanNames(b, func(at int) bool {
		j := at + 1
		for j < len(b) && isRegular(b[j]) {
			j++
		}
		switch string(b[at+1 : j]) { // no allocation: compared in place
		case "Filter":
			if !n.has(nFilter) {
				n.filter, n.filterIsStream = filterValue(b, j)
			}
			n.bits |= nFilter
		case "SubFilter":
			if n.subFilter == nil {
				n.subFilter = nameAfterBytes(b, j)
			}
		case "Type":
			if v := nameAfterBytes(b, j); string(v) == "Sig" || string(v) == "DocTimeStamp" {
				n.sigType = true
			}
		case "Recipients":
			n.bits |= nRecipients
		case "O":
			n.bits |= nO
		case "U":
			n.bits |= nU
		case "P":
			n.bits |= nP
		case "CF":
			n.bits |= nCF
		case "StmF":
			n.bits |= nStmF
		case "StrF":
			n.bits |= nStrF
		case "ByteRange":
			n.bits |= nByteRange
		case "Subtype":
			n.bits |= nSubtype
		case "Encrypt":
			n.bits |= nEncrypt
		case "OE":
			n.bits |= nOE
		case "UE":
			n.bits |= nUE
		}
		return false
	})
	return n
}

// filterValue reads the /Filter value whose key ends at i: its name (without
// the slash, aliasing b; nil when it is not a name) and whether it is a
// stream filter (a standard filter name, an array of filters, or a
// reference).
func filterValue(b []byte, i int) ([]byte, bool) {
	i = skipBlank(b, i)
	if i >= len(b) {
		return nil, false
	}
	switch c := b[i]; {
	case c == '[' || (c >= '0' && c <= '9'):
		return nil, true
	case c != '/':
		return nil, false
	}
	name := nameAfterBytes(b, i)
	return name, streamFilters[string(name)] // no allocation: a map index
}

// nameAfterBytes returns the name value at or after offset i in b (white-space
// skipped), without its slash, or nil when the value there is not a name. It
// aliases b.
func nameAfterBytes(b []byte, i int) []byte {
	i = skipBlank(b, i)
	if i >= len(b) || b[i] != '/' {
		return nil
	}
	j := i + 1
	for j < len(b) && isRegular(b[j]) {
		j++
	}
	return b[i+1 : j]
}

// hasName reports whether b holds the name pat ("/Filter") as a whole token:
// followed by a white-space or delimiter character or the end of b, and not
// inside a literal or hexadecimal string.
func hasName(b []byte, pat string) bool { return nameAt(b, pat) >= 0 }

// nameAt returns the offset of the first whole-token name pat in b outside
// strings (scanNames), or -1.
func nameAt(b []byte, pat string) int {
	at := -1
	scanNames(b, func(i int) bool {
		if isNameAt(b, i, pat) {
			at = i
			return true
		}
		return false
	})
	return at
}

// isNameAt reports whether the whole-token name pat starts at b[i].
func isNameAt(b []byte, i int, pat string) bool {
	return bytes.HasPrefix(b[i:], []byte(pat)) && (i+len(pat) == len(b) || !isRegular(b[i+len(pat)]))
}

// scanNames calls fn with the offset of every '/' in b outside strings, in
// order, until fn returns true. Literal strings are skipped with their
// escapes and nested parentheses (§7.3.4.2), hexadecimal strings to their
// '>' (§7.3.4.3; "<<" opens a dictionary, not a string). A string that is
// not terminated in b is not a string the bytes can be trusted to hold — a
// stray '(' or '<' in damaged bytes would hide every name after it — so from
// there on the rest of b is scanned plainly, every '/' included. At most two
// passes over b: linear whatever it holds.
func scanNames(b []byte, fn func(at int) bool) {
	plain := false
	for i := 0; i < len(b); {
		c := b[i]
		if !plain {
			switch {
			case c == '(':
				if end := skipLiteral(b, i+1); end >= 0 {
					i = end
				} else {
					plain = true
					i++
				}
				continue
			case c == '<' && i+1 < len(b) && b[i+1] == '<':
				i += 2
				continue
			case c == '<':
				if k := bytes.IndexByte(b[i+1:], '>'); k >= 0 {
					i += k + 2
				} else {
					plain = true
					i++
				}
				continue
			}
		}
		if c == '/' && fn(i) {
			return
		}
		i++
	}
}

// skipLiteral returns the offset just past the literal string whose body
// starts at i (its '(' already read), or -1 when it is not terminated in b.
func skipLiteral(b []byte, i int) int {
	depth := 1
	for ; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++ // the escaped byte
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

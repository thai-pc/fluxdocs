package parse

import (
	"bytes"
	"errors"
	"fmt"
	"math"
)

// XrefEntry is one entry in the cross-reference table.
//
// Three kinds (ISO 32000-1 §7.5.4 / §7.5.8):
//   - free:     Free == true (the object is deleted)
//   - in-use:   Offset is the byte position of the indirect object
//   - in-objstm: InObjStm == true; the object lives inside the compressed
//     object stream numbered StreamNum, at position StreamIdx
type XrefEntry struct {
	Offset int64 // in-use: byte offset to the indirect object
	Gen    int
	Free   bool

	InObjStm  bool // the object lives inside a compressed object stream
	StreamNum int  // object number of the containing /ObjStm
	StreamIdx int  // index of this object within that stream
}

// Xref is the cross-reference table merged along the /Prev chain (newest entry
// wins), together with the most recent trailer dictionary.
type Xref struct {
	Entries map[int]XrefEntry
	Trailer Dict
}

const maxXrefChain = 64 // bound a malicious /Prev loop (SECURITY.md §2)

// Xref size bounds (SECURITY.md §2). A classic table spends 20 bytes of file per
// entry, but an xref stream with /W [1 0 0] spends one decoded byte, and a body
// of zeros deflates about 1000:1, so a ~260 KB file could otherwise insert one
// map entry per byte of the 256 MiB decode cap (~268 M entries, ~30 GB).
const (
	// maxXrefEntries caps the rows of one section and the distinct objects of
	// the table merged along the /Prev chain. Rows go straight into that one
	// map (Xref.add), which at the cap is ~290 MiB of process memory native
	// and under js/wasm (testing/security/dos/xref_dos_test.go). Real
	// documents stay well below: a 10,000-page file has a few hundred
	// thousand objects, and ISO 32000-1 Annex C puts the object-number limit
	// at 8,388,607.
	maxXrefEntries = 1 << 21
	// maxXrefRows caps the rows charged across all sections of the chain. A
	// row for a number already in the table allocates nothing but still costs
	// a lookup (~0.5 µs at the cap), so without this cap 64 full-size
	// sections would cost 64 times the parse of one. 2× maxXrefEntries keeps
	// the worst chain at about twice one maximal section, and still opens
	// real incremental files that rewrite the whole table on every save: 64
	// saves of a 65,000-object table, or 10 of a 400,000-object one.
	maxXrefRows = 2 * maxXrefEntries
	// maxXrefFieldWidth is the widest /W field an xref stream may declare:
	// readField packs a field into an int64.
	maxXrefFieldWidth = 8
	// maxXrefDecoded caps the bytes the filters of all the chain's xref
	// streams may output together (every stage of a filter chain counted),
	// as defence in depth behind the row caps: maxXrefRows rows of at most
	// 24 bytes (a PNG predictor adds one byte a row) decode to ~100 MiB, but
	// a stream's /DecodeParms /Columns can make one predictor row up to
	// maxDecoded long whatever /W says. With this cap a chain costs at most
	// one maxDecoded decode, not one per section.
	maxXrefDecoded = maxDecoded
	// maxXrefObjectNumber is the largest object number an xref range may
	// reach; it keeps start+count inside a 32-bit int on every target.
	maxXrefObjectNumber = math.MaxInt32
)

// errXrefLimit reports an xref section or chain over maxXrefEntries,
// maxXrefRows or maxXrefDecoded. It fails the whole parse; it is never dropped like a damaged
// hybrid /XRefStm section.
var errXrefLimit = fmt.Errorf("%w: xref too large", errLex)

// xrefBudget charges xref rows against the per-section and per-chain caps
// before any entry is added: the counts a classic subsection header or an
// xref stream's /Index declares, or the rows a stream without /Index holds.
type xrefBudget struct {
	chainRows  int64 // rows declared so far across the /Prev chain
	decodeLeft int64 // xref-stream decode bytes the chain has left (maxXrefDecoded)
}

// newXrefBudget returns the budget of one ParseXref.
func newXrefBudget() *xrefBudget { return &xrefBudget{decodeLeft: maxXrefDecoded} }

// claim checks one subsection of count objects starting at start and charges
// it to the section total *section and the chain total. All arithmetic is in
// int64 on values already bounded, so it cannot overflow.
//
// Returns a wrapped errXrefLimit when a cap would be exceeded, or a wrapped
// errLex for a negative start/count or a range past maxXrefObjectNumber.
func (b *xrefBudget) claim(start, count int64, section *int64) error {
	if start < 0 || count < 0 {
		return fmt.Errorf("%w: negative xref range %d+%d", errLex, start, count)
	}
	if count > maxXrefEntries-*section {
		return fmt.Errorf("%w: section declares more than %d entries", errXrefLimit, maxXrefEntries)
	}
	if count > maxXrefRows-b.chainRows {
		return fmt.Errorf("%w: chain declares more than %d entries", errXrefLimit, maxXrefRows)
	}
	if start > maxXrefObjectNumber-count { // count ≤ maxXrefEntries: no overflow
		return fmt.Errorf("%w: xref range %d+%d out of bounds", errLex, start, count)
	}
	*section += count
	b.chainRows += count
	return nil
}

// room returns how many more rows one fresh section may claim: the smaller
// of the per-section cap and what the chain has left.
func (b *xrefBudget) room() int64 {
	return min(maxXrefEntries, maxXrefRows-b.chainRows)
}

// FindStartXref scans backward from the end of the file for "startxref" and
// returns the byte offset of the most recent xref section.
//
// Returns a wrapped errLex if "startxref" or its offset is absent.
func FindStartXref(data []byte) (int, error) {
	idx := bytes.LastIndex(data, []byte("startxref"))
	if idx < 0 {
		return 0, fmt.Errorf("%w: missing 'startxref'", errLex)
	}
	t, _, err := nextToken(data, idx+len("startxref"))
	if err != nil || t.kind != tokInt {
		return 0, fmt.Errorf("%w: 'startxref' not followed by an offset", errLex)
	}
	return int(t.int), nil
}

// ParseXref builds the full cross-reference table: it starts at startxref and
// follows the /Prev chain, handling both the classic xref table and xref
// streams (PDF 1.5+), including a hybrid table's /XRefStm pointer. Each
// section's rows go straight into the table (Xref.add), so a section costs no
// map of its own.
//
// A damaged hybrid /XRefStm section is dropped: the entries it added are
// removed, but the rows it declared stay charged to the chain (maxXrefRows),
// so dropping a section never refunds work done.
//
// Returns the merged Xref, or a wrapped errLex on malformed input, a chain
// longer than maxXrefChain / containing an offset loop, or a section, merged
// table or chain over maxXrefEntries / maxXrefRows / maxXrefDecoded.
func ParseXref(data []byte) (*Xref, error) {
	start, err := FindStartXref(data)
	if err != nil {
		return nil, err
	}

	x := &Xref{Entries: make(map[int]XrefEntry)}
	seen := make(map[int]bool) // visited section offsets — guard against loops
	budget := newXrefBudget()
	off := start

	for depth := 0; depth < maxXrefChain; depth++ {
		if off < 0 || off >= len(data) || seen[off] {
			return x, nil
		}
		seen[off] = true

		trailer, prev, xrefStm, err := parseXrefAt(data, off, x, budget, nil)
		if err != nil {
			return nil, err
		}
		if x.Trailer == nil {
			x.Trailer = trailer
		}

		// Hybrid file: a classic table may point to a parallel xref stream that
		// holds entries for compressed objects (ISO 32000-1 §7.5.8.4). A
		// damaged one is skipped; an oversized one fails like any section.
		if xrefStm >= 0 && xrefStm < len(data) && !seen[xrefStm] {
			seen[xrefStm] = true
			var added []int
			_, _, _, err := parseXrefAt(data, xrefStm, x, budget, &added)
			if errors.Is(err, errXrefLimit) {
				return nil, err
			}
			if err != nil {
				for _, num := range added {
					delete(x.Entries, num)
				}
			}
		}

		if prev < 0 {
			return x, nil
		}
		off = prev
	}
	return nil, fmt.Errorf("%w: xref chain too long (suspected loop)", errLex)
}

// add records e for object num unless num already has an entry. Sections are
// parsed newest first, so the newest section wins; within a section the first
// row for a number wins. When added is non-nil, a number add records is
// appended to it, so a dropped section can be undone.
//
// Returns a wrapped errXrefLimit once the table would exceed maxXrefEntries
// distinct objects; x is then incomplete and must be discarded.
func (x *Xref) add(num int, e XrefEntry, added *[]int) error {
	if _, exists := x.Entries[num]; exists {
		return nil
	}
	if len(x.Entries) >= maxXrefEntries {
		return fmt.Errorf("%w: merged table exceeds %d entries", errXrefLimit, maxXrefEntries)
	}
	x.Entries[num] = e
	if added != nil {
		*added = append(*added, num)
	}
	return nil
}

// parseXrefAt parses one xref section at off into x, dispatching on its form: a
// classic "xref ... trailer" table, or an xref-stream indirect object. Rows are
// charged to budget and added to x (see Xref.add, which records them in added
// when it is non-nil).
//
// Returns the trailer/stream dictionary, the /Prev offset (-1 if none), the
// /XRefStm offset for hybrid files (-1 if none), or a wrapped errLex
// (errXrefLimit when the section is over budget's caps or x is full). On an
// error x may hold some of the section's rows.
func parseXrefAt(data []byte, off int, x *Xref, budget *xrefBudget, added *[]int) (Dict, int, int, error) {
	t, _, err := nextToken(data, off)
	if err != nil {
		return nil, -1, -1, err
	}
	if t.kind == tokKeyword && t.kw == "xref" {
		trailer, prev, err := parseXrefSection(data, off, x, budget, added)
		if err != nil {
			return nil, -1, -1, err
		}
		xrefStm := -1
		if v, ok := trailer.GetInt("XRefStm"); ok {
			xrefStm = int(v)
		}
		return trailer, prev, xrefStm, nil
	}
	// Otherwise expect an xref-stream indirect object ("n g obj << >> stream").
	trailer, prev, err := parseXrefStreamAt(data, off, x, budget, added)
	return trailer, prev, -1, err
}

// parseXrefSection parses one "xref ... trailer << >>" at offset off, adding
// its rows to x (see parseXrefAt).
//
// Returns the trailer dictionary and the /Prev offset (-1 if absent), or a
// wrapped errLex on malformed input (errXrefLimit when a subsection header is
// over budget's caps or x is full).
func parseXrefSection(data []byte, off int, x *Xref, budget *xrefBudget, added *[]int) (Dict, int, error) {
	t, pos, err := nextToken(data, off)
	if err != nil {
		return nil, -1, err
	}
	if t.kind != tokKeyword || t.kw != "xref" {
		return nil, -1, fmt.Errorf("%w: xref streams unsupported (offset %d is not 'xref')", errLex, off)
	}

	var declared int64 // entries declared by this section's headers so far
	for {
		// Subsection header "start count", or 'trailer' to finish.
		ht, hp, err := nextToken(data, pos)
		if err != nil {
			return nil, -1, err
		}
		if ht.kind == tokKeyword && ht.kw == "trailer" {
			pos = hp
			break
		}
		if ht.kind != tokInt {
			return nil, -1, fmt.Errorf("%w: invalid xref subsection header", errLex)
		}
		ct, cp, err := nextToken(data, hp)
		if err != nil || ct.kind != tokInt {
			return nil, -1, fmt.Errorf("%w: xref subsection missing count", errLex)
		}
		if err := budget.claim(ht.int, ct.int, &declared); err != nil {
			return nil, -1, err
		}
		startNum, count := int(ht.int), int(ct.int)
		pos = cp

		for k := 0; k < count; k++ {
			offTok, p1, err := nextToken(data, pos)
			if err != nil || offTok.kind != tokInt {
				return nil, -1, fmt.Errorf("%w: xref entry missing offset", errLex)
			}
			genTok, p2, err := nextToken(data, p1)
			if err != nil || genTok.kind != tokInt {
				return nil, -1, fmt.Errorf("%w: xref entry missing generation", errLex)
			}
			typeTok, p3, err := nextToken(data, p2)
			if err != nil || typeTok.kind != tokKeyword || (typeTok.kw != "n" && typeTok.kw != "f") {
				return nil, -1, fmt.Errorf("%w: xref entry missing n/f type", errLex)
			}
			e := XrefEntry{Offset: offTok.int, Gen: int(genTok.int), Free: typeTok.kw == "f"}
			if err := x.add(startNum+k, e, added); err != nil {
				return nil, -1, err
			}
			pos = p3
		}
	}

	// Trailer dictionary.
	p := &iparser{data: data, pos: pos}
	tobj, err := p.parseValue()
	if err != nil {
		return nil, -1, fmt.Errorf("%w: trailer failed to parse: %v", errLex, err)
	}
	trailer, ok := tobj.(Dict)
	if !ok {
		return nil, -1, fmt.Errorf("%w: trailer is not a dictionary", errLex)
	}

	prev := -1
	if pv, ok := trailer.GetInt("Prev"); ok {
		prev = int(pv)
	}
	return trailer, prev, nil
}

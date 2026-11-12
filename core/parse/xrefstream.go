package parse

import (
	"errors"
	"fmt"
)

// parseXrefStreamAt parses a cross-reference stream (PDF 1.5+, ISO 32000-1
// §7.5.8) — an indirect object "n g obj << /Type /XRef … >> stream … endstream"
// whose decoded content is a packed table of fixed-width entries.
//
// Its rows are added to x (see parseXrefAt). Only the rows the table can use
// are decoded (decodeStreamPrefix), so a short table over a decompression
// bomb costs its rows, not maxDecoded. /Index counts are charged to budget
// before decoding; without /Index the rows charged are those the stream
// holds, up to /Size, so an oversized /Size over a short body still opens
// while more real rows than the caps allow fail (errXrefLimit) having decoded
// at most one row past them.
//
// Returns the stream dictionary (which doubles as the trailer) and the /Prev
// offset (-1 if none), or a wrapped errLex on malformed input.
func parseXrefStreamAt(data []byte, off int, x *Xref, budget *xrefBudget, added *[]int) (Dict, int, error) {
	_, _, obj, _, err := ParseIndirectObjectAt(data, off)
	if err != nil {
		return nil, -1, err
	}
	st, ok := obj.(*Stream)
	if !ok {
		return nil, -1, fmt.Errorf("%w: xref stream object at offset %d is not a stream", errLex, off)
	}
	dict := st.Dict

	// /W [w1 w2 w3] — the byte widths of the three entry fields.
	w, ok := dict.GetArray("W")
	if !ok || len(w) < 3 {
		return nil, -1, fmt.Errorf("%w: xref stream missing /W widths", errLex)
	}
	// Bound each width before summing (SECURITY.md §2): unbounded, three widths
	// of 2^62 wrap the row length negative, which slips past the truncation
	// check below and slices far beyond the data. readField packs a field into
	// an int64, so a field wider than 8 bytes cannot carry a meaningful value
	// (producers write 1–4). The bound keeps rowLen ≤ 24, so a section's rows
	// (≤ maxXrefEntries+1) decode to at most ~48 MiB; a wide row such as
	// /W [1 0 100000] made /Size 2^28 a 256 MiB decode per section.
	var width [3]int
	for i := 0; i < 3; i++ {
		n, ok := w[i].(Integer)
		if !ok || n < 0 || n > maxXrefFieldWidth {
			return nil, -1, fmt.Errorf("%w: invalid /W entry (a field is 0–%d bytes)", errLex, maxXrefFieldWidth)
		}
		width[i] = int(n)
	}
	rowLen := width[0] + width[1] + width[2]
	if rowLen == 0 {
		return nil, -1, fmt.Errorf("%w: xref stream /W widths are all zero", errLex)
	}

	// /Index [start count …] selects the object-number ranges; default is the
	// single range [0 Size]. Bound them before decoding (SECURITY.md §2).
	index, sized, err := xrefIndex(dict, budget)
	if err != nil {
		return nil, -1, err
	}
	// Decode only the rows the table can use, within what the chain's decode
	// budget has left. A /Size range is not charged yet: one row past what the
	// caps leave is enough to tell whether the stream really holds too many.
	var rows int64
	for _, rng := range index {
		rows += int64(rng.count)
	}
	if sized {
		rows = min(rows, budget.room()+1)
	}
	// rows ≤ maxXrefRows+1 and rowLen ≤ 24: the product cannot overflow.
	need := rows * int64(rowLen)
	decoded, produced, err := decodeStreamPrefix(st, min(need, maxDecoded), budget.decodeLeft)
	budget.decodeLeft -= produced // charged even if the section is dropped
	if errors.Is(err, ErrDecodeLimit) {
		return nil, -1, fmt.Errorf("%w: xref streams decode to more than %d bytes", errXrefLimit, maxXrefDecoded)
	}
	if err != nil {
		return nil, -1, err
	}
	if sized {
		have := min(int64(index[0].count), int64(len(decoded)/rowLen))
		var declared int64
		if err := budget.claim(0, have, &declared); err != nil {
			return nil, -1, err
		}
		index[0].count = int(have)
	}

	pos := 0
	for _, rng := range index {
		for k := 0; k < rng.count; k++ {
			if pos+rowLen > len(decoded) {
				// Truncated table — stop leniently rather than read past the end.
				return finishXrefStream(dict)
			}
			field0 := readFieldDefault(decoded[pos:pos+width[0]], 1) // type, default 1
			field1 := readField(decoded[pos+width[0] : pos+width[0]+width[1]])
			field2 := readField(decoded[pos+width[0]+width[1] : pos+rowLen])
			pos += rowLen

			var e XrefEntry
			switch field0 {
			case 0: // free
				e = XrefEntry{Free: true, Gen: int(field2)}
			case 1: // in-use
				e = XrefEntry{Offset: field1, Gen: int(field2)}
			case 2: // compressed (lives in an object stream)
				e = XrefEntry{InObjStm: true, StreamNum: int(field1), StreamIdx: int(field2)}
			default:
				// Unknown type — skip per spec (treat as a null/absent object).
				continue
			}
			if err := x.add(rng.start+k, e, added); err != nil {
				return nil, -1, err
			}
		}
	}
	return finishXrefStream(dict)
}

func finishXrefStream(dict Dict) (Dict, int, error) {
	prev := -1
	if pv, ok := dict.GetInt("Prev"); ok {
		prev = int(pv)
	}
	return dict, prev, nil
}

type xrefRange struct{ start, count int }

// xrefIndex returns the object-number ranges from /Index, each charged to
// budget, or the single range [0 Size] with sized set. A /Size range is not
// charged here: the caller charges the rows the stream really holds.
//
// Returns a wrapped errLex for a missing or negative /Size, a malformed or
// out-of-range /Index, or (errXrefLimit) /Index counts over budget's caps.
func xrefIndex(dict Dict, budget *xrefBudget) (ranges []xrefRange, sized bool, err error) {
	arr, ok := dict.GetArray("Index")
	if !ok {
		size, ok := dict.GetInt("Size")
		if !ok {
			return nil, false, fmt.Errorf("%w: xref stream missing /Size", errLex)
		}
		if size < 0 {
			return nil, false, fmt.Errorf("%w: negative xref stream /Size %d", errLex, size)
		}
		// Clamped so the count fits an int on every target; the caller charges
		// at most budget.room()+1 of it.
		return []xrefRange{{0, int(min(size, maxXrefObjectNumber))}}, true, nil
	}
	if len(arr)%2 != 0 {
		return nil, false, fmt.Errorf("%w: xref stream /Index has odd length", errLex)
	}
	var declared int64 // entries declared by this stream so far
	for i := 0; i+1 < len(arr); i += 2 {
		start, ok1 := arr[i].(Integer)
		count, ok2 := arr[i+1].(Integer)
		if !ok1 || !ok2 {
			return nil, false, fmt.Errorf("%w: invalid xref stream /Index pair", errLex)
		}
		// claim rejects a negative start or count, and a range past
		// maxXrefObjectNumber.
		if err := budget.claim(int64(start), int64(count), &declared); err != nil {
			return nil, false, err
		}
		ranges = append(ranges, xrefRange{int(start), int(count)})
	}
	return ranges, false, nil
}

// readField reads a big-endian unsigned integer from b (0 for an empty field).
func readField(b []byte) int64 {
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	return v
}

// readFieldDefault is readField but returns def when the field width is zero
// (used for the type field, whose default is 1 per spec).
func readFieldDefault(b []byte, def int64) int64 {
	if len(b) == 0 {
		return def
	}
	return readField(b)
}

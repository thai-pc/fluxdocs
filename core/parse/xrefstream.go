package parse

import "fmt"

// parseXrefStreamAt parses a cross-reference stream (PDF 1.5+, ISO 32000-1
// §7.5.8) — an indirect object "n g obj << /Type /XRef … >> stream … endstream"
// whose decoded content is a packed table of fixed-width entries.
//
// Returns the entries, the stream dictionary (which doubles as the trailer), the
// /Prev offset (-1 if none), or a wrapped errLex on malformed input.
func parseXrefStreamAt(data []byte, off int) (map[int]XrefEntry, Dict, int, error) {
	_, _, obj, _, err := ParseIndirectObjectAt(data, off)
	if err != nil {
		return nil, nil, -1, err
	}
	st, ok := obj.(*Stream)
	if !ok {
		return nil, nil, -1, fmt.Errorf("%w: xref stream object at offset %d is not a stream", errLex, off)
	}
	dict := st.Dict

	// /W [w1 w2 w3] — the byte widths of the three entry fields.
	w, ok := dict.GetArray("W")
	if !ok || len(w) < 3 {
		return nil, nil, -1, fmt.Errorf("%w: xref stream missing /W widths", errLex)
	}
	var width [3]int
	for i := 0; i < 3; i++ {
		n, ok := w[i].(Integer)
		if !ok || n < 0 {
			return nil, nil, -1, fmt.Errorf("%w: invalid /W entry", errLex)
		}
		width[i] = int(n)
	}
	rowLen := width[0] + width[1] + width[2]
	if rowLen == 0 {
		return nil, nil, -1, fmt.Errorf("%w: xref stream /W widths are all zero", errLex)
	}

	decoded, err := DecodeStream(st)
	if err != nil {
		return nil, nil, -1, err
	}

	// /Index [start count …] selects the object-number ranges; default is the
	// single range [0 Size].
	index, err := xrefIndex(dict)
	if err != nil {
		return nil, nil, -1, err
	}

	entries := make(map[int]XrefEntry)
	pos := 0
	for _, rng := range index {
		for k := 0; k < rng.count; k++ {
			if pos+rowLen > len(decoded) {
				// Truncated table — stop leniently rather than read past the end.
				return finishXrefStream(entries, dict)
			}
			field0 := readFieldDefault(decoded[pos:pos+width[0]], 1) // type, default 1
			field1 := readField(decoded[pos+width[0] : pos+width[0]+width[1]])
			field2 := readField(decoded[pos+width[0]+width[1] : pos+rowLen])
			pos += rowLen

			num := rng.start + k
			if _, exists := entries[num]; exists {
				continue
			}
			switch field0 {
			case 0: // free
				entries[num] = XrefEntry{Free: true, Gen: int(field2)}
			case 1: // in-use
				entries[num] = XrefEntry{Offset: field1, Gen: int(field2)}
			case 2: // compressed (lives in an object stream)
				entries[num] = XrefEntry{InObjStm: true, StreamNum: int(field1), StreamIdx: int(field2)}
			default:
				// Unknown type — skip per spec (treat as a null/absent object).
			}
		}
	}
	return finishXrefStream(entries, dict)
}

func finishXrefStream(entries map[int]XrefEntry, dict Dict) (map[int]XrefEntry, Dict, int, error) {
	prev := -1
	if pv, ok := dict.GetInt("Prev"); ok {
		prev = int(pv)
	}
	return entries, dict, prev, nil
}

type xrefRange struct{ start, count int }

// xrefIndex returns the object-number ranges from /Index, defaulting to the
// single range [0 Size].
func xrefIndex(dict Dict) ([]xrefRange, error) {
	arr, ok := dict.GetArray("Index")
	if !ok {
		size, ok := dict.GetInt("Size")
		if !ok {
			return nil, fmt.Errorf("%w: xref stream missing /Size", errLex)
		}
		return []xrefRange{{0, int(size)}}, nil
	}
	if len(arr)%2 != 0 {
		return nil, fmt.Errorf("%w: xref stream /Index has odd length", errLex)
	}
	var ranges []xrefRange
	for i := 0; i+1 < len(arr); i += 2 {
		start, ok1 := arr[i].(Integer)
		count, ok2 := arr[i+1].(Integer)
		if !ok1 || !ok2 || count < 0 {
			return nil, fmt.Errorf("%w: invalid xref stream /Index pair", errLex)
		}
		ranges = append(ranges, xrefRange{int(start), int(count)})
	}
	return ranges, nil
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

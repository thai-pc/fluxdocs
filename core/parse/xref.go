package parse

import (
	"bytes"
	"fmt"
)

// XrefEntry is one entry in the cross-reference table.
type XrefEntry struct {
	Offset int64 // byte offset to the indirect object (type 'n')
	Gen    int
	Free   bool // type 'f' — the object has been deleted
}

// Xref is the cross-reference table merged along the /Prev chain (newest entry
// wins), together with the most recent trailer dictionary.
type Xref struct {
	Entries map[int]XrefEntry
	Trailer Dict
}

const maxXrefChain = 64 // bound a malicious /Prev loop (SECURITY.md §2)

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
// follows the /Prev chain. Only the classic xref table is supported; xref
// streams (PDF 1.5+) are future work.
//
// Returns the merged Xref, or a wrapped errLex on malformed input or a /Prev
// chain longer than maxXrefChain.
func ParseXref(data []byte) (*Xref, error) {
	start, err := FindStartXref(data)
	if err != nil {
		return nil, err
	}

	x := &Xref{Entries: make(map[int]XrefEntry)}
	off := start
	for depth := 0; depth < maxXrefChain; depth++ {
		entries, trailer, prev, err := parseXrefSection(data, off)
		if err != nil {
			return nil, err
		}
		// Newest wins: only add entries not already present.
		for num, e := range entries {
			if _, exists := x.Entries[num]; !exists {
				x.Entries[num] = e
			}
		}
		if x.Trailer == nil {
			x.Trailer = trailer
		}
		if prev < 0 {
			return x, nil
		}
		off = prev
	}
	return nil, fmt.Errorf("%w: /Prev chain too long (suspected loop)", errLex)
}

// parseXrefSection parses one "xref ... trailer << >>" at offset off.
//
// Returns the section's entries, its trailer dictionary, and the /Prev offset
// (-1 if absent), or a wrapped errLex on malformed input.
func parseXrefSection(data []byte, off int) (map[int]XrefEntry, Dict, int, error) {
	t, pos, err := nextToken(data, off)
	if err != nil {
		return nil, nil, -1, err
	}
	if t.kind != tokKeyword || t.kw != "xref" {
		return nil, nil, -1, fmt.Errorf("%w: xref streams unsupported (offset %d is not 'xref')", errLex, off)
	}

	entries := make(map[int]XrefEntry)
	for {
		// Subsection header "start count", or 'trailer' to finish.
		ht, hp, err := nextToken(data, pos)
		if err != nil {
			return nil, nil, -1, err
		}
		if ht.kind == tokKeyword && ht.kw == "trailer" {
			pos = hp
			break
		}
		if ht.kind != tokInt {
			return nil, nil, -1, fmt.Errorf("%w: invalid xref subsection header", errLex)
		}
		ct, cp, err := nextToken(data, hp)
		if err != nil || ct.kind != tokInt {
			return nil, nil, -1, fmt.Errorf("%w: xref subsection missing count", errLex)
		}
		startNum, count := int(ht.int), int(ct.int)
		pos = cp

		for k := 0; k < count; k++ {
			offTok, p1, err := nextToken(data, pos)
			if err != nil || offTok.kind != tokInt {
				return nil, nil, -1, fmt.Errorf("%w: xref entry missing offset", errLex)
			}
			genTok, p2, err := nextToken(data, p1)
			if err != nil || genTok.kind != tokInt {
				return nil, nil, -1, fmt.Errorf("%w: xref entry missing generation", errLex)
			}
			typeTok, p3, err := nextToken(data, p2)
			if err != nil || typeTok.kind != tokKeyword || (typeTok.kw != "n" && typeTok.kw != "f") {
				return nil, nil, -1, fmt.Errorf("%w: xref entry missing n/f type", errLex)
			}
			num := startNum + k
			if _, exists := entries[num]; !exists {
				entries[num] = XrefEntry{
					Offset: offTok.int,
					Gen:    int(genTok.int),
					Free:   typeTok.kw == "f",
				}
			}
			pos = p3
		}
	}

	// Trailer dictionary.
	p := &iparser{data: data, pos: pos}
	tobj, err := p.parseValue()
	if err != nil {
		return nil, nil, -1, fmt.Errorf("%w: trailer failed to parse: %v", errLex, err)
	}
	trailer, ok := tobj.(Dict)
	if !ok {
		return nil, nil, -1, fmt.Errorf("%w: trailer is not a dictionary", errLex)
	}

	prev := -1
	if pv, ok := trailer.GetInt("Prev"); ok {
		prev = int(pv)
	}
	return entries, trailer, prev, nil
}

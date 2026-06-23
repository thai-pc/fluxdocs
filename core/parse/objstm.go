package parse

import "fmt"

// objStm is a decoded compressed object stream (ISO 32000-1 §7.5.7): the
// inflated bytes plus the (objectNumber, offset) header that locates each
// contained object relative to /First.
type objStm struct {
	decoded []byte
	first   int
	offsets []objStmEntry
}

type objStmEntry struct {
	num    int
	offset int // relative to first
}

// objectFromStream returns the object at index idx within the object stream
// numbered streamNum, expected to have object number wantNum.
//
// Returns the parsed Object, or a wrapped errLex if the stream is malformed or
// the index is out of range.
func (r *Resolver) objectFromStream(streamNum, idx, wantNum int) (Object, error) {
	os, err := r.loadObjStm(streamNum)
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= len(os.offsets) {
		return nil, fmt.Errorf("%w: object %d: index %d out of range in ObjStm %d", errLex, wantNum, idx, streamNum)
	}

	start := os.first + os.offsets[idx].offset
	if start < 0 || start > len(os.decoded) {
		return nil, fmt.Errorf("%w: object %d: bad offset in ObjStm %d", errLex, wantNum, streamNum)
	}

	p := &iparser{data: os.decoded, pos: start}
	obj, err := p.parseValue()
	if err != nil {
		return nil, fmt.Errorf("%w: object %d in ObjStm %d: %v", errLex, wantNum, streamNum, err)
	}
	return obj, nil
}

// loadObjStm decodes and caches the object stream numbered streamNum.
func (r *Resolver) loadObjStm(streamNum int) (*objStm, error) {
	if os, ok := r.objstms[streamNum]; ok {
		return os, nil
	}

	// An ObjStm is itself an in-use object; resolve it without recursing into
	// compressed handling (object streams cannot be nested in object streams).
	e, ok := r.xref.Entries[streamNum]
	if !ok || e.Free || e.InObjStm {
		return nil, fmt.Errorf("%w: ObjStm %d not found or not a plain object", errLex, streamNum)
	}
	if e.Offset < 0 || e.Offset >= int64(len(r.data)) {
		return nil, fmt.Errorf("%w: ObjStm %d offset out of range", errLex, streamNum)
	}
	_, _, obj, _, err := ParseIndirectObjectAt(r.data, int(e.Offset))
	if err != nil {
		return nil, fmt.Errorf("loading ObjStm %d: %w", streamNum, err)
	}
	st, ok := obj.(*Stream)
	if !ok {
		return nil, fmt.Errorf("%w: object %d is not an ObjStm", errLex, streamNum)
	}

	n, ok := st.Dict.GetInt("N")
	if !ok || n < 0 {
		return nil, fmt.Errorf("%w: ObjStm %d missing /N", errLex, streamNum)
	}
	first, ok := st.Dict.GetInt("First")
	if !ok || first < 0 {
		return nil, fmt.Errorf("%w: ObjStm %d missing /First", errLex, streamNum)
	}

	decoded, err := DecodeStream(st)
	if err != nil {
		return nil, err
	}
	if int(first) > len(decoded) {
		return nil, fmt.Errorf("%w: ObjStm %d /First past end", errLex, streamNum)
	}

	// Header: N pairs of integers "objNum offset" before /First.
	offsets := make([]objStmEntry, 0, n)
	hp := &iparser{data: decoded, pos: 0}
	for i := 0; i < int(n); i++ {
		numTok, np, err := nextToken(decoded, hp.pos)
		if err != nil || numTok.kind != tokInt {
			return nil, fmt.Errorf("%w: ObjStm %d header object number", errLex, streamNum)
		}
		offTok, op, err := nextToken(decoded, np)
		if err != nil || offTok.kind != tokInt {
			return nil, fmt.Errorf("%w: ObjStm %d header offset", errLex, streamNum)
		}
		offsets = append(offsets, objStmEntry{num: int(numTok.int), offset: int(offTok.int)})
		hp.pos = op
	}

	os := &objStm{decoded: decoded, first: int(first), offsets: offsets}
	r.objstms[streamNum] = os
	return os, nil
}

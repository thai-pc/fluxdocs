package parse

import (
	"errors"
	"fmt"
	"math"
)

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

// objStmEntrySize is the bytes an objStmEntry occupies on 64-bit targets
// (js/wasm included): two ints. It overcounts on 32-bit targets.
const objStmEntrySize = 16

// size is the memory os holds for the ObjStm cache bound: its decoded bytes
// plus its offset table, which a header of tiny entries can make up to 4× the
// decoded bytes.
func (os *objStm) size() int64 {
	return int64(len(os.decoded)) + int64(cap(os.offsets))*objStmEntrySize
}

// cachedObjStm is an entry of the Resolver's ObjStm LRU list.
type cachedObjStm struct {
	num  int
	os   *objStm
	size int64
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

// loadObjStm decodes and caches the object stream numbered streamNum. A failure
// is cached as well (the same error value is returned again), so an ObjStm that
// inflates successfully but has a bad header is not re-inflated for each object
// or reference that points into it. r.mu is held.
//
// The cache keeps at most r.objstmCap bytes (maxObjStmCache); see cacheObjStm.
// Decoding an evicted ObjStm again is charged to the re-decode budget.
func (r *Resolver) loadObjStm(streamNum int) (*objStm, error) {
	if el, ok := r.objstms[streamNum]; ok {
		r.objstmLRU.MoveToFront(el)
		return el.Value.(*cachedObjStm).os, nil
	}
	if err, ok := r.objstmErrs[streamNum]; ok {
		return nil, err
	}
	os, err := r.decodeObjStm(streamNum)
	if err != nil {
		r.objstmErrs[streamNum] = err
		return nil, err
	}
	r.cacheObjStm(streamNum, os)
	return os, nil
}

// cacheObjStm adds os to the cache as the most recently used ObjStm, then
// evicts the least recently used until the cache is within r.objstmCap. An
// ObjStm larger than the cap on its own is evicted at once; the caller still
// uses it for the object it is loading. r.mu is held.
func (r *Resolver) cacheObjStm(num int, os *objStm) {
	c := &cachedObjStm{num: num, os: os, size: os.size()}
	r.objstms[num] = r.objstmLRU.PushFront(c)
	r.objstmBytes += c.size
	for r.objstmBytes > r.objstmCap {
		old := r.objstmLRU.Remove(r.objstmLRU.Back()).(*cachedObjStm)
		delete(r.objstms, old.num)
		r.objstmBytes -= old.size
		r.evicted[old.num] = true
	}
}

// decodeObjStm does loadObjStm's work on a cache miss. Every failure but one
// depends only on the file bytes, the xref table and the decryption state.
// The exception, a spent re-decode budget, depends on which objects were
// loaded before; it is permanent too, since the budget never refills.
func (r *Resolver) decodeObjStm(streamNum int) (*objStm, error) {
	r.loads++

	// An ObjStm is itself an in-use object; resolve it without recursing into
	// compressed handling (object streams cannot be nested in object streams).
	e, ok := r.xref.Entries[streamNum]
	if !ok || e.Free || e.InObjStm {
		return nil, fmt.Errorf("%w: ObjStm %d not found or not a plain object", errLex, streamNum)
	}
	if e.Offset < 0 || e.Offset >= int64(len(r.data)) {
		return nil, fmt.Errorf("%w: ObjStm %d offset out of range", errLex, streamNum)
	}
	_, gen, obj, _, err := ParseIndirectObjectAt(r.data, int(e.Offset))
	if err != nil {
		return nil, fmt.Errorf("loading ObjStm %d: %w", streamNum, err)
	}
	st, ok := obj.(*Stream)
	if !ok {
		return nil, fmt.Errorf("%w: object %d is not an ObjStm", errLex, streamNum)
	}
	// An ObjStm stream is encrypted like any other; decrypt its raw bytes before
	// decoding. The objects it contains are then plaintext (not re-decrypted).
	if r.decrypt != nil {
		st.Raw = r.decrypt.decryptBytes(streamNum, gen, st.Raw)
	}

	n, ok := st.Dict.GetInt("N")
	if !ok || n < 0 {
		return nil, fmt.Errorf("%w: ObjStm %d missing /N", errLex, streamNum)
	}
	first, ok := st.Dict.GetInt("First")
	if !ok || first < 0 {
		return nil, fmt.Errorf("%w: ObjStm %d missing /First", errLex, streamNum)
	}

	// A first decode is capped only per stream (maxDecoded); decoding an
	// evicted ObjStm again is capped at what is left of the re-decode budget.
	limit := int64(math.MaxInt64)
	redecode := r.evicted[streamNum]
	if redecode {
		limit = max(r.redecodeCap-r.redecoded, 0)
	}
	decoded, produced, err := DecodeStreamLimit(st, limit)
	if redecode {
		r.redecoded += max(produced, int64(len(decoded))) // an unfiltered ObjStm costs its length
		if errors.Is(err, ErrDecodeLimit) || (err == nil && r.redecoded > r.redecodeCap) {
			return nil, fmt.Errorf("%w: ObjStm %d: evicted from the object-stream cache and the re-decode budget (%d bytes) is spent", errLex, streamNum, r.redecodeCap)
		}
	}
	if err != nil {
		return nil, err
	}
	// Compare in int64: /First and /N are attacker-controlled and must be
	// bounded by the decoded data before either is narrowed to int or sizes an
	// allocation (SECURITY.md §2).
	if int64(first) > int64(len(decoded)) {
		return nil, fmt.Errorf("%w: ObjStm %d /First past end", errLex, streamNum)
	}
	// Each of the N objects needs its "objNum offset" pair (≥ 3 bytes plus a
	// separator) and a body of ≥ 1 byte, so a well-formed stream holds at least
	// 4N bytes. A larger /N cannot be satisfied; rejecting it here keeps a
	// hostile /N 2^60 from reaching makeslice.
	if int64(n) > int64(len(decoded))/4 {
		return nil, fmt.Errorf("%w: ObjStm %d /N %d exceeds its data", errLex, streamNum, n)
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

	return &objStm{decoded: decoded, first: int(first), offsets: offsets}, nil
}

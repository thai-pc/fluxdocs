package parse

import (
	"fmt"
	"sync"
)

const maxRefChain = 32 // bound a reference->reference loop (SECURITY.md §2)

// Resolver pairs the file bytes with the cross-reference table to resolve a
// Reference into a concrete object. Parsed objects are cached by object number,
// and decoded compressed object streams (ObjStm) are cached by their object
// number.
//
// A Resolver is safe for concurrent use: RenderAllPages resolves objects from
// several goroutines at once (one per page). The mutex serializes the caches;
// the file bytes and xref table are immutable after NewResolver.
type Resolver struct {
	data    []byte
	xref    *Xref
	mu      sync.Mutex // guards cache and objstms
	cache   map[int]Object
	objstms map[int]*objStm // decoded /ObjStm, keyed by stream object number
}

// NewResolver builds the xref table from data and returns a Resolver ready to
// resolve references.
//
// Returns a wrapped errLex if the xref table cannot be built.
func NewResolver(data []byte) (*Resolver, error) {
	x, err := ParseXref(data)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		data:    data,
		xref:    x,
		cache:   make(map[int]Object),
		objstms: make(map[int]*objStm),
	}, nil
}

// Trailer returns the most recent trailer dictionary.
func (r *Resolver) Trailer() Dict { return r.xref.Trailer }

// Bytes returns the original file bytes the resolver was built from. The slice
// is the live buffer; callers must not modify it (incremental-update writers
// append to a copy).
func (r *Resolver) Bytes() []byte { return r.data }

// RootRef returns the /Root reference from the trailer, if the trailer points at
// the catalog by an indirect reference (the normal case).
func (r *Resolver) RootRef() (Reference, bool) {
	ref, ok := r.xref.Trailer["Root"].(Reference)
	return ref, ok
}

// MaxObjectNumber returns the highest object number present in the cross-
// reference table. An incremental update allocates new objects starting at
// MaxObjectNumber()+1.
func (r *Resolver) MaxObjectNumber() int {
	max := 0
	for num := range r.xref.Entries {
		if num > max {
			max = num
		}
	}
	return max
}

// object loads an indirect object by number (via its xref offset). A free or
// nonexistent object resolves to Null (per spec, a reference to a missing
// object is null).
//
// Returns the object, or a wrapped error if parsing at the offset fails.
func (r *Resolver) object(num int) (Object, error) {
	// Serialize cache access and parsing. object() never re-enters itself
	// (objectFromStream/loadObjStm read r.data directly), so a single mutex is
	// deadlock-free.
	r.mu.Lock()
	defer r.mu.Unlock()

	if o, ok := r.cache[num]; ok {
		return o, nil
	}
	e, ok := r.xref.Entries[num]
	if !ok || e.Free {
		return Null{}, nil
	}

	// Compressed object: it lives inside an object stream (ObjStm).
	if e.InObjStm {
		obj, err := r.objectFromStream(e.StreamNum, e.StreamIdx, num)
		if err != nil {
			return nil, err
		}
		r.cache[num] = obj
		return obj, nil
	}

	// Reject an out-of-range offset from a malformed/hostile xref before using
	// it as an index (SECURITY.md §2).
	if e.Offset < 0 || e.Offset >= int64(len(r.data)) {
		return nil, fmt.Errorf("%w: object %d offset %d out of range", errLex, num, e.Offset)
	}
	_, _, obj, _, err := ParseIndirectObjectAt(r.data, int(e.Offset))
	if err != nil {
		return nil, fmt.Errorf("loading object %d: %w", num, err)
	}
	r.cache[num] = obj
	return obj, nil
}

// Resolve follows a Reference (including a reference->reference chain) to the
// concrete object. A non-Reference object is returned unchanged.
//
// Returns a wrapped errLex if the chain exceeds maxRefChain (suspected loop).
func (r *Resolver) Resolve(obj Object) (Object, error) {
	for i := 0; i < maxRefChain; i++ {
		ref, ok := obj.(Reference)
		if !ok {
			return obj, nil
		}
		o, err := r.object(ref.Number)
		if err != nil {
			return nil, err
		}
		obj = o
	}
	return nil, fmt.Errorf("%w: reference chain too deep (suspected loop)", errLex)
}

// ResolveDict resolves obj to a Dict, following a reference if needed. Useful
// when walking the page tree, resources, or the catalog.
//
// Returns (dict, true, nil) on success, (nil, false, nil) if obj is not a
// dictionary, or (nil, false, err) if resolution fails.
func (r *Resolver) ResolveDict(obj Object) (Dict, bool, error) {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil, false, err
	}
	switch v := resolved.(type) {
	case Dict:
		return v, true, nil
	case *Stream:
		return v.Dict, true, nil // a stream also carries a dictionary
	default:
		return nil, false, nil
	}
}

// Catalog returns the document catalog (the trailer's /Root, resolved).
//
// Returns a wrapped errLex if /Root is missing or does not point to a
// dictionary.
func (r *Resolver) Catalog() (Dict, error) {
	root, ok := r.xref.Trailer["Root"]
	if !ok {
		return nil, fmt.Errorf("%w: trailer missing /Root", errLex)
	}
	cat, ok, err := r.ResolveDict(root)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: /Root does not point to a dictionary", errLex)
	}
	return cat, nil
}

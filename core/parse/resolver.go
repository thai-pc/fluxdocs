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
// Failures are cached too (errs, objstmErrs): an object that fails to load
// fails the same way every time, since loading depends only on the immutable
// file bytes, the xref table and the decryption state (EnableDecryption clears
// these caches when it changes that). Without them every reference to a broken
// object redid its work — re-parsing a stream that runs to the end of a large
// file, or re-inflating an ObjStm whose /First is bad — so a page that Do'd it
// repeatedly cost that work per Do, outside the render budget.
//
// A Resolver is safe for concurrent use: RenderAllPages resolves objects from
// several goroutines at once (one per page). The mutex serializes the caches;
// the file bytes and xref table are immutable after NewResolver.
type Resolver struct {
	data       []byte
	xref       *Xref
	mu         sync.Mutex // guards the caches and counters below
	cache      map[int]Object
	errs       map[int]error   // object() failures, keyed by object number
	objstms    map[int]*objStm // decoded /ObjStm, keyed by stream object number
	objstmErrs map[int]error   // loadObjStm failures, keyed by stream object number

	// loads counts offset parses and ObjStm loads actually performed (cache
	// misses), for tests that check the caches.
	loads int

	// decrypt, when non-nil, decrypts the strings and streams of every offset-
	// loaded object (and the ObjStm payloads) as they are read. It is installed
	// by EnableDecryption and never applied to the /Encrypt object itself.
	decrypt    *decryptor
	encryptNum int // object number of /Encrypt (never decrypted); 0 if none
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
		data:       data,
		xref:       x,
		cache:      make(map[int]Object),
		errs:       make(map[int]error),
		objstms:    make(map[int]*objStm),
		objstmErrs: make(map[int]error),
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
// Returns the object, or a wrapped error if parsing at the offset fails. Both
// outcomes are cached: a later call returns the same object, or the same error
// value (so errors.Is behaves identically), without redoing the work.
func (r *Resolver) object(num int) (Object, error) {
	// Serialize cache access and parsing. object() never re-enters itself
	// (objectFromStream/loadObjStm read r.data directly), so a single mutex is
	// deadlock-free.
	r.mu.Lock()
	defer r.mu.Unlock()

	if o, ok := r.cache[num]; ok {
		return o, nil
	}
	if err, ok := r.errs[num]; ok {
		return nil, err
	}
	obj, err := r.loadObject(num)
	if err != nil {
		r.errs[num] = err
		return nil, err
	}
	r.cache[num] = obj
	return obj, nil
}

// loadObject does object's work on a cache miss; r.mu is held. Every failure
// it returns is deterministic for the file and decryption state (it reads no
// other state and does not recurse through Resolve), so object caches it.
func (r *Resolver) loadObject(num int) (Object, error) {
	e, ok := r.xref.Entries[num]
	if !ok || e.Free {
		return Null{}, nil
	}

	// Compressed object: it lives inside an object stream (ObjStm).
	if e.InObjStm {
		return r.objectFromStream(e.StreamNum, e.StreamIdx, num)
	}

	// Reject an out-of-range offset from a malformed/hostile xref before using
	// it as an index (SECURITY.md §2).
	if e.Offset < 0 || e.Offset >= int64(len(r.data)) {
		return nil, fmt.Errorf("%w: object %d offset %d out of range", errLex, num, e.Offset)
	}
	r.loads++
	_, gen, obj, _, err := ParseIndirectObjectAt(r.data, int(e.Offset))
	if err != nil {
		return nil, fmt.Errorf("loading object %d: %w", num, err)
	}
	// Decrypt the object's strings/streams, except the /Encrypt object, whose
	// own strings (/O, /U) are stored unencrypted (ISO 32000 §7.6.1).
	if r.decrypt != nil && num != r.encryptNum {
		obj = r.decrypt.decryptObject(num, gen, obj)
	}
	return obj, nil
}

// EnableDecryption sets up the Standard Security Handler for an encrypted
// document using password (nil/empty for the common case). It is a no-op when
// the trailer has no /Encrypt. It must be called right after NewResolver, before
// any content object is resolved, so the cache holds decrypted objects.
//
// Returns an error if the handler is unsupported or the password is wrong.
func (r *Resolver) EnableDecryption(password []byte) error {
	encRef, ok := r.xref.Trailer["Encrypt"]
	if !ok {
		return nil // not encrypted
	}
	if ref, ok := encRef.(Reference); ok {
		r.encryptNum = ref.Number
	}
	enc, err := r.Resolve(encRef) // resolved (and cached) before r.decrypt is set
	if err != nil {
		return err
	}
	encDict, ok := enc.(Dict)
	if !ok {
		return fmt.Errorf("%w: /Encrypt is not a dictionary", errLex)
	}
	var id []byte
	if idArr, ok := r.xref.Trailer["ID"].(Array); ok && len(idArr) > 0 {
		id = stringValue(idArr[0])
	}
	d, err := newDecryptor(encDict, id, password)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.decrypt = d
	// An ObjStm decodes differently once decrypted, so a failure cached while
	// resolving /Encrypt (before r.decrypt was set) may no longer hold: drop
	// the failure caches. Only the /Encrypt chain can have been resolved so far.
	clear(r.errs)
	clear(r.objstmErrs)
	r.mu.Unlock()
	return nil
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

package parse

import (
	"container/list"
	"fmt"
	"sync"
)

const maxRefChain = 32 // bound a reference->reference loop (SECURITY.md §2)

const (
	// maxObjStmCache bounds the decoded object-stream bytes a Resolver keeps
	// (SECURITY.md §2): each ObjStm's decoded data plus its offset table. Past
	// it the least recently used ObjStms are evicted. Without it every decoded
	// ObjStm stayed cached for the document's lifetime, so N distinct 256 MiB
	// ObjStm bombs (≈ 256 KB compressed each) held N × 256 MiB. It admits one
	// ObjStm at the per-stream decode cap. Real files are far below it: a
	// 2000-page Ghostscript file holds 41 ObjStms totalling ≈ 410 KB, and no
	// file in testing/corpus holds more than 3 KB.
	maxObjStmCache = 256 << 20
	// maxObjStmRedecode bounds, over a Resolver's lifetime, the bytes decoded
	// again for ObjStms the cache evicted. Objects are cached once parsed, so
	// an evicted ObjStm is decoded again only for an object not loaded yet; a
	// file that rotates object loads across more than maxObjStmCache of
	// ObjStms would otherwise re-inflate up to 256 MiB per object. Once it is
	// spent, loading from an evicted ObjStm fails. It is the work of refilling
	// the cache once (≈ 0.3–0.5 s of inflate natively).
	maxObjStmRedecode = 256 << 20
)

// Resolver pairs the file bytes with the cross-reference table to resolve a
// Reference into a concrete object. Parsed objects are cached by object number,
// and decoded compressed object streams (ObjStm) are cached by their object
// number, up to maxObjStmCache bytes, least recently used evicted first.
//
// Failures are cached too (errs, objstmErrs): an object that fails to load
// fails the same way every time, since loading depends only on the immutable
// file bytes, the xref table and the decryption state (EnableDecryption clears
// these caches when it changes that), or on a spent ObjStm re-decode budget,
// which never refills. Without them every reference to a broken
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
	errs       map[int]error         // object() failures, keyed by object number
	lengths    map[int]int           // directLength results by object number; -1 for none
	objstms    map[int]*list.Element // decoded /ObjStm (*cachedObjStm in objstmLRU), keyed by stream object number
	objstmErrs map[int]error         // loadObjStm failures, keyed by stream object number

	// The ObjStm cache bound (maxObjStmCache) and the re-decode budget
	// (maxObjStmRedecode). objstmLRU holds the cached ObjStms, most recently
	// used first; objstmBytes is the sum of their sizes. evicted records the
	// ObjStms dropped from the cache: decoding one again is charged to
	// redecoded. The caps are fields so tests can lower them.
	objstmLRU   *list.List
	objstmBytes int64
	objstmCap   int64
	evicted     map[int]bool
	redecoded   int64
	redecodeCap int64

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
		data:        data,
		xref:        x,
		cache:       make(map[int]Object),
		errs:        make(map[int]error),
		lengths:     make(map[int]int),
		objstms:     make(map[int]*list.Element),
		objstmErrs:  make(map[int]error),
		objstmLRU:   list.New(),
		objstmCap:   maxObjStmCache,
		evicted:     make(map[int]bool),
		redecodeCap: maxObjStmRedecode,
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
	_, gen, obj, _, err := parseIndirectObjectAt(r.data, int(e.Offset), r.directLength)
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

// directLength resolves an indirect stream /Length: the integer object ref
// names, when it is an uncompressed object written exactly as
// "N G obj <integer> endobj" with ref's number and generation (a length in an
// object stream, or anything else, is left to the endstream scan). It reads
// only those tokens at the object's xref offset — never a whole object, so a
// /Length that names a huge array or a stream costs a few tokens — and caches
// the result per object number, failures included. It does not recurse into
// object, so it is safe while r.mu is held (it must be held: it uses
// r.lengths); integers are never encrypted. Returns false when ref is not
// such an object.
func (r *Resolver) directLength(ref Reference) (int, bool) {
	if n, ok := r.lengths[ref.Number]; ok {
		return n, n >= 0
	}
	n := r.readDirectLength(ref)
	r.lengths[ref.Number] = n
	return n, n >= 0
}

// readDirectLength is directLength's uncached read; -1 for none.
func (r *Resolver) readDirectLength(ref Reference) int {
	e, ok := r.xref.Entries[ref.Number]
	if !ok || e.Free || e.InObjStm || e.Offset < 0 || e.Offset >= int64(len(r.data)) {
		return -1
	}
	pos := int(e.Offset)
	next := func(kind tokenKind) (token, bool) {
		t, ni, err := nextToken(r.data, pos)
		if err != nil || t.kind != kind {
			return token{}, false
		}
		pos = ni
		return t, true
	}
	num, ok1 := next(tokInt)
	gen, ok2 := next(tokInt)
	kw, ok3 := next(tokKeyword)
	val, ok4 := next(tokInt)
	end, ok5 := next(tokKeyword)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || kw.kw != "obj" || end.kw != "endobj" ||
		num.int != int64(ref.Number) || gen.int != int64(ref.Generation) ||
		val.int < 0 || val.int > int64(len(r.data)) {
		return -1
	}
	return int(val.int)
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
	// The eviction record and the re-decode count carry over: they count work
	// done, and a spent re-decode budget stays spent.
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

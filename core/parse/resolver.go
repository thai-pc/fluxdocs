package parse

import (
	"container/list"
	"crypto/sha256"
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

	// repair, when non-nil, records that the object table was rebuilt rather
	// than read as the cross-reference data says (NewResolverShifted,
	// NewRepairedResolver). Immutable after construction.
	repair *RepairInfo
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
	return newResolver(data, x), nil
}

// newResolver returns a Resolver over data and the object table x, with
// empty caches.
func newResolver(data []byte, x *Xref) *Resolver {
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
	}
}

// NewResolverShifted is NewResolver for a file whose cross-reference data
// measures every offset from its "%PDF-" header instead of from the start of
// the file: shift bytes (the header's position) precede the header, so
// startxref, /Prev, /XRefStm and every in-use entry are read shift bytes
// later (the header-offset repair, docs/design/xref-reconstruction.md §2.2).
// The file's own revision structure (free entries, the /Prev chain) is kept.
// Repair reports the shift.
//
// Returns a wrapped errLex if shift is not inside data or the shifted table
// cannot be built.
func NewResolverShifted(data []byte, shift int) (*Resolver, error) {
	if shift <= 0 || shift >= len(data) {
		return nil, fmt.Errorf("%w: header offset %d out of range", errLex, shift)
	}
	x, err := parseXref(data, shift)
	if err != nil {
		return nil, err
	}
	objects := 0
	for _, e := range x.Entries {
		if !e.Free {
			objects++
		}
	}
	r := newResolver(data, x)
	r.repair = &RepairInfo{HeaderOffset: shift, Objects: objects}
	return r, nil
}

// Repair returns how the resolver's object table was rebuilt, and true; it
// returns false for a resolver built by NewResolver from the file's own
// cross-reference data. A repaired resolver's Bytes must never be extended by
// an incremental update (docs/design/xref-reconstruction.md §5, rule R): the
// update would keep the damaged revisions another reader may rebuild
// differently.
func (r *Resolver) Repair() (RepairInfo, bool) {
	if r.repair == nil {
		return RepairInfo{}, false
	}
	return *r.repair, true
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
	got, gen, obj, _, err := parseIndirectObjectAt(r.data, int(e.Offset), r.directLength)
	if err != nil {
		return nil, fmt.Errorf("loading object %d: %w", num, err)
	}
	// A repaired table (a shifted one above all) can be off by a few bytes and
	// land on a neighbour: an object written under another number is not this
	// one. The strict table keeps today's lenient reading.
	if r.repair != nil && got != num {
		return nil, fmt.Errorf("%w: object %d: its offset holds object %d", errLex, num, got)
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
// document using password as bytes (nil/empty for the common case): for R2–R4
// the bytes Algorithm 2 pads, for R5/R6 the bytes as they are, truncated to
// 127 and not SASLprep'd (EnableDecryptionPassword prepares a typed
// password). The password is tried as the user password, then as the owner
// password. It is a no-op when the trailer has no /Encrypt. It must be called
// right after NewResolver, before any content object is resolved, so the
// cache holds decrypted objects. On failure the resolver is unchanged and it
// may be called again with another password.
//
// Returns a wrapped ErrWrongPassword if the password is neither the user nor
// the owner password, a wrapped ErrEncryptionTampered if an AES-256 file key
// does not authenticate (/Perms), or another wrapped errLex if the handler or
// its dictionary is unsupported or malformed. No message holds the password
// or document bytes.
func (r *Resolver) EnableDecryption(password []byte) error {
	// A copy: the candidates are cleared after use, the caller's bytes not.
	return r.enableDecryption(func(Dict) [][]byte { return [][]byte{append([]byte(nil), password...)} }, nil)
}

// EnableDecryptionPassword is EnableDecryption for a password typed as text.
// It encodes it as each revision expects (aes256-decryption.md §4): for
// R5/R6, the SASLprep form (RFC 4013) as UTF-8 truncated to 127 bytes, then,
// if that fails both tests and differs, the raw UTF-8 bytes (some writers
// skip SASLprep; a password SASLprep refuses is tried raw only); for R2–R4,
// the PDFDocEncoding bytes, then the raw UTF-8 bytes when they differ (or
// only those, when PDFDocEncoding cannot represent the text). The empty
// password is tried as it is. The encoded bytes are cleared after use (best
// effort: Go cannot clear the caller's string).
//
// Returns EnableDecryption's errors.
func (r *Resolver) EnableDecryptionPassword(password string) error {
	return r.EnableDecryptionPasswordShared(password, nil)
}

// DecryptionCache carries the key derivation of one open across the
// resolvers of its repair ladder (core.OpenBytes builds up to three, one per
// rung, for the same file and password). The zero value is ready to use. It
// holds the derived decryptor — the file key — for the encryption dictionary
// last seen, and one AES-256 hash budget (v5MaxHashes) for all its uses: a
// rung that meets the same dictionary and file identifier reuses the result,
// key or error, with no hash at all; a rung that meets a different one
// derives again within what is left of the budget, and fails closed when it
// is spent. So one open costs at most v5MaxHashes hashes, whatever the
// ladder does. Not safe for concurrent use; it lives for one open only.
//
// The cache is keyed by the encryption dictionary and file identifier, not
// by the password: use one cache per password. Reusing it with a different
// password returns the first password's result without checking the second.
type DecryptionCache struct {
	hasher v5Hasher
	have   bool
	fp     [sha256.Size]byte
	d      *decryptor
	err    error
}

// EnableDecryptionPasswordShared is EnableDecryptionPassword that shares the
// key derivation and the hash budget of one open through c (nil: none).
//
// Returns EnableDecryption's errors; a wrapped errLex when the shared hash
// budget is spent.
func (r *Resolver) EnableDecryptionPasswordShared(password string, c *DecryptionCache) error {
	return r.enableDecryption(func(enc Dict) [][]byte { return passwordCandidates(enc, password) }, c)
}

// passwordCandidates returns the byte encodings of password to try, in
// order, for the handler of enc (EnableDecryptionPassword).
func passwordCandidates(enc Dict, password string) [][]byte {
	if password == "" {
		return [][]byte{nil}
	}
	raw := []byte(password)
	if v, _ := enc.GetInt("V"); v == 5 {
		raw = raw[:min(len(raw), v5MaxPassword)]
		prep, err := saslprep(password)
		if err != nil {
			return [][]byte{raw}
		}
		p := []byte(prep)
		p = p[:min(len(p), v5MaxPassword)]
		if string(p) == string(raw) {
			return [][]byte{p}
		}
		return [][]byte{p, raw}
	}
	if pd, ok := pdfDocPassword(password); ok && string(pd) != string(raw) {
		return [][]byte{pd, raw}
	}
	return [][]byte{raw}
}

// Decryption returns how EnableDecryption opened the document, and true; it
// returns false when decryption is not enabled (the document is not
// encrypted, or EnableDecryption was not called or failed).
func (r *Resolver) Decryption() (DecryptionInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.decrypt == nil {
		return DecryptionInfo{}, false
	}
	return r.decrypt.info, true
}

// enableDecryption is EnableDecryption with the candidate password bytes
// chosen once the encryption dictionary is read; they are cleared on return.
// With a cache, an identical dictionary and file identifier reuse its result.
func (r *Resolver) enableDecryption(candidates func(enc Dict) [][]byte, cache *DecryptionCache) error {
	encRef, ok := r.xref.Trailer["Encrypt"]
	if !ok {
		return nil // not encrypted
	}
	if ref, ok := encRef.(Reference); ok {
		r.encryptNum = ref.Number
	}
	enc, err := r.Resolve(encRef) // resolved (and cached) before r.decrypt is set
	if err != nil {
		// The cause can quote document bytes (a keyword, a character), and
		// core.OpenBytes passes this message on: report the failure only.
		return fmt.Errorf("%w: /Encrypt does not resolve", errLex)
	}
	encDict, ok := enc.(Dict)
	if !ok {
		return fmt.Errorf("%w: /Encrypt is not a dictionary", errLex)
	}
	var id []byte
	if idArr, ok := r.xref.Trailer["ID"].(Array); ok && len(idArr) > 0 {
		id = stringValue(idArr[0])
	}
	var fp [sha256.Size]byte
	hasher := &v5Hasher{}
	if cache != nil {
		// The result depends only on the dictionary, the identifier and the
		// password, which is the same for every use of one cache.
		h := sha256.New()
		h.Write(SerializeObject(encDict))
		h.Write([]byte{0})
		h.Write(id)
		h.Sum(fp[:0])
		if cache.have && cache.fp == fp {
			if cache.err != nil {
				return cache.err
			}
			return r.installDecryptor(cache.d)
		}
		hasher = &cache.hasher
	}
	cands := candidates(encDict)
	defer func() {
		for _, c := range cands {
			clear(c)
		}
	}()
	d, err := newDecryptorFor(encDict, id, cands, hasher)
	if cache != nil {
		cache.have, cache.fp, cache.d, cache.err = true, fp, d, err
	}
	if err != nil {
		return err
	}
	return r.installDecryptor(d)
}

// installDecryptor makes d the resolver's decryptor. d is read-only once
// built, so resolvers of one open may share it.
func (r *Resolver) installDecryptor(d *decryptor) error {
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

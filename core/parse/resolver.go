package parse

import "fmt"

const maxRefChain = 32 // bound a reference->reference loop (SECURITY.md §2)

// Resolver pairs the file bytes with the cross-reference table to resolve a
// Reference into a concrete object. Parsed objects are cached by object number.
type Resolver struct {
	data  []byte
	xref  *Xref
	cache map[int]Object
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
	return &Resolver{data: data, xref: x, cache: make(map[int]Object)}, nil
}

// Trailer returns the most recent trailer dictionary.
func (r *Resolver) Trailer() Dict { return r.xref.Trailer }

// object loads an indirect object by number (via its xref offset). A free or
// nonexistent object resolves to Null (per spec, a reference to a missing
// object is null).
//
// Returns the object, or a wrapped error if parsing at the offset fails.
func (r *Resolver) object(num int) (Object, error) {
	if o, ok := r.cache[num]; ok {
		return o, nil
	}
	e, ok := r.xref.Entries[num]
	if !ok || e.Free {
		return Null{}, nil
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

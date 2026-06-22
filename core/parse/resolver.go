package parse

import "fmt"

const maxRefChain = 32 // chặn chuỗi reference->reference vòng lặp (SECURITY.md §2)

// Resolver ghép byte file với cross-reference table để giải Reference thành
// object cụ thể. Object đã parse được cache theo số hiệu.
type Resolver struct {
	data  []byte
	xref  *Xref
	cache map[int]Object
}

// NewResolver dựng xref table từ data và trả Resolver sẵn sàng giải reference.
func NewResolver(data []byte) (*Resolver, error) {
	x, err := ParseXref(data)
	if err != nil {
		return nil, err
	}
	return &Resolver{data: data, xref: x, cache: make(map[int]Object)}, nil
}

// Trailer trả trailer dictionary mới nhất.
func (r *Resolver) Trailer() Dict { return r.xref.Trailer }

// object nạp indirect object theo số hiệu (qua offset trong xref). Object free
// hoặc không tồn tại trả Null (theo spec, reference tới object không có = null).
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
		return nil, fmt.Errorf("nạp object %d: %w", num, err)
	}
	r.cache[num] = obj
	return obj, nil
}

// Resolve đi theo Reference (kể cả chuỗi reference->reference) tới object cụ
// thể. Object không phải Reference được trả nguyên trạng.
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
	return nil, fmt.Errorf("%w: chuỗi reference quá sâu (nghi vòng lặp)", errLex)
}

// ResolveDict giải obj về Dict (đi qua reference nếu cần). Hữu ích khi duyệt
// page tree, resources, catalog.
func (r *Resolver) ResolveDict(obj Object) (Dict, bool, error) {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil, false, err
	}
	switch v := resolved.(type) {
	case Dict:
		return v, true, nil
	case *Stream:
		return v.Dict, true, nil // stream cũng có phần dict
	default:
		return nil, false, nil
	}
}

// Catalog trả document catalog (trailer /Root đã giải).
func (r *Resolver) Catalog() (Dict, error) {
	root, ok := r.xref.Trailer["Root"]
	if !ok {
		return nil, fmt.Errorf("%w: trailer thiếu /Root", errLex)
	}
	cat, ok, err := r.ResolveDict(root)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: /Root không trỏ tới dictionary", errLex)
	}
	return cat, nil
}

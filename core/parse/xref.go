package parse

import (
	"bytes"
	"fmt"
)

// XrefEntry là một mục trong cross-reference table.
type XrefEntry struct {
	Offset int64 // offset byte tới indirect object (type 'n')
	Gen    int
	Free   bool // type 'f' — object đã bị xóa
}

// Xref là cross-reference table đã merge theo chuỗi /Prev (bản mới nhất thắng),
// kèm trailer dictionary mới nhất.
type Xref struct {
	Entries map[int]XrefEntry
	Trailer Dict
}

const maxXrefChain = 64 // chặn vòng lặp /Prev độc hại (SECURITY.md §2)

// FindStartXref đọc ngược từ cuối file tìm "startxref" và trả offset của xref
// section mới nhất.
func FindStartXref(data []byte) (int, error) {
	idx := bytes.LastIndex(data, []byte("startxref"))
	if idx < 0 {
		return 0, fmt.Errorf("%w: thiếu 'startxref'", errLex)
	}
	t, _, err := nextToken(data, idx+len("startxref"))
	if err != nil || t.kind != tokInt {
		return 0, fmt.Errorf("%w: 'startxref' không theo sau bởi offset", errLex)
	}
	return int(t.int), nil
}

// ParseXref dựng cross-reference table đầy đủ: bắt đầu từ startxref rồi đi theo
// /Prev. Chỉ hỗ trợ xref table cổ điển; xref stream (PDF 1.5+) là việc về sau.
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
		// Bản mới nhất thắng: chỉ thêm entry chưa có.
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
	return nil, fmt.Errorf("%w: chuỗi /Prev quá dài (nghi vòng lặp)", errLex)
}

// parseXrefSection parse một "xref ... trailer << >>" tại offset off. Trả entry,
// trailer, và offset /Prev (-1 nếu không có).
func parseXrefSection(data []byte, off int) (map[int]XrefEntry, Dict, int, error) {
	t, pos, err := nextToken(data, off)
	if err != nil {
		return nil, nil, -1, err
	}
	if t.kind != tokKeyword || t.kw != "xref" {
		return nil, nil, -1, fmt.Errorf("%w: xref stream chưa hỗ trợ (offset %d không phải 'xref')", errLex, off)
	}

	entries := make(map[int]XrefEntry)
	for {
		// Header subsection "start count", hoặc 'trailer' để kết thúc.
		ht, hp, err := nextToken(data, pos)
		if err != nil {
			return nil, nil, -1, err
		}
		if ht.kind == tokKeyword && ht.kw == "trailer" {
			pos = hp
			break
		}
		if ht.kind != tokInt {
			return nil, nil, -1, fmt.Errorf("%w: xref subsection header không hợp lệ", errLex)
		}
		ct, cp, err := nextToken(data, hp)
		if err != nil || ct.kind != tokInt {
			return nil, nil, -1, fmt.Errorf("%w: xref subsection thiếu count", errLex)
		}
		startNum, count := int(ht.int), int(ct.int)
		pos = cp

		for k := 0; k < count; k++ {
			offTok, p1, err := nextToken(data, pos)
			if err != nil || offTok.kind != tokInt {
				return nil, nil, -1, fmt.Errorf("%w: xref entry thiếu offset", errLex)
			}
			genTok, p2, err := nextToken(data, p1)
			if err != nil || genTok.kind != tokInt {
				return nil, nil, -1, fmt.Errorf("%w: xref entry thiếu generation", errLex)
			}
			typeTok, p3, err := nextToken(data, p2)
			if err != nil || typeTok.kind != tokKeyword || (typeTok.kw != "n" && typeTok.kw != "f") {
				return nil, nil, -1, fmt.Errorf("%w: xref entry thiếu loại n/f", errLex)
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

	// trailer dictionary.
	p := &iparser{data: data, pos: pos}
	tobj, err := p.parseValue()
	if err != nil {
		return nil, nil, -1, fmt.Errorf("%w: trailer không parse được: %v", errLex, err)
	}
	trailer, ok := tobj.(Dict)
	if !ok {
		return nil, nil, -1, fmt.Errorf("%w: trailer không phải dictionary", errLex)
	}

	prev := -1
	if pv, ok := trailer.GetInt("Prev"); ok {
		prev = int(pv)
	}
	return entries, trailer, prev, nil
}

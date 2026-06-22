package parse

import (
	"bytes"
	"fmt"
)

// iparser là parser tăng dần theo offset trên []byte gốc, dùng nextToken. Khác
// với parser (dựa trên slice token đã tokenize sẵn), iparser đọc được file có
// stream nhị phân vì nó dừng đúng tại từ khóa `stream` rồi cắt byte thô.
type iparser struct {
	data []byte
	pos  int
}

// tryReference khớp mẫu "int int R" tại vị trí hiện tại. Chỉ commit (dời pos)
// khi khớp đủ; nếu không, pos giữ nguyên để parseValue đọc lại như số thường.
func (p *iparser) tryReference() (Reference, bool) {
	t0, n0, err := nextToken(p.data, p.pos)
	if err != nil || t0.kind != tokInt {
		return Reference{}, false
	}
	t1, n1, err := nextToken(p.data, n0)
	if err != nil || t1.kind != tokInt {
		return Reference{}, false
	}
	t2, n2, err := nextToken(p.data, n1)
	if err != nil || t2.kind != tokKeyword || t2.kw != "R" {
		return Reference{}, false
	}
	p.pos = n2
	return Reference{Number: int(t0.int), Generation: int(t1.int)}, true
}

func (p *iparser) parseValue() (Object, error) {
	if ref, ok := p.tryReference(); ok {
		return ref, nil
	}

	t, ni, err := nextToken(p.data, p.pos)
	if err != nil {
		return nil, err
	}
	p.pos = ni

	switch t.kind {
	case tokInt:
		return Integer(t.int), nil
	case tokReal:
		return Real(t.real), nil
	case tokString:
		return String{Value: t.str, Hex: t.hex}, nil
	case tokName:
		return Name(t.name), nil
	case tokArrayOpen:
		return p.parseArray()
	case tokDictOpen:
		return p.parseDictOrStream()
	case tokKeyword:
		switch t.kw {
		case "true":
			return Boolean(true), nil
		case "false":
			return Boolean(false), nil
		case "null":
			return Null{}, nil
		default:
			return nil, fmt.Errorf("%w: từ khóa không mong đợi %q", errLex, t.kw)
		}
	case tokEOF:
		return nil, fmt.Errorf("%w: hết input khi đang chờ object", errLex)
	default:
		return nil, fmt.Errorf("%w: token không mong đợi", errLex)
	}
}

func (p *iparser) parseArray() (Object, error) {
	arr := Array{}
	for {
		t, ni, err := nextToken(p.data, p.pos)
		if err != nil {
			return nil, err
		}
		switch t.kind {
		case tokArrayClose:
			p.pos = ni
			return arr, nil
		case tokEOF:
			return nil, fmt.Errorf("%w: mảng không đóng ']'", errLex)
		default:
			obj, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			arr = append(arr, obj)
		}
	}
}

// parseDictOrStream parse dictionary; nếu sau '>>' là từ khóa `stream` thì đọc
// tiếp dữ liệu thô và trả *Stream.
func (p *iparser) parseDictOrStream() (Object, error) {
	d := Dict{}
	for {
		t, ni, err := nextToken(p.data, p.pos)
		if err != nil {
			return nil, err
		}
		switch t.kind {
		case tokDictClose:
			p.pos = ni
			return p.maybeStream(d)
		case tokName:
			p.pos = ni
			val, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			d[Name(t.name)] = val
		case tokEOF:
			return nil, fmt.Errorf("%w: dictionary không đóng '>>'", errLex)
		default:
			return nil, fmt.Errorf("%w: khóa dictionary phải là Name", errLex)
		}
	}
}

// maybeStream kiểm tra từ khóa `stream` ngay sau dictionary. Dữ liệu thô được
// cắt theo /Length nếu là Integer trực tiếp và nhất quán; ngược lại fallback
// quét tới `endstream` (lenient, an toàn với /Length là indirect reference).
func (p *iparser) maybeStream(d Dict) (Object, error) {
	t, ni, err := nextToken(p.data, p.pos)
	if err != nil || t.kind != tokKeyword || t.kw != "stream" {
		return d, nil // dictionary thường
	}

	// Sau 'stream' bắt buộc là CRLF hoặc LF (§7.3.8.1); bỏ qua một EOL.
	start := skipStreamEOL(p.data, ni)

	end := -1
	if length, ok := d.GetInt("Length"); ok && int(length) >= 0 {
		cand := start + int(length)
		if cand <= len(p.data) {
			// Xác thực: sau Length byte (bỏ EOL) phải là 'endstream'.
			probe := skipStreamEOL(p.data, cand)
			if bytes.HasPrefix(p.data[probe:], []byte("endstream")) {
				end = cand
				p.pos = probe + len("endstream")
			}
		}
	}

	if end == -1 { // fallback: quét 'endstream'
		idx := bytes.Index(p.data[start:], []byte("endstream"))
		if idx < 0 {
			return nil, fmt.Errorf("%w: stream không có 'endstream'", errLex)
		}
		end = start + idx
		p.pos = end + len("endstream")
		end = trimTrailingEOL(p.data, start, end) // bỏ EOL ngay trước endstream
	}

	raw := make([]byte, end-start)
	copy(raw, p.data[start:end])
	return &Stream{Dict: d, Raw: raw}, nil
}

func skipStreamEOL(data []byte, i int) int {
	if i < len(data) && data[i] == '\r' {
		i++
		if i < len(data) && data[i] == '\n' {
			i++
		}
	} else if i < len(data) && data[i] == '\n' {
		i++
	}
	return i
}

// trimTrailingEOL lùi end qua đúng một EOL (\r\n, \n hoặc \r) nếu có, nhưng
// không lùi quá start.
func trimTrailingEOL(data []byte, start, end int) int {
	if end > start && data[end-1] == '\n' {
		end--
		if end > start && data[end-1] == '\r' {
			end--
		}
	} else if end > start && data[end-1] == '\r' {
		end--
	}
	return end
}

// ParseIndirectObjectAt parse "num gen obj <object> endobj" bắt đầu tại offset
// off trong data. Trả số hiệu, generation, object và offset ngay sau 'endobj'
// (hoặc sau object nếu thiếu 'endobj' — lenient).
func ParseIndirectObjectAt(data []byte, off int) (num, gen int, obj Object, next int, err error) {
	p := &iparser{data: data, pos: off}

	t0, n0, e := nextToken(data, p.pos)
	if e != nil || t0.kind != tokInt {
		return 0, 0, nil, off, fmt.Errorf("%w: thiếu số hiệu object tại offset %d", errLex, off)
	}
	t1, n1, e := nextToken(data, n0)
	if e != nil || t1.kind != tokInt {
		return 0, 0, nil, off, fmt.Errorf("%w: thiếu generation tại offset %d", errLex, off)
	}
	t2, n2, e := nextToken(data, n1)
	if e != nil || t2.kind != tokKeyword || t2.kw != "obj" {
		return 0, 0, nil, off, fmt.Errorf("%w: thiếu từ khóa 'obj' tại offset %d", errLex, off)
	}

	p.pos = n2
	obj, err = p.parseValue()
	if err != nil {
		return 0, 0, nil, off, err
	}

	// 'endobj' tùy chọn (lenient): bỏ qua nếu có.
	if et, en, ee := nextToken(data, p.pos); ee == nil && et.kind == tokKeyword && et.kw == "endobj" {
		p.pos = en
	}

	return int(t0.int), int(t1.int), obj, p.pos, nil
}

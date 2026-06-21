package parse

import "fmt"

// errStreamUnsupported: bản hiện tại parse được dictionary của stream nhưng
// chưa đọc dữ liệu thô (cần /Length, sẽ thêm cùng xref + indirect object).
var errStreamUnsupported = fmt.Errorf("%w: stream chưa được hỗ trợ ở tầng parse object", errLex)

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

// ParseObject đọc đúng MỘT object trực tiếp từ data: Integer, Real, Boolean,
// Null, String, Name, Array, Dict, hoặc Reference (`n g R`). Token thừa phía
// sau được bỏ qua (lenient). Chưa hỗ trợ stream và định nghĩa indirect object
// (`n g obj … endobj`) — sẽ thêm cùng xref resolver.
func ParseObject(data []byte) (Object, error) {
	toks, err := tokenize(data)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	return p.parseObject()
}

func (p *parser) parseObject() (Object, error) {
	t := p.peek()
	switch t.kind {
	case tokInt:
		// Lookahead: "int int R" = indirect reference.
		if p.pos+2 < len(p.toks) {
			t1, t2 := p.toks[p.pos+1], p.toks[p.pos+2]
			if t1.kind == tokInt && t2.kind == tokKeyword && t2.kw == "R" {
				p.pos += 3
				return Reference{Number: int(t.int), Generation: int(t1.int)}, nil
			}
		}
		p.next()
		return Integer(t.int), nil

	case tokReal:
		p.next()
		return Real(t.real), nil

	case tokString:
		p.next()
		return String{Value: t.str, Hex: t.hex}, nil

	case tokName:
		p.next()
		return Name(t.name), nil

	case tokArrayOpen:
		return p.parseArray()

	case tokDictOpen:
		return p.parseDict()

	case tokKeyword:
		switch t.kw {
		case "true":
			p.next()
			return Boolean(true), nil
		case "false":
			p.next()
			return Boolean(false), nil
		case "null":
			p.next()
			return Null{}, nil
		default:
			return nil, fmt.Errorf("%w: từ khóa không mong đợi %q", errLex, t.kw)
		}

	case tokEOF:
		return nil, fmt.Errorf("%w: hết input khi đang chờ object", errLex)

	default:
		return nil, fmt.Errorf("%w: token không mong đợi khi parse object", errLex)
	}
}

func (p *parser) parseArray() (Object, error) {
	p.next() // bỏ '['
	arr := Array{}
	for {
		switch p.peek().kind {
		case tokArrayClose:
			p.next()
			return arr, nil
		case tokEOF:
			return nil, fmt.Errorf("%w: mảng không đóng ']'", errLex)
		default:
			obj, err := p.parseObject()
			if err != nil {
				return nil, err
			}
			arr = append(arr, obj)
		}
	}
}

func (p *parser) parseDict() (Object, error) {
	p.next() // bỏ '<<'
	d := Dict{}
	for {
		t := p.peek()
		switch t.kind {
		case tokDictClose:
			p.next()
			// Nếu theo sau là từ khóa 'stream' thì đây là một Stream object.
			if nt := p.peek(); nt.kind == tokKeyword && nt.kw == "stream" {
				return nil, errStreamUnsupported
			}
			return d, nil
		case tokName:
			p.next()
			key := Name(t.name)
			val, err := p.parseObject()
			if err != nil {
				return nil, err
			}
			d[key] = val
		case tokEOF:
			return nil, fmt.Errorf("%w: dictionary không đóng '>>'", errLex)
		default:
			return nil, fmt.Errorf("%w: khóa dictionary phải là Name, gặp token khác", errLex)
		}
	}
}

package parse

import (
	"errors"
	"fmt"
	"strconv"
)

// errLex là lỗi cú pháp ở tầng lexer; parser bọc lại thành ErrInvalidPDF ở
// tầng core khi dựng object model thất bại.
var errLex = errors.New("parse: lexer error")

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokInt
	tokReal
	tokString // chuỗi đã decode về byte thô
	tokName   // tên đã bỏ '/' và decode '#xx'
	tokArrayOpen
	tokArrayClose
	tokDictOpen
	tokDictClose
	tokKeyword // true/false/null/R/obj/endobj/stream/... và mọi run ký tự thường
)

type token struct {
	kind tokenKind
	int  int64
	real float64
	str  []byte
	hex  bool
	name string
	kw   string
}

// isWhitespace theo Table 1, §7.2.2 ISO 32000-1.
func isWhitespace(c byte) bool {
	switch c {
	case 0x00, 0x09, 0x0a, 0x0c, 0x0d, 0x20:
		return true
	}
	return false
}

// isDelimiter theo Table 2, §7.2.2 ISO 32000-1.
func isDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool { return !isWhitespace(c) && !isDelimiter(c) }

// nextToken đọc đúng MỘT token bắt đầu từ offset i (đã bỏ qua whitespace và
// comment), trả token cùng offset ngay sau nó. Hết input trả token EOF. Hàm
// thuần (không giữ state) nên cho phép peek/lookahead bằng cách gọi với offset
// trả về mà chưa "commit" — đây là chìa khóa để đọc dữ liệu stream nhị phân:
// parser dừng tokenize ở từ khóa `stream` rồi đọc byte thô theo offset.
func nextToken(data []byte, i int) (token, int, error) {
	n := len(data)

	// Bỏ whitespace và comment.
	for i < n {
		c := data[i]
		if isWhitespace(c) {
			i++
			continue
		}
		if c == '%' {
			for i < n && data[i] != '\n' && data[i] != '\r' {
				i++
			}
			continue
		}
		break
	}
	if i >= n {
		return token{kind: tokEOF}, i, nil
	}

	c := data[i]
	switch {
	case c == '[':
		return token{kind: tokArrayOpen}, i + 1, nil
	case c == ']':
		return token{kind: tokArrayClose}, i + 1, nil

	case c == '<':
		if i+1 < n && data[i+1] == '<' {
			return token{kind: tokDictOpen}, i + 2, nil
		}
		s, ni, err := lexHexString(data, i)
		if err != nil {
			return token{}, i, err
		}
		return token{kind: tokString, str: s, hex: true}, ni, nil

	case c == '>':
		if i+1 < n && data[i+1] == '>' {
			return token{kind: tokDictClose}, i + 2, nil
		}
		return token{}, i, fmt.Errorf("%w: '>' đơn lẻ tại offset %d", errLex, i)

	case c == '(':
		s, ni, err := lexLiteralString(data, i)
		if err != nil {
			return token{}, i, err
		}
		return token{kind: tokString, str: s}, ni, nil

	case c == '/':
		name, ni, err := lexName(data, i)
		if err != nil {
			return token{}, i, err
		}
		return token{kind: tokName, name: name}, ni, nil

	case c == '{' || c == '}':
		return token{kind: tokKeyword, kw: string(c)}, i + 1, nil

	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return lexNumber(data, i)

	default: // run ký tự thường = keyword
		start := i
		for i < n && isRegular(data[i]) {
			i++
		}
		if i == start {
			return token{}, i, fmt.Errorf("%w: ký tự không hợp lệ %q tại offset %d", errLex, c, i)
		}
		return token{kind: tokKeyword, kw: string(data[start:i])}, i, nil
	}
}

// tokenize chuyển toàn bộ byte thành chuỗi token (kết thúc bằng EOF). Dùng cho
// parse object trực tiếp kích thước nhỏ; KHÔNG dùng cho file có stream nhị phân
// (xem nextToken).
func tokenize(data []byte) ([]token, error) {
	var toks []token
	i := 0
	for {
		t, ni, err := nextToken(data, i)
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.kind == tokEOF {
			return toks, nil
		}
		i = ni
	}
}

func lexNumber(data []byte, i int) (token, int, error) {
	start := i
	n := len(data)
	isReal := false
	if i < n && (data[i] == '+' || data[i] == '-') {
		i++
	}
	for i < n {
		c := data[i]
		if c >= '0' && c <= '9' {
			i++
		} else if c == '.' {
			isReal = true
			i++
		} else {
			break
		}
	}
	lit := string(data[start:i])
	if isReal {
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			return token{}, i, fmt.Errorf("%w: số thực không hợp lệ %q", errLex, lit)
		}
		return token{kind: tokReal, real: f}, i, nil
	}
	v, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		// "+" / "-" / "." đơn lẻ: coi như 0 theo tinh thần lenient parsing.
		return token{kind: tokInt, int: 0}, i, nil
	}
	return token{kind: tokInt, int: v}, i, nil
}

func lexName(data []byte, i int) (string, int, error) {
	n := len(data)
	i++ // bỏ '/'
	var out []byte
	for i < n && isRegular(data[i]) {
		c := data[i]
		if c == '#' && i+2 < n && isHex(data[i+1]) && isHex(data[i+2]) {
			out = append(out, hexVal(data[i+1])<<4|hexVal(data[i+2]))
			i += 3
			continue
		}
		out = append(out, c)
		i++
	}
	return string(out), i, nil
}

func lexLiteralString(data []byte, i int) ([]byte, int, error) {
	n := len(data)
	i++ // bỏ '('
	depth := 1
	var out []byte
	for i < n {
		c := data[i]
		switch c {
		case '\\':
			i++
			if i >= n {
				return nil, i, fmt.Errorf("%w: chuỗi literal kết thúc đột ngột", errLex)
			}
			e := data[i]
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r': // line continuation, nuốt cả \r\n
				if i+1 < n && data[i+1] == '\n' {
					i++
				}
			case '\n': // line continuation
			default:
				if e >= '0' && e <= '7' { // octal \ddd (1-3 chữ số)
					val := int(e - '0')
					for k := 0; k < 2 && i+1 < n && data[i+1] >= '0' && data[i+1] <= '7'; k++ {
						i++
						val = val<<3 | int(data[i]-'0')
					}
					out = append(out, byte(val))
				} else {
					out = append(out, e) // backslash dư: giữ ký tự
				}
			}
			i++
		case '(':
			depth++
			out = append(out, c)
			i++
		case ')':
			depth--
			if depth == 0 {
				i++
				return out, i, nil
			}
			out = append(out, c)
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return nil, i, fmt.Errorf("%w: chuỗi literal không đóng ')'", errLex)
}

func lexHexString(data []byte, i int) ([]byte, int, error) {
	n := len(data)
	i++ // bỏ '<'
	var nibbles []byte
	for i < n {
		c := data[i]
		if c == '>' {
			i++
			if len(nibbles)%2 == 1 { // chữ số lẻ cuối: ngầm thêm 0 (spec §7.3.4.3)
				nibbles = append(nibbles, 0)
			}
			out := make([]byte, len(nibbles)/2)
			for k := 0; k < len(out); k++ {
				out[k] = nibbles[2*k]<<4 | nibbles[2*k+1]
			}
			return out, i, nil
		}
		if isWhitespace(c) {
			i++
			continue
		}
		if !isHex(c) {
			return nil, i, fmt.Errorf("%w: ký tự hex không hợp lệ %q", errLex, c)
		}
		nibbles = append(nibbles, hexVal(c))
		i++
	}
	return nil, i, fmt.Errorf("%w: chuỗi hex không đóng '>'", errLex)
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

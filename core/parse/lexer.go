package parse

import (
	"errors"
	"fmt"
	"strconv"
)

// errLex is a lexer-level syntax error; the core layer wraps it into
// ErrInvalidPDF when building the object model fails.
var errLex = errors.New("parse: lexer error")

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokInt
	tokReal
	tokString // string decoded to raw bytes
	tokName   // name with '/' removed and '#xx' decoded
	tokArrayOpen
	tokArrayClose
	tokDictOpen
	tokDictClose
	tokKeyword // true/false/null/R/obj/endobj/stream/... and any regular-char run
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

// isWhitespace per Table 1, ISO 32000-1 §7.2.2.
func isWhitespace(c byte) bool {
	switch c {
	case 0x00, 0x09, 0x0a, 0x0c, 0x0d, 0x20:
		return true
	}
	return false
}

// isDelimiter per Table 2, ISO 32000-1 §7.2.2.
func isDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool { return !isWhitespace(c) && !isDelimiter(c) }

// nextToken reads exactly ONE token starting at offset i (after skipping
// whitespace and comments) and returns the token plus the offset just past it.
// At end of input it returns an EOF token. The function is pure (holds no
// state), which allows peeking/lookahead by calling it with the returned offset
// without committing. This is the key to reading binary stream data: the parser
// stops tokenizing at the `stream` keyword and then slices raw bytes by offset.
func nextToken(data []byte, i int) (token, int, error) {
	n := len(data)

	// Defensive: a negative offset (e.g. from a malformed xref entry) must not
	// index into data. Treat it as end of input rather than panicking
	// (SECURITY.md §2 — the parser must never crash on hostile input).
	if i < 0 {
		return token{kind: tokEOF}, i, nil
	}

	// Skip whitespace and comments.
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
		return token{}, i, fmt.Errorf("%w: stray '>' at offset %d", errLex, i)

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

	default: // regular-char run = keyword (true/false/null/R/obj/...)
		start := i
		for i < n && isRegular(data[i]) {
			i++
		}
		if i == start {
			return token{}, i, fmt.Errorf("%w: invalid character %q at offset %d", errLex, c, i)
		}
		return token{kind: tokKeyword, kw: string(data[start:i])}, i, nil
	}
}

// tokenize converts all bytes into a token slice (terminated by EOF). It is
// used for parsing small standalone objects; it is NOT for files containing
// binary streams (see nextToken).
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
			return token{}, i, fmt.Errorf("%w: invalid real number %q", errLex, lit)
		}
		return token{kind: tokReal, real: f}, i, nil
	}
	v, err := strconv.ParseInt(lit, 10, 64)
	if err != nil {
		// Lone "+" / "-" / ".": treat as 0 in the spirit of lenient parsing.
		return token{kind: tokInt, int: 0}, i, nil
	}
	return token{kind: tokInt, int: v}, i, nil
}

func lexName(data []byte, i int) (string, int, error) {
	n := len(data)
	i++ // drop '/'
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
	i++ // drop '('
	depth := 1
	var out []byte
	for i < n {
		c := data[i]
		switch c {
		case '\\':
			i++
			if i >= n {
				return nil, i, fmt.Errorf("%w: literal string ended abruptly", errLex)
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
			case '\r': // line continuation, also swallow \r\n
				if i+1 < n && data[i+1] == '\n' {
					i++
				}
			case '\n': // line continuation
			default:
				if e >= '0' && e <= '7' { // octal \ddd (1-3 digits)
					val := int(e - '0')
					for k := 0; k < 2 && i+1 < n && data[i+1] >= '0' && data[i+1] <= '7'; k++ {
						i++
						val = val<<3 | int(data[i]-'0')
					}
					out = append(out, byte(val))
				} else {
					out = append(out, e) // stray backslash: keep the character
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
	return nil, i, fmt.Errorf("%w: unterminated literal string (missing ')')", errLex)
}

func lexHexString(data []byte, i int) ([]byte, int, error) {
	n := len(data)
	i++ // drop '<'
	var nibbles []byte
	for i < n {
		c := data[i]
		if c == '>' {
			i++
			if len(nibbles)%2 == 1 { // odd final digit: implicit trailing 0 (§7.3.4.3)
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
			return nil, i, fmt.Errorf("%w: invalid hex character %q", errLex, c)
		}
		nibbles = append(nibbles, hexVal(c))
		i++
	}
	return nil, i, fmt.Errorf("%w: unterminated hex string (missing '>')", errLex)
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

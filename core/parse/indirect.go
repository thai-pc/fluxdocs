package parse

import (
	"bytes"
	"fmt"
)

// iparser is an offset-incremental parser over the original []byte, driven by
// nextToken. Unlike a slice-of-tokens parser, iparser can read files that
// contain binary streams because it stops exactly at the `stream` keyword and
// then slices the raw bytes itself.
type iparser struct {
	data  []byte
	pos   int
	depth int // arrays/dictionaries currently open; bounded by maxNestingDepth

	// lenient is set only by the content-stream scanner. A container nested
	// past maxNestingDepth is then skipped without recursion and parsed as one
	// Null placeholder (skipOverDeep), instead of failing the enclosing operand
	// — which would discard sibling operands such as the strings of a TJ array
	// that other viewers still render. Object parsing (xref, objstm, indirect
	// objects) leaves it false and rejects over-deep values.
	//
	// In this mode a dictionary is never followed by stream data: streams are
	// always indirect objects (ISO 32000-1 §7.3.8.1) and cannot occur inside a
	// content stream, so `<< >> stream … endstream` there is a dictionary and
	// unknown operators, not opaque bytes that would hide the operators within.
	lenient bool

	// length, when set, resolves an indirect /Length (a reference to an
	// integer object) for a stream: without it the stream's end is found by
	// scanning for endstream, which cannot tell a final CR or LF of the data
	// from the end-of-line before endstream.
	length func(Reference) (int, bool)
}

// maxNestingDepth bounds how deeply arrays and dictionaries may nest in one
// parsed value (SECURITY.md §2), as maxCopyDepth does for the copier. Parsing
// recurses once per level, and a stack overflow is a fatal runtime error that
// no recover can catch: 3,000,000 nested '[' (3 MB) killed the process from
// OpenBytes. Real files nest a handful of levels; 256 is generous.
const maxNestingDepth = 256

// enter opens one nesting level, or returns a wrapped errLex past
// maxNestingDepth. Callers defer p.leave() after a successful enter.
func (p *iparser) enter() error {
	if p.depth >= maxNestingDepth {
		return fmt.Errorf("%w: arrays/dictionaries nested deeper than %d", errLex, maxNestingDepth)
	}
	p.depth++
	return nil
}

func (p *iparser) leave() { p.depth-- }

// skipOverDeep consumes the rest of a container whose opener (open: '[' or
// '<<') was already read, iteratively: it tracks the open containers on an
// explicit byte stack until the opener is closed, so its cost is linear and its
// Go stack use constant however deep the input nests. Strings, hex strings,
// names and comments are lexed whole, so brackets inside them do not count.
//
// It accepts only what parseValue would: a closer must match the innermost
// opener, and the only keywords are true, false, null and R. Anything else —
// an operator, BI (whose binary data is not token-safe), stream, '{' — is an
// error rather than skipped, so a skipped subtree can never hide operators
// that an interpreter running inside arrays would execute.
//
// Returns Null{} with pos just past the matching closer, or a wrapped errLex at
// end of input, on a token error, or on an unexpected token (pos at the
// offending token).
func (p *iparser) skipOverDeep(open tokenKind) (Object, error) {
	isDict := []bool{open == tokDictOpen} // explicit stack: one byte per open level
	for len(isDict) > 0 {
		t, ni, err := nextToken(p.data, p.pos)
		if err != nil {
			return nil, err
		}
		switch t.kind {
		case tokEOF:
			return nil, fmt.Errorf("%w: unterminated array or dictionary nested deeper than %d", errLex, maxNestingDepth)
		case tokArrayOpen, tokDictOpen:
			isDict = append(isDict, t.kind == tokDictOpen)
		case tokArrayClose, tokDictClose:
			if isDict[len(isDict)-1] != (t.kind == tokDictClose) {
				return nil, fmt.Errorf("%w: mismatched closing delimiter at offset %d", errLex, p.pos)
			}
			isDict = isDict[:len(isDict)-1]
		case tokKeyword:
			switch t.kw {
			case "true", "false", "null", "R":
			default:
				return nil, fmt.Errorf("%w: unexpected keyword %q", errLex, t.kw)
			}
		}
		p.pos = ni
	}
	return Null{}, nil
}

// tryReference matches the pattern "int int R" at the current position. It
// commits (advances pos) only on a full match; otherwise pos is left unchanged
// so parseValue can re-read the leading integer as a plain number.
//
// Returns the Reference and true on match, or the zero Reference and false.
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

// parseValue reads one object at the current position, advancing pos past it.
// Returns the Object, or a wrapped errLex on syntax error.
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
			return nil, fmt.Errorf("%w: unexpected keyword %q", errLex, t.kw)
		}
	case tokEOF:
		return nil, fmt.Errorf("%w: end of input while expecting an object", errLex)
	default:
		return nil, fmt.Errorf("%w: unexpected token", errLex)
	}
}

// parseArray parses elements until ']'. Assumes the opening '[' was already
// consumed. Nesting is bounded (maxNestingDepth) here rather than in
// parseValue, because the content-stream scanner calls it directly; past the
// bound a lenient parser skips the array as Null (skipOverDeep).
func (p *iparser) parseArray() (Object, error) {
	if err := p.enter(); err != nil {
		if p.lenient {
			return p.skipOverDeep(tokArrayOpen)
		}
		return nil, err
	}
	defer p.leave()
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
			return nil, fmt.Errorf("%w: unterminated array (missing ']')", errLex)
		default:
			obj, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			arr = append(arr, obj)
		}
	}
}

// parseDictOrStream parses a dictionary; if a `stream` keyword follows the
// closing '>>', it reads the raw stream data and returns a *Stream (never in
// lenient, content-stream mode). Assumes the opening '<<' was already consumed.
// Nesting is bounded like parseArray.
func (p *iparser) parseDictOrStream() (Object, error) {
	if err := p.enter(); err != nil {
		if p.lenient {
			return p.skipOverDeep(tokDictOpen)
		}
		return nil, err
	}
	defer p.leave()
	d := Dict{}
	for {
		t, ni, err := nextToken(p.data, p.pos)
		if err != nil {
			return nil, err
		}
		switch t.kind {
		case tokDictClose:
			p.pos = ni
			if p.lenient {
				return d, nil // no streams inside a content stream; see iparser.lenient
			}
			return p.maybeStream(d)
		case tokName:
			p.pos = ni
			val, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			d[Name(t.name)] = val
		case tokEOF:
			return nil, fmt.Errorf("%w: unterminated dictionary (missing '>>')", errLex)
		default:
			return nil, fmt.Errorf("%w: dictionary key must be a Name", errLex)
		}
	}
}

// maybeStream checks for a `stream` keyword right after a dictionary. If absent,
// it returns the dictionary unchanged. If present, raw bytes are sliced by
// /Length when it is a consistent direct Integer; otherwise it falls back to
// scanning for `endstream` (lenient, and safe when /Length is an indirect
// reference).
func (p *iparser) maybeStream(d Dict) (Object, error) {
	t, ni, err := nextToken(p.data, p.pos)
	if err != nil || t.kind != tokKeyword || t.kw != "stream" {
		return d, nil // plain dictionary
	}

	// `stream` must be followed by CRLF or LF (§7.3.8.1); skip one EOL.
	start := skipStreamEOL(p.data, ni)

	end := -1
	// Bound /Length by the remaining input in int64 before adding: a hostile
	// MaxInt64 wrapped start+length negative and indexed data[-n] (SECURITY.md
	// §2). An over-long /Length falls back to the endstream scan below.
	length, ok := d.GetInt("Length")
	if ref, isRef := d["Length"].(Reference); isRef && p.length != nil {
		var n int
		if n, ok = p.length(ref); ok {
			length = Integer(n)
		}
	}
	if ok && length >= 0 && int64(length) <= int64(len(p.data)-start) {
		cand := start + int(length)
		// Validate: after Length bytes (skipping EOL) must come 'endstream'.
		probe := skipStreamEOL(p.data, cand)
		if bytes.HasPrefix(p.data[probe:], []byte("endstream")) {
			end = cand
			p.pos = probe + len("endstream")
		}
	}

	if end == -1 { // fallback: scan for 'endstream'
		idx := bytes.Index(p.data[start:], []byte("endstream"))
		if idx < 0 {
			return nil, fmt.Errorf("%w: stream has no 'endstream'", errLex)
		}
		end = start + idx
		p.pos = end + len("endstream")
		end = trimTrailingEOL(p.data, start, end) // drop the EOL just before endstream
	}

	raw := make([]byte, end-start)
	copy(raw, p.data[start:end])
	return &Stream{Dict: d, Raw: raw}, nil
}

// skipStreamEOL advances past a single EOL (\r\n, \n, or \r) if present.
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

// trimTrailingEOL moves end back over exactly one EOL (\r\n, \n, or \r) if
// present, but never past start.
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

// ParseIndirectObjectAt parses "num gen obj <object> endobj" starting at offset
// off in data.
//
// Returns the object number, generation, the parsed Object, and the offset just
// past 'endobj' (or just past the object if 'endobj' is missing — lenient). On
// failure it returns a wrapped errLex and next == off.
func ParseIndirectObjectAt(data []byte, off int) (num, gen int, obj Object, next int, err error) {
	return parseIndirectObjectAt(data, off, nil)
}

// parseIndirectObjectAt is ParseIndirectObjectAt with an indirect /Length
// resolver for a stream (see iparser.length; nil for none).
func parseIndirectObjectAt(data []byte, off int, length func(Reference) (int, bool)) (num, gen int, obj Object, next int, err error) {
	p := &iparser{data: data, pos: off, length: length}

	t0, n0, e := nextToken(data, p.pos)
	if e != nil || t0.kind != tokInt {
		return 0, 0, nil, off, fmt.Errorf("%w: missing object number at offset %d", errLex, off)
	}
	t1, n1, e := nextToken(data, n0)
	if e != nil || t1.kind != tokInt {
		return 0, 0, nil, off, fmt.Errorf("%w: missing generation at offset %d", errLex, off)
	}
	t2, n2, e := nextToken(data, n1)
	if e != nil || t2.kind != tokKeyword || t2.kw != "obj" {
		return 0, 0, nil, off, fmt.Errorf("%w: missing 'obj' keyword at offset %d", errLex, off)
	}

	p.pos = n2
	obj, err = p.parseValue()
	if err != nil {
		return 0, 0, nil, off, err
	}

	// 'endobj' is optional (lenient): consume it if present.
	if et, en, ee := nextToken(data, p.pos); ee == nil && et.kind == tokKeyword && et.kw == "endobj" {
		p.pos = en
	}

	return int(t0.int), int(t1.int), obj, p.pos, nil
}

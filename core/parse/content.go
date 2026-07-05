package parse

import "bytes"

// This file exposes a content-stream scanner. A PDF content stream (ISO 32000-1
// §7.8.2) is a sequence of operands followed by operators, e.g.
//
//	BT /F1 12 Tf 100 700 Td (Hello) Tj ET
//
// It shares the exact token grammar of the object layer (numbers, strings,
// names, arrays, dicts) plus operators, which are regular-character keywords
// (Tj, TJ, Tf, Td, …). We therefore reuse the lexer and iparser rather than
// duplicate lexing; the only content-specific concern is inline images, whose
// binary data (between the ID and EI operators) is not token-structured.

// ContentOp is one operator together with the operands that preceded it. The
// operands are ordinary PDF objects (Integer, Real, String, Name, Array, …).
type ContentOp struct {
	Op       string
	Operands []Object
}

// ContentOpSpan is a ContentOp plus the byte range [Start,End) it occupies in
// the scanned content stream — from its first operand through the operator.
// Removing that exact range from the original bytes deletes precisely this
// operation and leaves every other byte untouched, which is how redaction
// strips text-showing operators without re-serializing (and risking corruption
// of) the rest of the stream.
type ContentOpSpan struct {
	ContentOp
	Start int
	End   int
}

// ScanContent tokenizes a decoded content stream into a flat list of operators
// with their operands. It is lenient by design: content streams in the wild are
// frequently malformed, so a token error skips a byte rather than aborting, and
// inline images are skipped whole. It never returns an error — a hostile or
// broken stream yields whatever operators could be recovered.
func ScanContent(data []byte) []ContentOp {
	spans := ScanContentSpans(data)
	ops := make([]ContentOp, len(spans))
	for i, s := range spans {
		ops[i] = s.ContentOp
	}
	return ops
}

// ScanContentSpans is ScanContent with each operator's byte range recorded (see
// ContentOpSpan). It is the single implementation; ScanContent projects away the
// spans.
func ScanContentSpans(data []byte) []ContentOpSpan {
	var ops []ContentOpSpan
	var operands []Object
	opStart := -1 // byte offset of the current op group's first token
	p := &iparser{data: data, pos: 0}

	for p.pos < len(data) {
		start := p.pos
		t, ni, err := nextToken(data, p.pos)
		if err != nil {
			p.pos = start + 1 // lenient: skip the offending byte
			operands = nil
			opStart = -1
			continue
		}
		if opStart < 0 && t.kind != tokEOF {
			opStart = start
		}

		switch t.kind {
		case tokEOF:
			return ops

		case tokInt:
			operands = append(operands, Integer(t.int))
			p.pos = ni
		case tokReal:
			operands = append(operands, Real(t.real))
			p.pos = ni
		case tokString:
			operands = append(operands, String{Value: t.str, Hex: t.hex})
			p.pos = ni
		case tokName:
			operands = append(operands, Name(t.name))
			p.pos = ni

		case tokArrayOpen:
			p.pos = ni // parseArray expects '[' already consumed
			arr, err := p.parseArray()
			if err != nil {
				operands = nil
				opStart = -1
				break
			}
			operands = append(operands, arr)
		case tokDictOpen:
			p.pos = ni // parseDictOrStream expects '<<' already consumed
			obj, err := p.parseDictOrStream()
			if err != nil {
				operands = nil
				opStart = -1
				break
			}
			operands = append(operands, obj)

		case tokKeyword:
			switch t.kw {
			case "true":
				operands = append(operands, Boolean(true))
			case "false":
				operands = append(operands, Boolean(false))
			case "null":
				operands = append(operands, Null{})
			case "BI":
				// Inline image: its dictionary + binary data is not token-safe.
				// Skip everything through the closing EI and drop pending operands.
				p.pos = skipInlineImage(data, ni)
				operands = nil
				opStart = -1
				continue
			default: // any other keyword is an operator
				ops = append(ops, ContentOpSpan{
					ContentOp: ContentOp{Op: t.kw, Operands: operands},
					Start:     opStart,
					End:       ni,
				})
				operands = nil
				opStart = -1
			}
			p.pos = ni

		default: // stray ] or >> — resync
			operands = nil
			opStart = -1
			p.pos = ni
		}

		// Progress guard: never spin on a token that failed to advance.
		if p.pos <= start {
			p.pos = start + 1
		}
	}
	return ops
}

// skipInlineImage advances past an inline image body. i points just after the
// BI operator. It scans forward to the ID operator, then treats the bytes that
// follow as opaque until a whitespace-delimited EI is found. Returns the offset
// just past EI (or end of data if EI is missing).
func skipInlineImage(data []byte, i int) int {
	// Walk tokens (the image dictionary) until the ID operator.
	for i < len(data) {
		t, ni, err := nextToken(data, i)
		if err != nil {
			return i + 1
		}
		if t.kind == tokEOF {
			return len(data)
		}
		i = ni
		if t.kind == tokKeyword && t.kw == "ID" {
			break
		}
	}
	// Exactly one whitespace byte separates ID from the image data (§8.9.7).
	if i < len(data) && isWhitespace(data[i]) {
		i++
	}
	// Scan opaque bytes for a whitespace-delimited "EI".
	for i < len(data) {
		j := bytes.IndexByte(data[i:], 'E')
		if j < 0 {
			return len(data)
		}
		i += j
		if i+1 < len(data) && data[i+1] == 'I' {
			prevWS := i == 0 || isWhitespace(data[i-1])
			after := i + 2
			nextOK := after >= len(data) || isWhitespace(data[after]) || isDelimiter(data[after])
			if prevWS && nextOK {
				return after
			}
		}
		i++
	}
	return len(data)
}

// PageContent resolves a page's /Contents (a single stream, an array of
// streams, or a reference to either), decodes each per its /Filter, and returns
// the concatenated content bytes. Streams are joined with a newline so a token
// cannot straddle the boundary between two streams (§7.8.2).
//
// Returns nil (no error) if the page has no content stream.
func (r *Resolver) PageContent(pi PageInfo) ([]byte, error) {
	resolved, err := r.Resolve(pi.Contents)
	if err != nil {
		return nil, err
	}
	switch v := resolved.(type) {
	case *Stream:
		return DecodeStream(v)
	case Array:
		var buf bytes.Buffer
		for _, el := range v {
			ro, err := r.Resolve(el)
			if err != nil {
				return nil, err
			}
			st, ok := ro.(*Stream)
			if !ok {
				continue // skip a non-stream element leniently
			}
			dec, err := DecodeStream(st)
			if err != nil {
				return nil, err
			}
			buf.Write(dec)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), nil
	default:
		return nil, nil
	}
}

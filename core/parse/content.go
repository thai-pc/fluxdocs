package parse

import (
	"bytes"
	"errors"
	"fmt"
)

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
// frequently malformed, so a token error skips a byte rather than aborting, an
// array or dictionary nested past maxNestingDepth becomes one Null operand, and
// inline images are skipped whole. It never returns an error — a hostile or
// broken stream yields whatever operators could be recovered. Rendering and
// text extraction use it; redaction must use ScanContentSpansStrict.
func ScanContent(data []byte) []ContentOp {
	spans := ScanContentSpans(data)
	ops := make([]ContentOp, len(spans))
	for i, s := range spans {
		ops[i] = s.ContentOp
	}
	return ops
}

// ScanContentSpans is ScanContent with each operator's byte range recorded (see
// ContentOpSpan).
func ScanContentSpans(data []byte) []ContentOpSpan {
	ops, _ := scanContent(data, false)
	return ops
}

// ScanContentSpansStrict is ScanContentSpans for redaction, which must fail
// closed (SECURITY.md §1). The lenient scanner recovers from a syntax error by
// dropping the pending operands and resyncing, and it pairs operands with
// operators however they fall; a viewer may pair them differently, so a string
// it renders — e.g. in a TJ array next to a malformed element, or left over
// from an operator with surplus operands — can be invisible to the scan and
// escape redaction. Strict mode scans the same way but reports the first place
// where the scan and an interpreter could disagree instead of hiding it.
//
// Returns the spans, or nil and a wrapped errLex naming the first offset where
// input was discarded or could be read differently: a token error, an operand
// array/dictionary that failed to parse (including an over-deep one that never
// closes or holds a keyword), a stray ']' or '>>', operands pending at an
// inline image or at the end of the stream, an inline image whose dictionary
// is malformed or that has no ID/EI, or an operator whose operands do not
// match ISO 32000-1 Annex A (operandsFit; any operand on an unknown operator).
func ScanContentSpansStrict(data []byte) ([]ContentOpSpan, error) {
	ops, err := scanContent(data, true)
	if err != nil {
		return nil, err
	}
	return ops, nil
}

// scanContent is the single scanner implementation. It returns every operator
// it could recover plus, when it had to discard input to do so, a wrapped
// errLex describing the first discard (nil for a well-formed stream). strict
// adds the operand checks against Annex A, which lenient callers do not need.
func scanContent(data []byte, strict bool) ([]ContentOpSpan, error) {
	var ops []ContentOpSpan
	var operands []Object
	var lossErr error // first discarded input; see ScanContentSpansStrict
	loss := func(off int, what string, cause error) {
		if lossErr != nil {
			return
		}
		if cause != nil {
			lossErr = fmt.Errorf("%w: content stream: %s at offset %d: %v", errLex, what, off, cause)
		} else {
			lossErr = fmt.Errorf("%w: content stream: %s at offset %d", errLex, what, off)
		}
	}
	// resync is where scanning resumes after a parse error at start that left
	// the parser at pos: past a token that ran to end of input there is nothing
	// left to recover, and re-lexing it from each later offset would be
	// quadratic.
	resync := func(err error, pos int) int {
		if errors.Is(err, errTruncated) {
			return len(data)
		}
		return pos
	}
	opStart := -1 // byte offset of the current op group's first token
	p := &iparser{data: data, pos: 0, lenient: true}

	for p.pos < len(data) {
		start := p.pos
		t, ni, err := nextToken(data, p.pos)
		if err != nil {
			loss(start, "token error", err)
			p.pos = resync(err, start+1) // lenient: skip the offending byte
			operands = nil
			opStart = -1
			continue
		}
		if opStart < 0 && t.kind != tokEOF {
			opStart = start
		}

		switch t.kind {
		case tokEOF:
			p.pos = len(data)
			continue

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
				loss(start, "unreadable array operand", err)
				p.pos = resync(err, p.pos)
				operands = nil
				opStart = -1
				break
			}
			operands = append(operands, arr)
		case tokDictOpen:
			p.pos = ni // parseDictOrStream expects '<<' already consumed
			obj, err := p.parseDictOrStream()
			if err != nil {
				loss(start, "unreadable dictionary operand", err)
				p.pos = resync(err, p.pos)
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
				// Skip everything through the closing EI and drop pending operands
				// (BI takes none; an interpreter may keep them for a later operator).
				if len(operands) > 0 {
					loss(start, "operands pending before inline image", nil)
				}
				next, ok := skipInlineImage(data, ni)
				if !ok {
					loss(start, "malformed inline image", nil)
				}
				p.pos = next
				operands = nil
				opStart = -1
				continue
			default: // any other keyword is an operator
				if strict && lossErr == nil && !operandsFit(t.kw, operands) {
					loss(opStart, "operands do not match their operator", nil)
				}
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
			loss(start, "stray closing delimiter", nil)
			operands = nil
			opStart = -1
			p.pos = ni
		}

		// Progress guard: never spin on a token that failed to advance.
		if p.pos <= start {
			p.pos = start + 1
		}
	}
	if len(operands) > 0 {
		loss(opStart, "operands pending at end of stream", nil)
	}
	return ops, lossErr
}

// skipInlineImage advances past an inline image body. i points just after the
// BI operator. It scans forward to the ID operator, then treats the bytes that
// follow as opaque until a whitespace-delimited EI is found.
//
// Returns the offset just past EI and true; or, when the image dictionary has a
// token error or ID/EI is missing, the offset where scanning should resume (one
// byte past the error, or end of data — also after a token that runs to end of
// data, which must not be re-lexed) and false.
func skipInlineImage(data []byte, i int) (int, bool) {
	// Walk tokens (the image dictionary) until the ID operator.
	for i < len(data) {
		t, ni, err := nextToken(data, i)
		if err != nil {
			if errors.Is(err, errTruncated) {
				return len(data), false
			}
			return i + 1, false
		}
		if t.kind == tokEOF {
			return len(data), false
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
			return len(data), false
		}
		i += j
		if i+1 < len(data) && data[i+1] == 'I' {
			prevWS := i == 0 || isWhitespace(data[i-1])
			after := i + 2
			nextOK := after >= len(data) || isWhitespace(data[after]) || isDelimiter(data[after])
			if prevWS && nextOK {
				return after, true
			}
		}
		i++
	}
	return len(data), false
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

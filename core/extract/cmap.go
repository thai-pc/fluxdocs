package extract

import (
	"unicode/utf16"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// cmap is a parsed /ToUnicode CMap (ISO 32000-1 §9.10.3): it maps character
// codes, as they appear in a shown string, to Unicode text. A CMap uses the
// same token grammar as a content stream, so we parse it with parse.ScanContent
// and read the bfchar/bfrange sections from the operands of the endbfchar /
// endbfrange operators.
type cmap struct {
	codeLen int // bytes per character code (1 for simple fonts, 2 for Identity-H)
	single  map[uint32]string
	ranges  []bfRange
}

// bfRange maps a contiguous code range [lo, hi] to Unicode. Either dst (a base
// string whose final unit is incremented per code) or arr (an explicit string
// per code) is set.
type bfRange struct {
	lo, hi uint32
	dst    string
	arr    []string
}

// parseCMap parses a decoded /ToUnicode CMap stream. It is lenient: unrecognized
// constructs are ignored and a partially valid CMap still decodes what it can.
func parseCMap(data []byte) *cmap {
	c := &cmap{single: map[uint32]string{}}

	for _, op := range parse.ScanContent(data) {
		switch op.Op {
		case "endcodespacerange":
			// Pairs of <low> <high> hex strings; their byte length is the code width.
			for i := 0; i+1 < len(op.Operands); i += 2 {
				if s, ok := op.Operands[i].(parse.String); ok {
					c.noteCodeLen(len(s.Value))
				}
			}

		case "endbfchar":
			// Pairs of <src> <dst>.
			for i := 0; i+1 < len(op.Operands); i += 2 {
				src, ok1 := op.Operands[i].(parse.String)
				dst, ok2 := op.Operands[i+1].(parse.String)
				if !ok1 || !ok2 {
					continue
				}
				c.noteCodeLen(len(src.Value))
				c.single[bytesToCode(src.Value)] = utf16BEToString(dst.Value)
			}

		case "endbfrange":
			// Triples of <lo> <hi> <dst|[dst...]>.
			for i := 0; i+2 < len(op.Operands); i += 3 {
				lo, ok1 := op.Operands[i].(parse.String)
				hi, ok2 := op.Operands[i+1].(parse.String)
				if !ok1 || !ok2 {
					continue
				}
				c.noteCodeLen(len(lo.Value))
				rng := bfRange{lo: bytesToCode(lo.Value), hi: bytesToCode(hi.Value)}
				switch d := op.Operands[i+2].(type) {
				case parse.String:
					rng.dst = utf16BEToString(d.Value)
				case parse.Array:
					for _, e := range d {
						if s, ok := e.(parse.String); ok {
							rng.arr = append(rng.arr, utf16BEToString(s.Value))
						}
					}
				default:
					continue
				}
				c.ranges = append(c.ranges, rng)
			}
		}
	}

	if c.codeLen == 0 {
		c.codeLen = 1
	}
	return c
}

// noteCodeLen widens the recorded code length to the largest seen.
func (c *cmap) noteCodeLen(n int) {
	if n > c.codeLen {
		c.codeLen = n
	}
}

// lookup returns the Unicode text a code maps to, or ("", false) if unmapped.
func (c *cmap) lookup(code uint32) (string, bool) {
	if s, ok := c.single[code]; ok {
		return s, true
	}
	for _, r := range c.ranges {
		if code < r.lo || code > r.hi {
			continue
		}
		idx := code - r.lo
		if r.arr != nil {
			if int(idx) < len(r.arr) {
				return r.arr[idx], true
			}
			return "", false
		}
		// Increment the final code point of the base string (correct for the
		// common BMP case; §9.10.3).
		runes := []rune(r.dst)
		if len(runes) == 0 {
			return "", true
		}
		runes[len(runes)-1] += rune(idx)
		return string(runes), true
	}
	return "", false
}

// bytesToCode packs up to 4 big-endian bytes into a code value.
func bytesToCode(b []byte) uint32 {
	var v uint32
	for _, c := range b {
		v = v<<8 | uint32(c)
	}
	return v
}

// utf16BEToString decodes UTF-16BE bytes (the destination form in a /ToUnicode
// CMap) to a Go string. An odd trailing byte is ignored.
func utf16BEToString(b []byte) string {
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u16 = append(u16, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u16))
}

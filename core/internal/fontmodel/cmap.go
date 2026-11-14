package fontmodel

import (
	"unicode/utf16"
	"unicode/utf8"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// cmap is a parsed CMap stream: a /ToUnicode CMap (ISO 32000-1 §9.10.3), which
// maps character codes, as they appear in a shown string, to Unicode text, or
// an embedded encoding CMap of a Type 0 font (§9.7.5), of which only the
// codespace ranges, the parent CMap and the writing mode are read. A CMap uses
// the same token grammar as a content stream, so it is parsed with
// parse.ScanContent and the bfchar/bfrange/codespace sections are read from
// the operands of the endbfchar / endbfrange / endcodespacerange operators.
type cmap struct {
	codeLen int // bytes per character code as Text reads it (1 for simple fonts, 2 for Identity-H)
	single  map[uint32]string
	ranges  []bfRange

	// space holds the codespace ranges (§9.7.6.2) that are well formed:
	// low and high of equal length, 1–4 bytes. bad is set when a range could
	// not be read, so segmentation through this CMap is uncertain.
	space []spaceRange
	bad   bool
	// useCMap is the name given to usecmap (a parent CMap whose ranges also
	// apply), or "".
	useCMap parse.Name
	// wmode is the /WMode the CMap defines (1 vertical), or 0.
	wmode int
	// cut is set when some code's reading is cut by the bounds (dstCut):
	// readers that do not bound it read more text than Text, AltText and
	// ByteText return (Font.TextCut).
	cut bool
	// usesParent is set when the CMap holds a usecmap operator, whatever
	// its operand: a /ToUnicode CMap's parent maps codes lookup does not
	// see (Font.TextUnknown).
	usesParent bool
}

// bfRange maps a contiguous code range [lo, hi] to Unicode. Either dst (a base
// string whose final unit is incremented per code) or arr (an explicit string
// per code) is set.
type bfRange struct {
	lo, hi uint32
	dst    string
	arr    []string
}

// spaceRange is one codespace range: codes of n bytes whose byte i lies in
// [lo[i], hi[i]] (§9.7.6.2: ranges are compared byte by byte).
type spaceRange struct {
	n      int
	lo, hi [4]byte
}

// maxSpaceRanges bounds the codespace ranges kept from one CMap; a hostile
// CMap past it is treated as one whose segmentation is uncertain.
const maxSpaceRanges = 4096

// maxDstBytes bounds a bfchar or bfrange destination string, in bytes of
// UTF-16BE: 512 bytes, 256 code units. A destination is the text of one code
// (§9.10.3): a character, or the few of a ligature or a decomposition — the
// longest among the 19,002 destinations of the 757 /ToUnicode CMaps in the
// test corpus is 10 bytes. A longer one is cut to its first 512 bytes (a
// surrogate pair is kept whole or dropped). Unbounded, one destination of
// 1 MiB made every code of a shown string 1 MiB of text: one Tj of 1,400
// bytes was ≈ 700 MiB of text. What a code reads to is bounded again, per
// byte of the code, by codeText.
const maxDstBytes = 512

// parseCMap parses a decoded CMap stream. It is lenient: unrecognized
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
				c.addSpace(op.Operands[i], op.Operands[i+1])
			}
			if len(op.Operands)%2 != 0 {
				c.bad = true
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
				c.cut = c.cut || dstCut(dst.Value, false)
				c.single[bytesToCode(src.Value)] = dstString(dst.Value)
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
					c.cut = c.cut || dstCut(d.Value, rng.hi > rng.lo)
					rng.dst = dstString(d.Value)
				case parse.Array:
					for _, e := range d {
						if s, ok := e.(parse.String); ok {
							c.cut = c.cut || dstCut(s.Value, false)
							rng.arr = append(rng.arr, dstString(s.Value))
						}
					}
				default:
					continue
				}
				c.ranges = append(c.ranges, rng)
			}

		case "usecmap":
			// /Parent usecmap (§9.7.5.3): the parent's mappings and ranges apply too.
			c.usesParent = true
			if n := len(op.Operands); n > 0 {
				if name, ok := op.Operands[n-1].(parse.Name); ok {
					c.useCMap = name
				} else {
					c.bad = true
				}
			}

		case "def":
			// /WMode 1 def
			if n := len(op.Operands); n >= 2 {
				if key, ok := op.Operands[n-2].(parse.Name); ok && key == "WMode" {
					if v, ok := op.Operands[n-1].(parse.Integer); ok && v == 1 {
						c.wmode = 1
					}
				}
			}
		}
	}

	if c.codeLen == 0 {
		c.codeLen = 1
	}
	return c
}

// addSpace records one codespace range, or marks the CMap bad if it is not a
// pair of equal-length 1–4 byte strings.
func (c *cmap) addSpace(loObj, hiObj parse.Object) {
	lo, ok1 := loObj.(parse.String)
	hi, ok2 := hiObj.(parse.String)
	n := len(lo.Value)
	if !ok1 || !ok2 || n < 1 || n > 4 || len(hi.Value) != n || len(c.space) >= maxSpaceRanges {
		c.bad = true
		return
	}
	r := spaceRange{n: n}
	copy(r.lo[:], lo.Value)
	copy(r.hi[:], hi.Value)
	c.space = append(c.space, r)
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

// lookupText is lookup for a reading other than Text's: a code mapped to no
// text (an empty destination) counts as unmapped. Ghostscript reads such a
// code as one /ToUnicode does not map — through its glyph name, or the
// character collection of a Type 0 font — where Text, which extraction keeps,
// reads nothing.
func (c *cmap) lookupText(code uint32) (string, bool) {
	s, ok := c.lookup(code)
	return s, ok && s != ""
}

// bytesToCode packs up to 4 big-endian bytes into a code value.
func bytesToCode(b []byte) uint32 {
	var v uint32
	for _, c := range b {
		v = v<<8 | uint32(c)
	}
	return v
}

// dstString decodes a bfchar or bfrange destination (utf16BEToString), cut to
// its first maxDstBytes bytes: a high surrogate the cut separates from its low
// one is dropped with it.
func dstString(b []byte) string {
	if len(b) > maxDstBytes {
		b = b[:maxDstBytes]
		if u := uint16(b[len(b)-2])<<8 | uint16(b[len(b)-1]); utf16.IsSurrogate(rune(u)) && u < 0xDC00 {
			b = b[:len(b)-2]
		}
	}
	return utf16BEToString(b)
}

// dstCut reports whether a code mapped to destination b reads to text the
// bounds cut: b is longer than maxDstBytes, or its text is longer than
// maxTextPerCodeByte bytes, the bound for a one-byte code (ByteText reads
// every code of a simple font as one byte, whatever the CMap's code length).
// incremented is set for the base string of a bfrange of more than one code,
// whose final character lookup increments: it is counted as 4 bytes of UTF-8,
// the most an incremented character (or the U+FFFD past U+10FFFF) takes.
func dstCut(b []byte, incremented bool) bool {
	if len(b) > maxDstBytes {
		return true
	}
	s := utf16BEToString(b)
	n := len(s)
	if incremented && n > 0 {
		_, size := utf8.DecodeLastRuneInString(s)
		n += 4 - size
	}
	return n > maxTextPerCodeByte
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

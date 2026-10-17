package parse

import (
	"errors"
	"fmt"
)

// filter_ascii.go holds the stream filters besides Flate that carry content
// and text (ISO 32000-1 §7.4.2–7.4.5): ASCIIHexDecode, ASCII85Decode,
// LZWDecode and RunLengthDecode. Each decodes within the caller's allowance
// (decodeLimit) and maxDecoded, checking as it goes, so a hostile stream
// (RunLength expands 128×, LZW and ASCII85 "z" 4× or more) stops at the limit
// instead of allocating past it.

// errFilterData reports data a filter cannot decode.
var errFilterData = errors.New("invalid filter data")

// limitedOut is a filter's output buffer bounded by lim and maxDecoded.
type limitedOut struct {
	buf []byte
	lim *decodeLimit
}

// room reports an error once n more bytes would exceed the caller's
// allowance or maxDecoded.
func (o *limitedOut) room(n int) error {
	if int64(len(o.buf)+n) > o.lim.left {
		return fmt.Errorf("%w: more than %d bytes", ErrDecodeLimit, o.lim.left)
	}
	if len(o.buf)+n > maxDecoded {
		return fmt.Errorf("%w: decoded stream exceeds %d bytes (suspected bomb)", errLex, maxDecoded)
	}
	return nil
}

// done charges the output to lim and returns it.
func (o *limitedOut) done() []byte {
	o.lim.produced += int64(len(o.buf))
	o.lim.left -= int64(len(o.buf))
	return o.buf
}

// fail charges what was produced before an error.
func (o *limitedOut) fail(err error) ([]byte, error) {
	o.done()
	return nil, err
}

// asciiHexDecode decodes ASCIIHexDecode data (§7.4.2): pairs of hex digits,
// white-space ignored, up to the EOD marker '>' (or the end of the data); a
// final odd digit is followed by an implied 0. Any other byte is an error.
func asciiHexDecode(data []byte, lim *decodeLimit) ([]byte, error) {
	o := &limitedOut{lim: lim}
	var hi byte
	half := false
	for _, c := range data {
		if c == '>' {
			break
		}
		if isWhitespace(c) {
			continue
		}
		if !isHex(c) {
			return o.fail(fmt.Errorf("%w: ASCIIHexDecode byte 0x%02x", errFilterData, c))
		}
		v := hexVal(c)
		if !half {
			hi, half = v, true
			continue
		}
		if err := o.room(1); err != nil {
			return o.fail(err)
		}
		o.buf = append(o.buf, hi<<4|v)
		half = false
	}
	if half {
		if err := o.room(1); err != nil {
			return o.fail(err)
		}
		o.buf = append(o.buf, hi<<4)
	}
	return o.done(), nil
}

// ascii85Decode decodes ASCII85Decode data (§7.4.3): groups of five
// characters '!'…'u' to four bytes, 'z' for four zero bytes, white-space
// ignored, up to the EOD marker "~>" (or the end of the data). A final
// partial group of n characters gives n−1 bytes; a group of one character,
// a 'z' inside a group, a value past 2^32 or any other byte is an error.
func ascii85Decode(data []byte, lim *decodeLimit) ([]byte, error) {
	o := &limitedOut{lim: lim}
	var group [5]byte
	n := 0
	flush := func(count int) error {
		var v uint64
		for i := 0; i < 5; i++ {
			c := byte('u')
			if i < count {
				c = group[i]
			}
			v = v*85 + uint64(c-'!')
		}
		if v > 0xFFFFFFFF {
			return fmt.Errorf("%w: ASCII85Decode group out of range", errFilterData)
		}
		out := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		if err := o.room(count - 1); err != nil {
			return err
		}
		o.buf = append(o.buf, out[:count-1]...)
		return nil
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case isWhitespace(c):
			continue
		case c == '~':
			// EOD: "~>" (a '~' alone ends the data too).
			i = len(data)
			continue
		case c == 'z' && n == 0:
			if err := o.room(4); err != nil {
				return o.fail(err)
			}
			o.buf = append(o.buf, 0, 0, 0, 0)
		case c >= '!' && c <= 'u':
			group[n] = c
			n++
			if n == 5 {
				if err := flush(5); err != nil {
					return o.fail(err)
				}
				n = 0
			}
		default:
			return o.fail(fmt.Errorf("%w: ASCII85Decode byte 0x%02x", errFilterData, c))
		}
	}
	switch {
	case n == 1:
		return o.fail(fmt.Errorf("%w: ASCII85Decode final group of one character", errFilterData))
	case n > 1:
		if err := flush(n); err != nil {
			return o.fail(err)
		}
	}
	return o.done(), nil
}

// lzwDecode decodes LZWDecode data (§7.4.4): variable-width codes of 9 to 12
// bits, most significant bit first; 256 clears the table, 257 ends the data.
// With earlyChange (the default, /EarlyChange 1) the code width grows one
// code early. A code past the table, or a first code that is not a literal,
// is an error; data that ends without 257 ends the output there.
func lzwDecode(data []byte, earlyChange bool, lim *decodeLimit) ([]byte, error) {
	o := &limitedOut{lim: lim}
	const clear, eod = 256, 257
	// The table holds entries as (prefix code, last byte, length), so an
	// entry costs a few bytes whatever its length.
	type entry struct {
		prefix int
		last   byte
		length int
	}
	table := make([]entry, 258, 4096)
	for i := 0; i < 256; i++ {
		table[i] = entry{prefix: -1, last: byte(i), length: 1}
	}
	reset := func() { table = table[:258] }
	width := 9
	var bitBuf uint32
	bits := 0
	pos := 0
	prev := -1
	scratch := make([]byte, 0, 64)
	// expand appends the bytes of code to the output.
	expand := func(code int) error {
		e := table[code]
		if err := o.room(e.length); err != nil {
			return err
		}
		scratch = scratch[:0]
		for c := code; c >= 0; c = table[c].prefix {
			scratch = append(scratch, table[c].last)
		}
		for i := len(scratch) - 1; i >= 0; i-- {
			o.buf = append(o.buf, scratch[i])
		}
		return nil
	}
	first := func(code int) byte {
		for table[code].prefix >= 0 {
			code = table[code].prefix
		}
		return table[code].last
	}
	for {
		for bits < width && pos < len(data) {
			bitBuf = bitBuf<<8 | uint32(data[pos])
			pos++
			bits += 8
		}
		if bits < width {
			break // no EOD: the output ends here
		}
		code := int(bitBuf>>(bits-width)) & (1<<width - 1)
		bits -= width
		switch {
		case code == clear:
			reset()
			width = 9
			prev = -1
			continue
		case code == eod:
			return o.done(), nil
		}
		switch {
		case code < len(table):
			if err := expand(code); err != nil {
				return o.fail(err)
			}
			if prev >= 0 && len(table) < 4096 {
				table = append(table, entry{prefix: prev, last: first(code), length: table[prev].length + 1})
			}
		case code == len(table) && prev >= 0:
			// The KwKwK case: the code being defined.
			if len(table) < 4096 {
				table = append(table, entry{prefix: prev, last: first(prev), length: table[prev].length + 1})
			}
			if err := expand(code); err != nil {
				return o.fail(err)
			}
		default:
			return o.fail(fmt.Errorf("%w: LZWDecode code %d past the table", errFilterData, code))
		}
		prev = code
		early := 0
		if earlyChange {
			early = 1
		}
		switch {
		case len(table)+early >= 4096:
			width = 12
		case len(table)+early >= 2048:
			width = 12
		case len(table)+early >= 1024:
			width = 11
		case len(table)+early >= 512:
			width = 10
		}
	}
	return o.done(), nil
}

// runLengthDecode decodes RunLengthDecode data (§7.4.5): a length byte n
// followed by n+1 literal bytes (n < 128), or by one byte repeated 257−n
// times (n > 128); 128 ends the data. Data that ends inside a run is an
// error.
func runLengthDecode(data []byte, lim *decodeLimit) ([]byte, error) {
	o := &limitedOut{lim: lim}
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return o.done(), nil
		case n < 128:
			if i+n+1 > len(data) {
				return o.fail(fmt.Errorf("%w: RunLengthDecode literal run past the data", errFilterData))
			}
			if err := o.room(n + 1); err != nil {
				return o.fail(err)
			}
			o.buf = append(o.buf, data[i:i+n+1]...)
			i += n + 1
		default:
			if i >= len(data) {
				return o.fail(fmt.Errorf("%w: RunLengthDecode repeat run past the data", errFilterData))
			}
			count := 257 - n
			if err := o.room(count); err != nil {
				return o.fail(err)
			}
			for k := 0; k < count; k++ {
				o.buf = append(o.buf, data[i])
			}
			i++
		}
	}
	return o.done(), nil
}

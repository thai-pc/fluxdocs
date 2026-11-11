package psfont

import (
	"encoding/binary"
	"math"
	"sort"
	"strconv"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// CFF DICT operators used here (TN #5176 Tables 9, 10 and 23). Escaped
// operators (12 x) are numbered 32 + x, as for charstrings.
const (
	dictCharset        = 15
	dictEncoding       = 16
	dictCharStrings    = 17
	dictPrivate        = 18
	dictSubrs          = 19
	dictDefaultWidthX  = 20
	dictNominalWidthX  = 21
	dictCharstringType = 32 + 6
	dictFontMatrix     = 32 + 7
	dictROS            = 32 + 30
	dictFDArray        = 32 + 36
	dictFDSelect       = 32 + 37
)

// Estimated bytes per entry of the lookup tables ParseCFF builds, charged
// through Options.Charge before they are built: a map entry with its share
// of buckets, and a string header.
const (
	cffMapEntryCost = 48
	cffNameCost     = 16
)

// cffData is the CFF half of a Font. Charstrings and subroutines stay in the
// program's bytes and are read through their INDEX on use, so parsing does not
// copy them.
type cffData struct {
	charstrings cffIndex
	gsubrs      cffIndex
	fds         []cffFD // one for a non-CID-keyed font
	fdSelect    fdSelect
	cidKeyed    bool
	byCID       map[uint16]GID // CID-keyed fonts: the charset inverted, first GID wins
}

// cffFD is what a glyph's Font DICT (the Top DICT for a non-CID-keyed font)
// and its Private DICT give the charstring interpreter.
type cffFD struct {
	subrs         cffIndex
	defaultWidthX float64
	nominalWidthX float64
	matrix        [6]float64
}

// cffIndex is a validated INDEX (TN #5176 §5): count objects, offSize-byte
// offsets starting at offStart, 1-based relative to the byte before the
// object data. A zero value is an empty INDEX.
type cffIndex struct {
	data     []byte
	count    int
	offSize  int
	offStart int
	dataBase int // the byte before the object data
}

// readIndex reads and validates the INDEX at pos (TN #5176 §5, design §5.1):
// offSize 1–4, count+1 offsets that start at 1, never decrease, and end
// inside data, all computed in int64. Returns the INDEX and the offset just
// past it; on failure, errMalformed and pos.
func readIndex(data []byte, pos int) (cffIndex, int, error) {
	if pos < 0 || pos > len(data)-2 {
		return cffIndex{}, pos, errMalformed
	}
	count := int(binary.BigEndian.Uint16(data[pos:]))
	if count == 0 {
		return cffIndex{}, pos + 2, nil
	}
	if pos+2 >= len(data) {
		return cffIndex{}, pos, errMalformed
	}
	offSize := int(data[pos+2])
	if offSize < 1 || offSize > 4 {
		return cffIndex{}, pos, errMalformed
	}
	offStart := int64(pos) + 3
	offEnd := offStart + int64(count+1)*int64(offSize)
	if offEnd > int64(len(data)) {
		return cffIndex{}, pos, errMalformed
	}
	idx := cffIndex{data: data, count: count, offSize: offSize, offStart: int(offStart), dataBase: int(offEnd) - 1}
	room := int64(len(data)) - offEnd // object bytes available
	prev := uint32(1)
	for i := 0; i <= count; i++ {
		o := idx.offset(i)
		if (i == 0 && o != 1) || o < prev || int64(o)-1 > room {
			return cffIndex{}, pos, errMalformed
		}
		prev = o
	}
	return idx, idx.dataBase + int(prev), nil
}

// offset reads offset i (0 ≤ i ≤ count) of a validated INDEX.
func (x cffIndex) offset(i int) uint32 {
	p := x.offStart + i*x.offSize
	var v uint32
	for _, b := range x.data[p : p+x.offSize] {
		v = v<<8 | uint32(b)
	}
	return v
}

// get returns object i, a subslice of the program; false when i is out of
// range.
func (x cffIndex) get(i int) ([]byte, bool) {
	if i < 0 || i >= x.count {
		return nil, false
	}
	start, end := x.dataBase+int(x.offset(i)), x.dataBase+int(x.offset(i+1))
	return x.data[start:end:end], true
}

// cffDict is a parsed DICT: the operands of each operator the parser uses
// (dictKept), the first occurrence only. Other operators and repeats are
// skipped, so a DICT allocates at most len(dictKept) operand lists whatever
// its size or content.
type cffDict map[int][]float64

// dictKept reports the DICT operators ParseCFF reads.
func dictKept(op int) bool {
	switch op {
	case dictCharset, dictEncoding, dictCharStrings, dictPrivate, dictSubrs, dictDefaultWidthX,
		dictNominalWidthX, dictCharstringType, dictFontMatrix, dictROS, dictFDArray, dictFDSelect:
		return true
	}
	return false
}

// parseDict parses DICT data (TN #5176 §4): operands (integers, 28 and 29
// integers, 30 reals) then an operator. Returns errMalformed for a reserved
// byte, a truncated number or operator, more than maxDictOperands operands
// or a real past maxRealNibbles.
func parseDict(b []byte) (cffDict, error) {
	d := cffDict{}
	var ops [maxDictOperands]float64
	n := 0
	for i := 0; i < len(b); {
		v := b[i]
		var num float64
		switch {
		case v <= 21:
			op := int(v)
			i++
			if v == 12 {
				if i >= len(b) {
					return nil, errMalformed
				}
				op = 32 + int(b[i])
				i++
			}
			if _, seen := d[op]; !seen && dictKept(op) {
				d[op] = append([]float64(nil), ops[:n]...)
			}
			n = 0
			continue
		case v == 28:
			if i+3 > len(b) {
				return nil, errMalformed
			}
			num = float64(int16(binary.BigEndian.Uint16(b[i+1:])))
			i += 3
		case v == 29:
			if i+5 > len(b) {
				return nil, errMalformed
			}
			num = float64(int32(binary.BigEndian.Uint32(b[i+1:])))
			i += 5
		case v == 30:
			var err error
			if num, i, err = parseReal(b, i+1); err != nil {
				return nil, err
			}
		case v >= 32 && v <= 246:
			num = float64(int(v) - 139)
			i++
		case v >= 247 && v <= 250:
			if i+2 > len(b) {
				return nil, errMalformed
			}
			num = float64((int(v)-247)*256 + int(b[i+1]) + 108)
			i += 2
		case v >= 251 && v <= 254:
			if i+2 > len(b) {
				return nil, errMalformed
			}
			num = float64(-(int(v)-251)*256 - int(b[i+1]) - 108)
			i += 2
		default:
			return nil, errMalformed // 22–27, 31, 255: reserved
		}
		if n >= maxDictOperands {
			return nil, errMalformed
		}
		ops[n] = num
		n++
	}
	return d, nil
}

// parseReal reads the nibbles of a DICT real number starting at b[i] (TN
// #5176 Table 5): 0–9 digits, a '.', b 'E', c 'E-', e '-', f end; d is
// reserved. Returns the value and the offset after its last byte.
func parseReal(b []byte, i int) (float64, int, error) {
	var s [2 * maxRealNibbles]byte
	n, nibbles := 0, 0
	for {
		if i >= len(b) {
			return 0, i, errMalformed
		}
		c := b[i]
		i++
		for _, nib := range [2]byte{c >> 4, c & 15} {
			if nib == 15 {
				v, err := strconv.ParseFloat(string(s[:n]), 64)
				if err != nil && !isRangeErr(err) {
					return 0, i, errMalformed
				}
				return v, i, nil
			}
			if nibbles++; nibbles > maxRealNibbles {
				return 0, i, errMalformed
			}
			switch {
			case nib <= 9:
				s[n] = '0' + nib
				n++
			case nib == 0xa:
				s[n] = '.'
				n++
			case nib == 0xb:
				s[n] = 'E'
				n++
			case nib == 0xc:
				s[n], s[n+1] = 'E', '-'
				n += 2
			case nib == 0xe:
				s[n] = '-'
				n++
			default:
				return 0, i, errMalformed
			}
		}
	}
}

// isRangeErr reports strconv's out-of-range error: the value (±Inf or 0)
// is kept, and its use is validated where it matters.
func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// int returns operand k of op as an integer in [lo, hi]; false when op is
// absent, has fewer operands, or the value is not such an integer.
func (d cffDict) int(op, k int, lo, hi int64) (int, bool) {
	a := d[op]
	if k >= len(a) {
		return 0, false
	}
	v := a[k]
	if v != math.Trunc(v) || v < float64(lo) || v > float64(hi) {
		return 0, false
	}
	return int(v), true
}

// num returns the last operand of op, or def when op is absent or has none
// or it is not finite.
func (d cffDict) num(op int, def float64) float64 {
	a := d[op]
	if len(a) == 0 || math.IsNaN(a[len(a)-1]) || math.IsInf(a[len(a)-1], 0) {
		return def
	}
	return a[len(a)-1]
}

// matrix returns op's six operands as a FontMatrix; false when it is absent
// or not six operands.
func (d cffDict) matrix(op int) ([6]float64, bool) {
	a := d[op]
	var m [6]float64
	if len(a) != 6 {
		return m, false
	}
	copy(m[:], a)
	return m, true
}

// cffParser holds the state of one ParseCFF call.
type cffParser struct {
	data    []byte
	opts    Options
	strings cffIndex
	// privates caches Private DICTs by (offset, size) and subrs local Subrs
	// INDEXes by offset, so Font DICTs sharing one are read once;
	// privateBytes counts the distinct Private DICT bytes read, bounded by
	// the program size.
	privates     map[[2]int]cffFD
	subrs        map[int]cffIndex
	privateBytes int64
}

// ParseCFF parses a bare CFF program (an embedded /FontFile3 of subtype
// /Type1C or /CIDFontType0C, ISO 32000-1 §9.9 Table 127; TN #5176): the
// first font of its FontSet, CID-keyed or not, with Type 2 charstrings
// (design §2.3). The Font keeps references into data, which the caller must
// not modify afterwards.
//
// Returns errLimit for a program past maxProgramBytes or a refused charge,
// errMalformed for a structure that does not follow TN #5176: a header that
// is not major version 1, an INDEX or DICT that fails validation, a
// CharstringType other than 2, no CharStrings, a charset, Encoding or
// FDSelect format that is unknown or does not fit, more than maxFDs Font
// DICTs, or an FD index past the FDArray.
func ParseCFF(data []byte, opts Options) (*Font, error) {
	if len(data) > maxProgramBytes {
		return nil, errLimit
	}
	if len(data) < 4 || data[0] != 1 || data[2] < 4 {
		return nil, errMalformed
	}
	p := &cffParser{data: data, opts: opts, privates: map[[2]int]cffFD{}, subrs: map[int]cffIndex{}}
	_, pos, err := readIndex(data, int(data[2])) // Name INDEX
	if err != nil {
		return nil, err
	}
	top, pos, err := readIndex(data, pos)
	if err != nil {
		return nil, err
	}
	if p.strings, pos, err = readIndex(data, pos); err != nil {
		return nil, err
	}
	gsubrs, _, err := readIndex(data, pos)
	if err != nil {
		return nil, err
	}
	topData, ok := top.get(0)
	if !ok {
		return nil, errMalformed
	}
	td, err := parseDict(topData)
	if err != nil {
		return nil, err
	}
	if t := td.num(dictCharstringType, 2); t != 2 {
		return nil, errMalformed
	}
	csOff, ok := p.offset(td, dictCharStrings)
	if !ok {
		return nil, errMalformed
	}
	charstrings, _, err := readIndex(data, csOff)
	if err != nil || charstrings.count == 0 {
		return nil, errMalformed
	}
	nGlyphs := charstrings.count

	f := &Font{matrix: defaultMatrix}
	topMatrix, hasTopMatrix := td.matrix(dictFontMatrix)
	if hasTopMatrix && validMatrix(topMatrix) {
		f.matrix = topMatrix
	}
	c := &cffData{charstrings: charstrings, gsubrs: gsubrs}
	f.cff = c
	if _, c.cidKeyed = td[dictROS]; c.cidKeyed {
		if err := p.readCIDFonts(td, c, nGlyphs, topMatrix, hasTopMatrix); err != nil {
			return nil, err
		}
		if len(c.fds) > 0 {
			f.matrix = c.fds[0].matrix
		}
	} else {
		fd, err := p.readPrivate(td)
		if err != nil {
			return nil, err
		}
		fd.matrix = f.matrix
		c.fds = []cffFD{fd}
	}

	sids, err := p.readCharset(td, nGlyphs, c.cidKeyed)
	if err != nil {
		return nil, err
	}
	if c.cidKeyed {
		if !opts.charge(nGlyphs * cffMapEntryCost) {
			return nil, errLimit
		}
		c.byCID = make(map[uint16]GID, nGlyphs)
		for g, cid := range sids {
			if _, dup := c.byCID[cid]; !dup {
				c.byCID[cid] = GID(g)
			}
		}
		return f, nil
	}
	if err := p.nameGlyphs(f, sids); err != nil {
		return nil, err
	}
	if err := p.readEncoding(f, td, sids); err != nil {
		return nil, err
	}
	return f, nil
}

// offset returns the Top DICT offset operand of op (the first operand),
// checked to lie inside the program; false when absent or out of range.
func (p *cffParser) offset(d cffDict, op int) (int, bool) {
	return d.int(op, 0, 0, int64(len(p.data)))
}

// readPrivate reads the Private DICT a Top or Font DICT points at (size,
// offset), its widths and its local Subrs INDEX (offset relative to the
// Private DICT). A DICT with no Private has no local subroutines and zero
// widths. Private DICTs are cached by (offset, size) and Subrs INDEXes by
// offset; together, the distinct Private DICTs read may not exceed the
// program's size (errLimit), so Font DICTs that overlap theirs cannot
// multiply the parse work.
func (p *cffParser) readPrivate(d cffDict) (cffFD, error) {
	if _, ok := d[dictPrivate]; !ok {
		return cffFD{}, nil
	}
	size, ok1 := d.int(dictPrivate, 0, 0, int64(len(p.data)))
	off, ok2 := d.int(dictPrivate, 1, 0, int64(len(p.data)))
	if !ok1 || !ok2 || size > len(p.data)-off {
		return cffFD{}, errMalformed
	}
	key := [2]int{off, size}
	if fd, ok := p.privates[key]; ok {
		return fd, nil
	}
	if p.privateBytes += int64(size); p.privateBytes > int64(len(p.data)) {
		return cffFD{}, errLimit
	}
	pd, err := parseDict(p.data[off : off+size])
	if err != nil {
		return cffFD{}, err
	}
	fd := cffFD{
		defaultWidthX: pd.num(dictDefaultWidthX, 0),
		nominalWidthX: pd.num(dictNominalWidthX, 0),
	}
	// Widths are coordinates: within ±maxCoord, as an explicit width is
	// (takeWidth).
	if !finiteCoord(fd.defaultWidthX) || !finiteCoord(fd.nominalWidthX) {
		return cffFD{}, errLimit
	}
	if _, ok := pd[dictSubrs]; ok {
		rel, ok := pd.int(dictSubrs, 0, 0, int64(len(p.data)))
		if !ok || rel > len(p.data)-off {
			return cffFD{}, errMalformed
		}
		at := off + rel
		subrs, cached := p.subrs[at]
		if !cached {
			if subrs, _, err = readIndex(p.data, at); err != nil {
				return cffFD{}, err
			}
			p.subrs[at] = subrs
		}
		fd.subrs = subrs
	}
	p.privates[key] = fd
	return fd, nil
}

// readCIDFonts reads a CID-keyed font's FDArray (at most maxFDs Font DICTs,
// each with its Private DICT) and FDSelect. Each Font DICT's effective
// FontMatrix is its FontMatrix concatenated with the Top DICT's (TN #5176:
// a Font DICT's FontMatrix defaults to [0.001 0 0 0.001 0 0], as any
// FontMatrix does). Producers disagree when only one is written, so
// (design §2.3, §5.1):
//   - both present: own × top;
//   - Font DICT only: own;
//   - Top DICT only, already a glyph-space matrix (glyphScaled, the usual
//     [0.001 0 0 0.001 0 0] from a producer that writes the matrix once):
//     top alone, not composed with the default again;
//   - Top DICT only, otherwise (an explicit identity, say, meant to scale
//     the Font DICTs' matrices): the default × top;
//   - neither: the default.
//
// A result that fails validMatrix falls back to the default.
func (p *cffParser) readCIDFonts(td cffDict, c *cffData, nGlyphs int, top [6]float64, hasTop bool) error {
	arrOff, ok := p.offset(td, dictFDArray)
	if !ok {
		return errMalformed
	}
	arr, _, err := readIndex(p.data, arrOff)
	if err != nil || arr.count == 0 {
		return errMalformed
	}
	if arr.count > maxFDs {
		return errMalformed
	}
	c.fds = make([]cffFD, arr.count)
	for i := range c.fds {
		b, _ := arr.get(i)
		fdict, err := parseDict(b)
		if err != nil {
			return err
		}
		fd, err := p.readPrivate(fdict)
		if err != nil {
			return err
		}
		m := defaultMatrix
		own, hasOwn := fdict.matrix(dictFontMatrix)
		switch {
		case hasOwn && hasTop:
			m = mulMatrix(own, top)
		case hasOwn:
			m = own
		case hasTop && glyphScaled(top):
			m = top
		case hasTop:
			m = mulMatrix(defaultMatrix, top)
		}
		if !validMatrix(m) {
			m = defaultMatrix
		}
		fd.matrix = m
		c.fds[i] = fd
	}
	selOff, ok := p.offset(td, dictFDSelect)
	if !ok {
		return errMalformed
	}
	c.fdSelect, err = readFDSelect(p.data, selOff, nGlyphs, len(c.fds))
	return err
}

// glyphScaled reports whether m's linear part already maps glyph space to
// text space at an em of more than 10 units (every entry below 0.1 in
// magnitude), as a full FontMatrix does, rather than scaling another matrix
// (an identity Top DICT matrix over Font DICT matrices).
func glyphScaled(m [6]float64) bool {
	return math.Abs(m[0]) < 0.1 && math.Abs(m[1]) < 0.1 && math.Abs(m[2]) < 0.1 && math.Abs(m[3]) < 0.1
}

// mulMatrix returns m × n (m applied first), each product rounded to float64
// before the sum (see validMatrix: no fused multiply-add).
func mulMatrix(m, n [6]float64) [6]float64 {
	return [6]float64{
		float64(m[0]*n[0]) + float64(m[1]*n[2]),
		float64(m[0]*n[1]) + float64(m[1]*n[3]),
		float64(m[2]*n[0]) + float64(m[3]*n[2]),
		float64(m[2]*n[1]) + float64(m[3]*n[3]),
		float64(m[4]*n[0]) + float64(m[5]*n[2]) + n[4],
		float64(m[4]*n[1]) + float64(m[5]*n[3]) + n[5],
	}
}

// fdSelect maps a GID to its Font DICT (TN #5176 §19). The zero value
// selects FD 0 for every glyph (a non-CID-keyed font).
type fdSelect struct {
	data   []byte // nil: every glyph uses FD 0
	format int    // 0 or 3
	pos    int    // the format byte
	ranges int    // format 3
}

// readFDSelect reads and validates the FDSelect at pos: format 0 (one FD
// index per glyph) or 3 (ranges whose first GIDs start at 0 and increase).
// Every FD index must be below nFDs.
func readFDSelect(data []byte, pos, nGlyphs, nFDs int) (fdSelect, error) {
	if pos >= len(data) {
		return fdSelect{}, errMalformed
	}
	s := fdSelect{data: data, format: int(data[pos]), pos: pos}
	switch s.format {
	case 0:
		if int64(nGlyphs) > int64(len(data)-pos-1) {
			return fdSelect{}, errMalformed
		}
		for _, fd := range data[pos+1 : pos+1+nGlyphs] {
			if int(fd) >= nFDs {
				return fdSelect{}, errMalformed
			}
		}
		return s, nil
	case 3:
		if pos+3 > len(data) {
			return fdSelect{}, errMalformed
		}
		s.ranges = int(binary.BigEndian.Uint16(data[pos+1:]))
		// The ranges (3 bytes each) and the sentinel (2) must fit.
		if s.ranges == 0 || int64(s.ranges)*3+2 > int64(len(data)-pos-3) {
			return fdSelect{}, errMalformed
		}
		prev := -1
		for r := 0; r < s.ranges; r++ {
			first, fd := s.rangeAt(r)
			if (r == 0 && first != 0) || first <= prev || fd >= nFDs {
				return fdSelect{}, errMalformed
			}
			prev = first
		}
		return s, nil
	}
	return fdSelect{}, errMalformed
}

// rangeAt returns the first GID and FD index of format 3 range r.
func (s fdSelect) rangeAt(r int) (first, fd int) {
	p := s.pos + 3 + 3*r
	return int(binary.BigEndian.Uint16(s.data[p:])), int(s.data[p+2])
}

// fd returns the FD index of gid (already below the glyph count). GIDs past
// a format 3 sentinel take the last range's FD.
func (s fdSelect) fd(gid int) int {
	switch {
	case s.data == nil:
		return 0
	case s.format == 0:
		return int(s.data[s.pos+1+gid])
	}
	r := sort.Search(s.ranges, func(r int) bool {
		first, _ := s.rangeAt(r)
		return first > gid
	}) - 1
	_, fd := s.rangeAt(max(r, 0))
	return fd
}

// readCharset returns the SID (CID for a CID-keyed font) of every glyph
// (TN #5176 §13): predefined charsets 0 (ISOAdobe: SID = GID up to 228), 1
// (Expert) and 2 (ExpertSubset), GIDs past the charset unnamed (SID 0), or
// custom formats 0, 1 and 2. A CID-keyed font with a predefined charset uses
// CID = GID. GID 0 is always .notdef (SID or CID 0);
// ranges past the glyph count are clamped.
func (p *cffParser) readCharset(td cffDict, nGlyphs int, cidKeyed bool) ([]uint16, error) {
	if !p.opts.charge(2 * nGlyphs) {
		return nil, errLimit
	}
	sids := make([]uint16, nGlyphs)
	off := 0
	if _, ok := td[dictCharset]; ok {
		var ok bool
		if off, ok = p.offset(td, dictCharset); !ok {
			return nil, errMalformed
		}
	}
	if off <= 2 {
		for g := range sids {
			switch {
			case cidKeyed:
				sids[g] = uint16(g)
			case off == 0 && g <= isoAdobeLast:
				sids[g] = uint16(g)
			case off == 1 && g >= 1 && g <= len(expertCharset):
				sids[g] = expertCharset[g-1]
			case off == 2 && g >= 1 && g <= len(expertSubsetCharset):
				sids[g] = expertSubsetCharset[g-1]
			}
		}
		return sids, nil
	}
	b := p.data[off:]
	if len(b) == 0 {
		return nil, errMalformed
	}
	format := b[0]
	i := 1
	switch format {
	case 0:
		if int64(nGlyphs-1)*2 > int64(len(b)-1) {
			return nil, errMalformed
		}
		for g := 1; g < nGlyphs; g++ {
			sids[g] = binary.BigEndian.Uint16(b[i:])
			i += 2
		}
	case 1, 2:
		for g := 1; g < nGlyphs; {
			rangeLen := 3
			if format == 2 {
				rangeLen = 4
			}
			if i+rangeLen > len(b) {
				return nil, errMalformed
			}
			first := int(binary.BigEndian.Uint16(b[i:]))
			left := int(b[i+2])
			if format == 2 {
				left = int(binary.BigEndian.Uint16(b[i+2:]))
			}
			i += rangeLen
			// Clamped to the glyph count first: only SIDs the glyphs take
			// must fit in 16 bits.
			left = min(left, nGlyphs-1-g)
			if first+left > 0xFFFF {
				return nil, errMalformed
			}
			for k := 0; k <= left && g < nGlyphs; k++ {
				sids[g] = uint16(first + k)
				g++
			}
		}
	default:
		return nil, errMalformed
	}
	return sids, nil
}

// sidName returns the string of sid: a standard string or a String INDEX
// entry; "" when sid is past both.
func (p *cffParser) sidName(sid uint16) string {
	if sid < nStdStrings {
		return cffStdStrings[sid]
	}
	b, ok := p.strings.get(int(sid) - nStdStrings)
	if !ok {
		return ""
	}
	return string(b)
}

// nameGlyphs builds a non-CID-keyed font's glyph names and name → GID map
// from its charset (the first GID with a name wins). A glyph with no name
// (an unnamed SID, a SID past the String INDEX) cannot be selected by name.
func (p *cffParser) nameGlyphs(f *Font, sids []uint16) error {
	total := len(sids) * (cffNameCost + cffMapEntryCost)
	for _, sid := range sids {
		if sid >= nStdStrings {
			if b, ok := p.strings.get(int(sid) - nStdStrings); ok {
				total += len(b)
			}
		}
	}
	if !p.opts.charge(total) {
		return errLimit
	}
	f.names = make([]string, len(sids))
	f.byName = make(map[string]GID, len(sids))
	for g, sid := range sids {
		if g > 0 && sid == 0 {
			continue // unnamed (an untranscribed predefined charset)
		}
		name := p.sidName(sid)
		f.names[g] = name
		if _, dup := f.byName[name]; name != "" && !dup {
			f.byName[name] = GID(g)
		}
	}
	return nil
}

// readEncoding reads a non-CID-keyed font's built-in encoding (TN #5176
// §12): predefined 0 (Standard) or 1 (Expert), or a custom format 0 (a code
// per glyph from GID 1) or 1 (code ranges assigned to GIDs from 1), each
// optionally with supplements (code → SID; high bit of the format byte). The
// first glyph a code is assigned to wins; codes past 255 and GIDs past the
// glyph count are ignored.
func (p *cffParser) readEncoding(f *Font, td cffDict, sids []uint16) error {
	off := 0
	if _, ok := td[dictEncoding]; ok {
		var ok bool
		if off, ok = p.offset(td, dictEncoding); !ok {
			return errMalformed
		}
	}
	switch off {
	case 0:
		f.encoding, f.hasEncoding = std14.StandardEncoding(), true
		return nil
	case 1:
		for code, sid := range expertEncoding {
			if sid != 0 {
				f.encoding[code] = cffStdStrings[sid]
			}
		}
		f.hasEncoding = true
		return nil
	}
	b := p.data[off:]
	if len(b) < 2 {
		return errMalformed
	}
	format := b[0]
	var assigned [256]bool
	set := func(code int, name string) {
		if code >= 0 && code <= 255 && !assigned[code] {
			assigned[code] = true
			f.encoding[code] = name
		}
	}
	i := 2
	switch format & 0x7f {
	case 0:
		n := int(b[1])
		if i+n > len(b) {
			return errMalformed
		}
		for g := 1; g <= n; g++ {
			if g < len(sids) {
				set(int(b[i]), f.names[g])
			}
			i++
		}
	case 1:
		n := int(b[1])
		if i+2*n > len(b) {
			return errMalformed
		}
		g := 1
		for r := 0; r < n; r++ {
			first, left := int(b[i]), int(b[i+1])
			i += 2
			for k := 0; k <= left; k++ {
				if g < len(sids) {
					set(first+k, f.names[g])
				}
				g++
			}
		}
	default:
		return errMalformed
	}
	if format&0x80 != 0 {
		if i >= len(b) {
			return errMalformed
		}
		n := int(b[i])
		i++
		if i+3*n > len(b) {
			return errMalformed
		}
		for s := 0; s < n; s++ {
			code, sid := int(b[i]), binary.BigEndian.Uint16(b[i+1:])
			i += 3
			// A supplement names a SID, which may have no glyph or a
			// String INDEX entry; the charge for it is its length.
			name := p.sidName(sid)
			if !p.opts.charge(len(name)) {
				return errLimit
			}
			set(code, name)
		}
	}
	f.hasEncoding = true
	return nil
}

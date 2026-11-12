package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"unicode/utf16"
)

// ttf is a parsed TrueType (glyf-outline) font: its tables by tag and what
// the subsetter needs from them. The input fonts are pinned by SHA-256
// (inputs.go), so the parser is strict: anything unexpected is an error,
// not a guess.
type ttf struct {
	tables     map[string][]byte
	numGlyphs  int
	longLoca   bool
	loca       []uint32 // numGlyphs+1 offsets into glyf
	cmap       map[rune]uint16
	unitsPerEm int
}

var be = binary.BigEndian

// parseTTF reads the table directory, maxp, head, loca and the Unicode cmap.
func parseTTF(data []byte) (*ttf, error) {
	if len(data) < 12 || be.Uint32(data) != 0x00010000 {
		return nil, errors.New("not a TrueType font")
	}
	n := int(be.Uint16(data[4:]))
	if 12+16*n > len(data) {
		return nil, errors.New("truncated table directory")
	}
	f := &ttf{tables: map[string][]byte{}}
	for i := 0; i < n; i++ {
		rec := data[12+16*i:]
		tag := string(rec[:4])
		off, ln := int(be.Uint32(rec[8:])), int(be.Uint32(rec[12:]))
		if off < 0 || ln < 0 || off+ln > len(data) {
			return nil, fmt.Errorf("table %q out of range", tag)
		}
		f.tables[tag] = data[off : off+ln]
	}
	for _, t := range []string{"head", "hhea", "hmtx", "maxp", "loca", "glyf", "cmap", "post", "name", "OS/2"} {
		if f.tables[t] == nil {
			return nil, fmt.Errorf("missing table %q", t)
		}
	}
	head := f.tables["head"]
	if len(head) != 54 {
		return nil, errors.New("bad head")
	}
	f.unitsPerEm = int(be.Uint16(head[18:]))
	f.longLoca = be.Uint16(head[50:]) == 1
	maxp := f.tables["maxp"]
	if len(maxp) < 6 {
		return nil, errors.New("bad maxp")
	}
	f.numGlyphs = int(be.Uint16(maxp[4:]))
	loca := f.tables["loca"]
	glyf := f.tables["glyf"]
	f.loca = make([]uint32, f.numGlyphs+1)
	for i := range f.loca {
		if f.longLoca {
			if 4*i+4 > len(loca) {
				return nil, errors.New("short loca")
			}
			f.loca[i] = be.Uint32(loca[4*i:])
		} else {
			if 2*i+2 > len(loca) {
				return nil, errors.New("short loca")
			}
			f.loca[i] = 2 * uint32(be.Uint16(loca[2*i:]))
		}
		if int(f.loca[i]) > len(glyf) || i > 0 && f.loca[i] < f.loca[i-1] {
			return nil, errors.New("bad loca")
		}
	}
	cm, err := parseUnicodeCmap(f.tables["cmap"])
	if err != nil {
		return nil, err
	}
	f.cmap = cm
	return f, nil
}

// parseUnicodeCmap reads the (3,10) format 12 subtable when present, else the
// (3,1) format 4 one.
func parseUnicodeCmap(t []byte) (map[rune]uint16, error) {
	if len(t) < 4 {
		return nil, errors.New("bad cmap")
	}
	n := int(be.Uint16(t[2:]))
	var off4, off12 = -1, -1
	for i := 0; i < n; i++ {
		r := t[4+8*i:]
		pid, eid, off := be.Uint16(r), be.Uint16(r[2:]), int(be.Uint32(r[4:]))
		if pid == 3 && eid == 1 {
			off4 = off
		}
		if pid == 3 && eid == 10 {
			off12 = off
		}
	}
	m := map[rune]uint16{}
	switch {
	case off12 >= 0:
		s := t[off12:]
		if be.Uint16(s) != 12 {
			return nil, errors.New("(3,10) not format 12")
		}
		ng := int(be.Uint32(s[12:]))
		for i := 0; i < ng; i++ {
			g := s[16+12*i:]
			lo, hi, gid := be.Uint32(g), be.Uint32(g[4:]), be.Uint32(g[8:])
			for c := lo; c <= hi; c++ {
				m[rune(c)] = uint16(gid + c - lo)
			}
		}
	case off4 >= 0:
		s := t[off4:]
		if be.Uint16(s) != 4 {
			return nil, errors.New("(3,1) not format 4")
		}
		segX2 := int(be.Uint16(s[6:]))
		seg := segX2 / 2
		ends := s[14:]
		starts := s[16+segX2:]
		deltas := s[16+2*segX2:]
		ranges := s[16+3*segX2:]
		for i := 0; i < seg; i++ {
			end, start := be.Uint16(ends[2*i:]), be.Uint16(starts[2*i:])
			delta, ro := be.Uint16(deltas[2*i:]), int(be.Uint16(ranges[2*i:]))
			for c := int(start); c <= int(end) && c != 0xFFFF; c++ {
				var gid uint16
				if ro == 0 {
					gid = uint16(c) + delta
				} else {
					p := 16 + 3*segX2 + 2*i + ro + 2*(c-int(start))
					if p+2 > len(s) {
						return nil, errors.New("bad format 4")
					}
					gid = be.Uint16(s[p:])
					if gid != 0 {
						gid += delta
					}
				}
				if gid != 0 {
					m[rune(c)] = gid
				}
			}
		}
	default:
		return nil, errors.New("no Windows Unicode cmap")
	}
	return m, nil
}

// glyph returns the glyf data of gid (empty for an empty glyph).
func (f *ttf) glyph(gid int) []byte {
	return f.tables["glyf"][f.loca[gid]:f.loca[gid+1]]
}

// Composite glyph flags (OpenType glyf table).
const (
	argsAreWords   = 0x0001
	haveScale      = 0x0008
	moreComponents = 0x0020
	haveXYScale    = 0x0040
	haveTwoByTwo   = 0x0080
	haveInstr      = 0x0100
)

// components returns the component glyph IDs of a composite glyph, and the
// byte offsets of their glyphIndex fields.
func components(g []byte) (gids []int, offs []int, end int, err error) {
	if len(g) < 10 || int16(be.Uint16(g)) >= 0 {
		return nil, nil, 0, nil
	}
	p := 10
	for {
		if p+4 > len(g) {
			return nil, nil, 0, errors.New("truncated composite")
		}
		flags := be.Uint16(g[p:])
		gids = append(gids, int(be.Uint16(g[p+2:])))
		offs = append(offs, p+2)
		p += 4
		if flags&argsAreWords != 0 {
			p += 4
		} else {
			p += 2
		}
		switch {
		case flags&haveScale != 0:
			p += 2
		case flags&haveXYScale != 0:
			p += 4
		case flags&haveTwoByTwo != 0:
			p += 8
		}
		if flags&moreComponents == 0 {
			return gids, offs, p, nil
		}
	}
}

// bbox returns a glyph's header bounding box (xMin, yMin, xMax, yMax) and
// false for an empty glyph.
func bbox(g []byte) ([4]int16, bool) {
	if len(g) < 10 {
		return [4]int16{}, false
	}
	return [4]int16{int16(be.Uint16(g[2:])), int16(be.Uint16(g[4:])), int16(be.Uint16(g[6:])), int16(be.Uint16(g[8:]))}, true
}

// stripInstructions returns a copy of a glyph without TrueType hinting
// instructions (the renderer ignores hints, design §1.1), with composite
// component glyph IDs remapped through remap.
func stripInstructions(g []byte, remap map[int]int) ([]byte, error) {
	if len(g) == 0 {
		return nil, nil
	}
	nc := int16(be.Uint16(g))
	if nc >= 0 {
		p := 10 + 2*int(nc)
		if p+2 > len(g) {
			return nil, errors.New("truncated simple glyph")
		}
		il := int(be.Uint16(g[p:]))
		if p+2+il > len(g) {
			return nil, errors.New("truncated instructions")
		}
		out := append([]byte(nil), g[:p]...)
		out = append(out, 0, 0)
		return append(out, g[p+2+il:]...), nil
	}
	_, offs, end, err := components(g)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), g[:end]...)
	for _, o := range offs {
		old := int(be.Uint16(out[o:]))
		nw, ok := remap[old]
		if !ok {
			return nil, fmt.Errorf("component %d not in subset", old)
		}
		be.PutUint16(out[o:], uint16(nw))
		// Clear WE_HAVE_INSTRUCTIONS on every component (the instructions
		// after the last one are dropped).
		fl := be.Uint16(out[o-2:])
		be.PutUint16(out[o-2:], fl&^haveInstr)
	}
	return out, nil
}

// closure adds every component of a composite glyph in set, transitively.
func (f *ttf) closure(set map[int]bool) error {
	stack := make([]int, 0, len(set))
	for g := range set {
		stack = append(stack, g)
	}
	for len(stack) > 0 {
		g := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		comps, _, _, err := components(f.glyph(g))
		if err != nil {
			return fmt.Errorf("glyph %d: %w", g, err)
		}
		for _, c := range comps {
			if c >= f.numGlyphs {
				return fmt.Errorf("glyph %d: component %d out of range", g, c)
			}
			if !set[c] {
				set[c] = true
				stack = append(stack, c)
			}
		}
	}
	return nil
}

// nameRecord is one Windows (3,1,0x409) name table string.
type nameRecord struct {
	id   uint16
	text string
}

// names returns the Windows English name strings by name ID.
func (f *ttf) names() map[uint16]string {
	t := f.tables["name"]
	out := map[uint16]string{}
	if len(t) < 6 {
		return out
	}
	n, strOff := int(be.Uint16(t[2:])), int(be.Uint16(t[4:]))
	for i := 0; i < n; i++ {
		r := t[6+12*i:]
		pid, eid, lang, id := be.Uint16(r), be.Uint16(r[2:]), be.Uint16(r[4:]), be.Uint16(r[6:])
		ln, off := int(be.Uint16(r[8:])), int(be.Uint16(r[10:]))
		if pid != 3 || eid != 1 || lang != 0x409 || strOff+off+ln > len(t) {
			continue
		}
		b := t[strOff+off : strOff+off+ln]
		u := make([]uint16, len(b)/2)
		for j := range u {
			u[j] = be.Uint16(b[2*j:])
		}
		out[id] = string(utf16.Decode(u))
	}
	return out
}

// buildName writes a name table of Windows (3,1,0x409) records.
func buildName(recs []nameRecord) []byte {
	sort.Slice(recs, func(i, j int) bool { return recs[i].id < recs[j].id })
	var strs []byte
	hdr := make([]byte, 6+12*len(recs))
	be.PutUint16(hdr[2:], uint16(len(recs)))
	be.PutUint16(hdr[4:], uint16(len(hdr)))
	for i, r := range recs {
		u := utf16.Encode([]rune(r.text))
		b := make([]byte, 2*len(u))
		for j, c := range u {
			be.PutUint16(b[2*j:], c)
		}
		e := hdr[6+12*i:]
		be.PutUint16(e, 3)
		be.PutUint16(e[2:], 1)
		be.PutUint16(e[4:], 0x409)
		be.PutUint16(e[6:], r.id)
		be.PutUint16(e[8:], uint16(len(b)))
		be.PutUint16(e[10:], uint16(len(strs)))
		strs = append(strs, b...)
	}
	return append(hdr, strs...)
}

// subsetSpec says what to keep and how to rename.
type subsetSpec struct {
	runes []rune // Unicode values to keep (absent ones are skipped)
	// keep, when set, filters glyphs (by original GID) before they are
	// added: false drops the glyph and its cmap entries.
	keep  func(gid int) bool
	names []nameRecord
}

// subset builds a new font with the glyphs of spec.runes (plus .notdef and
// composite components), no hinting, a format 4 (3,1) cmap, a format 3 post
// table and the given name table. Output is deterministic.
func (f *ttf) subset(spec subsetSpec) ([]byte, error) {
	set := map[int]bool{0: true}
	cmap := map[rune]int{}
	for _, r := range spec.runes {
		g, ok := f.cmap[r]
		if !ok || g == 0 || int(g) >= f.numGlyphs || r > 0xFFFF {
			continue
		}
		if spec.keep != nil && !spec.keep(int(g)) {
			continue
		}
		set[int(g)] = true
		cmap[r] = int(g)
	}
	if err := f.closure(set); err != nil {
		return nil, err
	}
	old := make([]int, 0, len(set))
	for g := range set {
		old = append(old, g)
	}
	sort.Ints(old)
	remap := map[int]int{}
	for i, g := range old {
		remap[g] = i
	}

	// glyf + loca (short format when it fits; glyphs padded to 4 bytes).
	var glyf []byte
	offsets := make([]uint32, len(old)+1)
	for i, g := range old {
		offsets[i] = uint32(len(glyf))
		data, err := stripInstructions(f.glyph(g), remap)
		if err != nil {
			return nil, fmt.Errorf("glyph %d: %w", g, err)
		}
		glyf = append(glyf, data...)
		for len(glyf)%4 != 0 {
			glyf = append(glyf, 0)
		}
	}
	offsets[len(old)] = uint32(len(glyf))
	longLoca := len(glyf)/2 > 0xFFFF
	var loca []byte
	for _, o := range offsets {
		if longLoca {
			loca = be.AppendUint32(loca, o)
		} else {
			loca = be.AppendUint16(loca, uint16(o/2))
		}
	}

	// hmtx: a full metric per glyph.
	hhea := append([]byte(nil), f.tables["hhea"]...)
	nhm := int(be.Uint16(hhea[34:]))
	src := f.tables["hmtx"]
	var hmtx []byte
	for _, g := range old {
		var adv uint16
		var lsb uint16
		if g < nhm {
			adv, lsb = be.Uint16(src[4*g:]), be.Uint16(src[4*g+2:])
		} else {
			adv = be.Uint16(src[4*(nhm-1):])
			lsb = be.Uint16(src[4*nhm+2*(g-nhm):])
		}
		hmtx = be.AppendUint16(hmtx, adv)
		hmtx = be.AppendUint16(hmtx, lsb)
	}
	be.PutUint16(hhea[34:], uint16(len(old)))

	maxp := append([]byte(nil), f.tables["maxp"]...)
	be.PutUint16(maxp[4:], uint16(len(old)))
	if len(maxp) >= 32 {
		be.PutUint16(maxp[26:], 0) // maxSizeOfInstructions
	}

	head := append([]byte(nil), f.tables["head"]...)
	be.PutUint32(head[8:], 0) // checkSumAdjustment, set below
	if longLoca {
		be.PutUint16(head[50:], 1)
	} else {
		be.PutUint16(head[50:], 0)
	}

	post := make([]byte, 32)
	copy(post, f.tables["post"][:16]) // version replaced, italicAngle, underline
	be.PutUint32(post, 0x00030000)
	copy(post[12:16], f.tables["post"][12:16]) // isFixedPitch

	runes := make([]rune, 0, len(cmap))
	for r := range cmap {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	cm := buildCmap4(runes, func(r rune) uint16 { return uint16(remap[cmap[r]]) })

	tables := map[string][]byte{
		"OS/2": f.tables["OS/2"],
		"cmap": cm,
		"glyf": glyf,
		"head": head,
		"hhea": hhea,
		"hmtx": hmtx,
		"loca": loca,
		"maxp": maxp,
		"name": buildName(spec.names),
		"post": post,
	}
	return writeSFNT(tables), nil
}

// buildCmap4 writes a cmap table with one (3,1) format 4 subtable mapping
// the sorted BMP runes to glyphs: one segment per run of consecutive code
// points with consecutive glyph IDs, plus the 0xFFFF terminator.
func buildCmap4(runes []rune, gid func(rune) uint16) []byte {
	type seg struct{ start, end, delta uint16 }
	var segs []seg
	for i := 0; i < len(runes); {
		j := i
		for j+1 < len(runes) && runes[j+1] == runes[j]+1 && gid(runes[j+1]) == gid(runes[j])+1 {
			j++
		}
		segs = append(segs, seg{uint16(runes[i]), uint16(runes[j]), gid(runes[i]) - uint16(runes[i])})
		i = j + 1
	}
	segs = append(segs, seg{0xFFFF, 0xFFFF, 1})
	n := len(segs)
	sub := make([]byte, 16+8*n)
	be.PutUint16(sub, 4)
	be.PutUint16(sub[2:], uint16(len(sub)))
	be.PutUint16(sub[6:], uint16(2*n))
	sr := 1
	es := 0
	for sr*2 <= n {
		sr *= 2
		es++
	}
	be.PutUint16(sub[8:], uint16(2*sr))
	be.PutUint16(sub[10:], uint16(es))
	be.PutUint16(sub[12:], uint16(2*n-2*sr))
	for i, s := range segs {
		be.PutUint16(sub[14+2*i:], s.end)
		be.PutUint16(sub[16+2*n+2*i:], s.start)
		be.PutUint16(sub[16+4*n+2*i:], s.delta)
		// idRangeOffset stays 0.
	}
	out := make([]byte, 12)
	be.PutUint16(out[2:], 1)
	be.PutUint16(out[4:], 3)
	be.PutUint16(out[6:], 1)
	be.PutUint32(out[8:], 12)
	return append(out, sub...)
}

// writeSFNT assembles tables (sorted by tag, 4-byte aligned, checksummed)
// and sets head.checkSumAdjustment.
func writeSFNT(tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	n := len(tags)
	out := make([]byte, 12+16*n)
	be.PutUint32(out, 0x00010000)
	be.PutUint16(out[4:], uint16(n))
	sr, es := 1, 0
	for sr*2 <= n {
		sr *= 2
		es++
	}
	be.PutUint16(out[6:], uint16(16*sr))
	be.PutUint16(out[8:], uint16(es))
	be.PutUint16(out[10:], uint16(16*n-16*sr))
	headOff := 0
	for i, t := range tags {
		data := tables[t]
		off := len(out)
		if t == "head" {
			headOff = off
		}
		rec := out[12+16*i:]
		copy(rec, t)
		be.PutUint32(rec[4:], checksum(data))
		be.PutUint32(rec[8:], uint32(off))
		be.PutUint32(rec[12:], uint32(len(data)))
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	be.PutUint32(out[headOff+8:], 0xB1B0AFBA-checksum(out))
	return out
}

// checksum is the OpenType table checksum: the sum of big-endian uint32s,
// the data zero-padded to a multiple of 4.
func checksum(b []byte) uint32 {
	var s uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:])
		s += be.Uint32(w[:])
	}
	return s
}

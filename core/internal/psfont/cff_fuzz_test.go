package psfont

import (
	"bytes"
	"encoding/binary"
	"testing"

	cb "github.com/thai-pc/fluxdocs/core/internal/psfont/cffbuild"
)

// cffSeeds are FuzzCFF's seeds: the hand-built fonts (non-CID with every
// charset and encoding format, CID-keyed with FDSelect formats 0 and 3) and
// hostile programs (design §5.4).
func cffSeeds() [][]byte {
	var seeds [][]byte
	for _, cf := range []int{0, 1, 2} {
		f := cffTestFont()
		f.CharsetFormat, f.EncodingFormat = cf, cf%2
		f.Supplements = map[int]string{0x61: "square"}
		seeds = append(seeds, f.Bytes())
	}
	seeds = append(seeds, cidTestFont(0).Bytes(), cidTestFont(3).Bytes())

	glyph := func(css ...[]byte) cb.Font {
		f := cb.Font{Glyphs: []cb.Glyph{{Name: ".notdef", CS: cb.CS(cb.EndChar)}}}
		for i, cs := range css {
			f.Glyphs = append(f.Glyphs, cb.Glyph{Name: string(rune('a' + i)), CS: cs})
		}
		return f
	}
	self := glyph(cb.CS(-107, cb.CallSubr, cb.EndChar))
	self.Subrs = [][]byte{cb.CS(-107, cb.CallSubr, cb.Return)}
	mutual := glyph(cb.CS(-107, cb.CallSubr, cb.EndChar))
	mutual.Subrs = [][]byte{cb.CS(-107, cb.CallGSubr, cb.Return)}
	mutual.GSubrs = [][]byte{cb.CS(-107, cb.CallSubr, cb.Return)}
	hostile := glyph(
		cb.CS(cb.Raw(bytes.Repeat(cb.CS(1, 1, cb.HStem), 60)), cb.HintMask, cb.Raw{0xff}), // a huge hintmask stem count
		cb.CS(cb.Random, 0, cb.Div, cb.EndChar),
		cb.CS(-1, cb.Sqrt, cb.EndChar),
		cb.CS(30000, cb.Dup, cb.Mul, cb.Dup, cb.Mul, cb.Dup, cb.Mul, cb.Dup, cb.Mul, cb.Dup, cb.Mul, cb.EndChar),
		cb.CS(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, cb.Flex),
		cb.CS(5, 40, cb.Put, 40, cb.Get, 99, cb.Index, 9, 9, cb.Roll, cb.EndChar),
	)
	seac := cffTestFont()
	seac.Glyphs = append(seac.Glyphs, cb.Glyph{Name: "grave", CS: cffAacute}, cb.Glyph{Name: "x", CS: cb.CS(0, 0, 65, 193, cb.EndChar)})
	fds := cidTestFont(3)
	fds.FDs = make([]cb.FD, maxFDs)
	fds.FDSelect = []int{0, maxFDs - 1, 3, 3}
	badFD := cidTestFont(0)
	badFD.FDSelect = []int{0, 0, 7, 0}
	t1 := cffTestFont()
	t1.CharstringType = 1
	seeds = append(seeds, self.Bytes(), mutual.Bytes(), hostile.Bytes(), seac.Bytes(), fds.Bytes(), badFD.Bytes(), t1.Bytes())

	// Raw INDEX shapes after a valid header: offSize 5, decreasing offsets,
	// and a count whose offsets run past the data (count × offSize).
	hdr := []byte{1, 0, 4, 4}
	seeds = append(seeds,
		append(append([]byte(nil), hdr...), 0, 1, 5, 0, 0, 0, 0, 1, 0, 0, 0, 0, 2, 'x'),
		append(append([]byte(nil), hdr...), 0, 2, 1, 1, 3, 2, 'x', 'y'),
		append(append([]byte(nil), hdr...), 0xff, 0xff, 4, 0, 0, 0, 1),
		hdr,
	)
	// A Top DICT whose Private DICT and charset offsets are near MaxInt32.
	top := []byte{29, 0x7f, 0xff, 0xff, 0xff, 29, 0x7f, 0xff, 0xff, 0xff, 18, 29, 0x7f, 0xff, 0xff, 0xfe, 15}
	topIdx := cb.EncodeIndex([][]byte{top})
	seeds = append(seeds, bytes.Join([][]byte{hdr, cb.EncodeIndex([][]byte{[]byte("F")}), topIdx, {0, 0}, {0, 0}}, nil))
	return seeds
}

// FuzzCFF parses a bare CFF program and builds its glyphs: no input may
// panic, hang, or allocate without bound.
func FuzzCFF(f *testing.F) {
	for _, s := range cffSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var charged int64
		font, err := ParseCFF(data, Options{Charge: func(n int64) bool {
			charged += n
			return charged <= 4*maxProgramBytes
		}})
		if err != nil {
			return
		}
		_ = font.Matrix()
		_, _ = font.BuiltinEncoding()
		_, _ = font.GlyphByName(".notdef")
		for _, cid := range []uint32{0, 1, 2, 7, 100, 500, 0xFFFF, 0x10000} {
			if g, ok := font.GlyphByCID(cid); ok && int(g) >= font.NumGlyphs() {
				t.Fatalf("GlyphByCID(%d) = %d past %d glyphs", cid, g, font.NumGlyphs())
			}
		}
		for g := 0; g < font.NumGlyphs() && g < fuzzGlyphCap; g++ {
			gl, err := font.Outline(GID(g))
			if gl.Ops > maxGlyphOps+1 || len(gl.Segs) > maxGlyphSegments {
				t.Fatalf("glyph %d past the limits: %d ops, %d segments", g, gl.Ops, len(gl.Segs))
			}
			if err == nil {
				checkPoints(t, gl)
				if !validMatrix(gl.Matrix) {
					t.Fatalf("glyph %d matrix %v", g, gl.Matrix)
				}
			}
		}
	})
}

// t2Font builds the synthetic font FuzzT2Charstring runs against: glyph 0
// is cs; glyphs A and acute (the seac components of StandardEncoding codes
// 65 and 194) are sub and gsub; local subrs are sub and cs, global subrs
// gsub and sub.
func t2Font(cs, sub, gsub []byte) *Font {
	idx := func(objs ...[]byte) cffIndex {
		x, _, err := readIndex(cb.EncodeIndex(objs), 0)
		if err != nil {
			panic(err)
		}
		return x
	}
	return &Font{
		matrix: defaultMatrix,
		names:  []string{"g", "A", "acute"},
		byName: map[string]GID{"g": 0, "A": 1, "acute": 2},
		cff: &cffData{
			charstrings: idx(cs, sub, gsub),
			gsubrs:      idx(gsub, sub),
			fds:         []cffFD{{subrs: idx(sub, cs), defaultWidthX: 500, nominalWidthX: 100, matrix: defaultMatrix}},
		},
	}
}

// FuzzT2Charstring runs a raw Type 2 charstring against t2Font, whose local
// and global subroutines and seac components are fuzzed too.
func FuzzT2Charstring(f *testing.F) {
	for _, s := range [][3][]byte{
		{cffSquare, cb.CS(cb.Return), cb.CS(cb.Return)},
		{cffCurves, nil, nil},
		{cffHint, nil, nil},
		{cb.CS(0, 0, 65, 194, cb.EndChar), cffA, cffAcute},
		{cb.CS(0, 0, 65, 194, cb.EndChar), cb.CS(0, 0, 65, 194, cb.EndChar), nil},
		{cb.CS(-107, cb.CallSubr, cb.EndChar), cb.CS(-107, cb.CallSubr, cb.Return), nil},
		{cb.CS(-107, cb.CallGSubr, cb.EndChar), cb.CS(-106, cb.CallGSubr, cb.Return), cb.CS(-106, cb.CallSubr, cb.Return)},
		{cb.CS(1, 1, 1, cb.HStem, 1, cb.HintMask, cb.Raw{0xff}, cb.EndChar), nil, nil},
		{cb.CS(cb.Random, cb.Dup, cb.Mul, cb.Sqrt, 0, cb.Div), nil, nil},
		{bytes.Repeat(cb.CS(1, cb.HLineTo), 9000), nil, nil},
		{[]byte{255, 0x7f, 0xff, 0xff, 0xff, 255, 0x7f, 0xff, 0xff, 0xff, 21}, nil, nil},
		{binary.BigEndian.AppendUint16([]byte{28}, 0x8000), nil, nil},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, cs, sub, gsub []byte) {
		font := t2Font(cs, sub, gsub)
		for g := GID(0); g < 3; g++ {
			gl, err := font.Outline(g)
			if gl.Ops > maxGlyphOps+1 || len(gl.Segs) > maxGlyphSegments {
				t.Fatalf("past the limits: %d ops, %d segments", gl.Ops, len(gl.Segs))
			}
			if err == nil {
				checkPoints(t, gl)
			}
		}
	})
}

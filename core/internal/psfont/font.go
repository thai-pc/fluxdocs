// Package psfont parses the PostScript-based font programs a PDF may embed and
// builds glyph outlines from them: Type 1 (/FontFile, ISO 32000-1:2008 §9.9)
// with Type 1 charstrings, and bare CFF (/FontFile3 /Type1C and
// /CIDFontType0C, §9.9 Table 127), CID-keyed or not, with Type 2 charstrings
// (docs/design/type1-std14-fonts.md §2.2, §2.3).
//
// A program is untrusted input: every read is bounds-checked, recursion is
// bounded by an explicit depth, and limits.go caps sizes, counts and executed
// operators. Any error drops the font (from Parse*) or one glyph (from
// Outline); callers treat both as "not drawn" and never fail a page on them.
//
// Clean-room: written from Adobe's Type 1 Font Format (version 1.1, Chapters
// 6–8), Technical Notes #5015, #5040, #5176 (CFF) and #5177 (Type 2
// charstrings), and ISO 32000-1 only. Pure Go; Core (MIT).
package psfont

import (
	"errors"
	"math"
)

// Package-internal errors. Callers treat any error as "glyph (or font) not
// drawn"; the distinction is for tests.
var (
	// errMalformed reports a program or charstring that does not follow the
	// format (an unknown structure, a stack underflow, a bad subroutine).
	errMalformed = errors.New("psfont: malformed program")
	// errLimit reports a program or glyph past a limits.go bound, or a
	// charge the caller refused.
	errLimit = errors.New("psfont: limit exceeded")
)

// GID identifies a glyph within one Font: an index into its charstrings.
type GID uint32

// SegOp is the kind of an outline segment.
type SegOp uint8

// Outline segment kinds. A MoveTo starts a new subpath; every subpath is
// closed for filling.
const (
	MoveTo SegOp = iota
	LineTo
	CubeTo
)

// Point is a position in glyph space (the program's units, before its
// FontMatrix).
type Point struct{ X, Y float64 }

// Segment is one outline segment: MoveTo and LineTo use P[0]; CubeTo uses
// P[0] and P[1] as control points and P[2] as the end point.
type Segment struct {
	Op SegOp
	P  [3]Point
}

// Glyph is a built glyph.
type Glyph struct {
	// Segs is the outline in glyph space, filled with the nonzero rule.
	Segs []Segment
	// Width is the advance's x component in glyph space (hsbw or sbw; for
	// CFF the charstring's width or the Private DICT's defaultWidthX).
	Width float64
	// Matrix maps this glyph's space to text space at font size 1: the
	// font's FontMatrix, or for a CID-keyed CFF the one its Font DICT
	// selects (§2.3), validated like Font.Matrix.
	Matrix [6]float64
	// Ops is the number of charstring operators executed to build it,
	// subroutines and seac components included. It is set on error too, so
	// a caller can charge the work done.
	Ops int
}

// Options configures a Parse function.
type Options struct {
	// Charge, when non-nil, is called with the size of each buffer the
	// parser allocates for a decoded copy of the program (PFB segments
	// stripped, hex decoded, eexec decrypted, charstrings decrypted) or,
	// for CFF, for the lookup tables it builds (charset, glyph names, the
	// name and CID maps, an estimate per entry), before allocating it.
	// Returning false aborts the parse with an error.
	Charge func(n int64) bool
}

// charge calls o.Charge, if any.
func (o Options) charge(n int) bool {
	return o.Charge == nil || o.Charge(int64(n))
}

// defaultMatrix is the FontMatrix of a program that has no usable one.
var defaultMatrix = [6]float64{0.001, 0, 0, 0.001, 0, 0}

// Font is a parsed font program. It is immutable after parsing and safe for
// concurrent use: Outline keeps its state on the stack.
type Font struct {
	matrix      [6]float64
	encoding    [256]string
	hasEncoding bool

	names       []string // glyph names by GID
	charstrings [][]byte // Type 1: decrypted charstrings by GID (lenIV bytes removed)
	byName      map[string]GID
	subrs       [][]byte // Type 1: decrypted subroutines (nil entries are missing)

	cff *cffData // CFF programs: charstrings, subroutines, Font DICTs
}

// Matrix returns the program's validated FontMatrix (glyph space to text
// space at font size 1).
func (f *Font) Matrix() [6]float64 { return f.matrix }

// BuiltinEncoding returns the program's own encoding (the Type 1 /Encoding
// array, or StandardEncoding when the program names it), and false when the
// program declares none.
func (f *Font) BuiltinEncoding() (names [256]string, ok bool) {
	return f.encoding, f.hasEncoding
}

// GlyphByName returns the glyph named name, and false when the program has
// none.
func (f *Font) GlyphByName(name string) (GID, bool) {
	g, ok := f.byName[name]
	return g, ok
}

// GlyphByCID returns the glyph a CIDFont selects for CID cid (§9.7.4.2): in
// a CID-keyed CFF the glyph its charset gives that CID; in any other program
// the glyph whose index is cid. ok is false when there is none.
func (f *Font) GlyphByCID(cid uint32) (GID, bool) {
	if f.cff != nil && f.cff.cidKeyed {
		if cid > 0xFFFF {
			return 0, false
		}
		g, ok := f.cff.byCID[uint16(cid)]
		return g, ok
	}
	if int64(cid) >= int64(f.NumGlyphs()) {
		return 0, false
	}
	return GID(cid), true
}

// CIDKeyed reports whether the program is a CID-keyed CFF (its Top DICT
// starts with ROS): its glyphs have CIDs, not names.
func (f *Font) CIDKeyed() bool { return f.cff != nil && f.cff.cidKeyed }

// NumGlyphs returns the number of glyphs in the program.
func (f *Font) NumGlyphs() int {
	if f.cff != nil {
		return f.cff.charstrings.count
	}
	return len(f.charstrings)
}

// Outline builds glyph gid. On error (a malformed charstring or a limit) the
// glyph must not be drawn; Glyph.Ops still reports the operators executed.
func (f *Font) Outline(gid GID) (Glyph, error) {
	if int64(gid) >= int64(f.NumGlyphs()) {
		return Glyph{}, errMalformed
	}
	if f.cff != nil {
		return f.runType2(gid)
	}
	return f.runType1(gid)
}

// validMatrix reports whether m is usable as a FontMatrix: six finite
// entries, each of magnitude at most 1, with a non-zero determinant.
func validMatrix(m [6]float64) bool {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1 {
			return false
		}
	}
	det := float64(m[0]*m[3]) - float64(m[1]*m[2])
	return det != 0 && !math.IsNaN(det)
}

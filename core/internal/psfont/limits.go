package psfont

// Hard limits on what one font program may cost (SECURITY.md §2; design
// docs/design/type1-std14-fonts.md §5.1). Hitting a structural limit drops the
// font; hitting a per-glyph limit drops the glyph. None of them is an error
// for the page.
const (
	// maxProgramBytes bounds a program and each copy the parser makes of it
	// (PFB segments stripped, hex decoded, eexec decrypted).
	maxProgramBytes = 32 << 20
	// maxSubrs and maxCharStrings bound the /Subrs and /CharStrings entries.
	maxSubrs       = 1 << 16
	maxCharStrings = 1 << 16
	// maxCharstringLen bounds one charstring or subroutine (the Type 2 limit,
	// applied to Type 1 too).
	maxCharstringLen = 65535
	// maxLenIV is the largest /lenIV accepted; −1 means "not encrypted".
	maxLenIV = 16

	// maxSubrDepth bounds callsubr nesting (Type 1 Font Format Ch. 6).
	maxSubrDepth = 10
	// maxStack bounds the Type 1 operand stack; maxPSStack the PostScript
	// stack that carries OtherSubr results to pop.
	maxStack   = 24
	maxPSStack = 24
	// maxGlyphOps bounds the charstring operators executed for one glyph,
	// subroutines and seac components included. Real glyphs use < 1,000;
	// a looping or self-recursive glyph ends here.
	maxGlyphOps = 16384
	// maxGlyphSegments bounds the outline segments of one glyph.
	maxGlyphSegments = 8192
	// maxCoord bounds every coordinate, in glyph units.
	maxCoord = 1 << 20
	// flexPoints is the number of rmoveto points a flex collects: the
	// reference point and the six control and end points of two curves.
	flexPoints = 7
	// maxEncodingPuts bounds the `dup code /name put` entries read from a
	// cleartext /Encoding array.
	maxEncodingPuts = 256

	// maxT2Stack bounds the Type 2 argument stack (TN #5177 Appendix B).
	maxT2Stack = 48
	// maxStems bounds the Type 2 stem hints of one glyph (TN #5177
	// Appendix B), which size every hintmask and cntrmask.
	maxStems = 96
	// t2TransientSize is the number of Type 2 transient array slots that
	// put and get may address (TN #5177 Appendix B).
	t2TransientSize = 32
	// maxDictOperands bounds the operands before one CFF DICT operator (the
	// Type 2 stack limit; TN #5176 Table 2 operators take at most 13).
	maxDictOperands = 48
	// maxRealNibbles bounds the nibbles of one DICT real number.
	maxRealNibbles = 64
	// maxFDs bounds a CID-keyed font's FDArray: FDSelect stores an FD index
	// in a Card8.
	maxFDs = 256
)

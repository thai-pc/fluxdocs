package psfont

import (
	"encoding/binary"
	"math"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// Type 2 charstring operators (TN #5177 Appendix A). Escaped operators (byte
// 12 followed by a second byte) are numbered 32 + the second byte.
const (
	t2hstem      = 1
	t2vstem      = 3
	t2vmoveto    = 4
	t2rlineto    = 5
	t2hlineto    = 6
	t2vlineto    = 7
	t2rrcurveto  = 8
	t2callsubr   = 10
	t2return     = 11
	t2escape     = 12
	t2endchar    = 14
	t2hstemhm    = 18
	t2hintmask   = 19
	t2cntrmask   = 20
	t2rmoveto    = 21
	t2hmoveto    = 22
	t2vstemhm    = 23
	t2rcurveline = 24
	t2rlinecurve = 25
	t2vvcurveto  = 26
	t2hhcurveto  = 27
	t2shortint   = 28
	t2callgsubr  = 29
	t2vhcurveto  = 30
	t2hvcurveto  = 31
	t2and        = 32 + 3
	t2or         = 32 + 4
	t2not        = 32 + 5
	t2abs        = 32 + 9
	t2add        = 32 + 10
	t2sub        = 32 + 11
	t2div        = 32 + 12
	t2neg        = 32 + 14
	t2eq         = 32 + 15
	t2drop       = 32 + 18
	t2put        = 32 + 20
	t2get        = 32 + 21
	t2ifelse     = 32 + 22
	t2random     = 32 + 23
	t2mul        = 32 + 24
	t2sqrt       = 32 + 26
	t2dup        = 32 + 27
	t2exch       = 32 + 28
	t2index      = 32 + 29
	t2roll       = 32 + 30
	t2hflex      = 32 + 34
	t2flex       = 32 + 35
	t2hflex1     = 32 + 36
	t2flex1      = 32 + 37
)

// t2Random is what the random operator returns. TN #5177 asks for a
// pseudo-random number in (0, 1]; a constant keeps the output identical on
// every target and every run (design §2.3, §6).
const t2Random = 0.5

// subrBias returns the bias added to a callsubr or callgsubr operand for a
// subroutine INDEX of count entries (TN #5177 §4.7).
func subrBias(count int) int {
	switch {
	case count < 1240:
		return 107
	case count < 33900:
		return 1131
	}
	return 32768
}

// t2interp is the state of one Type 2 glyph build. It lives on the caller's
// stack, so a Font is safe for concurrent Outline calls.
type t2interp struct {
	c     *cffData
	f     *Font
	fd    *cffFD // the glyph's Font DICT: local subroutines and widths
	stack [maxT2Stack]float64
	sp    int
	trans [t2TransientSize]float64

	x, y   float64 // current point, glyph space (before the seac offset)
	dx, dy float64 // seac accent offset added to every emitted point
	open   bool    // a subpath has been started (MoveTo emitted)

	stems     int  // stem hints declared so far: they size hintmask
	haveWidth bool // the first stack-clearing operator has run
	width     float64
	segs      []Segment
	ops       int
	depth     int  // callsubr / callgsubr nesting
	inSeac    bool // building a seac component: seac is not allowed again
	done      bool // endchar reached
}

// runType2 builds glyph gid of a CFF font with its Font DICT's subroutines,
// widths and FontMatrix.
func (f *Font) runType2(gid GID) (Glyph, error) {
	c := f.cff
	fdi := c.fdSelect.fd(int(gid))
	if fdi >= len(c.fds) {
		return Glyph{}, errMalformed
	}
	fd := &c.fds[fdi]
	cs, _ := c.charstrings.get(int(gid))
	in := &t2interp{c: c, f: f, fd: fd, width: fd.defaultWidthX}
	err := in.run(cs) // a charstring without endchar ends at its last byte
	g := Glyph{Width: in.width, Ops: in.ops, Matrix: fd.matrix}
	if err != nil {
		return g, err
	}
	g.Segs = in.segs
	return g, nil
}

// push pushes v on the argument stack; a value that is not finite (an
// arithmetic overflow) aborts the glyph.
func (in *t2interp) push(v float64) error {
	if in.sp >= maxT2Stack {
		return errLimit
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errLimit
	}
	in.stack[in.sp] = v
	in.sp++
	return nil
}

// pop removes and returns the top n operands, or errMalformed on underflow.
// The returned slice aliases the stack: use it before the next push.
func (in *t2interp) pop(n int) ([]float64, error) {
	if in.sp < n {
		return nil, errMalformed
	}
	in.sp -= n
	return in.stack[in.sp : in.sp+n], nil
}

// run executes one charstring (the glyph's, a subroutine's or a seac
// component's) until endchar, return or its end.
func (in *t2interp) run(cs []byte) error {
	if len(cs) > maxCharstringLen {
		return errLimit
	}
	for i := 0; i < len(cs) && !in.done; {
		v := cs[i]
		i++
		switch {
		case v >= 32 && v <= 246:
			if err := in.push(float64(int(v) - 139)); err != nil {
				return err
			}
			continue
		case v >= 247 && v <= 250:
			if i >= len(cs) {
				return errMalformed
			}
			if err := in.push(float64((int(v)-247)*256 + int(cs[i]) + 108)); err != nil {
				return err
			}
			i++
			continue
		case v >= 251 && v <= 254:
			if i >= len(cs) {
				return errMalformed
			}
			if err := in.push(float64(-(int(v)-251)*256 - int(cs[i]) - 108)); err != nil {
				return err
			}
			i++
			continue
		case v == t2shortint:
			if i+2 > len(cs) {
				return errMalformed
			}
			if err := in.push(float64(int16(binary.BigEndian.Uint16(cs[i:])))); err != nil {
				return err
			}
			i += 2
			continue
		case v == 255: // 16.16 fixed point
			if i+4 > len(cs) {
				return errMalformed
			}
			if err := in.push(float64(int32(binary.BigEndian.Uint32(cs[i:]))) / 65536); err != nil {
				return err
			}
			i += 4
			continue
		}
		op := int(v)
		if op == t2escape {
			if i >= len(cs) {
				return errMalformed
			}
			op = 32 + int(cs[i])
			i++
		}
		if in.ops++; in.ops > maxGlyphOps {
			return errLimit
		}
		switch op {
		case t2return:
			return nil
		case t2hintmask, t2cntrmask:
			n, err := in.hintmask()
			if err != nil {
				return err
			}
			if n > len(cs)-i {
				return errMalformed
			}
			i += n // the mask bytes follow the operator
			continue
		}
		if err := in.exec(op); err != nil {
			return err
		}
	}
	return nil
}

// takeWidth handles the width the first stack-clearing operator may carry
// (TN #5177 §4.1): when has is true (an odd operand count for stems and
// masks, an extra operand for movetos and endchar), the bottom operand is
// the width less nominalWidthX. It returns the index of the operator's first
// own operand. Later operators carry no width; a seac component's width is
// consumed but not kept.
func (in *t2interp) takeWidth(has bool) (int, error) {
	if in.haveWidth {
		return 0, nil
	}
	in.haveWidth = true
	if !has || in.sp == 0 {
		return 0, nil
	}
	if !in.inSeac {
		w := in.fd.nominalWidthX + in.stack[0]
		if !finiteCoord(w) {
			return 0, errLimit
		}
		in.width = w
	}
	return 1, nil
}

// hintmask runs hintmask or cntrmask: operands before it are an implied
// vstem (pairs, after a possible width), the stem count must stay within
// maxStems, and it returns the number of mask bytes that follow, one bit per
// stem declared so far.
func (in *t2interp) hintmask() (int, error) {
	base, err := in.takeWidth(in.sp%2 == 1)
	if err != nil {
		return 0, err
	}
	in.stems += (in.sp - base) / 2
	in.sp = 0
	if in.stems > maxStems {
		return 0, errLimit
	}
	return (in.stems + 7) / 8, nil
}

// exec runs one operator other than return, hintmask and cntrmask.
func (in *t2interp) exec(op int) error {
	switch op {
	case t2callsubr, t2callgsubr:
		a, err := in.pop(1)
		if err != nil {
			return err
		}
		idx := &in.c.gsubrs
		if op == t2callsubr {
			idx = &in.fd.subrs
		}
		return in.callSubr(idx, a[0])
	case t2and, t2or, t2not, t2abs, t2add, t2sub, t2div, t2neg, t2eq, t2drop, t2put, t2get,
		t2ifelse, t2random, t2mul, t2sqrt, t2dup, t2exch, t2index, t2roll:
		return in.arith(op)
	}
	err := in.path(op)
	in.sp = 0 // every other operator clears the stack
	return err
}

// path runs a stem, path-construction or endchar operator over the stack.
func (in *t2interp) path(op int) error {
	var base int
	var err error
	switch op {
	case t2hstem, t2vstem, t2hstemhm, t2vstemhm:
		if base, err = in.takeWidth(in.sp%2 == 1); err != nil {
			return err
		}
		if in.stems += (in.sp - base) / 2; in.stems > maxStems {
			return errLimit
		}
		return nil
	case t2rmoveto:
		base, err = in.takeWidth(in.sp > 2)
	case t2hmoveto, t2vmoveto:
		base, err = in.takeWidth(in.sp > 1)
	case t2endchar:
		base, err = in.takeWidth(in.sp == 1 || in.sp == 5)
	default:
		// A drawing operator first is malformed per TN #5177; tolerated
		// as "no width".
		base, err = in.takeWidth(false)
	}
	if err != nil {
		return err
	}
	a := in.stack[base:in.sp]
	switch op {
	case t2rmoveto:
		if len(a) < 2 {
			return errMalformed
		}
		return in.moveTo(a[0], a[1])
	case t2hmoveto:
		if len(a) < 1 {
			return errMalformed
		}
		return in.moveTo(a[0], 0)
	case t2vmoveto:
		if len(a) < 1 {
			return errMalformed
		}
		return in.moveTo(0, a[0])
	case t2rlineto:
		return in.lines(a)
	case t2hlineto, t2vlineto:
		return in.altLines(a, op == t2hlineto)
	case t2rrcurveto:
		return in.curves(a)
	case t2hhcurveto:
		return in.hhCurves(a)
	case t2vvcurveto:
		return in.vvCurves(a)
	case t2hvcurveto, t2vhcurveto:
		return in.altCurves(a, op == t2hvcurveto)
	case t2rcurveline:
		if len(a) < 8 {
			return errMalformed
		}
		n := (len(a) - 2) / 6 * 6
		if err := in.curves(a[:n]); err != nil {
			return err
		}
		return in.lineTo(a[n], a[n+1])
	case t2rlinecurve:
		if len(a) < 8 {
			return errMalformed
		}
		n := (len(a) - 6) / 2 * 2
		if err := in.lines(a[:n]); err != nil {
			return err
		}
		return in.curveTo(a[n], a[n+1], a[n+2], a[n+3], a[n+4], a[n+5])
	case t2flex, t2hflex, t2hflex1, t2flex1:
		return in.flex(op, a)
	case t2endchar:
		in.open = false
		if len(a) == 4 {
			return in.seac(a[0], a[1], a[2], a[3])
		}
		in.done = true
		return nil
	}
	// dotsection (deprecated) and reserved operators: ignored with their
	// operands.
	return nil
}

// lines runs rlineto's {dxa dya}+.
func (in *t2interp) lines(a []float64) error {
	if len(a) < 2 {
		return errMalformed
	}
	for i := 0; i+2 <= len(a); i += 2 {
		if err := in.lineTo(a[i], a[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// altLines runs hlineto or vlineto: lines alternating horizontal and
// vertical, starting horizontal for hlineto.
func (in *t2interp) altLines(a []float64, horiz bool) error {
	if len(a) < 1 {
		return errMalformed
	}
	for _, d := range a {
		var err error
		if horiz {
			err = in.lineTo(d, 0)
		} else {
			err = in.lineTo(0, d)
		}
		if err != nil {
			return err
		}
		horiz = !horiz
	}
	return nil
}

// curves runs rrcurveto's {dxa dya dxb dyb dxc dyc}+.
func (in *t2interp) curves(a []float64) error {
	if len(a) < 6 {
		return errMalformed
	}
	for i := 0; i+6 <= len(a); i += 6 {
		if err := in.curveTo(a[i], a[i+1], a[i+2], a[i+3], a[i+4], a[i+5]); err != nil {
			return err
		}
	}
	return nil
}

// hhCurves runs hhcurveto: dy1? {dxa dxb dyb dxc}+, curves that start and
// end horizontal (the first may start with dy1).
func (in *t2interp) hhCurves(a []float64) error {
	i, d1 := 0, 0.0
	if len(a)%4 == 1 {
		i, d1 = 1, a[0]
	}
	if len(a)-i < 4 {
		return errMalformed
	}
	for ; i+4 <= len(a); i += 4 {
		if err := in.curveTo(a[i], d1, a[i+1], a[i+2], a[i+3], 0); err != nil {
			return err
		}
		d1 = 0
	}
	return nil
}

// vvCurves runs vvcurveto: dx1? {dya dxb dyb dyc}+, curves that start and
// end vertical (the first may start with dx1).
func (in *t2interp) vvCurves(a []float64) error {
	i, d1 := 0, 0.0
	if len(a)%4 == 1 {
		i, d1 = 1, a[0]
	}
	if len(a)-i < 4 {
		return errMalformed
	}
	for ; i+4 <= len(a); i += 4 {
		if err := in.curveTo(d1, a[i], a[i+1], a[i+2], 0, a[i+3]); err != nil {
			return err
		}
		d1 = 0
	}
	return nil
}

// altCurves runs hvcurveto or vhcurveto: curves of four operands whose start
// tangent alternates horizontal and vertical (horizontal first for
// hvcurveto), each ending perpendicular to its start; a fifth operand on the
// last curve is its final, otherwise zero, coordinate.
func (in *t2interp) altCurves(a []float64, horiz bool) error {
	if len(a) < 4 {
		return errMalformed
	}
	for i := 0; i+4 <= len(a); i += 4 {
		extra := 0.0
		if len(a)-i == 5 {
			extra = a[i+4]
		}
		var err error
		if horiz {
			err = in.curveTo(a[i], 0, a[i+1], a[i+2], extra, a[i+3])
		} else {
			err = in.curveTo(0, a[i], a[i+1], a[i+2], a[i+3], extra)
		}
		if err != nil {
			return err
		}
		horiz = !horiz
	}
	return nil
}

// flex runs flex, hflex, hflex1 and flex1 (TN #5177 §4.1), always drawn as
// their two curves (the flex depth, which only decides when a renderer may
// flatten them, is ignored).
func (in *t2interp) flex(op int, a []float64) error {
	var c [12]float64
	switch op {
	case t2flex: // dx1 dy1 … dx6 dy6 fd
		if len(a) < 13 {
			return errMalformed
		}
		copy(c[:], a[:12])
	case t2hflex: // dx1 dx2 dy2 dx3 dx4 dx5 dx6: ends at the start's y
		if len(a) < 7 {
			return errMalformed
		}
		c = [12]float64{a[0], 0, a[1], a[2], a[3], 0, a[4], 0, a[5], -a[2], a[6], 0}
	case t2hflex1: // dx1 dy1 dx2 dy2 dx3 dx4 dx5 dy5 dx6: ends at the start's y
		if len(a) < 9 {
			return errMalformed
		}
		c = [12]float64{a[0], a[1], a[2], a[3], a[4], 0, a[5], 0, a[6], a[7], a[8], -(a[1] + a[3] + a[7])}
	case t2flex1: // dx1 dy1 … dx5 dy5 d6: d6 is dx6 or dy6, the other returns to the start
		if len(a) < 11 {
			return errMalformed
		}
		copy(c[:10], a[:10])
		dx := a[0] + a[2] + a[4] + a[6] + a[8]
		dy := a[1] + a[3] + a[5] + a[7] + a[9]
		if math.Abs(dx) > math.Abs(dy) {
			c[10], c[11] = a[10], -dy
		} else {
			c[10], c[11] = -dx, a[10]
		}
	}
	if err := in.curveTo(c[0], c[1], c[2], c[3], c[4], c[5]); err != nil {
		return err
	}
	return in.curveTo(c[6], c[7], c[8], c[9], c[10], c[11])
}

// moveTo starts a new subpath at the current point plus (dx, dy); the
// previous one is closed (TN #5177: every moveto closes the subpath).
func (in *t2interp) moveTo(dx, dy float64) error {
	in.x += dx
	in.y += dy
	in.open = false
	return in.checkPoint()
}

// lineTo draws a line to the current point plus (dx, dy).
func (in *t2interp) lineTo(dx, dy float64) error {
	if err := in.startSubpath(); err != nil {
		return err
	}
	in.x += dx
	in.y += dy
	if err := in.checkPoint(); err != nil {
		return err
	}
	return in.emit(Segment{Op: LineTo, P: [3]Point{in.out(in.x, in.y)}})
}

// curveTo draws a cubic Bézier curve from relative control-point deltas.
func (in *t2interp) curveTo(dxa, dya, dxb, dyb, dxc, dyc float64) error {
	if err := in.startSubpath(); err != nil {
		return err
	}
	x1, y1 := in.x+dxa, in.y+dya
	x2, y2 := x1+dxb, y1+dyb
	in.x, in.y = x2+dxc, y2+dyc
	if !finiteCoord(x1) || !finiteCoord(y1) || !finiteCoord(x2) || !finiteCoord(y2) {
		return errLimit
	}
	if err := in.checkPoint(); err != nil {
		return err
	}
	return in.emit(Segment{Op: CubeTo, P: [3]Point{in.out(x1, y1), in.out(x2, y2), in.out(in.x, in.y)}})
}

// startSubpath emits the MoveTo of a new subpath at the current point when a
// drawing operator follows a moveto or the glyph start.
func (in *t2interp) startSubpath() error {
	if in.open {
		return nil
	}
	in.open = true
	return in.emit(Segment{Op: MoveTo, P: [3]Point{in.out(in.x, in.y)}})
}

// out applies the seac accent offset.
func (in *t2interp) out(x, y float64) Point { return Point{x + in.dx, y + in.dy} }

// emit appends a segment within maxGlyphSegments. Every emitted point, seac
// offset included, must lie within ±maxCoord.
func (in *t2interp) emit(s Segment) error {
	if len(in.segs) >= maxGlyphSegments {
		return errLimit
	}
	for _, p := range s.P {
		if !finiteCoord(p.X) || !finiteCoord(p.Y) {
			return errLimit
		}
	}
	in.segs = append(in.segs, s)
	return nil
}

// checkPoint rejects a current point outside ±maxCoord or not finite.
func (in *t2interp) checkPoint() error {
	if !finiteCoord(in.x) || !finiteCoord(in.y) {
		return errLimit
	}
	return nil
}

// callSubr runs the subroutine of idx that v selects after the bias, within
// maxSubrDepth.
func (in *t2interp) callSubr(idx *cffIndex, v float64) error {
	if in.depth >= maxSubrDepth {
		return errLimit
	}
	n, ok := intIn(v+float64(subrBias(idx.count)), 0, idx.count-1)
	if !ok {
		return errMalformed
	}
	sub, _ := idx.get(n)
	in.depth++
	err := in.run(sub)
	in.depth--
	return err
}

// intIn returns v as an int when it is an integer in [lo, hi].
func intIn(v float64, lo, hi int) (int, bool) {
	if v != math.Trunc(v) || v < float64(lo) || v > float64(hi) {
		return 0, false
	}
	return int(v), true
}

// arith runs an arithmetic or storage operator (TN #5177 §4.4, §4.5). The
// operands are consumed and the result, which must be finite, pushed; div by
// zero, sqrt of a negative number and an index outside the stack or the
// transient array abort the glyph.
func (in *t2interp) arith(op int) error {
	b2 := func(t bool) float64 {
		if t {
			return 1
		}
		return 0
	}
	switch op {
	case t2random:
		return in.push(t2Random)
	case t2abs, t2neg, t2not, t2sqrt, t2drop, t2dup, t2get:
		a, err := in.pop(1)
		if err != nil {
			return err
		}
		v := a[0]
		switch op {
		case t2abs:
			return in.push(math.Abs(v))
		case t2neg:
			return in.push(-v)
		case t2not:
			return in.push(b2(v == 0))
		case t2sqrt:
			if v < 0 {
				return errMalformed
			}
			return in.push(math.Sqrt(v))
		case t2drop:
			return nil
		case t2dup:
			if err := in.push(v); err != nil {
				return err
			}
			return in.push(v)
		case t2get:
			i, ok := intIn(v, 0, t2TransientSize-1)
			if !ok {
				return errMalformed
			}
			return in.push(in.trans[i])
		}
	case t2index:
		a, err := in.pop(1)
		if err != nil {
			return err
		}
		i := a[0]
		if i < 0 {
			i = 0 // a negative index copies the top element
		}
		k, ok := intIn(i, 0, in.sp-1)
		if !ok {
			return errMalformed
		}
		return in.push(in.stack[in.sp-1-k])
	case t2roll:
		a, err := in.pop(2)
		if err != nil {
			return err
		}
		n, ok1 := intIn(a[0], 0, in.sp)
		j, ok2 := intIn(a[1], -maxT2Stack*maxT2Stack, maxT2Stack*maxT2Stack)
		if !ok1 || !ok2 {
			return errMalformed
		}
		if n > 0 {
			// Positive j moves elements toward the top: element k (from the
			// bottom of the n) goes to (k + j) mod n.
			var tmp [maxT2Stack]float64
			top := in.stack[in.sp-n : in.sp]
			for k, v := range top {
				tmp[((k+j)%n+n)%n] = v
			}
			copy(top, tmp[:n])
		}
		return nil
	case t2ifelse:
		a, err := in.pop(4)
		if err != nil {
			return err
		}
		if a[2] <= a[3] {
			return in.push(a[0])
		}
		return in.push(a[1])
	default: // binary operators
		a, err := in.pop(2)
		if err != nil {
			return err
		}
		x, y := a[0], a[1]
		switch op {
		case t2and:
			return in.push(b2(x != 0 && y != 0))
		case t2or:
			return in.push(b2(x != 0 || y != 0))
		case t2eq:
			return in.push(b2(x == y))
		case t2add:
			return in.push(x + y)
		case t2sub:
			return in.push(x - y)
		case t2mul:
			return in.push(float64(x * y)) // rounded: never fused into a later add
		case t2div:
			if y == 0 {
				return errMalformed
			}
			return in.push(x / y)
		case t2exch:
			if err := in.push(y); err != nil {
				return err
			}
			return in.push(x)
		case t2put:
			i, ok := intIn(y, 0, t2TransientSize-1)
			if !ok {
				return errMalformed
			}
			in.trans[i] = x
			return nil
		}
	}
	return errMalformed
}

// seac composes an accented character from endchar's four extra operands
// (TN #5177 Appendix C, the deprecated seac form): "adx ady bchar achar
// endchar". The base character (StandardEncoding code bchar) is drawn at
// the glyph's origin and the accent (code achar) with its origin moved to
// (adx, ady): a Type 2 charstring has no sidebearing point, so there is no
// asb. Components are looked up by StandardEncoding name through the
// charset, whatever the font's encoding; a CID-keyed font has no names, and
// a component that itself uses seac aborts the glyph. seac ends the
// charstring.
func (in *t2interp) seac(adx, ady, bchar, achar float64) error {
	if in.inSeac || in.c.cidKeyed {
		return errMalformed
	}
	base, ok1 := in.component(bchar)
	accent, ok2 := in.component(achar)
	if !ok1 || !ok2 {
		return errMalformed
	}
	in.inSeac = true
	for _, c := range []struct {
		cs     []byte
		dx, dy float64
	}{
		{base, 0, 0},
		{accent, adx, ady},
	} {
		in.dx, in.dy = c.dx, c.dy
		in.x, in.y, in.open, in.done, in.sp = 0, 0, false, false, 0
		in.haveWidth, in.stems = false, 0
		if err := in.run(c.cs); err != nil {
			return err
		}
	}
	in.done = true
	return nil
}

// component returns the charstring of the glyph StandardEncoding names at
// code v, through the charset.
func (in *t2interp) component(v float64) ([]byte, bool) {
	code, ok := intIn(v, 0, 255)
	if !ok {
		return nil, false
	}
	name := std14.StandardName(byte(code))
	if name == "" {
		return nil, false
	}
	gid, ok := in.f.byName[name]
	if !ok {
		return nil, false
	}
	return in.c.charstrings.get(int(gid))
}

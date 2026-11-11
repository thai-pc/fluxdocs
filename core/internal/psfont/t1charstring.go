package psfont

import (
	"encoding/binary"
	"math"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
)

// Type 1 charstring operators (Type 1 Font Format Ch. 6). Escaped operators
// (byte 12 followed by a second byte) are numbered 32 + the second byte.
const (
	t1hstem           = 1
	t1vstem           = 3
	t1vmoveto         = 4
	t1rlineto         = 5
	t1hlineto         = 6
	t1vlineto         = 7
	t1rrcurveto       = 8
	t1closepath       = 9
	t1callsubr        = 10
	t1return          = 11
	t1escape          = 12
	t1hsbw            = 13
	t1endchar         = 14
	t1rmoveto         = 21
	t1hmoveto         = 22
	t1vhcurveto       = 30
	t1hvcurveto       = 31
	t1dotsection      = 32 + 0
	t1vstem3          = 32 + 1
	t1hstem3          = 32 + 2
	t1seac            = 32 + 6
	t1sbw             = 32 + 7
	t1div             = 32 + 12
	t1callothersubr   = 32 + 16
	t1pop             = 32 + 17
	t1setcurrentpoint = 32 + 33
)

// t1interp is the state of one glyph build. It lives on the caller's stack,
// so a Font is safe for concurrent Outline calls.
type t1interp struct {
	f     *Font
	stack [maxStack]float64
	sp    int
	ps    [maxPSStack]float64 // PostScript stack: OtherSubr results for pop
	psp   int

	x, y float64 // current point, glyph space (before the component offset)
	dx   float64 // seac component offset added to every emitted point
	dy   float64
	sbx  float64 // the glyph's sidebearing point x (hsbw/sbw), for seac
	open bool    // a subpath has been started (MoveTo emitted)

	width    float64
	hasWidth bool
	segs     []Segment
	ops      int
	depth    int  // callsubr nesting
	inSeac   bool // building a seac component: seac is not allowed again
	done     bool // endchar reached
	flexing  bool
	flexN    int
	flexPts  [flexPoints]Point
	flexFrom Point // current point when the flex started
}

// runType1 builds glyph gid.
func (f *Font) runType1(gid GID) (Glyph, error) {
	in := &t1interp{f: f}
	err := in.run(f.charstrings[gid]) // a charstring without endchar ends at its last byte
	g := Glyph{Width: in.width, Ops: in.ops}
	if err != nil {
		return g, err
	}
	g.Segs = in.segs
	return g, nil
}

// push pushes v on the operand stack.
func (in *t1interp) push(v float64) error {
	if in.sp >= maxStack {
		return errLimit
	}
	in.stack[in.sp] = v
	in.sp++
	return nil
}

// args returns the top n operands, or errMalformed on underflow.
func (in *t1interp) args(n int) ([]float64, error) {
	if in.sp < n {
		return nil, errMalformed
	}
	return in.stack[in.sp-n : in.sp], nil
}

// run executes one charstring (the glyph's, a subroutine's or a seac
// component's) until endchar, return or its end.
func (in *t1interp) run(cs []byte) error {
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
		case v == 255:
			if i+4 > len(cs) {
				return errMalformed
			}
			if err := in.push(float64(int32(binary.BigEndian.Uint32(cs[i : i+4])))); err != nil {
				return err
			}
			i += 4
			continue
		}
		op := int(v)
		if op == t1escape {
			if i >= len(cs) {
				return errMalformed
			}
			op = 32 + int(cs[i])
			i++
		}
		if in.ops++; in.ops > maxGlyphOps {
			return errLimit
		}
		if op == t1return {
			return nil
		}
		if err := in.exec(op); err != nil {
			return err
		}
	}
	return nil
}

// exec runs one operator. Operators take their operands from the top of the
// stack and, except callsubr, div, callothersubr and pop, clear it.
func (in *t1interp) exec(op int) error {
	switch op {
	case t1callsubr:
		a, err := in.args(1)
		if err != nil {
			return err
		}
		in.sp--
		return in.callSubr(a[0])
	case t1div:
		a, err := in.args(2)
		if err != nil {
			return err
		}
		if a[1] == 0 {
			return errMalformed
		}
		q := a[0] / a[1]
		in.sp -= 2
		return in.push(q)
	case t1callothersubr:
		return in.callOtherSubr()
	case t1pop:
		if in.psp == 0 {
			return errMalformed
		}
		in.psp--
		return in.push(in.ps[in.psp])
	}

	var err error
	switch op {
	case t1hsbw:
		err = in.sidebearing(2, func(a []float64) (sbx, sby, wx float64) { return a[0], 0, a[1] })
	case t1sbw:
		err = in.sidebearing(4, func(a []float64) (sbx, sby, wx float64) { return a[0], a[1], a[2] })
	case t1rmoveto:
		err = in.moveRel(2, func(a []float64) (float64, float64) { return a[0], a[1] })
	case t1hmoveto:
		err = in.moveRel(1, func(a []float64) (float64, float64) { return a[0], 0 })
	case t1vmoveto:
		err = in.moveRel(1, func(a []float64) (float64, float64) { return 0, a[0] })
	case t1rlineto:
		err = in.lineRel(2, func(a []float64) (float64, float64) { return a[0], a[1] })
	case t1hlineto:
		err = in.lineRel(1, func(a []float64) (float64, float64) { return a[0], 0 })
	case t1vlineto:
		err = in.lineRel(1, func(a []float64) (float64, float64) { return 0, a[0] })
	case t1rrcurveto:
		err = in.curveRel(6, func(a []float64) [6]float64 { return [6]float64{a[0], a[1], a[2], a[3], a[4], a[5]} })
	case t1vhcurveto:
		err = in.curveRel(4, func(a []float64) [6]float64 { return [6]float64{0, a[0], a[1], a[2], a[3], 0} })
	case t1hvcurveto:
		err = in.curveRel(4, func(a []float64) [6]float64 { return [6]float64{a[0], 0, a[1], a[2], 0, a[3]} })
	case t1closepath:
		// Unlike PostScript closepath, the current point stays where it is
		// (Ch. 6); the next drawing operator after a moveto starts a new
		// subpath, and every subpath is closed for filling.
		in.open = false
	case t1endchar:
		in.done = true
	case t1setcurrentpoint:
		var a []float64
		if a, err = in.args(2); err == nil {
			in.x, in.y = a[0], a[1]
			err = in.checkPoint()
		}
	case t1seac:
		err = in.seac()
	case t1hstem, t1vstem, t1hstem3, t1vstem3, t1dotsection:
		// Hints are ignored (anti-aliased outline rendering).
	default:
		// Reserved operators: ignored with their operands.
	}
	in.sp = 0
	return err
}

// sidebearing runs hsbw or sbw: the sidebearing point becomes the current
// point and the width, which must lie within ±maxCoord, is recorded. In a seac component only the point is set:
// the composite's own hsbw gave the width.
func (in *t1interp) sidebearing(n int, pick func([]float64) (sbx, sby, wx float64)) error {
	a, err := in.args(n)
	if err != nil {
		return err
	}
	sbx, sby, wx := pick(a)
	if !finiteCoord(wx) {
		return errLimit // a div chain can make the width ±Inf or NaN
	}
	in.x, in.y = sbx, sby
	if !in.inSeac {
		in.sbx = sbx
		in.width, in.hasWidth = wx, true
	}
	return in.checkPoint()
}

// moveRel runs a moveto. During a flex the point is collected instead
// (OtherSubrs 1 and 2, Ch. 8).
func (in *t1interp) moveRel(n int, pick func([]float64) (float64, float64)) error {
	a, err := in.args(n)
	if err != nil {
		return err
	}
	ddx, ddy := pick(a)
	in.x += ddx
	in.y += ddy
	if err := in.checkPoint(); err != nil {
		return err
	}
	if in.flexing {
		if in.flexN < flexPoints {
			in.flexPts[in.flexN] = Point{in.x, in.y}
		}
		in.flexN++
		return nil
	}
	in.open = false // the next drawing operator starts the subpath here
	return nil
}

// lineRel runs a lineto.
func (in *t1interp) lineRel(n int, pick func([]float64) (float64, float64)) error {
	a, err := in.args(n)
	if err != nil {
		return err
	}
	if err := in.startSubpath(); err != nil {
		return err
	}
	ddx, ddy := pick(a)
	in.x += ddx
	in.y += ddy
	if err := in.checkPoint(); err != nil {
		return err
	}
	return in.emit(Segment{Op: LineTo, P: [3]Point{in.out(in.x, in.y)}})
}

// curveRel runs a curveto given its six relative coordinates.
func (in *t1interp) curveRel(n int, pick func([]float64) [6]float64) error {
	a, err := in.args(n)
	if err != nil {
		return err
	}
	d := pick(a)
	if err := in.startSubpath(); err != nil {
		return err
	}
	x1, y1 := in.x+d[0], in.y+d[1]
	x2, y2 := x1+d[2], y1+d[3]
	in.x, in.y = x2+d[4], y2+d[5]
	if !finiteCoord(x1) || !finiteCoord(y1) || !finiteCoord(x2) || !finiteCoord(y2) {
		return errLimit
	}
	if err := in.checkPoint(); err != nil {
		return err
	}
	return in.emit(Segment{Op: CubeTo, P: [3]Point{in.out(x1, y1), in.out(x2, y2), in.out(in.x, in.y)}})
}

// startSubpath emits the MoveTo of a new subpath at the current point when a
// drawing operator follows a moveto, a closepath or the glyph start.
func (in *t1interp) startSubpath() error {
	if in.open {
		return nil
	}
	in.open = true
	return in.emit(Segment{Op: MoveTo, P: [3]Point{in.out(in.x, in.y)}})
}

// out applies the seac component offset.
func (in *t1interp) out(x, y float64) Point { return Point{x + in.dx, y + in.dy} }

// emit appends a segment within maxGlyphSegments. Every emitted point, seac
// component offset included, must lie within ±maxCoord.
func (in *t1interp) emit(s Segment) error {
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
func (in *t1interp) checkPoint() error {
	if !finiteCoord(in.x) || !finiteCoord(in.y) {
		return errLimit
	}
	return nil
}

func finiteCoord(v float64) bool { return !math.IsNaN(v) && v >= -maxCoord && v <= maxCoord }

// callSubr runs subroutine v within maxSubrDepth.
func (in *t1interp) callSubr(v float64) error {
	if in.depth >= maxSubrDepth {
		return errLimit
	}
	if v < 0 || v >= float64(len(in.f.subrs)) || v != math.Trunc(v) {
		return errMalformed
	}
	sub := in.f.subrs[int(v)]
	if sub == nil {
		return errMalformed
	}
	in.depth++
	err := in.run(sub)
	in.depth--
	return err
}

// callOtherSubr runs "arg1 … argn n othersubr# callothersubr" for rendering
// (Ch. 8): 1 starts a flex, 2 marks a flex point (the preceding rmoveto
// collected it), 0 ends a flex and draws it as two curves, 3 (hint
// replacement) returns its argument, and 12 and 13 (counter control, TN
// #5015) return nothing. Any other OtherSubr returns its arguments so the
// pops that follow read them in order. Results not popped before the next
// callothersubr are discarded: each call starts with an empty PostScript
// stack, so a glyph's unpopped calls cannot pile up into maxPSStack.
func (in *t1interp) callOtherSubr() error {
	in.psp = 0
	a, err := in.args(2)
	if err != nil {
		return err
	}
	num, n := a[1], a[0]
	in.sp -= 2
	if n < 0 || n > float64(in.sp) || n != math.Trunc(n) {
		return errMalformed
	}
	cnt := int(n)
	args := append([]float64(nil), in.stack[in.sp-cnt:in.sp]...)
	in.sp -= cnt
	switch {
	case num == 1:
		in.flexing, in.flexN, in.flexFrom = true, 0, Point{in.x, in.y}
		return nil
	case num == 2:
		return nil
	case num == 0 && cnt == 3:
		return in.endFlex(args[1], args[2])
	case num == 0:
		in.flexing = false // a malformed flex end still ends the flex
	case num == 12 || num == 13:
		return nil
	}
	// 3, a malformed 0 and unknown OtherSubrs: the results are the
	// arguments, the first one on top of the PostScript stack.
	if in.psp+cnt > maxPSStack {
		return errLimit
	}
	for i := cnt - 1; i >= 0; i-- {
		in.ps[in.psp] = args[i]
		in.psp++
	}
	return nil
}

// endFlex ends a flex (OtherSubr 0): with exactly flexPoints collected, it
// draws the two curves from the point the flex started at through collected
// points 1–3 and 4–6 (point 0 is the reference point, used only for hinting).
// Any other count discards the flex. It leaves x and y for the "pop pop
// setcurrentpoint" that follows, x on top.
func (in *t1interp) endFlex(x, y float64) error {
	in.flexing = false
	if in.psp+2 > maxPSStack {
		return errLimit
	}
	in.ps[in.psp] = y
	in.ps[in.psp+1] = x
	in.psp += 2
	if in.flexN != flexPoints {
		return nil
	}
	p := in.flexPts
	in.x, in.y = in.flexFrom.X, in.flexFrom.Y
	if err := in.startSubpath(); err != nil {
		return err
	}
	for _, c := range [2][3]Point{{p[1], p[2], p[3]}, {p[4], p[5], p[6]}} {
		if err := in.emit(Segment{Op: CubeTo, P: [3]Point{in.out(c[0].X, c[0].Y), in.out(c[1].X, c[1].Y), in.out(c[2].X, c[2].Y)}}); err != nil {
			return err
		}
	}
	in.x, in.y = p[6].X, p[6].Y
	return nil
}

// seac composes a StandardEncoding accented character (Ch. 6): "asb adx ady
// bchar achar seac". The base character (StandardEncoding code bchar) is
// drawn at the glyph's origin; the accent (code achar) is placed so that its
// sidebearing point, asb from its origin, lands at (sbx + adx, ady), where sbx
// is the sidebearing of the composite's own hsbw: its origin goes to
// (sbx + adx − asb, ady). Components are looked up by StandardEncoding name,
// whatever the font's encoding; a component that itself calls seac aborts the
// glyph. seac ends the charstring.
func (in *t1interp) seac() error {
	if in.inSeac {
		return errMalformed
	}
	a, err := in.args(5)
	if err != nil {
		return err
	}
	asb, adx, ady, bchar, achar := a[0], a[1], a[2], a[3], a[4]
	base, ok1 := in.component(bchar)
	accent, ok2 := in.component(achar)
	if !ok1 || !ok2 {
		return errMalformed
	}
	in.sp = 0
	in.inSeac = true
	sbx := in.sbx
	for _, c := range []struct {
		cs     []byte
		dx, dy float64
	}{
		{base, 0, 0},
		{accent, float64(sbx+adx) - asb, ady},
	} {
		in.dx, in.dy = c.dx, c.dy
		in.x, in.y, in.open, in.done, in.sp, in.psp = 0, 0, false, false, 0, 0
		in.flexing = false
		if err := in.run(c.cs); err != nil {
			return err
		}
	}
	in.done = true
	return nil
}

// component returns the charstring of the glyph StandardEncoding names at
// code v.
func (in *t1interp) component(v float64) ([]byte, bool) {
	if v < 0 || v > 255 || v != math.Trunc(v) {
		return nil, false
	}
	name := std14.StandardName(byte(v))
	if name == "" {
		return nil, false
	}
	gid, ok := in.f.byName[name]
	if !ok || int64(gid) >= int64(len(in.f.charstrings)) {
		return nil, false
	}
	return in.f.charstrings[gid], true
}

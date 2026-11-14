// Package textflow is text extraction's reading of a content stream (ISO
// 32000-1 §9.4, §9.10): the shown text in content order, decoded to Unicode
// through each font's model (core/internal/fontmodel), with a newline where
// the text line matrix moves to a new line and a space where a TJ adjustment
// reads as a word gap. It is the one state machine behind
// core/extract.PageText, kept here so redaction can read a page exactly as
// ExtractText does (docs/design/redaction-cross-operator.md §3.2, X3).
//
// The reading mirrors extraction including its documented non-conformances:
// the CTM is ignored, q/Q do not restore the font, size or leading, gs /Font
// is ignored, Tc/Tw/Tz/Ts have no effect, and forms are not entered. Changing
// any of these is a Core extraction change and changes every user at once.
//
// Each byte written is attributed to an owner given by the caller per
// operator (Apply, Owned), so a match in the text can be mapped back to the
// operators that showed it. Line-break newlines are separators and owned by
// no operator (NoOwner), including those the quote operators ' and " write.
//
// A Flow can also record the steps of its reading (Record, Events), so the
// reading of the same content with some operators' text left out — what
// extraction reads once those operators are removed — is computed without
// reading the content again (Replay).
package textflow

import (
	"math"
	"strings"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// SpaceThreshold is the minimum negative TJ adjustment, in thousandths of a
// text space unit (§9.4.3), that reads as an inter-word gap and is written as
// a space. Kerning adjustments are typically smaller in magnitude; word gaps
// are larger.
const SpaceThreshold = 100.0

// NoOwner is the owner of bytes no operator is credited with: line-break
// newlines, and every byte when the caller does not attribute operators.
const NoOwner int32 = -1

// Fonts resolves the font operand of Tf. A nil result (an unknown or
// unloadable font) decodes shown strings as Latin-1 (fontmodel.Font.Text).
type Fonts func(name parse.Name) *fontmodel.Font

// Piece is a maximal range of the output written for one owner. Pieces are
// contiguous and in output order: piece i covers [Start, next piece's Start),
// the last one up to the end of String().
type Piece struct {
	Start int
	Owner int32
}

// EventKind is the kind of a recorded reading step (Event).
type EventKind uint8

// The recorded reading steps.
const (
	// EventLine is a line break: a newline is written if any glyph was
	// written before it (by any operator), else nothing.
	EventLine EventKind = iota
	// EventShow starts a text-showing operator (Tj, ', ", TJ) of Owner,
	// whether or not it writes anything; for ' and " it follows the line
	// break the operator makes.
	EventShow
	// EventText is shown text of Owner: String()[Start:End] of the recording
	// Flow (never empty).
	EventText
	// EventGap is a TJ word gap of Owner: a space unless the output is empty
	// or already ends in a space, newline or tab.
	EventGap
)

// Event is one recorded reading step. Start and End are set for EventText
// only.
type Event struct {
	Kind       EventKind
	Owner      int32
	Start, End int32
}

// Flow is the running reading of one content stream. The zero value is not
// usable; create one with New. A Flow is not safe for concurrent use.
type Flow struct {
	sink
	fonts Fonts

	// record, when set, appends each reading step to events (Record).
	record   bool
	events   []Event
	overflow bool // the output passed what an Event offset can hold

	cur      *fontmodel.Font
	fontSize float64
	leading  float64 // set by TL and by TD

	// tlm is the text line matrix [a b c d e f]; only its translation (e, f)
	// is used for the line heuristic.
	tlm [6]float64

	haveLine  bool    // a line position has been established
	lastLineY float64 // baseline Y of the current line
}

// sink is the output of a reading: the text, its pieces and owners, and
// whether any glyph has been written. Flow and Replay write through the same
// rules, so a replay of everything equals the reading it was recorded from.
type sink struct {
	out     strings.Builder
	pieces  []Piece
	emitted bool // any glyph written yet
}

// write appends s to the output, attributed to owner.
func (s *sink) write(t string, owner int32) {
	if n := len(s.pieces); n == 0 || s.pieces[n-1].Owner != owner {
		s.pieces = append(s.pieces, Piece{Start: s.out.Len(), Owner: owner})
	}
	s.out.WriteString(t)
}

// text writes shown text (nothing for an empty one) and records that a glyph
// was written.
func (s *sink) text(t string, owner int32) {
	if t == "" {
		return
	}
	s.write(t, owner)
	s.emitted = true
}

// line writes the newline of a line break once a glyph has been written.
func (s *sink) line() {
	if s.emitted {
		s.write("\n", NoOwner)
	}
}

// gap writes a TJ word gap's space unless the output is empty or already ends
// with white space. The decision depends on the output before the operator,
// so a leading gap's space is owned by the operator that wrote it.
func (s *sink) gap(owner int32) {
	buf := s.out.String()
	if buf == "" {
		return
	}
	switch buf[len(buf)-1] {
	case ' ', '\n', '\t':
		return
	}
	s.write(" ", owner)
}

// New returns an empty Flow that resolves Tf font names through fonts (nil
// resolves every name to the Latin-1 fallback).
func New(fonts Fonts) *Flow {
	return &Flow{fonts: fonts}
}

// String returns the text read so far.
func (f *Flow) String() string { return f.out.String() }

// Record makes the Flow record its reading steps (Events). Call it before the
// first Apply.
func (f *Flow) Record() { f.record = true }

// Events returns the recorded reading steps in order, and false — with no
// events — when the Flow did not record or its output grew past what an
// Event offset holds (2 GiB). The slice aliases the Flow's state: it is valid
// until the next Apply and must not be modified.
func (f *Flow) Events() ([]Event, bool) {
	if !f.record || f.overflow {
		return nil, false
	}
	return f.events, true
}

// event records a reading step. A gap right after a gap of the same owner is
// not recorded: once the first is written the output is empty or ends in
// white space, in every replay that keeps them (they are kept together), so
// the second writes nothing. A gap then follows another kind of step (an
// owner changes only at a show step), so the steps are at most twice the
// others: at most two per operator (a quote's line break and show) and one
// per byte of shown text.
func (f *Flow) event(kind EventKind, owner int32, start, end int) {
	if !f.record || f.overflow {
		return
	}
	if n := len(f.events); kind == EventGap && n > 0 && f.events[n-1].Kind == EventGap && f.events[n-1].Owner == owner {
		return
	}
	if end > math.MaxInt32 {
		f.overflow, f.events = true, nil
		return
	}
	f.events = append(f.events, Event{Kind: kind, Owner: owner, Start: int32(start), End: int32(end)})
}

// Owned returns the pieces of String() and their owners, in output order (nil
// when nothing was written). The slice aliases the Flow's state: it is valid
// until the next Apply and must not be modified.
func (f *Flow) Owned() []Piece { return f.pieces }

// Font returns the font the last Tf selected, as extraction binds it (nil:
// none, or one that decodes as Latin-1).
func (f *Flow) Font() *fontmodel.Font { return f.cur }

// Apply interprets one content operator with its operands (in content order,
// as parse.ScanContent yields them). Shown text and TJ-gap spaces it writes
// are attributed to owner; a newline it writes for a line move is attributed
// to NoOwner. Operators that do not concern text, and operands of the wrong
// type or count, are ignored.
func (f *Flow) Apply(op string, operands []parse.Object, owner int32) {
	switch op {
	case "BT":
		// A text object resets the text/line matrix to identity (§9.4.1). The
		// last line position is kept, so a move in the new object is still
		// compared against the previous one for the line-break decision.
		f.tlm = identity()
	case "ET":
		// nothing to flush; text is written eagerly

	case "Tf":
		// /Font size Tf
		if len(operands) >= 2 {
			if name, ok := operands[len(operands)-2].(parse.Name); ok {
				f.cur = nil
				if f.fonts != nil {
					f.cur = f.fonts(name)
				}
			}
			f.fontSize = asFloat(operands[len(operands)-1])
		}
	case "TL":
		if len(operands) >= 1 {
			f.leading = asFloat(operands[len(operands)-1])
		}

	case "Td":
		if len(operands) >= 2 {
			f.moveLine(asFloat(operands[len(operands)-2]), asFloat(operands[len(operands)-1]))
		}
	case "TD":
		if len(operands) >= 2 {
			ty := asFloat(operands[len(operands)-1])
			f.leading = -ty
			f.moveLine(asFloat(operands[len(operands)-2]), ty)
		}
	case "Tm":
		// a b c d e f Tm — set the line matrix absolutely.
		if len(operands) >= 6 {
			var m [6]float64
			for i := 0; i < 6; i++ {
				m[i] = asFloat(operands[len(operands)-6+i])
			}
			f.setLine(m)
		}
	case "T*":
		f.moveLine(0, -f.leading)

	case "Tj":
		f.event(EventShow, owner, 0, 0)
		f.show(lastString(operands), owner)
	case "'":
		// Move to next line, then show.
		f.moveLine(0, -f.leading)
		f.event(EventShow, owner, 0, 0)
		f.show(lastString(operands), owner)
	case "\"":
		// aw ac string " — set word/char spacing (ignored), next line, show.
		f.moveLine(0, -f.leading)
		f.event(EventShow, owner, 0, 0)
		f.show(lastString(operands), owner)
	case "TJ":
		f.event(EventShow, owner, 0, 0)
		f.showArray(operands, owner)
	}
}

// moveLine applies a relative Td-style move to the text line matrix and
// updates the line heuristic.
func (f *Flow) moveLine(tx, ty float64) {
	f.tlm = mul(translate(tx, ty), f.tlm)
	f.setLine(f.tlm)
}

// setLine sets the text line matrix absolutely and writes a newline when the
// baseline moves vertically far enough to be a new line rather than a
// sub/superscript shift.
//
// Inter-word spacing on the same line is deliberately NOT inferred from the
// horizontal position here: without glyph-width metrics, a document that
// positions every glyph with its own Tm/Td is indistinguishable from one that
// positions words, so a gap-based rule would split every glyph. Instead the
// reading relies on explicit space characters in the shown strings and on TJ
// adjustments (showArray) — which is what Word, Chrome, LaTeX, and Adobe
// emit. Recovering spacing for glyph-positioned PDFs needs font widths.
func (f *Flow) setLine(m [6]float64) {
	y := m[5]
	// Whether the move is a line break does not depend on what was written,
	// only whether a newline is written for it does (sink.line).
	if f.haveLine {
		if dy := f.lastLineY - y; abs(dy) > f.newlineThreshold() {
			f.event(EventLine, NoOwner, 0, 0)
			f.line()
		}
	}
	f.tlm = m
	f.lastLineY = y
	f.haveLine = true
}

// newlineThreshold is the vertical movement above which a repositioning is
// treated as a new line rather than a sub/superscript shift.
func (f *Flow) newlineThreshold() float64 {
	if f.fontSize > 0 {
		return 0.5 * f.fontSize
	}
	return 2.0
}

// show decodes and writes a single shown string.
func (f *Flow) show(raw []byte, owner int32) {
	if raw == nil {
		return
	}
	text := f.cur.Text(raw)
	if text == "" {
		return
	}
	start := f.out.Len()
	f.text(text, owner)
	f.event(EventText, owner, start, f.out.Len())
}

// showArray handles TJ: an array of strings interleaved with numeric position
// adjustments. A sufficiently large negative adjustment is an inter-word gap.
func (f *Flow) showArray(operands []parse.Object, owner int32) {
	arr, ok := lastArray(operands)
	if !ok {
		return
	}
	for _, el := range arr {
		switch v := el.(type) {
		case parse.String:
			f.show(v.Value, owner)
		case parse.Integer:
			if float64(v) <= -SpaceThreshold {
				f.wordGap(owner)
			}
		case parse.Real:
			if float64(v) <= -SpaceThreshold {
				f.wordGap(owner)
			}
		}
	}
}

// wordGap writes a TJ word gap (sink.gap) and records it.
func (f *Flow) wordGap(owner int32) {
	f.event(EventGap, owner, 0, 0)
	f.gap(owner)
}

// Replay returns the reading recorded in events (from a Flow whose String()
// is text) with the operators of every owner keep rejects left out: what Flow
// reads from the same content once those text-showing operators are removed
// and their positioning kept (a ' or " replaced by its line move). Line
// breaks and word gaps are decided again against what is left: a newline is
// written only after the first kept glyph, and a gap's space only when the
// kept output does not already end in white space. keep is never called with
// NoOwner, whose steps are always kept.
//
// When shown is set, each kept operator writes shown(owner) instead of its
// recorded text, and word gaps write nothing: the same line structure read
// with another decoding of each operator (a glyph is written when shown
// returns a non-empty text). shown is never called with NoOwner either: an
// operator no owner is credited with writes nothing in that mode.
//
// Returns the text and its pieces (as Owned).
func Replay(text string, events []Event, keep func(owner int32) bool, shown func(owner int32) string) (string, []Piece) {
	var s sink
	kept := func(owner int32) bool { return owner == NoOwner || keep == nil || keep(owner) }
	for _, e := range events {
		switch e.Kind {
		case EventLine:
			s.line()
		case EventShow:
			if shown != nil && e.Owner != NoOwner && kept(e.Owner) {
				s.text(shown(e.Owner), e.Owner)
			}
		case EventText:
			if shown == nil && kept(e.Owner) {
				s.text(text[e.Start:e.End], e.Owner)
			}
		case EventGap:
			if shown == nil && kept(e.Owner) {
				s.gap(e.Owner)
			}
		}
	}
	return s.out.String(), s.pieces
}

// --- operand helpers ---

// lastString returns the bytes of the last String operand, or nil.
func lastString(operands []parse.Object) []byte {
	for i := len(operands) - 1; i >= 0; i-- {
		if s, ok := operands[i].(parse.String); ok {
			return s.Value
		}
	}
	return nil
}

// lastArray returns the last Array operand.
func lastArray(operands []parse.Object) (parse.Array, bool) {
	for i := len(operands) - 1; i >= 0; i-- {
		if a, ok := operands[i].(parse.Array); ok {
			return a, true
		}
	}
	return nil, false
}

// asFloat converts an Integer or Real operand to float64 (0 otherwise).
func asFloat(o parse.Object) float64 {
	switch v := o.(type) {
	case parse.Integer:
		return float64(v)
	case parse.Real:
		return float64(v)
	default:
		return 0
	}
}

// --- 2x3 affine matrix helpers ([a b c d e f] = [[a b 0],[c d 0],[e f 1]]) ---

func identity() [6]float64 { return [6]float64{1, 0, 0, 1, 0, 0} }

func translate(tx, ty float64) [6]float64 { return [6]float64{1, 0, 0, 1, tx, ty} }

// mul returns m * n.
func mul(m, n [6]float64) [6]float64 {
	return [6]float64{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

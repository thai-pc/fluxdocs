package textflow

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// record runs ops on a recording Flow.
func record(ops []op) *Flow {
	f := New(nil)
	f.Record()
	for _, o := range ops {
		f.Apply(o.name, o.operands, o.owner)
	}
	return f
}

// apply reads ops with every operator of an owner in drop removed the way
// redaction removes it: a Tj or TJ disappears, a ' or " keeps its line move
// (T*).
func applyRemoved(ops []op, drop map[int32]bool) *Flow {
	f := New(nil)
	for _, o := range ops {
		if o.owner != NoOwner && drop[o.owner] {
			switch o.name {
			case "'", "\"":
				f.Apply("T*", nil, NoOwner)
			}
			continue
		}
		f.Apply(o.name, o.operands, o.owner)
	}
	return f
}

// replayCases are content streams for the replay properties: every rule the
// reading has, with several operators per line.
func replayCases() map[string][]op {
	bt, et := op{"BT", nil, NoOwner}, op{"ET", nil, NoOwner}
	tl := op{"TL", nums(14), NoOwner}
	return map[string][]op{
		"same line": {bt, tf(12), td(72, 720), tj("SEC", 0), tj("RET-", 1), tj("1234", 2), et},
		"line breaks": {bt, tf(12), tl, td(72, 720), tj("a", 0), {"T*", nil, NoOwner}, tj("b", 1),
			td(0, -20), td(0, -20), tj("c", 2), {"'", []parse.Object{str("d")}, 3},
			{"\"", []parse.Object{parse.Integer(1), parse.Integer(2), str("e")}, 4}, et},
		"first glyph after moves": {bt, tf(12), td(72, 720), tj("x", 0), td(0, -20), tj("y", 1), td(0, -20), tj("z", 2), et},
		"gaps": {bt, tf(12), tjArr(0, str("a"), parse.Integer(-300)), tjArr(1, str("b")),
			tjArr(2, parse.Integer(-300), str("c"), parse.Integer(-300)), tjArr(3, parse.Integer(-300), str("d")), et},
		"gap after white space": {bt, tf(12), tj("a ", 0), tjArr(1, parse.Integer(-500), str("b")),
			tjArr(2, str("c\t")), tjArr(3, parse.Integer(-500), str("d")), et},
		"consecutive gaps": {bt, tf(12), tjArr(0, parse.Integer(-300), parse.Integer(-300), str("a"), parse.Integer(-300),
			parse.Real(-400), parse.Integer(-500), str("b"), parse.Integer(-300), parse.Integer(-300)),
			tjArr(1, parse.Integer(-300), parse.Integer(-300)), tjArr(2, str("c"), parse.Integer(-300)), tj("d", 3), et},
		"empty operators": {bt, tf(12), tj("", 0), tj("a", 1), tjArr(2), tjArr(3, parse.Integer(-400)), tj("b", 4), et},
		"two text objects": {bt, tf(24), td(72, 700), tj("SECRET-", 0), et, bt, tf(24), td(177.4, 700), tj("1234", 1), et,
			bt, tf(24), td(72, 650), tj("next", 2), et},
	}
}

// TestReplay_AllEqualsFlow: replaying everything is the recorded reading,
// text and pieces.
func TestReplay_AllEqualsFlow(t *testing.T) {
	for name, ops := range replayCases() {
		t.Run(name, func(t *testing.T) {
			f := record(ops)
			ev, ok := f.Events()
			if !ok {
				t.Fatal("no events")
			}
			text, pieces := Replay(f.String(), ev, nil, nil)
			if text != f.String() || !reflect.DeepEqual(pieces, f.Owned()) {
				t.Errorf("Replay = %q %v, want %q %v", text, pieces, f.String(), f.Owned())
			}
		})
	}
}

// TestReplay_RemovedEqualsReread: replaying with owners left out equals
// reading the content with those operators removed, for every subset of the
// owners (P3 of docs/design/redaction-cross-operator.md at the textflow
// level).
func TestReplay_RemovedEqualsReread(t *testing.T) {
	for name, ops := range replayCases() {
		owners := map[int32]bool{}
		for _, o := range ops {
			if o.owner != NoOwner {
				owners[o.owner] = true
			}
		}
		n := len(owners)
		f := record(ops)
		ev, _ := f.Events()
		for mask := 0; mask < 1<<n; mask++ {
			drop := map[int32]bool{}
			for i := 0; i < n; i++ {
				if mask&(1<<i) != 0 {
					drop[int32(i)] = true
				}
			}
			want := applyRemoved(ops, drop)
			text, pieces := Replay(f.String(), ev, func(o int32) bool { return !drop[o] }, nil)
			if text != want.String() || !reflect.DeepEqual(pieces, want.Owned()) {
				t.Errorf("%s drop %v: Replay = %q %v, want %q %v", name, drop, text, pieces, want.String(), want.Owned())
			}
		}
	}
}

func TestReplay_Cases(t *testing.T) {
	bt := op{"BT", nil, NoOwner}
	tests := []struct {
		name string
		ops  []op
		drop []int32
		want string
	}{
		{"first run dropped: no leading newline", []op{bt, tf(12), td(72, 720), tj("a", 0), td(0, -20), tj("b", 1)}, []int32{0}, "b"},
		{"run between two gaps dropped: one space", []op{bt, tf(12), tjArr(0, str("a"), parse.Integer(-300)),
			tjArr(1, str("b"), parse.Integer(-300)), tjArr(2, parse.Integer(-300), str("c"))}, []int32{1}, "a c"},
		{"neighbours joined", []op{bt, tf(12), tj("SECRET-", 0), tj("X", 1), tj("1234", 2)}, []int32{1}, "SECRET-1234"},
		{"newline kept between kept runs", []op{bt, tf(12), td(72, 720), tj("a", 0), td(0, -20), tj("x", 1), td(0, -20), tj("b", 2)}, []int32{1}, "a\n\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := record(tt.ops)
			ev, _ := f.Events()
			drop := map[int32]bool{}
			for _, d := range tt.drop {
				drop[d] = true
			}
			if got, _ := Replay(f.String(), ev, func(o int32) bool { return !drop[o] }, nil); got != tt.want {
				t.Errorf("Replay = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReplay_Shown: another decoding per operator keeps the line structure
// and writes no gap spaces.
func TestReplay_Shown(t *testing.T) {
	bt := op{"BT", nil, NoOwner}
	ops := []op{bt, tf(12), td(72, 720), tj("", 0), td(0, -20), tjArr(1, str("ab"), parse.Integer(-300), str("c")),
		td(0, -20), tj("d", 2)}
	f := record(ops)
	ev, _ := f.Events()
	shown := map[int32]string{0: "", 1: "AB-C", 2: "D"}
	text, pieces := Replay(f.String(), ev, nil, func(o int32) string { return shown[o] })
	if want := "AB-C\nD"; text != want {
		t.Errorf("Replay shown = %q, want %q (no newline before the first glyph)", text, want)
	}
	if want := []Piece{{0, 1}, {4, NoOwner}, {5, 2}}; !reflect.DeepEqual(pieces, want) {
		t.Errorf("pieces = %v, want %v", pieces, want)
	}
	text, _ = Replay(f.String(), ev, func(o int32) bool { return o != 1 }, func(o int32) string { return shown[o] })
	if text != "D" {
		t.Errorf("Replay shown without 1 = %q, want %q", text, "D")
	}
}

// TestReplay_ShownUnowned: in shown mode an operator no owner is credited
// with writes nothing, and shown is never called with NoOwner.
func TestReplay_ShownUnowned(t *testing.T) {
	bt := op{"BT", nil, NoOwner}
	f := record([]op{bt, tf(12), td(72, 720), tj("a", NoOwner), tj("b", 0)})
	ev, _ := f.Events()
	text, pieces := Replay(f.String(), ev, nil, func(o int32) string {
		if o == NoOwner {
			t.Fatal("shown called with NoOwner")
		}
		return "B"
	})
	if want := []Piece{{0, 0}}; text != "B" || !reflect.DeepEqual(pieces, want) {
		t.Errorf("Replay shown = %q %v, want %q %v", text, pieces, "B", want)
	}
}

// TestFlow_GapsRecordedOnce: a run of gaps of one operator is one step.
func TestFlow_GapsRecordedOnce(t *testing.T) {
	arr := []parse.Object{str("a")}
	for i := 0; i < 1000; i++ {
		arr = append(arr, parse.Integer(-300))
	}
	f := record([]op{{"BT", nil, NoOwner}, tf(12), tjArr(0, arr...), tjArr(1, parse.Integer(-300), str("b"))})
	ev, _ := f.Events()
	if len(ev) != 6 { // show, text, gap; show, gap, text
		t.Errorf("%d steps, want 6: %v", len(ev), ev)
	}
}

func TestFlow_EventsNotRecorded(t *testing.T) {
	f := run([]op{tf(12), tj("a", 0)})
	if ev, ok := f.Events(); ok || ev != nil {
		t.Errorf("Events() = %v, %v for a Flow that does not record", ev, ok)
	}
}

func TestFlow_RecordingDoesNotChangeTheReading(t *testing.T) {
	for name, ops := range replayCases() {
		a, b := run(ops), record(ops)
		if a.String() != b.String() || !reflect.DeepEqual(a.Owned(), b.Owned()) {
			t.Errorf("%s: recording changed the reading: %q vs %q", name, b.String(), a.String())
		}
	}
}

func TestPageFonts(t *testing.T) {
	objs := []string{"<< /F1 2 0 R /F2 99 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	var offs []int
	for i, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	r, err := parse.NewResolver([]byte(b.String()))
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	var loads []parse.Object
	load := func(obj parse.Object) *fontmodel.Font {
		loads = append(loads, obj)
		return fontmodel.Load(r, obj, fontmodel.Options{})
	}
	fonts := PageFonts(r, parse.Dict{"Font": parse.Reference{Number: 1}}, load)
	if fonts == nil {
		t.Fatal("PageFonts = nil for a page with fonts")
	}
	if fonts("F1") == nil {
		t.Error("F1 = nil, want Helvetica")
	}
	fonts("F1")
	if len(loads) != 1 {
		t.Errorf("F1 loaded %d times, want once", len(loads))
	}
	if fonts("F2") != nil || fonts("F3") != nil {
		t.Error("an unreadable or missing name must decode as Latin-1 (nil)")
	}
	if PageFonts(r, nil, load) != nil || PageFonts(r, parse.Dict{"Font": parse.Integer(1)}, load) != nil {
		t.Error("no /Font dictionary: want nil Fonts")
	}
}

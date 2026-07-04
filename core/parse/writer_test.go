package parse

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// --- Serializer unit tests -------------------------------------------------

func TestFormatReal(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1.5, "1.5"},
		{2.0, "2"},
		{0.5, "0.5"},
		{-3.14, "-3.14"},
		{1000000, "1000000"},   // large integral value stays plain, no exponent
		{0.000123, "0.000123"}, // small fractional value, still fixed-point
		{0, "0"},
		{math.Copysign(0, -1), "0"}, // negative zero collapses to "0"
		{math.NaN(), "0"},           // non-finite must degrade, never panic
		{math.Inf(1), "0"},
		{math.Inf(-1), "0"},
	}
	for _, tt := range tests {
		if got := formatReal(tt.in); got != tt.want {
			t.Errorf("formatReal(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatReal_NoScientificNotation(t *testing.T) {
	// A value that %g would render as "1e+21" must stay fixed-point: an
	// exponent is not a valid PDF number (design §3.2 / O1).
	for _, f := range []float64{1e21, 1e-9, -1e21} {
		got := formatReal(f)
		if strings.ContainsAny(got, "eE") {
			t.Errorf("formatReal(%v) = %q, contains an exponent (invalid PDF number)", f, got)
		}
	}
}

func TestSerializeName(t *testing.T) {
	tests := []struct {
		in   Name
		want string
	}{
		{"Foo", "/Foo"},
		{"F1", "/F1"},
		{"A B", "/A#20B"},  // space (< 0x21) is re-escaped
		{"a/b", "/a#2Fb"},  // '/' is a delimiter
		{"a(b", "/a#28b"},  // '(' is a delimiter
		{"a#b", "/a#23b"},  // '#' itself must re-escape to #23
		{"\xffX", "/#FFX"}, // high byte (> 0x7e)
		{"\x01Y", "/#01Y"}, // control byte (< 0x21)
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		serializeName(&buf, tt.in)
		if got := buf.String(); got != tt.want {
			t.Errorf("serializeName(%q) = %q, want %q", string(tt.in), got, tt.want)
		}
		if !strings.HasPrefix(buf.String(), "/") {
			t.Errorf("serializeName(%q) missing leading '/'", string(tt.in))
		}
	}
}

func TestSerializeString(t *testing.T) {
	tests := []struct {
		in   String
		want string
	}{
		// Literal form escapes '(', ')' and '\'.
		{String{Value: []byte("a(b)c\\")}, `(a\(b\)c\\)`},
		{String{Value: []byte("plain")}, "(plain)"},
		// Hex form emits each byte as two uppercase hex digits.
		{String{Value: []byte{0x00, 0xFF, 0x10}, Hex: true}, "<00FF10>"},
		{String{Value: []byte("Hi"), Hex: true}, "<4869>"},
	}
	for _, tt := range tests {
		var buf bytes.Buffer
		serializeString(&buf, tt.in)
		if got := buf.String(); got != tt.want {
			t.Errorf("serializeString(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSerializeObject_IntegerStaysInteger(t *testing.T) {
	for _, tt := range []struct {
		in   Object
		want string
	}{
		{Integer(42), "42"},
		{Integer(-42), "-42"},
		{Integer(0), "0"},
		{Boolean(true), "true"},
		{Boolean(false), "false"},
		{Null{}, "null"},
	} {
		var buf bytes.Buffer
		serializeObject(&buf, tt.in)
		if got := buf.String(); got != tt.want {
			t.Errorf("serializeObject(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWriteXrefEntry_Is20Bytes(t *testing.T) {
	// The §7.5.4 fixed-width rule is mandatory: validators reject a wrong width.
	var buf bytes.Buffer
	writeXrefEntry(&buf, 1234)
	got := buf.Bytes()
	if len(got) != 20 {
		t.Fatalf("xref entry length = %d, want exactly 20", len(got))
	}
	if want := "0000001234 00000 n \n"; string(got) != want {
		t.Errorf("xref entry = %q, want %q", got, want)
	}
}

// --- Writer end-to-end -----------------------------------------------------

func TestWriter_NoRootErrors(t *testing.T) {
	w := NewWriter()
	w.Add(Dict{"Type": Name("Test")})
	if _, err := w.Bytes(); err == nil {
		t.Fatal("Bytes() without SetRoot must return an error")
	}
}

// TestWriter_RoundTripBinaryStream pins three properties at once: the /Length
// rewrite, binary-safe stream bytes, and xref-offset accuracy. If any recorded
// byte offset were wrong, NewResolver/Resolve below would fail to parse the
// object at that offset.
func TestWriter_RoundTripBinaryStream(t *testing.T) {
	// Raw bytes that would break a naive serializer: NUL, '(', newline, high
	// byte, ')' and a backslash. None of these must be transformed.
	raw := []byte{0x00, '(', '\n', 0xFF, ')', '\\', 'x'}

	w := NewWriter()
	streamRef := w.Add(&Stream{
		Dict: Dict{"Type": Name("XObject"), "Length": Integer(999)}, // stale /Length
		Raw:  raw,
	})
	intRef := w.Add(Integer(7))
	rootRef := w.Add(Dict{
		"Type":    Name("Catalog"),
		"Stream":  streamRef,
		"Counter": intRef,
	})
	w.SetRoot(rootRef)

	out, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewResolver(out)
	if err != nil {
		t.Fatalf("re-parse of writer output failed: %v", err)
	}

	// /Root in the trailer must point at the catalog object we set.
	trailerRoot, ok := r.Trailer().GetReference("Root")
	if !ok || trailerRoot.Number != rootRef.Number {
		t.Fatalf("trailer /Root = %v, want reference to object %d", r.Trailer()["Root"], rootRef.Number)
	}

	// Resolve the catalog and follow /Stream.
	cat, ok, err := r.ResolveDict(trailerRoot)
	if err != nil || !ok {
		t.Fatalf("resolve /Root: ok=%v err=%v", ok, err)
	}
	if typ, _ := cat.GetName("Type"); typ != "Catalog" {
		t.Errorf("catalog /Type = %q, want Catalog", typ)
	}

	streamObj, err := r.Resolve(cat["Stream"])
	if err != nil {
		t.Fatal(err)
	}
	st, ok := streamObj.(*Stream)
	if !ok {
		t.Fatalf("resolved stream = %T, want *Stream", streamObj)
	}
	if !bytes.Equal(st.Raw, raw) {
		t.Errorf("stream Raw = %v, want byte-identical %v", st.Raw, raw)
	}
	// /Length must equal the TRUE raw length, not the stale 999 we passed in.
	if length, _ := st.Dict.GetInt("Length"); int(length) != len(raw) {
		t.Errorf("/Length = %d, want %d (true Raw length)", length, len(raw))
	}

	// The Integer object must round-trip as an Integer.
	counter, err := r.Resolve(cat["Counter"])
	if err != nil {
		t.Fatal(err)
	}
	if i, ok := counter.(Integer); !ok || i != 7 {
		t.Errorf("counter = %v (%T), want Integer(7)", counter, counter)
	}
}

// TestWriter_Deterministic asserts the same object set serializes byte-identically
// on repeat runs (sorted dict keys, stable numbering, fixed EOL). Golden files
// and Cloud content-hashing depend on this.
func TestWriter_Deterministic(t *testing.T) {
	build := func() []byte {
		w := NewWriter()
		// A dict with several keys exercises the sorted-key path (Go map order is
		// random, so an unsorted writer would flake here).
		ref := w.Add(Dict{
			"Zebra":    Integer(1),
			"Alpha":    Name("x"),
			"Mango":    String{Value: []byte("m")},
			"Beta":     Real(1.5),
			"Resource": Array{Integer(1), Integer(2)},
		})
		w.SetRoot(w.Add(Dict{"Type": Name("Catalog"), "Ref": ref}))
		b, err := w.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := build(), build()
	if !bytes.Equal(a, b) {
		t.Errorf("writer output is non-deterministic:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", a, b)
	}
}

// TestWriter_ReserveFill covers the reserve-then-fill path the copier relies on
// for cycles: a reserved slot must serialize with the filled body, not the Null
// placeholder.
func TestWriter_ReserveFill(t *testing.T) {
	w := NewWriter()
	slot := w.Reserve()
	other := w.Add(Dict{"Peer": slot})
	w.Fill(slot, Dict{"Type": Name("Filled"), "Back": other})
	w.SetRoot(w.Add(Dict{"Type": Name("Catalog"), "Pages": slot}))

	out, err := w.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(out)
	if err != nil {
		t.Fatal(err)
	}
	d, ok, err := r.ResolveDict(slot)
	if err != nil || !ok {
		t.Fatalf("resolve reserved slot: ok=%v err=%v", ok, err)
	}
	if typ, _ := d.GetName("Type"); typ != "Filled" {
		t.Errorf("reserved slot /Type = %q, want Filled (Fill body lost)", typ)
	}

	// Fill on an out-of-range ref must be a defensive no-op (never panic).
	w.Fill(Reference{Number: 9999}, Integer(1))
	w.Fill(Reference{Number: 0}, Integer(1))
}

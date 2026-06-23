// Package parse implements the PDF parsing layer per ISO 32000-1:2008: object
// model, lexer, xref, page tree, and content streams. It is a clean-room
// implementation based solely on the public specification (see
// .claude/SECURITY.md §6).
//
// This file defines the object model: the eight basic PDF object types
// (ISO §7.3) plus Reference (indirect object) and Stream.
package parse

import "fmt"

// Object is a value in the PDF object model. The set is closed: Null, Boolean,
// Integer, Real, String, Name, Array, Dict, Reference, Stream.
type Object interface {
	// String returns a debug representation, not a PDF serialization.
	String() string

	isObject()
}

// Null is the PDF null object (the `null` keyword).
type Null struct{}

// Boolean is `true` / `false`.
type Boolean bool

// Integer is a PDF integer, stored as int64 to hold large byte offsets.
type Integer int64

// Real is a PDF real number.
type Real float64

// String is a PDF string decoded to raw bytes. Hex records the original syntax
// (literal `(...)` vs hex `<...>`) so it can be re-encoded faithfully.
type String struct {
	Value []byte
	Hex   bool
}

// Name is a PDF name, stored WITHOUT the leading '/' and with `#xx` escapes
// decoded.
type Name string

// Array is a PDF array; elements may be any Object, including a Reference.
type Array []Object

// Dict is a PDF dictionary. Keys are always Name (per spec). Used for both
// plain dictionaries and the dictionary part of a Stream.
type Dict map[Name]Object

// Reference is an indirect reference `n g R` to an indirect object.
type Reference struct {
	Number     int
	Generation int
}

// Stream is a dictionary paired with its raw bytes (not yet decoded per
// /Filter). Decoding (Flate/LZW/…) belongs to the render/extract layers, not
// the lexer.
type Stream struct {
	Dict Dict
	Raw  []byte
}

func (Null) isObject()      {}
func (Boolean) isObject()   {}
func (Integer) isObject()   {}
func (Real) isObject()      {}
func (String) isObject()    {}
func (Name) isObject()      {}
func (Array) isObject()     {}
func (Dict) isObject()      {}
func (Reference) isObject() {}
func (*Stream) isObject()   {}

func (Null) String() string      { return "null" }
func (b Boolean) String() string { return fmt.Sprintf("%t", bool(b)) }
func (i Integer) String() string { return fmt.Sprintf("%d", int64(i)) }
func (r Real) String() string    { return fmt.Sprintf("%g", float64(r)) }
func (s String) String() string  { return fmt.Sprintf("(%s)", s.Value) }
func (n Name) String() string    { return "/" + string(n) }

func (a Array) String() string {
	out := "["
	for i, o := range a {
		if i > 0 {
			out += " "
		}
		out += o.String()
	}
	return out + "]"
}

func (d Dict) String() string {
	out := "<<"
	for k, v := range d {
		out += " " + k.String() + " " + v.String()
	}
	return out + " >>"
}

func (r Reference) String() string { return fmt.Sprintf("%d %d R", r.Number, r.Generation) }

func (s *Stream) String() string {
	return fmt.Sprintf("%s stream(%d bytes)", s.Dict.String(), len(s.Raw))
}

// --- Type-checked accessors, to avoid repeated type assertions at call sites ---

// GetName returns the Name value at key; ok is false if the key is missing or
// has a different type.
func (d Dict) GetName(key Name) (Name, bool) {
	n, ok := d[key].(Name)
	return n, ok
}

// GetInt returns the Integer value at key.
func (d Dict) GetInt(key Name) (Integer, bool) {
	i, ok := d[key].(Integer)
	return i, ok
}

// GetDict returns the nested Dict at key.
func (d Dict) GetDict(key Name) (Dict, bool) {
	sub, ok := d[key].(Dict)
	return sub, ok
}

// GetArray returns the Array at key.
func (d Dict) GetArray(key Name) (Array, bool) {
	a, ok := d[key].(Array)
	return a, ok
}

// GetReference returns the Reference at key.
func (d Dict) GetReference(key Name) (Reference, bool) {
	r, ok := d[key].(Reference)
	return r, ok
}

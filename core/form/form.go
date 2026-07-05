// Package form reads interactive AcroForm fields from a parsed PDF (ISO 32000-1
// §12.7). It walks the /AcroForm field tree, resolves fully-qualified field
// names, and reports each terminal field's type, value, options, and flags. It
// is pure Go (MIT-safe) and must not import core (import cycle) — core maps
// form.Field into its own FormField.
//
// Wave-2 scope: reading the field tree (flat fields and one level of
// widget/child kids). Deeply nested field hierarchies and XFA forms are
// best-effort.
package form

import (
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// FieldType classifies a form field.
type FieldType string

const (
	Text      FieldType = "text"
	Checkbox  FieldType = "checkbox"
	Radio     FieldType = "radio"
	Dropdown  FieldType = "dropdown"
	Signature FieldType = "signature"
	Unknown   FieldType = "unknown"
)

// Field is one terminal AcroForm field.
type Field struct {
	Name     string // fully-qualified (parent.child), from the /T chain
	Type     FieldType
	Value    string
	Options  []string
	ReadOnly bool
	Required bool
}

// maxFieldDepth bounds recursion into a hostile/cyclic field tree.
const maxFieldDepth = 32

// ReadFields walks the document's AcroForm and returns its terminal fields in
// document order. A document with no /AcroForm yields an empty slice (nil error).
func ReadFields(r *parse.Resolver) ([]Field, error) {
	cat, err := r.Catalog()
	if err != nil {
		return nil, err
	}
	af, ok, err := r.ResolveDict(cat["AcroForm"])
	if err != nil || !ok {
		return nil, nil
	}
	var out []Field
	visited := map[int]bool{}
	for _, fref := range resolveArray(r, af["Fields"]) {
		walkField(r, fref, "", inherited{}, 0, visited, &out)
	}
	return out, nil
}

// inherited carries the attributes a field inherits from its ancestors.
type inherited struct {
	ft    parse.Name // /FT
	flags int64      // /Ff
}

// walkField visits one node of the field tree, appending terminal fields to out.
func walkField(r *parse.Resolver, node parse.Object, parentName string, inh inherited, depth int, visited map[int]bool, out *[]Field) {
	if depth > maxFieldDepth {
		return
	}
	if ref, ok := node.(parse.Reference); ok {
		if visited[ref.Number] {
			return
		}
		visited[ref.Number] = true
	}
	d, ok, err := r.ResolveDict(node)
	if err != nil || !ok {
		return
	}

	// Extend the qualified name with this node's partial name (/T), if any.
	name := parentName
	if t, ok := d["T"].(parse.String); ok {
		part := decodeText(t.Value)
		if name == "" {
			name = part
		} else {
			name = name + "." + part
		}
	}
	// Inherit /FT and /Ff downward.
	if ft, ok := d.GetName("FT"); ok {
		inh.ft = ft
	}
	if ff, ok := d.GetInt("Ff"); ok {
		inh.flags = int64(ff)
	}

	// A node whose /Kids contain child FIELDS (they carry their own /T) is an
	// intermediate node; kids that are widgets (no /T) belong to this field.
	kids := resolveArray(r, d["Kids"])
	if hasChildFields(r, kids) {
		for _, k := range kids {
			walkField(r, k, name, inh, depth+1, visited, out)
		}
		return
	}

	// Terminal field: must have a field type to be meaningful.
	if inh.ft == "" {
		return
	}
	*out = append(*out, buildField(r, d, name, inh))
}

// hasChildFields reports whether any kid is itself a field (has a /T entry),
// distinguishing an intermediate node from a terminal field with widget kids.
func hasChildFields(r *parse.Resolver, kids []parse.Object) bool {
	for _, k := range kids {
		if kd, ok, err := r.ResolveDict(k); err == nil && ok {
			if _, has := kd["T"]; has {
				return true
			}
		}
	}
	return false
}

func buildField(r *parse.Resolver, d parse.Dict, name string, inh inherited) Field {
	f := Field{
		Name:     name,
		Type:     fieldType(inh.ft, inh.flags),
		Value:    decodeValue(d["V"]),
		ReadOnly: inh.flags&1 != 0,
		Required: inh.flags&2 != 0,
	}
	if inh.ft == "Ch" {
		f.Options = readOptions(r, d["Opt"])
	}
	return f
}

// fieldType maps /FT + /Ff bits to a FieldType.
func fieldType(ft parse.Name, flags int64) FieldType {
	switch ft {
	case "Tx":
		return Text
	case "Btn":
		switch {
		case flags&(1<<16) != 0: // Pushbutton
			return Unknown
		case flags&(1<<15) != 0: // Radio
			return Radio
		default:
			return Checkbox
		}
	case "Ch":
		return Dropdown
	case "Sig":
		return Signature
	default:
		return Unknown
	}
}

// decodeValue renders a field /V (String, Name, or Array) as a display string.
func decodeValue(v parse.Object) string {
	switch t := v.(type) {
	case parse.String:
		return decodeText(t.Value)
	case parse.Name:
		return string(t)
	case parse.Array:
		var parts []string
		for _, e := range t {
			if s, ok := e.(parse.String); ok {
				parts = append(parts, decodeText(s.Value))
			}
		}
		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

// readOptions decodes a choice field's /Opt: each entry is a string or a
// [export, display] pair (the display value is used).
func readOptions(r *parse.Resolver, o parse.Object) []string {
	arr := resolveArray(r, o)
	var out []string
	for _, e := range arr {
		switch v := e.(type) {
		case parse.String:
			out = append(out, decodeText(v.Value))
		case parse.Array:
			if len(v) == 2 {
				if s, ok := v[1].(parse.String); ok {
					out = append(out, decodeText(s.Value))
				}
			} else if len(v) == 1 {
				if s, ok := v[0].(parse.String); ok {
					out = append(out, decodeText(s.Value))
				}
			}
		}
	}
	return out
}

// resolveArray resolves obj (following a reference) to an Array, or nil.
func resolveArray(r *parse.Resolver, obj parse.Object) parse.Array {
	resolved, err := r.Resolve(obj)
	if err != nil {
		return nil
	}
	a, _ := resolved.(parse.Array)
	return a
}

// decodeText decodes a PDF text string: UTF-16BE with a BOM, else Latin-1.
func decodeText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		var sb strings.Builder
		for i := 2; i+1 < len(b); i += 2 {
			sb.WriteRune(rune(uint16(b[i])<<8 | uint16(b[i+1])))
		}
		return sb.String()
	}
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

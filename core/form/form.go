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

// FillWidget is a form widget to draw when flattening: its page, rectangle,
// field type, and the value to render.
type FillWidget struct {
	PageIndex int
	Rect      parse.Rectangle
	Type      FieldType
	Value     string
}

// WidgetsToFlatten scans each page's /Annots for form widgets. It returns the
// widgets that have a value to draw (applying overrides: field name -> value),
// and the set of pages that carry any widget (so the caller can drop their
// interactive annotations even where nothing is drawn). Empty text and unchecked
// buttons ("Off") produce no drawn widget.
func WidgetsToFlatten(r *parse.Resolver, pages []parse.PageInfo, overrides map[string]string) ([]FillWidget, map[int]bool) {
	var out []FillWidget
	withWidgets := map[int]bool{}
	for i := range pages {
		for _, aref := range resolveArray(r, pages[i].Dict["Annots"]) {
			ad, ok, err := r.ResolveDict(aref)
			if err != nil || !ok {
				continue
			}
			if sub, _ := ad.GetName("Subtype"); sub != "Widget" {
				continue
			}
			withWidgets[i] = true

			name, ft, flags, val, ok := fieldOfWidget(r, ad)
			if !ok {
				continue
			}
			if v, has := overrides[name]; has {
				val = v
			}
			if val == "" || val == "Off" {
				continue // nothing to draw (blank text / unchecked)
			}
			rect, ok := rectangle(r, ad["Rect"])
			if !ok {
				continue
			}
			out = append(out, FillWidget{PageIndex: i, Rect: rect, Type: fieldType(ft, flags), Value: val})
		}
	}
	return out, withWidgets
}

// fieldOfWidget climbs a widget's /Parent chain to resolve the owning field's
// qualified name, type, flags, and value (the widget itself may carry them when
// widget and field are merged).
func fieldOfWidget(r *parse.Resolver, widget parse.Dict) (name string, ft parse.Name, flags int64, value string, ok bool) {
	node := widget
	var parts []string
	gotFT, gotV := false, false
	for depth := 0; depth <= maxFieldDepth; depth++ {
		if t, has := node["T"].(parse.String); has {
			parts = append([]string{decodeText(t.Value)}, parts...)
		}
		if f, has := node.GetName("FT"); has && !gotFT {
			ft, gotFT = f, true
		}
		if ff, has := node.GetInt("Ff"); has && flags == 0 {
			flags = int64(ff)
		}
		if v, has := node["V"]; has && !gotV {
			value, gotV = decodeValue(v), true
		}
		parent, has := node["Parent"]
		if !has {
			break
		}
		pd, ok, err := r.ResolveDict(parent)
		if err != nil || !ok {
			break
		}
		node = pd
	}
	return strings.Join(parts, "."), ft, flags, value, gotFT
}

// rectangle resolves a /Rect array [llx lly urx ury] to a normalized Rectangle.
func rectangle(r *parse.Resolver, obj parse.Object) (parse.Rectangle, bool) {
	arr := resolveArray(r, obj)
	if len(arr) < 4 {
		return parse.Rectangle{}, false
	}
	var f [4]float64
	for i := 0; i < 4; i++ {
		v, err := r.Resolve(arr[i])
		if err != nil {
			return parse.Rectangle{}, false
		}
		switch n := v.(type) {
		case parse.Integer:
			f[i] = float64(n)
		case parse.Real:
			f[i] = float64(n)
		default:
			return parse.Rectangle{}, false
		}
	}
	llx, lly, urx, ury := f[0], f[1], f[2], f[3]
	if urx < llx {
		llx, urx = urx, llx
	}
	if ury < lly {
		lly, ury = ury, lly
	}
	return parse.Rectangle{LLX: llx, LLY: lly, URX: urx, URY: ury}, true
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

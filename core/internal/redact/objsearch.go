package redact

import (
	"errors"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// objsearch.go is the object-level half of verification (docs/design/
// redaction-hardening.md §5.6 check 3): every indirect object of a document
// is read the way it holds text — strings decoded as text strings, streams
// decoded through their filters, content streams by the strings their
// operators show — and searched for needles. Objects that hold no text but
// binary or structural data are not searched: their bytes would match short
// needles by chance (an Indexed palette or a sampled function ramp holds
// "1234" and "ABCD"; a font names "Adobe"). Those are image samples, font
// programs, CMaps and the other font structures, ICC profiles, functions,
// halftones and Indexed lookup tables.

// Role is how an indirect object is read (ObjectRoles): a set of bits. It
// is decided by the route that reaches the object — the key or array slot
// that refers to it — not by keys the object itself carries, which a file
// can add to anything (an unused form with /N or /Length1, a /PieceInfo
// dictionary with /Registry and /Ordering). Only an object that nothing
// reaches is classified by its keys (RoleUnreached).
type Role uint16

// The roles.
const (
	// RoleData: strings, and stream data unless the stream is image samples,
	// are text.
	RoleData Role = 1 << iota
	// RoleContent: a content stream (page contents, form, tiling pattern,
	// soft-mask group, annotation appearance, Type 3 glyph procedure): the
	// strings its operators show, and its dictionary's strings.
	RoleContent
	// RoleFont: a font, reached as one (a /Font entry of a resource
	// dictionary or ExtGState) or as part of one (its descriptor, programs,
	// CMaps, widths): neither its strings nor its data are text. A Type 3
	// font's glyph procedures and resources have their own roles.
	RoleFont
	// RoleBinary: binary data reached as such (an image, a function, an ICC
	// profile, a lookup table, a shading): not searched.
	RoleBinary
	// RoleUnreached: an object nothing reaches (an orphan): classified by its
	// keys.
	RoleUnreached

	// Markers of what a dictionary is, which decide its entries' roles; they
	// go with RoleData and do not propagate.
	roleResources  // a /Resources dictionary
	roleFontDir    // a resource dictionary's /Font: its entries are fonts
	roleGSDir      // a resource dictionary's /ExtGState
	roleExtGState  // an ExtGState: its /Font names a font
	roleShadingDir // a resource dictionary's /Shading: its entries are shadings
	roleCharProcs  // a Type 3 font's /CharProcs: its entries are glyph procedures
)

// markers are the dictionary markers.
const markers = roleResources | roleFontDir | roleGSDir | roleExtGState | roleShadingDir | roleCharProcs

// maxRoleDepth bounds the nesting of direct values ObjectRoles follows.
const maxRoleDepth = 64

// ObjectRoles returns the role(s) each indirect object reachable from the
// document's catalog and trailer /Info has, by the routes that reach it.
// maxObjects bounds the objects visited.
//
// Returns an error past maxObjects.
func ObjectRoles(r *parse.Resolver, maxObjects int) (map[int]Role, error) {
	rw := &roleWalk{r: r, roles: map[int]Role{}, max: maxObjects}
	tr := r.Trailer()
	if err := rw.value(tr["Root"], RoleData, 0); err != nil {
		return nil, err
	}
	if err := rw.value(tr["Info"], RoleData, 0); err != nil {
		return nil, err
	}
	for len(rw.queue) > 0 {
		it := rw.queue[len(rw.queue)-1]
		rw.queue = rw.queue[:len(rw.queue)-1]
		obj, err := r.Resolve(parse.Reference{Number: it.num})
		if err != nil {
			continue
		}
		if err := rw.value(obj, it.role, 0); err != nil {
			return nil, err
		}
	}
	return rw.roles, nil
}

// errRoleGraph is ObjectRoles' object bound.
var errRoleGraph = errors.New("verification object graph exceeds its bound")

type roleItem struct {
	num  int
	role Role
}

type roleWalk struct {
	r     *parse.Resolver
	roles map[int]Role
	queue []roleItem
	max   int
}

// value walks a value reached with role.
func (rw *roleWalk) value(o parse.Object, role Role, depth int) error {
	if depth > maxRoleDepth {
		return nil
	}
	switch v := o.(type) {
	case parse.Reference:
		obj, err := rw.r.Resolve(v)
		if err == nil {
			role = refine(obj, role)
		}
		if rw.roles[v.Number]&role == role {
			return nil
		}
		if _, seen := rw.roles[v.Number]; !seen && len(rw.roles) >= rw.max {
			return errRoleGraph
		}
		rw.roles[v.Number] |= role
		rw.queue = append(rw.queue, roleItem{v.Number, role})
	case parse.Array:
		cs := colorSpaceRoles(v)
		for i, e := range v {
			r := role &^ markers
			if role&(RoleFont|RoleBinary) != 0 {
				r = role
			}
			if cs != nil {
				r = cs(i, r)
			}
			if err := rw.value(e, r, depth+1); err != nil {
				return err
			}
		}
	case parse.Dict:
		role = refine(v, role)
		typ, _ := v["Type"].(parse.Name)
		for k, e := range v {
			cr := childRole(k, role)
			switch {
			case role&(RoleFont|RoleBinary) != 0:
			case k == "Contents" && typ == "Page", k == "G" && v["S"] != nil:
				cr = RoleContent // a page's content streams; a soft mask's group
			case k == "AP":
				if err := rw.appearances(e, depth+1); err != nil {
					return err
				}
				continue
			}
			if err := rw.value(e, cr, depth+1); err != nil {
				return err
			}
		}
	case *parse.Stream:
		return rw.value(v.Dict, refine(v, role), depth+1)
	}
	return nil
}

// appearances walks an annotation's /AP value: the appearance dictionary
// is data; its /N, /R and /D streams, or the streams of their state
// dictionaries, are content.
func (rw *roleWalk) appearances(ap parse.Object, depth int) error {
	if ref, ok := ap.(parse.Reference); ok {
		if err := rw.value(ref, RoleData, depth); err != nil {
			return err
		}
	}
	d, ok, err := rw.r.ResolveDict(ap)
	if err != nil || !ok {
		return nil
	}
	for _, k := range [...]parse.Name{"N", "R", "D"} {
		v := d[k]
		if ref, ok := v.(parse.Reference); ok {
			if obj, err := rw.r.Resolve(ref); err == nil {
				if _, isStream := obj.(*parse.Stream); !isStream {
					// A dictionary of appearance states.
					if err := rw.value(ref, RoleData, depth); err != nil {
						return err
					}
					v = obj
				}
			}
		}
		if states, ok := v.(parse.Dict); ok {
			for _, st := range states {
				if err := rw.value(st, RoleContent, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		if err := rw.value(v, RoleContent, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// refine is the role of a resolved object reached with role. For a stream
// reached by a route, only what the stream declares itself to be decides:
// image samples (an image /Subtype or an image-only filter, §5.6) are binary,
// a form or a pattern is content. An object nothing reaches is classified by
// its keys too (refineUnreached).
func refine(obj parse.Object, role Role) Role {
	if role&RoleUnreached != 0 {
		return refineUnreached(obj, role)
	}
	st, ok := obj.(*parse.Stream)
	if !ok || role&(RoleFont|RoleBinary) != 0 {
		return role
	}
	sub, _ := st.Dict["Subtype"].(parse.Name)
	switch {
	case sub == "Image", imageFilter(st.Dict["Filter"]):
		return RoleBinary
	case sub == "Form", st.Dict["PatternType"] != nil:
		return RoleContent | role&markers
	}
	return role
}

// refineUnreached classifies an object nothing reaches by its keys: font
// structures, functions, halftones, ICC profiles and image samples are not
// text; forms and patterns are content.
func refineUnreached(obj parse.Object, role Role) Role {
	var d parse.Dict
	stream := false
	switch v := obj.(type) {
	case parse.Dict:
		d = v
	case *parse.Stream:
		d, stream = v.Dict, true
	default:
		return role
	}
	typ, _ := d["Type"].(parse.Name)
	sub, _ := d["Subtype"].(parse.Name)
	switch {
	case typ == "Font", typ == "FontDescriptor", typ == "CMap":
		return RoleFont
	case d["Registry"] != nil && d["Ordering"] != nil: // CIDSystemInfo
		return RoleFont
	case d["FunctionType"] != nil, d["HalftoneType"] != nil:
		return RoleBinary
	}
	if !stream {
		return role
	}
	_, iccN := d["N"].(parse.Integer)
	switch {
	case sub == "Image", imageFilter(d["Filter"]), iccN:
		return RoleBinary
	case d["Length1"] != nil, d["Length2"] != nil, d["Length3"] != nil,
		sub == "Type1C", sub == "CIDFontType0C", sub == "OpenType":
		return RoleFont
	case sub == "Form", d["PatternType"] != nil:
		return RoleContent | RoleUnreached
	}
	return role
}

// childRole is the role of the value of key k in a dictionary read with
// role (by route).
func childRole(k parse.Name, role Role) Role {
	switch {
	case role&roleFontDir != 0:
		return RoleFont // every entry of a /Font resource dictionary is a font
	case role&roleCharProcs != 0:
		return RoleContent // every glyph procedure of a Type 3 font
	case role&roleShadingDir != 0:
		return RoleBinary
	case role&roleGSDir != 0:
		return RoleData | roleExtGState
	}
	switch k {
	case "Resources":
		return RoleData | roleResources
	case "CharProcs":
		if role&RoleFont != 0 {
			return RoleData | roleCharProcs
		}
	case "Font":
		if role&roleResources != 0 {
			return RoleData | roleFontDir
		}
		if role&roleExtGState != 0 {
			return RoleFont // /Font [font size]
		}
	case "ExtGState":
		if role&roleResources != 0 {
			return RoleData | roleGSDir
		}
	case "Shading":
		if role&roleResources != 0 {
			return RoleData | roleShadingDir
		}
		return RoleBinary // a shading pattern's shading
	}
	if role&RoleFont != 0 {
		// The parts of a font are font structures: descriptor, programs,
		// CMaps, encodings, widths.
		return RoleFont
	}
	switch k {
	case "Function", "Functions", "TR", "TR2", "BG", "BG2", "UCR", "UCR2", "HT", "SpotFunction",
		"TransferFunction":
		return RoleBinary
	}
	return RoleData | role&RoleUnreached
}

// imageFilter reports whether a /Filter value names a filter only image
// samples use (DCT, JPX, JBIG2, CCITT fax), alone or in an array.
func imageFilter(f parse.Object) bool {
	is := func(o parse.Object) bool {
		n, _ := o.(parse.Name)
		switch n {
		case "DCTDecode", "DCT", "JPXDecode", "JBIG2Decode", "CCITTFaxDecode", "CCF":
			return true
		}
		return false
	}
	if arr, ok := f.(parse.Array); ok {
		for _, e := range arr {
			if is(e) {
				return true
			}
		}
		return false
	}
	return is(f)
}

// colorSpaceRoles returns, for a colour space array, the role of each
// element: an ICC profile, an Indexed lookup and a Separation or DeviceN tint
// transform are binary. nil for any other array.
func colorSpaceRoles(a parse.Array) func(i int, role Role) Role {
	if len(a) == 0 {
		return nil
	}
	fam, _ := a[0].(parse.Name)
	binaryAt := -1
	switch fam {
	case "ICCBased":
		binaryAt = 1
	case "Indexed", "I", "Separation", "DeviceN":
		binaryAt = 3
	default:
		return nil
	}
	return func(i int, role Role) Role {
		if i == binaryAt {
			return RoleBinary
		}
		return role
	}
}

// ObjectSearch searches objects for needles (verification). Decoding is
// charged to a budget shared by every search of the call.
type ObjectSearch struct {
	R *parse.Resolver
	// Budget is the bytes the searches may still decode; a search past it
	// fails with a budget refusal.
	Budget int64
	// Latin1Shown reads the strings content streams show as Latin-1 only
	// (one of the readings the text model matches, Page.FindNeedles), not
	// every way Needles.FindBytes reads bytes: a hit is then also a hit of
	// the text model, so automatic extension can attribute it.
	Latin1Shown bool
}

// Search returns the tags of the needles of text (skeleton matching) and raw
// (byte matching, in the strings a content stream shows only) found in
// object num read with role. Returns a *Refusal (budget_exceeded) once the
// budget is spent or a stream exceeds the per-stream cap, and ErrUndecodable
// (with the tags of its dictionary's strings) for a stream it must read that
// does not decode.
func (s *ObjectSearch) Search(num int, role Role, text, raw *Needles) (uint8, error) {
	obj, err := s.R.Resolve(parse.Reference{Number: num})
	if err != nil {
		return 0, nil
	}
	role = refine(obj, role)
	if role&(RoleData|RoleContent) == 0 {
		return 0, nil
	}
	st, ok := obj.(*parse.Stream)
	if !ok {
		return stringTags(obj, text, role, 0), nil
	}
	tags := stringTags(st.Dict, text, role&^RoleContent|RoleData, 0)
	data, err := s.decode(st)
	if err != nil {
		return tags, err
	}
	if role&RoleContent != 0 {
		return tags | contentStrings(data, text, raw, s.Latin1Shown), nil
	}
	return tags | text.FindBytes(data, nil), nil
}

// ErrUndecodable is returned by ObjectSearch.Search for a stream it must
// read but cannot decode (an unsupported or failing filter): what it holds is
// unknown, so the caller must not treat it as holding nothing.
var ErrUndecodable = errors.New("a stream to search does not decode")

// decode decodes a stream within the budget. Returns a budget refusal past
// the call's budget or the per-stream cap, ErrUndecodable when the filters
// fail.
func (s *ObjectSearch) decode(st *parse.Stream) ([]byte, error) {
	limit := min(s.Budget, parse.MaxPageContent)
	data, produced, err := parse.DecodeStreamLimit(st, max(limit, 0))
	s.Budget -= max(produced, int64(len(data)))
	if s.Budget < 0 || errors.Is(err, parse.ErrDecodeLimit) {
		return nil, budget("verification decode")
	}
	if err != nil {
		return nil, ErrUndecodable
	}
	return data, nil
}

// ValueTags returns the tags of the text needles in the strings of a direct
// value (a dictionary or array written inside another object), read as
// Search reads an object's strings.
func ValueTags(o parse.Object, text *Needles) uint8 { return stringTags(o, text, RoleData, 0) }

// stringTags returns the tags of the text needles in the strings of a direct
// value read with role, skipping what its route makes font structures or
// binary data (fonts, Indexed lookups, functions).
func stringTags(o parse.Object, text *Needles, role Role, depth int) uint8 {
	if depth > maxRoleDepth || text.Len() == 0 || role&(RoleFont|RoleBinary) != 0 {
		return 0
	}
	var tags uint8
	switch v := o.(type) {
	case parse.String:
		tags |= text.FindBytes(v.Value, nil)
	case parse.Array:
		cs := colorSpaceRoles(v)
		for i, e := range v {
			r := role &^ markers
			if cs != nil {
				r = cs(i, r)
			}
			tags |= stringTags(e, text, r, depth+1)
		}
	case parse.Dict:
		role = refine(v, role)
		if role&(RoleFont|RoleBinary) != 0 {
			return 0
		}
		for k, e := range v {
			tags |= stringTags(e, text, childRole(k, role), depth+1)
		}
	}
	return tags
}

// contentStrings returns the tags of the needles in a content stream: the
// strings each text-showing operator shows, joined (raw needles match their
// bytes; latin1Only reads them as Latin-1 only, a reading the text model also
// has), and everything it holds but does not show — comments, the strings of
// other operators, inline-image dictionaries (forHidden) — read as the
// pre-pass reads a page's hidden content.
func contentStrings(data []byte, text, raw *Needles, latin1Only bool) uint8 {
	var tags uint8
	spans := parse.ScanContentSpansWithInlineImages(data)
	for _, op := range spans {
		switch op.Op {
		case "Tj", "TJ", "'", "\"":
			var b []byte
			for _, o := range op.Operands {
				b = appendStrings(b, o, 0)
			}
			if latin1Only {
				tags |= text.FindLatin1(b) | raw.Find(string(b))
				continue
			}
			tags |= text.FindBytes(b, raw)
		}
	}
	return tags | hiddenTags(data, spans, text, raw)
}

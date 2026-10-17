package redact

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// walk.go is the resource walker (docs/design/redaction-hardening.md §5.2,
// §6.2–6.4): from a page's content it follows every route by which ISO
// 32000-1 lets a page paint another content stream — Form XObjects (Do),
// soft-mask groups (gs with /SMask /G), tiling patterns (scn/SCN, and every
// tiling pattern in a unit's resources), Type 3 glyph procedures (Tf or gs
// /Font selecting a Type 3 font) — and checks that none of them shows text.
// Redaction rewrites only the page's own content in Phase 1, so a child unit
// that shows text is a refusal naming its route, never a skip.
//
// Names are looked up in every candidate scope, innermost first: the unit's
// own /Resources, the enclosing units' scopes, the page's resources and each
// /Pages ancestor's (§6.2; ISO says a form with /Resources uses only its own,
// but readers fall back outward), and every distinct object any scope binds
// the name to is walked. Every value is resolved through the Resolver before
// it is tested. Each (stream, route, scope chain) is walked once per call;
// decoding and operator visits are budgeted, and hitting a budget refuses.

// Reason is a refusal reason code (docs/design/redaction-hardening.md §9 P1).
// Core reports it as core.RedactionReason, with the same strings.
type Reason string

// The refusal reasons.
const (
	ReasonContentAmbiguous   Reason = "content_ambiguous"
	ReasonInlineImage        Reason = "inline_image"
	ReasonFormText           Reason = "form_text"
	ReasonPatternText        Reason = "pattern_text"
	ReasonSoftMaskText       Reason = "softmask_text"
	ReasonType3Text          Reason = "type3_text"
	ReasonBudgetExceeded     Reason = "budget_exceeded"
	ReasonUnsupportedXObject Reason = "unsupported_xobject"
)

// Refusal reports content that redaction cannot handle safely: content the
// strict scanner cannot read the way every reader does, a route that shows
// text redaction does not rewrite, an XObject of an unsupported subtype, or a
// page past a redaction budget. Detail describes where, by route, object
// number and content offset, and never holds document text. Err is the
// cause, when there is one: ErrFormXObjectText for form_text and
// unsupported_xobject, the strict scanner's loss for content_ambiguous and
// inline_image. Core reports a Refusal as a *core.RedactionError.
type Refusal struct {
	Reason Reason
	Detail string
	Err    error
}

// Error returns the detail and what is not supported.
func (e *Refusal) Error() string {
	switch e.Reason {
	case ReasonFormText, ReasonUnsupportedXObject:
		return e.Detail + "; " + ErrFormXObjectText.Error()
	case ReasonPatternText:
		return e.Detail + "; redaction of tiling patterns is not supported yet"
	case ReasonSoftMaskText:
		return e.Detail + "; redaction of soft-mask groups is not supported yet"
	case ReasonType3Text:
		return e.Detail + "; redaction of Type 3 glyph procedures is not supported yet"
	}
	return e.Detail
}

// Unwrap returns the cause.
func (e *Refusal) Unwrap() error { return e.Err }

// Walk budgets per page call (§6.4). Hitting any of them is a refusal.
const (
	maxDecodedBytes = 512 << 20 // content decoded: page, forms, groups, cells, glyph procedures, font streams
	// maxOpVisits bounds the operators scanned and interpreted (two visits
	// each: scanLimit), one per memoized revisit, and the characters a run's
	// readings hold. Scans stop at what it can pay for, so it bounds the
	// memory a hostile page costs too. It matches the renderer's default
	// operator budget; the largest page of a 3,100-page corpus needs 0.56 Mi
	// (docs/design/redaction-hardening.md, batch C notes, C27).
	maxOpVisits    = 8 << 20
	maxUnitDepth   = 12      // nesting of units below the page (render/form.go)
	maxInvocations = 4096    // form, soft-mask group and pattern invocations
	maxGlyphProcs  = 1 << 20 // Type 3 glyph procedures walked
	// maxFontSet bounds the fonts a reader may have selected at once (see
	// fontSet). Real files have one; each gs or Tf name bound differently by
	// several scopes adds candidates, and every shown string is decoded once
	// per candidate, so a hostile page could multiply the work.
	maxFontSet = 8
)

// unitKind is a content unit's route (§5.2).
type unitKind int

const (
	unitPage unitKind = iota
	unitForm
	unitPattern
	unitSoftMask
	unitType3
)

// reason is the refusal reason for text shown in a unit of kind k.
func (k unitKind) reason() Reason {
	switch k {
	case unitPattern:
		return ReasonPatternText
	case unitSoftMask:
		return ReasonSoftMaskText
	case unitType3:
		return ReasonType3Text
	}
	return ReasonFormText
}

// String names the route for a refusal's detail.
func (k unitKind) String() string {
	return [...]string{"page", "form XObject", "tiling pattern", "soft-mask group", "Type 3 glyph procedure"}[k]
}

// scope is one candidate resource scope of a unit; parent is the next one
// outward. key identifies the chain, so walk results can be memoized per
// chain without hashing dictionaries.
type scope struct {
	res    parse.Dict
	parent *scope
	key    string
	id     int // scopeID of res (0 for the empty outermost scope)
}

// candidate is an object a scope binds a name to, with its object number (0
// when the binding is direct); inner is set when the binding is the
// innermost scope's, which every reader uses.
type candidate struct {
	obj   parse.Object
	num   int
	inner bool
}

// memoKey identifies one walk of a stream.
type memoKey struct {
	st    *parse.Stream
	kind  unitKind
	scope string
}

// limits are the walk budgets; tests lower them.
type limits struct {
	decoded, ops       int64
	depth, invocations int
	glyphProcs         int
}

// defaultLimits are the budgets of a page call.
var defaultLimits = limits{
	decoded: maxDecodedBytes, ops: maxOpVisits, depth: maxUnitDepth,
	invocations: maxInvocations, glyphProcs: maxGlyphProcs,
}

// walker is the state of one page call.
type walker struct {
	r   *parse.Resolver
	lim limits

	decoded     int64 // bytes decoded (maxDecodedBytes)
	ops         int64 // operator visits (maxOpVisits)
	invocations int   // maxInvocations
	glyphProcs  int   // maxGlyphProcs
	budgetErr   error // a budget hit while decoding a font stream

	content map[*parse.Stream][]byte // decoded unit content
	cached  int64                    // bytes held in content
	// cacheLimit, when positive, is the most content holds: past it a unit
	// is decoded (and charged) again each time it is walked. unitDecoded
	// counts every unit byte decoded.
	cacheLimit  int64
	unitDecoded int64
	memo        map[memoKey]walkResult // walk results
	type3       map[string]walkResult  // Type 3 font checks, by font and scope chain

	// lenient selects the walk PaintedObjects does: content is read with the
	// lenient scanner, text and unsupported XObjects are not refused, a unit
	// that does not decode paints nothing, and tiling patterns count only
	// when selected.
	lenient bool

	// uses collects the resource names looked up (in any scope) while it is
	// set: the page's model sets it per operator so resource GC keeps exactly
	// what the surviving operators, and the units they invoke, can use.
	uses  useSet
	path  map[*parse.Stream]bool // units on the current invocation chain
	fonts map[uintptr]*fontmodel.Font
	// ids maps a dictionary's identity to its scopeID. Identity is a safe
	// key: the Resolver caches every object it parses for the document's
	// lifetime, so an object always resolves to the same map.
	ids map[uintptr]int
	// fingerprints maps the SHA-256 of a resource dictionary's exact
	// encoding (exactEncoding) to the dictionaries with that hash and their
	// scopeIDs; a hit is confirmed by comparing the encodings in full.
	fingerprints map[[sha256.Size]byte][]fingerprint
	nextScopeID  int

	// glyphWX is the d0/d1 width (wx, glyph space) each Type 3 glyph
	// procedure sets; type3Adv is, per Type 3 font, the hull of 0 and its
	// procedures' widths in text space. Ghostscript advances a Type 3 glyph by
	// that width, not by /Widths (§9.6.5 makes /Widths the rule), so boxes
	// cover both.
	glyphWX  map[*parse.Stream]float64
	type3Adv map[uintptr]iv
}

func newWalker(r *parse.Resolver) *walker {
	return &walker{
		r:       r,
		lim:     defaultLimits,
		content: map[*parse.Stream][]byte{},
		memo:    map[memoKey]walkResult{},
		type3:   map[string]walkResult{},
		path:    map[*parse.Stream]bool{},
		fonts:   map[uintptr]*fontmodel.Font{},
		ids:     map[uintptr]int{},

		fingerprints: map[[sha256.Size]byte][]fingerprint{},

		glyphWX:  map[*parse.Stream]float64{},
		type3Adv: map[uintptr]iv{},
	}
}

// resKey is a name of a category of /Resources (/Font, /XObject, …) as the
// scope with id scope binds it: the conforming binding of a lookup, which
// resource GC keeps when scope is the page's own resources.
type resKey struct {
	scope     int
	cat, name parse.Name
}

// objNum is an indirect object some candidate binding of a lookup reached (a
// resource value, or a reference inside a direct one): what removed content
// may have painted (the D side of the deny list).
type objNum int

// confObj is an indirect object the conforming binding of a lookup reached —
// the innermost scope that defines the name, as ISO 32000-1 readers resolve
// it and falling-back readers do when the inner scopes lack it: what kept
// content paints (Output.Used, Painter; the P side).
type confObj int

// useSet is a set of resource names (resKey) and objects (objNum, confObj)
// looked up.
type useSet map[any]struct{}

// add adds every element of t to s.
func (s useSet) add(t useSet) {
	for k := range t {
		s[k] = struct{}{}
	}
}

// objects returns the objects any candidate binding reached (objNum).
func (s useSet) objects() []int {
	var out []int
	for k := range s {
		if n, ok := k.(objNum); ok {
			out = append(out, int(n))
		}
	}
	return out
}

// conforming returns the objects conforming bindings reached (confObj).
func (s useSet) conforming() []int {
	var out []int
	for k := range s {
		if n, ok := k.(confObj); ok {
			out = append(out, int(n))
		}
	}
	return out
}

// walkResult is a memoized walk: its error and the resources it looked up.
type walkResult struct {
	err  error
	uses useSet
}

// use records the binding raw of name in category by the scope s: every
// binding as reachable (objNum); the conforming one (conforming set) also as
// the name resource GC keeps in that scope (resKey) and as a kept object
// (confObj).
func (w *walker) use(s *scope, category, name parse.Name, raw parse.Object, conforming bool) {
	if w.uses == nil {
		return
	}
	addRefs(w.uses, raw, 0, func(n int) any { return objNum(n) })
	if conforming {
		w.uses[resKey{s.id, category, name}] = struct{}{}
		addRefs(w.uses, raw, 0, func(n int) any { return confObj(n) })
	}
}

// addRefs adds key(n) for every reference n in o, at any depth up to a
// bound, to s.
func addRefs(s useSet, o parse.Object, depth int, key func(int) any) {
	if depth > 32 {
		return
	}
	switch v := o.(type) {
	case parse.Reference:
		s[key(v.Number)] = struct{}{}
	case parse.Array:
		for _, e := range v {
			addRefs(s, e, depth+1, key)
		}
	case parse.Dict:
		for _, e := range v {
			addRefs(s, e, depth+1, key)
		}
	case *parse.Stream:
		addRefs(s, v.Dict, depth+1, key)
	}
}

// note records a lookup of name in category without walking what it binds:
// the names and objects scopes bind, for resource GC and the D5 deny set. A
// resolve error is ignored: the operator paints nothing then.
func (w *walker) note(sc *scope, category, name parse.Name) {
	_, _ = w.lookup(sc, category, name)
}

// collect runs f with a fresh collector and returns what f looked up; the
// lookups are also added to the enclosing collector.
func (w *walker) collect(f func() error) walkResult {
	outer := w.uses
	w.uses = useSet{}
	err := f()
	r := walkResult{err: err, uses: w.uses}
	w.uses = outer
	w.replay(r)
	return r
}

// replay adds a memoized walk's lookups to the current collector.
func (w *walker) replay(r walkResult) {
	if w.uses != nil {
		w.uses.add(r.uses)
	}
}

// refuse returns a refusal; form_text and unsupported_xobject refusals wrap
// ErrFormXObjectText.
func refuse(reason Reason, format string, args ...any) error {
	r := &Refusal{Reason: reason, Detail: fmt.Sprintf(format, args...)}
	if reason == ReasonFormText || reason == ReasonUnsupportedXObject {
		r.Err = ErrFormXObjectText
	}
	return r
}

// scanRefusal is the refusal for a strict-scan error in the named unit:
// budget_exceeded past the operator budget, inline_image for an inline image
// some reader could read differently, content_ambiguous otherwise. The scan
// error's message holds no document bytes.
func scanRefusal(unit string, err error) error {
	switch {
	case errors.Is(err, parse.ErrOpLimit):
		return budget("operator")
	}
	// The detail is the loss without the parser's own prefix.
	detail := unit + ": " + strings.TrimPrefix(err.Error(), "parse: lexer error: ")
	if errors.Is(err, parse.ErrInlineImageLoss) {
		return &Refusal{Reason: ReasonInlineImage, Detail: detail, Err: err}
	}
	return &Refusal{Reason: ReasonContentAmbiguous, Detail: detail, Err: err}
}

// scanLimit is the most operators a strict scan may return
// (StrictOptions.MaxOps): what the operator budget can still pay for, at two
// visits per operator — one to scan it (a materialized span costs memory), one
// to interpret it. At least 1.
func (w *walker) scanLimit() int {
	return int(max((w.lim.ops-w.ops)/2, 1))
}

// strictScan scans content strictly within the operator budget and charges
// one visit per operator returned (interpreting them charges another).
// Returns the spans, or the scan's refusal for the named unit.
func (w *walker) strictScan(unit string, content parse.ContentSegments, sc *scope) ([]parse.ContentOpSpan, error) {
	if w.lenient {
		spans := parse.ScanContentSpansWithInlineImages(content.Data)
		if err := w.visit(int64(len(spans))); err != nil {
			return nil, err
		}
		return spans, nil
	}
	spans, err := parse.ScanContentStrict(content,
		parse.StrictOptions{InlineColorSpace: w.colorSpaces(sc), MaxOps: w.scanLimit()})
	if err != nil {
		return nil, scanRefusal(unit, err)
	}
	if err := w.visit(int64(len(spans))); err != nil {
		return nil, err
	}
	return spans, nil
}

// budget returns a budget refusal.
func budget(what string) error {
	return refuse(ReasonBudgetExceeded, "exceeds the redaction %s budget", what)
}

// visit charges n operator visits.
func (w *walker) visit(n int64) error {
	w.ops += n
	if w.ops > w.lim.ops {
		return budget("operator")
	}
	return nil
}

// charge counts decoded bytes against maxDecodedBytes.
func (w *walker) charge(n int64) error {
	w.decoded += n
	if w.decoded > w.lim.decoded {
		return budget("decode")
	}
	return nil
}

// decode returns a stream's decoded bytes within the decode budget, decoding
// each stream once per call. A stream past the per-stream cap stays the
// decode error it was; one past what the budget has left is a refusal.
func (w *walker) decode(st *parse.Stream) ([]byte, error) {
	if data, ok := w.content[st]; ok {
		return data, nil
	}
	left := w.lim.decoded - w.decoded
	limit := min(left, parse.MaxPageContent)
	data, produced, err := parse.DecodeStreamLimit(st, max(limit, 0))
	if cerr := w.charge(max(produced, int64(len(data)))); cerr != nil {
		return nil, cerr
	}
	if errors.Is(err, parse.ErrDecodeLimit) && left <= parse.MaxPageContent {
		return nil, budget("decode")
	}
	if err != nil {
		return nil, err
	}
	w.unitDecoded += int64(len(data))
	if w.cacheLimit > 0 && w.cached+int64(len(data)) > w.cacheLimit {
		return data, nil
	}
	w.content[st] = data
	w.cached += int64(len(data))
	return data, nil
}

// decodeFont is fontmodel's Decode for this call: font streams count against
// the decode budget too. A budget hit is kept, and the page is refused.
func (w *walker) decodeFont(st *parse.Stream) ([]byte, error) {
	data, err := w.decode(st)
	var re *Refusal
	if errors.As(err, &re) && w.budgetErr == nil {
		w.budgetErr = err
	}
	return data, err
}

// font returns the model of a font object, loading each font once per call.
func (w *walker) font(obj parse.Object) *fontmodel.Font {
	if w.r == nil {
		return nil
	}
	d, ok, err := w.r.ResolveDict(obj)
	if err != nil || !ok {
		return nil
	}
	id := dictID(d)
	if f, ok := w.fonts[id]; ok {
		return f
	}
	f := fontmodel.Load(w.r, d, fontmodel.Options{Metrics: true, Decode: w.decodeFont})
	w.fonts[id] = f
	return f
}

// dictID is a dictionary's identity. It is a safe key because the Resolver
// caches every object it parses for the document's lifetime: one object
// always resolves to the same map, and a map is never reused for another.
func dictID(d parse.Dict) uintptr { return reflect.ValueOf(d).Pointer() }

// child returns the scope of a unit whose own /Resources is res (resolved),
// inside sc. A unit without its own resources paints in sc (§8.10.1).
func (w *walker) child(sc *scope, res parse.Dict, tag string) *scope {
	if res == nil {
		return sc
	}
	id := w.scopeID(res)
	return &scope{res: res, parent: sc, key: sc.key + ">" + tag + strconv.Itoa(id), id: id}
}

// scopeID is a small id for a resource dictionary that is equal for
// dictionaries with equal contents (references compared as references): a
// unit walked in one page's resources is then memoized for another page whose
// own resource dictionary holds the same bindings (SF-1). Contents are
// fingerprinted once per dictionary object.
func (w *walker) scopeID(d parse.Dict) int {
	if id, ok := w.ids[dictID(d)]; ok {
		return id
	}
	w.nextScopeID++
	id := w.nextScopeID
	enc, ok := exactEncoding(d)
	if !ok {
		w.ids[dictID(d)] = id // too deep to compare: an id of its own
		return id
	}
	h := sha256.Sum256(enc)
	for _, f := range w.fingerprints[h] {
		if other, _ := exactEncoding(f.dict); bytes.Equal(other, enc) {
			w.nextScopeID--
			w.ids[dictID(d)] = f.id
			return f.id
		}
	}
	w.fingerprints[h] = append(w.fingerprints[h], fingerprint{dict: d, id: id})
	w.ids[dictID(d)] = id
	return id
}

// fingerprint is a resource dictionary with its scopeID; its encoding is
// recomputed on a hash hit rather than kept.
type fingerprint struct {
	dict parse.Dict
	id   int
}

// exactEncoding encodes o so that two values encode equally exactly when
// they bind the same objects: every value type-tagged, integers and reals
// apart (1 is not 1.0) and reals by their bits, strings and names by their
// bytes, references by number and generation, dictionary keys sorted. It
// reports false past a nesting bound (the value then compares unequal to all).
func exactEncoding(o parse.Object) ([]byte, bool) {
	var b bytes.Buffer
	ok := encodeExact(&b, o, 0)
	return b.Bytes(), ok
}

func encodeExact(b *bytes.Buffer, o parse.Object, depth int) bool {
	if depth > 64 {
		return false
	}
	bytesOf := func(tag byte, v []byte) {
		b.WriteByte(tag)
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.Write(v)
	}
	switch v := o.(type) {
	case nil, parse.Null:
		b.WriteByte('z')
	case parse.Boolean:
		if v {
			b.WriteString("b1")
		} else {
			b.WriteString("b0")
		}
	case parse.Integer:
		b.WriteString("i" + strconv.FormatInt(int64(v), 10) + ";")
	case parse.Real:
		b.WriteString("r" + strconv.FormatUint(math.Float64bits(float64(v)), 16) + ";")
	case parse.Name:
		bytesOf('n', []byte(v))
	case parse.String:
		bytesOf('s', v.Value)
	case parse.Reference:
		b.WriteString("R" + strconv.Itoa(v.Number) + "," + strconv.Itoa(v.Generation) + ";")
	case parse.Array:
		b.WriteByte('[')
		for _, e := range v {
			if !encodeExact(b, e, depth+1) {
				return false
			}
		}
		b.WriteByte(']')
	case parse.Dict:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		b.WriteByte('<')
		for _, k := range keys {
			bytesOf('n', []byte(k))
			if !encodeExact(b, v[parse.Name(k)], depth+1) {
				return false
			}
		}
		b.WriteByte('>')
	case *parse.Stream:
		b.WriteByte('S')
		if !encodeExact(b, v.Dict, depth+1) {
			return false
		}
		bytesOf('d', v.Raw)
	default:
		return false
	}
	return true
}

// resolveDict resolves obj to a dictionary (nil when it is not one).
func (w *walker) resolveDict(obj parse.Object) (parse.Dict, error) {
	if w.r == nil || obj == nil {
		return nil, nil
	}
	d, ok, err := w.r.ResolveDict(obj)
	if err != nil || !ok {
		return nil, err
	}
	return d, nil
}

// resolve resolves obj (nil stays nil).
func (w *walker) resolve(obj parse.Object) (parse.Object, error) {
	if w.r == nil || obj == nil {
		return obj, nil
	}
	return w.r.Resolve(obj)
}

// lookup returns every distinct object the scopes of sc bind name to in their
// category subdictionary (/XObject, /Font, …), innermost first.
func (w *walker) lookup(sc *scope, category, name parse.Name) ([]candidate, error) {
	var out []candidate
	seen := map[any]bool{}
	found := false
	for s := sc; s != nil; s = s.parent {
		if s.res == nil {
			continue
		}
		sub, err := w.resolveDict(s.res[category])
		if err != nil {
			return nil, err
		}
		raw, ok := sub[name]
		if !ok {
			continue
		}
		w.use(s, category, name, raw, !found) // the innermost binding conforms
		found = true
		obj, err := w.resolve(raw)
		if err != nil {
			return nil, err
		}
		num := 0
		if ref, ok := raw.(parse.Reference); ok {
			num = ref.Number
		}
		var id any
		switch v := obj.(type) {
		case *parse.Stream:
			id = v
		case parse.Dict:
			id = dictID(v)
		default:
			id = s // a value of another type: keep each binding
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, candidate{obj: obj, num: num, inner: s == sc})
		}
	}
	return out, nil
}

// colorSpaces is StrictOptions.InlineColorSpace for a unit painting in sc
// (C7). A name the innermost scope defines resolves there, as every reader
// does. A name only an outer scope defines is ambiguous — ISO readers find no
// colour space, falling-back readers find that one — so it resolves to
// nothing (the image is refused), except an abbreviation (G, RGB, CMYK): for
// those, being defined anywhere is what makes the image ambiguous, so they
// report the first definition and the scanner refuses the image. A family
// name (I, Indexed, …) never resolves.
func (w *walker) colorSpaces(sc *scope) func(parse.Name) (parse.Object, bool) {
	return func(name parse.Name) (parse.Object, bool) {
		if colorSpaceFamilies[name] {
			// A family name used alone is no colour space (Indexed needs its
			// parameters); a reader that does not look it up rejects the
			// image and may parse its data as content (T5).
			return nil, false
		}
		abbr := name == "G" || name == "RGB" || name == "CMYK"
		for s := sc; s != nil; s = s.parent {
			sub, err := w.resolveDict(s.res["ColorSpace"])
			if err != nil {
				return nil, false
			}
			raw, ok := sub[name]
			if ok {
				w.use(s, "ColorSpace", name, raw, true)
			}
			if !ok {
				if abbr {
					continue
				}
				// Absent from the innermost scope: ambiguous if any outer
				// scope has it, absent otherwise; nothing either way.
				return nil, false
			}
			return w.inlineColorSpace(raw)
		}
		return nil, false
	}
}

// colorSpaceFamilies are colour space family names (§8.6.3, Table 62) and the
// inline abbreviation of Indexed (Table 94), which colorSpaces never resolves
// as resource names.
var colorSpaceFamilies = map[parse.Name]bool{
	"I": true, "Indexed": true, "Pattern": true, "CalGray": true, "CalRGB": true,
	"Lab": true, "ICCBased": true, "Separation": true, "DeviceN": true,
}

// inlineColorSpace resolves a colour space resource for the inline-image
// allow-list: every element resolved, an Indexed lookup stream decoded to a
// String (StrictOptions.InlineColorSpace).
func (w *walker) inlineColorSpace(raw parse.Object) (parse.Object, bool) {
	obj, err := w.resolve(raw)
	if err != nil {
		return nil, false
	}
	switch v := obj.(type) {
	case parse.Name:
		return v, true
	case parse.Array:
		out := make(parse.Array, len(v))
		for i, e := range v {
			el, err := w.resolve(e)
			if err != nil {
				return nil, false
			}
			if st, ok := el.(*parse.Stream); ok {
				data, err := w.decode(st)
				if err != nil {
					return nil, false
				}
				el = parse.String{Value: data, Hex: true}
			}
			out[i] = el
		}
		return out, true
	}
	return nil, false
}

// pageScope returns the page's candidate scopes: its effective resources,
// then each /Pages ancestor's /Resources not already in the chain (§6.2).
func (w *walker) pageScope(pi parse.PageInfo) *scope {
	var chain []parse.Dict
	seen := map[uintptr]bool{}
	add := func(d parse.Dict) {
		if d == nil || seen[dictID(d)] {
			return
		}
		seen[dictID(d)] = true
		chain = append(chain, d)
	}
	add(pi.Resources)
	if w.r != nil {
		node := pi.Dict
		for depth := 0; depth < 64 && node != nil; depth++ {
			parent, err := w.resolveDict(node["Parent"])
			if err != nil || parent == nil {
				break
			}
			res, err := w.resolveDict(parent["Resources"])
			if err == nil {
				add(res)
			}
			node = parent
		}
	}
	// Link innermost first. Child scope keys extend the innermost key; the
	// chain is the same for every unit of the page. The key names the chain's
	// dictionaries, so a walker shared by several pages (Painter) memoizes a
	// unit walked in one page's scopes apart from another's.
	key := "p"
	for _, d := range chain {
		key += "," + strconv.Itoa(w.scopeID(d))
	}
	sc := &scope{key: key}
	for i := len(chain) - 1; i >= 0; i-- {
		sc = &scope{res: chain[i], parent: sc, key: key, id: w.scopeID(chain[i])}
	}
	return sc
}

// ownScopes returns the resource dictionaries of sc's chain that belong to the
// unit itself: for the page every scope in the chain; for a child unit with
// its own /Resources the innermost one; none for a child that paints in its
// caller's scope (the caller has walked those).
func ownScopes(sc *scope, kind unitKind, ownRes bool) []parse.Dict {
	if kind != unitPage {
		if !ownRes || sc.res == nil {
			return nil
		}
		return []parse.Dict{sc.res}
	}
	var out []parse.Dict
	for s := sc; s != nil; s = s.parent {
		if s.res != nil {
			out = append(out, s.res)
		}
	}
	return out
}

// walkUnit checks the child unit st of the given kind, invoked from a unit
// painting in sc; depth is st's own depth (the page is 0, its children 1). num
// is its object number (0 if unknown), for the refusal detail.
func (w *walker) walkUnit(st *parse.Stream, num int, kind unitKind, sc *scope, depth int) error {
	if w.path[st] {
		return nil // already being checked further up this chain
	}
	if depth > w.lim.depth {
		return refuse(ReasonBudgetExceeded, "nests a %s deeper than %d units", unitName(kind, num), w.lim.depth)
	}
	if kind == unitType3 {
		if w.glyphProcs++; w.glyphProcs > w.lim.glyphProcs {
			return budget("Type 3 glyph procedure")
		}
	} else if w.invocations++; w.invocations > w.lim.invocations {
		return budget("invocation")
	}
	res, err := w.resolveDict(st.Dict["Resources"])
	if err != nil {
		return err
	}
	usc := w.child(sc, res, "u")
	key := memoKey{st: st, kind: kind, scope: usc.key}
	if r, ok := w.memo[key]; ok {
		if verr := w.visit(1); verr != nil {
			return verr
		}
		w.replay(r)
		return r.err
	}
	r := w.collect(func() error { return w.scanUnit(st, num, kind, usc, res != nil, depth) })
	w.memo[key] = r
	return r.err
}

// scanUnit decodes, strictly scans and checks one child unit.
func (w *walker) scanUnit(st *parse.Stream, num int, kind unitKind, sc *scope, ownRes bool, depth int) error {
	data, err := w.decode(st)
	if err != nil {
		var re *Refusal
		if errors.As(err, &re) {
			return err
		}
		if w.lenient {
			return nil // like the renderer: a unit that does not decode paints nothing
		}
		return fmt.Errorf("%s: %w", unitName(kind, num), err)
	}
	spans, err := w.strictScan(unitName(kind, num), parse.ContentSegments{Data: data}, sc)
	if err != nil {
		return err
	}
	w.path[st] = true
	defer delete(w.path, st)
	it := &interp{w: w, kind: kind, num: num, st: st, sc: sc, ownRes: ownRes, depth: depth}
	return it.run(spans)
}

// unitName names a unit for a refusal or error detail.
func unitName(kind unitKind, num int) string {
	if num > 0 {
		return fmt.Sprintf("%s (object %d)", kind, num)
	}
	return kind.String()
}

// reachPatterns walks every tiling pattern the unit's own resources define,
// selected or not (§6.3: a reader's colour-space tracking may differ from
// ours, so reachability rather than selection decides).
func (w *walker) reachPatterns(sc *scope, kind unitKind, ownRes bool, depth int) error {
	if w.lenient {
		return nil // PaintedObjects counts only the patterns content selects
	}
	for _, res := range ownScopes(sc, kind, ownRes) {
		pats, err := w.resolveDict(res["Pattern"])
		if err != nil {
			return err
		}
		for _, raw := range pats {
			if err := w.pattern(raw, sc, depth); err != nil {
				return err
			}
		}
	}
	return nil
}

// pattern walks a pattern object if it is a tiling pattern (PatternType 1);
// shading patterns carry no content stream (§8.7.4).
func (w *walker) pattern(raw parse.Object, sc *scope, depth int) error {
	obj, err := w.resolve(raw)
	if err != nil {
		return err
	}
	st, ok := obj.(*parse.Stream)
	if !ok {
		return nil
	}
	pt, err := w.resolve(st.Dict["PatternType"])
	if err != nil {
		return err
	}
	if n, ok := pt.(parse.Integer); ok && n == 2 {
		return nil
	}
	num := 0
	if ref, ok := raw.(parse.Reference); ok {
		num = ref.Number
	}
	return w.walkUnit(st, num, unitPattern, sc, depth)
}

// xobject handles a Do of the candidate c. It classifies the XObject by its
// resolved /Subtype (§6.2): a form is walked; an image paints no content; a
// PostScript or other subtype is refused (decision D8). An XObject whose
// /Subtype is missing or not a Name is walked as a form unless it has /Width
// and /Height and no /BBox. Returns whether it was an image.
func (w *walker) xobject(c candidate, sc *scope, depth int) (image bool, err error) {
	st, ok := c.obj.(*parse.Stream)
	if !ok {
		return false, nil // nothing to paint
	}
	sub, err := w.resolve(st.Dict["Subtype"])
	if err != nil {
		return false, err
	}
	switch name, isName := sub.(parse.Name); {
	case isName && name == "Image":
		return true, nil
	case isName && name == "Form":
	case isName:
		if w.lenient {
			return false, nil
		}
		return false, refuse(ReasonUnsupportedXObject, "draws an XObject (object %d) of an unsupported subtype", c.num)
	default:
		_, hasW := st.Dict["Width"]
		_, hasH := st.Dict["Height"]
		_, hasBBox := st.Dict["BBox"]
		if hasW && hasH && !hasBBox {
			return true, nil
		}
	}
	return false, w.walkUnit(st, c.num, unitForm, sc, depth)
}

// xobjectBox is the page-space box an XObject candidate of a Do paints under
// ctm (§6.10): the unit square for an image (§8.9.5), the form's /BBox
// through its /Matrix for a form (§8.10.1). A form whose /BBox or /Matrix
// cannot be read is unbounded; a candidate that is not a stream paints
// nothing.
func (w *walker) xobjectBox(c candidate, image bool, ctm matrix) box4 {
	st, ok := c.obj.(*parse.Stream)
	if !ok {
		return emptyBox4()
	}
	if image {
		return placeBox(ctm, 0, 0, 1, 1)
	}
	bbox, ok := w.numbers(st.Dict["BBox"], 4)
	if !ok {
		return box4{unbounded: true}
	}
	m := identity()
	if raw, has := st.Dict["Matrix"]; has {
		vals, ok := w.numbers(raw, 6)
		if !ok {
			return box4{unbounded: true}
		}
		m = matrix(vals)
	}
	return placeBox(m.mul(ctm), min(bbox[0], bbox[2]), min(bbox[1], bbox[3]), max(bbox[0], bbox[2]), max(bbox[1], bbox[3]))
}

// numbers resolves obj to an array of exactly n numbers.
func (w *walker) numbers(obj parse.Object, n int) ([]float64, bool) {
	v, err := w.resolve(obj)
	if err != nil {
		return nil, false
	}
	arr, ok := v.(parse.Array)
	if !ok || len(arr) != n {
		return nil, false
	}
	out := make([]float64, n)
	for i, e := range arr {
		ev, err := w.resolve(e)
		if err != nil {
			return nil, false
		}
		switch x := ev.(type) {
		case parse.Integer:
			out[i] = float64(x)
		case parse.Real:
			out[i] = float64(x)
		default:
			return nil, false
		}
	}
	return out, true
}

// extGSParams are the parameters an ExtGState sets that the text model and
// the D5 geometry track: the font and size (§8.4.5, Table 58, N6), the line
// width /LW and miter limit /ML. A parameter present with a value that is not
// a number is +Inf (a reader may apply anything).
type extGSParams struct {
	font         parse.Object
	size         float64
	hasFont      bool
	lw, ml       float64
	hasLW, hasML bool
}

// extGState handles a gs of the candidate c: a soft mask's transparency group
// (/SMask /G, §11.6.5.2) is walked, a Type 3 /Font is checked, and the
// parameters the caller tracks are returned.
func (w *walker) extGState(c candidate, sc *scope, depth int) (extGSParams, error) {
	var out extGSParams
	d, ok := c.obj.(parse.Dict)
	if !ok {
		if st, isStream := c.obj.(*parse.Stream); isStream {
			d = st.Dict
		} else {
			return out, nil
		}
	}
	smask, err := w.resolveDict(d["SMask"])
	if err != nil {
		return out, err
	}
	if smask != nil {
		g, err := w.resolve(smask["G"])
		if err != nil {
			return out, err
		}
		if st, ok := g.(*parse.Stream); ok {
			num := 0
			if ref, ok := smask["G"].(parse.Reference); ok {
				num = ref.Number
			}
			if err := w.walkUnit(st, num, unitSoftMask, sc, depth); err != nil {
				return out, err
			}
		}
	}
	for _, p := range []struct {
		key parse.Name
		v   *float64
		has *bool
	}{{"LW", &out.lw, &out.hasLW}, {"ML", &out.ml, &out.hasML}} {
		raw, ok := d[p.key]
		if !ok {
			continue
		}
		v, err := w.resolve(raw)
		if err != nil {
			return out, err
		}
		*p.has = true
		switch n := v.(type) {
		case parse.Integer, parse.Real:
			*p.v = math.Abs(num(n))
		default:
			*p.v = math.Inf(1)
		}
	}
	fobj, err := w.resolve(d["Font"])
	if err != nil {
		return out, err
	}
	if arr, ok := fobj.(parse.Array); ok && len(arr) == 2 {
		szObj, err := w.resolve(arr[1])
		if err != nil {
			return out, err
		}
		if err := w.checkFont(arr[0], sc, depth); err != nil {
			return out, err
		}
		out.font, out.size, out.hasFont = arr[0], num(szObj), true
	}
	return out, nil
}

// checkFont checks a font selected by Tf or gs /Font: a Type 3 font's glyph
// procedures are units (§9.6.5) whose resources are the font's /Resources,
// then the invoking scopes. Every glyph procedure of the font is walked, not
// only those of codes shown (§6.3).
func (w *walker) checkFont(obj parse.Object, sc *scope, depth int) error {
	f := w.font(obj)
	if f == nil || f.Kind() != fontmodel.Type3 {
		return w.budgetErr
	}
	fd := f.Dict()
	res, err := w.resolveDict(fd["Resources"])
	if err != nil {
		return err
	}
	fsc := w.child(sc, res, "f")
	key := strconv.FormatUint(uint64(dictID(fd)), 16) + "@" + fsc.key
	if r, ok := w.type3[key]; ok {
		w.replay(r)
		return r.err
	}
	r := w.collect(func() error { return w.checkCharProcs(fd, fsc, depth) })
	w.type3[key] = r
	if r.err == nil {
		w.noteType3Advance(f)
	}
	return r.err
}

// noteType3Advance records the hull of 0 and the d0/d1 widths of a Type 3
// font's glyph procedures, in text space (type3Adv).
func (w *walker) noteType3Advance(f *fontmodel.Font) {
	id := dictID(f.Dict())
	adv := w.type3Adv[id]
	procs, err := w.resolveDict(f.Dict()["CharProcs"])
	if err != nil {
		return
	}
	for _, raw := range procs {
		obj, err := w.resolve(raw)
		if err != nil {
			continue
		}
		if st, ok := obj.(*parse.Stream); ok {
			if wx, ok := w.glyphWX[st]; ok {
				adv = adv.hull(point(f.GlyphToText(wx)))
			}
		}
	}
	w.type3Adv[id] = adv
}

// type3Advance widens adv by the d0/d1 advances of the Type 3 fonts of fs
// (adv unchanged when there are none).
func (w *walker) type3Advance(fs fontSet, adv iv) iv {
	for _, f := range fs.fonts() {
		if f.Kind() == fontmodel.Type3 {
			adv = adv.hull(w.type3Adv[dictID(f.Dict())])
		}
	}
	return adv
}

// checkCharProcs walks every /CharProcs entry of a Type 3 font.
func (w *walker) checkCharProcs(fd parse.Dict, sc *scope, depth int) error {
	procs, err := w.resolveDict(fd["CharProcs"])
	if err != nil {
		return err
	}
	for _, raw := range procs {
		obj, err := w.resolve(raw)
		if err != nil {
			return err
		}
		st, ok := obj.(*parse.Stream)
		if !ok {
			continue
		}
		num := 0
		if ref, ok := raw.(parse.Reference); ok {
			num = ref.Number
		}
		if err := w.walkUnit(st, num, unitType3, sc, depth); err != nil {
			return err
		}
	}
	return nil
}

// textKeys are the property-list entries that carry text a reader shows or
// copies (§14.9.3–5): replacement text, alternate description, expansion.
var textKeys = []parse.Name{"ActualText", "Alt", "E"}

// propertyLists returns the property lists a BDC or DP operand names: the
// inline dictionary, or every /Properties entry the scopes bind the name to.
func (w *walker) propertyLists(operand parse.Object, sc *scope) ([]parse.Dict, error) {
	switch v := operand.(type) {
	case parse.Dict:
		return []parse.Dict{v}, nil
	case parse.Name:
		cands, err := w.lookup(sc, "Properties", v)
		if err != nil {
			return nil, err
		}
		var out []parse.Dict
		for _, c := range cands {
			switch d := c.obj.(type) {
			case parse.Dict:
				out = append(out, d)
			case *parse.Stream:
				out = append(out, d.Dict)
			}
		}
		return out, nil
	}
	return nil, nil
}

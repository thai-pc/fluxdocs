package redact

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

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
//
// Interim plumbing with no compatibility guarantee: step 1.8 replaces Reason
// and RefusalError with core.RedactionError.
type Reason string

// The refusal reasons the walker reports.
const (
	ReasonFormText           Reason = "form_text"
	ReasonPatternText        Reason = "pattern_text"
	ReasonSoftMaskText       Reason = "softmask_text"
	ReasonType3Text          Reason = "type3_text"
	ReasonBudgetExceeded     Reason = "budget_exceeded"
	ReasonUnsupportedXObject Reason = "unsupported_xobject"
)

// RefusalError reports content that redaction cannot handle safely yet: a
// route that shows text redaction does not rewrite, an XObject of an
// unsupported subtype, or a page past a redaction budget. Detail describes
// where, by route and object number, and never holds document text. A
// form_text or unsupported_xobject refusal matches ErrFormXObjectText with
// errors.Is.
//
// Interim plumbing with no compatibility guarantee: step 1.8 replaces it with
// core.RedactionError (ErrFormXObjectText stays reachable one more release).
type RefusalError struct {
	Reason Reason
	Detail string
}

// Error returns the detail and what is not supported.
func (e *RefusalError) Error() string {
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

// Is reports whether target is ErrFormXObjectText for a form_text or
// unsupported_xobject refusal (the error those refusals had before they had
// reasons).
func (e *RefusalError) Is(target error) bool {
	return target == ErrFormXObjectText && (e.Reason == ReasonFormText || e.Reason == ReasonUnsupportedXObject)
}

// Walk budgets per page call (§6.4). Hitting any of them is a refusal.
const (
	maxDecodedBytes = 512 << 20 // content decoded: page, forms, groups, cells, glyph procedures, font streams
	maxOpVisits     = 64 << 20  // operators interpreted, plus one per memoized revisit
	maxUnitDepth    = 12        // nesting of units below the page (render/form.go)
	maxInvocations  = 4096      // form, soft-mask group and pattern invocations
	maxGlyphProcs   = 1 << 20   // Type 3 glyph procedures walked
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
	memo    map[memoKey]error        // walk results
	type3   map[string]error         // Type 3 font checks, by font and scope chain
	path    map[*parse.Stream]bool   // units on the current invocation chain
	fonts   map[uintptr]*fontmodel.Font
	ids     map[uintptr]int // dictionary identity -> small id, for scope keys

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
		memo:    map[memoKey]error{},
		type3:   map[string]error{},
		path:    map[*parse.Stream]bool{},
		fonts:   map[uintptr]*fontmodel.Font{},
		ids:     map[uintptr]int{},

		glyphWX:  map[*parse.Stream]float64{},
		type3Adv: map[uintptr]iv{},
	}
}

// refuse returns a refusal.
func refuse(reason Reason, format string, args ...any) error {
	return &RefusalError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
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
	w.content[st] = data
	return data, nil
}

// decodeFont is fontmodel's Decode for this call: font streams count against
// the decode budget too. A budget hit is kept, and the page is refused.
func (w *walker) decodeFont(st *parse.Stream) ([]byte, error) {
	data, err := w.decode(st)
	var re *RefusalError
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

// dictID is a dictionary's identity. The Resolver caches parsed objects, so
// one object always resolves to the same map.
func dictID(d parse.Dict) uintptr { return reflect.ValueOf(d).Pointer() }

// child returns the scope of a unit whose own /Resources is res (resolved),
// inside sc. A unit without its own resources paints in sc (§8.10.1).
func (w *walker) child(sc *scope, res parse.Dict, tag string) *scope {
	if res == nil {
		return sc
	}
	id, ok := w.ids[dictID(res)]
	if !ok {
		id = len(w.ids) + 1
		w.ids[dictID(res)] = id
	}
	return &scope{res: res, parent: sc, key: sc.key + ">" + tag + strconv.Itoa(id)}
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
	// chain is the same for every unit of the page.
	sc := &scope{key: "p"}
	for i := len(chain) - 1; i >= 0; i-- {
		sc = &scope{res: chain[i], parent: sc, key: "p"}
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
	if err, ok := w.memo[key]; ok {
		if verr := w.visit(1); verr != nil {
			return verr
		}
		return err
	}
	err = w.scanUnit(st, num, kind, usc, res != nil, depth)
	w.memo[key] = err
	return err
}

// scanUnit decodes, strictly scans and checks one child unit.
func (w *walker) scanUnit(st *parse.Stream, num int, kind unitKind, sc *scope, ownRes bool, depth int) error {
	data, err := w.decode(st)
	if err != nil {
		var re *RefusalError
		if errors.As(err, &re) {
			return err
		}
		return fmt.Errorf("%s: %w", unitName(kind, num), err)
	}
	spans, err := parse.ScanContentStrict(parse.ContentSegments{Data: data},
		parse.StrictOptions{InlineColorSpace: w.colorSpaces(sc)})
	if err != nil {
		return fmt.Errorf("%s: %w", unitName(kind, num), err)
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

// extGState handles a gs of the candidate c: a soft mask's transparency group
// (/SMask /G, §11.6.5.2) is walked, and a /Font entry (§8.4.5, Table 58) is
// returned so the caller can select it; a Type 3 font is checked.
func (w *walker) extGState(c candidate, sc *scope, depth int) (font parse.Object, size float64, hasFont bool, err error) {
	d, ok := c.obj.(parse.Dict)
	if !ok {
		if st, isStream := c.obj.(*parse.Stream); isStream {
			d = st.Dict
		} else {
			return nil, 0, false, nil
		}
	}
	smask, err := w.resolveDict(d["SMask"])
	if err != nil {
		return nil, 0, false, err
	}
	if smask != nil {
		g, err := w.resolve(smask["G"])
		if err != nil {
			return nil, 0, false, err
		}
		if st, ok := g.(*parse.Stream); ok {
			num := 0
			if ref, ok := smask["G"].(parse.Reference); ok {
				num = ref.Number
			}
			if err := w.walkUnit(st, num, unitSoftMask, sc, depth); err != nil {
				return nil, 0, false, err
			}
		}
	}
	fobj, err := w.resolve(d["Font"])
	if err != nil {
		return nil, 0, false, err
	}
	if arr, ok := fobj.(parse.Array); ok && len(arr) == 2 {
		szObj, err := w.resolve(arr[1])
		if err != nil {
			return nil, 0, false, err
		}
		if err := w.checkFont(arr[0], sc, depth); err != nil {
			return nil, 0, false, err
		}
		return arr[0], num(szObj), true, nil
	}
	return nil, 0, false, nil
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
	if err, ok := w.type3[key]; ok {
		return err
	}
	err = w.checkCharProcs(fd, fsc, depth)
	w.type3[key] = err
	if err == nil {
		w.noteType3Advance(f)
	}
	return err
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

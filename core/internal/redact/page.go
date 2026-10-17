package redact

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// page.go is the page-level API core uses: AnalyzePage builds a page's text
// model once its content is known to be safe to rewrite; Match plans the
// removals for a RedactText pattern (span marks, docs/design/
// redaction-hardening.md §5.4, §6.5, §6.7); Rewrite removes them, together
// with everything under a region (§6.10) and every property-list text key
// (§6.7, D7), and garbage-collects the page's resources (§5.5).

// ErrMarkMismatch is returned by Rewrite for a mark that does not name an
// operator of the page: a mark made on another page or document. Core
// reports it as an internal error.
var ErrMarkMismatch = errors.New("redaction mark does not belong to this page")

// Page is the redaction model of one page's content: its operators, its text
// runs, its marked-content property lists, and the resources they were read
// with. It is built by AnalyzePage and is read-only afterwards.
type Page struct {
	id      pageKey // the page's identity (see Mark)
	r       *parse.Resolver
	content []byte
	spans   []parse.ContentOpSpan
	runs    []textRun
	props   []propList
	items   []paintItem // what the page paints besides text (§6.10)
	states  []stateOp   // state-setting operators that name a resource (stateNode)
	// uses are the resources looked up by operators Rewrite never removes,
	// and by the units they invoke; an item's own lookups are in the item.
	uses useSet
	res  parse.Dict // the page's effective /Resources
	// resID is the scopeID of res in the walk that built the page: the
	// lookups whose conforming binding is the page's own resources carry it.
	resID int
	box   Rect // the page's MediaBox
}

// Mark is a planned change to one operator of a page's content that applies
// whatever the geometry (a span mark, §5.4): remove a text-showing operator,
// or strip a text key from a BDC/DP property list. Marks are produced by
// Match and are valid only for the page they were made on: a Mark records the
// page's document and object number (so the same page analysed again, or
// after Rotate clones its dictionary, has the same identity), and Rewrite
// refuses a mark from any other page.
type Mark struct {
	page       pageKey
	start, end int
	strip      parse.Name // the property-list key to strip; "" removes the operator
}

// Match is what one pattern matched on a page.
type Match struct {
	// Rects are the display boxes of the matched runs, and of the runs inside
	// each matched marked-content sequence (their union per sequence): where
	// RedactAndFlatten paints the black box. They are not removal regions —
	// Marks remove the matched runs — so they use the em vertical extent, not
	// the /FontBBox (textRun.disp). A run whose box is not finite is given the
	// page's MediaBox.
	Rects []Rect
	// Marks are the span marks to apply whatever the geometry.
	Marks []Mark
	// Needles are the matched substrings, for automatic plan extension
	// (§6.11, step 1.13). They are document text: never log them.
	Needles []string
}

// AnalyzePage builds the redaction model of a page. content is the page's
// content as r.PageContentSegments assembles it; pi gives its resources.
//
// It scans the content with the strict scanner (inline-image colour space
// names resolved through the page's resource scopes) and walks every route
// to another content stream (walk.go). Returns a *Refusal if the page content
// or a child unit cannot be read the way every reader reads it
// (content_ambiguous, inline_image), a child unit shows text, an XObject has
// an unsupported subtype or a budget is exceeded; a resolve or decode error
// for a file that is broken; the model otherwise.
func AnalyzePage(r *parse.Resolver, pi parse.PageInfo, content parse.ContentSegments) (*Page, error) {
	return analyzePage(newWalker(r), pi, content)
}

// analyzePage is AnalyzePage with the walker (and its budgets) given.
func analyzePage(w *walker, pi parse.PageInfo, content parse.ContentSegments) (*Page, error) {
	r := w.r
	if err := w.charge(int64(len(content.Data))); err != nil {
		return nil, err
	}
	sc := w.pageScope(pi)
	spans, err := w.strictScan("page content", content, sc)
	if err != nil {
		return nil, err
	}
	mb := pi.MediaBox
	resID := -1
	if pi.Resources != nil {
		resID = sc.id
	}
	p := &Page{
		resID:   resID,
		id:      pageID(r, pi),
		r:       r,
		content: content.Data,
		spans:   spans,
		uses:    useSet{},
		res:     pi.Resources,
		box: Rect{X: min(mb.LLX, mb.URX), Y: min(mb.LLY, mb.URY),
			Width: math.Abs(mb.URX - mb.LLX), Height: math.Abs(mb.URY - mb.LLY)},
	}
	it := &interp{w: w, kind: unitPage, sc: sc, page: p}
	if err := it.run(spans); err != nil {
		return nil, err
	}
	return p, nil
}

// Painter finds what pages that are not redacted paint (PaintedObjects), so
// core can keep the objects they share with removed content (B3). One Painter
// serves one RedactAndFlatten call. Its walker's memo is shared by every page
// it walks, so pages that share a unit — with the same bindings — pay for it
// once. Each page gets the per-page redaction budgets; the call is bounded by
// maxPainterOps operator visits and maxPainterDecoded bytes of decoded unit
// content, and holds at most painterCacheLimit of it for the memo.
type Painter struct {
	w   *walker
	ops int64 // operator visits of the pages walked before the current one
}

// Call-level bounds of a Painter (C47). Measured (native, one call, pages
// each painting a distinct unit): an operator visit costs 0.1 µs (`n`) to
// 0.65 µs (`0 0 1 1 re f`, dominated by the lenient scan): the worst content
// refuses after 15.5 s at 32 Mi, against 44 s at 64 Mi. Ordinary documents
// need far less: 200 pages of 50 k operators each are 20 M visits (5.7 s);
// the largest corpus page needs 0.56 Mi. Decoded unit content is kept for
// the memo up to painterCacheLimit (past it a unit is decoded again,
// charged), and all unit decoding in the call is capped.
const (
	maxPainterOps     = 32 << 20
	maxPainterDecoded = 1 << 30
	painterCacheLimit = 256 << 20
)

// Charge counts n units of the caller's own work (core's reachability
// passes) against the call's operator budget. Returns a *Refusal
// (budget_exceeded) once the call is past maxPainterOps.
func (pt *Painter) Charge(n int64) error {
	pt.ops += n
	if pt.ops+pt.w.ops > maxPainterOps {
		return budget("operator")
	}
	return nil
}

// NewPainter returns a Painter reading r.
func NewPainter(r *parse.Resolver) *Painter {
	w := newWalker(r)
	w.lenient = true
	w.cacheLimit = painterCacheLimit
	return &Painter{w: w}
}

// PaintedObjects returns the indirect objects a page paints with, read
// leniently as the renderer reads them: what every name its content uses binds
// to in any candidate scope — through forms, selected tiling patterns,
// soft-mask groups and Type 3 glyph procedures, the default colour spaces of
// every scope, and the named colour spaces of inline images — and the same for
// each appearance stream of its annotations (/AP /N, /R, /D, every state),
// plus those streams themselves. Values are leaf objects: the resource
// dictionaries that hold them are not included.
//
// Returns a *Refusal (budget_exceeded) once the Painter's budget is spent, or
// a resolve error; content that does not decode paints nothing.
func (pt *Painter) PaintedObjects(pi parse.PageInfo) ([]int, error) {
	w := pt.w
	r := w.r
	// A fresh per-page budget; the call's totals carry over.
	pt.ops += w.ops
	w.ops, w.decoded, w.invocations, w.glyphProcs = 0, 0, 0, 0
	if err := pt.overCall(); err != nil {
		return nil, err
	}
	data, err := r.PageContent(pi)
	if errors.Is(err, parse.ErrDecodeLimit) {
		return nil, budget("decode")
	}
	if err != nil {
		data = nil // like the renderer: content that does not decode paints nothing
	}
	if err := w.charge(int64(len(data))); err != nil {
		return nil, err
	}
	sc := w.pageScope(pi)
	spans, err := w.strictScan("page content", parse.ContentSegments{Data: data}, sc)
	if err != nil {
		return nil, err
	}
	uses := useSet{}
	w.uses = uses
	defer func() { w.uses = nil }()
	it := &interp{w: w, kind: unitPage, sc: sc}
	if err := it.run(spans); err != nil {
		return nil, err
	}
	if err := pt.appearances(pi, sc); err != nil {
		return nil, err
	}
	if err := pt.overCall(); err != nil {
		return nil, err
	}
	return uses.conforming(), nil
}

// overCall refuses once the call is past its operator or decode budget.
func (pt *Painter) overCall() error {
	if pt.ops+pt.w.ops > maxPainterOps {
		return budget("operator")
	}
	if pt.w.unitDecoded > maxPainterDecoded {
		return budget("decode")
	}
	return nil
}

// appearances walks the appearance streams of a page's annotations as forms
// painted in the page's scopes, recording each stream.
func (pt *Painter) appearances(pi parse.PageInfo, sc *scope) error {
	w := pt.w
	annots, err := w.resolve(pi.Dict["Annots"])
	if err != nil {
		return err
	}
	arr, _ := annots.(parse.Array)
	for _, a := range arr {
		ad, err := w.resolveDict(a)
		if err != nil || ad == nil {
			continue
		}
		ap, err := w.resolveDict(ad["AP"])
		if err != nil || ap == nil {
			continue
		}
		for _, k := range [...]parse.Name{"N", "R", "D"} {
			var streams []parse.Object
			v, err := w.resolve(ap[k])
			if err != nil {
				continue
			}
			switch x := v.(type) {
			case *parse.Stream:
				streams = append(streams, ap[k])
			case parse.Dict: // appearance states
				for _, st := range x {
					streams = append(streams, st)
				}
			}
			for _, raw := range streams {
				obj, err := w.resolve(raw)
				if err != nil {
					continue
				}
				st, ok := obj.(*parse.Stream)
				if !ok {
					continue
				}
				if w.uses != nil {
					addRefs(w.uses, raw, 0, func(n int) any { return confObj(n) })
				}
				num := 0
				if ref, ok := raw.(parse.Reference); ok {
					num = ref.Number
				}
				if err := w.walkUnit(st, num, unitForm, sc, 1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// pageKey identifies a page: its document (the Resolver) and its object
// number, or for a page without one the identity of its dictionary.
type pageKey struct {
	doc  *parse.Resolver
	num  int
	dict uintptr
}

// pageID is a page's identity: its document and object number, which
// survive a Rotate (it clones the page dictionary), or the dictionary's
// identity when the page has no number; zero for a bare content stream.
func pageID(r *parse.Resolver, pi parse.PageInfo) pageKey {
	if n := pi.ObjectNumber(); n > 0 {
		return pageKey{doc: r, num: n}
	}
	if pi.Dict == nil {
		return pageKey{doc: r}
	}
	return pageKey{doc: r, dict: dictID(pi.Dict)}
}

// Match plans the removal of every text that pattern matches on the page
// (§6.5, D12, D14):
//   - a run (one text-showing operator; a TJ's strings at every nesting depth
//     concatenated) whose text, in any of its readings, matches: the run is
//     span-marked and its box returned;
//   - a BDC or DP property list whose /ActualText, /Alt or /E text matches:
//     that key is stripped (a mark), every run inside the BDC…EMC sequence is
//     span-marked, and the union of their boxes is returned. A matched list
//     marks the page even when the sequence holds no runs.
//
// An empty text never matches.
func (p *Page) Match(pattern *regexp.Regexp) Match {
	var m Match
	for _, r := range p.runs {
		matched := false
		for _, t := range r.texts {
			if t == "" || !pattern.MatchString(t) {
				continue
			}
			matched = true
			m.Needles = appendNeedles(m.Needles, pattern, t)
		}
		if matched {
			m.Marks = append(m.Marks, Mark{page: p.id, start: r.start, end: r.end})
			m.Rects = append(m.Rects, p.runRect(r))
		}
	}
	for _, pl := range p.props {
		matched := false
		for _, k := range textKeys {
			hit := false
			for _, t := range pl.texts[k] {
				if t != "" && pattern.MatchString(t) {
					hit = true
					m.Needles = appendNeedles(m.Needles, pattern, t)
				}
			}
			if hit {
				matched = true
				m.Marks = append(m.Marks, Mark{page: p.id, start: pl.start, end: pl.end, strip: k})
			}
		}
		if !matched || pl.endRun <= pl.firstRun {
			continue
		}
		var union Rect
		for i, r := range p.runs[pl.firstRun:pl.endRun] {
			m.Marks = append(m.Marks, Mark{page: p.id, start: r.start, end: r.end})
			if rc := p.runRect(r); i == 0 {
				union = rc
			} else {
				union = unionRect(union, rc)
			}
		}
		m.Rects = append(m.Rects, union)
	}
	return m
}

// appendNeedles appends every non-empty substring of t that pattern matches.
func appendNeedles(dst []string, pattern *regexp.Regexp, t string) []string {
	for _, s := range pattern.FindAllString(t, -1) {
		if s != "" {
			dst = append(dst, s)
		}
	}
	return dst
}

// runRect is a run's display box as a Rect, or the page's MediaBox when the
// run is not finite.
func (p *Page) runRect(r textRun) Rect {
	if r.nonFinite {
		return p.box
	}
	return Rect{X: r.disp[0], Y: r.disp[1], Width: r.disp[2] - r.disp[0], Height: r.disp[3] - r.disp[1]}
}

// unionRect is the smallest Rect covering a and b.
func unionRect(a, b Rect) Rect {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.Width, b.X+b.Width), max(a.Y+a.Height, b.Y+b.Height)
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

// intersects reports whether a run meets a region (§6.5): closed intervals,
// so a run that only touches the region, or that has collapsed to a point or
// a segment, meets it; a run whose box is not finite meets every region.
// rc must be normalized (non-negative size).
func (r textRun) intersects(rc Rect) bool {
	if r.nonFinite {
		return true
	}
	return r.box[0] <= rc.X+rc.Width && r.box[2] >= rc.X &&
		r.box[1] <= rc.Y+rc.Height && r.box[3] >= rc.Y
}

// normalize returns r with a negative width or height flipped.
func normalize(r Rect) Rect {
	if r.Width < 0 {
		r.X, r.Width = r.X+r.Width, -r.Width
	}
	if r.Height < 0 {
		r.Y, r.Height = r.Y+r.Height, -r.Height
	}
	return r
}

// Output is a rewritten page.
type Output struct {
	// Content is the page's new content.
	Content []byte
	// Resources is the page's new /Resources, a page-private dictionary to
	// write instead of its effective resources: only the entries the
	// surviving content can use (resource GC, §5.5), with every property-list
	// text key stripped. Values other than the stripped lists are the
	// original objects (references stay references).
	Resources parse.Dict
	// Stripped are the /ActualText, /Alt and /E values stripped from the
	// page's property lists, for verification (§5.6 (d)). They are document
	// text: never log them.
	Stripped []string
	// Used are the indirect objects the surviving content looks up: the leaf
	// values its names bind to, at every depth of the units it invokes (not
	// the resource dictionaries holding them).
	Used []int
	// Removed are the indirect objects that only removed content looked up
	// (a removed image, form, pattern or soft-mask ExtGState, and references
	// inside a direct resource value): what D5 removed. They and what only
	// they reach must not be written unless content kept elsewhere paints
	// them (core denies the rest build-wide).
	Removed []int
}

// Rewrite returns the page with:
//   - every run a mark names, or whose box meets any of regions (normalized
//     first), removed;
//   - everything else it paints that meets a region removed (§6.10, D5): an
//     image XObject or a text-free form (Do), an inline image (BI … EI), a
//     painted path (its construction operators and painting operator; a path
//     that also clips keeps its clip, its painting operator replaced by n),
//     and a shading (sh) whose clip meets the region or is unknown;
//   - the /ActualText, /Alt and /E entries of every marked-content property
//     list stripped (D7, D14): an inline list re-emits its BDC/DP operator
//     without them, a named one gets a page-private /Properties entry;
//   - a solid black rectangle over each of boxes (display only);
//   - page-private resources holding only what the surviving content can use.
//
// Everything else is byte-identical (I7), except that a removed ' or " keeps
// its line move (and " its spacing): it is replaced by T* (aw Tw ac Tc T*),
// so later text keeps its place.
//
// Returns an error wrapping ErrMarkMismatch, and no output, if a mark was not
// made on this page; or a resolve error for the resources.
func (p *Page) Rewrite(regions, boxes []Rect, marks []Mark) (Output, error) {
	// remove marks the spans to remove (a bitset: a hostile page may have
	// millions); with holds the replacements other than removal.
	remove := make([]uint64, (len(p.spans)+63)/64)
	del := func(i int) { remove[i/64] |= 1 << (i % 64) }
	with := map[int][]byte{}
	for _, mk := range marks {
		if mk.page != p.id {
			return Output{}, fmt.Errorf("%w: made on another page", ErrMarkMismatch)
		}
		i, ok := p.spanAt(mk.start, mk.end)
		if mk.strip != "" {
			// Every text key of every list is stripped below (D7); the mark
			// only has to name a list of this page.
			if !ok || !p.isPropList(i) {
				return Output{}, fmt.Errorf("%w: it names no property list", ErrMarkMismatch)
			}
			continue
		}
		if !ok {
			return Output{}, fmt.Errorf("%w: it names no operator", ErrMarkMismatch)
		}
		del(i)
	}
	norm := make([]Rect, len(regions))
	for i, rc := range regions {
		norm[i] = normalize(rc)
	}
	for _, r := range p.runs {
		for _, rc := range norm {
			if r.intersects(rc) {
				del(r.span)
				break
			}
		}
	}
	isDel := func(i int) bool { return remove[i/64]&(1<<(i%64)) != 0 }
	// keep marks the state-setting operators a surviving paint consumes.
	keep := make([]bool, len(p.states))
	consume := func(c consumes) {
		for _, n := range [...]*stateNode{c.fill, c.stroke, c.gs} {
			for ; n != nil && !keep[n.op]; n = n.parent {
				keep[n.op] = true
			}
		}
	}
	for _, r := range p.runs {
		if !isDel(r.span) {
			consume(r.use)
		}
	}
	used, dropped := useSet{}, useSet{}
	used.add(p.uses)
	for _, it := range p.items {
		if !it.meetsAny(norm) {
			used.add(it.uses)
			consume(it.use)
			continue
		}
		dropped.add(it.uses)
		switch {
		case it.kind == itemPath && it.clip:
			// The clip (W/W* before the painting operator) needs its path:
			// only the painting stops.
			with[it.last] = []byte(" n ")
		case it.kind == itemPath:
			for i := it.first; i <= it.last; i++ {
				if pathConstruction[p.spans[i].Op] || i == it.last {
					del(i)
				}
			}
		default:
			del(it.first)
		}
	}
	// A state-setting operator no surviving paint consumes goes, with what
	// it names: a pattern or soft mask painted only under a region must not
	// survive through its colour or ExtGState (B3). One that names nothing
	// is left as it is.
	for i, st := range p.states {
		switch {
		case keep[i]:
			used.add(st.uses)
		case len(st.uses) > 0:
			del(st.span)
			dropped.add(st.uses)
		}
	}
	var stripped []string
	named := map[parse.Name]bool{}
	for _, pl := range p.props {
		for _, k := range textKeys {
			stripped = append(stripped, pl.texts[k]...)
		}
		if pl.inline == nil {
			named[pl.name] = true
		} else if hasTextKey(pl.inline) {
			with[pl.span] = reemitProps(pl)
		}
	}

	// One pass over the spans, in content order: every byte outside a
	// removed or replaced span is copied as it is.
	var b bytes.Buffer
	b.Grow(len(p.content))
	prev := 0
	for i, s := range p.spans {
		r, replaced := with[i]
		if remove[i/64]&(1<<(i%64)) == 0 && !replaced {
			continue
		}
		if !replaced || remove[i/64]&(1<<(i%64)) != 0 {
			r = p.removal(s) // removal wins over a replacement
		}
		b.Write(p.content[prev:s.Start])
		b.Write(r)
		prev = s.End
	}
	b.Write(p.content[prev:])
	for _, r := range boxes {
		writeBlackRect(&b, r)
	}

	res, err := p.privateResources(used, named)
	if err != nil {
		return Output{}, err
	}
	// Objects only removed content looked up — through any candidate
	// binding — and that no conforming binding of kept content reaches: core
	// denies them build-wide unless content kept elsewhere uses them.
	var removed []int
	for _, n := range dropped.objects() {
		if _, ok := used[confObj(n)]; !ok {
			removed = append(removed, n)
		}
	}
	sort.Ints(removed)
	kept := used.conforming()
	sort.Ints(kept)
	return Output{Content: b.Bytes(), Resources: res, Stripped: stripped, Used: kept, Removed: removed}, nil
}

// spanAt returns the index of the span [start, end), if the page has one.
// Spans are in content order and do not overlap.
func (p *Page) spanAt(start, end int) (int, bool) {
	i := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].Start >= start })
	if i < len(p.spans) && p.spans[i].Start == start && p.spans[i].End == end {
		return i, true
	}
	return 0, false
}

// isPropList reports whether span i is a BDC or DP operator with a property
// list the model recorded.
func (p *Page) isPropList(i int) bool {
	j := sort.Search(len(p.props), func(j int) bool { return p.props[j].span >= i })
	return j < len(p.props) && p.props[j].span == i
}

// pathConstruction are the path construction operators (§8.5.2).
var pathConstruction = map[string]bool{"m": true, "l": true, "c": true, "v": true, "y": true, "h": true, "re": true}

// removal is what replaces a removed text-showing operator: a space, or for
// the quote operators what they do besides showing text (§9.4.3): ' moves to
// the next line (T*), and " also sets word and character spacing, its first
// two operands copied byte for byte.
func (p *Page) removal(s parse.ContentOpSpan) []byte {
	switch s.Op {
	case "'":
		return []byte(" T* ")
	case "\"":
		toks := leadingTokens(p.content[s.Start:s.End], 2)
		if len(toks) == 2 {
			return []byte(" " + toks[0] + " Tw " + toks[1] + " Tc T* ")
		}
		// Unreachable for strictly scanned content (two numbers first).
		return []byte(" " + formatNum(num(s.Operands[0])) + " Tw " + formatNum(num(s.Operands[1])) + " Tc T* ")
	}
	return []byte(" ")
}

// leadingTokens returns the first n regular-character tokens of b, skipping
// white-space and comments: the bytes of numeric operands as written.
func leadingTokens(b []byte, n int) []string {
	var out []string
	for i := 0; i < len(b) && len(out) < n; {
		c := b[i]
		switch {
		case c == '%':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
		case isSpaceByte(c):
			i++
		case isDelimiterByte(c):
			return out // a number cannot start with a delimiter
		default:
			j := i
			for j < len(b) && !isSpaceByte(b[j]) && !isDelimiterByte(b[j]) {
				j++
			}
			out = append(out, string(b[i:j]))
			i = j
		}
	}
	return out
}

// isSpaceByte and isDelimiterByte are the PDF white-space and delimiter
// characters (§7.2.2, Tables 1 and 2).
func isSpaceByte(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isDelimiterByte(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// reemitProps serializes a BDC/DP operator with its inline property list
// minus its text keys (textKeys). Strings are written in hex form, so their
// bytes round-trip exactly.
func reemitProps(pl propList) []byte {
	d := make(parse.Dict, len(pl.inline))
	for k, v := range pl.inline {
		d[k] = hexStrings(v)
	}
	for _, k := range textKeys {
		delete(d, k)
	}
	var b bytes.Buffer
	b.WriteByte(' ')
	b.Write(parse.SerializeObject(pl.tag))
	b.WriteByte(' ')
	b.Write(parse.SerializeObject(d))
	b.WriteByte(' ')
	b.WriteString(pl.op)
	b.WriteByte(' ')
	return b.Bytes()
}

// hasTextKey reports whether a property list has any of textKeys.
func hasTextKey(d parse.Dict) bool {
	for _, k := range textKeys {
		if _, ok := d[k]; ok {
			return true
		}
	}
	return false
}

// hexStrings returns o with every string, at any depth, in hex form.
func hexStrings(o parse.Object) parse.Object {
	switch v := o.(type) {
	case parse.String:
		return parse.String{Value: v.Value, Hex: true}
	case parse.Array:
		out := make(parse.Array, len(v))
		for i, e := range v {
			out[i] = hexStrings(e)
		}
		return out
	case parse.Dict:
		out := make(parse.Dict, len(v))
		for k, e := range v {
			out[k] = hexStrings(e)
		}
		return out
	}
	return o
}

// resourceCategories are the /Resources entries that bind names (ISO
// 32000-1 Table 33); resource GC keeps the used names of each.
var resourceCategories = []parse.Name{"ExtGState", "ColorSpace", "Pattern", "Shading", "XObject", "Font", "Properties"}

// defaultColorSpaces apply whenever content paints in a device colour space
// (§8.6.5.6), so GC keeps them without a name lookup.
var defaultColorSpaces = map[parse.Name]bool{"DefaultGray": true, "DefaultRGB": true, "DefaultCMYK": true}

// privateResources returns the page's page-private /Resources (resource GC,
// §5.5): of each category (resourceCategories), only the names in used and,
// for /ColorSpace, the default colour spaces; /ProcSet when it is an array of
// names; nothing else. Each /Properties entry in named loses its text keys
// (a copy; the original is not modified). Only the page's effective
// resources are written to the output, so an entry found only in a /Pages
// ancestor's resources is not copied at all.
//
// Returns an error if a category dictionary or a named /Properties entry
// cannot be resolved.
func (p *Page) privateResources(used useSet, named map[parse.Name]bool) (parse.Dict, error) {
	w := newWalker(p.r)
	out := parse.Dict{}
	for _, cat := range resourceCategories {
		sub, err := w.resolveDict(p.res[cat])
		if err != nil {
			return nil, err
		}
		kept := parse.Dict{}
		for name, v := range sub {
			if _, ok := used[resKey{p.resID, cat, name}]; !ok && !(cat == "ColorSpace" && defaultColorSpaces[name]) {
				continue
			}
			if cat == "Properties" && named[name] {
				entry, err := w.resolveDict(v)
				if err != nil {
					return nil, err
				}
				if entry != nil && hasTextKey(entry) {
					e := make(parse.Dict, len(entry))
					for k, ev := range entry {
						e[k] = ev
					}
					for _, k := range textKeys {
						delete(e, k)
					}
					v = e
				}
			}
			kept[name] = v
		}
		if len(kept) > 0 {
			out[cat] = kept
		}
	}
	if ps, err := w.resolve(p.res["ProcSet"]); err == nil {
		if arr, ok := ps.(parse.Array); ok && allNames(arr) {
			out["ProcSet"] = arr
		}
	}
	return out, nil
}

// allNames reports whether every element of a is a name.
func allNames(a parse.Array) bool {
	for _, e := range a {
		if _, ok := e.(parse.Name); !ok {
			return false
		}
	}
	return true
}

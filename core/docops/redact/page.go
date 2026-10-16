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
// redaction-hardening.md §5.4, §6.5, §6.7); Rewrite removes them.
//
// This engine API (AnalyzePage, Page, Match, Mark, Rewrite, RefusalError,
// Reason) is interim plumbing between core and this package, with no
// compatibility guarantee: step 1.8 moves the engine to core/internal/redact.

// ErrMarkMismatch is returned by Rewrite for a mark that does not name an
// operator of the page: a mark made on another page or document. Core
// reports it as an internal error.
var ErrMarkMismatch = errors.New("redaction mark does not belong to this page")

// Page is the redaction model of one page's content: its operators, its text
// runs, its marked-content property lists, and the resources they were read
// with. It is built by AnalyzePage and is read-only afterwards.
type Page struct {
	id         uintptr // the page dictionary's identity (see Mark)
	r          *parse.Resolver
	content    []byte
	spans      []parse.ContentOpSpan
	runs       []textRun
	props      []propList
	placements []placement
	res        parse.Dict // the page's effective /Resources
	box        Rect       // the page's MediaBox
}

// Mark is a planned change to one operator of a page's content that applies
// whatever the geometry (a span mark, §5.4): remove a text-showing operator,
// or strip a text key from a BDC/DP property list. Marks are produced by
// Match and are valid only for the page they were made on: a Mark records the
// page dictionary's identity (the Resolver caches it, so the same page object
// analysed again has the same identity), and Rewrite refuses a mark from any
// other page.
type Mark struct {
	page       uintptr
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
// to another content stream (walk.go). Returns a wrapped parse error if the
// page content or a child unit cannot be read without discarding input, a
// *RefusalError if a child unit shows text or a budget is exceeded, or a
// resolve or decode error; the model otherwise.
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
	spans, err := parse.ScanContentStrict(content, parse.StrictOptions{InlineColorSpace: w.colorSpaces(sc)})
	if err != nil {
		return nil, err
	}
	mb := pi.MediaBox
	p := &Page{
		id:      pageID(pi.Dict),
		r:       r,
		content: content.Data,
		spans:   spans,
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

// pageID is a page dictionary's identity, 0 for none (a bare content stream).
func pageID(d parse.Dict) uintptr {
	if d == nil {
		return 0
	}
	return dictID(d)
}

// analyzeContent is AnalyzePage for a bare content stream with no resources
// (RedactContent, FindTextRects): fonts are unknown, so text decodes as
// Latin-1 and widths are bounded, and nothing is invoked.
func analyzeContent(content []byte) (*Page, error) {
	return AnalyzePage(nil, parse.PageInfo{MediaBox: parse.Rectangle{LLX: -1e6, LLY: -1e6, URX: 1e6, URY: 1e6}},
		parse.ContentSegments{Data: content})
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

// Rewrite returns the page's content with every run that a mark names, or
// whose box meets any of regions (normalized first), removed, and every
// marked property-list key stripped, followed by a solid black rectangle over
// each of boxes (display only). Everything else is byte-identical (I7),
// except that a removed ' or " keeps its line move (and " its spacing): it is
// replaced by T* (aw Tw ac Tc T*), so later text keeps its place. A key
// stripped from an inline property list re-emits that BDC/DP operator
// without it; one stripped from a named /Properties entry is returned in
// resources, a page-private copy of the page's /Resources to replace them in
// the output (nil when no named entry changed).
//
// Returns an error wrapping ErrMarkMismatch, and no content, if a mark was
// not made on this page; or a resolve error for the /Properties copy.
func (p *Page) Rewrite(regions, boxes []Rect, marks []Mark) (content []byte, resources parse.Dict, err error) {
	ops := make(map[[2]int]int, len(p.spans))
	for i, s := range p.spans {
		ops[[2]int{s.Start, s.End}] = i
	}
	remove := map[int]bool{}        // span index
	strip := map[int][]parse.Name{} // prop index -> keys
	propAt := make(map[[2]int]int, len(p.props))
	for i, pl := range p.props {
		propAt[[2]int{pl.start, pl.end}] = i
	}
	for _, mk := range marks {
		if mk.page != p.id {
			return nil, nil, fmt.Errorf("%w: made on another page", ErrMarkMismatch)
		}
		at := [2]int{mk.start, mk.end}
		if mk.strip != "" {
			i, ok := propAt[at]
			if !ok {
				return nil, nil, fmt.Errorf("%w: it names no property list", ErrMarkMismatch)
			}
			strip[i] = append(strip[i], mk.strip)
			continue
		}
		i, ok := ops[at]
		if !ok {
			return nil, nil, fmt.Errorf("%w: it names no operator", ErrMarkMismatch)
		}
		remove[i] = true
	}
	norm := make([]Rect, len(regions))
	for i, rc := range regions {
		norm[i] = normalize(rc)
	}
	for _, r := range p.runs {
		for _, rc := range norm {
			if r.intersects(rc) {
				remove[ops[[2]int{r.start, r.end}]] = true
				break
			}
		}
	}

	type edit struct {
		start, end int
		with       []byte
	}
	var edits []edit
	for i := range remove {
		s := p.spans[i]
		edits = append(edits, edit{s.Start, s.End, p.removal(s)})
	}
	var namedStrips map[parse.Name][]parse.Name
	for i, keys := range strip {
		pl := p.props[i]
		if pl.inline != nil {
			edits = append(edits, edit{pl.start, pl.end, reemitProps(pl, keys)})
			continue
		}
		if namedStrips == nil {
			namedStrips = map[parse.Name][]parse.Name{}
		}
		namedStrips[pl.name] = append(namedStrips[pl.name], keys...)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })

	var b bytes.Buffer
	prev := 0
	for _, e := range edits {
		if e.start < prev {
			continue // operators do not overlap; defensive
		}
		b.Write(p.content[prev:e.start])
		b.Write(e.with)
		prev = e.end
	}
	b.Write(p.content[prev:])
	for _, r := range boxes {
		writeBlackRect(&b, r)
	}

	if namedStrips != nil {
		if resources, err = p.stripProperties(namedStrips); err != nil {
			return nil, nil, err
		}
	}
	return b.Bytes(), resources, nil
}

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
// minus keys. Strings are written in hex form, so their bytes round-trip
// exactly.
func reemitProps(pl propList, keys []parse.Name) []byte {
	d := make(parse.Dict, len(pl.inline))
	for k, v := range pl.inline {
		d[k] = hexStrings(v)
	}
	for _, k := range keys {
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

// stripProperties returns a page-private copy of the page's /Resources whose
// /Properties entries named in strips lack the listed keys. Only the page's
// effective resources are copied to the output, so an entry found only in a
// /Pages ancestor's resources needs no copy. Other values stay as they are
// (references are copied by the builder as usual).
//
// Returns an error if the /Properties dictionary or an entry cannot be
// resolved.
func (p *Page) stripProperties(strips map[parse.Name][]parse.Name) (parse.Dict, error) {
	w := newWalker(p.r)
	props, err := w.resolveDict(p.res["Properties"])
	if err != nil || props == nil {
		return nil, err
	}
	clone := make(parse.Dict, len(props))
	for k, v := range props {
		clone[k] = v
	}
	changed := false
	for name, keys := range strips {
		entry, err := w.resolveDict(props[name])
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		e := make(parse.Dict, len(entry))
		for k, v := range entry {
			e[k] = v
		}
		for _, k := range keys {
			delete(e, k)
		}
		clone[name] = e
		changed = true
	}
	if !changed {
		return nil, nil
	}
	res := make(parse.Dict, len(p.res))
	for k, v := range p.res {
		res[k] = v
	}
	res["Properties"] = clone
	return res, nil
}

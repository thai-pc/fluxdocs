package redact

import (
	"errors"

	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// scan.go finds needles in what a page shows, read leniently as the renderer
// reads it — the page's own content and every unit it paints (forms, tiling
// patterns, soft-mask groups, Type 3 glyph procedures) — and in what an
// annotation holds: automatic plan extension looks for the removed text on
// every page (docs/design/redaction-hardening.md §6.11), and verification
// looks for it in the output (§5.6).

// TextScanner finds needles in pages and annotations of one document. Each
// page and annotation is read with its own walker, under the per-page
// budgets; Decoded totals what all of them decoded, for a call-wide cap.
type TextScanner struct {
	r *parse.Resolver
	n *Needles
	// fonts are the fonts every walk has loaded: a font is read once per
	// scanner, not once per page (its streams are charged to the walk that
	// loads it).
	fonts map[uintptr]*fontmodel.Font
	// Decoded and Ops total the bytes decoded and operator visits of every
	// walk so far.
	Decoded, Ops int64
	// Walked are the content streams the walks read and found no needle
	// in: units (forms, patterns, soft-mask groups, glyph procedures,
	// appearance streams; a unit holding one is reported as PageHits.Unit),
	// and the content streams of pages whose own content holds none.
	Walked map[*parse.Stream]bool
}

// NewTextScanner returns a TextScanner for needles n in the document of r.
func NewTextScanner(r *parse.Resolver, n *Needles) *TextScanner {
	return &TextScanner{r: r, n: n, fonts: map[uintptr]*fontmodel.Font{}}
}

// PageHits is where a page shows a needle.
type PageHits struct {
	// Content is set when a run or property list of the page's own content
	// holds one: Page.FindNeedles on the page's strict model marks it.
	Content bool
	// Unit, when set, is the refusal for the first unit the page paints
	// whose text holds one: redaction does not rewrite such units yet.
	Unit *Refusal
	// Disputed, when set, is why the reading may not be every reader's: the
	// page's content or a unit it paints does not decode, or the strict
	// scanner reports a loss in it (which a lenient reading may hide text
	// behind, e.g. an inline image whose end readers disagree on). The page
	// may then show a needle the search did not see.
	Disputed *Refusal
	// Hidden are the tags of the needles found in what the page's own
	// content holds but does not show (comments, strings of operators that
	// show nothing, property lists): page hygiene removes them.
	Hidden uint8
}

// walker returns a lenient walker that reports the units whose text holds a
// needle in *unit.
func (s *TextScanner) walker(unit **Refusal) *walker {
	w := newWalker(s.r)
	w.lenient = true
	w.fonts = s.fonts
	w.texts = func(kind unitKind, num int, texts []string) {
		if *unit != nil {
			return
		}
		for _, t := range texts {
			if s.n.FindText(t) != 0 {
				r := refuse(kind.reason(), "automatic extension: the removed text is also shown through a %s", unitName(kind, num)).(*Refusal)
				*unit = r
				return
			}
		}
	}
	return w
}

// account adds a walker's totals and, when its reading is not disputed,
// the units it walked.
func (s *TextScanner) account(w *walker) {
	s.Decoded += w.decoded
	s.Ops += w.ops
	if s.Walked == nil {
		s.Walked = map[*parse.Stream]bool{}
	}
	if w.disputed != nil {
		return
	}
	for k := range w.memo {
		s.Walked[k.st] = true
	}
}

// MarkRead records the content streams of a page whose text was searched
// without a hit by other means (a strict page model) as read (Walked).
func (s *TextScanner) MarkRead(pi parse.PageInfo) { s.contentStreams(pi) }

// contentStreams adds the page's content streams to Walked.
func (s *TextScanner) contentStreams(pi parse.PageInfo) {
	if s.Walked == nil {
		s.Walked = map[*parse.Stream]bool{}
	}
	v, err := s.r.Resolve(pi.Dict["Contents"])
	if err != nil {
		return
	}
	add := func(o parse.Object) {
		if obj, err := s.r.Resolve(o); err == nil {
			if st, ok := obj.(*parse.Stream); ok {
				s.Walked[st] = true
			}
		}
	}
	if arr, ok := v.(parse.Array); ok {
		for _, e := range arr {
			add(e)
		}
		return
	}
	add(pi.Dict["Contents"])
}

// Page returns where page pi shows a needle. Content that does not decode
// shows nothing (as when rendering). Returns a *Refusal (budget_exceeded)
// past a budget, or a resolve error for an object the page needs.
func (s *TextScanner) Page(pi parse.PageInfo) (PageHits, error) {
	var unit *Refusal
	w := s.walker(&unit)
	defer s.account(w)
	content, err := s.r.PageContentSegments(pi)
	if errors.Is(err, parse.ErrDecodeLimit) {
		return PageHits{}, budget("decode")
	}
	if err != nil {
		content = parse.ContentSegments{}
		w.dispute(&Refusal{Reason: ReasonContentAmbiguous,
			Detail: "page content does not decode, so the text it shows is unknown"})
	}
	hit := false
	w.pageText = func(reading []byte) {
		hit = hit || s.n.FindTextBytes(reading) != 0
	}
	page, err := analyzePage(w, pi, content)
	if err != nil {
		return PageHits{}, err
	}
	if !hit {
		marks, _ := page.FindNeedles(s.n, nil) // the property lists (it keeps no run)
		hit = len(marks) > 0
	}
	if unit == nil {
		unit = page.UnitHidden(s.n)
	}
	hidden := page.HiddenTags(s.n, nil)
	if !hit && hidden == 0 && w.disputed == nil {
		s.contentStreams(pi)
	}
	return PageHits{Content: hit, Unit: unit, Disputed: w.disputed, Hidden: hidden}, nil
}

// annotTextKeys are the annotation entries that hold text a reader shows or
// exports (ISO 32000-1 Table 164, Table 170): the contents, the title (author),
// the subject, the rich text and the name.
var annotTextKeys = []parse.Name{"Contents", "T", "Subj", "RC", "NM"}

// fieldTextKeys are the field entries that hold text (Tables 220, 222, 228):
// the value, the default value, the alternate name, the options.
var fieldTextKeys = []parse.Name{"V", "DV", "TU", "Opt"}

// Annotation reports whether annotation annot of page pi holds a needle: in
// one of its text entries (/Contents, /T, /Subj, /RC, /NM), in the text
// entries of the field it belongs to (/V, /DV, /TU, /Opt, along its /Parent
// chain), or in what its appearance streams show (/AP /N, /R, /D, every
// state, read as forms painted in the page's resources).
//
// Returns a *Refusal (budget_exceeded) past a budget, or a resolve error.
func (s *TextScanner) Annotation(pi parse.PageInfo, annot parse.Dict) (bool, error) {
	var unit *Refusal
	w := s.walker(&unit)
	defer s.account(w)
	found := false
	text := func(v parse.Object) error {
		obj, err := w.resolve(v)
		if err != nil {
			return err
		}
		found = found || s.textOf(w, obj, 0)
		return nil
	}
	for _, k := range annotTextKeys {
		if err := text(annot[k]); err != nil {
			return false, err
		}
	}
	field := annot
	for depth := 0; depth < 32 && field != nil && !found; depth++ {
		for _, k := range fieldTextKeys {
			if err := text(field[k]); err != nil {
				return false, err
			}
		}
		parent, err := w.resolveDict(field["Parent"])
		if err != nil {
			return false, err
		}
		field = parent
	}
	if found {
		return true, nil
	}
	ap, err := w.resolveDict(annot["AP"])
	if err != nil || ap == nil {
		return false, err
	}
	sc := w.pageScope(pi)
	for _, k := range [...]parse.Name{"N", "R", "D"} {
		v, err := w.resolve(ap[k])
		if err != nil {
			return false, err
		}
		var streams []parse.Object
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
				return false, err
			}
			st, ok := obj.(*parse.Stream)
			if !ok {
				continue
			}
			num := 0
			if ref, ok := raw.(parse.Reference); ok {
				num = ref.Number
			}
			if err := w.walkUnit(st, num, unitForm, sc, 1); err != nil {
				return false, err
			}
			if unit != nil || w.disputed != nil || s.hiddenHit(w) {
				// A needle, or an appearance whose text cannot be read
				// for certain: removing the annotation is the safe outcome.
				return true, nil
			}
		}
	}
	return false, nil
}

// hiddenHit reports whether what the units w walked hold but do not show
// holds a needle.
func (s *TextScanner) hiddenHit(w *walker) bool {
	for _, h := range w.hidden {
		if s.n.FindBytes(h.text, nil) != 0 {
			return true
		}
	}
	return false
}

// textOf reports whether a text value holds a needle: a string (every
// reading, Needles.FindBytes), the strings of an array (a field's /Opt
// pairs) or a text stream (rich text), decoded within the walker's budget.
func (s *TextScanner) textOf(w *walker, obj parse.Object, depth int) bool {
	switch v := obj.(type) {
	case parse.String:
		return s.n.FindBytes(v.Value, nil) != 0
	case parse.Array:
		if depth > 2 {
			return false
		}
		for _, e := range v {
			el, err := w.resolve(e)
			if err == nil && s.textOf(w, el, depth+1) {
				return true
			}
		}
	case *parse.Stream:
		data, err := w.decode(v)
		return err == nil && s.n.FindBytes(data, nil) != 0
	}
	return false
}

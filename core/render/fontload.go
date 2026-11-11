package render

import (
	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// fontLoader loads the fonts of one page render on first use
// (docs/design/type1-std14-fonts.md §4.2). Pages often declare fonts they
// never select (merged and Word documents), so a font is loaded on the first
// Tf that names it, not for every resource entry. Loaded fonts are cached by
// their font dictionary's reference, and parsed programs by their stream's
// reference, so a font listed under several names or in several forms'
// resources, or a program several font dictionaries share, is loaded once
// per render. It is owned by the page's single goroutine.
//
// Each font dictionary loaded costs fontLoadCost raster work, and at most
// maxFontLoads are loaded per render: font dictionaries sharing one program
// pay for the program once, but each still builds its own metrics and code
// map (see budget.go).
type fontLoader struct {
	r     *parse.Resolver
	b     *budget
	fonts map[parse.Reference]*glyphFont // by font dictionary; nil = not drawable
	// progs is keyed by the program stream's reference alone: which
	// /FontFile key reached it does not matter, since parseProgram sniffs
	// the decoded bytes, and the key only decides an unrecognized program,
	// whose result (nil, not drawable) a second key would not change in
	// practice. A direct (unreferenced) program stream is not cached.
	progs map[parse.Reference]any // by program stream: decodeProgram's result
	// cidModels holds composite fonts' metrics (cidMetrics), by descendant
	// CIDFont and /Encoding.
	cidModels map[cidMetricsKey]*fontmodel.Font

	// loads counts the font dictionaries loaded, bounded by maxFontLoads.
	loads int
	// cachedSegs counts the outline segments held by every psEntry's glyph
	// cache, bounded by maxCachedSegments.
	cachedSegs int64
}

func newFontLoader(r *parse.Resolver, b *budget) *fontLoader {
	return &fontLoader{
		r:     r,
		b:     b,
		fonts: map[parse.Reference]*glyphFont{},
		progs: map[parse.Reference]any{},

		cidModels: map[cidMetricsKey]*fontmodel.Font{},
	}
}

// fontScope is the /Font resource dictionary of one content stream (the page
// or a Form XObject) with the fonts its names resolved to so far.
type fontScope struct {
	fl     *fontLoader
	dict   parse.Dict
	byName map[parse.Name]*glyphFont
}

// scope returns the font scope of a resource dictionary (nil resources or no
// /Font: a scope where every name is missing).
func (fl *fontLoader) scope(resources parse.Dict) *fontScope {
	s := &fontScope{fl: fl, byName: map[parse.Name]*glyphFont{}}
	if resources == nil || fl == nil || fl.r == nil {
		return s
	}
	if d, ok, err := fl.r.ResolveDict(resources["Font"]); err == nil && ok {
		s.dict = d
	}
	return s
}

// font returns the font a Tf name selects, loading it on first use; nil when
// the name is missing or its font cannot be drawn from (its text is skipped).
// Once the budget has stopped no font is loaded.
func (s *fontScope) font(name parse.Name) *glyphFont {
	if s == nil || s.dict == nil {
		return nil
	}
	if gf, ok := s.byName[name]; ok {
		return gf
	}
	if s.fl.b.stopped() {
		return nil
	}
	gf := s.fl.load(s.dict[name])
	s.byName[name] = gf
	return gf
}

// load loads the font dictionary obj, through the reference cache when obj is
// a reference. A load not served by the cache costs fontLoadCost raster work;
// past maxFontLoads, or when the charge trips the budget, the font is not
// loaded (nil: its text is skipped).
func (fl *fontLoader) load(obj parse.Object) *glyphFont {
	if obj == nil {
		return nil
	}
	ref, hasRef := obj.(parse.Reference)
	if hasRef {
		if gf, ok := fl.fonts[ref]; ok {
			return gf
		}
	}
	var gf *glyphFont
	if fl.loads < maxFontLoads && fl.b.chargePaint(fontLoadCost) {
		fl.loads++
		if fd, ok, err := fl.r.ResolveDict(obj); err == nil && ok {
			gf = fl.loadOneFont(fd)
		}
	}
	if hasRef && !fl.b.stopped() {
		fl.fonts[ref] = gf
	}
	return gf
}

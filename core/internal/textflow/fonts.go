package textflow

import (
	"github.com/thai-pc/fluxdocs/core/internal/fontmodel"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// FontDict returns the /Font subdictionary of resources as extraction binds
// font names: resources' own /Font entry, resolved through r; nil when there
// is none or it is not a dictionary. A page passes its effective (inherited)
// /Resources, so a name only a /Pages ancestor's resources define is not
// bound.
func FontDict(r *parse.Resolver, resources parse.Dict) parse.Dict {
	if r == nil || resources == nil {
		return nil
	}
	fd, ok, err := r.ResolveDict(resources["Font"])
	if err != nil || !ok {
		return nil
	}
	return fd
}

// PageFonts returns the Fonts of content painted with resources, bound as
// extraction binds them (FontDict): a name is loaded by load from its /Font
// entry the first time Tf selects it; a name the dictionary lacks, or whose
// entry load cannot read (nil), decodes as Latin-1. load is typically
// fontmodel.Load with the caller's options.
func PageFonts(r *parse.Resolver, resources parse.Dict, load func(obj parse.Object) *fontmodel.Font) Fonts {
	fd := FontDict(r, resources)
	if len(fd) == 0 {
		return nil
	}
	loaded := map[parse.Name]*fontmodel.Font{}
	return func(name parse.Name) *fontmodel.Font {
		if f, ok := loaded[name]; ok {
			return f
		}
		var f *fontmodel.Font
		if obj, ok := fd[name]; ok {
			f = load(obj)
		}
		loaded[name] = f
		return f
	}
}

//go:build !nostd14

package faces

import (
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/internal/std14"
	"golang.org/x/image/font/sfnt"
)

// reserved are the words no naming string of a bundled face may contain
// (design §11 D1, D3): the Reserved Font Names of Liberation 2.x (OFL 1.1
// §3), the words the Bitstream Vera and Arev licenses forbid in a modified
// font's name, and the upstream family names.
var reserved = []string{"liberation", "arimo", "tinos", "cousine", "bitstream", "vera", "arev", "tavmjong", "dejavu"}

// noticeIDs are the name IDs copied verbatim from upstream because the
// licenses require their notices kept: copyright (0) and license
// description and URL (13, 14). They may name the upstream fonts.
var noticeIDs = map[sfnt.NameID]bool{sfnt.NameIDCopyright: true, sfnt.NameIDLicense: true, sfnt.NameIDLicenseURL: true}

// TestFacesRenamed is the Reserved Font Name check of design §11.1: every
// embedded face parses, keeps its copyright notice, and no naming string
// (family, style, unique ID, full name, PostScript name, typographic names,
// description …) contains a reserved word.
func TestFacesRenamed(t *testing.T) {
	var buf sfnt.Buffer
	for f := std14.Face(0); f < std14.NumFaces; f++ {
		sf, err := sfnt.Parse(Data(f))
		if err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if cr, err := sf.Name(&buf, sfnt.NameIDCopyright); err != nil || !strings.Contains(cr, "Copyright") {
			t.Errorf("%v: copyright notice %q, %v", f, cr, err)
		}
		family, err := sf.Name(&buf, sfnt.NameIDFamily)
		if err != nil || !strings.HasPrefix(family, "FluxStd ") {
			t.Errorf("%v: family %q, %v", f, family, err)
		}
		for id := sfnt.NameID(0); id < 256; id++ {
			if noticeIDs[id] {
				continue
			}
			s, err := sf.Name(&buf, id)
			if err != nil {
				continue // absent
			}
			for _, w := range reserved {
				if strings.Contains(strings.ToLower(s), w) {
					t.Errorf("%v: name ID %d %q contains %q", f, id, s, w)
				}
			}
		}
	}
	if Data(std14.NumFaces) != nil {
		t.Error("Data of an out-of-range face is not nil")
	}
}

// TestSymbolFaceCoverage: the symbol face has a glyph for every Symbol and
// ZapfDingbats code of their built-in encodings, through std14.Rune or
// std14.AltRune, except the few private-use Symbol glyphs with no standard
// equivalent (radicalex, apple).
func TestSymbolFaceCoverage(t *testing.T) {
	sf, err := sfnt.Parse(Data(std14.Symbol))
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	missingOK := map[string]bool{"radicalex": true}
	for _, face := range []std14.Face{std14.Symbol, std14.ZapfDingbats} {
		enc := std14.BuiltinEncoding(face)
		for code, name := range enc {
			if name == "" || name == "space" || missingOK[name] {
				continue
			}
			var g sfnt.GlyphIndex
			if r, ok := std14.Rune(name); ok {
				g, _ = sf.GlyphIndex(&buf, r)
			}
			if r, ok := std14.AltRune(name); ok && g == 0 {
				g, _ = sf.GlyphIndex(&buf, r)
			}
			if g == 0 {
				t.Errorf("%v code %#x %q: no glyph", face, code, name)
			}
		}
	}
}

// Command std14fonts builds FluxDocs' standard-14 font data from pinned
// upstream files (docs/design/type1-std14-fonts.md §3.3, §11): the renamed
// Liberation 2.x and DejaVu Sans subsets embedded by core/internal/std14/faces,
// the AFM width and encoding tables and the glyph-name table generated into
// core/internal/std14, and the license files under
// core/internal/std14/LICENSES.
//
// It is an offline, maintainer-run step, not a build dependency: its output
// is committed. Every input is checked against inputs.go before anything is
// read, and the output is deterministic (no timestamps), so running it again
// on the same inputs reproduces the committed files byte for byte. It prints
// the SHA-256 of every file it writes, for THIRD_PARTY_NOTICES.md.
//
// Usage (from the repository root):
//
//	go run ./testing/tools/std14fonts -liberation DIR -dejavu DIR -afm DIR -agl DIR [-out .]
//
// The subsetter is a minimal TrueType (glyf) one written for these inputs:
// it keeps the glyphs of a Unicode set plus their composite components,
// drops hinting and layout tables, and rewrites the name table so no
// Reserved Font Name remains (OFL 1.1 §3; Bitstream Vera and Arev licenses).
// It is not a general-purpose tool.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The redaction model's substitutedBBox (core/internal/fontmodel), in
// thousandths of an em: every glyph a substitute face draws must lie inside
// it at scale 1 (design §4.6). Glyphs that do not are left out of the subset.
var substitutedBBox = [4]float64{-300, -450, 1450, 1250}

// latinFace is one Liberation-derived output face.
type latinFace struct {
	face   string // std14 Face constant(s) it serves, for the report
	src    string // Liberation file
	family string // output family
	style  string // output subfamily
	afm    string // the AFM whose glyph set it covers
}

var latinFaces = []latinFace{
	{"Courier", "LiberationMono-Regular.ttf", "Mono", "Regular", "Courier.afm"},
	{"CourierBold", "LiberationMono-Bold.ttf", "Mono", "Bold", "Courier-Bold.afm"},
	{"CourierOblique", "LiberationMono-Italic.ttf", "Mono", "Italic", "Courier-Oblique.afm"},
	{"CourierBoldOblique", "LiberationMono-BoldItalic.ttf", "Mono", "Bold Italic", "Courier-BoldOblique.afm"},
	{"Helvetica", "LiberationSans-Regular.ttf", "Sans", "Regular", "Helvetica.afm"},
	{"HelveticaBold", "LiberationSans-Bold.ttf", "Sans", "Bold", "Helvetica-Bold.afm"},
	{"HelveticaOblique", "LiberationSans-Italic.ttf", "Sans", "Italic", "Helvetica-Oblique.afm"},
	{"HelveticaBoldOblique", "LiberationSans-BoldItalic.ttf", "Sans", "Bold Italic", "Helvetica-BoldOblique.afm"},
	{"TimesRoman", "LiberationSerif-Regular.ttf", "Serif", "Regular", "Times-Roman.afm"},
	{"TimesBold", "LiberationSerif-Bold.ttf", "Serif", "Bold", "Times-Bold.afm"},
	{"TimesItalic", "LiberationSerif-Italic.ttf", "Serif", "Italic", "Times-Italic.afm"},
	{"TimesBoldItalic", "LiberationSerif-BoldItalic.ttf", "Serif", "Bold Italic", "Times-BoldItalic.afm"},
}

// afmFaces maps each AFM file to its std14 Face constant, in Face order.
var afmFaces = []struct{ file, face string }{
	{"Courier.afm", "Courier"}, {"Courier-Bold.afm", "CourierBold"},
	{"Courier-Oblique.afm", "CourierOblique"}, {"Courier-BoldOblique.afm", "CourierBoldOblique"},
	{"Helvetica.afm", "Helvetica"}, {"Helvetica-Bold.afm", "HelveticaBold"},
	{"Helvetica-Oblique.afm", "HelveticaOblique"}, {"Helvetica-BoldOblique.afm", "HelveticaBoldOblique"},
	{"Times-Roman.afm", "TimesRoman"}, {"Times-Bold.afm", "TimesBold"},
	{"Times-Italic.afm", "TimesItalic"}, {"Times-BoldItalic.afm", "TimesBoldItalic"},
	{"Symbol.afm", "Symbol"}, {"ZapfDingbats.afm", "ZapfDingbats"},
}

// reserved are the words no naming string of an output face may contain:
// the OFL Reserved Font Names of Liberation 2.x and the names the Bitstream
// Vera and Arev licenses forbid in a modified font's name, plus the upstream
// family names themselves. The copyright (ID 0) and license (IDs 13, 14)
// strings are copied verbatim, as the licenses require their notices kept.
// afmSource holds the AFM files by name, for the generated header.
var afmSource map[string][]byte

var reserved = []string{"liberation", "arimo", "tinos", "cousine", "bitstream", "vera", "arev", "tavmjong", "dejavu"}

func main() {
	libDir := flag.String("liberation", "", "extracted liberation-fonts-ttf-2.1.5 directory")
	djvDir := flag.String("dejavu", "", "extracted dejavu-fonts-ttf-2.37 directory")
	afmDir := flag.String("afm", "", "extracted Core14_AFMs.zip directory")
	aglDir := flag.String("agl", "", "agl-aglfn files (glyphlist.txt, zapfdingbats.txt, LICENSE.md)")
	out := flag.String("out", ".", "repository root")
	flag.Parse()
	if *libDir == "" || *djvDir == "" || *afmDir == "" || *aglDir == "" {
		flag.Usage()
		os.Exit(2)
	}
	in := map[string]map[string][]byte{}
	for src, dir := range map[string]string{"liberation": *libDir, "dejavu": *djvDir, "afm": *afmDir, "agl": *aglDir} {
		in[src] = map[string][]byte{}
		for name, want := range inputSums[src] {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err != nil {
				log.Fatal(err)
			}
			if got := sum(data); got != want {
				log.Fatalf("%s/%s: SHA-256 %s, want %s", src, name, got, want)
			}
			in[src][name] = data
		}
	}

	agl, err := parseAGL(in["agl"]["glyphlist.txt"])
	if err != nil {
		log.Fatal(err)
	}
	zapf, err := parseAGL(in["agl"]["zapfdingbats.txt"])
	if err != nil {
		log.Fatal(err)
	}
	afms := map[string]*afm{}
	afmSource = in["afm"]
	for _, af := range afmFaces {
		a, err := parseAFM(in["afm"][af.file])
		if err != nil {
			log.Fatalf("%s: %v", af.file, err)
		}
		afms[af.file] = a
	}
	// nameRune is the Unicode value(s) a glyph name selects in a substitute:
	// the zapfdingbats list for ZapfDingbats names, the AGL otherwise, then
	// the Symbol private-use fallbacks (symbolAlt, mirrored in std14).
	nameRunes := func(name string, dingbat bool) []rune {
		var rs []rune
		if dingbat {
			rs = append(rs, zapf[name]...)
		}
		if len(rs) == 0 {
			rs = append(rs, agl[name]...)
		}
		if r, ok := symbolAlt[name]; ok {
			rs = append(rs, r)
		}
		return rs
	}

	files := map[string][]byte{} // output path (slash-separated) → content
	kept := map[rune]bool{}      // every rune some face maps
	var report []string

	// Latin faces.
	for _, lf := range latinFaces {
		f, err := parseTTF(in["liberation"][lf.src])
		if err != nil {
			log.Fatalf("%s: %v", lf.src, err)
		}
		var runes []rune
		for r := rune(0x20); r <= 0x7E; r++ {
			runes = append(runes, r)
		}
		for r := rune(0xA0); r <= 0xFF; r++ {
			runes = append(runes, r)
		}
		for _, g := range afms[lf.afm].glyphs {
			rs := nameRunes(g.name, false)
			if len(rs) == 0 {
				log.Fatalf("%s: glyph %q has no AGL value", lf.afm, g.name)
			}
			runes = append(runes, rs...)
		}
		ttfName := "FluxStd" + lf.family + "-" + strings.ReplaceAll(lf.style, " ", "") + ".ttf"
		data, missing, dropped := buildFace(f, runes, names(f, "FluxStd "+lf.family, lf.style, false))
		files["core/internal/std14/faces/"+ttfName] = data
		markKept(data, kept)
		report = append(report, fmt.Sprintf("%-28s %-22s %6d bytes; runes absent upstream %v; dropped by box %v", ttfName, lf.face, len(data), missing, dropped))
	}

	// Symbol + ZapfDingbats face.
	{
		f, err := parseTTF(in["dejavu"]["ttf/DejaVuSans.ttf"])
		if err != nil {
			log.Fatal(err)
		}
		runes := []rune{0x20}
		for _, g := range afms["Symbol.afm"].glyphs {
			runes = append(runes, nameRunes(g.name, false)...)
		}
		for _, g := range afms["ZapfDingbats.afm"].glyphs {
			rs := nameRunes(g.name, true)
			if len(rs) == 0 {
				log.Fatalf("ZapfDingbats.afm: glyph %q has no value", g.name)
			}
			runes = append(runes, rs...)
		}
		data, missing, dropped := buildFace(f, runes, names(f, "FluxStd Symbol", "Regular", true))
		files["core/internal/std14/faces/FluxStdSymbol-Regular.ttf"] = data
		markKept(data, kept)
		report = append(report, fmt.Sprintf("%-28s %-22s %6d bytes; runes absent upstream %v; dropped by box %v", "FluxStdSymbol-Regular.ttf", "Symbol, ZapfDingbats", len(data), missing, dropped))
	}

	files["core/internal/std14/afm_gen.go"] = genAFM(afms)
	files["core/internal/std14/agl_gen.go"] = genAGL(agl, zapf, afms, kept)
	for name, data := range licenseFiles(in) {
		files["core/internal/std14/LICENSES/"+name] = data
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		dst := filepath.Join(*out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(dst, files[p], 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s  %s  %d\n", sum(files[p]), p, len(files[p]))
	}
	for _, l := range report {
		fmt.Println("#", l)
	}
}

// buildFace subsets f to runes, leaving out glyphs outside substitutedBBox.
// It returns the font, the runes f has no glyph for, and the runes dropped
// by the box.
func buildFace(f *ttf, runes []rune, recs []nameRecord) ([]byte, []string, []string) {
	scale := 1000 / float64(f.unitsPerEm)
	seen := map[rune]bool{}
	var uniq []rune
	var missing, dropped []string
	for _, r := range runes {
		if seen[r] {
			continue
		}
		seen[r] = true
		g, ok := f.cmap[r]
		if !ok {
			missing = append(missing, fmt.Sprintf("U+%04X", r))
			continue
		}
		if b, ok := bbox(f.glyph(int(g))); ok {
			if float64(b[0])*scale < substitutedBBox[0] || float64(b[1])*scale < substitutedBBox[1] ||
				float64(b[2])*scale > substitutedBBox[2] || float64(b[3])*scale > substitutedBBox[3] {
				dropped = append(dropped, fmt.Sprintf("U+%04X", r))
				continue
			}
		}
		uniq = append(uniq, r)
	}
	data, err := f.subset(subsetSpec{runes: uniq, names: recs})
	if err != nil {
		log.Fatal(err)
	}
	for _, rec := range recs {
		if rec.id == 0 || rec.id == 13 || rec.id == 14 {
			continue
		}
		for _, w := range reserved {
			if strings.Contains(strings.ToLower(rec.text), w) {
				log.Fatalf("name ID %d %q contains %q", rec.id, rec.text, w)
			}
		}
	}
	return data, missing, dropped
}

// names returns the output name table: upstream copyright (ID 0), version
// (5) and license (13, 14) strings verbatim; the Liberation designer credits
// (8, 9); and new naming strings (1–4, 6) plus a description (10).
func names(f *ttf, family, style string, dejavu bool) []nameRecord {
	src := f.names()
	ps := strings.ReplaceAll(family, " ", "") + "-" + strings.ReplaceAll(style, " ", "")
	recs := []nameRecord{
		{0, src[0]},
		{1, family},
		{2, style},
		{3, family + " " + style + " (FluxDocs subset)"},
		{4, family + " " + style},
		{5, src[5]},
		{6, ps},
		{10, "Renamed subset made for FluxDocs; see THIRD_PARTY_NOTICES.md for the source and its license."},
		{13, src[13]},
		{14, src[14]},
	}
	if !dejavu {
		recs = append(recs, nameRecord{8, src[8]}, nameRecord{9, src[9]})
	}
	for _, r := range recs {
		if r.text == "" {
			log.Fatalf("%s: empty name ID %d", family, r.id)
		}
	}
	return recs
}

// markKept records the runes the output font's cmap maps.
func markKept(data []byte, kept map[rune]bool) {
	f, err := parseTTF(data)
	if err != nil {
		log.Fatalf("re-parse: %v", err)
	}
	for r := range f.cmap {
		kept[r] = true
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// licenseFiles returns the files of core/internal/std14/LICENSES: each
// upstream license verbatim after a short header saying what it covers.
func licenseFiles(in map[string]map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	out["OFL-1.1-Liberation.txt"] = append([]byte(
		"This file covers core/internal/std14/faces/FluxStdSans-*.ttf,\n"+
			"FluxStdSerif-*.ttf and FluxStdMono-*.ttf: subsets of Liberation Sans,\n"+
			"Liberation Serif and Liberation Mono 2.1.5, renamed (\"FluxStd Sans\",\n"+
			"\"FluxStd Serif\", \"FluxStd Mono\") as SIL OFL 1.1 section 3 requires for\n"+
			"a Modified Version, with hinting and layout tables removed. The upstream\n"+
			"LICENSE file follows, verbatim.\n\n"+
			"------------------------------------------------------------------------\n\n"),
		in["liberation"]["LICENSE"]...)
	out["Bitstream-Vera-DejaVu.txt"] = append([]byte(
		"This file covers core/internal/std14/faces/FluxStdSymbol-Regular.ttf: a\n"+
			"subset of DejaVu Sans 2.37 (the glyphs the Symbol and ZapfDingbats fonts\n"+
			"need), renamed (\"FluxStd Symbol\") as the Bitstream Vera and Arev\n"+
			"licenses require for a modified font, with hinting and layout tables\n"+
			"removed. The upstream LICENSE file follows, verbatim.\n\n"+
			"------------------------------------------------------------------------\n\n"),
		in["dejavu"]["LICENSE"]...)

	para := regexp.MustCompile(`This file and the 14 PostScript\(R\) AFM files[^<]*?the AFM files\.`).Find(in["afm"]["MustRead.html"])
	if para == nil {
		log.Fatal("MustRead.html: license paragraph not found")
	}
	// Each distinct notice once, in order of first appearance, labelled with
	// every AFM file that carries it (in afmFaces order).
	var order []string
	filesOf := map[string][]string{}
	for _, af := range afmFaces {
		for _, line := range strings.Split(string(in["afm"][af.file]), "\n") {
			line = strings.TrimRight(line, "\r")
			if !strings.HasPrefix(line, "Notice ") {
				continue
			}
			n := strings.TrimPrefix(line, "Notice ")
			fs := filesOf[n]
			if len(fs) == 0 {
				order = append(order, n)
			}
			if len(fs) == 0 || fs[len(fs)-1] != af.file {
				filesOf[n] = append(fs, af.file)
			}
		}
	}
	var notices []string
	for _, n := range order {
		notices = append(notices, strings.Join(filesOf[n], ", ")+":\n  "+n)
	}
	out["APAFML.txt"] = []byte(
		"This file covers core/internal/std14/afm_gen.go: the character widths,\n" +
			"FontBBox values and built-in encodings of the Adobe Core 14 AFM files\n" +
			"(Core14_AFMs.zip), extracted to Go tables. That extraction is the only\n" +
			"modification; the AFM files themselves are not distributed.\n\n" +
			"Copyright notices of the AFM files, retained as the license requires:\n\n" +
			strings.Join(notices, "\n") + "\n\n" +
			"The license paragraph of the accompanying MustRead.html, verbatim:\n\n" +
			"------------------------------------------------------------------------\n\n" +
			string(para) + "\n")
	agl := string(in["agl"]["glyphlist.txt"])
	cr := regexp.MustCompile(`Copyright [0-9-]+ Adobe \(http://www\.adobe\.com/\)\.`).FindString(agl)
	if cr == "" {
		log.Fatal("glyphlist.txt: copyright line not found")
	}
	out["BSD-3-Clause-AGL.txt"] = append([]byte(
		"This file covers core/internal/std14/agl_gen.go: glyph name to Unicode\n"+
			"entries selected from the Adobe Glyph List (glyphlist.txt) and the ITC\n"+
			"Zapf Dingbats glyph list (zapfdingbats.txt) of\n"+
			"github.com/adobe-type-tools/agl-aglfn, converted to Go tables.\n\n"+
			cr+"\n\n"+
			"The upstream LICENSE.md follows, verbatim.\n\n"+
			"------------------------------------------------------------------------\n\n"),
		in["agl"]["LICENSE.md"]...)
	return out
}

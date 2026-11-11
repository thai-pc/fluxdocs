package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
	"github.com/thai-pc/fluxdocs/core/render"
)

// This file holds the Phase 0 measurements of docs/design/type1-std14-fonts.md
// (§7.2, §8): TestFontCensus counts the font kinds a corpus uses, and
// TestRenderHashes records per-page render and per-file extraction hashes so
// each font phase can show which pages it changed. Both run only when
// FLUXDOCS_EXTRA_CORPUS names a file with one PDF path per line (the format of
// redaction_regress_test.go). Their output holds counts, hashes and the 14
// standard font names only: no document text and no other font names
// (SECURITY.md §4 spirit).
//
// FLUXDOCS_CENSUS_WRITE=path writes the per-page font kinds (TSV: file, page,
// comma-separated kinds); the summary is logged. FLUXDOCS_HASH_WRITE=path
// writes TSV lines "file<TAB>page<TAB>what<TAB>sha256-or-outcome<TAB>ms" for
// PNG renders of the first FLUXDOCS_HASH_PAGES pages (default 3) at 72 and
// 150 DPI, an SVG render, and one "extract" line per file (ExtractText of the
// whole document, hashed). A PNG is hashed by its decoded pixels (width,
// height, then 8-bit RGBA rows), so the hashes do not depend on the Go
// version's PNG encoder; an SVG is hashed by its bytes. A failed render or
// extraction records "error:" and the class of the error (errClass), never
// its message.

// Font kinds counted by the census.
const (
	kindType1         = "type1_embedded"       // /FontFile
	kindMMType1       = "mmtype1"              // /Subtype /MMType1, any program
	kindType1C        = "type1c"               // /FontFile3 /Type1C
	kindCIDType0C     = "cidfonttype0c"        // /FontFile3 /CIDFontType0C
	kindTrueType      = "truetype"             // /FontFile2 under a simple font
	kindCIDType2      = "cidfonttype2"         // /FontFile2 under a Type 0 font
	kindOpenType      = "opentype"             // /FontFile3 /OpenType
	kindFontFile3     = "fontfile3_other"      // /FontFile3 with another or no /Subtype
	kindStd14         = "std14_nonembedded"    // one of the 14 names, no program
	kindOtherNonEmb   = "other_nonembedded"    // simple font, no program
	kindType0NonEmb   = "type0_nonembedded"    // composite font, no program
	kindType3         = "type3"                // /Subtype /Type3
	kindTTNoCmap      = "truetype_no_cmap"     // /FontFile2 without a cmap table
	kindTTSymbolic30  = "truetype_30_only"     // /FontFile2 whose only cmap is (3,0)
	kindType1Sniffed  = "type1_in_fontfile3"   // Type 1 bytes under /FontFile3
	kindUndecodable   = "program_undecodable"  // program stream that does not decode
	kindUnknownFormat = "program_unrecognized" // decoded program of no known format
)

// std14Names are the standard 14 font names (ISO 32000-1 §9.6.2.2).
var std14Names = map[string]bool{
	"Times-Roman": true, "Times-Bold": true, "Times-Italic": true, "Times-BoldItalic": true,
	"Helvetica": true, "Helvetica-Bold": true, "Helvetica-Oblique": true, "Helvetica-BoldOblique": true,
	"Courier": true, "Courier-Bold": true, "Courier-Oblique": true, "Courier-BoldOblique": true,
	"Symbol": true, "ZapfDingbats": true,
}

// extraCorpus returns the paths listed by FLUXDOCS_EXTRA_CORPUS, or skips.
func extraCorpus(t *testing.T) []string {
	t.Helper()
	list := os.Getenv("FLUXDOCS_EXTRA_CORPUS")
	if list == "" {
		t.Skip("FLUXDOCS_EXTRA_CORPUS not set")
	}
	paths, err := readLines(list)
	if err != nil {
		t.Fatalf("FLUXDOCS_EXTRA_CORPUS: %v", err)
	}
	return paths
}

// parallel runs fn(i) for i in [0, n) on runtime.NumCPU() goroutines.
func parallel(n int, fn func(i int)) {
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

// censusFile is the census of one PDF: the kinds per page, and the distinct
// font objects per kind.
type censusFile struct {
	status string                 // "ok", "open_failed", "encrypted", "read_failed", "panic"
	pages  []map[string]bool      // kinds per page
	fonts  map[string]map[any]int // kind -> distinct font identities
}

// fontCensus classifies every font one PDF's pages use, through their
// /Resources and those of the Form XObjects they reach (depth ≤ 12).
func fontCensus(data []byte) (out censusFile) {
	defer func() {
		if recover() != nil {
			out = censusFile{status: "panic"}
		}
	}()
	r, err := parse.NewResolver(data)
	if err != nil {
		return censusFile{status: "open_failed"}
	}
	if err := r.EnableDecryption(nil); err != nil {
		return censusFile{status: "encrypted"}
	}
	pages, err := r.Pages()
	if err != nil {
		return censusFile{status: "open_failed"}
	}
	out = censusFile{status: "ok", fonts: map[string]map[any]int{}}
	kindsOf := map[any][]string{} // font identity -> kinds (cached per file)
	direct := 0                   // direct font dictionaries: one identity each
	for _, pi := range pages {
		kinds := map[string]bool{}
		seenForms := map[parse.Reference]bool{}
		var walk func(res parse.Dict, depth int)
		walk = func(res parse.Dict, depth int) {
			if res == nil || depth > 12 {
				return
			}
			if fonts, ok, _ := r.ResolveDict(res["Font"]); ok {
				for _, v := range fonts {
					var id any
					if ref, isRef := v.(parse.Reference); isRef {
						id = ref
					} else {
						direct++
						id = direct
					}
					ks, done := kindsOf[id]
					if !done {
						fd, ok, _ := r.ResolveDict(v)
						if !ok {
							continue
						}
						ks = classifyFont(r, fd)
						kindsOf[id] = ks
					}
					for _, k := range ks {
						kinds[k] = true
						if out.fonts[k] == nil {
							out.fonts[k] = map[any]int{}
						}
						out.fonts[k][id]++
					}
				}
			}
			xobjs, ok, _ := r.ResolveDict(res["XObject"])
			if !ok {
				return
			}
			for _, v := range xobjs {
				ref, isRef := v.(parse.Reference)
				if isRef {
					if seenForms[ref] {
						continue
					}
					seenForms[ref] = true
				}
				o, err := r.Resolve(v)
				if err != nil {
					continue
				}
				st, ok := o.(*parse.Stream)
				if !ok {
					continue
				}
				if sub, _ := st.Dict.GetName("Subtype"); sub != "Form" {
					continue
				}
				fres, ok, _ := r.ResolveDict(st.Dict["Resources"])
				if ok {
					walk(fres, depth+1)
				}
			}
		}
		walk(pi.Resources, 0)
		out.pages = append(out.pages, kinds)
	}
	return out
}

// classifyFont returns the census kinds of one font dictionary.
func classifyFont(r *parse.Resolver, fd parse.Dict) []string {
	name := func(obj parse.Object) parse.Name {
		o, err := r.Resolve(obj)
		if err != nil {
			return ""
		}
		n, _ := o.(parse.Name)
		return n
	}
	subtype := name(fd["Subtype"])
	switch subtype {
	case "Type3":
		return []string{kindType3}
	}
	holder := fd
	composite := subtype == "Type0"
	if composite {
		o, err := r.Resolve(fd["DescendantFonts"])
		arr, ok := o.(parse.Array)
		if err != nil || !ok || len(arr) == 0 {
			return []string{kindType0NonEmb}
		}
		d, ok, _ := r.ResolveDict(arr[0])
		if !ok {
			return []string{kindType0NonEmb}
		}
		holder = d
	}
	var kinds []string
	if subtype == "MMType1" {
		kinds = append(kinds, kindMMType1)
	}
	fdesc, _, _ := r.ResolveDict(holder["FontDescriptor"])
	stream := func(key parse.Name) (*parse.Stream, bool) {
		if fdesc == nil {
			return nil, false
		}
		o, err := r.Resolve(fdesc[key])
		if err != nil {
			return nil, false
		}
		st, ok := o.(*parse.Stream)
		return st, ok
	}
	if _, ok := stream("FontFile"); ok {
		return append(kinds, kindType1)
	}
	if st, ok := stream("FontFile2"); ok {
		if composite {
			kinds = append(kinds, kindCIDType2)
		} else {
			kinds = append(kinds, kindTrueType)
		}
		data, err := parse.DecodeStream(st)
		if err != nil {
			return append(kinds, kindUndecodable)
		}
		switch hasCmap, only30 := sfntCmap(data); {
		case !hasCmap:
			kinds = append(kinds, kindTTNoCmap)
		case only30:
			kinds = append(kinds, kindTTSymbolic30)
		}
		return kinds
	}
	if st, ok := stream("FontFile3"); ok {
		switch name(st.Dict["Subtype"]) {
		case "Type1C":
			kinds = append(kinds, kindType1C)
		case "CIDFontType0C":
			kinds = append(kinds, kindCIDType0C)
		case "OpenType":
			kinds = append(kinds, kindOpenType)
		default:
			kinds = append(kinds, kindFontFile3)
		}
		if data, err := parse.DecodeStream(st); err != nil {
			kinds = append(kinds, kindUndecodable)
		} else if sniffType1(data) {
			kinds = append(kinds, kindType1Sniffed)
		}
		return kinds
	}
	if composite {
		return append(kinds, kindType0NonEmb)
	}
	base := string(name(fd["BaseFont"]))
	if i := strings.IndexByte(base, '+'); i == 6 {
		base = base[i+1:]
	}
	if std14Names[base] {
		return append(kinds, kindStd14)
	}
	return append(kinds, kindOtherNonEmb)
}

// sniffType1 reports whether a program starts like a Type 1 font.
func sniffType1(b []byte) bool {
	return strings.HasPrefix(string(b), "%!PS-AdobeFont") || strings.HasPrefix(string(b), "%!FontType1") ||
		(len(b) >= 2 && b[0] == 0x80 && b[1] == 0x01)
}

// sfntCmap reads an sfnt table directory and reports whether it has a cmap
// table, and whether that table's only encoding record is (3,0).
func sfntCmap(b []byte) (hasCmap, only30 bool) {
	if len(b) < 12 {
		return false, false
	}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		if rec+16 > len(b) {
			return false, false
		}
		if string(b[rec:rec+4]) != "cmap" {
			continue
		}
		off := int64(binary.BigEndian.Uint32(b[rec+8 : rec+12]))
		if off+4 > int64(len(b)) {
			return true, false
		}
		cm := b[off:]
		num := int(binary.BigEndian.Uint16(cm[2:4]))
		if num == 0 || 4+8*num > len(cm) {
			return true, false
		}
		only30 = true
		for j := 0; j < num; j++ {
			e := cm[4+8*j:]
			if binary.BigEndian.Uint16(e[0:2]) != 3 || binary.BigEndian.Uint16(e[2:4]) != 0 {
				only30 = false
			}
		}
		return true, only30
	}
	return false, false
}

// TestFontCensus is the Phase 0 font census (design §7.2).
func TestFontCensus(t *testing.T) {
	paths := extraCorpus(t)
	results := make([]censusFile, len(paths))
	parallel(len(paths), func(i int) {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			results[i] = censusFile{status: "read_failed"}
			return
		}
		results[i] = fontCensus(data)
	})

	status := map[string]int{}
	files := map[string]int{}
	pagesN := map[string]int{}
	fonts := map[string]int{}
	totalPages, filesWithFonts := 0, 0
	var lines []string
	for i, res := range results {
		status[res.status]++
		totalPages += len(res.pages)
		if len(res.fonts) > 0 {
			filesWithFonts++
		}
		for k, ids := range res.fonts {
			files[k]++
			fonts[k] += len(ids)
		}
		for p, kinds := range res.pages {
			ks := make([]string, 0, len(kinds))
			for k := range kinds {
				pagesN[k]++
				ks = append(ks, k)
			}
			sort.Strings(ks)
			lines = append(lines, paths[i]+"\t"+strconv.Itoa(p)+"\t"+strings.Join(ks, ","))
		}
	}
	if path := os.Getenv("FLUXDOCS_CENSUS_WRITE"); path != "" {
		sort.Strings(lines)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "files=%d pages=%d files_with_fonts=%d status=%v\n", len(paths), totalPages, filesWithFonts, status)
	fmt.Fprintf(&b, "%-22s %6s %6s %6s %7s\n", "kind", "files", "pages", "fonts", "%files")
	kinds := make([]string, 0, len(files))
	for k := range files {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(&b, "%-22s %6d %6d %6d %6.1f%%\n", k, files[k], pagesN[k], fonts[k],
			100*float64(files[k])/float64(len(paths)))
	}
	t.Log("\n" + b.String())
}

// TestRenderHashes records render and extraction hashes (design §7.2) to the
// file named by FLUXDOCS_HASH_WRITE.
func TestRenderHashes(t *testing.T) {
	paths := extraCorpus(t)
	out := os.Getenv("FLUXDOCS_HASH_WRITE")
	if out == "" {
		t.Skip("FLUXDOCS_HASH_WRITE not set")
	}
	maxPages := 3
	if v := os.Getenv("FLUXDOCS_HASH_PAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("FLUXDOCS_HASH_PAGES=%q", v)
		}
		maxPages = n
	}
	results := make([][]string, len(paths))
	parallel(len(paths), func(i int) {
		results[i] = fileHashes(paths[i], maxPages)
	})
	var lines []string
	for _, r := range results {
		lines = append(lines, r...)
	}
	sort.Strings(lines)
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d hash lines to %s", len(lines), out)
}

// fileHashes renders and extracts one PDF; see TestRenderHashes.
func fileHashes(path string, maxPages int) []string {
	line := func(page int, what, v string, d time.Duration) string {
		return fmt.Sprintf("%s\t%d\t%s\t%s\t%d", path, page, what, v, d.Milliseconds())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{line(-1, "open", "read_failed", 0)}
	}
	doc, err := core.OpenBytes(data)
	if err != nil {
		return []string{line(-1, "open", "open_failed", 0)}
	}
	defer doc.Close()
	sum := func(b []byte, err error) string {
		if err != nil {
			return "error:" + errClass(err)
		}
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	pixels := func(b []byte, err error) string {
		if err != nil {
			return "error:" + errClass(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return "error:png"
		}
		bd := img.Bounds()
		h := sha256.New()
		fmt.Fprintf(h, "%dx%d\n", bd.Dx(), bd.Dy())
		row := make([]byte, 0, 4*bd.Dx())
		for y := bd.Min.Y; y < bd.Max.Y; y++ {
			row = row[:0]
			for x := bd.Min.X; x < bd.Max.X; x++ {
				c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
				row = append(row, c.R, c.G, c.B, c.A)
			}
			h.Write(row)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	var out []string
	start := time.Now()
	text, err := doc.ExtractText(core.ExtractOptions{})
	out = append(out, line(-1, "extract", sum([]byte(text), err), time.Since(start)))
	for p := 0; p < min(doc.PageCount, maxPages); p++ {
		for _, o := range []core.RenderOptions{{DPI: 72, Format: "png"}, {DPI: 150, Format: "png"}, {DPI: 72, Format: "svg"}} {
			start := time.Now()
			img, err := doc.RenderPage(p, o)
			d := time.Since(start)
			hash := pixels
			if o.Format == "svg" {
				hash = sum
			}
			out = append(out, line(p, fmt.Sprintf("%s%d", o.Format, o.DPI), hash(img, err), d))
		}
	}
	return out
}

// errClass names an error by the public sentinel it matches, or "other": only
// the class is recorded, never message text, which may quote document data.
func errClass(err error) string {
	for _, s := range []struct {
		name string
		err  error
	}{
		{"budget", core.ErrRenderBudgetExceeded},
		{"internal", core.ErrInternal},
		{"invalid", core.ErrInvalidPDF},
		{"encrypted", core.ErrEncryptedDocument},
		{"unsupported", core.ErrUnsupportedFormat},
		{"unsupported", render.ErrUnsupportedFormat}, // SVG output, until it ships
	} {
		if errors.Is(err, s.err) {
			return s.name
		}
	}
	return "other"
}

// TestFileHashesOutcomes checks fileHashes on a clean corpus file: every PNG
// render and the extraction are hashed, the SVG render is hashed by its bytes
// or, while RenderPage does not produce SVG, recorded as "unsupported" (never
// run through the PNG decoder), and errClass reports a class, never message
// text.
func TestFileHashesOutcomes(t *testing.T) {
	path := filepath.Join("clean", "text.pdf")
	lines := fileHashes(path, 1)
	if len(lines) != 4 { // extract + png72 + png150 + svg72
		t.Fatalf("fileHashes returned %d lines, want 4: %q", len(lines), lines)
	}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) != 5 {
			t.Fatalf("line %q: %d fields, want 5", l, len(f))
		}
		if f[2] == "svg72" && f[3] == "error:unsupported" {
			continue
		}
		if strings.HasPrefix(f[3], "error:") || len(f[3]) != 64 {
			t.Errorf("%s: outcome %q, want a sha256 hash", f[2], f[3])
		}
	}
	if got := errClass(errors.New("render: secret document text")); got != "other" {
		t.Errorf("errClass(unclassified) = %q, want %q", got, "other")
	}
	if got := errClass(fmt.Errorf("page 1: %w", core.ErrRenderBudgetExceeded)); got != "budget" {
		t.Errorf("errClass(budget) = %q, want %q", got, "budget")
	}
}

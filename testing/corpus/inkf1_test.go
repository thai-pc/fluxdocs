//go:build !js

package corpus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
)

// This file is the ink-F1 measurement of docs/design/clipping-paths.md §9
// (and type1-std14-fonts.md §7.2): it renders pages with FluxDocs and with
// Ghostscript, binarizes both (gray < 128 is ink) and reports, per page and
// DPI, precision = |ours ∩ gs| / |ours|, recall = |ours ∩ gs| / |gs|, their
// harmonic mean F1, and IoU = |ours ∩ gs| / |ours ∪ gs|. Ghostscript (AGPL) is
// an external black-box oracle run as a separate process: nothing is linked
// or vendored, its images are never committed.
//
// It runs only when FLUXDOCS_EXTRA_CORPUS names a file with one PDF path per
// line, and skips without gs on PATH unless FLUXDOCS_REQUIRE_GS=1. Knobs:
//   - FLUXDOCS_INKF1_PAGES=path: measure only the "file<TAB>page" lines of
//     that file (e.g. the pages whose hashes changed); otherwise the first
//     FLUXDOCS_HASH_PAGES pages (default 8) of every file;
//   - FLUXDOCS_INKF1_DPI: comma-separated DPIs (default "72,150");
//   - FLUXDOCS_GS_CACHE=dir: keep Ghostscript's renders there, keyed by the
//     file's SHA-256, page and DPI, so a before/after pair runs gs once;
//   - FLUXDOCS_INKF1_WRITE=path: write TSV lines
//     "file page dpi ours gs inter precision recall f1 iou" (or "file page
//     dpi error:<class>"); the per-DPI means are logged.
//
// The two renders are compared over their common area: Ghostscript sizes a
// page by rounding its own way, which can differ from ours by one pixel.

// inkStats is the comparison of one page at one DPI.
type inkStats struct {
	ours, gs, inter int64
}

func (s inkStats) precision() float64 {
	if s.ours == 0 {
		return boolF(s.gs == 0)
	}
	return float64(s.inter) / float64(s.ours)
}

func (s inkStats) recall() float64 {
	if s.gs == 0 {
		return boolF(s.ours == 0)
	}
	return float64(s.inter) / float64(s.gs)
}

// f1 is the harmonic mean of precision and recall; two blank pages agree.
func (s inkStats) f1() float64 {
	if s.ours == 0 && s.gs == 0 {
		return 1
	}
	p, r := s.precision(), s.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// iou is the intersection over the union of the two inks.
func (s inkStats) iou() float64 {
	u := s.ours + s.gs - s.inter
	if u == 0 {
		return 1
	}
	return float64(s.inter) / float64(u)
}

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// isInk binarizes a pixel: luma below 128 is ink.
func isInk(c color.Color) bool {
	return color.GrayModel.Convert(c).(color.Gray).Y < 128
}

// compareInk compares two renders over their common area.
func compareInk(ours, gs image.Image) inkStats {
	bo, bg := ours.Bounds(), gs.Bounds()
	w, h := min(bo.Dx(), bg.Dx()), min(bo.Dy(), bg.Dy())
	var s inkStats
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := isInk(ours.At(bo.Min.X+x, bo.Min.Y+y))
			b := isInk(gs.At(bg.Min.X+x, bg.Min.Y+y))
			if a {
				s.ours++
			}
			if b {
				s.gs++
			}
			if a && b {
				s.inter++
			}
		}
	}
	return s
}

// gsPages renders pages [first, last] (1-based) of the PDF at path with
// Ghostscript at dpi into cache (or a temporary directory) and returns the
// image file of each page, "" for a page gs did not produce.
func gsPages(gs, path, cache string, sum string, first, last, dpi int) (map[int]string, error) {
	out := map[int]string{}
	name := func(p int) string { return filepath.Join(cache, fmt.Sprintf("%s-p%d-r%d.png", sum, p, dpi)) }
	missing := false
	for p := first; p <= last; p++ {
		if _, err := os.Stat(name(p)); err == nil {
			out[p] = name(p)
		} else {
			missing = true
		}
	}
	if !missing {
		return out, nil
	}
	tmp, err := os.MkdirTemp("", "inkf1-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, gs, "-q", "-dSAFER", "-dNOPAUSE", "-dBATCH", "-sDEVICE=pnggray",
		"-r"+strconv.Itoa(dpi), "-dFirstPage="+strconv.Itoa(first), "-dLastPage="+strconv.Itoa(last),
		"-sOutputFile="+filepath.Join(tmp, "p%d.png"), path)
	_ = cmd.Run() // a damaged file may still render some pages
	for p := first; p <= last; p++ {
		src := filepath.Join(tmp, fmt.Sprintf("p%d.png", p-first+1))
		b, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(name(p), b, 0o644); err != nil {
			return nil, err
		}
		out[p] = name(p)
	}
	return out, nil
}

// inkF1File measures the given 0-based pages of one PDF at each DPI.
func inkF1File(gs, path, cache string, pages []int, dpis []int) []string {
	line := func(p, dpi int, rest string) string { return fmt.Sprintf("%s\t%d\t%d\t%s", path, p, dpi, rest) }
	var out []string
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{line(-1, 0, "error:read")}
	}
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	doc, err := core.OpenBytes(data)
	if err != nil {
		return []string{line(-1, 0, "error:open")}
	}
	defer doc.Close()
	first, last := pages[0], pages[len(pages)-1]
	for _, dpi := range dpis {
		files, err := gsPages(gs, path, cache, sum, first+1, last+1, dpi)
		if err != nil {
			out = append(out, line(-1, dpi, "error:cache"))
			continue
		}
		for _, p := range pages {
			if p >= doc.PageCount {
				continue
			}
			b, err := doc.RenderPage(p, core.RenderOptions{DPI: dpi, Format: "png"})
			if err != nil {
				out = append(out, line(p, dpi, "error:"+errClass(err)))
				continue
			}
			ours, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				out = append(out, line(p, dpi, "error:png"))
				continue
			}
			gb, err := os.ReadFile(files[p+1])
			if err != nil {
				out = append(out, line(p, dpi, "error:gs"))
				continue
			}
			theirs, err := png.Decode(bytes.NewReader(gb))
			if err != nil {
				out = append(out, line(p, dpi, "error:gs"))
				continue
			}
			s := compareInk(ours, theirs)
			out = append(out, line(p, dpi, fmt.Sprintf("%d\t%d\t%d\t%.6f\t%.6f\t%.6f\t%.6f",
				s.ours, s.gs, s.inter, s.precision(), s.recall(), s.f1(), s.iou())))
		}
	}
	return out
}

// TestInkF1 measures ink F1 against Ghostscript; see the file comment.
func TestInkF1(t *testing.T) {
	paths := extraCorpus(t)
	gs, _ := exec.LookPath("gs")
	if gs == "" {
		if os.Getenv("FLUXDOCS_REQUIRE_GS") == "1" {
			t.Fatal("gs not found and FLUXDOCS_REQUIRE_GS=1")
		}
		t.Skip("gs not installed")
	}
	dpis := []int{72, 150}
	if v := os.Getenv("FLUXDOCS_INKF1_DPI"); v != "" {
		dpis = nil
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || n < 1 || n > 600 {
				t.Fatalf("FLUXDOCS_INKF1_DPI=%q", v)
			}
			dpis = append(dpis, n)
		}
	}
	maxPages := 8
	if v := os.Getenv("FLUXDOCS_HASH_PAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("FLUXDOCS_HASH_PAGES=%q", v)
		}
		maxPages = n
	}
	want := map[string][]int{}
	var order []string
	if list := os.Getenv("FLUXDOCS_INKF1_PAGES"); list != "" {
		lines, err := readLines(list)
		if err != nil {
			t.Fatalf("FLUXDOCS_INKF1_PAGES: %v", err)
		}
		for _, l := range lines {
			f, p, ok := strings.Cut(l, "\t")
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if !ok || err != nil || n < 0 {
				t.Fatalf("FLUXDOCS_INKF1_PAGES: bad line %q", l)
			}
			if want[f] == nil {
				order = append(order, f)
			}
			want[f] = append(want[f], n)
		}
	} else {
		for _, p := range paths {
			order = append(order, p)
			for i := 0; i < maxPages; i++ {
				want[p] = append(want[p], i)
			}
		}
	}
	cache := os.Getenv("FLUXDOCS_GS_CACHE")
	if cache == "" {
		cache = t.TempDir()
	} else if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	results := make([][]string, len(order))
	parallel(len(order), func(i int) {
		ps := want[order[i]]
		sort.Ints(ps)
		results[i] = inkF1File(gs, order[i], cache, ps, dpis)
	})
	var lines []string
	sumF1 := map[int]float64{}
	n := map[int]int{}
	errs := 0
	for _, r := range results {
		for _, l := range r {
			lines = append(lines, l)
			f := strings.Split(l, "\t")
			if len(f) < 9 {
				errs++
				continue
			}
			dpi, _ := strconv.Atoi(f[2])
			v, _ := strconv.ParseFloat(f[8], 64)
			sumF1[dpi] += v
			n[dpi]++
		}
	}
	sort.Strings(lines)
	if path := os.Getenv("FLUXDOCS_INKF1_WRITE"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dpi := range dpis {
		if n[dpi] > 0 {
			t.Logf("%d DPI: %d pages, mean ink F1 %.4f", dpi, n[dpi], sumF1[dpi]/float64(n[dpi]))
		}
	}
	t.Logf("%d lines with an error", errs)
}

// TestInkStats pins the metric on small cases (no Ghostscript needed).
func TestInkStats(t *testing.T) {
	for _, tt := range []struct {
		s             inkStats
		p, r, f1, iou float64
	}{
		{inkStats{0, 0, 0}, 1, 1, 1, 1},
		{inkStats{10, 0, 0}, 0, 0, 0, 0},
		{inkStats{0, 10, 0}, 0, 0, 0, 0},
		{inkStats{10, 10, 10}, 1, 1, 1, 1},
		{inkStats{10, 20, 10}, 1, 0.5, 2.0 / 3, 0.5},
	} {
		if tt.s.precision() != tt.p || tt.s.recall() != tt.r || tt.s.f1() != tt.f1 || tt.s.iou() != tt.iou {
			t.Errorf("%+v: p=%v r=%v f1=%v iou=%v, want %v %v %v %v", tt.s,
				tt.s.precision(), tt.s.recall(), tt.s.f1(), tt.s.iou(), tt.p, tt.r, tt.f1, tt.iou)
		}
	}
	a := image.NewGray(image.Rect(0, 0, 3, 2))
	b := image.NewGray(image.Rect(0, 0, 2, 3)) // compared over the common 2×2
	for i := range a.Pix {
		a.Pix[i] = 0xff
	}
	for i := range b.Pix {
		b.Pix[i] = 0xff
	}
	a.SetGray(0, 0, color.Gray{0})
	a.SetGray(2, 0, color.Gray{0}) // outside the common area
	b.SetGray(0, 0, color.Gray{127})
	b.SetGray(1, 1, color.Gray{0})
	if s := compareInk(a, b); s != (inkStats{1, 2, 1}) {
		t.Errorf("compareInk = %+v, want {1 2 1}", s)
	}
}

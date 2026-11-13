package corpus

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file holds the Phase 0 clip census of docs/design/clipping-paths.md §9:
// TestClipCensus walks every page's content (and the Form XObjects it
// reaches, depth ≤ 12) the way the renderer's interpreter will, and counts
// what the clip implementation has to handle. It runs only when
// FLUXDOCS_EXTRA_CORPUS names a file with one PDF path per line.
// FLUXDOCS_CLIP_CENSUS_WRITE=path writes one TSV line per page (file, page,
// then the counts in clipCensusColumns); the summary is logged. The output
// holds counts only, never document text (SECURITY.md §4 spirit).
//
// The walk mirrors the design, not the renderer's code: device coordinates at
// 150 DPI through the same matrix arithmetic, a clip applied by the painting
// operator that ends a W/W* path, the rectangle test of §3.2 (one closed
// subpath of four points, edges axis-aligned by ==), and the mask liveness
// rule of §3.4 with a mask sized by the path's device box clipped to the
// current clip box, in 64-pixel words. Live bytes are therefore an estimate of
// what the renderer allocates, exact in count.

// clipCensusDPI is the resolution the census sizes masks at (the default).
const clipCensusDPI = 150

// clipCensusColumns names the per-page counts, in TSV order.
var clipCensusColumns = []string{
	"clip_ops",         // painting operators that applied a pending W/W*
	"clip_rect",        // of which the rectangle fast path
	"clip_general",     // of which a general path (a mask)
	"clip_evenodd",     // of which W*
	"clip_empty",       // of which a path fill paints nothing for (all clipped)
	"w_no_point",       // W/W* applied with no current point (ignored, §2.2)
	"w_then_path",      // W/W* followed by more path construction (Q3)
	"w_then_q",         // W/W* pending across Q, Do or BT/ET (§2.2)
	"w_in_bt",          // W/W* inside a text object
	"w_unpainted",      // W/W* still pending at the end of a content stream
	"max_live_masks",   // largest number of live masks (§3.4)
	"max_live_bytes",   // largest live mask bytes at 150 DPI (estimate)
	"max_nesting",      // most clips in effect at once (narrowings in the state)
	"text_clip_int",    // text objects showing text in Tr 4–7 (integer)
	"text_clip_nonint", // text objects showing text in a non-integer Tr in [4, 8)
	"sh_ops",           // sh operators (not rendered yet; context for the review)
	"pattern_color",    // sc/scn/SC/SCN selecting a pattern by name (not rendered)
	"gs_alpha",         // gs selecting an ExtGState with /CA or /ca < 1 (not rendered)
}

// clipCounts is the census of one page.
type clipCounts [18]int64

const (
	ccOps = iota
	ccRect
	ccGeneral
	ccEvenOdd
	ccEmpty
	ccNoPoint
	ccThenPath
	ccThenQ
	ccInBT
	ccUnpainted
	ccLiveMasks
	ccLiveBytes
	ccNesting
	ccTextInt
	ccTextNonInt
	ccSh
	ccPattern
	ccAlpha
)

// censusMask is a simulated clip mask: its build depth and size.
type censusMask struct {
	depth int
	bytes int64
}

// censusClip is the simulated clip of a graphics state.
type censusClip struct {
	x0, y0, x1, y1 float64 // device box (not rounded)
	mask           *censusMask
	nest           int
}

// censusState is the simulated graphics state.
type censusState struct {
	ctm  [6]float64
	clip censusClip
	tr   float64 // text rendering mode as written
}

// censusWalker walks one page.
type censusWalker struct {
	r       *parse.Resolver
	c       clipCounts
	gs      censusState
	stack   []censusState
	path    [][][2]float64 // device subpaths
	closed  []bool
	cur     [][2]float64
	pending int // 0 none, 1 W, 2 W*
	afterW  bool
	inBT    bool
	textHit bool // this text object showed text in a clip mode
	textNI  bool
	live    int64
	liveN   int64
	seen    map[parse.Reference]bool
	depth   int
	floor   int // Q may not pop below this stack length (a form's own frames)
}

func censusMul(m, n [6]float64) [6]float64 {
	return [6]float64{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func censusApply(m [6]float64, x, y float64) [2]float64 {
	return [2]float64{m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]}
}

// censusBase is the page's user-space-to-device matrix (render's
// deviceMatrix) and canvas size.
func censusBase(pi parse.PageInfo, s float64) ([6]float64, float64, float64) {
	mb := pi.MediaBox
	w, h := mb.Width(), mb.Height()
	switch pi.Rotate {
	case 90:
		return [6]float64{0, s, s, 0, -mb.LLY * s, -mb.LLX * s}, math.Ceil(h * s), math.Ceil(w * s)
	case 180:
		return [6]float64{-s, 0, 0, s, mb.URX * s, -mb.LLY * s}, math.Ceil(w * s), math.Ceil(h * s)
	case 270:
		return [6]float64{0, -s, -s, 0, mb.URY * s, mb.URX * s}, math.Ceil(h * s), math.Ceil(w * s)
	}
	return [6]float64{s, 0, 0, -s, -mb.LLX * s, mb.URY * s}, math.Ceil(w * s), math.Ceil(h * s)
}

func censusNum(o parse.Object) (float64, bool) {
	switch v := o.(type) {
	case parse.Integer:
		return float64(v), true
	case parse.Real:
		return float64(v), true
	}
	return 0, false
}

func censusNums(o []parse.Object, n int) ([]float64, bool) {
	if len(o) < n {
		return nil, false
	}
	out := make([]float64, n)
	for i := range out {
		v, _ := censusNum(o[len(o)-n+i])
		out[i] = v
	}
	return out, true
}

// clipCensusPage walks one page and returns its counts.
func clipCensusPage(r *parse.Resolver, pi parse.PageInfo) (clipCounts, bool) {
	content, err := r.PageContent(pi)
	if err != nil {
		return clipCounts{}, false
	}
	base, w, h := censusBase(pi, clipCensusDPI/72.0)
	cw := &censusWalker{r: r, seen: map[parse.Reference]bool{}}
	cw.gs = censusState{ctm: base, clip: censusClip{x1: w, y1: h}}
	cw.walk(parse.ScanContent(content), pi.Resources)
	return cw.c, true
}

func (cw *censusWalker) flush(closed bool) {
	if len(cw.cur) > 0 {
		cw.path = append(cw.path, cw.cur)
		cw.closed = append(cw.closed, closed)
		cw.cur = nil
	}
}

func (cw *censusWalker) clearPath() {
	cw.path, cw.closed, cw.cur = nil, nil, nil
}

func (cw *censusWalker) construct() {
	if cw.pending != 0 {
		cw.afterW = true
	}
}

// release drops a mask's bytes from the live count.
func (cw *censusWalker) release(m *censusMask) {
	cw.live -= m.bytes
	cw.liveN--
}

// replaceClip installs c as the current clip, releasing the old mask when the
// liveness rule (§3.4) says nothing else can reference it.
func (cw *censusWalker) replaceClip(c censusClip) {
	if old := cw.gs.clip.mask; old != nil && old != c.mask && old.depth == len(cw.stack) {
		cw.release(old)
	}
	cw.gs.clip = c
}

// pop restores the top saved state (Q), applying the liveness rule.
func (cw *censusWalker) pop() {
	n := len(cw.stack)
	next := cw.stack[n-1]
	if m := cw.gs.clip.mask; m != nil && m.depth > n-1 && m != next.clip.mask {
		cw.release(m)
	}
	cw.gs = next
	cw.stack = cw.stack[:n-1]
}

// applyClip applies a pending W/W* with the current path.
func (cw *censusWalker) applyClip() {
	if cw.pending == 0 {
		return
	}
	if cw.afterW {
		cw.c[ccThenPath]++
	}
	evenOdd := cw.pending == 2
	cw.pending, cw.afterW = 0, false
	cw.flush(false)
	npts := 0
	for _, sp := range cw.path {
		npts += len(sp)
	}
	if npts == 0 {
		cw.c[ccNoPoint]++
		return
	}
	cw.c[ccOps]++
	if evenOdd {
		cw.c[ccEvenOdd]++
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	finite, nan := true, false
	for _, sp := range cw.path {
		for _, p := range sp {
			nan = nan || math.IsNaN(p[0]) || math.IsNaN(p[1])
			if math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
				finite = false
			}
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	cur := cw.gs.clip
	nc := censusClip{
		x0: math.Max(cur.x0, minX), y0: math.Max(cur.y0, minY),
		x1: math.Min(cur.x1, maxX), y1: math.Min(cur.y1, maxY),
		mask: cur.mask, nest: cur.nest + 1,
	}
	if nan || !(maxX-minX > 0) || !(maxY-minY > 0) {
		cw.c[ccEmpty]++
		cw.replaceClip(censusClip{nest: nc.nest})
		return
	}
	if finite && censusIsRect(cw.path) {
		cw.c[ccRect]++
		cw.gs.clip = nc // keeps the mask
	} else {
		cw.c[ccGeneral]++
		if nc.x1 <= nc.x0 || nc.y1 <= nc.y0 {
			cw.replaceClip(censusClip{nest: nc.nest})
		} else {
			x0w := math.Floor(math.Floor(nc.x0) / 64)
			x1w := math.Ceil(math.Ceil(nc.x1) / 64)
			rows := math.Ceil(nc.y1) - math.Floor(nc.y0)
			m := &censusMask{depth: len(cw.stack), bytes: int64((x1w-x0w)*rows*8 + rows*8)}
			cw.live += m.bytes
			cw.liveN++
			nc.mask = m
			cw.replaceClip(nc)
		}
	}
	cw.c[ccLiveMasks] = max(cw.c[ccLiveMasks], cw.liveN)
	cw.c[ccLiveBytes] = max(cw.c[ccLiveBytes], cw.live)
	cw.c[ccNesting] = max(cw.c[ccNesting], int64(cw.gs.clip.nest))
}

// censusIsRect is the §3.2 rectangle test.
func censusIsRect(path [][][2]float64) bool {
	if len(path) != 1 {
		return false
	}
	p := path[0]
	if len(p) == 5 && p[4] == p[0] {
		p = p[:4]
	}
	if len(p) != 4 {
		return false
	}
	return (p[0][1] == p[1][1] && p[1][0] == p[2][0] && p[2][1] == p[3][1] && p[3][0] == p[0][0]) ||
		(p[0][0] == p[1][0] && p[1][1] == p[2][1] && p[2][0] == p[3][0] && p[3][1] == p[0][1])
}

func (cw *censusWalker) paint() {
	cw.applyClip()
	cw.clearPath()
}

func (cw *censusWalker) noteShow() {
	if !cw.inBT {
		return
	}
	tr := cw.gs.tr
	if tr >= 4 && tr < 8 {
		if tr == math.Trunc(tr) {
			cw.textHit = true
		} else {
			cw.textNI = true
		}
	}
}

// walk interprets ops with resources res.
func (cw *censusWalker) walk(ops []parse.ContentOp, res parse.Dict) {
	var cx, cy, sx, sy float64
	for _, op := range ops {
		o := op.Operands
		switch op.Op {
		case "q":
			if len(cw.stack) < 1024 {
				cw.stack = append(cw.stack, cw.gs)
			}
		case "Q":
			if cw.pending != 0 {
				cw.c[ccThenQ]++
			}
			if len(cw.stack) > cw.floor {
				cw.pop()
			}
		case "cm":
			if v, ok := censusNums(o, 6); ok {
				cw.gs.ctm = censusMul([6]float64{v[0], v[1], v[2], v[3], v[4], v[5]}, cw.gs.ctm)
			}
		case "m":
			if v, ok := censusNums(o, 2); ok {
				cw.construct()
				cw.flush(false)
				cw.cur = [][2]float64{censusApply(cw.gs.ctm, v[0], v[1])}
				cx, cy, sx, sy = v[0], v[1], v[0], v[1]
			}
		case "l":
			if v, ok := censusNums(o, 2); ok {
				cw.construct()
				cw.cur = append(cw.cur, censusApply(cw.gs.ctm, v[0], v[1]))
				cx, cy = v[0], v[1]
			}
		case "c", "v", "y":
			n := 6
			if op.Op != "c" {
				n = 4
			}
			if v, ok := censusNums(o, n); ok {
				cw.construct()
				if cw.cur == nil {
					cw.cur = [][2]float64{censusApply(cw.gs.ctm, cx, cy)}
				}
				ex, ey := v[n-2], v[n-1]
				// A curve is never part of a rectangle: 16 flattened points.
				for i := 0; i < 16; i++ {
					cw.cur = append(cw.cur, censusApply(cw.gs.ctm, ex, ey))
				}
				cx, cy = ex, ey
			}
		case "re":
			if v, ok := censusNums(o, 4); ok {
				cw.construct()
				cw.flush(false)
				x, y, w, h := v[0], v[1], v[2], v[3]
				cw.path = append(cw.path, [][2]float64{
					censusApply(cw.gs.ctm, x, y), censusApply(cw.gs.ctm, x+w, y),
					censusApply(cw.gs.ctm, x+w, y+h), censusApply(cw.gs.ctm, x, y+h)})
				cw.closed = append(cw.closed, true)
				cx, cy, sx, sy = x, y, x, y
			}
		case "h":
			if len(cw.cur) > 0 {
				cx, cy = sx, sy
				cw.flush(true)
			}
		case "W", "W*":
			if cw.inBT {
				cw.c[ccInBT]++
			}
			cw.pending = 1
			if op.Op == "W*" {
				cw.pending = 2
			}
			cw.afterW = false
		case "f", "F", "f*", "S", "s", "B", "B*", "b", "b*", "n":
			cw.paint()
		case "BT":
			if cw.pending != 0 {
				cw.c[ccThenQ]++
			}
			cw.inBT, cw.textHit, cw.textNI = true, false, false
		case "ET":
			if cw.pending != 0 {
				cw.c[ccThenQ]++
			}
			if cw.textHit {
				cw.c[ccTextInt]++
			}
			if cw.textNI {
				cw.c[ccTextNonInt]++
			}
			cw.inBT, cw.textHit, cw.textNI = false, false, false
		case "Tr":
			if len(o) > 0 {
				cw.gs.tr, _ = censusNum(o[len(o)-1])
			}
		case "Tj", "TJ", "'", "\"":
			cw.noteShow()
		case "sh":
			cw.c[ccSh]++
		case "gs":
			if len(o) > 0 {
				if name, ok := o[len(o)-1].(parse.Name); ok && cw.alphaGS(res, name) {
					cw.c[ccAlpha]++
				}
			}
		case "scn", "SCN", "sc", "SC":
			if len(o) > 0 {
				if _, ok := o[len(o)-1].(parse.Name); ok {
					cw.c[ccPattern]++
				}
			}
		case "Do":
			if cw.pending != 0 {
				cw.c[ccThenQ]++
			}
			if len(o) > 0 {
				if name, ok := o[len(o)-1].(parse.Name); ok {
					cw.doForm(res, name)
				}
			}
		}
	}
	if cw.pending != 0 {
		cw.c[ccUnpainted]++
	}
}

// alphaGS reports whether the ExtGState name selects a constant alpha below 1.
func (cw *censusWalker) alphaGS(res parse.Dict, name parse.Name) bool {
	if res == nil {
		return false
	}
	gss, ok, _ := cw.r.ResolveDict(res["ExtGState"])
	if !ok {
		return false
	}
	gs, ok, _ := cw.r.ResolveDict(gss[name])
	if !ok {
		return false
	}
	for _, k := range []parse.Name{"CA", "ca"} {
		if o, err := cw.r.Resolve(gs[k]); err == nil {
			if v, ok := censusNum(o); ok && v < 1 {
				return true
			}
		}
	}
	return false
}

// doForm walks a Form XObject with its implicit q/Q, saving the path and the
// pending mark like the renderer (§4.1).
func (cw *censusWalker) doForm(res parse.Dict, name parse.Name) {
	if cw.depth >= 12 || res == nil {
		return
	}
	xobjs, ok, _ := cw.r.ResolveDict(res["XObject"])
	if !ok {
		return
	}
	raw := xobjs[name]
	ref, isRef := raw.(parse.Reference)
	if isRef && cw.seen[ref] {
		return
	}
	o, err := cw.r.Resolve(raw)
	if err != nil {
		return
	}
	st, ok := o.(*parse.Stream)
	if !ok {
		return
	}
	if sub, _ := st.Dict.GetName("Subtype"); sub != "Form" {
		return
	}
	data, err := parse.DecodeStream(st)
	if err != nil {
		return
	}
	formRes := res
	if fr, ok, _ := cw.r.ResolveDict(st.Dict["Resources"]); ok {
		formRes = fr
	}
	m := [6]float64{1, 0, 0, 1, 0, 0}
	if mo, err := cw.r.Resolve(st.Dict["Matrix"]); err == nil {
		if arr, ok := mo.(parse.Array); ok && len(arr) >= 6 {
			for i := range m {
				m[i], _ = censusNum(arr[i])
			}
		}
	}
	if isRef {
		cw.seen[ref] = true
		defer delete(cw.seen, ref)
	}
	saved, savedLen := cw.gs, len(cw.stack)
	path, closed, cur, pending, afterW, inBT := cw.path, cw.closed, cw.cur, cw.pending, cw.afterW, cw.inBT
	cw.clearPath()
	cw.pending, cw.afterW, cw.inBT = 0, false, false
	cw.stack = append(cw.stack, cw.gs)
	savedFloor := cw.floor
	cw.floor = savedLen
	cw.gs.ctm = censusMul(m, cw.gs.ctm)
	cw.depth++
	cw.walk(parse.ScanContent(data), formRes)
	cw.depth--
	cw.floor = savedFloor
	for len(cw.stack) > savedLen {
		cw.pop()
	}
	cw.replaceClip(saved.clip)
	cw.gs = saved
	cw.path, cw.closed, cw.cur, cw.pending, cw.afterW, cw.inBT = path, closed, cur, pending, afterW, inBT
}

// clipCensusFile returns the census lines of one PDF, or a status line.
func clipCensusFile(path string, maxPages int) (lines []string, pages []clipCounts, status string) {
	defer func() {
		if recover() != nil {
			lines, pages, status = nil, nil, "panic"
		}
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "read_failed"
	}
	r, err := parse.NewResolver(data)
	if err != nil {
		return nil, nil, "open_failed"
	}
	if err := r.EnableDecryption(nil); err != nil {
		return nil, nil, "encrypted"
	}
	pis, err := r.Pages()
	if err != nil {
		return nil, nil, "open_failed"
	}
	for i, pi := range pis {
		if i >= maxPages {
			break
		}
		c, ok := clipCensusPage(r, pi)
		if !ok {
			continue
		}
		cols := make([]string, len(c))
		for j, v := range c {
			cols[j] = strconv.FormatInt(v, 10)
		}
		lines = append(lines, path+"\t"+strconv.Itoa(i)+"\t"+strings.Join(cols, "\t"))
		pages = append(pages, c)
	}
	return lines, pages, "ok"
}

// TestClipCensus is the Phase 0 clip census (design §9). Pages are the first
// FLUXDOCS_HASH_PAGES of each file (default 8, the hash run's).
func TestClipCensus(t *testing.T) {
	paths := extraCorpus(t)
	maxPages := 8
	if v := os.Getenv("FLUXDOCS_HASH_PAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("FLUXDOCS_HASH_PAGES=%q", v)
		}
		maxPages = n
	}
	type result struct {
		lines  []string
		pages  []clipCounts
		status string
	}
	results := make([]result, len(paths))
	parallel(len(paths), func(i int) {
		l, p, s := clipCensusFile(paths[i], maxPages)
		results[i] = result{l, p, s}
	})

	status := map[string]int{}
	var lines []string
	var total clipCounts
	pagesWith := make([]int, len(clipCensusColumns))
	filesWith := make([]int, len(clipCensusColumns))
	var liveBytes, nesting []int64
	nPages := 0
	for _, res := range results {
		status[res.status]++
		lines = append(lines, res.lines...)
		var any [len(clipCounts{})]bool
		for _, c := range res.pages {
			nPages++
			for j, v := range c {
				if v > 0 {
					pagesWith[j]++
					any[j] = true
				}
				switch j {
				case ccLiveMasks, ccLiveBytes, ccNesting:
					total[j] = max(total[j], v)
				default:
					total[j] += v
				}
			}
			if c[ccOps] > 0 {
				liveBytes = append(liveBytes, c[ccLiveBytes])
				nesting = append(nesting, c[ccNesting])
			}
		}
		for j, a := range any {
			if a {
				filesWith[j]++
			}
		}
	}
	if path := os.Getenv("FLUXDOCS_CLIP_CENSUS_WRITE"); path != "" {
		sort.Strings(lines)
		head := "file\tpage\t" + strings.Join(clipCensusColumns, "\t")
		if err := os.WriteFile(path, []byte(head+"\n"+strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pct := func(s []int64, p float64) int64 {
		if len(s) == 0 {
			return 0
		}
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[min(len(s)-1, int(p*float64(len(s))))]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "files=%d pages=%d status=%v\n", len(paths), nPages, status)
	fmt.Fprintf(&b, "%-17s %12s %7s %7s\n", "count", "total/max", "pages", "files")
	for j, name := range clipCensusColumns {
		fmt.Fprintf(&b, "%-17s %12d %7d %7d\n", name, total[j], pagesWith[j], filesWith[j])
	}
	fmt.Fprintf(&b, "pages with clips: live bytes p50=%d p99=%d max=%d; nesting p50=%d p99=%d max=%d\n",
		pct(liveBytes, 0.5), pct(liveBytes, 0.99), pct(liveBytes, 1), pct(nesting, 0.5), pct(nesting, 0.99), pct(nesting, 1))
	t.Log("\n" + b.String())
}

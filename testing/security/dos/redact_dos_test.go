package dos

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/internal/reltime"
)

// fontCandidatesDoc is a page whose content selects, by gs, each of n
// ExtGStates that only the /Pages node's resources define (so every gs may
// or may not apply, and the redaction text model keeps both the current and
// the new font), each with its own font, then shows m strings. Before the
// font-set cap one RedactText on n = 4000, m = 1600 took 77 s and 404 MB:
// every string was decoded once per accumulated font (go-reviewer, batch B).
func fontCandidatesDoc(t *testing.T, n, m int) *core.Document {
	t.Helper()
	var eg, c strings.Builder
	eg.WriteString("<< /ExtGState << ")
	c.WriteString("BT 10 700 Td ")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&eg, "/G%d << /Font [<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica "+
			"/Encoding << /Differences [120 /uni%04X] >> >> 12] >> ", i, 0x4E00+i)
		fmt.Fprintf(&c, "/G%d gs ", i)
	}
	eg.WriteString(">> >>")
	c.WriteString(strings.Repeat("(x) Tj ", m))
	c.WriteString("ET")
	content := c.String()
	doc, err := core.OpenBytes(classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] /Resources " + eg.String() + " >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDoS_RedactFontCandidatesAreCapped: past the font-set cap the page is
// refused (budget_exceeded) at once — for at most twice what the same strings
// cost with the largest set allowed (seven fallback fonts plus "no font") —
// and at the cap the work stays linear in the strings shown (4× the strings,
// at most reltime.LinearBound× the cost). Both are measured against their
// reference in the same run (reltime), not by an absolute time; both measure
// under half their bound (≈ 0.9× and ≈ 3.75×), so over reltime.FewRuns.
func TestDoS_RedactFontCandidatesAreCapped(t *testing.T) {
	skipUnlessTimed(t)
	redact := func(n, m int) func() {
		doc := fontCandidatesDoc(t, n, m)
		return func() {
			_, err := doc.RedactText(0, `SECRET`)
			var re *core.RedactionError
			switch {
			case n > 7 && (!errors.As(err, &re) || re.Reason != core.RedactionBudgetExceeded):
				t.Fatalf("RedactText with %d fonts = %v, want a budget_exceeded refusal", n, err)
			case n <= 7 && err != nil:
				t.Fatalf("RedactText at the cap: %v", err)
			}
		}
	}
	reltime.Check(t, "refusal past the cap (4000 fonts) against 1600 strings at the cap",
		reltime.AgainstN(reltime.FewRuns, reltime.CPU, func() func() { return redact(7, 1600) }, func() func() { return redact(4000, 1600) }), 2)
	reltime.Check(t, "50,000 strings at the cap against 12,500",
		reltime.LinearN(reltime.FewRuns, reltime.CPU, 4, func(m int) func() { return redact(7, m) }, 12_500), reltime.LinearBound)
}

// pdfCache holds the files openCached built, by key.
var pdfCache sync.Map

// openCached opens the file build returns, built once per key and kept for
// the package's tests: a gate opening a large fixture in every run pays for
// the open, not for building the file again.
func openCached(t *testing.T, key string, build func() []byte) *core.Document {
	t.Helper()
	v, ok := pdfCache.Load(key)
	if !ok {
		v, _ = pdfCache.LoadOrStore(key, build())
	}
	doc, err := core.OpenBytes(v.([]byte))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// floodRedactPDF is a one-page document whose content is n copies of op
// inside one text object (or outside one when text is false).
func floodRedactPDF(op string, n int, text bool) []byte {
	var c strings.Builder
	if text {
		c.WriteString("BT /F1 12 Tf 10 700 Td ")
	}
	c.WriteString(strings.Repeat(op, n))
	if text {
		c.WriteString("ET")
	}
	content := c.String()
	return classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	})
}

// TestDoS_RedactOperatorFloodIsBounded: a page with more operators than the
// redaction operator budget (8 Mi visits) is refused (budget_exceeded) with
// work and memory bounded by the budget, not by the content: the strict scan
// stops at what the budget can pay for instead of materializing every
// operator (StrictOptions.MaxOps), and a text run's readings reuse their
// buffers.
// Four times the content must not grow the allocation, and grows the CPU
// time by at most 2× (measured ≈ 0.9–1.2× against the smaller page,
// reltime; growing with the content would be 4×): the median of
// reltime.FewRuns alternated runs, each measuring both. Before (batch B
// review, S-1), 4 Mi "(x)Tj" took 9.3 s and 2.97 GB RSS, and both grew with
// the content.
func TestDoS_RedactOperatorFloodIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	const budget = 8 << 20
	for _, tc := range []struct {
		name string
		op   string
		n    int // operators of the smaller page; the larger has four times as many
		text bool
	}{
		// The scan stops at what the budget can pay for, two visits per
		// operator (budget / 2 operators); text runs also pay for their
		// readings, so their text model stops sooner.
		{"Tj", "(x)Tj ", budget / 2, true},
		{"n", "n ", budget, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// refuse prepares RedactText of a page of n operators, timed by
			// clock, and returns the allocation besides the content.
			docs := map[int][]byte{}
			refuse := func(n int, clock reltime.Clock) (time.Duration, uint64) {
				pdf, ok := docs[n]
				if !ok {
					pdf = floodRedactPDF(tc.op, n, tc.text)
					docs[n] = pdf
				}
				doc, err := core.OpenBytes(pdf)
				if err != nil {
					t.Fatal(err)
				}
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := clock.Now()
				_, err = doc.RedactText(0, `SECRET`)
				elapsed := clock.Now() - start
				runtime.ReadMemStats(&after)
				var re *core.RedactionError
				if !errors.As(err, &re) || re.Reason != core.RedactionBudgetExceeded {
					t.Fatalf("RedactText (%d operators) = %v, want a budget_exceeded refusal", n, err)
				}
				// The decoded content itself is charged to the decode budget
				// and grows with the page; the rest must not.
				return elapsed, after.TotalAlloc - before.TotalAlloc - uint64(len(tc.op)*n)
			}
			const runs = reltime.FewRuns
			var ratios []float64
			var allocs [2][]uint64
			var refs, works []time.Duration
			for i := 0; i < runs; i++ {
				var r, w time.Duration
				var ra, wa uint64
				if i%2 == 0 {
					r, ra = refuse(tc.n, reltime.CPU)
					w, wa = refuse(4*tc.n, reltime.CPU)
				} else {
					w, wa = refuse(4*tc.n, reltime.CPU)
					r, ra = refuse(tc.n, reltime.CPU)
				}
				ratios = append(ratios, float64(w)/float64(max(r, time.Microsecond)))
				refs, works = append(refs, r), append(works, w)
				allocs[0], allocs[1] = append(allocs[0], ra), append(allocs[1], wa)
			}
			for i := range allocs {
				slices.Sort(allocs[i])
			}
			a0, a1 := allocs[0][runs/2], allocs[1][runs/2]
			t.Logf("%d and %d operators: %d and %d MiB allocated besides the content", tc.n, 4*tc.n, a0>>20, a1>>20)
			// A ceiling above the measured cost, far below what an unbounded
			// scan of these pages allocates.
			if a1 > 2<<30 {
				t.Errorf("allocated %d MiB, want at most 2 GiB", a1>>20)
			}
			if float64(a1) > 1.25*float64(a0) {
				t.Errorf("four times the operators allocated %d MiB against %d MiB: memory grows with the content, not the budget", a1>>20, a0>>20)
			}
			slices.Sort(ratios)
			slices.Sort(refs)
			slices.Sort(works)
			reltime.Check(t, "refusal of four times the operators",
				reltime.Result{Ratio: ratios[runs/2], Ratios: ratios, Ref: refs[runs/2], Work: works[runs/2]}, 2)
		})
	}
}

// sharedFormDoc is pages pages; page 0 paints an image (removed by the
// redaction), every other page paints a form of ops "n" operators, and every
// page lists the image. Resources per mode: "shared", one dictionary for all
// pages; "copies", a dictionary per page with the same values; "distinct", a
// dictionary and a form per page.
func sharedFormDoc(t *testing.T, pages, ops int, mode string) *core.Document {
	t.Helper()
	return openCached(t, fmt.Sprint("sharedFormDoc", pages, ops, mode), func() []byte {
		var zb bytes.Buffer
		zw := zlib.NewWriter(&zb)
		zw.Write([]byte(strings.Repeat("n ", ops)))
		zw.Close()
		form := fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 10 10] /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", zb.Len(), zb.String())
		objs := []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"", // pages
			form,
			"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 1 >>\nstream\n\x80\nendstream",
			"<< /XObject << /Im1 4 0 R /F 3 0 R >> >>",
		}
		kids := ""
		for i := 0; i < pages; i++ {
			res := "5 0 R"
			switch mode {
			case "copies":
				res = "<< /XObject << /Im1 4 0 R /F 3 0 R >> >>"
			case "distinct":
				objs = append(objs, form)
				res = fmt.Sprintf("<< /XObject << /Im1 4 0 R /F %d 0 R >> >>", len(objs))
			}
			content := "/F Do"
			if i == 0 {
				content = "q 100 0 0 100 300 300 cm /Im1 Do Q"
			}
			objs = append(objs,
				fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources %s /Contents %d 0 R >>", res, len(objs)+2),
				fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
			kids += fmt.Sprintf("%d 0 R ", len(objs)-1)
		}
		objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages)
		return classicPDF(objs)
	})
}

// TestDoS_RedactSharedUnitWalkedOnce: pages that are not redacted but share
// objects with removed content are walked to see what they paint, with one
// memo for the whole call, a per-page budget, and a 32 Mi call budget (batch
// C reviews SF-1). A form shared by 40 pages is walked once, whether the
// pages share one resource dictionary or each holds a copy with the same
// values (before, 40 pages of a 3 M-operator form took 47 s): 40 pages of a
// 1 M-operator form cost at most 2× what 10 do (walked per page: 4×, past
// the call budget). A distinct 3 M-operator form
// per page is walked per page, and the call budget refuses it after a few
// pages: 40 such pages cost at most 12× the 40 pages of copies, which walk
// the form once (measured ≈ 5×: the budget's worth of walks; unbounded, 40
// walks), over reltime.FewRuns. Each ratio is measured in the same run
// (reltime), not an absolute time.
func TestDoS_RedactSharedUnitWalkedOnce(t *testing.T) {
	skipUnlessTimed(t)
	// flatten prepares RedactAndFlatten of a pages-page sharedFormDoc in
	// mode with a form of ops operators, page 0 marked, and checks it
	// refuses past the call budget (refuse) or succeeds.
	flatten := func(mode string, ops int, refuse bool) func(pages int) func() {
		return func(pages int) func() {
			doc := sharedFormDoc(t, pages, ops, mode)
			if err := doc.RedactArea(0, core.Rect{X: 340, Y: 340, Width: 5, Height: 5}); err != nil {
				t.Fatal(err)
			}
			return func() {
				_, err := doc.RedactAndFlatten()
				var re *core.RedactionError
				if got := errors.As(err, &re) && re.Reason == core.RedactionBudgetExceeded; got != refuse {
					t.Fatalf("RedactAndFlatten (%s, %d pages) err = %v, want a budget refusal: %v", mode, pages, err, refuse)
				}
				if !refuse && err != nil {
					t.Fatalf("RedactAndFlatten: %v", err)
				}
			}
		}
	}
	for _, mode := range []string{"shared", "copies"} {
		t.Run(mode, func(t *testing.T) {
			reltime.Check(t, "40 pages against 10", reltime.LinearBounded(reltime.CPU, 4, flatten(mode, 1_000_000, false), 10, 2), 2)
		})
	}
	t.Run("distinct", func(t *testing.T) {
		copies, distinct := flatten("copies", 3_000_000, false), flatten("distinct", 3_000_000, true)
		reltime.Check(t, "40 pages of distinct forms against 40 pages of copies",
			reltime.AgainstN(reltime.FewRuns, reltime.CPU,
				func() func() { return copies(40) }, func() func() { return distinct(40) }), 12)
	})
}

// reachDoc is the shape of the batch C review round 5 REACH probe: k images
// removed under the region on page 0, page i (1…k) painting image i-1
// elsewhere, and one shared resource dictionary that also lists m filler
// dictionaries each referring to the last image, so the set of undecided
// removed objects shrinks one image per page while every page can reach it.
func reachDoc(t *testing.T, k, m int) *core.Document {
	t.Helper()
	return openCached(t, fmt.Sprint("reachDoc", k, m), func() []byte {
		var objs []string
		add := func(s string) int { objs = append(objs, s); return len(objs) }
		add("<< /Type /Catalog /Pages 2 0 R >>")
		add("") // pages
		add("") // shared resources
		var xo, p0 strings.Builder
		last := 0
		for i := 0; i < k; i++ {
			n := add(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Length 1 >>\nstream\n%c\nendstream", 'a'+i%26))
			fmt.Fprintf(&xo, "/I%d %d 0 R ", i, n)
			fmt.Fprintf(&p0, "q 1 0 0 1 340 340 cm /I%d Do Q ", i)
			last = n
		}
		var junk strings.Builder
		for j := 0; j < m; j++ {
			fmt.Fprintf(&junk, "%d 0 R ", add(fmt.Sprintf("<< /A %d 0 R >>", last)))
		}
		objs[2] = "<< /XObject << " + xo.String() + " >> /Junk [" + junk.String() + "] >>"
		var kids strings.Builder
		for i := 0; i <= k; i++ {
			c := p0.String()
			if i > 0 {
				c = fmt.Sprintf("q 1 0 0 1 10 10 cm /I%d Do Q", i-1)
			}
			cn := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
			fmt.Fprintf(&kids, "%d 0 R ", add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources 3 0 R /Contents %d 0 R >>", cn)))
		}
		objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), k+1)
		return classicPDF(objs)
	})
}

// TestDoS_RedactReachabilityBounded: deciding which removed objects pages
// that are not redacted still use costs O(graph · log |removed|), not a full
// reachability pass per decided object (batch C review round 5, S-1: k = 2000,
// m = 100 000 took 63 s before): 4× the images and fillers (k = 1000,
// m = 50,000) cost at most reltime.LinearBound× (quadratic: 16×), measured
// in the same run.
func TestDoS_RedactReachabilityBounded(t *testing.T) {
	skipUnlessTimed(t)
	reltime.Check(t, "k = 1000, m = 50,000 against k = 250, m = 12,500",
		reltime.LinearBounded(reltime.CPU, 4, func(n int) func() { return reachFlatten(t, 250*n, 12_500*n) }, 1, reltime.LinearBound), reltime.LinearBound)
}

// reachFlatten returns RedactAndFlatten of reachDoc(k, m) with page 0's
// images under a region, the document built and marked untimed.
func reachFlatten(t *testing.T, k, m int) func() {
	doc := reachDoc(t, k, m)
	if err := doc.RedactArea(0, core.Rect{X: 340, Y: 340, Width: 0.5, Height: 0.5}); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := doc.RedactAndFlatten(); err != nil {
			t.Fatalf("RedactAndFlatten: %v", err)
		}
	}
}

// TestDoS_RedactUndecidedPerPage: each page that is not redacted but can
// reach a removed object costs what it adds to the kept set, not a pass over
// every still-undecided object (F1, batch C review: 60,000 pages × 10 filler
// objects took 66 s, 84% of it iterating the undecided set once per page).
// Each page here decides one of k removed images, so the undecided set
// stays large while every page is walked: 10,000 pages cost at most
// reltime.LinearBound× what 2,500 do (quadratic: 16×), in the same run
// (measured ≈ 3.9× at 30,000 against 7,500, so over reltime.FewRuns).
func TestDoS_RedactUndecidedPerPage(t *testing.T) {
	skipUnlessTimed(t)
	reltime.Check(t, "10,000 pages against 2,500",
		reltime.LinearBounded(reltime.CPU, 4, func(k int) func() { return reachFlatten(t, k, 10) }, 2_500, reltime.LinearBound), reltime.LinearBound)
}

// TestDoS_IndirectStreamLengthIsCheap: every page's content stream has
// /Length 4 0 R, and object 4 is an array of 500,000 integers (about 1 MB).
// Resolving the indirect length reads a few tokens once, not the whole
// target per stream (batch D review round 2, BL1: 200 pages took 35.8 s and
// 2,000 pages 7 min 37 s, against milliseconds without the target). Opening
// and extracting costs at most 2× the same document with direct lengths,
// measured in the same run (reltime; ≈ 1×, so over reltime.FewRuns).
func TestDoS_IndirectStreamLengthIsCheap(t *testing.T) {
	skipUnlessTimed(t)
	build := func(indirect bool) []byte {
		objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			"[" + strings.Repeat("1 ", 500_000) + "]"}
		var kids strings.Builder
		const pages = 500
		for p := 0; p < pages; p++ {
			pn := len(objs) + 1
			content := fmt.Sprintf("BT /F1 12 Tf 72 700 Td (page %d) Tj ET", p)
			length := "4 0 R"
			if !indirect {
				length = fmt.Sprint(len(content))
			}
			objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", pn+1),
				fmt.Sprintf("<< /Length %s >>\nstream\n%s\nendstream", length, content))
			fmt.Fprintf(&kids, "%d 0 R ", pn)
		}
		objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), pages)
		return classicPDF(objs)
	}
	extract := func(pdf []byte) func() {
		return func() {
			doc, err := core.OpenBytes(pdf)
			if err != nil {
				t.Fatal(err)
			}
			text, err := doc.ExtractText(core.ExtractOptions{})
			if err != nil || !strings.Contains(text, "page 499") {
				t.Fatalf("ExtractText: %v (text holds page 499: %v)", err, strings.Contains(text, "page 499"))
			}
		}
	}
	direct, indirect := build(false), build(true)
	reltime.Check(t, "indirect /Length 4 0 R against direct lengths",
		reltime.AgainstN(reltime.FewRuns, reltime.CPU, reltime.Unprepared(extract(direct)), reltime.Unprepared(extract(indirect))), 2)
}

// attrDoc is the batch D review round 2 BL3 shape: page 0 shows the secret,
// page 1 carries it in /PieceInfo (a residual that verification attributes,
// page hygiene), and every page shares one /Resources whose /X array reaches
// m small objects.
func attrDoc(t *testing.T, pages, m int) *core.Document {
	t.Helper()
	return openCached(t, fmt.Sprint("attrDoc", pages, m), func() []byte {
		objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Font << /F1 5 0 R >> /X 4 0 R >>", "",
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
		var arr strings.Builder
		arr.WriteString("[")
		for i := 0; i < m; i++ {
			fmt.Fprintf(&arr, "%d 0 R ", len(objs)+1)
			objs = append(objs, "[1]")
		}
		arr.WriteString("]")
		objs[3] = arr.String()
		var kids strings.Builder
		for p := 0; p < pages; p++ {
			pn := len(objs) + 1
			text, extra := "(public)", ""
			if p == 0 {
				text = "(SECRET-1234)"
			}
			if p == 1 {
				extra = " /PieceInfo << /Q << /Private (SECRET-1234) >> >>"
			}
			c := "BT /F1 24 Tf 72 700 Td " + text + " Tj ET"
			objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources 3 0 R /Contents %d 0 R%s >>", pn+1, extra),
				fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
			fmt.Fprintf(&kids, "%d 0 R ", pn)
		}
		objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), pages)
		return classicPDF(objs)
	})
}

// TestDoS_RedactAttributionIsOnePass: attributing a residual costs one walk of
// what the pages that are not redacted reach, not a closure per page and
// annotation (BL3: 4,000 pages sharing 40,000 objects took 23.2 s): 2,000
// pages and 20,000 objects cost at most reltime.LinearBound× what 500 and
// 5,000 do (a closure per page: 16×), measured in the same run.
func TestDoS_RedactAttributionIsOnePass(t *testing.T) {
	skipUnlessTimed(t)
	flatten := func(n int) func() {
		doc := attrDoc(t, 500*n, 5_000*n)
		if _, err := doc.RedactText(0, `SECRET-\d+`); err != nil {
			t.Fatal(err)
		}
		return func() {
			out, err := doc.RedactAndFlatten()
			if err != nil {
				t.Fatalf("RedactAndFlatten: %v", err)
			}
			if r, _ := out.RedactionReport(); len(r.Extensions) != 1 || r.Extensions[0].Where != "page-hygiene" {
				t.Errorf("report %+v, want page hygiene on page 1", r)
			}
		}
	}
	reltime.Check(t, "2,000 pages and 20,000 objects against 500 and 5,000",
		reltime.LinearBounded(reltime.CPU, 4, flatten, 1, reltime.LinearBound), reltime.LinearBound)
}

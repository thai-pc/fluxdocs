package dos

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core"
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
// refused (budget_exceeded) at once; at the cap the work stays linear.
func TestDoS_RedactFontCandidatesAreCapped(t *testing.T) {
	skipUnlessTimed(t)

	start := time.Now()
	_, err := fontCandidatesDoc(t, 4000, 1600).RedactText(0, `SECRET`)
	var re *core.RedactionError
	if !errors.As(err, &re) || re.Reason != core.RedactionBudgetExceeded {
		t.Fatalf("RedactText = %v, want a budget_exceeded refusal", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("refusal took %v, want it prompt", d)
	}

	// Seven fallback fonts plus "no font": the largest set allowed.
	start = time.Now()
	if _, err := fontCandidatesDoc(t, 7, 50000).RedactText(0, `SECRET`); err != nil {
		t.Fatalf("RedactText at the cap: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("RedactText at the cap took %v for 50000 strings, want linear work", d)
	}
}

// floodRedactDoc is a one-page document whose content is n copies of op
// inside one text object (or outside one when text is false).
func floodRedactDoc(t *testing.T, op string, n int, text bool) *core.Document {
	t.Helper()
	var c strings.Builder
	if text {
		c.WriteString("BT /F1 12 Tf 10 700 Td ")
	}
	c.WriteString(strings.Repeat(op, n))
	if text {
		c.WriteString("ET")
	}
	content := c.String()
	doc, err := core.OpenBytes(classicPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 400 800] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDoS_RedactOperatorFloodIsBounded: a page with more operators than the
// redaction operator budget (8 Mi visits) is refused (budget_exceeded) with
// work and memory bounded by the budget, not by the content: the strict scan
// stops at what the budget can pay for instead of materializing every
// operator (StrictOptions.MaxOps), and a text run's readings reuse their
// buffers.
// Doubling the content must not grow the allocation. Before (batch B review,
// S-1), 4 Mi "(x)Tj" took 9.3 s and 2.97 GB RSS, and both grew with the
// content.
func TestDoS_RedactOperatorFloodIsBounded(t *testing.T) {
	skipUnlessTimed(t)
	const budget = 8 << 20
	for _, tc := range []struct {
		name string
		op   string
		n    int // operators of the smaller page; the larger has twice as many
		text bool
	}{
		// The scan stops at what the budget can pay for, two visits per
		// operator (budget / 2 operators); text runs also pay for their
		// readings, so their text model stops sooner.
		{"Tj", "(x)Tj ", budget / 2, true},
		{"n", "n ", budget, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var allocs [2]uint64
			for i, n := range []int{tc.n, 2 * tc.n} {
				doc := floodRedactDoc(t, tc.op, n, tc.text)
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := time.Now()
				_, err := doc.RedactText(0, `SECRET`)
				elapsed := time.Since(start)
				runtime.ReadMemStats(&after)
				// The decoded content itself is charged to the decode budget
				// and grows with the page; the rest must not.
				allocs[i] = after.TotalAlloc - before.TotalAlloc - uint64(len(tc.op)*n)
				t.Logf("%d operators: %v, %d MiB allocated besides the content", n, elapsed, allocs[i]>>20)
				var re *core.RedactionError
				if !errors.As(err, &re) || re.Reason != core.RedactionBudgetExceeded {
					t.Fatalf("RedactText = %v, want a budget_exceeded refusal", err)
				}
				if elapsed > 20*time.Second {
					t.Errorf("refusal took %v, want it bounded by the budget", elapsed)
				}
				// A ceiling above the measured cost, far below what an
				// unbounded scan of these pages allocates.
				if allocs[i] > 2<<30 {
					t.Errorf("allocated %d MiB, want at most 2 GiB", allocs[i]>>20)
				}
			}
			if float64(allocs[1]) > 1.25*float64(allocs[0]) {
				t.Errorf("twice the operators allocated %d MiB against %d MiB: memory grows with the content, not the budget",
					allocs[1]>>20, allocs[0]>>20)
			}
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
	doc, err := core.OpenBytes(classicPDF(objs))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDoS_RedactSharedUnitWalkedOnce: pages that are not redacted but share
// objects with removed content are walked to see what they paint, with one
// memo for the whole call, a per-page budget, and a 32 Mi call budget (batch
// C reviews SF-1). A form shared by 40 pages is walked once, whether the
// pages share one resource dictionary or each holds a copy with the same
// values (before, 40 pages of a 3 M-operator form took 47 s). A distinct
// 3 M-operator form per page is walked per page, and the call budget refuses
// it after a few pages.
func TestDoS_RedactSharedUnitWalkedOnce(t *testing.T) {
	skipUnlessTimed(t)
	for _, tc := range []struct {
		mode   string
		refuse bool
	}{
		{"shared", false},
		{"copies", false},
		{"distinct", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			doc := sharedFormDoc(t, 40, 3_000_000, tc.mode)
			if err := doc.RedactArea(0, core.Rect{X: 340, Y: 340, Width: 5, Height: 5}); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err := doc.RedactAndFlatten()
			elapsed := time.Since(start)
			t.Logf("%v, err %v", elapsed, err)
			var re *core.RedactionError
			if got := errors.As(err, &re) && re.Reason == core.RedactionBudgetExceeded; got != tc.refuse {
				t.Errorf("RedactAndFlatten err = %v, want a budget refusal: %v", err, tc.refuse)
			}
			if !tc.refuse && err != nil {
				t.Fatalf("RedactAndFlatten: %v", err)
			}
			if elapsed > 15*time.Second {
				t.Errorf("took %v, want the walk bounded by the call budget", elapsed)
			}
		})
	}
}

// reachDoc is the shape of the batch C review round 5 REACH probe: k images
// removed under the region on page 0, page i (1…k) painting image i-1
// elsewhere, and one shared resource dictionary that also lists m filler
// dictionaries each referring to the last image, so the set of undecided
// removed objects shrinks one image per page while every page can reach it.
func reachDoc(t *testing.T, k, m int) *core.Document {
	t.Helper()
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
	doc, err := core.OpenBytes(classicPDF(objs))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestDoS_RedactReachabilityBounded: deciding which removed objects pages
// that are not redacted still use costs O(graph · log |removed|), not a full
// reachability pass per decided object (batch C review round 5, S-1: k = 2000,
// m = 100 000 took 63 s before).
func TestDoS_RedactReachabilityBounded(t *testing.T) {
	skipUnlessTimed(t)
	doc := reachDoc(t, 2000, 100_000)
	if err := doc.RedactArea(0, core.Rect{X: 340, Y: 340, Width: 0.5, Height: 0.5}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := doc.RedactAndFlatten(); err != nil {
		t.Fatalf("RedactAndFlatten: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v, want the reachability passes bounded", d)
	}
}

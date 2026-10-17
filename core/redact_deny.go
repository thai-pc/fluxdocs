package core

import (
	"errors"
	"fmt"
	"sort"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact_deny.go keeps what redaction removed under a region out of the
// output when other routes still reference it (docs/design/
// redaction-hardening.md §6.10, batch C corrections C36, C41, C47–C49). A rewritten
// page gets page-private resources without the removed image, form, pattern
// or soft mask, but the same object can still be reached through a resource
// dictionary the page shares with a page that is not redacted, a kept form's
// or font's resources, a thumbnail, and so on.
//
// Invariant: the objects denied build-wide are exactly the objects that only
// removed content used — never one that a conforming binding of a kept lookup
// resolves (the innermost scope defining the name, as readers resolve it) —
// and the resource dictionaries holding them are never denied. A shared resource
// dictionary is copied as usual, and its entries for denied objects become
// null.
//
// It is computed as D \ P over leaf objects, where neither closure follows a
// /Resources entry (a resource dictionary lists what a unit may use, not what
// it uses; the names a unit actually uses are already leaves of D or P), and
// P does not follow entries of a kept object that paint nothing (/Alternates,
// /OPI, /Metadata, /PieceInfo, /AF):
//   - D: the objects only removed content looked up, through any candidate
//     binding (redact.Output.Removed), and what they reach;
//   - P: the objects kept content looks up and what they reach — for a
//     rewritten page the leaves its surviving content uses at any depth
//     (Output.Used), the values its private resources keep and its painted
//     page entries; for each page that is not redacted but can reach D, what
//     its content and its annotations' appearance streams look up
//     (redact.Painter), its content streams, its annotations and its
//     transparency group.
//
// Pages that are not redacted are walked only while some object of D is
// still undecided and only if they can reach one. A page that cannot be read
// because an object does not resolve, or that exceeds the redaction budget,
// refuses the redaction (never a guess that could under-remove).

// maxDenyGraph bounds the objects the deny computation visits.
const maxDenyGraph = 1 << 20

// errDenyGraph is the object-graph bound of the deny computation.
var errDenyGraph = fmt.Errorf("redaction deny graph exceeds %d objects", maxDenyGraph)

// redactionDenySet returns the objects to deny build-wide (see the file
// comment). outs maps each rewritten page to its rewrite.
//
// Returns a *RedactionError (budget_exceeded): on a page that is not redacted
// but shares objects with removed content and cannot be walked within the
// redaction budget, or with Page -1 when the objects to examine exceed
// maxDenyGraph.
func (d *Document) redactionDenySet(outs map[int]redact.Output) ([]int, error) {
	var removed []int
	for _, o := range outs {
		removed = append(removed, o.Removed...)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	r := d.resolver
	gone := map[int]bool{}
	if err := closeInto(r, gone, removed, closeD); err != nil {
		return nil, denyRefusal(-1, err)
	}

	kept := map[int]bool{}
	var keep []int
	for i, o := range outs {
		keep = append(keep, o.Used...)
		keep = appendRefs(keep, o.Resources)
		for _, k := range paintedPageEntries {
			keep = appendRefs(keep, d.pageInfos[i].Dict[k])
		}
	}
	if err := closeInto(r, kept, keep, closeP); err != nil {
		return nil, denyRefusal(-1, err)
	}
	// undecided are the removed objects no kept content uses yet.
	undecided := map[int]bool{}
	for n := range gone {
		if !kept[n] {
			undecided[n] = true
		}
	}

	// Pages that are not redacted and can reach an undecided object (the
	// copier follows everything, /Resources included): walk what they paint,
	// until nothing is undecided. Which pages can reach is recomputed only
	// when the undecided set shrinks.
	var roots [][]int
	var unmarked []int
	for i, pi := range d.pageInfos {
		if _, marked := outs[i]; marked {
			continue
		}
		unmarked = append(unmarked, i)
		roots = append(roots, pageRoots(pi))
	}
	g, err := buildGraph(r, roots)
	if err != nil {
		return nil, denyRefusal(-1, err)
	}
	painter := redact.NewPainter(r)
	// reaches over-approximates the objects that can reach an undecided one:
	// it is recomputed only when the undecided set has halved since, so the
	// recomputations cost O(graph · log |D|), charged to the call's budget.
	// A stale set is a superset, so a page it lets through is merely walked.
	reaches, visits := g.reachers(undecided)
	if err := painter.Charge(visits); err != nil {
		return nil, denyRefusal(-1, err)
	}
	sizeAtReach := len(undecided)
	for j, i := range unmarked {
		if len(undecided) == 0 {
			break
		}
		hit := false
		for _, n := range roots[j] {
			if reaches[n] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		pi := d.pageInfos[i]
		painted, err := painter.PaintedObjects(pi)
		var rf *redact.Refusal
		switch {
		case errors.As(err, &rf):
			return nil, denyRefusal(i, err)
		case err != nil:
			// An object of this page does not resolve, so what it paints is
			// unknown: refuse rather than guess (the error, which can quote
			// the file, is not reported).
			return nil, &RedactionError{Page: i, Reason: RedactionContentAmbiguous,
				Detail: "a page that shares objects with removed content cannot be read"}
		}
		keep = painted
		for _, k := range []parse.Name{"Contents", "Annots", "Group"} {
			keep = appendRefs(keep, pi.Dict[k])
		}
		if err := closeInto(r, kept, keep, closeP); err != nil {
			return nil, denyRefusal(-1, err)
		}
		for n := range undecided {
			if kept[n] {
				delete(undecided, n)
			}
		}
		if len(undecided) > 0 && len(undecided) <= sizeAtReach/2 {
			reaches, visits = g.reachers(undecided)
			if err := painter.Charge(visits); err != nil {
				return nil, denyRefusal(-1, err)
			}
			sizeAtReach = len(undecided)
		}
	}
	deny := make([]int, 0, len(undecided))
	for n := range undecided {
		deny = append(deny, n)
	}
	sort.Ints(deny)
	return deny, nil
}

// paintedPageEntries are the entries of a rewritten page, besides its
// resources and content, that the output keeps (parse.PageSource.Redacted).
var paintedPageEntries = []parse.Name{
	"Group", "MediaBox", "CropBox", "BleedBox", "TrimBox", "ArtBox", "Rotate", "UserUnit",
}

// denyRefusal reports a failure of the deny computation as a
// budget_exceeded *RedactionError: on page (a page that is not redacted), or
// -1 for the document as a whole (the object graph bound). The detail never
// quotes the file.
func denyRefusal(page int, err error) error {
	re := &RedactionError{Page: page, Reason: RedactionBudgetExceeded}
	var rf *redact.Refusal
	switch {
	case errors.As(err, &rf):
		re.Reason = RedactionReason(rf.Reason)
		re.Detail = "content shared with removed content cannot be checked: " + rf.Error()
		re.Err = rf.Err
	default:
		re.Detail = fmt.Sprintf("the objects shared with removed content exceed %d", maxDenyGraph)
	}
	return re
}

// pageRoots are the objects a copy of a page reaches directly: the
// references in its dictionary (but /Parent), and those in its resources
// when it inherits them (its own /Resources entry is among the former).
func pageRoots(pi parse.PageInfo) []int {
	var out []int
	for k, v := range pi.Dict {
		if k != "Parent" {
			out = appendRefs(out, v)
		}
	}
	if _, own := pi.Dict["Resources"]; !own {
		out = appendRefs(out, pi.Resources)
	}
	return out
}

// appendRefs appends the object numbers of the references in o (at any depth
// of a direct value) to dst.
func appendRefs(dst []int, o parse.Object) []int {
	return collectRefs(dst, o, closeAll)
}

// closeMode is which dictionary entries a closure follows.
type closeMode int

const (
	closeAll closeMode = iota // everything, as the copier copies
	closeD                    // not /Resources: what removed content used
	closeP                    // not /Resources, nor entries a reader does not paint
)

// unpainted are entries of a kept object that do not make it paint anything:
// a removed image listed there is denied (the entry becomes null) rather
// than kept.
var unpainted = map[parse.Name]bool{"Alternates": true, "OPI": true, "Metadata": true, "PieceInfo": true, "AF": true}

// collectRefs appends the references in o to dst, skipping the dictionary
// entries mode does not follow.
func collectRefs(dst []int, o parse.Object, mode closeMode) []int {
	var walk func(o parse.Object, depth int)
	walk = func(o parse.Object, depth int) {
		if depth > 64 {
			return
		}
		switch v := o.(type) {
		case parse.Reference:
			dst = append(dst, v.Number)
		case parse.Array:
			for _, e := range v {
				walk(e, depth+1)
			}
		case parse.Dict:
			for k, e := range v {
				if (k == "Resources" && mode != closeAll) || (mode == closeP && unpainted[k]) {
					continue
				}
				walk(e, depth+1)
			}
		case *parse.Stream:
			walk(v.Dict, depth+1)
		}
	}
	walk(o, 0)
	return dst
}

// refsOf resolves object num and returns the references it holds (as mode
// follows them), and whether it is a page object or page-tree node (never
// followed, as the copier never follows it).
func refsOf(r *parse.Resolver, num int, mode closeMode) ([]int, bool, error) {
	obj, err := r.Resolve(parse.Reference{Number: num})
	if err != nil {
		return nil, false, err
	}
	if parse.IsPageTreeObject(r, obj) {
		return nil, true, nil
	}
	return collectRefs(nil, obj, mode), false, nil
}

// closeInto adds roots and every object reachable from them (as mode
// follows), page objects excluded, to set; objects already in set are not
// followed again. An object that does not resolve is added and not followed.
func closeInto(r *parse.Resolver, set map[int]bool, roots []int, mode closeMode) error {
	queue := append([]int(nil), roots...)
	for len(queue) > 0 {
		n := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if set[n] {
			continue
		}
		refs, page, err := refsOf(r, n, mode)
		if page {
			continue
		}
		set[n] = true
		if len(set) > maxDenyGraph {
			return errDenyGraph
		}
		if err != nil {
			continue
		}
		queue = append(queue, refs...)
	}
	return nil
}

// graph is the objects reachable from the roots of the pages that are not
// redacted, as the copier follows them, with reverse edges.
type graph struct {
	seen map[int]bool
	rev  map[int][]int
}

// buildGraph walks everything reachable from roots (each a page's roots).
func buildGraph(r *parse.Resolver, roots [][]int) (*graph, error) {
	g := &graph{seen: map[int]bool{}, rev: map[int][]int{}}
	var queue []int
	push := func(n int) {
		if !g.seen[n] {
			g.seen[n] = true
			queue = append(queue, n)
		}
	}
	for _, rs := range roots {
		for _, n := range rs {
			push(n)
		}
	}
	for len(queue) > 0 {
		n := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if len(g.seen) > maxDenyGraph {
			return nil, errDenyGraph
		}
		refs, _, err := refsOf(r, n, closeAll)
		if err != nil {
			continue
		}
		for _, c := range refs {
			g.rev[c] = append(g.rev[c], n)
			push(c)
		}
	}
	return g, nil
}

// reachers returns the objects of g from which some object of targets is
// reachable, targets included, and the edges it visited.
func (g *graph) reachers(targets map[int]bool) (map[int]bool, int64) {
	out := map[int]bool{}
	var queue []int
	for n := range targets {
		if g.seen[n] {
			queue = append(queue, n)
		}
	}
	var visits int64
	for len(queue) > 0 {
		n := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visits++
		if out[n] {
			continue
		}
		out[n] = true
		queue = append(queue, g.rev[n]...)
	}
	return out, visits
}

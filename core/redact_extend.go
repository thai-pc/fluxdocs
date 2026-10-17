package core

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/thai-pc/fluxdocs/core/internal/redact"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// redact_extend.go is automatic plan extension (docs/design/
// redaction-hardening.md §6.11, decision D4): text the caller asked to remove
// must not survive anywhere else in the output. The needles are fixed once,
// from the caller's plan: every string RedactText or AutoRedactPII matched
// (4 or more characters of its skeleton — NFKC, no white-space or invisible
// characters, case kept) and the text of every run removed only because it
// met a RedactArea region (8 or more). Text removed by the extension itself
// never becomes a needle, so the extension terminates. Before the first
// build, a pre-pass searches every page's text, every annotation and the
// document information for them and extends the plan:
//   - a page that shows a needle gets the runs that hold it removed (with a
//     black box), and a property list holding one loses that key; the page
//     is then redacted like any other (D7, resource GC, the page-dictionary
//     allow-list). A needle in a form, tiling pattern, soft-mask group or
//     Type 3 glyph procedure is refused: Phase 1 does not rewrite those;
//   - an annotation holding a needle (its text, its field's values, its
//     appearance) is removed with its popup and its replies;
//   - a document information field holding a needle is not written.
//
// Whenever the plan is not empty, every file attachment annotation is
// removed: its content cannot be inspected (attachments policy). The
// verification (redact_verify.go) then re-reads the output; what it still
// finds extends the plan again, up to maxExtendRounds more builds. The
// source document's plan is never changed: the extension lives in one
// RedactAndFlatten call and is reported (RedactionReport).

// maxExtendRounds is how many more builds verification residuals may cause
// (§6.11): the pre-pass normally finds everything, round 1 is for what only
// the byte-level search sees, round 2 confirms.
const maxExtendRounds = 2

// maxRedactCallDecoded caps the bytes the pre-pass and verification decode
// in one RedactAndFlatten call, over all pages (§6.11, §7).
const maxRedactCallDecoded = 2 << 30

// The needle tags (redact.Needles): where a needle comes from.
const (
	tagMatch   uint8 = 1 << iota // a string RedactText or AutoRedactPII matched (propagates)
	tagArea                      // text removed under a RedactArea region (propagates)
	tagRemoved                   // other removed or stripped text (verification only)
)

// redactHooks are test hooks (set by the package's tests only): noPrepass
// skips the pre-pass, so verification has to find the occurrences;
// noopExtension makes verification residuals change nothing, so the rounds
// cannot converge.
var redactHooks struct {
	noPrepass     bool
	noopExtension bool
	// mutate, when set, changes each build's bytes before verification (to
	// seed the leaks verification must catch).
	mutate func([]byte) []byte
	// noVerify returns the first build unverified: with noPrepass, the
	// redaction without automatic extension and verification, which the
	// benchmarks compare against.
	noVerify bool
}

// redactPlan is the plan of one RedactAndFlatten call: the caller's, copied
// from the document, plus what automatic extension adds.
type redactPlan struct {
	regions  map[int][]Rect        // RedactArea regions: removal regions
	boxes    map[int][]Rect        // black boxes: regions, RedactText matches, extension runs
	marks    map[int][]redact.Mark // span marks: matches and extension runs and keys
	hygiene  map[int]bool          // pages redacted with nothing of their own to remove
	patterns map[int][]*regexp.Regexp
	user     map[int]bool // pages the caller's plan redacts

	annots    map[int]map[int]string // page -> index in its /Annots -> why the annotation is removed
	annotDeny map[int]bool           // the removed annotations (and popups), by object number
	info      map[string]bool        // /Info fields not written

	report  RedactionReport
	extIdx  map[extKey]int // report.Extensions index per (page, source, where)
	changed map[int]bool   // pages whose plan changed since their last rewrite
}

// extKey groups a report's extensions.
type extKey struct {
	page          int
	source, where string
}

// redacted reports whether page i is redacted (its content rewritten).
func (p *redactPlan) redacted(i int) bool {
	return len(p.boxes[i]) > 0 || len(p.marks[i]) > 0 || len(p.regions[i]) > 0 || p.hygiene[i]
}

// empty reports whether the caller planned nothing.
func (p *redactPlan) empty() bool { return len(p.user) == 0 }

// sourceOf names a needle source for the report.
func sourceOf(tags uint8) string {
	if tags&tagMatch != 0 {
		return "text-match"
	}
	return "area-text"
}

// extend adds an extension of page i: the marks, and per hit a black box
// (runs) and a report entry.
func (p *redactPlan) extend(i int, marks []redact.Mark, hits []redact.NeedleHit) {
	p.marks[i] = append(p.marks[i], marks...)
	p.changed[i] = true
	for _, h := range hits {
		where := "content"
		if h.Property {
			where = "marked-content-property"
		}
		e := p.extension(i, sourceOf(h.Tags), where)
		if !h.Property {
			rc := Rect{X: h.Rect.X, Y: h.Rect.Y, Width: h.Rect.Width, Height: h.Rect.Height}
			p.boxes[i] = append(p.boxes[i], rc)
			e.Rects = append(e.Rects, rc)
		}
	}
}

// hygieneFor makes page i redacted with nothing of its own to remove (page
// hygiene) when tags is not 0: what it holds but does not show held
// propagating needles of those sources.
func (p *redactPlan) hygieneFor(i int, tags uint8) {
	if tags == 0 || p.hygiene[i] {
		return
	}
	p.hygiene[i] = true
	p.changed[i] = true
	p.extension(i, sourceOf(tags), "page-hygiene")
}

// extension returns the report entry for (page, source, where), adding it.
func (p *redactPlan) extension(page int, source, where string) *RedactionExtension {
	k := extKey{page, source, where}
	i, ok := p.extIdx[k]
	if !ok {
		i = len(p.report.Extensions)
		p.extIdx[k] = i
		p.report.Extensions = append(p.report.Extensions, RedactionExtension{Page: page, Source: source, Where: where})
	}
	return &p.report.Extensions[i]
}

// needleSets are a call's needles.
type needleSets struct {
	// prop are the propagating needles (tagMatch, tagArea): fixed from the
	// caller's plan.
	prop *redact.Needles
	// all are every needle of at least redact.MinNeedle: prop, and the text
	// of every removed run and stripped property value (tagRemoved). They are
	// searched in objects only redacted pages reach and in orphans.
	all *redact.Needles
	// raw are the bytes removed runs showed, searched in the strings content
	// streams of those objects show.
	raw *redact.Needles

	propList []string // prop's skeletons, in order, with their tags
	propTags []uint8

	// read are the content streams (by their raw bytes) the pre-pass read
	// with fonts in the source: their text holds no propagating needle
	// outside the runs it extended to.
	read streamSet
}

// streamSet is a set of streams by their exact dictionary filters and raw
// bytes (SHA-256): a copy of a stream in the output has the same.
type streamSet map[[sha256.Size]byte]bool

// key is a stream's identity in a streamSet.
func streamKey(st *parse.Stream) [sha256.Size]byte {
	h := sha256.New()
	h.Write(parse.SerializeObject(st.Dict["Filter"]))
	h.Write([]byte{0})
	h.Write(parse.SerializeObject(st.Dict["DecodeParms"]))
	h.Write([]byte{0})
	h.Write(st.Raw)
	var k [sha256.Size]byte
	h.Sum(k[:0])
	return k
}

// has reports whether st's bytes are in the set.
func (s streamSet) has(st *parse.Stream) bool {
	return len(s) > 0 && s[streamKey(st)]
}

// addProp adds a propagating needle: text whose skeleton has at least min
// characters.
func (ns *needleSets) addProp(text string, tag uint8, min int) {
	sk := redact.Skeleton(text)
	if redact.SkeletonLen(sk) < min {
		return
	}
	ns.prop.Add(sk, tag)
	ns.propList = append(ns.propList, sk)
	ns.propTags = append(ns.propTags, tag)
}

// resetRemoved rebuilds all and raw from prop and the removed text of the
// rewritten pages outs.
func (ns *needleSets) resetRemoved(outs map[int]redact.Output) {
	ns.all, ns.raw = &redact.Needles{}, &redact.Needles{}
	for i, sk := range ns.propList {
		ns.all.Add(sk, ns.propTags[i])
	}
	add := func(texts []string) {
		for _, t := range texts {
			if sk := redact.Skeleton(t); redact.SkeletonLen(sk) >= redact.MinNeedle {
				ns.all.Add(sk, tagRemoved)
			}
		}
	}
	for _, o := range outs {
		add(o.RemovedTexts)
		add(o.Stripped)
		for _, c := range o.RemovedCodes {
			ns.raw.Add(string(c), tagRemoved)
		}
	}
}

// newRedactPlan copies the caller's plan. The plan's patterns all compiled
// when RedactText accepted them.
func newRedactPlan(boxes, regions map[int][]Rect, marks map[int][]redact.Mark, patterns map[int][]string) (*redactPlan, error) {
	p := &redactPlan{
		regions: regions, boxes: boxes, marks: marks,
		hygiene: map[int]bool{}, patterns: map[int][]*regexp.Regexp{}, user: map[int]bool{},
		annots: map[int]map[int]string{}, annotDeny: map[int]bool{}, info: map[string]bool{},
		extIdx: map[extKey]int{}, changed: map[int]bool{},
	}
	for i, pats := range patterns {
		for _, s := range pats {
			re, err := regexp.Compile(s)
			if err != nil {
				return nil, fmt.Errorf("%w: a planned pattern does not compile", ErrInternal)
			}
			p.patterns[i] = append(p.patterns[i], re)
		}
	}
	for i := range boxes {
		if p.redacted(i) {
			p.user[i] = true
		}
	}
	for i := range marks {
		if p.redacted(i) {
			p.user[i] = true
		}
	}
	return p, nil
}

// prepass extends the plan to every page that shows a propagating needle
// (§6.11): runs that hold one are removed, property-list keys that hold one
// are stripped. Each page is first read for its text only, leniently, as
// the renderer reads it (redact.TextScanner); a page that shows a needle is
// then read with the strict model, which places the runs to remove. models
// holds the strict model of each page already read (the caller's pages);
// outs their first rewrite, whose planned removals are not extensions. A
// page extended keeps its model in models.
//
// Returns a *RedactionError when a page that shows a needle cannot be
// redacted — the needle is in a unit Phase 1 does not rewrite, or the page
// is refused for another reason — when a page cannot be searched for
// certain (its content or a unit it paints does not decode, or the strict
// scanner reports a loss in it, behind which a lenient reading may hide
// text), or past the call's budget. The detail of a refusal of a page the
// caller did not plan starts with extensionDetail.
func (d *Document) prepass(plan *redactPlan, ns *needleSets, models map[int]*redact.Page, outs map[int]redact.Output, budget *callBudget) error {
	sc := redact.NewTextScanner(d.resolver, ns.prop)
	defer func() {
		budget.used += sc.Decoded
		ns.read = streamSet{}
		for st := range sc.Walked {
			ns.read[streamKey(st)] = true
		}
	}()
	for i, pi := range d.pageInfos {
		var done *redact.Output
		if o, ok := outs[i]; ok {
			done = &o
		}
		// A page already read with the strict model (marking, an earlier
		// call) is searched in that model: its reading succeeded, so no unit
		// it paints shows text and nothing in it is disputed.
		if page := d.cachedPage(i); page != nil {
			if rf := page.UnitHidden(ns.prop); rf != nil {
				return redactionScanError(i, rf)
			}
			marks, found := page.FindNeedles(ns.prop, done)
			if len(marks) > 0 {
				models[i] = page
				plan.extend(i, marks, found)
			}
			hidden := uint8(0)
			if done == nil {
				hidden = page.HiddenTags(ns.prop, nil)
				plan.hygieneFor(i, hidden)
			}
			if len(marks) == 0 && hidden == 0 && done == nil {
				sc.MarkRead(pi)
			}
			continue
		}
		hits, err := sc.Page(pi)
		if err := budget.spent(sc.Decoded); err != nil {
			return err
		}
		switch {
		case err != nil:
			return extensionScanError(i, err)
		case hits.Unit != nil:
			return redactionScanError(i, hits.Unit)
		case hits.Disputed != nil:
			// The page may show the removed text in a way the search could
			// not read: fail closed rather than leave it.
			return extensionRefusal(redactionScanError(i, hits.Disputed))
		}
		if done == nil {
			// Removed text in what the page holds but does not show
			// (comments, property lists): the page is rewritten, which
			// blanks and scrubs them.
			plan.hygieneFor(i, hits.Hidden)
		}
		if !hits.Content {
			continue
		}
		page := models[i]
		if page == nil {
			p, err := d.redactionPage(i)
			if err != nil {
				return extensionRefusal(err)
			}
			page = p
			budget.used += p.Decoded()
		}
		marks, found := page.FindNeedles(ns.prop, done)
		if len(marks) > 0 {
			models[i] = page
			plan.extend(i, marks, found)
		}
	}
	return nil
}

// extensionRefusal marks a refusal of a page automatic extension reaches:
// its detail starts with extensionDetail. Other errors are returned as they
// are.
func extensionRefusal(err error) error {
	var re *RedactionError
	if errors.As(err, &re) && !strings.HasPrefix(re.Detail, extensionDetail) {
		ext := *re
		ext.Detail = extensionDetail + re.Detail
		return &ext
	}
	return err
}

// extensionDetail starts the detail of every refusal automatic extension
// causes: the page refused is not one the caller planned.
const extensionDetail = "automatic extension: "

// extensionScanError reports a page that automatic extension or
// verification must search but cannot read: a budget refusal as such; any
// other failure (an object that does not resolve) as content_ambiguous —
// the page could hold the removed text. No file text is reported.
func extensionScanError(page int, err error) error {
	var rf *redact.Refusal
	if errors.As(err, &rf) {
		return redactionScanError(page, err)
	}
	return &RedactionError{Page: page, Reason: RedactionContentAmbiguous,
		Detail: extensionDetail + "a page that must be searched for the removed text cannot be read"}
}

// annotRef is one annotation of the source: its page, its index in the
// page's /Annots, its object number (0 when written directly) and dict.
type annotRef struct {
	page, idx, num int
	dict           parse.Dict
	subtype        string
}

// sourceAnnots lists every annotation of the source document.
func (d *Document) sourceAnnots() []annotRef {
	var out []annotRef
	r := d.resolver
	for i, pi := range d.pageInfos {
		v, err := r.Resolve(pi.Dict["Annots"])
		if err != nil {
			continue
		}
		arr, _ := v.(parse.Array)
		for j, a := range arr {
			ad, ok, err := r.ResolveDict(a)
			if err != nil || !ok {
				continue
			}
			ar := annotRef{page: i, idx: j, dict: ad}
			if ref, ok := a.(parse.Reference); ok {
				ar.num = ref.Number
			}
			if sub, err := r.Resolve(ad["Subtype"]); err == nil {
				if n, ok := sub.(parse.Name); ok {
					ar.subtype = string(n)
				}
			}
			out = append(out, ar)
		}
	}
	return out
}

// annotationPolicy removes, on the pages that are not redacted, every
// annotation that holds a propagating needle (scanned by sc; nil when there
// are none), and on every page every file attachment annotation — both with
// their popups and replies.
//
// Returns a *RedactionError (budget_exceeded) past a budget, or
// content_ambiguous when an annotation cannot be read.
func (d *Document) annotationPolicy(plan *redactPlan, sc *redact.TextScanner, budget *callBudget) error {
	all := d.sourceAnnots()
	removed := map[int]string{} // object number -> reason
	for _, ar := range all {
		switch {
		case ar.subtype == "FileAttachment":
			plan.removeAnnot(ar, "attachment-policy", removed)
		case sc != nil && !plan.redacted(ar.page):
			hit, err := sc.Annotation(d.pageInfos[ar.page], ar.dict)
			if err != nil {
				return extensionScanError(ar.page, err)
			}
			if err := budget.spent(sc.Decoded); err != nil {
				return err
			}
			if hit {
				plan.removeAnnot(ar, "needle", removed)
			}
		}
	}
	plan.closeAnnots(all, removed)
	return nil
}

// removeAnnot removes one annotation: from its page's /Annots, and by its
// number (and its popup's) from the whole build.
func (p *redactPlan) removeAnnot(ar annotRef, reason string, removed map[int]string) {
	if p.annots[ar.page] == nil {
		p.annots[ar.page] = map[int]string{}
	}
	if _, done := p.annots[ar.page][ar.idx]; done {
		return
	}
	p.annots[ar.page][ar.idx] = reason
	if ar.num > 0 {
		p.annotDeny[ar.num] = true
		removed[ar.num] = reason
	}
	if pop, ok := ar.dict["Popup"].(parse.Reference); ok {
		p.annotDeny[pop.Number] = true
		if _, ok := removed[pop.Number]; !ok {
			removed[pop.Number] = reason
		}
	}
	if ar.subtype == "FileAttachment" || !p.redacted(ar.page) {
		p.report.RemovedAnnotations = append(p.report.RemovedAnnotations,
			RemovedAnnotation{Page: ar.page, Subtype: reportSubtype(ar.subtype), Reason: reason})
	}
}

// closeAnnots removes, to a fixed point, every annotation that goes with a
// removed one: a popup whose parent is removed, a popup listed by a removed
// annotation, and a reply (/IRT) to a removed annotation.
func (p *redactPlan) closeAnnots(all []annotRef, removed map[int]string) {
	for changed := true; changed; {
		changed = false
		for _, ar := range all {
			if _, done := p.annots[ar.page][ar.idx]; done {
				continue
			}
			reason, ok := removed[ar.num]
			if !ok || ar.num == 0 {
				for _, k := range [...]parse.Name{"Parent", "IRT"} {
					if ref, isRef := ar.dict[k].(parse.Reference); isRef {
						if r, hit := removed[ref.Number]; hit && (k == "IRT" || ar.subtype == "Popup") {
							reason, ok = r, true
						}
					}
				}
			}
			if ok {
				p.removeAnnot(ar, reason, removed)
				changed = true
			}
		}
	}
}

// infoPolicy marks the document information fields that hold a propagating
// needle or match a RedactText pattern of the plan.
func (d *Document) infoPolicy(plan *redactPlan, ns *needleSets) {
	fields := map[string]string{"Title": d.Title, "Author": d.Metadata.Author, "Subject": d.Metadata.Subject}
	for k, v := range fields {
		if v == "" || plan.info[k] {
			continue
		}
		hit := ns.prop.FindText(v) != 0
		for _, pats := range plan.patterns {
			for _, re := range pats {
				hit = hit || re.MatchString(v)
			}
		}
		if hit {
			plan.removeInfo(k)
		}
	}
}

// finishReport adds to the report the annotations of each page the plan
// redacts that the caller did not plan: a redacted page keeps no annotation,
// so page hygiene (Reason "page-hygiene") and extension of a page's text
// ("page-redacted") drop them all. Annotations already reported are not
// repeated.
func (d *Document) finishReport(plan *redactPlan) RedactionReport {
	report := plan.report
	report.RemovedAnnotations = append([]RemovedAnnotation(nil), report.RemovedAnnotations...)
	for _, ar := range d.sourceAnnots() {
		if plan.user[ar.page] || !plan.redacted(ar.page) || ar.subtype == "FileAttachment" {
			continue
		}
		if _, done := plan.annots[ar.page][ar.idx]; done {
			continue
		}
		reason := "page-redacted"
		if plan.hygiene[ar.page] {
			reason = "page-hygiene"
		}
		report.RemovedAnnotations = append(report.RemovedAnnotations,
			RemovedAnnotation{Page: ar.page, Subtype: reportSubtype(ar.subtype), Reason: reason})
	}
	return report
}

// removeInfo marks a document information field as not written.
func (p *redactPlan) removeInfo(k string) {
	if p.info[k] {
		return
	}
	p.info[k] = true
	p.report.RemovedInfoFields = append(p.report.RemovedInfoFields, k)
	sort.Strings(p.report.RemovedInfoFields)
}

// callBudget is the decode budget of the pre-pass and verification of one
// RedactAndFlatten call.
type callBudget struct {
	used int64 // decoded by finished searches
}

// spent checks that used plus n (a search in progress) is within the cap.
func (b *callBudget) spent(n int64) error {
	if b.used+n > maxRedactCallDecoded {
		return &RedactionError{Page: -1, Reason: RedactionBudgetExceeded,
			Detail: extensionDetail + "searching the document for the removed text exceeds the redaction decode budget"}
	}
	return nil
}

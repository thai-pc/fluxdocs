package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// redact_report.go is the text-free account of what RedactAndFlatten removed
// beyond the caller's plan (docs/design/redaction-hardening.md §9 P8): text the
// caller asked to remove that also occurred elsewhere is removed there too
// (automatic plan extension, §6.11), and file attachments are dropped
// whenever a redaction runs. The report carries pages, rects, locations and
// counts only — never document text — so it is safe to log and to return
// from an API.

// RedactionReport describes what a redaction removed beyond the caller's
// plan. Get it from the document RedactAndFlatten returned
// (Document.RedactionReport).
type RedactionReport struct {
	// Extensions are the places the redaction was extended to because they
	// hold text the plan removed (a string RedactText or AutoRedactPII
	// matched, or text removed under a RedactArea region), one per page,
	// source and location.
	Extensions []RedactionExtension
	// RemovedAnnotations are the annotations the redaction removed beyond
	// the caller's plan: on pages the caller did not redact (including those
	// automatic extension redacted, which keep no annotation), and every file
	// attachment annotation. The annotations of the caller's own pages are
	// dropped too and not listed.
	RemovedAnnotations []RemovedAnnotation
	// RemovedInfoFields are the document information fields not written
	// (e.g. "Title"): they held text the plan removed, or matched a
	// RedactText pattern.
	RemovedInfoFields []string
}

// RedactionExtension is one page the redaction was extended to.
type RedactionExtension struct {
	Page  int    // 0-based page index
	Rects []Rect // the black boxes drawn for the removed runs (none for a property list or page hygiene)
	// Source is what the removed text was: "text-match" (a string RedactText
	// or AutoRedactPII matched, at least 4 characters) or "area-text" (text
	// removed under a RedactArea region, at least 8 characters).
	Source string
	// Where is what held it: "content" (text the page shows; the runs are
	// removed), "marked-content-property" (an /ActualText, /Alt or /E entry;
	// it is stripped) or "page-hygiene" (something the page carries but does
	// not show: an unused resource, /PieceInfo, a comment or a property-list
	// string in its content).
	//
	// Either way the page is then redacted like a page of the plan: it keeps
	// only the resources its remaining content uses and the page-dictionary
	// entries needed to paint it, every property-list string of its marked
	// content is stripped, and all its annotations are dropped (each
	// listed in RemovedAnnotations). Page hygiene draws no black box.
	Where string
}

// RemovedAnnotation is one annotation the redaction removed.
type RemovedAnnotation struct {
	Page int // 0-based page index
	// Subtype is the annotation's /Subtype (e.g. "Text", "Widget",
	// "FileAttachment"), or "Unknown" for one that is not a standard
	// annotation type.
	Subtype string
	// Reason is "needle" (it, its field or its appearance held text the
	// plan removed, or an appearance whose text cannot be read for certain;
	// its popup and its replies go with it), "attachment-policy" (a file
	// attachment, whose content redaction cannot inspect, is dropped
	// whenever a redaction runs), "page-hygiene" or "page-redacted" (its page
	// was redacted by automatic extension — page hygiene, or text the page
	// shows — and a redacted page keeps no annotation).
	Reason string
}

// RedactionReport returns what the redaction that produced d removed
// beyond the caller's plan, and true; for any other document it returns
// false. The report holds no document text.
func (d *Document) RedactionReport() (RedactionReport, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.redactionReport == nil {
		return RedactionReport{}, false
	}
	r := *d.redactionReport
	r.Extensions = append([]RedactionExtension(nil), r.Extensions...)
	for i := range r.Extensions {
		r.Extensions[i].Rects = append([]Rect(nil), r.Extensions[i].Rects...)
	}
	r.RemovedAnnotations = append([]RemovedAnnotation(nil), r.RemovedAnnotations...)
	r.RemovedInfoFields = append([]string(nil), r.RemovedInfoFields...)
	return r, true
}

// annotationSubtypes are the annotation types of ISO 32000-1 Table 169: a
// report names only these, never a name the file chose.
var annotationSubtypes = map[string]bool{
	"Text": true, "Link": true, "FreeText": true, "Line": true, "Square": true, "Circle": true,
	"Polygon": true, "PolyLine": true, "Highlight": true, "Underline": true, "Squiggly": true,
	"StrikeOut": true, "Stamp": true, "Caret": true, "Ink": true, "Popup": true,
	"FileAttachment": true, "Sound": true, "Movie": true, "Widget": true, "Screen": true,
	"PrinterMark": true, "TrapNet": true, "Watermark": true, "3D": true, "Redact": true,
}

// reportSubtype is an annotation subtype as a report may name it.
func reportSubtype(s string) string {
	if annotationSubtypes[s] {
		return s
	}
	return "Unknown"
}

// maxSummaryPages caps the page numbers Summary lists.
const maxSummaryPages = 10

// Summary is a one-line account of the report for a log or a CLI, with no
// document text: what automatic extension removed (places, pages,
// annotations, information fields) and the file attachments the
// attachments policy dropped. Page numbers are 0-based indices; at most
// maxSummaryPages are listed. It is "" for an empty report.
func (r RedactionReport) Summary() string {
	var parts []string
	if len(r.Extensions) > 0 {
		pages := make([]int, len(r.Extensions))
		for i, e := range r.Extensions {
			pages[i] = e.Page
		}
		parts = append(parts, fmt.Sprintf("extended to %d place(s) on page(s) %s", len(r.Extensions), pageList(pages)))
	}
	var annots, attachments []int
	for _, a := range r.RemovedAnnotations {
		if a.Reason == "attachment-policy" {
			attachments = append(attachments, a.Page)
		} else {
			annots = append(annots, a.Page)
		}
	}
	if len(annots) > 0 {
		parts = append(parts, fmt.Sprintf("removed %d annotation(s) on page(s) %s", len(annots), pageList(annots)))
	}
	if len(r.RemovedInfoFields) > 0 {
		parts = append(parts, "removed document information field(s) "+strings.Join(r.RemovedInfoFields, ", "))
	}
	var out []string
	if len(parts) > 0 {
		out = append(out, "automatic extension: "+strings.Join(parts, "; "))
	}
	if len(attachments) > 0 {
		out = append(out, fmt.Sprintf("attachments policy: removed %d file attachment(s) on page(s) %s",
			len(attachments), pageList(attachments)))
	}
	return strings.Join(out, "; ")
}

// pageList formats the distinct pages, in order, at most maxSummaryPages.
func pageList(pages []int) string {
	seen := map[int]bool{}
	var distinct []int
	for _, p := range pages {
		if !seen[p] {
			seen[p] = true
			distinct = append(distinct, p)
		}
	}
	sort.Ints(distinct)
	var b strings.Builder
	for i, p := range distinct {
		if i == maxSummaryPages {
			fmt.Fprintf(&b, " and %d more", len(distinct)-maxSummaryPages)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(p))
	}
	return b.String()
}

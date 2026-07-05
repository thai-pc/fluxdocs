package core

import "github.com/thai-pc/fluxdocs/core/docops/compare"

// compare.go implements Document.Compare: a word-level text comparison of two
// documents, page by page (Pro). It reuses the text extractor and a pure-Go LCS
// diff; it reports what text was added/removed/unchanged, not a visual redline.

// Compare returns a per-page word diff between d (the original) and other (the
// revision). Pages are aligned by index; a page present in only one document is
// reported as all-inserted or all-deleted. Neither document is modified.
//
// Returns ErrInvalidPDF if either document was not opened from bytes.
func (d *Document) Compare(other *Document) (*CompareResult, error) {
	if other == nil || d.resolver == nil || other.resolver == nil {
		return nil, ErrInvalidPDF
	}

	pages := d.PageCount
	if other.PageCount > pages {
		pages = other.PageCount
	}

	result := &CompareResult{Identical: true, Pages: make([]PageDiff, pages)}
	for i := 0; i < pages; i++ {
		segs := compare.Words(pageText(d, i), pageText(other, i))
		pd := PageDiff{PageIndex: i, Identical: true}
		for _, s := range segs {
			op := DiffEqual
			switch s.Op {
			case compare.Insert:
				op = DiffInsert
				pd.Identical = false
			case compare.Delete:
				op = DiffDelete
				pd.Identical = false
			}
			pd.Segments = append(pd.Segments, DiffSegment{Op: op, Text: s.Text})
		}
		if !pd.Identical {
			result.Identical = false
		}
		result.Pages[i] = pd
	}
	return result, nil
}

// pageText extracts one page's text, or "" if the page is out of range or fails.
func pageText(d *Document, i int) string {
	if i < 0 || i >= d.PageCount {
		return ""
	}
	t, err := d.ExtractTextByPage(i, ExtractOptions{})
	if err != nil {
		return ""
	}
	return t
}

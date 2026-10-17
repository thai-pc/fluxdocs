package redact

import (
	"regexp"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file holds the bare-content entry points the engine's tests use (the
// pre-1.8 RedactContent, FindTextRects and CheckFormXObjects, unexported by
// step 1.8: nothing outside the engine needs them).

// redactContent removes every text-showing operator of a bare content stream
// whose box meets any rect, and appends a solid black rectangle over each
// rect. With no resources, text is decoded as Latin-1 and glyph widths are
// bounded by the em defaults. If rects is empty it returns the content
// unchanged.
func redactContent(content []byte, rects []Rect) ([]byte, error) {
	if len(rects) == 0 {
		return content, nil
	}
	p, err := analyzeContent(content)
	if err != nil {
		return nil, err
	}
	out, err := p.Rewrite(rects, rects, nil)
	if err != nil {
		return nil, err
	}
	return out.Content, nil
}

// findTextRects returns the page-space boxes of the runs of a bare content
// stream whose text matches pattern (see Page.Match).
func findTextRects(content []byte, pattern *regexp.Regexp) ([]Rect, error) {
	p, err := analyzeContent(content)
	if err != nil {
		return nil, err
	}
	return p.Match(pattern).Rects, nil
}

// checkFormXObjects reports whether content, painted with the given
// resources, paints any other content stream that shows text (see walk.go).
func checkFormXObjects(r *parse.Resolver, resources parse.Dict, content []byte) error {
	_, err := AnalyzePage(r, parse.PageInfo{Resources: resources}, parse.ContentSegments{Data: content})
	return err
}

// analyzeContent is AnalyzePage for a bare content stream with no resources:
// fonts are unknown, so text decodes as Latin-1 and widths are bounded, and
// nothing is invoked.
func analyzeContent(content []byte) (*Page, error) {
	return AnalyzePage(nil, parse.PageInfo{MediaBox: parse.Rectangle{LLX: -1e6, LLY: -1e6, URX: 1e6, URY: 1e6}},
		parse.ContentSegments{Data: content})
}

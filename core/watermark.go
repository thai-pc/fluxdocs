package core

import (
	"math"
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// watermark.go stamps a translucent, rotated text watermark over pages and
// returns a new document (Pro). The watermark is baked into each target page's
// content stream via the writer overlay (a new content stream + injected
// Helvetica font and constant-alpha ExtGState in a page-private resources
// clone), so it survives in any viewer. The source document is unchanged.
//
// The watermark uses the standard-14 Helvetica font; FluxDocs's own raster
// renderer does not yet draw non-embedded fonts, so the watermark appears in
// standard PDF viewers (and in ExtractText) but not yet in RenderPage — a
// limitation of the renderer, not the watermark.

const (
	watermarkFontName = "FdWMHelv"
	watermarkGSName   = "FdWMgs"
	// avgGlyphEm estimates a glyph's advance for centering the text.
	avgGlyphEm = 0.5
)

// Watermark stamps opts.Text over the target pages and returns a new document.
//
// Returns ErrInvalidPDF if the document was not opened from bytes,
// ErrInvalidArgument if the text is empty, ErrPageNotFound if a page index is
// out of range, or a build/re-open error.
func (d *Document) Watermark(opts WatermarkOptions) (_ *Document, err error) {
	defer guard("Watermark", -1, &err)
	if d.resolver == nil || len(d.pageInfos) != len(d.Pages) {
		return nil, ErrInvalidPDF
	}
	if strings.TrimSpace(opts.Text) == "" {
		return nil, ErrInvalidArgument
	}

	targets, err := d.watermarkTargets(opts.Pages)
	if err != nil {
		return nil, err
	}

	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 48
	}
	opacity := opts.Opacity
	if opacity <= 0 {
		opacity = 0.3
	}
	rotation := opts.Rotation
	if rotation == 0 {
		rotation = 45
	}
	r, g, b := watermarkColor(opts.Color)

	fonts := map[parse.Name]parse.Dict{
		watermarkFontName: {
			"Type":     parse.Name("Font"),
			"Subtype":  parse.Name("Type1"),
			"BaseFont": parse.Name("Helvetica"),
		},
	}
	exts := map[parse.Name]parse.Dict{
		watermarkGSName: {
			"Type": parse.Name("ExtGState"),
			"ca":   parse.Real(clampOpacity(opacity)),
			"CA":   parse.Real(clampOpacity(opacity)),
		},
	}

	sources := make([]parse.PageSource, len(d.pageInfos))
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}
		if targets[i] {
			src.AppendContent = watermarkContent(d.pageInfos[i].MediaBox, opts.Text, fontSize, rotation, r, g, b)
			src.Fonts = fonts
			src.ExtGStates = exts
		}
		sources[i] = src
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return nil, err
	}
	return OpenBytes(built)
}

// watermarkTargets resolves the target page set. An empty list means all pages;
// an out-of-range index is an error.
func (d *Document) watermarkTargets(pages []int) (map[int]bool, error) {
	targets := map[int]bool{}
	if len(pages) == 0 {
		for i := range d.pageInfos {
			targets[i] = true
		}
		return targets, nil
	}
	for _, p := range pages {
		if p < 0 || p >= len(d.pageInfos) {
			return nil, ErrPageNotFound
		}
		targets[p] = true
	}
	return targets, nil
}

// watermarkContent generates the content-stream operators that draw the text
// centered on the page and rotated by `rotation` degrees, at the given color.
// Alpha and font come from the injected /Resources entries.
func watermarkContent(mb parse.Rectangle, text string, fontSize, rotation, r, g, b float64) []byte {
	cx := (mb.LLX + mb.URX) / 2
	cy := (mb.LLY + mb.URY) / 2
	rad := rotation * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)

	// Offset so the text is centered on the pivot before rotation.
	halfWidth := float64(len([]rune(text))) * avgGlyphEm * fontSize / 2
	baseline := -fontSize * 0.35

	var s strings.Builder
	s.WriteString("q ")
	s.WriteByte('/')
	s.WriteString(watermarkGSName)
	s.WriteString(" gs ")
	// Rotation + translate-to-center matrix (row-vector: origin -> center).
	writeWMNums(&s, cos, sin, -sin, cos, cx, cy)
	s.WriteString("cm ")
	writeWMNums(&s, r, g, b)
	s.WriteString("rg BT /")
	s.WriteString(watermarkFontName)
	s.WriteByte(' ')
	s.WriteString(formatNum(fontSize))
	s.WriteString(" Tf ")
	writeWMNums(&s, -halfWidth, baseline)
	s.WriteString("Td (")
	s.WriteString(escapePDFLiteral(text))
	s.WriteString(") Tj ET Q\n")
	return []byte(s.String())
}

// watermarkColor parses a hex color to 0..1 RGB, defaulting to a neutral gray.
func watermarkColor(hex string) (r, g, b float64) {
	if hex == "" {
		return 0.5, 0.5, 0.5
	}
	return annotationColorRGB(hex)
}

func writeWMNums(s *strings.Builder, vals ...float64) {
	for _, v := range vals {
		s.WriteString(formatNum(v))
		s.WriteByte(' ')
	}
}

// escapePDFLiteral escapes the characters special to a PDF literal string:
// backslash and the parentheses.
func escapePDFLiteral(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

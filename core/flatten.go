package core

import (
	"strconv"
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// flatten.go implements FlattenAnnotations: it bakes the FluxDocs annotation
// layer permanently into the page content streams and returns a NEW document.
// Unlike the render overlay (core/render), this writes PDF content operators (in
// PDF user space) into each page via the writer, so the annotations become part
// of the PDF and can no longer be edited. The annotations are generated in user
// space, so no device matrix is involved.

// annotationExtGStatePrefix names the injected constant-alpha graphics states.
const annotationExtGStatePrefix = "FdGS"

// noteMarkerSize is the side, in points, of the flattened note marker square.
const noteMarkerSize = 10.0

// maxFlattenPoints bounds how many freehand points one draw annotation bakes
// into the content stream, so a hostile imported Points list cannot balloon the
// output (SECURITY.md §2–3). Mirrors the render overlay's cap.
const maxFlattenPoints = 100000

// FlattenAnnotations bakes annotations into the PDF content and returns a new
// document — flattened annotations can no longer be edited.
//
// The four Wave-1 drawable types (highlight, note, draw, shape) are written into
// each page's content stream in PDF user space; deferred types (stamp/redact/
// signature) are ignored. The returned document is re-opened from the serialized
// bytes, so its own annotation layer is empty (the annotations now live in the
// content). The source document is unchanged.
//
// Returns ErrInvalidPDF if the document was not opened from bytes/a file (a
// resolver-less document cannot be serialized), or a build/re-open error.
func (d *Document) FlattenAnnotations() (*Document, error) {
	if d.resolver == nil || len(d.pageInfos) != len(d.Pages) {
		return nil, ErrInvalidPDF
	}

	sources := make([]parse.PageSource, len(d.pageInfos))
	for i := range d.pageInfos {
		src := parse.PageSource{Resolver: d.resolver, Page: d.pageInfos[i]}

		anns, err := d.GetAnnotations(d.Pages[i].ID)
		if err == nil && len(anns) > 0 {
			content, exts := flattenOps(anns)
			if len(content) > 0 {
				src.AppendContent = content
				src.ExtGStates = exts
			}
		}
		sources[i] = src
	}

	built, err := parse.BuildDocument(sources, buildInfoDict(d))
	if err != nil {
		return nil, err
	}
	return OpenBytes(built)
}

// flattenOps generates the PDF content-stream operators for one page's
// annotations (in PDF user space) plus the ExtGState dictionaries any
// translucent annotation needs. Returns empty content if nothing is drawable.
func flattenOps(anns []Annotation) ([]byte, map[parse.Name]parse.Dict) {
	var b strings.Builder
	exts := map[parse.Name]parse.Dict{}
	alphaNames := map[string]parse.Name{} // dedup ExtGState by alpha value

	// gsFor returns the ExtGState name to apply for opacity op (registering it on
	// first use), or "" when op is fully opaque (no state change needed).
	gsFor := func(op float64) parse.Name {
		a := clampOpacity(op)
		if a >= 1 {
			return ""
		}
		key := strconv.FormatFloat(a, 'f', 3, 64)
		if n, ok := alphaNames[key]; ok {
			return n
		}
		n := parse.Name(annotationExtGStatePrefix + strconv.Itoa(len(alphaNames)))
		alphaNames[key] = n
		exts[n] = parse.Dict{
			"Type": parse.Name("ExtGState"),
			"ca":   parse.Real(a),
			"CA":   parse.Real(a),
		}
		return n
	}

	for _, a := range anns {
		switch a.Type {
		case AnnotationHighlight:
			writeFilledRect(&b, a.Rect, a.Color, gsFor(a.Opacity))
		case AnnotationNote:
			r := Rect{X: a.Rect.X, Y: a.Rect.Y + a.Rect.Height - noteMarkerSize, Width: noteMarkerSize, Height: noteMarkerSize}
			writeFilledRect(&b, r, a.Color, gsFor(a.Opacity))
		case AnnotationShape:
			writeStrokedRect(&b, a.Rect, a.Color, 1.5, gsFor(a.Opacity))
		case AnnotationDraw:
			writePolyline(&b, a.Points, a.Color, 2, gsFor(a.Opacity))
		}
	}
	return []byte(b.String()), exts
}

// writeFilledRect emits `q [/GS gs] r g b rg x y w h re f Q`.
func writeFilledRect(b *strings.Builder, rect Rect, hexColor string, gs parse.Name) {
	r, g, bl := annotationColorRGB(hexColor)
	b.WriteString("q ")
	if gs != "" {
		b.WriteByte('/')
		b.WriteString(string(gs))
		b.WriteString(" gs ")
	}
	writeNums(b, r, g, bl)
	b.WriteString("rg ")
	writeNums(b, rect.X, rect.Y, rect.Width, rect.Height)
	b.WriteString("re f Q\n")
}

// writeStrokedRect emits `q [/GS gs] r g b RG width w x y w h re S Q`.
func writeStrokedRect(b *strings.Builder, rect Rect, hexColor string, width float64, gs parse.Name) {
	r, g, bl := annotationColorRGB(hexColor)
	b.WriteString("q ")
	if gs != "" {
		b.WriteByte('/')
		b.WriteString(string(gs))
		b.WriteString(" gs ")
	}
	writeNums(b, r, g, bl)
	b.WriteString("RG ")
	writeNums(b, width)
	b.WriteString("w ")
	writeNums(b, rect.X, rect.Y, rect.Width, rect.Height)
	b.WriteString("re S Q\n")
}

// writePolyline emits `q [/GS gs] r g b RG width w x0 y0 m x1 y1 l … S Q`. Fewer
// than two points draws nothing.
func writePolyline(b *strings.Builder, pts []Point, hexColor string, width float64, gs parse.Name) {
	if len(pts) < 2 {
		return
	}
	if len(pts) > maxFlattenPoints {
		pts = pts[:maxFlattenPoints]
	}
	r, g, bl := annotationColorRGB(hexColor)
	b.WriteString("q ")
	if gs != "" {
		b.WriteByte('/')
		b.WriteString(string(gs))
		b.WriteString(" gs ")
	}
	writeNums(b, r, g, bl)
	b.WriteString("RG ")
	writeNums(b, width)
	b.WriteString("w ")
	writeNums(b, pts[0].X, pts[0].Y)
	b.WriteString("m ")
	for _, p := range pts[1:] {
		writeNums(b, p.X, p.Y)
		b.WriteString("l ")
	}
	b.WriteString("S Q\n")
}

// writeNums writes each value formatted as a PDF number followed by a space.
func writeNums(b *strings.Builder, vals ...float64) {
	for _, v := range vals {
		b.WriteString(formatNum(v))
		b.WriteByte(' ')
	}
}

// formatNum formats a float as a PDF number: fixed-point (no scientific
// notation), trailing zeros and a trailing dot trimmed. Non-finite maps to 0.
func formatNum(f float64) string {
	if f != f || f > 1e308 || f < -1e308 { // NaN or ±Inf
		return "0"
	}
	s := strconv.FormatFloat(f, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-0" {
		s = "0"
	}
	return s
}

// annotationColorRGB parses "#rgb"/"#rrggbb" into RGB components in [0,1].
// Anything invalid falls back to the design-token amber #f59e0b.
func annotationColorRGB(hex string) (r, g, b float64) {
	ri, gi, bi, ok := parseHexRGB255(hex)
	if !ok {
		ri, gi, bi = 0xf5, 0x9e, 0x0b // amber
	}
	return float64(ri) / 255, float64(gi) / 255, float64(bi) / 255
}

// parseHexRGB255 decodes "#rgb" or "#rrggbb" (case-insensitive) into 0..255
// components. ok is false for any other form.
func parseHexRGB255(s string) (r, g, b uint8, ok bool) {
	if len(s) == 0 || s[0] != '#' {
		return 0, 0, 0, false
	}
	body := s[1:]
	switch len(body) {
	case 3:
		rn, ok1 := hexNib(body[0])
		gn, ok2 := hexNib(body[1])
		bn, ok3 := hexNib(body[2])
		if !ok1 || !ok2 || !ok3 {
			return 0, 0, 0, false
		}
		return rn * 17, gn * 17, bn * 17, true
	case 6:
		rh, ok1 := hexNib(body[0])
		rl, ok2 := hexNib(body[1])
		gh, ok3 := hexNib(body[2])
		gl, ok4 := hexNib(body[3])
		bh, ok5 := hexNib(body[4])
		bl, ok6 := hexNib(body[5])
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
			return 0, 0, 0, false
		}
		return rh<<4 | rl, gh<<4 | gl, bh<<4 | bl, true
	default:
		return 0, 0, 0, false
	}
}

func hexNib(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

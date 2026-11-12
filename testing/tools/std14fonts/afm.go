package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// afm is what the generator reads from an Adobe Font Metrics file (Adobe
// Technical Note #5004, version 4.1): the header's FontName and FontBBox,
// and per character metric line its code (C), width (WX) and name (N).
type afm struct {
	fontName string
	bbox     [4]int
	glyphs   []afmGlyph
}

type afmGlyph struct {
	code  int // -1: not encoded
	width int
	name  string
}

// parseAFM reads the StartCharMetrics section and the header keys above.
// Kerning and composite data are not needed.
func parseAFM(data []byte) (*afm, error) {
	a := &afm{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	inMetrics := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, rest, _ := strings.Cut(line, " ")
		switch {
		case key == "FontName":
			a.fontName = strings.TrimSpace(rest)
		case key == "FontBBox":
			f := strings.Fields(rest)
			if len(f) != 4 {
				return nil, fmt.Errorf("FontBBox %q", rest)
			}
			for i, s := range f {
				v, err := strconv.Atoi(s)
				if err != nil {
					return nil, err
				}
				a.bbox[i] = v
			}
		case key == "StartCharMetrics":
			inMetrics = true
		case key == "EndCharMetrics":
			inMetrics = false
		case inMetrics && key == "C":
			g := afmGlyph{code: -2, width: -1}
			for _, item := range strings.Split(line, ";") {
				f := strings.Fields(item)
				if len(f) < 2 {
					continue
				}
				switch f[0] {
				case "C":
					v, err := strconv.Atoi(f[1])
					if err != nil {
						return nil, err
					}
					g.code = v
				case "WX":
					v, err := strconv.Atoi(f[1])
					if err != nil {
						return nil, err
					}
					g.width = v
				case "N":
					g.name = f[1]
				}
			}
			if g.code < -1 || g.code > 255 || g.width < 0 || g.width > 0xFFFE || g.name == "" {
				return nil, fmt.Errorf("bad metric line %q", line)
			}
			a.glyphs = append(a.glyphs, g)
		}
	}
	if a.fontName == "" || len(a.glyphs) == 0 {
		return nil, fmt.Errorf("no FontName or metrics")
	}
	return a, sc.Err()
}

// parseAGL reads an Adobe Glyph List file (glyphlist.txt or
// zapfdingbats.txt): "name;XXXX[ XXXX…]" lines, # comments. A name listed
// on several lines has several Unicode values, in file order; a line with
// several code points is a sequence, which a single glyph cannot show, and
// is skipped.
func parseAGL(data []byte) (map[string][]rune, error) {
	out := map[string][]rune{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, codes, ok := strings.Cut(line, ";")
		if !ok {
			return nil, fmt.Errorf("bad AGL line %q", line)
		}
		f := strings.Fields(codes)
		if len(f) != 1 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 16, 32)
		if err != nil {
			return nil, fmt.Errorf("bad AGL line %q", line)
		}
		out[name] = append(out[name], rune(v))
	}
	return out, sc.Err()
}

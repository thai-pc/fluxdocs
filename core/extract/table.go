package extract

import (
	"sort"
	"strings"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// table.go detects tabular data on a page using the "stream" method: it collects
// each text-showing operator as a positioned run, groups runs into rows by their
// baseline Y, infers column anchors from the widest row, and assigns every run
// to a column. This finds grid-shaped layouts (the common case from Word/Excel/
// LaTeX exports) without needing ruled lines. It is a heuristic — one dominant
// grid per page — not a full table-structure recognizer.

// Table is a detected grid of text cells and its bounding box (minX, minY, maxX,
// maxY in PDF user space). Callers in the core package map it to core.Table.
type Table struct {
	Rows   [][]string
	Bounds [4]float64
}

// run is one text-showing operator's decoded text at its start position.
type run struct {
	x, y float64
	text string
}

// PageTables returns the tables detected on a page (empty when none look
// tabular).
func PageTables(r *parse.Resolver, pi parse.PageInfo, _ Options) ([]Table, error) {
	content, err := r.PageContent(pi)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, nil
	}
	runs := collectRuns(content, buildFonts(r, pi.Resources))
	if t, ok := detectTable(runs); ok {
		return []Table{t}, nil
	}
	return nil, nil
}

// collectRuns interprets the content stream and records each shown string with
// its baseline position. It tracks only the text line matrix translation, which
// is enough to place a run for row/column clustering.
func collectRuns(content []byte, fonts map[parse.Name]*font) []run {
	var (
		cur     *font
		tlm     = identity()
		leading float64
		runs    []run
	)
	emit := func(raw []byte) {
		txt := strings.TrimSpace(decode(cur, raw))
		if txt != "" {
			runs = append(runs, run{x: tlm[4], y: tlm[5], text: txt})
		}
	}
	for _, op := range parse.ScanContent(content) {
		switch op.Op {
		case "BT":
			tlm = identity()
		case "Tf":
			if len(op.Operands) >= 2 {
				if name, ok := op.Operands[len(op.Operands)-2].(parse.Name); ok {
					cur = fonts[name]
				}
			}
		case "TL":
			if len(op.Operands) >= 1 {
				leading = asFloat(op.Operands[len(op.Operands)-1])
			}
		case "Td":
			if len(op.Operands) >= 2 {
				tlm = mul(translate(asFloat(op.Operands[len(op.Operands)-2]), asFloat(op.Operands[len(op.Operands)-1])), tlm)
			}
		case "TD":
			if len(op.Operands) >= 2 {
				ty := asFloat(op.Operands[len(op.Operands)-1])
				leading = -ty
				tlm = mul(translate(asFloat(op.Operands[len(op.Operands)-2]), ty), tlm)
			}
		case "Tm":
			if len(op.Operands) >= 6 {
				var m [6]float64
				for i := 0; i < 6; i++ {
					m[i] = asFloat(op.Operands[len(op.Operands)-6+i])
				}
				tlm = m
			}
		case "T*":
			tlm = mul(translate(0, -leading), tlm)
		case "Tj":
			emit(lastString(op.Operands))
		case "'":
			tlm = mul(translate(0, -leading), tlm)
			emit(lastString(op.Operands))
		case "\"":
			tlm = mul(translate(0, -leading), tlm)
			emit(lastString(op.Operands))
		case "TJ":
			if arr, ok := lastArray(op.Operands); ok {
				var b strings.Builder
				for _, el := range arr {
					switch v := el.(type) {
					case parse.String:
						b.WriteString(decode(cur, v.Value))
					case parse.Integer:
						if float64(v) <= -tjSpaceThreshold {
							b.WriteByte(' ')
						}
					case parse.Real:
						if float64(v) <= -tjSpaceThreshold {
							b.WriteByte(' ')
						}
					}
				}
				if txt := strings.TrimSpace(b.String()); txt != "" {
					runs = append(runs, run{x: tlm[4], y: tlm[5], text: txt})
				}
			}
		}
	}
	return runs
}

// detectTable groups runs into a grid. It requires at least two rows and two
// columns, with most rows multi-celled, to qualify as a table.
func detectTable(runs []run) (Table, bool) {
	if len(runs) < 4 {
		return Table{}, false
	}
	rows := groupRows(runs)
	if len(rows) < 2 {
		return Table{}, false
	}

	// Column anchors come from the row with the most cells.
	var anchors []float64
	for _, row := range rows {
		if len(row) > len(anchors) {
			anchors = xsOf(row)
		}
	}
	if len(anchors) < 2 {
		return Table{}, false
	}

	grid := make([][]string, 0, len(rows))
	multi := 0
	minX, minY, maxX, maxY := runs[0].x, runs[0].y, runs[0].x, runs[0].y
	for _, row := range rows {
		cells := make([]string, len(anchors))
		filled := 0
		for _, rn := range row {
			c := nearestCol(anchors, rn.x)
			if cells[c] != "" {
				cells[c] += " " + rn.text
			} else {
				cells[c] = rn.text
				filled++
			}
			minX, maxX = min2(minX, rn.x), max2(maxX, rn.x)
			minY, maxY = min2(minY, rn.y), max2(maxY, rn.y)
		}
		if filled >= 2 {
			multi++
		}
		grid = append(grid, cells)
	}
	// Guard against a page of ordinary paragraphs: a real table has most rows
	// spanning multiple columns.
	if multi < 2 || multi*2 < len(rows) {
		return Table{}, false
	}
	return Table{Rows: grid, Bounds: [4]float64{minX, minY, maxX, maxY}}, true
}

// groupRows clusters runs sharing a baseline (within rowTol) into rows, ordered
// top-to-bottom; runs within a row are ordered left-to-right.
func groupRows(runs []run) [][]run {
	const rowTol = 3.0 // points

	sorted := append([]run(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].y > sorted[j].y })

	var rows [][]run
	for _, rn := range sorted {
		placed := false
		for i := range rows {
			if absf(rows[i][0].y-rn.y) <= rowTol {
				rows[i] = append(rows[i], rn)
				placed = true
				break
			}
		}
		if !placed {
			rows = append(rows, []run{rn})
		}
	}
	for i := range rows {
		sort.Slice(rows[i], func(a, b int) bool { return rows[i][a].x < rows[i][b].x })
	}
	return rows
}

func xsOf(row []run) []float64 {
	xs := make([]float64, len(row))
	for i, rn := range row {
		xs[i] = rn.x
	}
	sort.Float64s(xs)
	return xs
}

func nearestCol(anchors []float64, x float64) int {
	best, bestD := 0, absf(anchors[0]-x)
	for i := 1; i < len(anchors); i++ {
		if d := absf(anchors[i] - x); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
func min2(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

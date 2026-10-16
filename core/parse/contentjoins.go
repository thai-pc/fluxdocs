package parse

import (
	"bytes"
	"fmt"
)

// checkJoins reports the first join of ContentSegments that a reader could
// read differently from PageContent's newline. Readers disagree on a page with
// several /Contents streams: one concatenates them as they are, one separates
// them with white-space, one parses each stream on its own (ISO 32000-1
// §7.7.3.3 concatenates; §7.8.2 allows a split only between tokens). The
// readings agree only when every join falls between tokens and the bytes on
// either side of it could not form one token without the separator.
//
// It walks data with the lexer, which yields the tokens the strict scan read
// (it succeeded, so there was no token error), and skips each inline image to
// the end the scan computed (images: offset just past BI → offset past EI).
//
// Returns nil, or a wrapped errLex naming the join if it lies inside a token,
// a comment (which a separator would end) or an inline image, or between two
// bytes that would merge into one token.
func checkJoins(data []byte, joins []int, images map[int]int) error {
	for i, j := range joins {
		if j < 0 || j >= len(data) || (i > 0 && j <= joins[i-1]) {
			return fmt.Errorf("%w: content stream: invalid join at offset %d", errLex, j)
		}
	}
	lossAt := func(off int, what string) error {
		return fmt.Errorf("%w: content stream: %s at offset %d", errLex, what, off)
	}
	ji, pos := 0, 0
	for ji < len(joins) {
		tokStart := skipBlank(data, pos)
		// Joins in the white-space and comments before the next token. Joins
		// next to each other (an empty stream) are one boundary.
		for ji < len(joins) && joins[ji] < tokStart {
			a := joins[ji]
			b := a
			for ji+1 < len(joins) && joins[ji+1] == b+1 {
				ji++
				b++
			}
			ji++
			if what := gapJoinLoss(data, pos, a, b); what != "" {
				return lossAt(a, what)
			}
		}
		if ji >= len(joins) || tokStart >= len(data) {
			break
		}
		t, end, err := nextToken(data, tokStart)
		if err != nil {
			return lossAt(tokStart, "token error")
		}
		if t.kind == tokKeyword && t.kw == "BI" {
			e, ok := images[end]
			if !ok {
				return lossAt(tokStart, "inline image not delimited")
			}
			if joins[ji] < e {
				return lossAt(joins[ji], "an inline image spans two content streams")
			}
			end = e
		}
		if joins[ji] < end {
			return lossAt(joins[ji], "a token spans two content streams")
		}
		pos = end
	}
	return nil
}

// gapJoinLoss checks the run of joins [a, b] that lies in white-space and
// comments starting at gapStart. It returns a description of the loss if a
// comment runs up to the join, so that the separator ends it while plain
// concatenation would continue it into the next stream; or if the bytes on
// either side of the run would form one token without it (a name, number or
// keyword continued by a regular character, or "<<" / ">>"). Otherwise it
// returns "".
func gapJoinLoss(data []byte, gapStart, a, b int) string {
	line := gapStart
	if k := bytes.LastIndexAny(data[gapStart:a], "\r\n"); k >= 0 {
		line = gapStart + k + 1
	}
	if bytes.IndexByte(data[line:a], '%') >= 0 {
		return "a comment spans two content streams"
	}
	if a == 0 || b+1 >= len(data) {
		return ""
	}
	prev, next := data[a-1], data[b+1]
	if (isRegular(prev) || prev == '/') && isRegular(next) {
		return "a token spans two content streams"
	}
	if prev == next && (prev == '<' || prev == '>') {
		return "a token spans two content streams"
	}
	return ""
}

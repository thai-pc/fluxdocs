// Package compare computes a word-level diff between two pieces of text. It is a
// pure, dependency-free LCS diff used by Document.Compare to report what changed
// between two PDFs. It takes plain strings, so it does not import core.
package compare

import "strings"

// Op classifies a diff segment.
type Op int

const (
	Equal  Op = iota // present in both, unchanged
	Insert           // present only in the second text (added)
	Delete           // present only in the first text (removed)
)

// Segment is a run of words sharing one Op.
type Segment struct {
	Op   Op
	Text string
}

// maxTokens bounds the words diffed per side. The LCS table is O(n*m); beyond
// this a whole-block diff is returned instead of a fine one (an untrusted PDF's
// extracted text must not make the diff blow up — SECURITY.md §3).
const maxTokens = 4000

// Words returns the word-level diff turning a into b. Consecutive words with the
// same Op are merged into one Segment (joined by single spaces). Two identical
// texts yield a single Equal segment (or nothing when both are empty).
func Words(a, b string) []Segment {
	aw := strings.Fields(a)
	bw := strings.Fields(b)

	if len(aw) > maxTokens || len(bw) > maxTokens {
		return coarse(aw, bw)
	}
	return lcsDiff(aw, bw)
}

// lcsDiff runs the standard longest-common-subsequence diff over the word slices.
func lcsDiff(aw, bw []string) []Segment {
	n, m := len(aw), len(bw)
	// dp[i][j] = LCS length of aw[i:] and bw[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if aw[i] == bw[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var segs []Segment
	emit := func(op Op, w string) {
		if k := len(segs) - 1; k >= 0 && segs[k].Op == op {
			segs[k].Text += " " + w
		} else {
			segs = append(segs, Segment{Op: op, Text: w})
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case aw[i] == bw[j]:
			emit(Equal, aw[i])
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			emit(Delete, aw[i])
			i++
		default:
			emit(Insert, bw[j])
			j++
		}
	}
	for ; i < n; i++ {
		emit(Delete, aw[i])
	}
	for ; j < m; j++ {
		emit(Insert, bw[j])
	}
	return segs
}

// coarse is the fallback for very large inputs: equal if identical, otherwise a
// wholesale delete-then-insert.
func coarse(aw, bw []string) []Segment {
	if equalWords(aw, bw) {
		if len(aw) == 0 {
			return nil
		}
		return []Segment{{Op: Equal, Text: strings.Join(aw, " ")}}
	}
	var segs []Segment
	if len(aw) > 0 {
		segs = append(segs, Segment{Op: Delete, Text: strings.Join(aw, " ")})
	}
	if len(bw) > 0 {
		segs = append(segs, Segment{Op: Insert, Text: strings.Join(bw, " ")})
	}
	return segs
}

func equalWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

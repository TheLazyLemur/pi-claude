package main

import "strings"

// hunk is one run of lines in a unified diff.
type hunk struct {
	Kind string // "add", "del", "keep"
	Text string
	Old  int
	New  int
}

// unifiedDiff produces a compact line diff with three lines of context. It is a
// plain LCS walk: enough to show a reader what changed, not a git replacement.
func unifiedDiff(before, after string) []hunk {
	a := strings.Split(strings.TrimRight(before, "\n"), "\n")
	b := strings.Split(strings.TrimRight(after, "\n"), "\n")
	if before == "" {
		a = nil
	}

	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}

	var all []hunk
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			all = append(all, hunk{Kind: "keep", Text: a[i], Old: i + 1, New: j + 1})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			all = append(all, hunk{Kind: "del", Text: a[i], Old: i + 1})
			i++
		default:
			all = append(all, hunk{Kind: "add", Text: b[j], New: j + 1})
			j++
		}
	}
	for ; i < len(a); i++ {
		all = append(all, hunk{Kind: "del", Text: a[i], Old: i + 1})
	}
	for ; j < len(b); j++ {
		all = append(all, hunk{Kind: "add", Text: b[j], New: j + 1})
	}

	return trimContext(all, 3)
}

// trimContext drops long runs of unchanged lines, leaving n either side of a
// change so the reader keeps their bearings.
func trimContext(all []hunk, n int) []hunk {
	keep := make([]bool, len(all))
	for i, h := range all {
		if h.Kind == "keep" {
			continue
		}
		for k := max(0, i-n); k <= min(len(all)-1, i+n); k++ {
			keep[k] = true
		}
	}

	var out []hunk
	gap := false
	for i, h := range all {
		if keep[i] {
			if gap {
				out = append(out, hunk{Kind: "gap"})
				gap = false
			}
			out = append(out, h)
			continue
		}
		gap = len(out) > 0
	}
	return out
}

// countChanges reports added and removed line counts.
func countChanges(hunks []hunk) (added, removed int) {
	for _, h := range hunks {
		switch h.Kind {
		case "add":
			added++
		case "del":
			removed++
		}
	}
	return added, removed
}

package module

import (
	"sort"
	"strings"
)

// Suggest returns the candidates closest to s, for a "did you mean ...?" after a typo, or nil when none is close enough: at most
// 2 edits (insertions, deletions, substitutions or a swap of two neighbours), and fewer than half of s's length, so a short word
// never "matches" an unrelated one. Every candidate at the best distance is returned, sorted: with "-lst", "-last" and "-list"
// are equally likely and guessing one would mislead.
func Suggest(s string, candidates []string) []string {
	s = strings.ToLower(s)
	var best []string
	bestD := 3
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || strings.EqualFold(c, s) || seen[c] {
			continue
		}
		seen[c] = true
		d := distance(s, strings.ToLower(c))
		switch {
		case d*2 >= len(s) || d > bestD:
		case d < bestD:
			best, bestD = []string{c}, d
		default:
			best = append(best, c)
		}
	}
	sort.Strings(best)
	return best
}

// DidYouMean formats Suggest's answer: "did you mean: <prefix>a or <prefix>b?" ("" when there is nothing to propose).
func DidYouMean(prefix string, suggestions []string) string {
	if len(suggestions) == 0 {
		return ""
	}
	parts := make([]string, len(suggestions))
	for i, s := range suggestions {
		parts[i] = prefix + s
	}
	return "did you mean " + strings.Join(parts, " or ") + "?"
}

// distance is the optimal string alignment distance: Levenshtein plus the transposition of two adjacent characters as a single
// edit ("stauts" -> "status" is one), the most common typo.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

// Package suggest finds the closest match for a mistyped word, so error
// messages can say "did you mean ...?".
package suggest

import "strings"

// Closest returns the candidate closest to word, or "" when nothing is close
// enough to be a plausible typo.
func Closest(word string, candidates []string) string {
	word = strings.ToLower(word)
	best, bestDist := "", -1
	for _, c := range candidates {
		d := distance(word, strings.ToLower(c))
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	// Allow roughly one edit per three characters, and at least one.
	limit := max(1, len(word)/3)
	if bestDist < 0 || bestDist > limit {
		return ""
	}
	return best
}

// DidYouMean returns " (did you mean "x"?)" or "" when there is no close match.
func DidYouMean(word string, candidates []string) string {
	if s := Closest(word, candidates); s != "" {
		return ` (did you mean "` + s + `"?)`
	}
	return ""
}

// distance is the edit distance between a and b, counting insertions,
// deletions, substitutions and swaps of adjacent characters ("rakc" is one
// edit from "rack").
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

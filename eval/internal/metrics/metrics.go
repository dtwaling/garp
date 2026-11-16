// Package metrics computes retrieval quality without corpus or process I/O.
// File sets use map[string]bool with only true entries. Rankings may contain
// duplicates; only the first occurrence of each path contributes to its rank.
package metrics

// RecallAt is the fraction of gold files in the first k distinct results.
// Empty gold or a nonpositive k yields zero.
func RecallAt(gold map[string]bool, ranking []string, k int) float64 {
	if len(gold) == 0 || k <= 0 {
		return 0
	}
	hits := 0
	for _, file := range distinctTop(ranking, k) {
		if gold[file] {
			hits++
		}
	}
	return float64(hits) / float64(len(gold))
}

// MRRAt10 is the reciprocal rank of the first gold file in the first ten
// distinct results, or zero if there is no such result.
func MRRAt10(gold map[string]bool, ranking []string) float64 {
	for i, file := range distinctTop(ranking, 10) {
		if gold[file] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// Unjudged10 returns files among the first ten distinct results that are neither
// gold nor judged nonrelevant, in ranking order. Its length is unjudged@10;
// the paths allow reports to list the files still needing judgment.
func Unjudged10(gold, judgedNonrelevant map[string]bool, ranking []string) []string {
	var unjudged []string
	for _, file := range distinctTop(ranking, 10) {
		if !gold[file] && !judgedNonrelevant[file] {
			unjudged = append(unjudged, file)
		}
	}
	return unjudged
}

func distinctTop(ranking []string, k int) []string {
	var top []string
	seen := make(map[string]bool)
	for _, file := range ranking {
		if len(top) >= k {
			break
		}
		if !seen[file] {
			seen[file] = true
			top = append(top, file)
		}
	}
	return top
}

// Mean gives every query equal weight, for corpus and per-kind macro averages.
// An empty group yields zero.
func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

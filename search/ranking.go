package search

import (
	"sort"
)

// rankedInterval is one two-sided minimal qualifying occurrence window.
type rankedInterval struct {
	start int
	end   int
}

// rankedTextAnalysis is the authoritative Stage 2 text-ranking result.
type rankedTextAnalysis struct {
	Score           int
	TermCount       int
	MatchedTerms    []string
	BoundaryQuality int
	SpanLength      int
	start           int
	end             int
	bestIntervals   []rankedInterval
}

type rankedOccurrence struct {
	start           int
	end             int
	termIndex       int
	wholeIdentifier bool
}

type rankedWindow struct {
	score           int
	termCount       int
	boundaryQuality int
	primaryPresent  bool
	start           int
	end             int
	left            int
	right           int
}

type rankedBestWindows struct {
	score          int
	termCount      int
	representative rankedWindow
	haveBest       bool
	intervals      []rankedInterval
	seenIntervals  map[rankedInterval]struct{}
}

// analyzeRankedText finds every two-sided minimal qualifying window and then
// selects the representative by the Stage 2 ranking rules.
func analyzeRankedText(text string, query compiledQuery, distance int, strict bool) (rankedTextAnalysis, bool) {
	if len(query.matchers) == 0 {
		return rankedTextAnalysis{}, false
	}

	occurrences := rankedOccurrences(text, query)
	if len(occurrences) == 0 {
		return rankedTextAnalysis{}, false
	}

	latest := make([]int, len(query.terms))
	latestBoundary := make([]int, len(query.terms))
	for i := range latest {
		latest[i] = -1
		latestBoundary[i] = -1
	}
	endRanges := newRangeMaximum(occurrences)
	var best rankedBestWindows
	for right, occurrence := range occurrences {
		termIndex := occurrence.termIndex
		previousOccurrence := latest[termIndex]
		latest[termIndex] = right
		if occurrence.wholeIdentifier {
			latestBoundary[termIndex] = right
		}

		// In a two-sided minimal window, the left occurrence is the final
		// occurrence of its term, and the right occurrence is the first
		// occurrence of its term after that left edge.
		for _, left := range latest {
			if left < 0 || previousOccurrence >= left {
				continue
			}
			if occurrences[right].start-occurrences[left].start > distance {
				continue
			}

			window := buildRankedWindow(query, occurrences, latest, latestBoundary, endRanges, left, right)
			if !rankedWindowQualifies(window, len(query.terms), strict) {
				continue
			}
			best.observe(window)
		}
	}
	if !best.haveBest {
		return rankedTextAnalysis{}, false
	}

	sort.Slice(best.intervals, func(i, j int) bool {
		if best.intervals[i].start != best.intervals[j].start {
			return best.intervals[i].start < best.intervals[j].start
		}
		return best.intervals[i].end < best.intervals[j].end
	})

	representative := best.representative
	return rankedTextAnalysis{
		Score:           representative.score,
		TermCount:       representative.termCount,
		MatchedTerms:    rankedWindowTerms(query, occurrences, representative.left, representative.right),
		BoundaryQuality: representative.boundaryQuality,
		SpanLength:      representative.end - representative.start,
		start:           representative.start,
		end:             representative.end,
		bestIntervals:   best.intervals,
	}, true
}

func (best *rankedBestWindows) observe(window rankedWindow) {
	betterPair := !best.haveBest ||
		window.score > best.score ||
		(window.score == best.score && window.termCount > best.termCount)
	if betterPair {
		best.score = window.score
		best.termCount = window.termCount
		best.representative = window
		best.haveBest = true
		best.intervals = nil
		best.seenIntervals = make(map[rankedInterval]struct{})
	} else if window.score != best.score || window.termCount != best.termCount {
		return
	} else if rankedWindowBetter(window, best.representative) {
		best.representative = window
	}

	interval := rankedInterval{start: window.start, end: window.end}
	if _, seen := best.seenIntervals[interval]; seen {
		return
	}
	best.seenIntervals[interval] = struct{}{}
	best.intervals = append(best.intervals, interval)
}

func rankedWindowTerms(query compiledQuery, occurrences []rankedOccurrence, left, right int) []string {
	present := make([]bool, len(query.terms))
	for i := left; i <= right; i++ {
		present[occurrences[i].termIndex] = true
	}
	terms := make([]string, 0, len(query.terms))
	for i, term := range query.terms {
		if present[i] {
			terms = append(terms, term)
		}
	}
	return terms
}

func rankedOccurrences(text string, query compiledQuery) []rankedOccurrence {
	occurrences := make([]rankedOccurrence, 0)
	for termIndex, matcher := range query.matchers {
		for _, found := range matcher.findAll(text) {
			occurrences = append(occurrences, rankedOccurrence{
				start:           found.start,
				end:             found.end,
				termIndex:       termIndex,
				wholeIdentifier: hasWholeIdentifierEdges(text, found.start, found.end),
			})
		}
	}
	sort.Slice(occurrences, func(i, j int) bool {
		if occurrences[i].start != occurrences[j].start {
			return occurrences[i].start < occurrences[j].start
		}
		if occurrences[i].end != occurrences[j].end {
			return occurrences[i].end < occurrences[j].end
		}
		return occurrences[i].termIndex < occurrences[j].termIndex
	})
	return occurrences
}

func hasWholeIdentifierEdges(text string, start, end int) bool {
	if start < 0 || end < start || end > len(text) || start == end {
		return false
	}
	left := start == 0 || !isWordByte(text[start-1]) || isASCIIPunctuation(text[start])
	right := end == len(text) || !isWordByte(text[end]) || isASCIIPunctuation(text[end-1])
	return left && right
}

func isASCIIPunctuation(b byte) bool {
	return b >= '!' && b <= '~' && !isWordByte(b)
}

func buildRankedWindow(
	query compiledQuery,
	occurrences []rankedOccurrence,
	latest, latestBoundary []int,
	endRanges rangeMaximum,
	left, right int,
) rankedWindow {
	window := rankedWindow{
		start: occurrences[left].start,
		end:   endRanges.maximum(left, right+1),
		left:  left,
		right: right,
	}
	for termIndex, latestOccurrence := range latest {
		if latestOccurrence < left {
			continue
		}
		window.score += len(query.terms[termIndex])
		window.termCount++
		if termIndex == 0 {
			window.primaryPresent = true
		}
		if latestBoundary[termIndex] >= left {
			window.boundaryQuality++
		}
	}
	return window
}

func rankedWindowQualifies(window rankedWindow, requiredCount int, strict bool) bool {
	if !window.primaryPresent {
		return false
	}
	if strict || requiredCount <= 2 {
		return window.termCount == requiredCount
	}
	return window.termCount >= 2
}

func rankedWindowBetter(candidate, current rankedWindow) bool {
	if candidate.boundaryQuality != current.boundaryQuality {
		return candidate.boundaryQuality > current.boundaryQuality
	}
	candidateSpan := candidate.end - candidate.start
	currentSpan := current.end - current.start
	if candidateSpan != currentSpan {
		return candidateSpan < currentSpan
	}
	return candidate.start < current.start
}

// rangeMaximum supports exact span bounds when an earlier occurrence extends
// beyond the final occurrence in a window.
type rangeMaximum struct {
	size int
	tree []int
}

func newRangeMaximum(occurrences []rankedOccurrence) rangeMaximum {
	size := 1
	for size < len(occurrences) {
		size *= 2
	}
	tree := make([]int, size*2)
	for i, occurrence := range occurrences {
		tree[size+i] = occurrence.end
	}
	for i := size - 1; i > 0; i-- {
		tree[i] = maxInt(tree[i*2], tree[i*2+1])
	}
	return rangeMaximum{size: size, tree: tree}
}

func (r rangeMaximum) maximum(start, end int) int {
	start += r.size
	end += r.size
	maximum := 0
	for start < end {
		if start%2 == 1 {
			maximum = maxInt(maximum, r.tree[start])
			start++
		}
		if end%2 == 1 {
			end--
			maximum = maxInt(maximum, r.tree[end])
		}
		start /= 2
		end /= 2
	}
	return maximum
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

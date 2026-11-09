package search

import "strings"

// lineIndexer maps a byte offset in a string to its 1-based line number using a
// precomputed table of line-start offsets and a binary search.
type lineIndexer struct {
	starts []int // starts[k] = byte offset of the (k+1)-th line
}

func newLineIndexer(s string) *lineIndexer {
	starts := make([]int, 1, 1+strings.Count(s, "\n"))
	starts[0] = 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndexer{starts: starts}
}

// lineOf returns the 1-based line number containing byte offset off.
func (li *lineIndexer) lineOf(off int) int {
	if off < 0 {
		off = 0
	}
	lo, hi := 0, len(li.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if li.starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// computeExcerptSpanLinesQuery anchors excerpt metadata back to raw source
// lines. A value of 0 means unknown.
func computeExcerptSpanLinesQuery(raw string, spans []excerptSpan, query compiledQuery, window int) []int {
	out := make([]int, len(spans))
	if raw == "" || len(spans) == 0 || len(query.matchers) == 0 {
		return out
	}

	// Use the caller's proximity bound; only unspecified windows need a fallback.
	if window <= 0 {
		window = 800
	}

	li := newLineIndexer(raw)

	// Precompute occurrence offsets using the same partial-aware matcher as the engine so
	// anchors line up with matches.
	type termInfo struct {
		index   int
		offsets []int
	}
	infos := make([]termInfo, 0, len(query.matchers))
	for index, matcher := range query.matchers {
		locs := matcher.findAll(raw)
		offs := make([]int, len(locs))
		for k, l := range locs {
			offs[k] = l.start
		}
		// A term with no raw occurrences can never be an anchor; skip it here so the
		// per-excerpt loop below doesn't have to filter empty-offset entries.
		if len(offs) > 0 {
			infos = append(infos, termInfo{index: index, offsets: offs})
		}
	}

	// nearest returns the offset in offs closest to pos within window, if any.
	nearest := func(offs []int, pos int) (int, bool) {
		best, found, bestDist := 0, false, window+1
		for _, o := range offs {
			d := o - pos
			if d < 0 {
				d = -d
			}
			if d <= window && d < bestDist {
				best, bestDist, found = o, d, true
			}
		}
		return best, found
	}

	// cursor keeps multi-excerpt output in document order: each excerpt prefers an anchor
	// at or after the previous excerpt's anchor.
	cursor := 0
	for i, span := range spans {
		// Cleaning changes byte offsets but normally preserves per-term occurrence
		// order. Resolve the emitted matches by identity before using the legacy
		// coverage heuristic. Only trust ordinals when occurrence counts agree;
		// markup removal can otherwise shift them relative to the raw source.
		sourceCounts := make([]int, len(query.matchers))
		for _, match := range span.sourceMatches {
			sourceCounts[match.term]++
		}
		lo, exact := len(raw), len(span.matches) > 0
		for _, match := range span.matches {
			found := false
			for _, info := range infos {
				if info.index == match.term && match.occurrence > 0 &&
					match.occurrence <= len(info.offsets) && sourceCounts[match.term] == len(info.offsets) {
					lo = min(lo, info.offsets[match.occurrence-1])
					found = true
					break
				}
			}
			exact = exact && found
		}
		if exact {
			out[i] = li.lineOf(lo)
			cursor = lo + 1
			continue
		}
		// Determine which terms appear in this excerpt, and pick the rarest (fewest raw
		// occurrences) as the anchor -- rarer terms disambiguate position more reliably.
		var anchor *termInfo
		present := make([]*termInfo, 0, len(infos))
		presentTerms := make(map[int]struct{}, len(span.matches))
		for _, match := range span.matches {
			presentTerms[match.term] = struct{}{}
		}
		for j := range infos {
			if _, ok := presentTerms[infos[j].index]; ok {
				present = append(present, &infos[j])
				if anchor == nil || len(infos[j].offsets) < len(anchor.offsets) {
					anchor = &infos[j]
				}
			}
		}
		if anchor == nil {
			continue // no anchorable term -> leave lines unknown (0)
		}

		score := func(pos int) int {
			s := 0
			for _, p := range present {
				if _, ok := nearest(p.offsets, pos); ok {
					s++
				}
			}
			return s
		}

		// Choose the anchor occurrence (preferring positions at/after the cursor) whose
		// surrounding window clusters the most present terms.
		bestPos, bestScore := -1, -1
		for _, o := range anchor.offsets {
			if o < cursor {
				continue
			}
			if sc := score(o); sc > bestScore {
				bestScore, bestPos = sc, o
			}
		}
		if bestPos == -1 { // nothing at/after cursor; fall back to global best
			for _, o := range anchor.offsets {
				if sc := score(o); sc > bestScore {
					bestScore, bestPos = sc, o
				}
			}
		}
		if bestPos == -1 {
			continue
		}

		// Start line = the earliest present-term occurrence in the chosen cluster.
		lo = bestPos
		for _, p := range present {
			if o, ok := nearest(p.offsets, bestPos); ok && o < lo {
				lo = o
			}
		}
		out[i] = li.lineOf(lo)
		cursor = bestPos + 1
	}
	return out
}

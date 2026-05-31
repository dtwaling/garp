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

// computeExcerptLines anchors each excerpt back to the raw (uncleaned) file content and
// returns its 1-based start line (the line where the matched terms begin). A value of 0
// means "unknown" -- callers omit it from machine-readable output.
//
// Why anchoring rather than offset arithmetic: excerpts are produced from a cleaned,
// whitespace-collapsed, newline-stripped copy of the file, so an excerpt's byte offset in
// the cleaned string does not map to any offset in the raw bytes. The search terms, however,
// survive cleaning verbatim. So for each excerpt we locate the raw occurrence of its terms
// where they cluster together (which disambiguates files that repeat a term), and take the
// line of the earliest matched term in that cluster as the start line.
//
// Only meaningful for text/code files whose raw bytes carry stable source lines; callers must
// not invoke this for binary/extracted formats (PDF, DOCX, email) where source lines don't exist.
// window bounds how far apart clustered terms may be (typically the search distance).
func computeExcerptLines(raw string, excerpts []string, terms []string, window int) []int {
	out := make([]int, len(excerpts))
	if raw == "" || len(excerpts) == 0 || len(terms) == 0 {
		return out
	}

	// Clamp the clustering window: tight enough to stay local, wide enough to span a match.
	if window <= 0 || window > 4000 {
		window = 800
	}
	if window < 200 {
		window = 200
	}

	li := newLineIndexer(raw)

	// Precompute whole-word occurrence offsets per term, using the same matcher the engine
	// uses (case-insensitive, plural/smart-forms aware) so anchors line up with matches.
	type termInfo struct {
		term    string
		offsets []int
	}
	infos := make([]termInfo, 0, len(terms))
	for _, t := range terms {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		re := buildWordRegexCI(tt)
		locs := re.FindAllStringIndex(raw, -1)
		offs := make([]int, 0, len(locs))
		for _, l := range locs {
			offs = append(offs, l[0])
		}
		infos = append(infos, termInfo{term: tt, offsets: offs})
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
	for i, ex := range excerpts {
		// Determine which terms appear in this excerpt, and pick the rarest (fewest raw
		// occurrences) as the anchor -- rarer terms disambiguate position more reliably.
		var anchor *termInfo
		present := make([]*termInfo, 0, len(infos))
		for j := range infos {
			if len(infos[j].offsets) == 0 {
				continue
			}
			if buildWordRegexCI(infos[j].term).MatchString(ex) {
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
		lo := bestPos
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

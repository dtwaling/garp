package search

import (
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	bm25K1                  = 1.2
	bm25B                   = 0.75
	bm25PathSlotWeight      = 0.60
	proximityBoostWeight    = 0.35
	proximityScaleCap       = 200.0
	rarityMaxDFRatio        = 0.05
	fallbackScarcityK       = 5
	fallbackTailCap         = 3
	inlineSubsetIntervalCap = 64
)

// RoundScore is the common comparison and output precision. Invalid and
// nonpositive scores (including negative zero) have the canonical value zero.
func RoundScore(x float64) float64 {
	if x <= 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	rounded := math.Round(x*1e4) / 1e4
	if math.IsInf(rounded, 0) {
		return 0
	}
	return rounded
}

// FormatScore renders rounded scores without insignificant trailing zeroes.
func FormatScore(x float64) string { return strconv.FormatFloat(RoundScore(x), 'f', -1, 64) }

// canonicalFloatSum requires aligned values and unique canonical keys. Sorting
// keys before addition makes rounding independent of query argument order.
func canonicalFloatSum(vals []float64, keys []string) float64 {
	order := make([]int, len(vals))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return keys[order[i]] < keys[order[j]] })
	sum := 0.0
	for _, i := range order {
		sum += vals[i]
	}
	return sum
}

func canonicalSlotMaskWeight(query compiledQuery, mask []bool, weights []float64) float64 {
	vals := make([]float64, 0, len(mask))
	keys := make([]string, 0, len(mask))
	for s, present := range mask {
		if !present {
			continue
		}
		value := float64(len(query.slotLabel(s)))
		if len(weights) > 0 {
			value = weights[s]
		}
		vals = append(vals, value)
		keys = append(keys, query.slotLabel(s))
	}
	return canonicalFloatSum(vals, keys)
}

type corpusStats struct {
	N        int
	AvgDL    float64
	IDF      []float64
	RareSlot []bool
}

func buildCorpusStats(numSlots, N int, sumDL float64, df []int) corpusStats {
	stats := corpusStats{N: N, AvgDL: 1, IDF: make([]float64, numSlots), RareSlot: make([]bool, numSlots)}
	if N > 0 && sumDL > 0 {
		stats.AvgDL = sumDL / float64(N)
	}
	maxRareDF := max(1, int(rarityMaxDFRatio*float64(N)))
	for s := range stats.IDF {
		count := df[s]
		stats.IDF[s] = math.Log(1 + (float64(max(1, N)-count)+0.5)/(float64(count)+0.5))
		stats.RareSlot[s] = N >= 2 && count >= 1 && count <= maxRareDF
	}
	return stats
}

// rankedInterval is one two-sided minimal qualifying occurrence window.
type rankedInterval struct{ start, end int }

// rankedTextAnalysis contains no document text. bestIntervals is per-file
// scratch; callers retaining an analysis across files must clear it.
type rankedTextAnalysis struct {
	Score            float64
	TermCount        int
	MatchedTerms     []string
	BoundaryQuality  int
	SpanLength       int
	TopQualityChunks int
	WordCount        int
	SlotTF           []int
	MatchedSlots     []bool
	start, end       int
	bestIntervals    []rankedInterval
}

type rankedOccurrence struct {
	start, end, termIndex int
	wholeIdentifier       bool
}

type rankedWindow struct {
	score                      float64
	termCount, boundaryQuality int
	slots                      []bool
	start, end, left, right    int
}

type rankedBestWindows struct {
	score          float64
	termCount      int
	representative rankedWindow
	haveBest       bool
	intervals      []rankedInterval
	seenIntervals  map[rankedInterval]struct{}
}

// rankedSubsetSummary stores only slot identities and numeric metadata, never
// source text or occurrences. topQualityChunks is exact even after intervals
// overflow; only unions of tied subsets require their original intervals.
type rankedSubsetSummary struct {
	representative   rankedWindow
	topQualityChunks int
	intervals        []rankedInterval
	intervalOverflow bool
}

type rankedSubsetCatalog struct {
	WordCount    int
	SlotTF       []int
	SlotPresent  []bool
	MaxTermCount int
	Subsets      []rankedSubsetSummary
}

// analyzeRankedCatalog retains only maximal-cardinality subsets. Temporary
// occurrence/interval storage is released at return; retained intervals are
// capped per subset, or absent entirely for the single-subset common path.
func analyzeRankedCatalog(text string, query compiledQuery, distance int, strict, allowFallback bool) rankedSubsetCatalog {
	occurrences := rankedOccurrences(text, query)
	cat := rankedSubsetCatalog{WordCount: countCleanedWords(text), SlotTF: occurrenceSlotTF(occurrences, query.slotCount()), SlotPresent: make([]bool, query.slotCount())}
	for s, tf := range cat.SlotTF {
		cat.SlotPresent[s] = tf > 0
	}
	subsets := make(map[string]*rankedBestWindows)
	var order []string
	visitRankedWindows(occurrences, query, distance, func(window rankedWindow) {
		if !rankedWindowQualifies(window, query.slotCount(), strict, allowFallback, nil) || window.termCount < cat.MaxTermCount {
			return
		}
		if window.termCount > cat.MaxTermCount {
			cat.MaxTermCount = window.termCount
			clear(subsets)
			order = nil
		}
		mask := make([]byte, len(window.slots))
		for s, present := range window.slots {
			if present {
				mask[s] = 1
			}
		}
		key := string(mask)
		best := subsets[key]
		if best == nil {
			best = &rankedBestWindows{}
			subsets[key] = best
			order = append(order, key)
		}
		best.observe(window)
	})
	for _, key := range order {
		best := subsets[key]
		sub := rankedSubsetSummary{representative: best.representative, topQualityChunks: topQualityChunkCount(best.intervals)}
		if len(order) > 1 {
			sub.intervalOverflow = len(best.intervals) > inlineSubsetIntervalCap
			if !sub.intervalOverflow {
				sub.intervals = best.intervals
			}
		}
		cat.Subsets = append(cat.Subsets, sub)
	}
	return cat
}

// resolveRankedCatalog resolves corpus-dependent weights without retaining
// text. reloadText is needed only for an interval union across multiple tied
// subsets with overflow; a failed/missing reload rejects rather than inventing
// a chunk count. Rare slots and weights, when supplied, must align with query.
func resolveRankedCatalog(cat rankedSubsetCatalog, query compiledQuery, distance int, strict, allowFallback bool, rareSlot []bool, slotWeights []float64, reloadText func() (string, bool)) (rankedTextAnalysis, bool) {
	bestWeight := math.Inf(-1)
	weights := make([]float64, len(cat.Subsets))
	eligible := make([]bool, len(cat.Subsets))
	for i, sub := range cat.Subsets {
		if !rankedWindowQualifies(sub.representative, query.slotCount(), strict, allowFallback, rareSlot) {
			continue
		}
		eligible[i] = true
		weights[i] = canonicalSlotMaskWeight(query, sub.representative.slots, slotWeights)
		bestWeight = math.Max(bestWeight, weights[i])
	}
	var tied []int
	overflow := false
	bestIndex := -1
	for i, sub := range cat.Subsets {
		if !eligible[i] || math.Abs(weights[i]-bestWeight) > 1e-12 {
			continue
		}
		tied = append(tied, i)
		overflow = overflow || sub.intervalOverflow
		if bestIndex < 0 || rankedWindowBetter(sub.representative, cat.Subsets[bestIndex].representative) {
			bestIndex = i
		}
	}
	if bestIndex < 0 {
		return rankedTextAnalysis{}, false
	}
	sub := cat.Subsets[bestIndex]
	an := analysisFromWindow(sub.representative, query)
	an.Score = RoundScore(weights[bestIndex])
	an.WordCount, an.SlotTF = cat.WordCount, cat.SlotTF
	an.TopQualityChunks = sub.topQualityChunks
	if len(tied) > 1 {
		var intervals []rankedInterval
		if overflow {
			if reloadText == nil {
				return rankedTextAnalysis{}, false
			}
			text, ok := reloadText()
			if !ok {
				return rankedTextAnalysis{}, false
			}
			visitRankedWindows(rankedOccurrences(text, query), query, distance, func(window rankedWindow) {
				if window.termCount != cat.MaxTermCount || !rankedWindowQualifies(window, query.slotCount(), strict, allowFallback, rareSlot) {
					return
				}
				if math.Abs(canonicalSlotMaskWeight(query, window.slots, slotWeights)-bestWeight) <= 1e-12 {
					intervals = append(intervals, rankedInterval{window.start, window.end})
				}
			})
		} else {
			for _, i := range tied {
				intervals = append(intervals, cat.Subsets[i].intervals...)
			}
		}
		an.TopQualityChunks = topQualityChunkCount(intervals)
	}
	return an, true
}

func scoreAnalyzedDocument(an rankedTextAnalysis, query compiledQuery, pathSlots []bool, stats corpusStats, distance int) float64 {
	norm := 1 - bm25B + bm25B*float64(an.WordCount)/stats.AvgDL
	var vals []float64
	var keys []string
	for s := 0; s < query.slotCount(); s++ {
		if an.MatchedSlots[s] {
			tf := float64(an.SlotTF[s])
			vals = append(vals, stats.IDF[s]*tf*(bm25K1+1)/(tf+bm25K1*norm))
			keys = append(keys, query.slotLabel(s))
		}
		if len(pathSlots) > 0 && pathSlots[s] {
			vals = append(vals, bm25PathSlotWeight*stats.IDF[s])
			keys = append(keys, "\x00path:"+query.slotLabel(s))
		}
	}
	prox, cov := 1.0, 1.0
	if an.TermCount >= 2 {
		prox += proximityBoostWeight / (1 + float64(an.SpanLength)/math.Min(float64(max(1, distance)), proximityScaleCap))
	}
	if query.slotCount() > 1 {
		cov = float64(an.TermCount) / float64(query.slotCount())
	}
	return RoundScore(canonicalFloatSum(vals, keys) * prox * cov)
}

func occurrenceSlotTF(occurrences []rankedOccurrence, count int) []int {
	tf := make([]int, count)
	for _, occurrence := range occurrences {
		tf[occurrence.termIndex]++
	}
	return tf
}

func analyzeRankedText(text string, query compiledQuery, distance int, strict bool) (rankedTextAnalysis, bool) {
	return analyzeRankedTextWithOptions(text, query, distance, strict, false, nil, nil)
}

// Stateless nil-weight callers retain byte-length weighting and score-first
// selection. Corpus callers supply IDF weights and prioritize slot coverage.
func analyzeRankedTextWithOptions(text string, query compiledQuery, distance int, strict, allowFallback bool, rareSlot []bool, slotWeights []float64) (rankedTextAnalysis, bool) {
	occurrences := rankedOccurrences(text, query)
	var best rankedBestWindows
	visitRankedWindows(occurrences, query, distance, func(window rankedWindow) {
		if !rankedWindowQualifies(window, query.slotCount(), strict, allowFallback, rareSlot) {
			return
		}
		window.score = canonicalSlotMaskWeight(query, window.slots, slotWeights)
		if len(slotWeights) > 0 && best.haveBest {
			if window.termCount < best.termCount {
				return
			}
			if window.termCount > best.termCount {
				best = rankedBestWindows{}
			}
		}
		best.observe(window)
	})
	if !best.haveBest {
		return rankedTextAnalysis{}, false
	}
	sort.Slice(best.intervals, func(i, j int) bool {
		if best.intervals[i].start != best.intervals[j].start {
			return best.intervals[i].start < best.intervals[j].start
		}
		return best.intervals[i].end < best.intervals[j].end
	})
	an := analysisFromWindow(best.representative, query)
	an.WordCount = countCleanedWords(text)
	an.SlotTF = occurrenceSlotTF(occurrences, query.slotCount())
	an.TopQualityChunks = topQualityChunkCount(best.intervals)
	an.bestIntervals = best.intervals
	return an, true
}

func analysisFromWindow(window rankedWindow, query compiledQuery) rankedTextAnalysis {
	terms := make([]string, 0, window.termCount)
	for s, present := range window.slots {
		if present {
			terms = append(terms, query.slotLabel(s))
		}
	}
	return rankedTextAnalysis{Score: RoundScore(window.score), TermCount: window.termCount, MatchedTerms: terms, MatchedSlots: window.slots, BoundaryQuality: window.boundaryQuality, SpanLength: window.end - window.start, start: window.start, end: window.end}
}

func visitRankedWindows(occurrences []rankedOccurrence, query compiledQuery, distance int, visit func(rankedWindow)) {
	latest := make([]int, query.slotCount())
	latestBoundary := make([]int, query.slotCount())
	for i := range latest {
		latest[i] = -1
		latestBoundary[i] = -1
	}
	endRanges := newRangeMaximum(occurrences)
	for right, occurrence := range occurrences {
		termIndex := occurrence.termIndex
		previousOccurrence := latest[termIndex]
		latest[termIndex] = right
		if occurrence.wholeIdentifier {
			latestBoundary[termIndex] = right
		}
		// Both edges must be indispensable to this window's slot set.
		for _, left := range latest {
			if left < 0 || previousOccurrence >= left {
				continue
			}
			if occurrences[right].start-occurrences[left].start > distance {
				continue
			}
			visit(buildRankedWindow(query, occurrences, latest, latestBoundary, endRanges, left, right))
		}
	}
}

// pathMatchCount counts distinct canonical query slots represented in the
// slash-normalized path relative to the active search root.
func pathMatchCount(filePath, startDir string, query compiledQuery) int {
	_, count := pathMatchStats(filePath, startDir, query)
	return count
}

func pathMatchStats(filePath, startDir string, query compiledQuery) ([]bool, int) {
	relPath := rankingRelativePath(filePath, resolveRankingRoot(startDir))
	present := make([]bool, query.slotCount())
	count := 0
	for s := 0; s < query.slotCount(); s++ {
		matched := query.slotMatches(s, relPath)
		core := alphanumericCore(query.slotLabel(s))
		if !matched && len(core) >= 2 {
			matched = strings.Contains(relPath, core)
			caseInsensitive := false
			if len(query.slots) > 0 {
				caseInsensitive = query.slots[s].alts[0].caseInsensitive
			} else {
				caseInsensitive = query.matchers[s].caseInsensitive
			}
			if caseInsensitive {
				matched = strings.Contains(strings.ToLower(relPath), strings.ToLower(core))
			}
		}
		if matched {
			present[s] = true
			count++
		}
	}
	return present, count
}

// resolveRankingRoot mirrors the logical-root side of resolveWalkRoot without
// requiring the root to still be readable during Stage 2.
func resolveRankingRoot(startDir string) string {
	if startDir == "" {
		startDir = "."
	}
	absolute, err := filepath.Abs(startDir)
	if err != nil {
		return filepath.Clean(startDir)
	}
	return filepath.Clean(absolute)
}

func rankingRelativePath(filePath, resolvedRoot string) string {
	absolutePath, err := filepath.Abs(filePath)
	if err != nil {
		return filepath.ToSlash(filepath.Base(filePath))
	}
	absolutePath = filepath.Clean(absolutePath)
	relPath, err := filepath.Rel(resolvedRoot, absolutePath)
	if err != nil {
		return filepath.ToSlash(filepath.Base(absolutePath))
	}
	relPath = filepath.ToSlash(relPath)
	if relPath == "" || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return filepath.ToSlash(filepath.Base(absolutePath))
	}
	return relPath
}

func alphanumericCore(term string) string {
	var core strings.Builder
	core.Grow(len(term))
	for i := 0; i < len(term); i++ {
		if isASCIIAlphanumeric(term[i]) {
			core.WriteByte(term[i])
		}
	}
	return core.String()
}

func isASCIIAlphanumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// topQualityChunkCount returns the maximum cardinality subset of pairwise
// non-overlapping half-open intervals.
func topQualityChunkCount(intervals []rankedInterval) int {
	ordered := append([]rankedInterval(nil), intervals...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].end != ordered[j].end {
			return ordered[i].end < ordered[j].end
		}
		return ordered[i].start < ordered[j].start
	})
	count, lastEnd := 0, 0
	for _, interval := range ordered {
		if interval.end <= interval.start {
			continue
		}
		if count == 0 || interval.start >= lastEnd {
			count++
			lastEnd = interval.end
		}
	}
	return count
}

func (best *rankedBestWindows) observe(window rankedWindow) {
	betterPair := !best.haveBest || window.score > best.score || (window.score == best.score && window.termCount > best.termCount)
	if betterPair {
		best.score, best.termCount = window.score, window.termCount
		best.representative = window
		best.haveBest = true
		best.intervals = nil
		best.seenIntervals = make(map[rankedInterval]struct{})
	} else if window.score != best.score || window.termCount != best.termCount {
		return
	} else if rankedWindowBetter(window, best.representative) {
		best.representative = window
	}
	interval := rankedInterval{window.start, window.end}
	if _, seen := best.seenIntervals[interval]; seen {
		return
	}
	best.seenIntervals[interval] = struct{}{}
	best.intervals = append(best.intervals, interval)
}

func rankedOccurrences(text string, query compiledQuery) []rankedOccurrence {
	occurrences := make([]rankedOccurrence, 0)
	for s := 0; s < query.slotCount(); s++ {
		for _, found := range query.slotFindAll(s, text) {
			occurrences = append(occurrences, rankedOccurrence{found.start, found.end, s, hasWholeIdentifierEdges(text, found.start, found.end)})
		}
	}
	sort.Slice(occurrences, func(i, j int) bool {
		if occurrences[i].start != occurrences[j].start {
			return occurrences[i].start < occurrences[j].start
		}
		if occurrences[i].end != occurrences[j].end {
			return occurrences[i].end < occurrences[j].end
		}
		return query.slotLabel(occurrences[i].termIndex) < query.slotLabel(occurrences[j].termIndex)
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

func isASCIIPunctuation(b byte) bool { return b >= '!' && b <= '~' && !isWordByte(b) }

func buildRankedWindow(query compiledQuery, occurrences []rankedOccurrence, latest, latestBoundary []int, endRanges rangeMaximum, left, right int) rankedWindow {
	window := rankedWindow{start: occurrences[left].start, end: endRanges.maximum(left, right+1), left: left, right: right, slots: make([]bool, query.slotCount())}
	for s, idx := range latest {
		if idx < left {
			continue
		}
		window.slots[s] = true
		window.termCount++
		if latestBoundary[s] >= left {
			window.boundaryQuality++
		}
	}
	return window
}

func rankedWindowQualifies(window rankedWindow, requiredCount int, strict, allowFallback bool, rareSlot []bool) bool {
	if strict || requiredCount <= 1 {
		return window.termCount == requiredCount
	}
	if window.termCount >= 2 {
		return true
	}
	if window.termCount != 1 || !allowFallback {
		return false
	}
	for s, present := range window.slots {
		if present {
			return rareSlot == nil || rareSlot[s]
		}
	}
	return false
}

func rankedWindowBetter(candidate, current rankedWindow) bool {
	if candidate.boundaryQuality != current.boundaryQuality {
		return candidate.boundaryQuality > current.boundaryQuality
	}
	candidateSpan, currentSpan := candidate.end-candidate.start, current.end-current.start
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

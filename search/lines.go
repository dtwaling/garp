package search

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// codePrefix records a cleaned prefix as a slice of the shared normalized text
// plus, at most, an incomplete UTF-8 rune. Prefix trimming must happen before
// considering later bytes: a prefix ending inside a whitespace run is different
// from an offset into the fully normalized string.
type codePrefix struct {
	end  int
	tail string
}

// codePrefixCorrespondence scans raw once and retains only requested offsets.
// Control runes become spaces (they are not deleted), ASCII whitespace runs
// collapse, and Unicode whitespace is trimmed only at the prefix boundaries,
// exactly as in CleanContentCode. Invalid UTF-8 bytes remain unchanged.
func codePrefixCorrespondence(raw string, offsets map[int]codePrefix) string {
	var text strings.Builder
	text.Grow(len(raw))
	trimmedEnd := 0
	inSpace := false
	started := false
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		// A truncated rune is invalid UTF-8, not whitespace or a control rune.
		for n := 1; n < size; n++ {
			if _, ok := offsets[i+n]; ok {
				offsets[i+n] = codePrefix{text.Len(), raw[i : i+n]}
			}
		}
		space := r <= 0x20 || (r >= 0x7f && r <= 0x9f)
		if space {
			if started && !inSpace {
				text.WriteByte(' ')
			}
		} else if started || !unicode.IsSpace(r) {
			text.WriteString(raw[i : i+size])
		}
		inSpace = space
		if !space && !unicode.IsSpace(r) {
			started = true
			trimmedEnd = text.Len()
		}
		i += size
		if _, ok := offsets[i]; ok {
			offsets[i] = codePrefix{end: trimmedEnd}
		}
	}
	return text.String()
}

// corresponds compares offsets, not whole prefixes, avoiding another O(M*N)
// scan in string equality. The shared text and normalized have identical prefix
// bytes; only an incomplete rune's tail needs a byte comparison.
func (p codePrefix) corresponds(normalized string, end int) bool {
	return p.end+len(p.tail) == end && p.end <= len(normalized) &&
		strings.HasPrefix(normalized[p.end:], p.tail)
}

// docStep retains effective regex replacement ranges, not one raw identity per
// document byte. Literal no-ops are excluded so their byte identity survives.
type docStep struct {
	locs        [][]int
	replacement string
	trimLeft    int
}

// replacementRanges visits copied captures and synthetic literal bytes in order.
// Cleaner templates use only literals and single-digit capture groups.
func replacementRanges(loc []int, replacement string, visit func(start, length int)) {
	for j := 0; j < len(replacement); j++ {
		if replacement[j] == '$' && j+1 < len(replacement) {
			j++
			group := int(replacement[j]-'0') * 2
			if group+1 < len(loc) && loc[group] >= 0 {
				visit(loc[group], loc[group+1]-loc[group])
			}
		} else {
			visit(-1, 1)
		}
	}
}

// documentReplace preserves literal no-ops as untouched ranges in both the
// forward text and the reverse mapping. Captures retain their explicit origins.
func documentReplace(text string, re *regexp.Regexp, replacement string) (string, docStep) {
	locs := re.FindAllStringSubmatchIndex(text, -1)
	if !strings.Contains(replacement, "$") {
		changed := locs[:0]
		for _, loc := range locs {
			if text[loc[0]:loc[1]] != replacement {
				changed = append(changed, loc)
			}
		}
		// Drop references to skipped matches from the retained backing array.
		clear(locs[len(changed):])
		locs = changed
	}
	if len(locs) == 0 {
		return text, docStep{}
	}
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, loc := range locs {
		out.WriteString(text[last:loc[0]])
		literal := 0
		replacementRanges(loc, replacement, func(start, length int) {
			if start >= 0 {
				out.WriteString(text[start : start+length])
			} else {
				// Locate the next literal, skipping capture references.
				for literal+1 < len(replacement) && replacement[literal] == '$' {
					literal += 2
				}
				out.WriteByte(replacement[literal])
				literal++
			}
		})
		last = loc[1]
	}
	out.WriteString(text[last:])
	return out.String(), docStep{locs: locs, replacement: replacement}
}

func resolveDocumentOffsets(text string, steps []docStep, offsets map[int]int) {
	active := make([]int, 0, len(offsets))
	for k := range offsets {
		offsets[k] = -1
		if k >= 0 && k < len(text) {
			offsets[k] = k
			active = append(active, k)
		}
	}
	sort.Ints(active)
	for i := len(steps) - 1; i >= 0 && len(active) > 0; i-- {
		step := steps[i]
		if len(step.locs) == 0 {
			for _, k := range active {
				offsets[k] += step.trimLeft
			}
			continue
		}
		cursor, kept, inLast, outLast := 0, 0, 0, 0
		mapRange := func(start, length int) {
			end := outLast + length
			for cursor < len(active) && offsets[active[cursor]] < end {
				k := active[cursor]
				pos := offsets[k]
				cursor++
				offsets[k] = -1
				if start >= 0 {
					offsets[k] = start + pos - outLast
					active[kept] = k
					kept++
				}
			}
			outLast = end
		}
		for _, loc := range step.locs {
			mapRange(inLast, loc[0]-inLast)
			if cursor == len(active) {
				break
			}
			replacementRanges(loc, step.replacement, mapRange)
			inLast = loc[1]
			if cursor == len(active) {
				break
			}
		}
		for cursor < len(active) {
			k := active[cursor]
			cursor++
			offsets[k] = inLast + offsets[k] - outLast
			active[kept] = k
			kept++
		}
		active = active[:kept]
		// Captures can repeat or reorder input ranges. Restore position order
		// before the next linear reverse pass (normally already sorted).
		less := func(a, b int) bool { return offsets[active[a]] < offsets[active[b]] }
		if !sort.SliceIsSorted(active, less) {
			sort.Slice(active, less)
		}
	}
}

// documentCorrespondence mirrors CleanContent followed by excerpt prep, or
// prep alone, resolving only requested final-text byte offsets back to raw.
func documentCorrespondence(raw string, clean bool, offsets map[int]int) string {
	text := raw
	var steps []docStep
	replace := func(re *regexp.Regexp, replacement string) {
		var step docStep
		text, step = documentReplace(text, re, replacement)
		if len(offsets) > 0 && len(step.locs) > 0 {
			steps = append(steps, step)
		}
	}
	strip := func(control string) {
		replace(cssRegex, "")
		replace(jsRegex, "")
		replace(htmlTagRegex, " ")
		replace(htmlEntityRegex, " ")
		replace(controlCharRegex, control)
		replace(junkSymbolsRegex, "")
		replace(emailQuotingRegex, "")
		replace(quoteLineStartRegex, "")
		replace(quoteMidRegex, " ")
	}
	if clean {
		strip(" ")
		replace(whitespaceRegex, " ")
		replace(letterDigitRegex, "$1 $2")
		replace(digitLetterRegex, "$1 $2")
		replace(commaMissingSpace, ", $1")
		left := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
		text = strings.TrimSpace(text)
		if len(offsets) > 0 && left > 0 {
			steps = append(steps, docStep{trimLeft: left})
		}
	}
	strip("")
	if len(offsets) > 0 {
		resolveDocumentOffsets(text, steps, offsets)
	}
	return text
}

// requestedDocumentOffsets collects only valid emitted match bytes, deduplicated
// across spans. Invalid metadata must not cause unbounded offset collection.
func requestedDocumentOffsets(spans []excerptSpan) map[int]int {
	offsets := make(map[int]int)
	for _, span := range spans {
		for _, match := range span.matches {
			if match.source.start < 0 || match.source.end > len(span.sourceText) {
				continue
			}
			for j := match.source.start; j < match.source.end; j++ {
				offsets[j] = -1
			}
		}
	}
	return offsets
}

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
		// Code and heuristic anchors need raw occurrences. Document byte mappings
		// can anchor retained matches even when cleaning created their boundaries.
		if len(offs) > 0 {
			infos = append(infos, termInfo{index: index, offsets: offs})
		}
	}

	// nearest prefers unused occurrences, with an exclusive cluster boundary.
	nearest := func(offs []int, pos, cursor, end int) (int, bool) {
		best, found, bestDist := 0, false, window+1
		for _, o := range offs {
			if o >= end {
				continue
			}
			d := o - pos
			if d < 0 {
				d = -d
			}
			if d <= window && (!found || (o >= cursor && best < cursor) ||
				((o >= cursor) == (best >= cursor) && d < bestDist)) {
				best, bestDist, found = o, d, true
			}
		}
		return best, found
	}

	var normalizedRaw string
	normalizedReady := false
	var prefixes map[int]codePrefix
	var docOffsets map[int]int
	var documentSource string
	documentMatches := false
	sourceCounts := make([]int, len(query.matchers))
	first := make([]int, len(query.matchers))
	present := make([]*termInfo, 0, len(infos))
	// The fallback prefers anchors beyond every match in the previous cluster.
	// Exact identities may still resolve excerpts emitted in ranking order.
	cursor := 0
	for i, span := range spans {
		// Equal counts alone do not prove correspondence. Check raw byte identity
		// for document transformations, and normalized prefixes for code.
		if span.sourceText != "" && !normalizedReady {
			// Synthetic spans need no normalization. Exact-identity candidates
			// share one scan that also records the requested code prefixes.
			prefixes = make(map[int]codePrefix)
			for _, info := range infos {
				for _, offset := range info.offsets {
					prefixes[offset] = codePrefix{}
				}
			}
			normalizedRaw = strings.TrimSpace(codePrefixCorrespondence(raw, prefixes))
			normalizedReady = true
		}
		code := span.sourceText != "" && span.sourceText == normalizedRaw
		document := false
		if !code && span.sourceText != "" {
			if docOffsets == nil {
				docOffsets = requestedDocumentOffsets(spans)
			}
			if documentSource != span.sourceText {
				documentSource = span.sourceText
				documentMatches = documentCorrespondence(raw, true, docOffsets) == documentSource
				if !documentMatches {
					documentMatches = documentCorrespondence(raw, false, docOffsets) == documentSource
				}
			}
			document = documentMatches
		}
		clear(sourceCounts)
		for _, match := range span.sourceMatches {
			sourceCounts[match.term]++
		}
		lo, hi := len(raw), -1
		exact := len(span.matches) > 0 && (code || document)
		for _, match := range span.matches {
			if !exact {
				break
			}
			if match.source.start < 0 || match.source.start >= len(span.sourceText) {
				exact = false
				break
			}
			if document {
				start, end := match.source.start, match.source.end
				if end <= start || end > len(span.sourceText) {
					exact = false
					break
				}
				offset, ok := docOffsets[start]
				if !ok || offset < 0 || offset >= len(raw) || end-start > len(raw)-offset {
					exact = false
					break
				}
				// Cleaning can create word boundaries around retained bytes. Trust
				// their identity, not raw matcher membership, but reject any match
				// containing synthetic bytes or crossing a removed raw segment.
				for j := start; j < end; j++ {
					if mapped, ok := docOffsets[j]; !ok || mapped != offset+j-start {
						exact = false
						break
					}
				}
				if !exact {
					break
				}
				lo = min(lo, offset)
				hi = max(hi, offset)
				continue
			}
			found := false
			for _, info := range infos {
				if info.index != match.term {
					continue
				}
				if match.occurrence <= 0 || match.occurrence > len(info.offsets) || sourceCounts[match.term] != len(info.offsets) {
					break
				}
				offset := info.offsets[match.occurrence-1]
				if !prefixes[offset].corresponds(normalizedRaw, len(strings.TrimSpace(span.sourceText[:match.source.start]))) {
					break
				}
				lo = min(lo, offset)
				hi = max(hi, offset)
				found = true
				break
			}
			exact = exact && found
		}
		if exact {
			out[i] = li.lineOf(lo)
			cursor = hi + 1
			continue
		}
		// Determine which terms appear in this excerpt, and pick the rarest (fewest raw
		// occurrences) as the anchor -- rarer terms disambiguate position more reliably.
		var anchor *termInfo
		present = present[:0]
		for j := range first {
			first[j] = -1
		}
		for _, match := range span.matches {
			if first[match.term] == -1 || match.source.start < first[match.term] {
				first[match.term] = match.source.start
			}
		}
		for j := range infos {
			if first[infos[j].index] != -1 {
				present = append(present, &infos[j])
				if anchor == nil || len(infos[j].offsets) < len(anchor.offsets) {
					anchor = &infos[j]
				}
			}
		}
		if anchor == nil {
			continue // no anchorable term -> leave lines unknown (0)
		}

		// Preserve known source ordering so a preceding term cannot be borrowed
		// from the following cluster simply because it is closer to the anchor.
		cluster := func(idx int) (score, lo, hi int) {
			pos := anchor.offsets[idx]
			lo, hi = pos, pos
			for _, p := range present {
				end := len(raw)
				if first[p.index] < first[anchor.index] {
					end = pos
				} else if i+1 < len(spans) && idx+1 < len(anchor.offsets) {
					nextAnchor := anchor.offsets[idx+1]
					if o, ok := nearest(p.offsets, pos, cursor, nextAnchor); ok && o >= cursor {
						end = nextAnchor
					}
				}
				if o, ok := nearest(p.offsets, pos, cursor, end); ok {
					score++
					lo, hi = min(lo, o), max(hi, o)
				}
			}
			return
		}

		// Choose the anchor occurrence (preferring positions at/after the cursor) whose
		// surrounding window clusters the most present terms.
		bestIdx, bestScore, bestWidth := -1, -1, len(raw)+1
		consider := func(idx int) {
			sc, l, h := cluster(idx)
			if sc > bestScore || (sc == bestScore && h-l < bestWidth) {
				bestIdx, bestScore, bestWidth = idx, sc, h-l
				lo, hi = l, h
			}
		}
		for idx, o := range anchor.offsets {
			if o < cursor {
				continue
			}
			consider(idx)
		}
		if bestIdx == -1 { // nothing at/after cursor; fall back to global best
			for idx := range anchor.offsets {
				consider(idx)
			}
		}
		if bestIdx == -1 {
			continue
		}

		// Use the earliest occurrence for the line and the latest for the cursor,
		// just as in the exact path, so later fallbacks cannot reuse this cluster.

		out[i] = li.lineOf(lo)
		cursor = hi + 1
	}
	return out
}

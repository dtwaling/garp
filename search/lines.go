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

// mappedContent retains raw byte identities through cleaning. Inserted separator
// bytes map to the nearest preceding captured byte (or the replacement start).
// A nil offsets slice selects text-only cleaning for Stage 2 analysis.
type mappedContent struct {
	text    string
	offsets []int
}

func (m mappedContent) replace(re *regexp.Regexp, replacement string) mappedContent {
	if m.offsets == nil {
		return mappedContent{text: re.ReplaceAllString(m.text, replacement)}
	}
	locs := re.FindAllStringSubmatchIndex(m.text, -1)
	if len(locs) == 0 {
		return m
	}
	var text strings.Builder
	offsets := make([]int, 0, len(m.offsets))
	copyRange := func(start, end int) {
		text.WriteString(m.text[start:end])
		offsets = append(offsets, m.offsets[start:end]...)
	}
	last := 0
	for _, loc := range locs {
		copyRange(last, loc[0])
		anchor := loc[0]
		// Cleaner templates use only literals and single-digit capture groups.
		for j := 0; j < len(replacement); j++ {
			if replacement[j] == '$' && j+1 < len(replacement) {
				j++
				group := int(replacement[j]-'0') * 2
				if group+1 < len(loc) && loc[group] >= 0 {
					copyRange(loc[group], loc[group+1])
					anchor = max(loc[group], loc[group+1]-1)
				}
			} else {
				text.WriteByte(replacement[j])
				offsets = append(offsets, m.offsets[anchor])
			}
		}
		last = loc[1]
	}
	copyRange(last, len(m.text))
	return mappedContent{text.String(), offsets}
}

// documentCorrespondence mirrors CleanContent followed by excerpt prep, or
// prep alone. Compute once per source variant, never once per emitted match.
func documentCorrespondence(raw string, clean bool) mappedContent {
	m := mappedContent{text: raw, offsets: make([]int, len(raw))}
	for i := range m.offsets {
		m.offsets[i] = i
	}
	strip := func(control string) {
		m = m.replace(cssRegex, "").replace(jsRegex, "")
		m = m.replace(htmlTagRegex, " ").replace(htmlEntityRegex, " ")
		m = m.replace(controlCharRegex, control).replace(junkSymbolsRegex, "")
		m = m.replace(emailQuotingRegex, "").replace(quoteLineStartRegex, "").replace(quoteMidRegex, " ")
	}
	if clean {
		m = cleanSearchContent(m, false)
	}
	strip("")
	return m
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
	needsFallback := false
	for i, span := range spans {
		if span.rawStartValid && span.rawStart >= 0 && span.rawStart < len(raw) {
			out[i] = li.lineOf(span.rawStart)
		} else {
			needsFallback = true
		}
	}
	if !needsFallback {
		return out
	}

	// Legacy synthetic spans have no mode metadata. Exclude document noise
	// before matching, but retain raw byte positions and token boundaries.
	visible := cleanStage3Content(raw, false)
	retained := make([]bool, len(raw))
	for _, offset := range visible.offsets {
		retained[offset] = true
	}

	// Precompute occurrence offsets using the same partial-aware matcher as the engine so
	// anchors line up with matches.
	type termInfo struct {
		index   int
		offsets []int
	}
	infos := make([]termInfo, 0, len(query.matchers))

	for index, matcher := range query.matchers {
		locs := matcher.findAll(raw)
		offs := make([]int, 0, len(locs))
		for _, l := range locs {
			if retained[l.start] {
				offs = append(offs, l.start)
			}
		}
		// A term with no raw occurrences can never be an anchor; skip it here so the
		// per-excerpt loop below doesn't have to filter empty-offset entries.
		if len(offs) > 0 {
			infos = append(infos, termInfo{index: index, offsets: offs})
		}
	}

	// nearest prefers unused occurrences, with an exclusive cluster boundary.
	nearest := func(offs []int, pos, cursor, end int) (int, bool) {
		lo := sort.SearchInts(offs, max(cursor, pos-window))
		hi := sort.SearchInts(offs, min(end, pos+window+1))
		if lo >= hi {
			return 0, false
		}
		idx := lo + sort.SearchInts(offs[lo:hi], pos)
		if idx == hi || idx > lo && pos-offs[idx-1] <= offs[idx]-pos {
			idx--
		}
		return offs[idx], true
	}

	var normalizedRaw string
	normalizedReady := false
	var prefixes map[int]codePrefix
	var documents []mappedContent
	documentsReady := false
	sourceCounts := make([]int, len(query.matchers))
	first := make([]int, len(query.matchers))
	present := make([]*termInfo, 0, len(infos))
	// The fallback prefers anchors beyond every match in the previous cluster.
	// Exact identities may still resolve excerpts emitted in ranking order.
	cursor := 0
	for i, span := range spans {
		if out[i] != 0 {
			cursor = max(cursor, span.rawStart+1)
			continue
		}
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
		var document *mappedContent
		if !code && span.sourceText != "" {
			if !documentsReady {
				documents = []mappedContent{documentCorrespondence(raw, true), documentCorrespondence(raw, false)}
				documentsReady = true
			}
			for j := range documents {
				if documents[j].text == span.sourceText {
					document = &documents[j]
					break
				}
			}
		}
		clear(sourceCounts)
		for _, match := range span.sourceMatches {
			sourceCounts[match.term]++
		}
		lo, hi := len(raw), -1
		exact := len(span.matches) > 0 && (code || document != nil)
		for _, match := range span.matches {
			if !exact {
				break
			}
			found := false
			for _, info := range infos {
				if info.index == match.term && match.occurrence > 0 &&
					match.occurrence <= len(info.offsets) && sourceCounts[match.term] == len(info.offsets) {
					offset := info.offsets[match.occurrence-1]
					if match.source.start < 0 || match.source.start >= len(span.sourceText) {
						break
					}
					if code {
						if !prefixes[offset].corresponds(normalizedRaw, len(strings.TrimSpace(span.sourceText[:match.source.start]))) {
							break
						}
					} else if document.offsets[match.source.start] != offset {
						break
					}
					lo = min(lo, offset)
					hi = max(hi, offset)
					found = true
					break
				}
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

		if bestIdx == -1 {
			continue
		}

		out[i] = li.lineOf(lo)
		// Advance past the cluster start, not its widest borrowed match. The
		// latter can skip remaining clusters. nearest excludes earlier starts,
		// and exhaustion stays unknown rather than resetting to a duplicate.
		cursor = lo + 1
	}
	return out
}

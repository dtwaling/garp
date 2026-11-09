package search

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// unconstrainedDistance disables the excerpt proximity bound for legacy helpers.
const unconstrainedDistance = -1

var (
	// HTML/XML tags
	htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

	// HTML entities
	htmlEntityRegex = regexp.MustCompile(`&[a-zA-Z0-9#]*;`)

	// Email headers (case insensitive)
	emailHeaderRegex = regexp.MustCompile(`(?i)^(Content-Type|Content-Transfer-Encoding|MIME-Version|Date|From|To|Subject|Message-ID|Return-Path|Received|X-[^:]*|Authentication-Results):`)

	// CSS/JavaScript blocks (separate patterns since Go doesn't support backreferences)
	cssRegex = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	jsRegex  = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)

	// Control characters and excessive whitespace
	controlCharRegex = regexp.MustCompile(`[\x00-\x1f\x7f-\x9f]`)
	whitespaceRegex  = regexp.MustCompile(`\s+`)

	// Junk divider lines with excessive =, #, -, or _
	junkSymbolsRegex = regexp.MustCompile(`(?m)^\s*[-_=#]{5,}\s*$`)

	// Email quoting lines and markers
	emailQuotingRegex   = regexp.MustCompile(`(?m)^>+.*$`)
	quoteLineStartRegex = regexp.MustCompile(`(?m)^\s*>+\s*`)
	quoteMidRegex       = regexp.MustCompile(`\s*>+\s*`)

	// Precompiled regexes for CleanContent - CRITICAL: these were being compiled on EVERY call
	// letterDigitRegex injects a space between an uppercase letter and a digit to fix OCR artifacts
	// like "Account10" -> "Account 10" or "PDF32" -> "PDF 32". Restricted to uppercase-start to
	// avoid mangling code identifiers such as float32, int64, np.float32.
	letterDigitRegex  = regexp.MustCompile(`([A-Z])([0-9])`)
	digitLetterRegex  = regexp.MustCompile(`([0-9])([A-Z])`)
	commaMissingSpace = regexp.MustCompile(`,([^\s])`)

	// Precompiled regexes for ExtractMeaningfulExcerpts - also leaked on every call
	dividerRegex = regexp.MustCompile(`[-_=#]{5,}`)
)

// excerptOverlapTolerance is the maximum fraction of an emitted window that
// may overlap prior emitted windows. 0.10 means every chunk is >= 90% new.
const excerptOverlapTolerance = 0.10

var excerptContextLimit int // 0 means auto (use default heuristic)

// SetExcerptContextLimit allows the engine to set an excerpt window hint (usually the distance).
// Use 0 to reset to automatic behavior.
func SetExcerptContextLimit(n int) {
	if n < 0 {
		n = 0
	}
	excerptContextLimit = n
}

// CleanContent removes markup, headers, and other noise from content
func CleanContent(content string) string {
	// Remove CSS and JavaScript blocks first
	content = cssRegex.ReplaceAllString(content, "")
	content = jsRegex.ReplaceAllString(content, "")

	// Remove HTML tags
	content = htmlTagRegex.ReplaceAllString(content, " ")

	// Remove HTML entities
	content = htmlEntityRegex.ReplaceAllString(content, " ")

	// Remove control characters
	content = controlCharRegex.ReplaceAllString(content, " ")

	// Remove junk divider lines made of repeated =, #, -, or _
	content = junkSymbolsRegex.ReplaceAllString(content, "")

	// Remove email quoting lines
	content = emailQuotingRegex.ReplaceAllString(content, "")
	// Strip leading '>' quote markers at line starts
	content = quoteLineStartRegex.ReplaceAllString(content, "")
	// Collapse midline '>' quote markers (e.g., >>>> >>>>) into a single space
	content = quoteMidRegex.ReplaceAllString(content, " ")

	// Normalize whitespace
	content = whitespaceRegex.ReplaceAllString(content, " ")

	// Insert missing spaces to improve readability in extracted content
	// Letter followed by digit (e.g., "Account10" -> "Account 10")
	content = letterDigitRegex.ReplaceAllString(content, "$1 $2")
	// Digit followed by letter (e.g., "7367NEXT" -> "7367 NEXT")
	content = digitLetterRegex.ReplaceAllString(content, "$1 $2")
	// Ensure a space after commas when missing (e.g., "21,5:43" -> "21, 5:43")
	content = commaMissingSpace.ReplaceAllString(content, ", $1")

	return strings.TrimSpace(content)
}

// CleanContentCode is a minimal cleaner for source code. Unlike CleanContent it does NOT strip
// HTML/markup, CSS/JS blocks, entities, or email quoting -- those transforms corrupt code:
// PHP "<?php ... ?>" and "$obj->prop", Delphi generics "TList<T>", and comparisons like
// "if a < b then" all contain angle brackets the document cleaner would treat as tags and delete.
// It only normalizes control characters and collapses whitespace, so matching and excerpts stay
// faithful to the source. The OCR-oriented digit/space fixups in CleanContent are also skipped
// because they mangle identifiers (e.g. "Int32" -> "Int 32").
func CleanContentCode(content string) string {
	content = controlCharRegex.ReplaceAllString(content, " ")
	content = whitespaceRegex.ReplaceAllString(content, " ")
	return strings.TrimSpace(content)
}

// cleanStage3Content applies the Stage 3 cleaning pipeline once while carrying
// raw byte offsets through every transformation. Strip line-oriented noise
// before normalizing controls so quote/divider boundaries are still available.
func cleanStage3Content(raw string, isCode bool) mappedContent {
	m := mappedContent{text: raw, offsets: make([]int, len(raw))}
	for i := range m.offsets {
		m.offsets[i] = i
	}
	if !isCode {
		m = m.replace(cssRegex, "").replace(jsRegex, "")
		m = m.replace(htmlTagRegex, " ").replace(htmlEntityRegex, " ")
		m = m.replace(junkSymbolsRegex, "").replace(emailQuotingRegex, "")
		m = m.replace(quoteLineStartRegex, "").replace(quoteMidRegex, " ")
	}
	m = m.replace(controlCharRegex, " ").replace(whitespaceRegex, " ")
	if !isCode {
		m = m.replace(letterDigitRegex, "$1 $2").replace(digitLetterRegex, "$1 $2")
		m = m.replace(commaMissingSpace, ", $1")
	}
	left := len(m.text) - len(strings.TrimLeftFunc(m.text, unicode.IsSpace))
	right := max(left, len(strings.TrimRightFunc(m.text, unicode.IsSpace)))
	return mappedContent{m.text[left:right], m.offsets[left:right]}
}

// excerptSpan is one emitted chunk: its final text plus the [left, right)
// half-open span it occupies in the cleaned string, plus the ranked-match
// quality (score = sum of matched distinct-term lengths, termCount = their
// count) of the window that framed it. Quality fields are zero for
// fallback-path excerpts that do not frame a ranked window.
type excerptSpan struct {
	rawStart      int  // earliest raw match in the seed, independent of displayed matches
	rawStartValid bool // zero is a valid offset; false denotes legacy/synthetic spans
	text          string
	left          int
	right         int
	score         int
	termCount     int
	matches       []excerptMatch
	sourceText    string
	sourceMatches []excerptMatch
}

type excerptMatch struct {
	matchRange
	source     matchRange
	term       int
	occurrence int // 1-based per-term occurrence in sourceText; 0 means unknown
}

func excerptSpanTexts(spans []excerptSpan) []string {
	excerpts := make([]string, len(spans))
	for i, span := range spans {
		excerpts[i] = span.text
	}
	return excerpts
}

func excerptSpanOverlap(a, b excerptSpan) int {
	return max(0, min(a.right, b.right)-max(a.left, b.left))
}

// excerptOverlapUnion computes the exact byte overlap between a candidate
// window and the union of already-emitted windows. Spans may be emitted in
// ranked order (not document order), so a simple sum of pairwise overlaps
// cannot be used: the same byte range inside an intersection of two emitted
// windows must be counted once.
func excerptOverlapUnion(candidate excerptSpan, emitted []excerptSpan) int {
	if len(emitted) == 0 {
		return 0
	}
	// Collect the emitted intervals, merge them, then intersect with the candidate.
	type interval struct{ left, right int }
	intervals := make([]interval, 0, len(emitted))
	for _, e := range emitted {
		if e.right > e.left {
			intervals = append(intervals, interval{e.left, e.right})
		}
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].left != intervals[j].left {
			return intervals[i].left < intervals[j].left
		}
		return intervals[i].right < intervals[j].right
	})
	merged := intervals[:1]
	for _, iv := range intervals[1:] {
		last := &merged[len(merged)-1]
		if iv.left <= last.right {
			if iv.right > last.right {
				last.right = iv.right
			}
		} else {
			merged = append(merged, iv)
		}
	}
	overlap := 0
	for _, iv := range merged {
		left := max(candidate.left, iv.left)
		right := min(candidate.right, iv.right)
		if right > left {
			overlap += right - left
		}
	}
	return overlap
}

// excerptSpanWithinOverlapTolerance checks the candidate against the emitted
// union: a chunk is acceptable when its overlap with prior chunks is at most
// excerptOverlapTolerance of its own window length (>= 90% new content).
func excerptSpanWithinOverlapTolerance(candidate excerptSpan, emitted []excerptSpan) bool {
	overlap := excerptOverlapUnion(candidate, emitted)
	return overlap <= int(excerptOverlapTolerance*float64(candidate.right-candidate.left))
}

func firstRangeWithin(ranges []matchRange, left, right int) (matchRange, bool) {
	i := sort.Search(len(ranges), func(i int) bool {
		return ranges[i].start >= left
	})
	for ; i < len(ranges) && ranges[i].start < right; i++ {
		if ranges[i].end <= right {
			return ranges[i], true
		}
	}
	return matchRange{}, false
}

func utf8BudgetCut(text string, budget int) int {
	if budget <= 0 {
		return 0
	}
	if budget >= len(text) {
		return len(text)
	}
	if !utf8.ValidString(text) {
		return budget
	}
	for budget > 0 && !utf8.RuneStart(text[budget]) {
		budget--
	}
	return budget
}

func truncateExcerpt(text string, matches []excerptMatch, budget int) (string, []excerptMatch) {
	if budget >= len(text) {
		return text, matches
	}
	cut := utf8BudgetCut(text, budget)
	text = text[:cut]
	matches = slices.DeleteFunc(matches, func(match excerptMatch) bool {
		return match.start >= cut || match.end <= match.start
	})
	for i := range matches {
		if matches[i].end > cut {
			matches[i].end = cut
		}
	}
	return text, matches
}

func normalizeExcerptBounds(source string, left, right int) (int, int) {
	if !utf8.ValidString(source) {
		return left, right
	}
	for left < right && !utf8.RuneStart(source[left]) {
		left++
	}
	for right > left && right < len(source) && !utf8.RuneStart(source[right]) {
		right--
	}
	return left, right
}

func formatExcerptText(source string, left, right, budget int, matches []excerptMatch) (string, []excerptMatch) {
	return formatExcerptTextBoundaryMatches(source, left, right, budget, matches, false)
}

func formatMultipartExcerptText(source string, left, right int, matches []excerptMatch) (string, []excerptMatch) {
	return formatExcerptTextBoundaryMatches(source, left, right, 0, matches, true)
}

func formatExcerptTextBoundaryMatches(source string, left, right, budget int, matches []excerptMatch, clampBoundaries bool) (string, []excerptMatch) {
	left, right = normalizeExcerptBounds(source, left, right)
	raw := source[left:right]
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	trimLeft := strings.Index(raw, trimmed)
	trimRight := trimLeft + len(trimmed)

	local := make([]excerptMatch, 0, len(matches))
	visibleLeft := left + trimLeft
	visibleRight := left + trimRight
	for _, match := range matches {
		if match.end <= visibleLeft || match.start >= visibleRight {
			continue
		}
		if !clampBoundaries && (match.start < visibleLeft || match.end > visibleRight) {
			continue
		}
		matchStart := max(match.start, visibleLeft)
		matchEnd := min(match.end, visibleRight)
		local = append(local, excerptMatch{
			matchRange: matchRange{start: matchStart - visibleLeft, end: matchEnd - visibleLeft},
			source:     match.source,
			term:       match.term,
			occurrence: match.occurrence,
		})
	}

	text := trimmed
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	text, local = replaceExcerptText(text, dividerRegex, " ", local)
	text, local = replaceExcerptText(text, whitespaceRegex, " ", local)
	if budget > 0 && len(text) > budget {
		text, local = truncateExcerpt(text, local, budget)
	}
	return text, local
}

func replaceExcerptText(text string, re *regexp.Regexp, replacement string, matches []excerptMatch) (string, []excerptMatch) {
	replacements := re.FindAllStringIndex(text, -1)
	if len(replacements) == 0 {
		return text, matches
	}

	mapOffset := func(offset int) int {
		delta := 0
		for _, loc := range replacements {
			if offset <= loc[0] {
				break
			}
			if offset >= loc[1] {
				delta += len(replacement) - (loc[1] - loc[0])
				continue
			}
			return loc[0] + delta
		}
		return offset + delta
	}
	mapped := matches[:0]
	for _, match := range matches {
		changed := false
		for _, loc := range replacements {
			if match.start < loc[1] && match.end > loc[0] && text[loc[0]:loc[1]] != replacement {
				changed = true
				break
			}
		}
		if changed {
			continue
		}
		match.start = mapOffset(match.start)
		match.end = mapOffset(match.end)
		if match.end > match.start {
			mapped = append(mapped, match)
		}
	}
	return re.ReplaceAllString(text, replacement), mapped
}

func emittedOverlapRight(candidate excerptSpan, emitted []excerptSpan) int {
	right := -1
	for i := len(emitted) - 1; i >= 0 && i >= len(emitted)-2; i-- {
		if excerptSpanOverlap(candidate, emitted[i]) > 0 && emitted[i].right > right {
			right = emitted[i].right
		}
	}
	return right
}

// expandToBoundaries widens [left, right) around the match span to sentence,
// paragraph, or email-header boundaries within the maxContext scan clamp.
func expandToBoundaries(cleaned string, left, right, maxContext int) (int, int) {
	matchLeft, matchRight := left, right
	limitLeft := left - maxContext/2
	if limitLeft < 0 {
		limitLeft = 0
	}
	// Expand left until punctuation or email header boundary.
	for left > limitLeft {
		if cleaned[left] == '.' || cleaned[left] == '!' || cleaned[left] == '?' {
			break
		}
		// Stop at email header lines (From:, To:, Subject:, Date:, etc.).
		if cleaned[left] == '\n' {
			ls := left + 1
			le := ls
			for le < len(cleaned) && cleaned[le] != '\n' && le-ls < 128 {
				le++
			}
			if ls < len(cleaned) && emailHeaderRegex.MatchString(cleaned[ls:le]) {
				break
			}
		}
		left--
	}
	if left > 0 && (cleaned[left] == '.' || cleaned[left] == '!' || cleaned[left] == '?') {
		left++
	} else if left <= limitLeft {
		// Paragraph fallback: last blank line in window.
		if idx := strings.LastIndex(cleaned[limitLeft:matchLeft], "\n\n"); idx != -1 {
			left = limitLeft + idx + 2
		} else if idx := strings.LastIndex(cleaned[limitLeft:matchLeft], "\n"); idx != -1 {
			// Single newline fallback.
			left = limitLeft + idx + 1
		} else {
			left = limitLeft
		}
	}

	limitRight := right + maxContext/2
	if limitRight > len(cleaned) {
		limitRight = len(cleaned)
	}
	// Expand right until punctuation or email header boundary.
	for right < limitRight {
		if cleaned[right] == '.' || cleaned[right] == '!' || cleaned[right] == '?' {
			right++
			break
		}
		// Stop at email header lines (From:, To:, Subject:, Date:, etc.).
		if cleaned[right] == '\n' {
			ls := right + 1
			le := ls
			for le < len(cleaned) && cleaned[le] != '\n' && le-ls < 128 {
				le++
			}
			if ls < len(cleaned) && emailHeaderRegex.MatchString(cleaned[ls:le]) {
				break
			}
		}
		right++
	}
	if right >= limitRight {
		// Paragraph fallback: next blank line in window.
		if idx := strings.Index(cleaned[matchRight:limitRight], "\n\n"); idx != -1 {
			right = matchRight + idx
		} else if idx := strings.Index(cleaned[matchRight:limitRight], "\n"); idx != -1 {
			right = matchRight + idx
		} else {
			right = limitRight
		}
	}

	return left, right
}

// ExtractMeaningfulExcerpts returns targeted, per-match snippets around each term.
// We extract tight, local windows around each match with email-aware boundaries,
// paragraph fallbacks, and punctuation-aware sentence ends. We avoid global scans.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func ExtractMeaningfulExcerpts(content string, searchTerms []string, maxExcerpts int, targetTerms int) []string {
	return ExtractMeaningfulExcerptsPartial(content, searchTerms, maxExcerpts, targetTerms, PartialModeOff)
}

// ExtractMeaningfulExcerptsPartial returns targeted excerpts using the requested
// term matching mode.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func ExtractMeaningfulExcerptsPartial(content string, searchTerms []string, maxExcerpts int, targetTerms int, partial PartialMode) []string {
	query := compileQuery(searchTerms, partial)
	return excerptSpanTexts(extractExcerptSpansQuery(content, query, maxExcerpts, targetTerms, false))
}

// ExtractMeaningfulExcerptsCode is the code-aware variant of ExtractMeaningfulExcerpts. It uses
// minimal, code-safe cleaning so source tokens (angle brackets, operators, PHP/markup) survive.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func ExtractMeaningfulExcerptsCode(content string, searchTerms []string, maxExcerpts int, targetTerms int) []string {
	return ExtractMeaningfulExcerptsCodePartial(content, searchTerms, maxExcerpts, targetTerms, PartialModeOff)
}

// ExtractMeaningfulExcerptsCodePartial is the code-aware partial-match variant.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func ExtractMeaningfulExcerptsCodePartial(content string, searchTerms []string, maxExcerpts int, targetTerms int, partial PartialMode) []string {
	query := compileQuery(searchTerms, partial)
	return excerptSpanTexts(extractExcerptSpansQuery(content, query, maxExcerpts, targetTerms, true))
}

// extractExcerptSpans extracts spans with default whole-word matching.
func extractExcerptSpans(content string, searchTerms []string, maxExcerpts int, isCode bool) []excerptSpan {
	return extractExcerptSpansPartial(content, searchTerms, maxExcerpts, len(searchTerms), isCode, PartialModeOff)
}

// extractExcerptSpansPartial extracts spans using the requested matching mode.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func extractExcerptSpansPartial(content string, searchTerms []string, maxExcerpts int, targetTerms int, isCode bool, partial PartialMode) []excerptSpan {
	query := compileQuery(searchTerms, partial)
	return extractExcerptSpansQuery(content, query, maxExcerpts, targetTerms, isCode)
}

// extractExcerptSpansQuery extracts spans using a compiled query.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above len(query.matchers) use that full count.
func extractExcerptSpansQuery(content string, query compiledQuery, maxExcerpts int, targetTerms int, isCode bool) []excerptSpan {
	return extractRankedExcerptSpansQuery(content, query, maxExcerpts, targetTerms, 0, isCode, unconstrainedDistance)
}

// extractRankedExcerptSpans emits per-file chunks ranked best-first. Every
// coverage level from minTerms up to targetTerms can seed a
// chunk, so a file's best cluster no longer suppresses its other, separate
// lower-quality clusters (the per-file result subset).
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func extractRankedExcerptSpans(content string, searchTerms []string, maxExcerpts int, targetTerms int, minTerms int, isCode bool, partial PartialMode) []excerptSpan {
	return extractRankedExcerptSpansPartial(content, searchTerms, maxExcerpts, targetTerms, minTerms, isCode, partial)
}

// extractRankedExcerptSpansPartial is the shared implementation. minTerms > 0
// enables multi-level candidate generation (per-file ranked subsets); the
// legacy single-level behavior runs otherwise.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above the normalized searchTerms count use that full count.
func extractRankedExcerptSpansPartial(content string, searchTerms []string, maxExcerpts int, targetTerms int, minTerms int, isCode bool, partial PartialMode) []excerptSpan {
	query := compileQuery(searchTerms, partial)
	return extractRankedExcerptSpansQuery(content, query, maxExcerpts, targetTerms, minTerms, isCode, unconstrainedDistance)
}

// extractRankedExcerptSpansQuery reuses the execution's exact matcher ranges.
// targetTerms is the distinct-term coverage target, not the weighted term-length
// Score. Values <= 0 or above len(query.matchers) use that full count.
// Nonnegative distance matches Stage 2's inclusive start-to-start bound, including
// zero; NewSearchEngine defaults Distance to 5000. unconstrainedDistance disables
// the bound for legacy helpers (all negative distances retain that behavior).
func extractRankedExcerptSpansQuery(content string, query compiledQuery, maxExcerpts int, targetTerms int, minTerms int, isCode bool, distance int) []excerptSpan {
	return extractRankedMappedExcerptSpansQuery(cleanStage3Content(content, isCode), query, maxExcerpts, targetTerms, minTerms, distance)
}

// extractRankedMappedExcerptSpansQuery consumes the same single cleaning result
// used for Stage 3's display content. Offsets always refer to the original input.
func extractRankedMappedExcerptSpansQuery(mapped mappedContent, query compiledQuery, maxExcerpts, targetTerms, minTerms, distance int) []excerptSpan {
	cleaned := mapped.text

	if maxExcerpts <= 0 {
		maxExcerpts = 3
	}
	if len(cleaned) == 0 || len(query.matchers) == 0 {
		return []excerptSpan{}
	}

	if targetTerms <= 0 || targetTerms > len(query.matchers) {
		targetTerms = len(query.matchers)
	}
	// Coverage levels that can seed a chunk. Ranked mode (minTerms > 0) spans
	// minTerms up to the Stage 2 term count so every allowed quality tier is
	// eligible; the legacy single-level path pins the level to targetTerms.
	levelMin, levelMax := targetTerms, targetTerms
	if minTerms > 0 {
		levelMin = max(1, minTerms)
	}

	// Clamp window for scanning sentence boundaries around each match
	maxContext := func() int {
		// Base from configured excerptContextLimit (typically the search distance). 0 means auto default.
		base := excerptContextLimit
		if base <= 0 {
			base = 800
		}
		// Scale relative to distance but clamp to keep excerpts readable and performant.
		win := base * 2
		if win < 200 {
			win = 200
		}
		if win > 4000 {
			win = 4000
		}
		return win
	}()
	excerpts := make([]excerptSpan, 0, maxExcerpts)
	seen := make(map[string]struct{})

	// Sliding-window strategy: collect candidate spans covering the target term count.
	// A single term is a one-term window, so every query shape uses this selector.
	type tmatch struct {
		start int
		end   int
		idx   int
	}
	all := make([]tmatch, 0, 128)
	rangesByMatcher := make([][]matchRange, len(query.matchers))
	allExactMatches := make([]excerptMatch, 0, 128)
	for i, matcher := range query.matchers {
		rangesByMatcher[i] = matcher.findAll(cleaned)
		for ordinal, loc := range rangesByMatcher[i] {
			all = append(all, tmatch{start: loc.start, end: loc.end, idx: i})
			allExactMatches = append(allExactMatches, excerptMatch{matchRange: loc, source: loc, term: i, occurrence: ordinal + 1})
		}
	}
	if len(all) > 0 {
		sort.Slice(all, func(i, j int) bool { return all[i].start < all[j].start })

		// Collect candidate spans at every coverage level in [levelMin, levelMax].
		// Each candidate records the distinct-term set it covered so its own
		// ranked quality (score, termCount) can ride with the chunk.
		type candidate struct {
			rawStart  int
			left      int
			right     int
			score     int
			termCount int
		}
		candidates := make([]candidate, 0, maxExcerpts)
		counts := make([]int, len(query.matchers))
		for level := levelMax; level >= levelMin; level-- {
			clear(counts)
			covered := 0
			r := 0
			for l := 0; l < len(all); l++ {
				for r < len(all) && covered < level {
					mm := all[r]
					if counts[mm.idx] == 0 {
						covered++
					}
					counts[mm.idx]++
					r++
				}
				// Match Stage 2's inclusive start-to-start proximity window.
				if covered == level && (minTerms <= 0 || counts[0] > 0) &&
					(distance <= unconstrainedDistance || all[r-1].start-all[l].start <= distance) {
					right := all[l].end
					score, termCount := 0, 0
					for i := l; i < r; i++ {
						if all[i].end > right {
							right = all[i].end
						}
					}
					for idx, count := range counts {
						if count > 0 {
							score += len(query.terms[idx])
							termCount++
						}
					}
					candidates = append(candidates, candidate{rawStart: mapped.offsets[all[l].start], left: all[l].start, right: right, score: score, termCount: termCount})
				}
				leftm := all[l]
				counts[leftm.idx]--
				if counts[leftm.idx] == 0 {
					covered--
				}
			}
		}

		// Rank candidates best-first: score desc, term count desc, tighter span
		// first, then document position (stable, deterministic).
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].score != candidates[j].score {
				return candidates[i].score > candidates[j].score
			}
			if candidates[i].termCount != candidates[j].termCount {
				return candidates[i].termCount > candidates[j].termCount
			}
			if candidates[i].right-candidates[i].left != candidates[j].right-candidates[j].left {
				return candidates[i].right-candidates[i].left < candidates[j].right-candidates[j].left
			}
			return candidates[i].left < candidates[j].left
		})

		// Dynamic budget from UI (fallback to 400).
		budget := 400
		if ExcerptCharBudget != nil {
			if b := ExcerptCharBudget(); b > 0 {
				budget = b
			}
		}
		if budget < 200 {
			budget = 200
		}

		// Best-first emission with the bounded-overlap guarantee. A candidate
		// seeds a chunk only when its matched core [left, right) is disjoint
		// from every prior emitted window, and its padded window must be >= 90% new
		// content against the emitted union (exact union arithmetic -- emission
		// order is ranked, not document order). Rejected candidates are simply
		// skipped; later (lower-ranked) ones stay eligible.
		emitted := make([]excerptSpan, 0, maxExcerpts)
		for _, c := range candidates {
			if len(excerpts) >= maxExcerpts {
				break
			}
			span := c.right - c.left
			window := excerptSpan{left: c.left, right: c.right}
			if span <= budget {
				pad := (budget - span) / 2
				window.left = max(0, c.left-pad)
				window.right = min(len(cleaned), c.right+pad)
				window.left, window.right = expandToBoundaries(cleaned, window.left, window.right, maxContext)
				if window.right-window.left > budget {
					center := c.left + span/2
					window.left = max(window.left, center-budget/2)
					window.right = min(window.right, window.left+budget)
					if window.right-window.left < budget {
						window.left = max(window.left, window.right-budget)
					}
				}
			}

			// Only context padding may overlap. Check the full half-open matched
			// core, including the right endpoint and any enclosed prior window,
			// so a wide multipart candidate cannot reuse a match under the 10% cap.
			seedIsNew := true
			for _, e := range emitted {
				if c.left < e.right && e.left < c.right {
					seedIsNew = false
					break
				}
			}
			if !seedIsNew {
				continue
			}
			if !excerptSpanWithinOverlapTolerance(window, emitted) {
				continue
			}

			if span <= budget {
				formatLeft, formatRight := normalizeExcerptBounds(cleaned, window.left, window.right)
				ex, matches := formatExcerptText(cleaned, formatLeft, formatRight, budget, allExactMatches)
				if _, ok := seen[ex]; !ok && ex != "" {
					seen[ex] = struct{}{}
					excerpts = append(excerpts, excerptSpan{
						text: ex, left: formatLeft, right: formatRight,
						rawStart: c.rawStart, rawStartValid: true,
						score: c.score, termCount: c.termCount, matches: matches,
						sourceText: cleaned, sourceMatches: allExactMatches,
					})
					emitted = append(emitted, window)
				}
			} else {
				perTerm := budget / max(1, len(query.matchers))
				if perTerm < 60 {
					perTerm = 60
				}
				parts := make([]string, 0, len(query.matchers))
				partMatches := make([]excerptMatch, 0, len(query.matchers))
				locations := make([]matchRange, 0, len(query.matchers))
				for i := range query.matchers {
					if loc, ok := firstRangeWithin(rangesByMatcher[i], c.left, c.right); ok {
						locations = append(locations, loc)
					}
				}
				sort.Slice(locations, func(i, j int) bool { return locations[i].start < locations[j].start })
				windows := make([]matchRange, 0, len(locations))
				for _, loc := range locations {
					center := loc.start + (loc.end-loc.start)/2
					partLeft := max(0, center-perTerm/2)
					partRight := min(len(cleaned), center+perTerm/2)
					partLeft, partRight = normalizeExcerptBounds(cleaned, partLeft, partRight)
					windows = append(windows, matchRange{start: partLeft, end: partRight})
				}
				// Nested matches can have centers in a different order than their
				// starts. Merge context windows in source order before formatting
				// so neither text nor match metadata is duplicated at overlaps.
				sort.Slice(windows, func(i, j int) bool { return windows[i].start < windows[j].start })
				merged := windows[:0]
				for _, part := range windows {
					if len(merged) > 0 && part.start <= merged[len(merged)-1].end {
						last := &merged[len(merged)-1]
						last.end = max(last.end, part.end)
					} else {
						merged = append(merged, part)
					}
				}
				for _, part := range merged {
					frag, fragMatches := formatMultipartExcerptText(cleaned, part.start, part.end, allExactMatches)
					if frag != "" {
						shift := 0
						if len(parts) > 0 {
							shift = len(strings.Join(parts, " ... ")) + len(" ... ")
						}
						for _, match := range fragMatches {
							match.start += shift
							match.end += shift
							partMatches = append(partMatches, match)
						}
						parts = append(parts, frag)
					}
				}
				ex := strings.Join(parts, " ... ")
				if len(ex) > budget {
					ex, partMatches = truncateExcerpt(ex, partMatches, budget)
				}
				if _, ok := seen[ex]; !ok && ex != "" {
					seen[ex] = struct{}{}
					excerpts = append(excerpts, excerptSpan{
						text: ex, left: c.left, right: c.right,
						rawStart: c.rawStart, rawStartValid: true,
						score: c.score, termCount: c.termCount, matches: partMatches,
						sourceText: cleaned, sourceMatches: allExactMatches,
					})
					emitted = append(emitted, window)
				}
			}
		}
		if len(excerpts) > 0 {
			return excerpts
		}
	}

	// Do not let the legacy sentence fallback bypass ranked constraints.
	if distance > unconstrainedDistance || minTerms > 0 {
		return excerpts
	}

	// Ensure at least one sentence per term (when possible)
	for _, ranges := range rangesByMatcher {
		if len(excerpts) >= maxExcerpts {
			break
		}
		if len(ranges) > 3 {
			ranges = ranges[:3]
		}
		for _, loc := range ranges {
			if len(excerpts) >= maxExcerpts {
				break
			}
			left, right := expandToBoundaries(cleaned, loc.start, loc.end, maxContext)
			left, right = normalizeExcerptBounds(cleaned, left, right)

			ex, matches := formatExcerptText(cleaned, left, right, 0, allExactMatches)
			if ex == "" {
				continue
			}

			if _, ok := seen[ex]; ok {
				continue
			}
			seen[ex] = struct{}{}
			excerpts = append(excerpts, excerptSpan{
				text: ex, left: left, right: right, matches: matches,
				rawStart: mapped.offsets[loc.start], rawStartValid: true,
				sourceText: cleaned, sourceMatches: allExactMatches,
			})
		}
	}

	if len(excerpts) > 0 {
		// Cap per-excerpt and total excerpt length based on UI-provided budget
		maxEx := 400
		if ExcerptCharBudget != nil {
			if b := ExcerptCharBudget(); b > 0 {
				maxEx = b
			}
		}
		maxTotal := maxEx * max(1, maxExcerpts)
		total := 0
		for i := range excerpts {
			if len(excerpts[i].text) > maxEx {
				excerpts[i].text, excerpts[i].matches = truncateExcerpt(
					excerpts[i].text,
					excerpts[i].matches,
					maxEx,
				)
			}
			total += len(excerpts[i].text)
			if total > maxTotal {
				// Trim current excerpt to fit and drop the rest
				excess := total - maxTotal
				cut := len(excerpts[i].text) - excess
				if cut < 0 {
					cut = 0
				}
				if cut < len(excerpts[i].text) {
					excerpts[i].text, excerpts[i].matches = truncateExcerpt(
						excerpts[i].text,
						excerpts[i].matches,
						cut,
					)
				}
				excerpts = excerpts[:i+1]
				break
			}
		}
		return excerpts
	}

	// Fallback: find the first occurrence of any term and expand within a small window
	for _, ranges := range rangesByMatcher {
		if len(ranges) == 0 {
			continue
		}
		loc := ranges[0]
		left, right := expandToBoundaries(cleaned, loc.start, loc.end, maxContext)
		left, right = normalizeExcerptBounds(cleaned, left, right)

		ex, matches := formatExcerptText(cleaned, left, right, 0, allExactMatches)
		if ex != "" {
			return []excerptSpan{{
				text: ex, left: left, right: right, matches: matches,
				rawStart: mapped.offsets[loc.start], rawStartValid: true,
				sourceText: cleaned, sourceMatches: allExactMatches,
			}}
		}
	}

	return []excerptSpan{}
}

// submatchRange returns the first capture group when present, preserving the
// exact token range for boundary-aware prefix patterns.
func submatchRange(loc []int) (int, int) {
	if len(loc) >= 4 && loc[2] >= 0 && loc[3] >= 0 {
		return loc[2], loc[3]
	}
	return loc[0], loc[1]
}

func expandShortExcerptSpan(span excerptSpan, target int) excerptSpan {
	sentences := sourceSentenceSpans(span.sourceText, span.sourceMatches)
	if len(sentences) == 0 || len(span.matches) == 0 || len(span.sourceMatches) == 0 {
		return span
	}

	sentenceAt := func(pos int) int {
		i := sort.Search(len(sentences), func(i int) bool {
			return sentences[i].end > pos
		})
		if i < len(sentences) && sentences[i].start <= pos {
			return i
		}
		return -1
	}
	left, right := len(sentences), -1
	for _, match := range span.matches {
		first := sentenceAt(match.source.start)
		last := sentenceAt(match.source.end - 1)
		if first == -1 || last == -1 {
			return span
		}
		left = min(left, first)
		right = max(right, last)
	}
	if right == -1 {
		return span
	}
	if sentences[right].end-sentences[left].start > target {
		return span
	}

	expanded := span
	for {
		text, matches := formatExcerptText(
			span.sourceText,
			sentences[left].start,
			sentences[right].end,
			0,
			span.sourceMatches,
		)
		expanded.text = text
		expanded.left = sentences[left].start
		expanded.right = sentences[right].end
		expanded.matches = matches
		if len(text) >= target || left == 0 && right == len(sentences)-1 {
			return expanded
		}
		if left > 0 {
			left--
		}
		if len(text) < target && right < len(sentences)-1 {
			right++
		}
	}
}

func sourceSentenceSpans(text string, matches []excerptMatch) []matchRange {
	abbreviations := map[string]bool{
		"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
		"sr": true, "jr": true, "st": true, "vs": true, "no": true,
		"inc": true, "ltd": true, "co": true,
	}
	matchRanges := make([]matchRange, 0, len(matches))
	for _, match := range matches {
		if match.source.end > match.source.start {
			matchRanges = append(matchRanges, match.source)
		}
	}
	sort.Slice(matchRanges, func(i, j int) bool {
		return matchRanges[i].start < matchRanges[j].start
	})
	for i := 1; i < len(matchRanges); i++ {
		if matchRanges[i].end < matchRanges[i-1].end {
			matchRanges[i].end = matchRanges[i-1].end
		}
	}
	boundaryInsideMatch := func(boundary int) bool {
		firstAtOrAfterBoundary := sort.Search(len(matchRanges), func(i int) bool {
			return matchRanges[i].start >= boundary
		})
		return firstAtOrAfterBoundary > 0 &&
			matchRanges[firstAtOrAfterBoundary-1].end > boundary
	}

	var spans []matchRange
	start := 0
	appendSpan := func(end int) {
		for start < end {
			r, size := utf8.DecodeRuneInString(text[start:end])
			if r == utf8.RuneError && size == 1 || !unicode.IsSpace(r) {
				break
			}
			start += size
		}
		for end > start {
			r, size := utf8.DecodeLastRuneInString(text[start:end])
			if r == utf8.RuneError && size == 1 || !unicode.IsSpace(r) {
				break
			}
			end -= size
		}
		if end > start {
			spans = append(spans, matchRange{start: start, end: end})
		}
		start = end
	}

	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && i+1 < len(text) && text[i+1] == '\n' {
			if boundaryInsideMatch(i+1) || boundaryInsideMatch(i+2) {
				i++
				continue
			}
			appendSpan(i)
			start = i + 2
			i++
			continue
		}
		if text[i] != '.' && text[i] != '!' && text[i] != '?' {
			continue
		}
		end := i + 1
		if boundaryInsideMatch(end) {
			continue
		}
		if text[i] == '.' {
			if i > 0 && i+1 < len(text) && text[i-1] >= '0' && text[i-1] <= '9' &&
				text[i+1] >= '0' && text[i+1] <= '9' {
				continue
			}
			tokenStart := i
			for tokenStart > start {
				r, size := utf8.DecodeLastRuneInString(text[start:tokenStart])
				if !unicode.IsLetter(r) {
					break
				}
				tokenStart -= size
			}
			if abbreviations[strings.ToLower(text[tokenStart:i])] {
				continue
			}
		}
		nextUpper := false
		if end < len(text) {
			next, _ := utf8.DecodeRuneInString(text[end:])
			nextUpper = unicode.IsUpper(next)
		}
		if end == len(text) || nextUpper || end < len(text) && (text[end] == ' ' || text[end] == '\t' || text[end] == '\r' || text[end] == '\n') {
			appendSpan(end)
			start = end
		}
	}
	appendSpan(len(text))
	return spans
}

// containsWholeWord checks if text contains a whole word (case insensitive, plural-aware)
func containsWholeWord(text, word string) bool {
	// Match base, base+s, or base+es. getWordRegex caches the compiled pattern so
	// this stays cheap when called per-file across many exclude words.
	pattern := fmt.Sprintf(`(?i)\b(?:%s(?:es|s)?)\b`, regexp.QuoteMeta(word))
	return getWordRegex(pattern).MatchString(text)
}

// MatchRange is a half-open byte range [Start, End) of a source-validated match,
// along with the canonical query Term that produced it when known.
type MatchRange struct {
	Start int
	End   int
	Term  string
}

// HighlightTerms highlights search terms in text with legacy whole-word matching.
func HighlightTerms(text string, searchTerms []string) string {
	return HighlightTermsPartial(text, searchTerms, PartialModeOff)
}

// HighlightTermsPartial highlights search terms without coloring left boundary
// characters consumed by prefix-match patterns.
func HighlightTermsPartial(text string, searchTerms []string, partial PartialMode) string {
	return highlightTermsQuery(text, compileQuery(searchTerms, partial))
}

// HighlightTermsWithOptions highlights terms with explicit matching behavior.
func HighlightTermsWithOptions(text string, searchTerms []string, partial PartialMode, options QueryOptions) string {
	return highlightTermsQuery(text, compileQueryWithOptions(searchTerms, partial, options))
}

// FindMatchRangesWithOptions returns source-validated byte ranges for all search
// terms in text under the given matching mode and options.
func FindMatchRangesWithOptions(text string, searchTerms []string, partial PartialMode, options QueryOptions) []MatchRange {
	query := compileQueryWithOptions(searchTerms, partial, options)
	var ranges []MatchRange
	for i, matcher := range query.matchers {
		var term string
		if i < len(query.terms) {
			term = query.terms[i]
		}
		for _, loc := range matcher.findAll(text) {
			ranges = append(ranges, MatchRange{Start: loc.start, End: loc.end, Term: term})
		}
	}
	return ranges
}

// HighlightRanges applies ANSI highlight styling to pre-validated byte ranges in text.
func HighlightRanges(text string, ranges []MatchRange) string {
	if len(ranges) == 0 {
		return text
	}
	exact := make([]matchRange, 0, len(ranges))
	for _, r := range ranges {
		start := max(0, min(r.Start, len(text)))
		end := max(start, min(r.End, len(text)))
		if utf8.ValidString(text) {
			for start < end && !utf8.RuneStart(text[start]) {
				start++
			}
			for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
				end--
			}
		}
		if end > start {
			exact = append(exact, matchRange{start: start, end: end})
		}
	}
	return highlightExactRanges(text, exact)
}

func highlightTermsQuery(text string, query compiledQuery) string {
	var ranges []matchRange
	for _, matcher := range query.matchers {
		ranges = append(ranges, matcher.findAll(text)...)
	}
	return highlightExactRanges(text, ranges)
}

func excerptSpanMatchRanges(span excerptSpan, query compiledQuery) []MatchRange {
	ranges := make([]MatchRange, len(span.matches))
	for i, match := range span.matches {
		var term string
		if match.term >= 0 && match.term < len(query.terms) {
			term = query.terms[match.term]
		}
		ranges[i] = MatchRange{
			Start: match.start,
			End:   match.end,
			Term:  term,
		}
	}
	return ranges
}

func highlightExcerptSpan(span excerptSpan) string {
	ranges := make([]matchRange, len(span.matches))
	for i, match := range span.matches {
		ranges[i] = match.matchRange
	}
	return highlightExactRanges(span.text, ranges)
}

func highlightExactRanges(text string, ranges []matchRange) string {
	const HI = "\033[1;31m"
	const NC = "\033[0m"

	if len(ranges) == 0 {
		return text
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end > ranges[j].end
	})
	merged := ranges[:0]
	for _, loc := range ranges {
		if loc.end <= loc.start {
			continue
		}
		if len(merged) > 0 && loc.start < merged[len(merged)-1].end {
			if loc.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = loc.end
			}
			continue
		}
		merged = append(merged, loc)
	}

	var result strings.Builder
	result.Grow(len(text) + len(merged)*(len(HI)+len(NC)))
	cursor := 0
	for _, loc := range merged {
		result.WriteString(text[cursor:loc.start])
		result.WriteString(HI)
		result.WriteString(text[loc.start:loc.end])
		result.WriteString(NC)
		cursor = loc.end
	}
	result.WriteString(text[cursor:])
	return result.String()
}

package search

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

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

// ExtractMeaningfulExcerpts returns targeted, per-match snippets around each term.
// We extract tight, local windows around each match with email-aware boundaries,
// paragraph fallbacks, and punctuation-aware sentence ends. We avoid global scans.
func ExtractMeaningfulExcerpts(content string, searchTerms []string, maxExcerpts int, targetScore int) []string {
	return ExtractMeaningfulExcerptsPartial(content, searchTerms, maxExcerpts, targetScore, PartialModeOff)
}

// ExtractMeaningfulExcerptsPartial returns targeted excerpts using the requested
// term matching mode.
func ExtractMeaningfulExcerptsPartial(content string, searchTerms []string, maxExcerpts int, targetScore int, partial PartialMode) []string {
	return extractMeaningfulExcerpts(content, searchTerms, maxExcerpts, targetScore, false, partial)
}

// ExtractMeaningfulExcerptsCode is the code-aware variant of ExtractMeaningfulExcerpts. It uses
// minimal, code-safe cleaning so source tokens (angle brackets, operators, PHP/markup) survive.
func ExtractMeaningfulExcerptsCode(content string, searchTerms []string, maxExcerpts int, targetScore int) []string {
	return ExtractMeaningfulExcerptsCodePartial(content, searchTerms, maxExcerpts, targetScore, PartialModeOff)
}

// ExtractMeaningfulExcerptsCodePartial is the code-aware partial-match variant.
func ExtractMeaningfulExcerptsCodePartial(content string, searchTerms []string, maxExcerpts int, targetScore int, partial PartialMode) []string {
	return extractMeaningfulExcerpts(content, searchTerms, maxExcerpts, targetScore, true, partial)
}

func extractMeaningfulExcerpts(content string, searchTerms []string, maxExcerpts int, targetScore int, isCode bool, partial PartialMode) []string {
	var cleaned string
	if isCode {
		// Minimal cleaning: preserve every code token; only normalize control chars/whitespace.
		cleaned = CleanContentCode(content)
	} else {
		// Line-preserving clean for boundary finding: remove heavy markup/noise but keep newlines
		prep := cssRegex.ReplaceAllString(content, "")
		prep = jsRegex.ReplaceAllString(prep, "")
		prep = htmlTagRegex.ReplaceAllString(prep, " ")
		prep = htmlEntityRegex.ReplaceAllString(prep, " ")
		prep = controlCharRegex.ReplaceAllString(prep, "")
		prep = junkSymbolsRegex.ReplaceAllString(prep, "")
		prep = emailQuotingRegex.ReplaceAllString(prep, "")
		// Strip leading '>' markers and collapse midline quote markers
		prep = quoteLineStartRegex.ReplaceAllString(prep, "")
		prep = quoteMidRegex.ReplaceAllString(prep, " ")
		cleaned = prep
	}

	if maxExcerpts <= 0 {
		maxExcerpts = 3
	}
	if len(cleaned) == 0 || len(searchTerms) == 0 {
		return []string{}
	}

	// Build regexes for each term using the same partial-aware matcher as filtering.
	termRE := make([]*regexp.Regexp, 0, len(searchTerms))
	for _, t := range searchTerms {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		termRE = append(termRE, buildTermRegexCI(tt, partial))
	}
	if len(termRE) == 0 {
		return []string{}
	}
	if targetScore <= 0 || targetScore > len(termRE) {
		targetScore = len(termRE)
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
	excerpts := make([]string, 0, maxExcerpts)
	seen := make(map[string]struct{})

	// Sliding-window strategy: collect non-overlapping spans covering all terms.
	if len(termRE) > 1 {
		type tmatch struct {
			start int
			end   int
			idx   int
		}
		type candidate struct {
			left  int
			right int
		}
		all := make([]tmatch, 0, 128)
		for i, re := range termRE {
			idxs := re.FindAllStringSubmatchIndex(cleaned, -1)
			for _, loc := range idxs {
				start, end := submatchRange(loc)
				all = append(all, tmatch{start: start, end: end, idx: i})
			}
		}
		if len(all) > 0 {
			sort.Slice(all, func(i, j int) bool { return all[i].start < all[j].start })
			counts := make(map[int]int, len(termRE))
			covered := 0
			r := 0
			candidates := make([]candidate, 0, maxExcerpts)
			for l := 0; l < len(all); l++ {
				for r < len(all) && covered < targetScore {
					mm := all[r]
					if counts[mm.idx] == 0 {
						covered++
					}
					counts[mm.idx]++
					r++
				}
				if covered == targetScore {
					right := all[l].end
					for i := l; i < r; i++ {
						if all[i].end > right {
							right = all[i].end
						}
					}
					candidates = append(candidates, candidate{left: all[l].start, right: right})
				}
				leftm := all[l]
				counts[leftm.idx]--
				if counts[leftm.idx] == 0 {
					covered--
				}
			}
			if len(candidates) > 0 {
				// Dynamic budget from UI (fallback to 400)
				budget := 400
				if ExcerptCharBudget != nil {
					if b := ExcerptCharBudget(); b > 0 {
						budget = b
					}
				}
				if budget < 200 {
					budget = 200
				}

				selected := make([]candidate, 0, maxExcerpts)
				lastRight := -1
				for _, c := range candidates {
					if c.left < lastRight {
						continue
					}
					selected = append(selected, c)
					lastRight = c.right
					if len(selected) >= maxExcerpts {
						break
					}
				}

				for _, c := range selected {
					span := c.right - c.left
					left := c.left
					right := c.right
					if span <= budget {
						pad := (budget - span) / 2
						left = max(0, left-pad)
						right = min(len(cleaned), right+pad)

						for left > 0 && cleaned[left] != ' ' {
							left--
						}
						for right < len(cleaned) && right > 0 && cleaned[right-1] != ' ' {
							right++
							if right >= len(cleaned) {
								break
							}
						}
						ex := strings.TrimSpace(cleaned[left:right])
						ex = strings.ReplaceAll(ex, "\n", " ")
						ex = strings.ReplaceAll(ex, "\t", " ")
						ex = dividerRegex.ReplaceAllString(ex, " ")
						ex = whitespaceRegex.ReplaceAllString(ex, " ")
						if len(ex) > budget {
							ex = ex[:budget]
						}
						if _, ok := seen[ex]; !ok && ex != "" {
							seen[ex] = struct{}{}
							excerpts = append(excerpts, ex)
						}
					} else {
						perTerm := budget / max(1, len(termRE))
						if perTerm < 60 {
							perTerm = 60
						}
						parts := make([]string, 0, len(termRE))
						spanText := cleaned[c.left:c.right]
						for _, re := range termRE {
							loc := re.FindStringSubmatchIndex(spanText)
							if loc == nil {
								continue
							}
							start, end := submatchRange(loc)
							center := c.left + start + (end-start)/2
							partLeft := max(0, center-perTerm/2)
							partRight := min(len(cleaned), center+perTerm/2)
							frag := strings.TrimSpace(cleaned[partLeft:partRight])
							frag = strings.ReplaceAll(frag, "\n", " ")
							frag = strings.ReplaceAll(frag, "\t", " ")
							frag = dividerRegex.ReplaceAllString(frag, " ")
							frag = whitespaceRegex.ReplaceAllString(frag, " ")
							if frag != "" {
								parts = append(parts, frag)
							}
						}
						ex := strings.Join(parts, " ... ")
						if len(ex) > budget {
							ex = ex[:budget]
						}
						if _, ok := seen[ex]; !ok && ex != "" {
							seen[ex] = struct{}{}
							excerpts = append(excerpts, ex)
						}
					}
					if len(excerpts) >= maxExcerpts {
						break
					}
				}
			}
			if len(excerpts) > 0 {
				return excerpts
			}
		}
	}

	// Ensure at least one sentence per term (when possible)
	for _, re := range termRE {
		if len(excerpts) >= maxExcerpts {
			break
		}
		locs := re.FindAllStringSubmatchIndex(cleaned, 3) // up to 3 matches per term
		for _, loc := range locs {
			if len(excerpts) >= maxExcerpts {
				break
			}
			start, end := submatchRange(loc)

			// Find local sentence boundaries with clamped scan (email-aware + punctuation)
			left := start
			limitLeft := left - maxContext/2
			if limitLeft < 0 {
				limitLeft = 0
			}
			// Expand left until punctuation or email header boundary
			for left > limitLeft {
				if cleaned[left] == '.' || cleaned[left] == '!' || cleaned[left] == '?' {
					break
				}
				// Stop at email header lines (From:, To:, Subject:, Date:, etc.)
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
				// Paragraph fallback: last blank line in window
				if idx := strings.LastIndex(cleaned[limitLeft:start], "\n\n"); idx != -1 {
					left = limitLeft + idx + 2
				} else {
					// Single newline fallback
					if idx := strings.LastIndex(cleaned[limitLeft:start], "\n"); idx != -1 {
						left = limitLeft + idx + 1
					} else {
						left = limitLeft
					}
				}
			}

			right := end
			limitRight := right + maxContext/2
			if limitRight > len(cleaned) {
				limitRight = len(cleaned)
			}
			// Expand right until punctuation or email header boundary
			for right < limitRight {
				if cleaned[right] == '.' || cleaned[right] == '!' || cleaned[right] == '?' {
					right++
					break
				}
				// Stop at email header lines (From:, To:, Subject:, Date:, etc.)
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
				// Paragraph fallback: next blank line in window
				if idx := strings.Index(cleaned[end:limitRight], "\n\n"); idx != -1 {
					right = end + idx
				} else if idx := strings.Index(cleaned[end:limitRight], "\n"); idx != -1 {
					right = end + idx
				} else {
					right = limitRight
				}
			}

			// Build sentence snippet and normalize whitespace
			ex := strings.TrimSpace(cleaned[left:right])
			if ex == "" {
				continue
			}
			ex = strings.ReplaceAll(ex, "\n", " ")
			ex = strings.ReplaceAll(ex, "\t", " ")
			ex = whitespaceRegex.ReplaceAllString(ex, " ")

			if _, ok := seen[ex]; ok {
				continue
			}
			seen[ex] = struct{}{}
			excerpts = append(excerpts, ex)
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
			if len(excerpts[i]) > maxEx {
				excerpts[i] = excerpts[i][:maxEx]
			}
			total += len(excerpts[i])
			if total > maxTotal {
				// Trim current excerpt to fit and drop the rest
				excess := total - maxTotal
				cut := len(excerpts[i]) - excess
				if cut < 0 {
					cut = 0
				}
				if cut < len(excerpts[i]) {
					excerpts[i] = excerpts[i][:cut]
				}
				excerpts = excerpts[:i+1]
				break
			}
		}
		return excerpts
	}

	// Fallback: find the first occurrence of any term and expand within a small window
	for _, re := range termRE {
		loc := re.FindStringSubmatchIndex(cleaned)
		if loc == nil {
			continue
		}
		start, end := submatchRange(loc)

		left := start
		limitLeft := left - maxContext/2
		if limitLeft < 0 {
			limitLeft = 0
		}
		for left > limitLeft {
			if cleaned[left] == '.' || cleaned[left] == '!' || cleaned[left] == '?' {
				break
			}
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
			if idx := strings.LastIndex(cleaned[limitLeft:start], "\n\n"); idx != -1 {
				left = limitLeft + idx + 2
			} else if idx := strings.LastIndex(cleaned[limitLeft:start], "\n"); idx != -1 {
				left = limitLeft + idx + 1
			} else {
				left = limitLeft
			}
		}

		right := end
		limitRight := right + maxContext/2
		if limitRight > len(cleaned) {
			limitRight = len(cleaned)
		}
		for right < limitRight {
			if cleaned[right] == '.' || cleaned[right] == '!' || cleaned[right] == '?' {
				right++
				break
			}
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
			if idx := strings.Index(cleaned[end:limitRight], "\n\n"); idx != -1 {
				right = end + idx
			} else if idx := strings.Index(cleaned[end:limitRight], "\n"); idx != -1 {
				right = end + idx
			} else {
				right = limitRight
			}
		}

		ex := strings.TrimSpace(cleaned[left:right])
		ex = strings.ReplaceAll(ex, "\n", " ")
		ex = strings.ReplaceAll(ex, "\t", " ")
		ex = whitespaceRegex.ReplaceAllString(ex, " ")
		if ex != "" {
			return []string{ex}
		}
	}

	return []string{}
}

// submatchRange returns the first capture group when present, preserving the
// exact token range for boundary-aware prefix patterns.
func submatchRange(loc []int) (int, int) {
	if len(loc) >= 4 && loc[2] >= 0 && loc[3] >= 0 {
		return loc[2], loc[3]
	}
	return loc[0], loc[1]
}

// matchInfo represents a search term match location
type matchInfo struct {
	start int
	end   int
	term  string
}

// containsAnySearchTerm checks if text contains any of the search terms
func containsAnySearchTerm(text string, searchTerms []string) bool {
	for _, term := range searchTerms {
		if containsWholeWord(text, term) {
			return true
		}
	}
	return false
}

// splitIntoSentences splits content into sentences for better excerpt extraction
// Improved boundaries:
// - End at punctuation followed by whitespace OR directly followed by an uppercase letter.
// - Avoid splitting inside common abbreviations (e.g., "e.g.", "i.e.", "Dr.", "U.S.") and decimals (e.g., "3.14").
// - Treat double newlines as hard paragraph boundaries.
// Falls back to line or chunk splitting when boundaries are scarce.
func splitIntoSentences(content string) []string {
	// Clean content first
	cleaned := CleanContent(content)
	if strings.TrimSpace(cleaned) == "" {
		return []string{}
	}

	// Common abbreviations (lowercased without trailing dot)
	abbr := map[string]bool{
		"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true,
		"sr": true, "jr": true, "st": true, "vs": true, "no": true,
		"inc": true, "ltd": true, "co": true, "u.s": true, "u.k": true,
		"e.g": true, "i.e": true,
	}

	sentences := make([]string, 0, 16)
	var sb strings.Builder

	isUpper := func(r rune) bool { return unicode.IsUpper(r) }
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }

	runes := []rune(cleaned)
	n := len(runes)
	for i := 0; i < n; i++ {
		r := runes[i]
		sb.WriteRune(r)

		// Hard paragraph boundary on double newline
		if r == '\n' {
			if i+1 < n && runes[i+1] == '\n' {
				s := strings.TrimSpace(sb.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				sb.Reset()
				i++ // consume the second newline
				continue
			}
		}

		// Sentence-ending punctuation
		if r == '.' || r == '!' || r == '?' {
			// Avoid decimals: digit '.' digit
			if i > 0 && i+1 < n && isDigit(runes[i-1]) && isDigit(runes[i+1]) {
				continue
			}

			// Abbreviation check: capture token before dot
			j := i - 1
			for j >= 0 && unicode.IsSpace(runes[j]) {
				j--
			}
			k := j
			for k >= 0 && unicode.IsLetter(runes[k]) {
				k--
			}
			token := strings.ToLower(string(runes[k+1 : j+1])) // letters immediately before '.'
			if token != "" && abbr[token] {
				// e.g., "Dr.", "Inc.", "e.g.", "U.S."
				// For initialisms like "U.S." the next '.' will be handled here as well.
				continue
			}

			// Decide boundary based on next rune
			nextIsUpper, nextIsSpace := false, false
			if i+1 < n {
				next := runes[i+1]
				nextIsUpper = isUpper(next)
				nextIsSpace = unicode.IsSpace(next)
			} else {
				nextIsSpace = true
			}

			if nextIsSpace || nextIsUpper {
				// End of sentence here
				s := strings.TrimSpace(sb.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				sb.Reset()

				// Skip a single space after punctuation to avoid leading space
				if nextIsSpace && i+1 < n && unicode.IsSpace(runes[i+1]) {
					i++
				}
			}
		}
	}

	// Flush remainder
	if sb.Len() > 0 {
		s := strings.TrimSpace(sb.String())
		if s != "" {
			sentences = append(sentences, s)
		}
	}

	// Fallbacks when we couldn't segment well
	if len(sentences) == 0 {
		parts := strings.Split(cleaned, "\n")
		if len(parts) > 1 {
			return parts
		}
		if len(cleaned) > 500 {
			if strings.Contains(cleaned, "  ") {
				return strings.Split(cleaned, "  ")
			}
			return chunkText(cleaned, 100)
		}
		return []string{cleaned}
	}

	return sentences
}

// chunkText splits text into chunks at word boundaries
func chunkText(text string, chunkSize int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}

	var chunks []string
	var currentChunk strings.Builder

	for _, word := range words {
		if currentChunk.Len()+len(word)+1 > chunkSize && currentChunk.Len() > 0 {
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
		}

		if currentChunk.Len() > 0 {
			currentChunk.WriteString(" ")
		}
		currentChunk.WriteString(word)
	}

	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	return chunks
}

// containsWholeWord checks if text contains a whole word (case insensitive, plural-aware)
func containsWholeWord(text, word string) bool {
	// Match base, base+s, or base+es. getWordRegex caches the compiled pattern so
	// this stays cheap when called per-file across many exclude words.
	pattern := fmt.Sprintf(`(?i)\b(?:%s(?:es|s)?)\b`, regexp.QuoteMeta(word))
	return getWordRegex(pattern).MatchString(text)
}

// HighlightTerms highlights search terms in text with legacy whole-word matching.
func HighlightTerms(text string, searchTerms []string) string {
	return HighlightTermsPartial(text, searchTerms, PartialModeOff)
}

// HighlightTermsPartial highlights search terms without coloring left boundary
// characters consumed by prefix-match patterns.
func HighlightTermsPartial(text string, searchTerms []string, partial PartialMode) string {
	const HI = "\033[1;31m" // bold red for stronger, more visible highlighting
	const NC = "\033[0m"

	result := text
	for _, term := range searchTerms {
		base := strings.TrimSpace(term)
		mode := partial
		isGlob := strings.HasSuffix(base, "*") && len(strings.TrimSuffix(base, "*")) > 1
		if isGlob {
			base = strings.TrimSuffix(base, "*")
			if mode != PartialModeContains {
				mode = PartialModePrefix
			}
		} else if len(base) <= 2 {
			mode = PartialModeOff
		}

		switch mode {
		case PartialModePrefix:
			pattern := fmt.Sprintf(`(?i)(^|[^a-zA-Z0-9_]|_)(%s\w*)`, regexp.QuoteMeta(base))
			result = getWordRegex(pattern).ReplaceAllString(result, "$1"+HI+"$2"+NC)
		case PartialModeContains:
			pattern := fmt.Sprintf(`(?i)(%s)`, regexp.QuoteMeta(base))
			result = getWordRegex(pattern).ReplaceAllString(result, HI+"$1"+NC)
		default:
			pattern := fmt.Sprintf(`(?i)\b(?:%s(?:es|s)?)\b`, regexp.QuoteMeta(term))
			result = getWordRegex(pattern).ReplaceAllString(result, HI+"$0"+NC)
		}
	}

	return result
}

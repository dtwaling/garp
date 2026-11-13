package search

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	minWildcardLiteralBytes = 2
	maxWildcardLiteralBytes = 128
	maxWildcardStars        = 4
	maxWildcardQuestions    = 16
	maxWildcardExpansion    = 128
	termMatcherCacheMaxSize = 512
)

type termMatcherCacheKey struct {
	term          string
	mode          PartialMode
	caseSensitive bool
	smartForms    bool
}

var (
	termMatcherCache   = make(map[termMatcherCacheKey]termMatcher)
	termMatcherCacheMu sync.RWMutex
)

// matchRange is an exact byte range in the searched text.
type matchRange struct {
	start int
	end   int
}

// compiledQuery is the immutable matching form of one normalized query.
// For a nonempty query, terms[0] and matchers[0] represent the primary query term
// used for ranked seeding in Stages 2 and 3. Compilation preserves normalized
// input order and keeps terms and matchers aligned by index.
type compiledQuery struct {
	terms             []string
	matchers          []termMatcher
	maxMatchBytes     int
	maxDetectionBytes int
	// owners is nil except in prefilter admission queries, where matchers[i]
	// must hit together with all matchers sharing its owner to admit
	// terms[owners[i]]. Never pass these queries to ranking or excerpt code.
	owners []int
}

// QueryOptions defines immutable matching behavior for a compiled query.
type QueryOptions struct {
	CaseSensitive bool
	SmartForms    bool
}

// termMatcher owns the canonical and effective matching semantics for one term.
type termMatcher struct {
	term            string
	mode            PartialMode
	validWildcard   bool
	startsWildcard  bool
	endsWildcard    bool
	corePattern     string
	core            *regexp.Regexp
	prefixBase      *regexp.Regexp
	compatibility   *regexp.Regexp
	leadingBound    bool
	trailingBound   bool
	strictBoundary  bool
	caseInsensitive bool
	smartForms      bool
	byteExact       bool
	// maxMatchBytes is -1 when the selected range is unbounded.
	maxMatchBytes int
	// detectionBytes covers the finite bytes needed to detect a match.
	// Chunked callers retain one additional byte for a leading boundary.
	detectionBytes int
}

// NormalizeQueryTerms applies the default case-insensitive query contract.
func NormalizeQueryTerms(terms []string) []string {
	return NormalizeQueryTermsWithOptions(terms, QueryOptions{})
}

// NormalizeQueryTermsWithOptions canonicalizes and deduplicates query terms.
func NormalizeQueryTermsWithOptions(terms []string, options QueryOptions) []string {
	normalized := make([]string, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		term = collapseStars(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		key := term
		if !options.CaseSensitive {
			term = lowerWithinSimpleFoldOrbit(term)
			key = unicodeFoldKey(term)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, term)
	}
	return normalized
}

func lowerWithinSimpleFoldOrbit(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		lower := unicode.ToLower(r)
		if lower != r && runeInSimpleFoldOrbit(r, lower) {
			r = lower
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func runeInSimpleFoldOrbit(start, candidate rune) bool {
	for folded := unicode.SimpleFold(start); folded != start; folded = unicode.SimpleFold(folded) {
		if folded == candidate {
			return true
		}
	}
	return false
}

func collapseStars(term string) string {
	var b strings.Builder
	b.Grow(len(term))
	previousStar := false
	for i := 0; i < len(term); i++ {
		c := term[i]
		if c == '*' {
			if previousStar {
				continue
			}
			previousStar = true
		} else {
			previousStar = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

func unicodeFoldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		canonical := r
		for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
			if folded < canonical {
				canonical = folded
			}
		}
		b.WriteRune(canonical)
		i += size
	}
	return b.String()
}

func compileQuery(terms []string, mode PartialMode) compiledQuery {
	return compileQueryWithOptions(terms, mode, QueryOptions{})
}

func compileQueryWithOptions(terms []string, mode PartialMode, options QueryOptions) compiledQuery {
	normalized := NormalizeQueryTermsWithOptions(terms, options)
	query := compiledQuery{
		terms:    append([]string(nil), normalized...),
		matchers: make([]termMatcher, 0, len(normalized)),
	}
	for _, term := range normalized {
		matcher := compileTermMatcherWithOptions(term, mode, options)
		query.matchers = append(query.matchers, matcher)
		if matcher.maxMatchBytes < 0 {
			query.maxMatchBytes = -1
		} else if query.maxMatchBytes >= 0 && matcher.maxMatchBytes > query.maxMatchBytes {
			query.maxMatchBytes = matcher.maxMatchBytes
		}
		if matcher.detectionBytes > query.maxDetectionBytes {
			query.maxDetectionBytes = matcher.detectionBytes
		}
	}
	return query
}

func compileTermMatcher(term string, requestedMode PartialMode, caseInsensitive bool) termMatcher {
	return compileTermMatcherWithOptions(term, requestedMode, QueryOptions{
		CaseSensitive: !caseInsensitive,
	})
}

func compileTermMatcherWithOptions(term string, requestedMode PartialMode, options QueryOptions) termMatcher {
	term = collapseStars(strings.TrimSpace(term))
	key := termMatcherCacheKey{
		term:          term,
		mode:          requestedMode,
		caseSensitive: options.CaseSensitive,
		smartForms:    options.SmartForms,
	}
	termMatcherCacheMu.RLock()
	matcher, ok := termMatcherCache[key]
	termMatcherCacheMu.RUnlock()
	if ok {
		return matcher
	}

	matcher = compileTermMatcherUncached(term, requestedMode, !options.CaseSensitive, options.SmartForms)
	termMatcherCacheMu.Lock()
	if cached, ok := termMatcherCache[key]; ok {
		matcher = cached
	} else {
		if len(termMatcherCache) >= termMatcherCacheMaxSize {
			termMatcherCache = make(map[termMatcherCacheKey]termMatcher)
		}
		termMatcherCache[key] = matcher
	}
	termMatcherCacheMu.Unlock()
	return matcher
}

func compileTermMatcherUncached(term string, requestedMode PartialMode, caseInsensitive, smartForms bool) termMatcher {
	if term == "" {
		never := getWordRegex(`a\A`)
		return termMatcher{
			term:          term,
			mode:          requestedMode,
			corePattern:   `a\A`,
			core:          never,
			compatibility: never,
		}
	}

	stars := strings.Count(term, "*")
	questions := strings.Count(term, "?")
	literalBytes := len(term) - stars - questions
	hasWildcardSyntax := stars > 0 || questions > 0
	validWildcard := hasWildcardSyntax &&
		literalBytes >= minWildcardLiteralBytes &&
		literalBytes <= maxWildcardLiteralBytes &&
		stars <= maxWildcardStars &&
		questions <= maxWildcardQuestions

	mode := requestedMode
	if hasWildcardSyntax && !validWildcard {
		mode = PartialModeOff
	} else if !validWildcard && len(term) <= 2 {
		mode = PartialModeOff
	}

	var searchPattern string
	var prefixBasePattern string
	literalFoldBytes := widestFoldedLiteralBytes(term, caseInsensitive, validWildcard)
	maxMatchBytes := literalFoldBytes
	detectionBytes := literalFoldBytes
	if validWildcard {
		starWidth := 0
		if stars > 0 {
			starWidth = maxWildcardExpansion / stars
		}
		searchPattern = wildcardPattern(term, starWidth, caseInsensitive)
		internalStars := stars
		if term[0] == '*' {
			internalStars--
		}
		if len(term) > 1 && term[len(term)-1] == '*' {
			internalStars--
		}
		maxMatchBytes = literalFoldBytes + questions + internalStars*starWidth
		detectionBytes = maxMatchBytes
	} else {
		searchPattern = literalPattern(term, caseInsensitive)
		switch mode {
		case PartialModePrefix:
			prefixBasePattern = searchPattern
			searchPattern += `[a-zA-Z0-9_]*`
			maxMatchBytes = -1
		case PartialModeContains:
			// The literal core is sufficient.
		default:
			if isWordByte(term[len(term)-1]) && (caseInsensitive || smartForms) {
				suffix := inflectionSuffix(caseInsensitive, smartForms)
				searchPattern = `(?:` + searchPattern + suffix + `)`
				suffixBytes := 2
				if smartForms {
					suffixBytes = 5
				}
				maxMatchBytes += suffixBytes
				detectionBytes += suffixBytes
			}
		}
	}

	startsWildcard := validWildcard && len(term) > 0 && (term[0] == '*' || term[0] == '?')
	endsWildcard := validWildcard && len(term) > 0 && (term[len(term)-1] == '*' || term[len(term)-1] == '?')
	leadingBound := !startsWildcard && mode != PartialModeContains
	trailingBound := !endsWildcard && mode == PartialModeOff
	strictBoundary := hasWildcardSyntax || strings.ContainsAny(term, "-.")
	byteExact := regexpInexact(term)

	if trailingBound {
		assertion := `\B`
		if isWordByte(term[len(term)-1]) {
			assertion = `\b`
		}
		searchPattern += assertion
	}

	compatibilityPattern := "(" + searchPattern + ")"
	if leadingBound {
		leftBoundary := `(?:^|[^a-zA-Z0-9_])`
		if !strictBoundary && mode == PartialModePrefix {
			leftBoundary = `(?:^|[^a-zA-Z0-9_]|_)`
		}
		compatibilityPattern = leftBoundary + compatibilityPattern
	}
	corePattern := searchPattern
	if byteExact {
		// regexp maps every invalid byte and literal U+FFFD to RuneError.
		// Do not expose an inexact regexp match. Later tasks replace remaining
		// compatibility callers with exact matcher ranges.
		corePattern = `a\A`
		compatibilityPattern = `a\A`
		prefixBasePattern = ""
	}

	var prefixBase *regexp.Regexp
	if prefixBasePattern != "" {
		prefixBase = getWordRegex(prefixBasePattern)
	}
	return termMatcher{
		term:            term,
		mode:            mode,
		validWildcard:   validWildcard,
		startsWildcard:  startsWildcard,
		endsWildcard:    endsWildcard,
		corePattern:     corePattern,
		core:            getWordRegex(corePattern),
		prefixBase:      prefixBase,
		compatibility:   getWordRegex(compatibilityPattern),
		leadingBound:    leadingBound,
		trailingBound:   trailingBound,
		strictBoundary:  strictBoundary,
		caseInsensitive: caseInsensitive,
		smartForms:      smartForms,
		byteExact:       byteExact,
		maxMatchBytes:   maxMatchBytes,
		detectionBytes:  detectionBytes,
	}
}

// regexpInexact reports terms that need byte-exact matching because regexp
// decodes every invalid byte and literal U+FFFD as the same RuneError.
func regexpInexact(term string) bool {
	return !utf8.ValidString(term) || strings.Contains(term, "\uFFFD")
}

func wildcardPattern(term string, starWidth int, caseInsensitive bool) string {
	var pattern strings.Builder
	for i := 0; i < len(term); {
		switch term[i] {
		case '*':
			if i == 0 || i == len(term)-1 {
				i++
				continue
			}
			pattern.WriteString(`[a-zA-Z0-9_.-]{0,`)
			pattern.WriteString(decimalString(starWidth))
			pattern.WriteByte('}')
			pattern.WriteByte('?')
			i++
		case '?':
			pattern.WriteString(`[a-zA-Z0-9_.-]`)
			i++
		default:
			i = appendLiteralPattern(&pattern, term, i, caseInsensitive)
		}
	}
	return pattern.String()
}

func inflectionSuffix(caseInsensitive, smartForms bool) string {
	if smartForms {
		return `(?:[eE][sS]|[sS]|[eE][dD]|[iI][nN][gG]|[aA][lL]|[tT][iI][oO][nN]|[aA][tT][iI][oO][nN])?`
	}
	if !caseInsensitive {
		return ``
	}
	return `(?:[eE][sS]|[sS])?`
}

func literalPattern(term string, caseInsensitive bool) string {
	var pattern strings.Builder
	for i := 0; i < len(term); {
		i = appendLiteralPattern(&pattern, term, i, caseInsensitive)
	}
	return pattern.String()
}

func appendLiteralPattern(pattern *strings.Builder, term string, i int, caseInsensitive bool) int {
	r, size := utf8.DecodeRuneInString(term[i:])
	if r == utf8.RuneError && size == 1 {
		// Go's regexp engine exposes each invalid UTF-8 byte as RuneError.
		pattern.WriteString(`\x{FFFD}`)
		return i + 1
	}
	literal := regexp.QuoteMeta(term[i : i+size])
	if caseInsensitive {
		pattern.WriteString(`(?i:`)
		pattern.WriteString(literal)
		pattern.WriteByte(')')
	} else {
		pattern.WriteString(literal)
	}
	return i + size
}

func widestFoldedLiteralBytes(term string, caseInsensitive, wildcard bool) int {
	total := 0
	for i := 0; i < len(term); {
		if wildcard && (term[i] == '*' || term[i] == '?') {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(term[i:])
		if r == utf8.RuneError && size == 1 {
			total++
			i++
			continue
		}
		widest := size
		if caseInsensitive {
			for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
				if width := utf8.RuneLen(folded); width > widest {
					widest = width
				}
			}
		}
		total += widest
		i += size
	}
	return total
}

func decimalString(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' ||
		b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' ||
		b == '_'
}

func (m termMatcher) boundaryOK(text string, start, end int) bool {
	if m.leadingBound && start > 0 && isWordByte(text[start-1]) &&
		(m.strictBoundary || m.mode != PartialModePrefix || text[start-1] != '_') {
		return false
	}
	if m.trailingBound && end < len(text) && isWordByte(text[end]) {
		return false
	}
	return true
}

func (m termMatcher) find(text string, from int) (matchRange, bool) {
	if m.byteExact {
		return m.findByteExact(text, from)
	}
	if m.prefixBase != nil {
		return m.findPlainPrefix(text, from)
	}
	if from < 0 {
		from = 0
	}
	for from <= len(text) {
		loc := m.core.FindStringIndex(text[from:])
		if loc == nil {
			return matchRange{}, false
		}
		start, end := from+loc[0], from+loc[1]
		if m.boundaryOK(text, start, end) {
			return matchRange{start: start, end: end}, true
		}
		from = start + 1
	}
	return matchRange{}, false
}

func (m termMatcher) findPlainPrefix(text string, from int) (matchRange, bool) {
	if from < 0 {
		from = 0
	}
	for from <= len(text) {
		loc := m.prefixBase.FindStringIndex(text[from:])
		if loc == nil {
			return matchRange{}, false
		}
		start, end := from+loc[0], from+loc[1]
		if m.boundaryOK(text, start, end) {
			for end < len(text) && isWordByte(text[end]) {
				end++
			}
			return matchRange{start: start, end: end}, true
		}
		from = nextPlainPrefixCandidate(text, start)
	}
	return matchRange{}, false
}

func nextPlainPrefixCandidate(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		if text[i] == '_' {
			return i
		}
		if !isWordByte(text[i]) {
			return i
		}
	}
	return len(text) + 1
}

func (m termMatcher) findByteExact(text string, from int) (matchRange, bool) {
	boundaries := utf8ByteBoundaries(text)
	failed := make(map[wildcardByteState]struct{})
	return m.findByteExactWithBoundaries(text, from, boundaries, failed)
}

func (m termMatcher) findByteExactWithBoundaries(
	text string,
	from int,
	boundaries []bool,
	failed map[wildcardByteState]struct{},
) (matchRange, bool) {
	if from < 0 {
		from = 0
	}
	for start := from; start <= len(text); start++ {
		if !boundaries[start] {
			continue
		}
		if m.leadingBound && start > 0 && isWordByte(text[start-1]) &&
			(m.strictBoundary || m.mode != PartialModePrefix || text[start-1] != '_') {
			continue
		}

		end, ok := m.matchByteExactAt(text, start, boundaries, failed)
		if ok {
			return matchRange{start: start, end: end}, true
		}
	}
	return matchRange{}, false
}

func utf8ByteBoundaries(text string) []bool {
	boundaries := make([]bool, len(text)+1)
	for i := 0; i < len(text); {
		boundaries[i] = true
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	boundaries[len(text)] = true
	return boundaries
}

func (m termMatcher) matchByteExactAt(
	text string,
	start int,
	boundaries []bool,
	failed map[wildcardByteState]struct{},
) (int, bool) {
	if m.validWildcard {
		patternStart, patternEnd := 0, len(m.term)
		if m.term[0] == '*' {
			patternStart++
		}
		if m.term[len(m.term)-1] == '*' {
			patternEnd--
		}
		starWidth := 0
		if stars := strings.Count(m.term, "*"); stars > 0 {
			starWidth = maxWildcardExpansion / stars
		}
		return m.matchWildcardBytes(text, start, patternStart, patternEnd, starWidth, boundaries, failed)
	}

	end, ok := matchLiteralBytes(text, start, m.term, m.caseInsensitive)
	if !ok {
		return 0, false
	}
	switch m.mode {
	case PartialModePrefix:
		for end < len(text) && isWordByte(text[end]) {
			end++
		}
		return end, boundaries[end]
	case PartialModeContains:
		return end, boundaries[end]
	}

	forms := []string{""}
	if isWordByte(m.term[len(m.term)-1]) && (m.caseInsensitive || m.smartForms) {
		forms = []string{"es", "s", ""}
		if m.smartForms {
			forms = []string{"ation", "tion", "ing", "ed", "al", "es", "s", ""}
		}
	}
	for _, form := range forms {
		suffixInsensitive := m.caseInsensitive || m.smartForms
		candidate, matched := matchASCIIBytes(text, end, form, suffixInsensitive)
		if matched && boundaries[candidate] &&
			(!m.trailingBound || candidate == len(text) || !isWordByte(text[candidate])) {
			return candidate, true
		}
	}
	return 0, false
}

func matchASCIIBytes(text string, start int, literal string, caseInsensitive bool) (int, bool) {
	if len(text)-start < len(literal) {
		return 0, false
	}
	for i := 0; i < len(literal); i++ {
		got, want := text[start+i], literal[i]
		if caseInsensitive {
			if got >= 'A' && got <= 'Z' {
				got += 'a' - 'A'
			}
			if want >= 'A' && want <= 'Z' {
				want += 'a' - 'A'
			}
		}
		if got != want {
			return 0, false
		}
	}
	return start + len(literal), true
}

type wildcardByteState struct {
	textIndex    int
	patternIndex int
}

func (m termMatcher) matchWildcardBytes(
	text string,
	textIndex, patternIndex, patternEnd, starWidth int,
	boundaries []bool,
	failed map[wildcardByteState]struct{},
) (end int, ok bool) {
	if patternIndex == patternEnd {
		if !boundaries[textIndex] {
			return 0, false
		}
		if m.trailingBound && textIndex < len(text) && isWordByte(text[textIndex]) {
			return 0, false
		}
		return textIndex, true
	}

	switch m.term[patternIndex] {
	case '*':
		state := wildcardByteState{textIndex: textIndex, patternIndex: patternIndex}
		if _, seen := failed[state]; seen {
			return 0, false
		}
		end := textIndex
		for count := 0; count <= starWidth; count++ {
			if matchedEnd, matched := m.matchWildcardBytes(text, end, patternIndex+1, patternEnd, starWidth, boundaries, failed); matched {
				return matchedEnd, true
			}
			if end >= len(text) || !isWildcardByte(text[end]) {
				break
			}
			end++
		}
		failed[state] = struct{}{}
		return 0, false
	case '?':
		if textIndex >= len(text) || !isWildcardByte(text[textIndex]) {
			return 0, false
		}
		return m.matchWildcardBytes(text, textIndex+1, patternIndex+1, patternEnd, starWidth, boundaries, failed)
	default:
		nextText, nextPattern, matched := matchFoldedLiteralAt(text, textIndex, m.term, patternIndex, m.caseInsensitive)
		if !matched {
			return 0, false
		}
		return m.matchWildcardBytes(text, nextText, nextPattern, patternEnd, starWidth, boundaries, failed)
	}
}

func matchLiteralBytes(text string, start int, literal string, caseInsensitive bool) (int, bool) {
	textIndex := start
	for patternIndex := 0; patternIndex < len(literal); {
		nextText, nextPattern, ok := matchFoldedLiteralAt(text, textIndex, literal, patternIndex, caseInsensitive)
		if !ok {
			return 0, false
		}
		textIndex, patternIndex = nextText, nextPattern
	}
	return textIndex, true
}

func matchFoldedLiteralAt(text string, textIndex int, pattern string, patternIndex int, caseInsensitive bool) (int, int, bool) {
	if textIndex >= len(text) || patternIndex >= len(pattern) {
		return 0, 0, false
	}

	want, wantSize := utf8.DecodeRuneInString(pattern[patternIndex:])
	if want == utf8.RuneError && wantSize == 1 {
		if text[textIndex] != pattern[patternIndex] {
			return 0, 0, false
		}
		return textIndex + 1, patternIndex + 1, true
	}
	got, gotSize := utf8.DecodeRuneInString(text[textIndex:])
	if got == utf8.RuneError && gotSize == 1 {
		return 0, 0, false
	}
	if !runesEqualFold(got, want, caseInsensitive) {
		return 0, 0, false
	}
	return textIndex + gotSize, patternIndex + wantSize, true
}

func runesEqualFold(got, want rune, caseInsensitive bool) bool {
	if got == want {
		return true
	}
	if !caseInsensitive {
		return false
	}
	for folded := unicode.SimpleFold(want); folded != want; folded = unicode.SimpleFold(folded) {
		if got == folded {
			return true
		}
	}
	return false
}

func isWildcardByte(b byte) bool {
	return isWordByte(b) || b == '.' || b == '-'
}

func (m termMatcher) findAll(text string) []matchRange {
	var ranges []matchRange
	from := 0
	var boundaries []bool
	var failed map[wildcardByteState]struct{}
	if m.byteExact {
		boundaries = utf8ByteBoundaries(text)
		failed = make(map[wildcardByteState]struct{})
	}
	for from <= len(text) {
		var found matchRange
		var ok bool
		if m.byteExact {
			found, ok = m.findByteExactWithBoundaries(text, from, boundaries, failed)
		} else {
			found, ok = m.find(text, from)
		}
		if !ok {
			break
		}
		ranges = append(ranges, found)
		if found.end > found.start {
			from = found.end
		} else {
			from = found.start + 1
		}
	}
	return ranges
}

func (m termMatcher) matches(text string) bool {
	_, ok := m.find(text, 0)
	return ok
}

// compatibilityRegex adapts exact matcher semantics to legacy regexp callers.
// Boundary bytes are outside capture group 1, which remains the exact match.
func (m termMatcher) compatibilityRegex() *regexp.Regexp {
	return m.compatibility
}

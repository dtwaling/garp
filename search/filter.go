package search

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/richardlehane/mscfb"

	"garp/config"
)

// Regex cache for word matching - prevents MustCompile on every call
var (
	wordRegexCache   = make(map[string]*regexp.Regexp)
	wordRegexCacheMu sync.RWMutex
	wordCacheMaxSize = 256
	// Note: whitespaceRegex is defined in cleaner.go (same package)
)

// PartialMode controls how search terms expand beyond legacy whole-word matching.
type PartialMode string

const (
	PartialModeOff      PartialMode = ""
	PartialModePrefix   PartialMode = "prefix"
	PartialModeContains PartialMode = "contains"
)

// getWordRegex returns a cached compiled regex or compiles and caches it.
func getWordRegex(pattern string) *regexp.Regexp {
	wordRegexCacheMu.RLock()
	re, ok := wordRegexCache[pattern]
	wordRegexCacheMu.RUnlock()

	if ok {
		return re
	}

	wordRegexCacheMu.Lock()
	defer wordRegexCacheMu.Unlock()

	// Double-check after acquiring write lock
	if re, ok := wordRegexCache[pattern]; ok {
		return re
	}

	re = regexp.MustCompile(pattern)

	// Evict if at capacity (simple clear)
	if len(wordRegexCache) >= wordCacheMaxSize {
		wordRegexCache = make(map[string]*regexp.Regexp)
	}

	wordRegexCache[pattern] = re
	return re
}

// Memory pressure pacing helpers
var memSampleMu sync.Mutex
var lastMemAvailKB int64
var lastMemSample time.Time

// memAvailableKB returns a cached MemAvailable (kB). It re-samples at most every ~300ms.
func memAvailableKB() int64 {
	memSampleMu.Lock()
	defer memSampleMu.Unlock()

	if time.Since(lastMemSample) < 300*time.Millisecond && lastMemAvailKB > 0 {
		return lastMemAvailKB
	}

	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		// If we can't read, assume plenty to avoid unnecessary sleeping
		lastMemAvailKB = 1 << 60
		lastMemSample = time.Now()
		return lastMemAvailKB
	}

	var avail int64
	lines := strings.Split(string(b), "\n")
	for _, ln := range lines {
		if strings.HasPrefix(ln, "MemAvailable:") {
			fields := strings.Fields(ln)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					avail = kb
				}
			}
			break
		}
	}
	if avail == 0 {
		for _, ln := range lines {
			if strings.HasPrefix(ln, "MemFree:") {
				fields := strings.Fields(ln)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						avail = kb
					}
				}
				break
			}
		}
	}
	if avail <= 0 {
		avail = 1 << 60
	}

	lastMemAvailKB = avail
	lastMemSample = time.Now()
	return lastMemAvailKB
}

// maybePaceForMemory sleeps briefly under low MemAvailable to reduce I/O thrash.
func maybePaceForMemory() {
	kb := memAvailableKB()
	switch {
	case kb < 128*1024: // < 128 MiB
		time.Sleep(20 * time.Millisecond)
	case kb < 256*1024: // < 256 MiB
		time.Sleep(5 * time.Millisecond)
	default:
		// no-op
	}
}

func buildTermRegexLower(word string, mode PartialMode) *regexp.Regexp {
	base := strings.ToLower(strings.TrimSpace(word))
	if base == "" {
		// never matches; safe fallback
		return getWordRegex(`a\A`)
	}
	return compileTermMatcher(base, mode, true).compatibilityRegex()
}

func buildTermRegexCI(word string, mode PartialMode) *regexp.Regexp {
	base := strings.TrimSpace(word)
	if base == "" {
		// never matches; safe fallback
		return getWordRegex(`a\A`)
	}
	return compileTermMatcher(base, mode, true).compatibilityRegex()
}

// FindTermIndexCI returns the exact matched byte range without delimiters.
func FindTermIndexCI(text, term string, mode PartialMode) []int {
	found, ok := compileTermMatcher(term, mode, true).find(text, 0)
	if ok {
		return []int{found.start, found.end}
	}
	return nil
}

// FindTermIndex returns the exact matched byte range for explicit query options.
func FindTermIndex(text, term string, mode PartialMode, options QueryOptions) []int {
	found, ok := compileTermMatcherWithOptions(term, mode, options).find(text, 0)
	if ok {
		return []int{found.start, found.end}
	}
	return nil
}

func buildTermRegex(base string, mode PartialMode, caseInsensitive bool) *regexp.Regexp {
	return compileTermMatcher(base, mode, caseInsensitive).compatibilityRegex()
}

// buildWordRegexLower preserves the legacy whole-word matcher API.
func buildWordRegexLower(word string) *regexp.Regexp {
	return buildTermRegexLower(word, PartialModeOff)
}

// buildWordRegexCI preserves the legacy case-insensitive whole-word matcher API.
func buildWordRegexCI(word string) *regexp.Regexp {
	return buildTermRegexCI(word, PartialModeOff)
}

// CheckTextContainsAllWords preserves the legacy strict-conjunction behavior.
func CheckTextContainsAllWords(text string, words []string, distance int) bool {
	return CheckTextContainsAllWordsPartial(text, words, distance, PartialModeOff)
}

// CheckTextContainsAllWordsPartial checks strict proximity conjunction using
// the requested partial-match mode.
func CheckTextContainsAllWordsPartial(text string, words []string, distance int, partial PartialMode) bool {
	_, _, _, _, ok := CheckTextContainsRankedWordsPartial(text, words, distance, true, partial)
	return ok
}

// CheckTextContainsRankedWords finds the highest-scoring proximity cluster in
// text. A score is the sum of the lengths of distinct query terms in the
// cluster. For three or more terms, non-strict mode requires the first term
// plus at least one secondary term; strict mode requires every term.
func CheckTextContainsRankedWords(text string, words []string, distance int, strict bool) (score int, termCount int, matchedTerms []string, spanLen int, ok bool) {
	return CheckTextContainsRankedWordsPartial(text, words, distance, strict, PartialModeOff)
}

// CheckTextContainsRankedWordsPartial finds the highest-scoring proximity
// cluster using the requested partial-match mode.
func CheckTextContainsRankedWordsPartial(text string, words []string, distance int, strict bool, partial PartialMode) (score int, termCount int, matchedTerms []string, spanLen int, ok bool) {
	if len(words) == 0 {
		return 0, 0, nil, 0, true
	}
	analysis, ok := analyzeRankedText(text, compileQuery(words, partial), distance, strict)
	if !ok {
		return 0, 0, nil, 0, false
	}
	return analysis.Score, analysis.TermCount, analysis.MatchedTerms, analysis.SpanLength, true
}

// CheckTextContainsExcludeWords checks if extracted text contains any exclude words
func CheckTextContainsExcludeWords(text string, excludeWords []string) bool {
	return CheckTextContainsExcludeWordsWithOptions(text, excludeWords, QueryOptions{})
}

// CheckTextContainsExcludeWordsWithOptions applies the active query case mode.
func CheckTextContainsExcludeWordsWithOptions(text string, excludeWords []string, options QueryOptions) bool {
	if len(excludeWords) == 0 {
		return false
	}
	for _, word := range excludeWords {
		if containsLiteralExcludeWord(text, word, options.CaseSensitive) {
			return true
		}
	}

	return false
}

func containsLiteralExcludeWord(text, word string, caseSensitive bool) bool {
	word = strings.TrimSpace(word)
	if word == "" {
		return false
	}
	literal := regexp.QuoteMeta(word)
	if !caseSensitive && isWordByte(word[len(word)-1]) {
		literal += `(?:es|s)?`
	}
	if !caseSensitive {
		literal = `(?i:` + literal + `)`
	}
	pattern := `\b(?:` + literal + `)\b`
	return getWordRegex(pattern).MatchString(text)
}

// FileInfo represents information about a file
type FileInfo struct {
	Path string
	Size int64
}

func splitPathScopeParts(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func pathScopePartsMatch(patternParts, relParts []string) bool {
	// reachable[j] means the pattern prefix matches the first j path parts.
	// Visit each (pattern, path) state once: O(N*M) segment transitions and
	// O(M) space, in addition to the cost of individual path.Match calls.
	reachable := make([]bool, len(relParts)+1)
	reachable[0] = true
	for i, part := range patternParts {
		if part == "**" {
			// Normalize consecutive globstars as we consume the pattern. The
			// shared splitter also handles literal paths, which must not collapse.
			if i > 0 && patternParts[i-1] == "**" {
				continue
			}
			for j := 1; j < len(reachable); j++ {
				// Either consume no part, or extend this globstar by one part.
				reachable[j] = reachable[j] || reachable[j-1]
			}
			continue
		}

		// Descend so the preceding pattern row is not overwritten before use.
		anyReachable := false
		for j := len(relParts); j > 0; j-- {
			reachable[j] = false
			if reachable[j-1] {
				matched, err := path.Match(part, relParts[j-1])
				reachable[j] = err == nil && matched
				anyReachable = anyReachable || reachable[j]
			}
		}
		reachable[0] = false
		if !anyReachable {
			return false
		}
	}
	return reachable[len(relParts)]
}

func pathScopeLiteralPrefix(pattern string) string {
	parts := splitPathScopeParts(pattern)
	literals := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, "*?") {
			break
		}
		literals = append(literals, part)
	}
	return strings.Join(literals, "/")
}

func scopePatternMatchesDirPrefix(matchPattern, matchRel string) bool {
	if !strings.ContainsAny(matchPattern, "*?") {
		prefix := strings.TrimRight(matchPattern, "/")
		return prefix != "" && (matchRel == prefix || strings.HasPrefix(matchRel, prefix+"/"))
	}

	if strings.HasSuffix(matchPattern, "/*") {
		prefix := strings.TrimSuffix(matchPattern, "/*")
		if prefix != "" && !strings.ContainsAny(prefix, "*?") {
			return matchRel == prefix || strings.HasPrefix(matchRel, prefix+"/")
		}
	}
	return false
}

// fileTypeMatcher decides whether a walked path is in scope, given the "-g" glob list the
// caller built (via config.BuildRipgrepFileTypes / OnlyTypeGlobs). Extension globs ("*.go")
// match on the lowercased extension for O(1) lookup; name globs ("Dockerfile", "Dockerfile.*")
// match case-insensitively against the base name. With no globs at all, everything is in scope.
type fileTypeMatcher struct {
	exts      map[string]bool
	nameGlobs []string
}

func newFileTypeMatcher(fileTypes []string) *fileTypeMatcher {
	m := &fileTypeMatcher{exts: make(map[string]bool)}
	for i := 0; i < len(fileTypes); i++ {
		if fileTypes[i] == "-g" && i+1 < len(fileTypes) {
			i++
			glob := fileTypes[i]
			if strings.HasPrefix(glob, "*.") {
				m.exts[strings.ToLower(glob[1:])] = true // ".go"
			} else {
				m.nameGlobs = append(m.nameGlobs, strings.ToLower(glob))
			}
		}
	}
	return m
}

// allows reports whether path passes the type filter.
func (m *fileTypeMatcher) allows(path string) bool {
	if len(m.exts) == 0 && len(m.nameGlobs) == 0 {
		return true // no restriction
	}
	if m.exts[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	if len(m.nameGlobs) > 0 {
		// filepath.Base only recognizes the native separator. Normalize Windows
		// paths as well because searches can receive paths from either platform.
		base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, "\\", "/")))
		for _, g := range m.nameGlobs {
			if ok, err := filepath.Match(g, base); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// GetDocumentFileCount returns the count of document files that will be searched (pure Go).
// walkRoot specifies the directory to search from; use "" or "." for the current directory.
// pathScope, if non-empty, restricts results to files whose relative path matches at least
// one simple glob pattern (e.g., "*/backend/*", "tests/*").
func GetDocumentFileCount(fileTypes []string, walkRoot string, pathScope []string) (int, error) {
	return GetDocumentFileCountWithOptions(fileTypes, walkRoot, pathScope, WalkOptions{})
}

// GetDocumentFileCountWithOptions counts candidates using the shared walk policy.
func GetDocumentFileCountWithOptions(fileTypes []string, walkRoot string, pathScope []string, options WalkOptions) (int, error) {
	count := 0
	err := walkCandidates(fileTypes, walkRoot, pathScope, options, func(_ string, _ os.FileInfo) error {
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetDocumentFileCountForRoots counts the canonical union of candidates from
// prepared roots using the same traversal policy as discovery.
func GetDocumentFileCountForRoots(fileTypes []string, roots []StartRoot, pathScope []string, options WalkOptions) (int, error) {
	return getDocumentFileCountForRoots(fileTypes, roots, pathScope, options, nil)
}

func getDocumentFileCountForRoots(fileTypes []string, roots []StartRoot, pathScope []string, options WalkOptions, onKeyTracking func()) (int, error) {
	overlap := preparedPhysicalRootsOverlap(roots)
	count := 0
	var seen map[string]struct{}
	if overlap {
		seen = make(map[string]struct{})
		if onKeyTracking != nil {
			onKeyTracking()
		}
	}
	for _, root := range roots {
		err := walkPreparedCandidates(fileTypes, root, pathScope, options, func(path string, _ os.FileInfo) error {
			if overlap {
				seen[canonicalCandidateKey(root, path)] = struct{}{}
			} else {
				count++
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	if !overlap {
		return count, nil
	}
	return len(seen), nil
}

// FindFilesWithFirstWord finds all files containing the first search word (pure Go).
// walkRoot specifies the directory to search from; use "" or "." for the current directory.
// pathScope, if non-empty, restricts results to files whose relative path matches at least
// one simple glob pattern.
func FindFilesWithFirstWord(word string, fileTypes []string, walkRoot string, pathScope []string) ([]string, error) {
	return FindFilesWithFirstWordPartial(word, fileTypes, walkRoot, pathScope, PartialModeOff)
}

// FindFilesWithFirstWordPartial finds candidates using the requested Stage 1 match mode.
func FindFilesWithFirstWordPartial(word string, fileTypes []string, walkRoot string, pathScope []string, partial PartialMode) ([]string, error) {
	return FindFilesWithFirstWordPartialWithOptions(word, fileTypes, walkRoot, pathScope, partial, WalkOptions{})
}

// FindFilesWithFirstWordPartialWithOptions finds candidates using the shared walk policy.
func FindFilesWithFirstWordPartialWithOptions(word string, fileTypes []string, walkRoot string, pathScope []string, partial PartialMode, options WalkOptions) ([]string, error) {
	return findFilesWithFirstWordMatcherWithOptions(
		compileTermMatcher(word, partial, true),
		fileTypes,
		walkRoot,
		pathScope,
		options,
	)
}

func findFilesWithFirstWordMatcherWithOptions(matcher termMatcher, fileTypes []string, walkRoot string, pathScope []string, options WalkOptions) ([]string, error) {
	admission, provable := prefilterAdmissionQuery(compiledQuery{
		terms: []string{matcher.term}, matchers: []termMatcher{matcher}, maxDetectionBytes: matcher.detectionBytes,
	})
	matches := make([]string, 0, 128)
	err := walkCandidates(fileTypes, walkRoot, pathScope, options, func(path string, _ os.FileInfo) error {
		if IsBinaryFormat(path) {
			// include heavy binary types as candidates; full check later
			matches = append(matches, path)
			return nil
		}

		if discoveryAdmitsTextFile(path, admission, provable, options.TextMaxBytes()) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches, nil
}

// discoveryAdmitsTextFile applies the cleaned-text admission proof to Stage 1.
// Its query contains only the primary term's necessary matchers.
func discoveryAdmitsTextFile(path string, admission compiledQuery, provable bool, maxBytes int64) bool {
	f, st, err := openRegularSearchFile(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if !provable {
		return true
	}
	_ = adviseSequential(f)
	defer adviseDontNeed(f)
	r := io.Reader(io.LimitReader(f, maxBytes))
	if !config.IsCodeFile(path) {
		r = &cleanerSpacingReader{r: r}
	}
	scan := discoveryScanner{chunk: int(min(st.Size()+1, discoveryChunkSize))}
	return scan.admits(r, admission)
}

const discoveryChunkSize = 64 * 1024

// discoveryScanner reuses one file-sized (at most chunk-sized) window.
type discoveryScanner struct {
	chunk  int
	window []byte
}

// admits reports whether every matcher of q hits r, or r fails to read.
func (s *discoveryScanner) admits(r io.Reader, q compiledQuery) bool {
	hit := make([]bool, len(q.matchers))
	remaining := len(hit)
	observe := func(data []byte, from int) bool {
		for i, matcher := range q.matchers {
			if !hit[i] && discoveryMatchIndex(data, matcher, from) >= 0 {
				hit[i] = true
				remaining--
			}
		}
		return remaining == 0
	}
	overlap := streamQueryOverlap(q)
	if len(s.window) < overlap+s.chunk {
		s.window = make([]byte, overlap+s.chunk)
	}
	kept := 0
	var total int64
	for {
		n, readErr := r.Read(s.window[kept:])
		if n > 0 {
			data := s.window[:kept+n]
			if observe(data, discoveryChunkMatchStart(total, kept)) {
				return true
			}
			kept = copy(s.window, data[max(0, len(data)-overlap):])
			total += int64(n)
		}
		if readErr == io.EOF {
			return false
		}
		if readErr != nil {
			return true
		}
	}
}

func discoveryMatcherOverlap(matcher termMatcher) int {
	overlap := matcher.detectionBytes + 1
	if overlap < 32 {
		return 32
	}
	return overlap
}

func discoveryChunkMatchStart(bytesRead int64, retained int) int {
	if retained > 0 && bytesRead > int64(retained) {
		return 1
	}
	return 0
}

func discoveryMatchIndex(buf []byte, matcher termMatcher, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(buf) {
		return -1
	}
	if canUseASCIIPlainTermFastPath(buf, matcher) {
		return asciiPlainTermIndexCI(buf, matcher, from)
	}

	found, ok := matcher.find(string(buf), from)
	if !ok {
		return -1
	}
	return found.start
}

func canUseASCIIPlainTermFastPath(buf []byte, matcher termMatcher) bool {
	return !matcher.validWildcard &&
		!strings.ContainsAny(matcher.term, "*?") &&
		isASCIIString(matcher.term) &&
		(!matcher.caseInsensitive || !containsUnicodeASCIIFoldVariant(buf))
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func containsUnicodeASCIIFoldVariant(buf []byte) bool {
	for i := 0; i < len(buf); i++ {
		switch buf[i] {
		case 0xc5:
			if i+1 < len(buf) && buf[i+1] == 0xbf { // U+017F, long s
				return true
			}
		case 0xe2:
			if i+2 < len(buf) && buf[i+1] == 0x84 && buf[i+2] == 0xaa { // U+212A, Kelvin sign
				return true
			}
		}
	}
	return false
}

func asciiPlainTermIndexCI(buf []byte, matcher termMatcher, from int) int {
	term := matcher.term
	if term == "" || len(buf)-from < len(term) {
		return -1
	}

	limit := len(buf) - len(term)
	first, firstFolded := term[0], term[0]
	if matcher.caseInsensitive {
		first = asciiLower(first)
		firstFolded = first
		if first >= 'a' && first <= 'z' {
			firstFolded = first ^ 0x20
		}
	}
	for start := from; start <= limit; start++ {
		if b := buf[start]; b != first && b != firstFolded {
			continue
		}
		if matcher.leadingBound && start > 0 && isWordByte(buf[start-1]) &&
			(matcher.strictBoundary || matcher.mode != PartialModePrefix || buf[start-1] != '_') {
			continue
		}
		if !asciiLiteralEqualFold(buf[start:start+len(term)], term, matcher.caseInsensitive) {
			continue
		}

		end := start + len(term)
		if matcher.mode == PartialModeContains || matcher.mode == PartialModePrefix {
			return start
		}
		if asciiPlainTermHasAcceptedEnd(buf, end, matcher) {
			return start
		}
	}
	return -1
}

func asciiLiteralEqualFold(got []byte, want string, caseInsensitive bool) bool {
	for i, b := range got {
		expected := want[i]
		if caseInsensitive {
			b = asciiLower(b)
			expected = asciiLower(expected)
		}
		if b != expected {
			return false
		}
	}
	return true
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func asciiPlainTermHasAcceptedEnd(buf []byte, end int, matcher termMatcher) bool {
	if !isWordByte(matcher.term[len(matcher.term)-1]) {
		return end == len(buf) || !isWordByte(buf[end])
	}
	if end == len(buf) || !isWordByte(buf[end]) {
		return true
	}

	if !matcher.caseInsensitive && !matcher.smartForms {
		return false
	}
	suffixInsensitive := matcher.caseInsensitive || matcher.smartForms
	if asciiSuffixEndsAtBoundary(buf, end, "es", suffixInsensitive) ||
		asciiSuffixEndsAtBoundary(buf, end, "s", suffixInsensitive) {
		return true
	}
	if !matcher.smartForms {
		return false
	}
	for _, suffix := range [...]string{"ed", "ing", "al", "tion", "ation"} {
		if asciiSuffixEndsAtBoundary(buf, end, suffix, suffixInsensitive) {
			return true
		}
	}
	return false
}

func asciiSuffixEndsAtBoundary(buf []byte, start int, suffix string, caseInsensitive bool) bool {
	end := start + len(suffix)
	return end <= len(buf) &&
		asciiLiteralEqualFold(buf[start:end], suffix, caseInsensitive) &&
		(end == len(buf) || !isWordByte(buf[end]))
}

// FindFilesWithFirstWordProgress is like FindFilesWithFirstWord but emits per-file discovery progress.
// walkRoot specifies the directory to search from; use "" or "." for the current directory.
// pathScope, if non-empty, restricts results to files whose relative path matches at least
// one simple glob pattern.
func FindFilesWithFirstWordProgress(words []string, fileTypes []string, workers int, onProgress func(processed, total int, path string), walkRoot string, pathScope []string) ([]string, error) {
	return FindFilesWithFirstWordProgressPartial(words, fileTypes, workers, onProgress, walkRoot, pathScope, PartialModeOff)
}

// FindFilesWithFirstWordProgressPartial emits discovery progress using partial matching.
func FindFilesWithFirstWordProgressPartial(words []string, fileTypes []string, workers int, onProgress func(processed, total int, path string), walkRoot string, pathScope []string, partial PartialMode) ([]string, error) {
	return FindFilesWithFirstWordProgressPartialWithOptions(words, fileTypes, workers, onProgress, walkRoot, pathScope, partial, WalkOptions{})
}

// FindFilesWithFirstWordProgressPartialWithOptions emits discovery progress
// using the shared walk policy.
func FindFilesWithFirstWordProgressPartialWithOptions(words []string, fileTypes []string, workers int, onProgress func(processed, total int, path string), walkRoot string, pathScope []string, partial PartialMode, options WalkOptions) ([]string, error) {
	return findFilesWithFirstWordProgressQueryWithOptions(
		compileQuery(words, partial),
		fileTypes,
		workers,
		onProgress,
		walkRoot,
		pathScope,
		partial,
		options,
	)
}

func findFilesWithFirstWordProgressQueryWithOptions(query compiledQuery, fileTypes []string, workers int, onProgress func(processed, total int, path string), walkRoot string, pathScope []string, partial PartialMode, options WalkOptions) ([]string, error) {
	raw := []string{walkRoot}
	if walkRoot == "" {
		raw = nil
	}
	roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{
		NoIgnore:  options.NoIgnore,
		PathScope: pathScope,
		UserGlobs: options.UserGlobs,
	})
	if err != nil {
		return nil, err
	}
	if len(roots) != 1 {
		return nil, fmt.Errorf("single-root discovery received %d effective roots", len(roots))
	}
	return findFilesWithFirstWordProgressQueryWithRoot(query, fileTypes, workers, onProgress, roots[0], pathScope, options)
}

func findFilesWithFirstWordProgressQueryWithRoot(query compiledQuery, fileTypes []string, workers int, onProgress func(processed, total int, path string), root StartRoot, pathScope []string, options WalkOptions) ([]string, error) {
	// Emit initial progress with unknown total
	if onProgress != nil {
		onProgress(0, 0, "")
	}
	if len(query.matchers) == 0 {
		return nil, nil
	}

	primaryMatcher := query.matchers[0]
	primaryQuery := compiledQuery{
		terms:             query.terms[:1],
		matchers:          query.matchers[:1],
		maxMatchBytes:     primaryMatcher.maxMatchBytes,
		maxDetectionBytes: primaryMatcher.detectionBytes,
	}
	admission, provable := prefilterAdmissionQuery(primaryQuery)

	// Results and synchronization
	matches := make([]string, 0, 128)
	var mu sync.Mutex

	// Bounded worker pool
	if workers <= 0 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	} else if workers > 16 {
		workers = 16
	}
	paths := make(chan string, 1024)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			maxBytes := options.TextMaxBytes()

			for p := range paths {
				maybePaceForMemory()
				if discoveryAdmitsTextFile(p, admission, provable, maxBytes) {
					mu.Lock()
					matches = append(matches, p)
					mu.Unlock()
				}
			}
		}()
	}

	processed := 0

	// Walk and stream paths to workers
	walkErr := walkPreparedCandidates(fileTypes, root, pathScope, options, func(path string, _ os.FileInfo) error {
		ext := strings.ToLower(filepath.Ext(path))

		processed++
		if onProgress != nil {
			onProgress(processed, 0, path)
		}

		// Heavy files: conservative prefilter for non-PDF; include unless decisively absent
		if IsBinaryFormat(path) {
			if ext == ".pdf" || ext == ".msg" {
				// These containers require extraction; raw scans cannot skip it.
				mu.Lock()
				matches = append(matches, path)
				mu.Unlock()
				return nil
			}
			// For non-PDF heavy types, run a small capped streaming prefilter for the first term.
			// Only skip when conclusively absent; undecided or found => include.
			capBytes := capForBinaryPrefilter(ext)
			found, decided := binaryStreamingPrefilterDecidedQuery(path, primaryQuery, capBytes, true)
			if decided && !found {
				return nil // safe to skip
			}
			mu.Lock()
			matches = append(matches, path)
			mu.Unlock()
			return nil
		}

		// Enqueue for worker scanning
		paths <- path
		return nil
	})

	// Close path feed and wait for workers
	close(paths)
	wg.Wait()

	if walkErr != nil {
		return nil, walkErr
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.Strings(matches)
	return matches, nil
}

// StreamContainsAllWordsDecided streams a file and returns whether all words are present.
func StreamContainsAllWordsDecided(filePath string, words []string) (found bool, decided bool) {
	return StreamContainsAllWordsDecidedPartial(filePath, words, PartialModeOff)
}

// StreamContainsAllWordsDecidedPartial streams a file and checks each term
// using the requested partial-match mode.
func StreamContainsAllWordsDecidedPartial(filePath string, words []string, partial PartialMode) (found bool, decided bool) {
	return streamContainsAllWordsDecidedQuery(filePath, compileQuery(words, partial), defaultTextMaxBytes)
}

// streamContainsAllWordsDecidedPartial treats maxBytes as the complete
// searchable text prefix. Reaching the limit is therefore a conclusive miss.
func streamContainsAllWordsDecidedPartial(filePath string, words []string, partial PartialMode, maxBytes int64) (found bool, decided bool) {
	return streamContainsAllWordsDecidedQuery(filePath, compileQuery(words, partial), maxBytes)
}

func streamContainsAllWordsDecidedQuery(filePath string, query compiledQuery, maxBytes int64) (found bool, decided bool) {
	return streamContainsRankedWordsDecidedQuery(filePath, query, true, maxBytes)
}

func StreamContainsAllWords(filePath string, words []string) bool {
	found, _ := StreamContainsAllWordsDecided(filePath, words)
	return found
}

// StreamContainsRankedWordsDecided streams a text file and determines whether
// it can satisfy ranked matching before the distance check. Relaxed matching
// requires the primary term plus at least one secondary term; strict matching
// requires every query term.
func StreamContainsRankedWordsDecided(filePath string, words []string, strict bool) (found bool, decided bool) {
	return StreamContainsRankedWordsDecidedPartial(filePath, words, strict, PartialModeOff)
}

// StreamContainsRankedWordsDecidedPartial streams a text file using the
// requested partial-match mode before the distance check.
func StreamContainsRankedWordsDecidedPartial(filePath string, words []string, strict bool, partial PartialMode) (found bool, decided bool) {
	return streamContainsRankedWordsDecidedQuery(filePath, compileQuery(words, partial), strict, defaultTextMaxBytes)
}

// streamContainsRankedWordsDecidedPartial treats maxBytes as the complete
// searchable text prefix. Reaching the limit is therefore a conclusive miss.
func streamContainsRankedWordsDecidedPartial(filePath string, words []string, strict bool, partial PartialMode, maxBytes int64) (found bool, decided bool) {
	return streamContainsRankedWordsDecidedQuery(filePath, compileQuery(words, partial), strict, maxBytes)
}

func streamContainsRankedWordsDecidedQuery(filePath string, query compiledQuery, strict bool, maxBytes int64) (found bool, decided bool) {
	if len(query.matchers) == 0 {
		return true, true
	}
	if maxBytes <= 0 {
		maxBytes = defaultTextMaxBytes
	}
	f, _, err := openRegularSearchFile(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()

	found, complete, readErr := streamMatchesCleanedQuery(f, query, strict, maxBytes, config.IsCodeFile(filePath))
	_ = adviseDontNeed(f)
	return decideStreamQueryResult(found, complete, readErr, true)
}

// StreamContainsAllWordsDecidedWithCap streams a file and returns whether all words are present.
// - found = true, decided = true: conclusively found all words
// - found = false, decided = true: conclusively not all words present
// - found = false, decided = false: budget reached; prefilter is undecided (do not skip)
func StreamContainsAllWordsDecidedWithCap(filePath string, words []string, capBytes int64) (bool, bool) {
	return StreamContainsAllWordsDecidedWithCapPartial(filePath, words, capBytes, PartialModeOff)
}

// StreamContainsAllWordsDecidedWithCapPartial is the capped streaming
// prefilter using the requested partial-match mode.
func StreamContainsAllWordsDecidedWithCapPartial(filePath string, words []string, capBytes int64, partial PartialMode) (bool, bool) {
	return streamContainsQueryDecidedWithCap(filePath, compileQuery(words, partial), true, capBytes)
}

func streamContainsQueryDecidedWithCap(filePath string, query compiledQuery, strict bool, capBytes int64) (bool, bool) {
	if len(query.matchers) == 0 {
		return true, true
	}
	f, stat, err := openRegularSearchFile(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()

	// Align with GetFileContent limits, then apply optional capBytes
	maxBytes := int64(maxFileSizeBytes)
	completeAtLimit := false
	if stat.Size() <= maxBytes {
		maxBytes = stat.Size()
		completeAtLimit = true
	}
	if capBytes > 0 && capBytes < maxBytes {
		maxBytes = capBytes
		completeAtLimit = false
	}

	found, complete, readErr := streamMatchesCleanedQuery(f, query, strict, maxBytes, config.IsCodeFile(filePath))
	_ = adviseDontNeed(f)
	return decideStreamQueryResult(found, complete, readErr, completeAtLimit)
}

func decideStreamQueryResult(found, complete bool, readErr error, completeAtLimit bool) (bool, bool) {
	if found {
		return true, true
	}
	if readErr != nil {
		return false, false
	}
	if complete || completeAtLimit {
		return false, true
	}
	return false, false
}

func readerValidatedEOF(r io.Reader) bool {
	var probe [1]byte
	n, err := r.Read(probe[:])
	return n == 0 && err == io.EOF
}

type streamQueryMatches struct {
	found  []bool
	owners []int
	terms  int
}

func (m *streamQueryMatches) observe(text string, query compiledQuery, from int) {
	for i, matcher := range query.matchers {
		if !m.found[i] {
			if _, ok := matcher.find(text, from); ok {
				m.found[i] = true
			}
		}
	}
}

func (m *streamQueryMatches) satisfied(strict bool) bool {
	if m.owners != nil {
		grouped := streamQueryMatches{found: make([]bool, m.terms)}
		for i := range grouped.found {
			grouped.found[i] = true
		}
		for i, owner := range m.owners {
			grouped.found[owner] = grouped.found[owner] && m.found[i]
		}
		return grouped.satisfied(strict)
	}
	if len(m.found) == 0 {
		return true
	}
	if strict || len(m.found) <= 2 {
		for _, found := range m.found {
			if !found {
				return false
			}
		}
		return true
	}
	if !m.found[0] {
		return false
	}
	for _, found := range m.found[1:] {
		if found {
			return true
		}
	}
	return false
}

func streamQueryOverlap(query compiledQuery) int {
	overlap := query.maxDetectionBytes + 1
	if overlap < 32 {
		return 32
	}
	return overlap
}

// streamMatchesQuery returns complete only when the reader reaches EOF.
func streamMatchesQuery(r io.Reader, query compiledQuery, strict bool, maxBytes int64) (found bool, complete bool, err error) {
	if len(query.matchers) == 0 {
		return true, true, nil
	}
	state := streamQueryMatches{found: make([]bool, len(query.matchers)), owners: query.owners, terms: len(query.terms)}
	const chunkSize = 64 * 1024
	overlap := streamQueryOverlap(query)
	window := make([]byte, overlap+chunkSize)
	kept := 0
	var total int64
	for {
		maybePaceForMemory()
		if total >= maxBytes {
			return false, false, nil
		}
		toRead := chunkSize
		if rem := maxBytes - total; rem < int64(toRead) {
			toRead = int(rem)
		}
		n, readErr := r.Read(window[kept : kept+toRead])
		if n > 0 {
			data := window[:kept+n]
			from := discoveryChunkMatchStart(total, kept)
			state.observe(string(data), query, from)
			if state.satisfied(strict) {
				return true, true, nil
			}
			kept = copy(window, data[max(0, len(data)-overlap):])
			total += int64(n)
		}
		if readErr == io.EOF {
			return false, true, nil
		}
		if readErr != nil {
			return false, false, readErr
		}
	}
}

// prefilterAdmissionQuery requires every usable phrase piece to occur, without
// requiring adjacency in raw markup. Unusable pieces cannot prove absence.
func prefilterAdmissionQuery(q compiledQuery) (compiledQuery, bool) {
	hasPhrase := false
	for _, term := range q.terms {
		hasPhrase = hasPhrase || strings.Contains(term, " ")
	}
	if !hasPhrase {
		return q, true
	}
	aq := compiledQuery{terms: q.terms, owners: make([]int, 0, len(q.matchers))}
	for i, p := range q.matchers {
		start := len(aq.matchers)
		if !strings.Contains(p.term, " ") {
			aq.matchers = append(aq.matchers, p)
		} else {
			for _, piece := range strings.Split(p.term, " ") {
				// An invalid wildcard parent matches literal '*' and '?'.
				// Recompiling such pieces as wildcards can lose real matches.
				if piece == "" || !p.validWildcard && strings.ContainsAny(piece, "*?") {
					continue
				}
				m := compileTermMatcherWithOptions(piece, PartialModeContains, QueryOptions{CaseSensitive: !p.caseInsensitive})
				if m.mode == PartialModeContains {
					aq.matchers = append(aq.matchers, m)
				}
			}
		}
		if len(aq.matchers) == start {
			return compiledQuery{}, false
		}
		for _, m := range aq.matchers[start:] {
			aq.owners = append(aq.owners, i)
			aq.maxDetectionBytes = max(aq.maxDetectionBytes, m.detectionBytes)
		}
	}
	return aq, true
}

var errUnprovableMiss = errors.New("prefilter miss cannot prove a cleaned-text miss")

// streamMatchesCleanedQuery permits a conclusive miss only when the bounded
// stream cannot satisfy prefilterAdmissionQuery after cleanerSpacingReader
// (non-code). Cleaning never joins raw tokens: spaceless matches survive the
// spacing replay, and phrase matches imply every usable piece is present.
// Thus an authoritative cleaned-text match cannot be a decided prefilter miss.
// Stage 1 admits a text file unless that same proof succeeds.
func streamMatchesCleanedQuery(r io.Reader, query compiledQuery, strict bool, maxBytes int64, isCode bool) (found, complete bool, err error) {
	aq, ok := prefilterAdmissionQuery(query)
	if !ok {
		return false, false, errUnprovableMiss
	}
	if isCode {
		return streamMatchesQuery(r, aq, strict, maxBytes)
	}
	sr := &cleanerSpacingReader{r: io.LimitReader(r, maxBytes)}
	found, complete, err = streamMatchesQuery(sr, aq, strict, math.MaxInt64)
	return found, complete && sr.raw < maxBytes, err
}

// BinaryStreamingPrefilterDecided performs a bounded streaming prefilter for select binary types
// (eml, msg, mbox, rtf). It returns:
//   - found = true, decided = true   => conclusively found (prefilter passes)
//   - found = false, decided = true  => conclusively absent (prefilter fails; safe to skip)
//   - found = false, decided = false => inconclusive (do not skip; proceed to extraction)
//
// Compatibility APIs use strict conjunction. Ranked Stage 2 callers pass their
// strictness through the internal query-aware form.
func BinaryStreamingPrefilterDecided(filePath string, words []string, capBytes int64) (bool, bool) {
	return BinaryStreamingPrefilterDecidedPartial(filePath, words, capBytes, PartialModeOff)
}

// BinaryStreamingPrefilterDecidedPartial performs bounded binary prefiltering
// with partial-aware term matchers.
func BinaryStreamingPrefilterDecidedPartial(filePath string, words []string, capBytes int64, partial PartialMode) (bool, bool) {
	return binaryStreamingPrefilterDecidedQuery(filePath, compileQuery(words, partial), capBytes, true)
}

// BinaryStreamingPrefilterRankedDecided applies Stage 2 strict or relaxed
// ranked semantics. The compatibility APIs above always use strict conjunction.
func BinaryStreamingPrefilterRankedDecided(filePath string, words []string, capBytes int64, strict bool) (bool, bool) {
	return BinaryStreamingPrefilterRankedDecidedPartial(filePath, words, capBytes, strict, PartialModeOff)
}

// BinaryStreamingPrefilterRankedDecidedPartial applies Stage 2 ranked
// semantics with the requested partial-match mode.
func BinaryStreamingPrefilterRankedDecidedPartial(filePath string, words []string, capBytes int64, strict bool, partial PartialMode) (bool, bool) {
	return binaryStreamingPrefilterDecidedQuery(filePath, compileQuery(words, partial), capBytes, strict)
}

// mailEncodingReader observes the same bytes used by the streaming matcher.
// Keep enough overlap to detect even the longest marker across read boundaries.
type mailEncodingReader struct {
	io.Reader
	tail    string
	encoded bool
}

func (r *mailEncodingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && !r.encoded {
		text := r.tail + strings.ToLower(string(p[:n]))
		// Any charset declaration requires MIME interpretation. Gate even UTF-8
		// and ASCII declarations rather than partially parsing folded parameters.
		// HTML bodies also need extraction to decode entities, even without a
		// charset or transfer-encoding declaration.
		for _, marker := range []string{"base64", "quoted-printable", "=?", "charset", "text/html", "utf-16", "ucs-2", "\x00"} {
			if strings.Contains(text, marker) {
				r.encoded = true
				break
			}
		}
		const overlap = len("quoted-printable") - 1
		if len(text) > overlap {
			text = text[len(text)-overlap:]
		}
		r.tail = text
	}
	return n, err
}

func encodingAwarePrefilter(filePath string, query compiledQuery, strict bool, capBytes int64, rtf bool) (bool, bool) {
	if len(query.matchers) == 0 {
		return true, true
	}
	f, _, err := openRegularSearchFile(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()
	defer adviseDontNeed(f)
	maxBytes := int64(maxFileSizeBytes)
	if capBytes > 0 && capBytes < maxBytes {
		maxBytes = capBytes
	}

	if rtf {
		var raw strings.Builder
		found, complete, readErr := streamMatchesQuery(io.TeeReader(f, &raw), query, strict, maxBytes)
		if found {
			return true, true
		}
		if readErr != nil {
			return false, false
		}
		// Transform the entire bounded prefix so control words and groups split
		// across read boundaries behave exactly as in RTFExtractor.
		lower := strings.ToLower(raw.String())
		escaped := strings.Contains(lower, `\'`) || strings.Contains(lower, `\u`) || strings.Contains(lower, `\bin`)
		text, _ := (&RTFExtractor{}).ExtractText([]byte(raw.String()))
		text = cleanSearchContent(mappedContent{text: text}, false).text
		state := streamQueryMatches{found: make([]bool, len(query.matchers))}
		state.observe(text, query, 0)
		if state.satisfied(strict) {
			return true, true
		}
		return false, !escaped && (complete || readerValidatedEOF(f))
	}

	r := &mailEncodingReader{Reader: f}
	var raw strings.Builder
	found, complete, readErr := streamMatchesQuery(io.TeeReader(r, &raw), query, strict, maxBytes)
	if found {
		return true, true
	}
	if readErr != nil || r.encoded {
		return false, false
	}
	// Check the same cleaning transformations as Stage 2 before declaring a
	// raw miss. Raw mail is not extracted body text, so a normalized hit must
	// still go through extraction. Retain only the bounded scan and preserve
	// the streaming early-hit path.
	state := streamQueryMatches{found: make([]bool, len(query.matchers))}
	state.observe(cleanSearchContent(mappedContent{text: raw.String()}, false).text, query, 0)
	if state.satisfied(strict) {
		return false, false
	}
	// EMLExtractor collapses whitespace before Stage 2 cleaning (including
	// each message in MBOXExtractor). Preserve matches that line-oriented
	// cleanup of the raw mail would otherwise remove, such as quoted lines.
	collapsed := whitespaceRegex.ReplaceAllString(strings.TrimSpace(raw.String()), " ")
	state.observe(cleanSearchContent(mappedContent{text: collapsed}, false).text, query, 0)
	// Header brackets and body comparisons must not become HTML tags spanning
	// the body, or quote prefixes that erase tokens before OCR splitting.
	state.observe(cleanSearchContent(mappedContent{text: strings.NewReplacer("<", " ", ">", " ").Replace(collapsed)}, false).text, query, 0)
	if state.satisfied(strict) {
		return false, false
	}
	// Extraction joins the subject and body without intervening mail headers.
	// A phrase can span that join even when every raw-text variant misses it.
	for _, term := range query.terms {
		if strings.ContainsFunc(term, unicode.IsSpace) {
			return false, false
		}
	}
	// Validate EOF at the exact cap rather than trusting a stale file size.
	return false, complete || readerValidatedEOF(f)
}

func binaryStreamingPrefilterDecidedQuery(filePath string, query compiledQuery, capBytes int64, strict bool) (bool, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".msg":
		// OLE2 streams require extraction; raw hits and misses cannot skip it.
		return false, false
	case ".eml", ".mbox", ".rtf":
		return encodingAwarePrefilter(filePath, query, strict, capBytes, ext == ".rtf")

	case ".docx", ".odt":
		// Conservative ZIP sniff + capped XML stream:
		// - .docx: stream "word/document.xml"
		// - .odt:  stream "content.xml"
		// If we can conclusively find all words: return (true, true)
		// If we can conclusively determine absence at EOF: return (false, true)
		// Otherwise (errors, missing entries, or cap reached): return (false, false)
		f, st, err := openRegularSearchFile(filePath)
		if err != nil {
			return false, false
		}
		_ = adviseSequential(f)
		defer f.Close()

		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return false, false
		}

		var target string
		if ext == ".docx" {
			target = "word/document.xml"
		} else {
			target = "content.xml"
		}

		var xmlFile *zip.File
		for _, file := range zr.File {
			if file.Name == target {
				xmlFile = file
				break
			}
		}
		if xmlFile == nil {
			// Can't locate the main document stream; undecided
			return false, false
		}

		rc, err := xmlFile.Open()
		if err != nil {
			return false, false
		}
		defer rc.Close()

		if len(query.matchers) == 0 {
			return true, true
		}

		maxBytes := capBytes
		if maxBytes <= 0 {
			maxBytes = 5 * 1024 * 1024
		}
		found, complete, readErr := streamMatchesCleanedQuery(rc, query, strict, maxBytes, false)
		validatedAtLimit := false
		if !found && !complete && readErr == nil &&
			uint64(maxBytes) >= xmlFile.UncompressedSize64 {
			validatedAtLimit = readerValidatedEOF(rc)
		}
		return decideStreamQueryResult(
			found,
			complete,
			readErr,
			validatedAtLimit,
		)

	case ".doc":
		// Conservative OLE (.doc) prefilter:
		// - Open the compound file and stream a few likely text-bearing streams (WordDocument, 1Table, 0Table)
		// - Decode text with the same legacy-stream rule as Stage 3 extraction
		// - If all words are conclusively found within a capped budget: (true, true)
		// - Otherwise: (false, false) — undecided (never mark as conclusively absent)
		f, _, err := openRegularSearchFile(filePath)
		if err != nil {
			return false, false
		}
		_ = adviseSequential(f)
		defer f.Close()

		cf, err := mscfb.New(f)
		if err != nil {
			return false, false
		}

		if len(query.matchers) == 0 {
			return true, true
		}

		// Budget: total bytes across considered streams
		maxBytes := capBytes
		if maxBytes <= 0 {
			maxBytes = 2 * 1024 * 1024 // 2MB default cap
		}
		var total int64

		// Prioritized streams commonly containing main/body text
		targetStreams := map[string]bool{
			"WordDocument": true,
			"1Table":       true,
			"0Table":       true,
		}

		state := streamQueryMatches{found: make([]bool, len(query.matchers))}

		for ent, err2 := cf.Next(); err2 == nil; ent, err2 = cf.Next() {
			if total >= maxBytes {
				break
			}
			name := ent.Name
			if !targetStreams[name] {
				continue
			}

			// Read a limited portion of the stream
			budget := maxBytes - total
			if budget <= 0 {
				break
			}
			data, _ := io.ReadAll(io.LimitReader(ent, budget))
			total += int64(len(data))
			if len(data) == 0 {
				continue
			}

			state.observe(decodeLegacyStreamText(data, true), query, 0)
			if state.satisfied(strict) {
				return true, true
			}
		}

		// Not conclusively found within our conservative budget; leave as undecided
		return false, false

	case ".pdf":
		// PDF prefilter stays undecided; the pure-Go PDF extractor performs
		// the authoritative text extraction under the engine's timeout guard.
		return false, false
	default:
		// For other types, leave decision to the main path.
		return false, false
	}
}

// CheckFileContainsAllWords checks if a file contains all search words
func CheckFileContainsAllWords(filePath string, words []string, distance int, silent bool) (bool, error) {
	return CheckFileContainsAllWordsPartial(filePath, words, distance, silent, PartialModeOff)
}

// CheckFileContainsAllWordsPartial checks streamed and extracted text using
// the requested partial-match mode.
func CheckFileContainsAllWordsPartial(filePath string, words []string, distance int, silent bool, partial PartialMode) (bool, error) {
	return checkFileContainsAllWordsPartial(filePath, words, distance, silent, partial, defaultTextMaxBytes)
}

func checkFileContainsAllWordsPartial(filePath string, words []string, distance int, silent bool, partial PartialMode, maxBytes int64) (bool, error) {
	return checkFileContainsAllWordsQuery(filePath, compileQuery(words, partial), distance, silent, partial, maxBytes)
}

func checkFileContainsAllWordsQuery(filePath string, query compiledQuery, distance int, silent bool, partial PartialMode, maxBytes int64) (bool, error) {
	// Fast prefilter: require presence of all words before full distance check
	found, decided := streamContainsAllWordsDecidedQuery(filePath, query, maxBytes)
	if decided && !found {
		return false, nil
	}

	content, err := readTextForStage2(filePath, maxBytes)
	if err != nil {
		return false, err
	}
	// Clean the content so matching aligns with excerpt generation. Code files use the
	// minimal code-safe cleaner so markup/operator stripping can't drop a real match
	// (e.g. a term adjacent to "<?php", "->", or inside a "TList<T>" generic).
	cleaned := cleanSearchContent(mappedContent{text: content}, config.IsCodeFile(filePath)).text
	return CheckTextContainsAllWordsPartial(cleaned, query.terms, distance, partial), nil
}

// CheckFileContainsRankedWords checks a text file using the ranked matching
// semantics after a streaming candidate prefilter.
func CheckFileContainsRankedWords(filePath string, words []string, distance int, strict bool, silent bool) (score int, termCount int, matchedTerms []string, spanLen int, ok bool, err error) {
	return CheckFileContainsRankedWordsPartial(filePath, words, distance, strict, silent, PartialModeOff)
}

// CheckFileContainsRankedWordsPartial checks a text file using the requested
// partial-match mode after a streaming candidate prefilter.
func CheckFileContainsRankedWordsPartial(filePath string, words []string, distance int, strict bool, silent bool, partial PartialMode) (score int, termCount int, matchedTerms []string, spanLen int, ok bool, err error) {
	return checkFileContainsRankedWordsPartial(filePath, words, distance, strict, silent, partial, defaultTextMaxBytes)
}

func checkFileContainsRankedWordsPartial(filePath string, words []string, distance int, strict bool, silent bool, partial PartialMode, maxBytes int64) (score int, termCount int, matchedTerms []string, spanLen int, ok bool, err error) {
	return checkFileContainsRankedWordsQuery(filePath, compileQuery(words, partial), distance, strict, silent, maxBytes)
}

func checkFileContainsRankedWordsQuery(filePath string, query compiledQuery, distance int, strict bool, silent bool, maxBytes int64) (score int, termCount int, matchedTerms []string, spanLen int, ok bool, err error) {
	analysis, ok, err := checkFileRankedAnalysisQuery(filePath, query, distance, strict, maxBytes)
	if err != nil || !ok {
		return 0, 0, nil, 0, ok, err
	}
	return analysis.Score, analysis.TermCount, analysis.MatchedTerms, analysis.SpanLength, true, nil
}

func checkFileRankedAnalysisQuery(filePath string, query compiledQuery, distance int, strict bool, maxBytes int64) (rankedTextAnalysis, bool, error) {
	found, decided := streamContainsRankedWordsDecidedQuery(filePath, query, strict, maxBytes)
	if decided && !found {
		return rankedTextAnalysis{}, false, nil
	}

	content, err := readTextForStage2(filePath, maxBytes)
	if err != nil {
		return rankedTextAnalysis{}, false, err
	}
	cleaned := cleanSearchContent(mappedContent{text: content}, config.IsCodeFile(filePath)).text
	analysis, ok := analyzeRankedText(cleaned, query, distance, strict)
	if !ok {
		return rankedTextAnalysis{}, false, nil
	}
	return analysis, true, nil
}

// CheckFileContainsExcludeWords checks if a file contains any exclude words
func CheckFileContainsExcludeWords(filePath string, excludeWords []string) (bool, error) {
	return checkFileContainsExcludeWords(filePath, excludeWords, defaultTextMaxBytes)
}

func checkFileContainsExcludeWords(filePath string, excludeWords []string, maxBytes int64) (bool, error) {
	return checkFileContainsExcludeWordsWithOptions(filePath, excludeWords, maxBytes, QueryOptions{})
}

func checkFileContainsExcludeWordsWithOptions(filePath string, excludeWords []string, maxBytes int64, options QueryOptions) (bool, error) {
	if len(excludeWords) == 0 {
		return false, nil
	}

	content, err := readTextForStage2(filePath, maxBytes)
	if err != nil {
		return false, err
	}
	for _, word := range excludeWords {
		if containsLiteralExcludeWord(content, word, options.CaseSensitive) {
			return true, nil
		}
	}

	return false, nil
}

func readTextForStage2(filePath string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = defaultTextMaxBytes
	}
	file, _, err := openRegularSearchFile(filePath)
	if err != nil {
		return "", err
	}
	_ = adviseSequential(file)
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxBytes))
	_ = adviseDontNeed(file)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// readTextPrefix reads only the active searchable prefix while reporting the
// file size observed before the read.
func readTextPrefix(filePath string, maxBytes int64) (string, int64, error) {
	if maxBytes <= 0 {
		maxBytes = defaultTextMaxBytes
	}

	file, stat, err := openRegularSearchFile(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	_ = adviseSequential(file)

	content, err := io.ReadAll(io.LimitReader(file, maxBytes))
	_ = adviseDontNeed(file)
	if err != nil {
		return "", 0, err
	}
	return string(content), stat.Size(), nil
}

// GetFileContent reads file content using the default size limit.
func GetFileContent(filePath string) (string, int64, error) {
	return GetFileContentWithLimit(filePath, 0)
}

// GetFileContentWithLimit reads at most maxBytes and returns the full file size.
// Non-positive limits use the default binary file size limit.
func GetFileContentWithLimit(filePath string, maxBytes int64) (string, int64, error) {
	if maxBytes <= 0 {
		maxBytes = int64(maxFileSizeBytes)
	}
	file, stat, err := openRegularSearchFile(filePath)
	if err != nil {
		return "", 0, err
	}
	_ = adviseSequential(file)
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxBytes))
	_ = adviseDontNeed(file)
	if err != nil {
		return "", 0, err
	}
	return string(content), stat.Size(), nil
}

func asciiIndexWholeWordCI(buf []byte, wordLower []byte) bool {
	return asciiIndexPartialCI(buf, wordLower, PartialModeOff) >= 0
}

// asciiIndexPartialCI returns the first case-insensitive match for wordLower.
func asciiIndexPartialCI(buf []byte, wordLower []byte, mode PartialMode) int {
	return discoveryMatchIndex(buf, compileTermMatcher(string(wordLower), mode, true), 0)
}

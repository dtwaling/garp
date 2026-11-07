package search

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	if len(patternParts) == 0 {
		return len(relParts) == 0
	}

	part := patternParts[0]
	if part == "**" {
		if pathScopePartsMatch(patternParts[1:], relParts) {
			return true
		}
		for i := range relParts {
			if pathScopePartsMatch(patternParts[1:], relParts[i+1:]) {
				return true
			}
		}
		return false
	}

	if len(relParts) == 0 {
		return false
	}
	matched, err := path.Match(part, relParts[0])
	if err != nil || !matched {
		return false
	}
	return pathScopePartsMatch(patternParts[1:], relParts[1:])
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
	matches := make([]string, 0, 128)
	err := walkCandidates(fileTypes, walkRoot, pathScope, options, func(path string, _ os.FileInfo) error {
		if IsBinaryFormat(path) {
			// include heavy binary types as candidates; full check later
			matches = append(matches, path)
			return nil
		}

		// Stream up to maxBytes looking for the first word
		const chunkSize = 64 * 1024
		maxBytes := options.TextMaxBytes()
		overlap := discoveryMatcherOverlap(matcher)

		f, openErr := os.Open(path)
		_ = adviseSequential(f)
		if openErr != nil {
			return nil
		}

		// Early path for small files: read whole file at once, avoid chunk loop
		if st, stErr := f.Stat(); stErr == nil && st.Size() <= chunkSize {
			data, _ := io.ReadAll(io.LimitReader(f, maxBytes))
			if discoveryMatchIndex(data, matcher, 0) >= 0 {
				matches = append(matches, path)
			}
			_ = adviseDontNeed(f)
			_ = f.Close()
			return nil
		}

		var total int64
		prev := make([]byte, 0, overlap)
		buf := make([]byte, chunkSize)
		found := false
		for {
			if total >= maxBytes {
				break
			}
			toRead := chunkSize
			if rem := maxBytes - total; rem < int64(toRead) {
				toRead = int(rem)
			}
			n, rErr := f.Read(buf[:toRead])
			if n > 0 {
				combined := append(prev, buf[:n]...)
				from := discoveryChunkMatchStart(total, len(prev))
				if discoveryMatchIndex(combined, matcher, from) >= 0 {
					found = true
				}
				if n >= overlap {
					prev = append(prev[:0], buf[n-overlap:n]...)
				} else {
					if len(combined) >= overlap {
						prev = append(prev[:0], combined[len(combined)-overlap:]...)
					} else {
						prev = append(prev[:0], combined...)
					}
				}
				total += int64(n)
			}
			if rErr == io.EOF {
				break
			}
			if rErr != nil {
				break
			}
		}

		if found {
			matches = append(matches, path)
		}
		_ = adviseDontNeed(f)
		_ = f.Close()
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
	for start := from; start <= limit; start++ {
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
			const chunkSize = 64 * 1024
			maxBytes := options.TextMaxBytes()
			overlap := discoveryMatcherOverlap(primaryMatcher)

			for p := range paths {
				maybePaceForMemory()

				f, openErr := os.Open(p)
				_ = adviseSequential(f)
				if openErr != nil {
					continue
				}

				// Early path for small files: read whole file at once, avoid chunk loop
				if st, stErr := f.Stat(); stErr == nil && st.Size() <= chunkSize {
					data, _ := io.ReadAll(io.LimitReader(f, maxBytes))
					_ = adviseDontNeed(f)
					_ = f.Close()

					if discoveryMatchIndex(data, primaryMatcher, 0) >= 0 {
						mu.Lock()
						matches = append(matches, p)
						mu.Unlock()
					}
					continue
				}

				var readTotal int64
				prev := make([]byte, 0, overlap)
				buf := make([]byte, chunkSize)
				found := false

				for {
					if readTotal >= maxBytes {
						break
					}
					toRead := chunkSize
					if rem := maxBytes - readTotal; rem < int64(toRead) {
						toRead = int(rem)
					}
					n, rErr := f.Read(buf[:toRead])
					if n > 0 {
						combined := append(prev, buf[:n]...)
						from := discoveryChunkMatchStart(readTotal, len(prev))
						if discoveryMatchIndex(combined, primaryMatcher, from) >= 0 {
							found = true
						}
						if n >= overlap {
							prev = append(prev[:0], buf[n-overlap:n]...)
						} else {
							if len(combined) >= overlap {
								prev = append(prev[:0], combined[len(combined)-overlap:]...)
							} else {
								prev = append(prev[:0], combined...)
							}
						}
						readTotal += int64(n)
					}
					if rErr == io.EOF {
						break
					}
					if rErr != nil {
						break
					}
				}

				_ = adviseDontNeed(f)
				_ = f.Close()

				if found {
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
			if ext == ".pdf" {
				// PDFs are handled later under strict guardrails; include as candidate
				mu.Lock()
				matches = append(matches, path)
				mu.Unlock()
				return nil
			}
			// For non-PDF heavy types, run a small capped streaming prefilter for the first term.
			// Only skip when conclusively absent; undecided or found => include.
			var capBytes int64
			switch ext {
			case ".eml", ".msg", ".mbox":
				capBytes = 256 * 1024
			default:
				capBytes = 2 * 1024 * 1024
			}
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
	if len(query.matchers) == 0 {
		return true, true
	}
	if maxBytes <= 0 {
		maxBytes = defaultTextMaxBytes
	}
	f, err := os.Open(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()

	found, complete, readErr := streamMatchesQuery(f, query, true, maxBytes)
	_ = adviseDontNeed(f)
	return decideStreamQueryResult(found, complete, readErr, true)
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
	f, err := os.Open(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()

	found, complete, readErr := streamMatchesQuery(f, query, strict, maxBytes)
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
	f, err := os.Open(filePath)
	if err != nil {
		return false, false
	}
	_ = adviseSequential(f)
	defer f.Close()

	// Align with GetFileContent limits, then apply optional capBytes
	stat, statErr := f.Stat()
	maxBytes := int64(maxFileSizeBytes)
	completeAtLimit := false
	if statErr == nil && stat.Size() <= maxBytes {
		maxBytes = stat.Size()
		completeAtLimit = true
	}
	if capBytes > 0 && capBytes < maxBytes {
		maxBytes = capBytes
		completeAtLimit = false
	}

	found, complete, readErr := streamMatchesQuery(f, query, strict, maxBytes)
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
	found []bool
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
	state := streamQueryMatches{found: make([]bool, len(query.matchers))}
	const chunkSize = 64 * 1024
	overlap := streamQueryOverlap(query)
	prev := make([]byte, 0, overlap)
	buf := make([]byte, chunkSize)
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
		n, readErr := r.Read(buf[:toRead])
		if n > 0 {
			combined := append(prev, buf[:n]...)
			from := discoveryChunkMatchStart(total, len(prev))
			state.observe(string(combined), query, from)
			if state.satisfied(strict) {
				return true, true, nil
			}
			if len(combined) > overlap {
				prev = append(prev[:0], combined[len(combined)-overlap:]...)
			} else {
				prev = append(prev[:0], combined...)
			}
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

func binaryStreamingPrefilterDecidedQuery(filePath string, query compiledQuery, capBytes int64, strict bool) (bool, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".eml", ".msg", ".mbox", ".rtf":
		return streamContainsQueryDecidedWithCap(filePath, query, strict, capBytes)

	case ".docx", ".odt":
		// Conservative ZIP sniff + capped XML stream:
		// - .docx: stream "word/document.xml"
		// - .odt:  stream "content.xml"
		// If we can conclusively find all words: return (true, true)
		// If we can conclusively determine absence at EOF: return (false, true)
		// Otherwise (errors, missing entries, or cap reached): return (false, false)
		f, err := os.Open(filePath)
		_ = adviseSequential(f)
		if err != nil {
			return false, false
		}
		defer f.Close()

		st, err := f.Stat()
		if err != nil {
			return false, false
		}

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
		found, complete, readErr := streamMatchesQuery(rc, query, strict, maxBytes)
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
		// - Salvage text best-effort (UTF-16 if possible, else ASCII with whitespace normalization)
		// - If all words are conclusively found within a capped budget: (true, true)
		// - Otherwise: (false, false) — undecided (never mark as conclusively absent)
		f, err := os.Open(filePath)
		_ = adviseSequential(f)
		if err != nil {
			return false, false
		}
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

			// Best-effort text salvage
			var text string
			if s, ok := tryDecodeUTF16BestEffort(data); ok {
				text = s
			} else {
				buf := make([]rune, 0, len(data))
				for _, b := range data {
					if b == 0x09 || b == 0x0a || b == 0x0d || (b >= 0x20 && b <= 0x7e) {
						buf = append(buf, rune(b))
					} else {
						buf = append(buf, ' ')
					}
				}
				// Use precompiled whitespaceRegex from cleaner.go
				text = strings.TrimSpace(whitespaceRegex.ReplaceAllString(string(buf), " "))
			}

			state.observe(text, query, 0)
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
	cleaned := CleanContent(content)
	if config.IsCodeFile(filePath) {
		cleaned = CleanContentCode(content)
	}
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
	cleaned := CleanContent(content)
	if config.IsCodeFile(filePath) {
		cleaned = CleanContentCode(content)
	}
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
	file, err := os.Open(filePath)
	_ = adviseSequential(file)
	if err != nil {
		return "", err
	}
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

	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	_ = adviseSequential(file)

	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
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
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	_ = adviseSequential(file)
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}

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

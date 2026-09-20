package search

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
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

// CheckTextContainsAllWords checks if extracted text contains all search words
// in any order, within a distance window (in characters) between the earliest
// and latest matched term positions.
func smartFormsEnabled() bool {
	return strings.EqualFold(os.Getenv("GARP_SMART_FORMS"), "1")
}

func buildTermRegexLower(word string, mode PartialMode) *regexp.Regexp {
	base := strings.ToLower(strings.TrimSpace(word))
	if base == "" {
		// never matches; safe fallback
		return getWordRegex(`a\A`)
	}
	return buildTermRegex(base, mode, false)
}

func buildTermRegexCI(word string, mode PartialMode) *regexp.Regexp {
	base := strings.TrimSpace(word)
	if base == "" {
		// never matches; safe fallback
		return getWordRegex(`a\A`)
	}
	return buildTermRegex(base, mode, true)
}

func buildTermRegex(base string, mode PartialMode, caseInsensitive bool) *regexp.Regexp {
	isGlob := strings.HasSuffix(base, "*") && len(strings.TrimSuffix(base, "*")) > 1
	if isGlob {
		base = strings.TrimSuffix(base, "*")
		if mode != PartialModeContains {
			mode = PartialModePrefix
		}
	} else if len(base) <= 2 {
		mode = PartialModeOff
	}

	prefix := ""
	if caseInsensitive {
		prefix = "(?i)"
	}

	switch mode {
	case PartialModePrefix:
		pat := fmt.Sprintf(`%s(?:^|[^a-zA-Z0-9_]|_)(%s\w*)`, prefix, regexp.QuoteMeta(base))
		return getWordRegex(pat)
	case PartialModeContains:
		pat := fmt.Sprintf(`%s(%s)`, prefix, regexp.QuoteMeta(base))
		return getWordRegex(pat)
	}

	suffix := `(?:es|s)?`
	if smartFormsEnabled() {
		suffix = `(?:es|s|ed|ing|al|tion|ation)?`
	}
	pat := fmt.Sprintf(`%s\b(?:%s%s)\b`, prefix, regexp.QuoteMeta(base), suffix)
	return getWordRegex(pat)
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
	_, _, _, _, ok := CheckTextContainsRankedWords(text, words, distance, true)
	return ok
}

// CheckTextContainsRankedWords finds the highest-scoring proximity cluster in
// text. A score is the sum of the lengths of distinct query terms in the
// cluster. For three or more terms, non-strict mode requires the first term
// plus at least one secondary term; strict mode requires every term.
func CheckTextContainsRankedWords(text string, words []string, distance int, strict bool) (score int, termCount int, matchedTerms []string, spanLen int, ok bool) {
	if len(words) == 0 {
		return 0, 0, nil, 0, true
	}

	type queryTerm struct {
		word string
	}
	type match struct {
		start     int
		end       int
		termIndex int
	}

	// A repeated query term is one distinct term for both matching and scoring.
	terms := make([]queryTerm, 0, len(words))
	termIndexes := make(map[string]int, len(words))
	for _, word := range words {
		trimmed := strings.TrimSpace(word)
		key := strings.ToLower(trimmed)
		if _, exists := termIndexes[key]; exists {
			continue
		}
		termIndexes[key] = len(terms)
		terms = append(terms, queryTerm{word: trimmed})
	}
	if len(terms) == 0 {
		return 0, 0, nil, 0, false
	}

	contentStr := strings.ToLower(text)
	matches := make([]match, 0)
	for i, term := range terms {
		indexes := buildWordRegexLower(term.word).FindAllStringIndex(contentStr, -1)
		for _, idx := range indexes {
			matches = append(matches, match{start: idx[0], end: idx[1], termIndex: i})
		}
	}
	if len(matches) == 0 {
		return 0, 0, nil, 0, false
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		if matches[i].end != matches[j].end {
			return matches[i].end < matches[j].end
		}
		return matches[i].termIndex < matches[j].termIndex
	})

	requiredCount := len(terms)
	minimumCount := requiredCount
	if !strict && requiredCount >= 3 {
		minimumCount = 2
	}

	counts := make([]int, requiredCount)
	covered := 0
	left := 0
	bestSpan := 0
	for right := 0; right < len(matches); right++ {
		termIndex := matches[right].termIndex
		if counts[termIndex] == 0 {
			covered++
		}
		counts[termIndex]++

		// Preserve the legacy distance rule: match start positions must fit in
		// the requested window. SpanLength below additionally includes the
		// final matched term's end for ranking ties.
		for left <= right && matches[right].start-matches[left].start > distance {
			leftTermIndex := matches[left].termIndex
			counts[leftTermIndex]--
			if counts[leftTermIndex] == 0 {
				covered--
			}
			left++
		}

		// A duplicate at the left cannot improve the term set, so drop it to
		// retain the tightest window for the current set of distinct terms.
		for left < right && counts[matches[left].termIndex] > 1 {
			counts[matches[left].termIndex]--
			left++
		}

		valid := covered >= minimumCount && counts[0] > 0
		if strict {
			valid = covered == requiredCount
		}
		if !valid {
			continue
		}

		windowScore := 0
		windowTerms := make([]string, 0, covered)
		for i, count := range counts {
			if count > 0 {
				windowScore += len(terms[i].word)
				windowTerms = append(windowTerms, terms[i].word)
			}
		}
		windowSpan := matches[right].end - matches[left].start
		if !ok || windowScore > score ||
			(windowScore == score && covered > termCount) ||
			(windowScore == score && covered == termCount && windowSpan < bestSpan) {
			score = windowScore
			termCount = covered
			matchedTerms = windowTerms
			bestSpan = windowSpan
			ok = true
		}
	}

	if !ok {
		return 0, 0, nil, 0, false
	}
	return score, termCount, matchedTerms, bestSpan, true
}

// CheckTextContainsExcludeWords checks if extracted text contains any exclude words
func CheckTextContainsExcludeWords(text string, excludeWords []string) bool {
	if len(excludeWords) == 0 {
		return false
	}

	contentStr := strings.ToLower(text)

	// Check each exclude word
	for _, word := range excludeWords {
		if containsWholeWord(contentStr, strings.ToLower(word)) {
			return true
		}
	}

	return false
}

// FileInfo represents information about a file
type FileInfo struct {
	Path string
	Size int64
}

// matchesPathScope returns true if the file path (relative to walkRoot) matches
// at least one pattern in normScope. If normScope is empty, all files match.
// Patterns use filepath.Match semantics (simple globs: * and ? only).
// The comparison uses forward-slash paths for cross-platform consistency.
//
// Directory-prefix shorthand: a pattern with no wildcards that ends in "/" (or
// a bare dir name) is treated as a prefix match so "audio2midi/" matches any
// file under audio2midi/ at any depth.
//
// normScope must already be run through normalizePathScopePatterns -- the
// per-pattern normalization is hoisted out of this per-entry hot path.
func matchesPathScope(absPath, walkRoot string, normScope []string) bool {
	if len(normScope) == 0 {
		return true
	}
	rel, err := filepath.Rel(walkRoot, absPath)
	if err != nil {
		return false
	}
	relSlash := filepath.ToSlash(rel)
	for _, pattern := range normScope {
		matchPattern, matchRel := normalizePathScopeMatchPair(pattern, relSlash)
		if pathScopePatternMatches(matchPattern, matchRel) {
			return true
		}
		if scopePatternMatchesDirPrefix(matchPattern, matchRel) {
			return true
		}
	}
	return false
}

// dirCouldMatchPathScope returns true if descending into this directory could
// ever yield a file that matches at least one pathScope pattern. When it
// returns false the walk can safely skip the entire subtree with SkipDir.
//
// A directory could yield matches when any pattern:
//  1. Has this dir as a prefix (e.g. pattern "audio2midi/dsp.py" -> dir "audio2midi" is a prefix)
//  2. Has a wildcard that could span into this dir (e.g. pattern "*/foo/*" can match dirs at any level)
//  3. Is a bare wildcard pattern like "*" or "**"
//
// When normScope is empty (no restriction) all dirs are traversed.
// normScope must already be run through normalizePathScopePatterns.
func dirCouldMatchPathScope(absDir, walkRoot string, normScope []string) bool {
	if len(normScope) == 0 {
		return true
	}
	rel, err := filepath.Rel(walkRoot, absDir)
	if err != nil {
		return true // can't determine, don't prune
	}
	relSlash := filepath.ToSlash(rel)
	if relSlash == "." {
		return true // root always traversed
	}
	for _, pattern := range normScope {
		matchPattern, matchRel := normalizePathScopeMatchPair(pattern, relSlash)
		literalPrefix := pathScopeLiteralPrefix(matchPattern)
		if literalPrefix != "" &&
			matchRel != literalPrefix &&
			!strings.HasPrefix(matchRel, literalPrefix+"/") &&
			!strings.HasPrefix(literalPrefix, matchRel+"/") {
			continue
		}

		// Pattern contains wildcards -- can't safely prune based on dir name alone;
		// wildcards like "*" or "*/foo/*" could match any depth. Allow traversal.
		if strings.ContainsAny(matchPattern, "*?") {
			return true
		}
		// Literal pattern: prune only if dir is provably outside all patterns.
		// Dir is "inside" a pattern when:
		//   a) dir IS the pattern prefix (e.g. dir="audio2midi", pattern="audio2midi/dsp.py")
		//   b) dir is a component of pattern (e.g. dir="docs", pattern="docs/plans")
		//   c) pattern is a prefix of dir (e.g. dir="audio2midi/sub", pattern="audio2midi")
		prefix := strings.TrimRight(matchPattern, "/")
		if prefix == "" {
			continue
		}
		if matchRel == prefix ||
			strings.HasPrefix(matchRel, prefix+"/") ||
			strings.HasPrefix(prefix, matchRel+"/") {
			return true
		}
	}
	return false
}

// normalizePathScopePatterns normalizes every pattern once, before the walk, so
// the per-file/per-directory matchers don't repeat this pure (pattern, walkRoot)
// work on the hot path. Returns nil for an empty scope.
func normalizePathScopePatterns(pathScope []string, walkRoot string) []string {
	if len(pathScope) == 0 {
		return nil
	}
	out := make([]string, len(pathScope))
	for i, p := range pathScope {
		out[i] = normalizePathScopePattern(p, walkRoot)
	}
	return out
}

func normalizePathScopePattern(pattern, walkRoot string) string {
	pattern = stripMatchingQuotes(strings.TrimSpace(pattern))
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	if pattern == "" {
		return pattern
	}

	nativePattern := filepath.FromSlash(pattern)
	if filepath.IsAbs(nativePattern) {
		if rel, err := filepath.Rel(walkRoot, nativePattern); err == nil {
			if relSlash := filepath.ToSlash(rel); relSlash != "." && !strings.HasPrefix(relSlash, "../") && relSlash != ".." {
				return relSlash
			}
		}
	}
	return pattern
}

func normalizePathScopeMatchPair(pattern, relSlash string) (string, string) {
	if runtime.GOOS != "windows" {
		return pattern, relSlash
	}
	return strings.ToLower(pattern), strings.ToLower(relSlash)
}

func pathScopePatternMatches(pattern, relSlash string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		if matched, err := path.Match(pattern, path.Base(relSlash)); err == nil && matched {
			return true
		}
	}

	patternParts := splitPathScopeParts(pattern)
	relParts := splitPathScopeParts(relSlash)
	return pathScopePartsMatch(patternParts, relParts)
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
		base := strings.ToLower(filepath.Base(path))
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
	if walkRoot == "" {
		walkRoot = "."
	}
	matcher := newFileTypeMatcher(fileTypes)

	absRoot, err := filepath.Abs(walkRoot)
	if err != nil {
		return 0, err
	}
	normScope := normalizePathScopePatterns(pathScope, absRoot)

	count := 0
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Ignore permission errors; keep walking
			return nil
		}
		if d.IsDir() {
			if d.Name() != "." && config.ShouldSkipDirectory(d.Name()) {
				return filepath.SkipDir
			}
			// Prune entire subtree if no pathScope pattern can match anything under it.
			if len(normScope) > 0 && !dirCouldMatchPathScope(path, absRoot, normScope) {
				return filepath.SkipDir
			}
			return nil
		}
		if !matcher.allows(path) {
			return nil
		}
		if !matchesPathScope(path, absRoot, normScope) {
			return nil
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
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
	if walkRoot == "" {
		walkRoot = "."
	}
	absRoot, err := filepath.Abs(walkRoot)
	if err != nil {
		return nil, err
	}
	matcher := newFileTypeMatcher(fileTypes)
	normScope := normalizePathScopePatterns(pathScope, absRoot)

	// Precompute lowercased search word for fast ASCII whole-word scan
	wLower := strings.ToLower(word)
	heavy := map[string]bool{
		".pdf":  true,
		".docx": true,
		".odt":  true,
		".msg":  true,
		".eml":  true,
		".mbox": true,
	}
	matches := make([]string, 0, 128)
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Ignore permission errors; keep walking
			return nil
		}
		if d.IsDir() {
			if d.Name() != "." && config.ShouldSkipDirectory(d.Name()) {
				return filepath.SkipDir
			}
			// Prune entire subtree if no pathScope pattern can match anything under it.
			if len(normScope) > 0 && !dirCouldMatchPathScope(path, absRoot, normScope) {
				return filepath.SkipDir
			}
			return nil
		}

		// Filter by type (extension or special name) if provided
		if !matcher.allows(path) {
			return nil
		}

		// Filter by pathScope if provided
		if !matchesPathScope(path, absRoot, normScope) {
			return nil
		}
		if heavy[strings.ToLower(filepath.Ext(path))] {
			// include heavy binary types as candidates; full check later
			matches = append(matches, path)
			return nil
		}

		// Stream up to maxBytes looking for the first word
		const chunkSize = 64 * 1024
		const maxBytes = 5 * 1024 * 1024
		overlap := 32
		if l := len(wLower) - 1; l > overlap {
			overlap = l
		}

		f, openErr := os.Open(path)
		_ = adviseSequential(f)
		if openErr != nil {
			return nil
		}

		// Early path for small files: read whole file at once, avoid chunk loop
		if st, stErr := f.Stat(); stErr == nil && st.Size() <= chunkSize {
			data, _ := io.ReadAll(f)
			found := asciiIndexPartialCI(data, []byte(wLower), partial)
			if found >= 0 {
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
				if asciiIndexPartialCI(combined, []byte(wLower), partial) >= 0 {
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

// heavyBinaryPrefilterTerms keeps the discovery anchor while selecting the most
// specific secondary term for bounded heavy-binary prefiltering.
func heavyBinaryPrefilterTerms(words []string) []string {
	if len(words) < 3 {
		return words
	}

	longestSecondary := words[1]
	for _, term := range words[2:] {
		if len(term) > len(longestSecondary) {
			longestSecondary = term
		}
	}
	return []string{words[0], longestSecondary}
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
	if walkRoot == "" {
		walkRoot = "."
	}
	absRoot, err := filepath.Abs(walkRoot)
	if err != nil {
		return nil, err
	}
	matcher := newFileTypeMatcher(fileTypes)
	normScope := normalizePathScopePatterns(pathScope, absRoot)

	// Emit initial progress with unknown total
	if onProgress != nil {
		onProgress(0, 0, "")
	}

	primaryLower := strings.ToLower(words[0])
	termsToCheck := heavyBinaryPrefilterTerms(words)
	heavy := map[string]bool{
		".pdf":  true,
		".docx": true,
		".odt":  true,
		".msg":  true,
		".eml":  true,
		".mbox": true,
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
			const maxBytes = 5 * 1024 * 1024
			overlap := 32
			if l := len(primaryLower) - 1; l > overlap {
				overlap = l
			}

			for p := range paths {
				maybePaceForMemory()

				f, openErr := os.Open(p)
				_ = adviseSequential(f)
				if openErr != nil {
					continue
				}

				// Early path for small files: read whole file at once, avoid chunk loop
				if st, stErr := f.Stat(); stErr == nil && st.Size() <= chunkSize {
					data, _ := io.ReadAll(f)
					_ = adviseDontNeed(f)
					_ = f.Close()

					found := asciiIndexPartialCI(data, []byte(primaryLower), partial)
					if found >= 0 {
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
						if asciiIndexPartialCI(combined, []byte(primaryLower), partial) >= 0 {
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

				if found || (!found && readTotal >= maxBytes) {
					mu.Lock()
					matches = append(matches, p)
					mu.Unlock()
				}
			}
		}()
	}

	processed := 0

	// Walk and stream paths to workers
	var walkErr error
	walkErr = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() != "." && config.ShouldSkipDirectory(d.Name()) {
				return filepath.SkipDir
			}
			// Prune entire subtree if no pathScope pattern can match anything under it.
			if len(normScope) > 0 && !dirCouldMatchPathScope(path, absRoot, normScope) {
				return filepath.SkipDir
			}
			return nil
		}

		if !matcher.allows(path) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))

		// Filter by pathScope if provided
		if !matchesPathScope(path, absRoot, normScope) {
			return nil
		}

		processed++
		if onProgress != nil {
			onProgress(processed, 0, path)
		}

		// Heavy files: conservative prefilter for non-PDF; include unless decisively absent
		if heavy[ext] {
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
			found, decided := BinaryStreamingPrefilterDecided(path, termsToCheck, capBytes)
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
	return matches, nil
}

// StreamContainsAllWords streams a file and returns true if all words are present (unordered, plural-aware, CI).
func StreamContainsAllWordsDecided(filePath string, words []string) (found bool, decided bool) {
	if len(words) == 0 {
		return true, true
	}
	f, err := os.Open(filePath)
	_ = adviseSequential(f)
	if err != nil {
		return false, true
	}
	defer f.Close()

	// Build plural-aware whole-word regexes (?i)\b(?:word(?:es|s)?)\b
	res := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		pat := fmt.Sprintf(`(?i)\b(?:%s(?:es|s)?)\b`, regexp.QuoteMeta(w))
		res = append(res, getWordRegex(pat))
	}
	if len(res) == 0 {
		return true, true
	}

	const chunkSize = 64 * 1024
	const overlap = 128

	// Align with GetFileContent limits
	stat, statErr := f.Stat()
	var maxBytes int64
	if statErr == nil {
		switch {
		case stat.Size() > 50*1024*1024:
			maxBytes = 10 * 1024 * 1024
		case stat.Size() > 10*1024*1024:
			maxBytes = 5 * 1024 * 1024
		default:
			maxBytes = stat.Size()
		}
	} else {
		maxBytes = 10 * 1024 * 1024
	}

	foundFlags := make([]bool, len(res))
	remaining := len(res)

	var total int64
	prev := make([]byte, 0, overlap)
	buf := make([]byte, chunkSize)
	for {
		maybePaceForMemory()
		if total >= maxBytes {
			// Budget reached; we couldn't decide conclusively
			_ = adviseDontNeed(f)
			return false, false
		}
		toRead := chunkSize
		if rem := maxBytes - total; rem < int64(toRead) {
			toRead = int(rem)
		}
		n, rErr := f.Read(buf[:toRead])
		if n > 0 {
			combined := append(prev, buf[:n]...)
			for i, re := range res {
				if !foundFlags[i] && re.Match(combined) {
					foundFlags[i] = true
					remaining--
					if remaining == 0 {
						_ = adviseDontNeed(f)
						return true, true
					}
				}
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
			// End of file; if not all found, the decision is conclusive
			_ = adviseDontNeed(f)
			return false, true
		}
		if rErr != nil {
			// I/O error; treat as decided false
			_ = adviseDontNeed(f)
			return false, true
		}
	}
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
	if len(words) <= 1 {
		return StreamContainsAllWordsDecided(filePath, words)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return false, true
	}
	_ = adviseSequential(f)
	defer f.Close()

	res := make([]*regexp.Regexp, 0, len(words))
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		res = append(res, buildWordRegexCI(word))
	}
	if len(res) <= 1 {
		return StreamContainsAllWordsDecided(filePath, words)
	}

	const chunkSize = 64 * 1024
	const overlap = 128
	foundFlags := make([]bool, len(res))
	remaining := len(res)
	var total int64
	prev := make([]byte, 0, overlap)
	buf := make([]byte, chunkSize)
	for {
		maybePaceForMemory()
		n, readErr := f.Read(buf)
		if n > 0 {
			combined := append(prev, buf[:n]...)
			for i, re := range res {
				if !foundFlags[i] && re.Match(combined) {
					foundFlags[i] = true
					remaining--
				}
			}
			secondaryFound := false
			for _, matched := range foundFlags[1:] {
				secondaryFound = secondaryFound || matched
			}
			if (strict && remaining == 0) || (!strict && foundFlags[0] && secondaryFound) {
				_ = adviseDontNeed(f)
				return true, true
			}
			if n >= overlap {
				prev = append(prev[:0], buf[n-overlap:n]...)
			} else if len(combined) >= overlap {
				prev = append(prev[:0], combined[len(combined)-overlap:]...)
			} else {
				prev = append(prev[:0], combined...)
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			_ = adviseDontNeed(f)
			return false, true
		}
		if readErr != nil {
			_ = adviseDontNeed(f)
			return false, true
		}
	}
}

// StreamContainsAllWordsDecidedWithCap streams a file and returns whether all words are present.
// - found = true, decided = true: conclusively found all words
// - found = false, decided = true: conclusively not all words present
// - found = false, decided = false: budget reached; prefilter is undecided (do not skip)
func StreamContainsAllWordsDecidedWithCap(filePath string, words []string, capBytes int64) (bool, bool) {
	if len(words) == 0 {
		return true, true
	}
	f, err := os.Open(filePath)
	_ = adviseSequential(f)
	if err != nil {
		return false, true
	}
	defer f.Close()

	// Build plural/smart-forms aware whole-word regexes
	res := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		res = append(res, buildWordRegexCI(w))
	}
	if len(res) == 0 {
		return true, true
	}

	const chunkSize = 64 * 1024
	const overlap = 128

	// Align with GetFileContent limits, then apply optional capBytes
	stat, statErr := f.Stat()
	var maxBytes int64
	var capped bool
	if statErr == nil {
		switch {
		case stat.Size() > 50*1024*1024:
			maxBytes = 10 * 1024 * 1024
		case stat.Size() > 10*1024*1024:
			maxBytes = 5 * 1024 * 1024
		default:
			maxBytes = stat.Size()
		}
	} else {
		maxBytes = 10 * 1024 * 1024
	}
	if capBytes > 0 && capBytes < maxBytes {
		maxBytes = capBytes
		capped = true
	}

	foundFlags := make([]bool, len(res))
	remaining := len(res)

	var total int64
	prev := make([]byte, 0, overlap)
	buf := make([]byte, chunkSize)
	for {
		maybePaceForMemory()
		if total >= maxBytes {
			_ = adviseDontNeed(f)
			if !capped {
				// We reached the end of file without finding all terms
				return false, true // decided miss
			}
			return false, false // budget reached; undecided
		}
		toRead := chunkSize
		if rem := maxBytes - total; rem < int64(toRead) {
			toRead = int(rem)
		}
		n, rErr := f.Read(buf[:toRead])
		if n > 0 {
			combined := append(prev, buf[:n]...)
			for i, re := range res {
				if !foundFlags[i] && re.Match(combined) {
					foundFlags[i] = true
					remaining--
					if remaining == 0 {
						_ = adviseDontNeed(f)
						return true, true
					}
				}
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
			_ = adviseDontNeed(f)
			return false, true // EOF: conclusively not all present
		}
		if rErr != nil {
			_ = adviseDontNeed(f)
			return false, true // I/O error: treat as decided false
		}
	}
}

// BinaryStreamingPrefilterDecided performs a bounded streaming prefilter for select binary types
// (eml, msg, mbox, rtf). It returns:
//   - found = true, decided = true   => conclusively found (prefilter passes)
//   - found = false, decided = true  => conclusively absent (prefilter fails; safe to skip)
//   - found = false, decided = false => inconclusive (do not skip; proceed to extraction)
//
// It uses the existing StreamContainsAllWordsDecidedWithCap checker and, for 3+ terms,
// checks the first query term and the longest secondary term.
func BinaryStreamingPrefilterDecided(filePath string, words []string, capBytes int64) (bool, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	termsToCheck := heavyBinaryPrefilterTerms(words)
	switch ext {
	case ".eml", ".msg", ".mbox", ".rtf":
		// Existing streaming prefilter for email/rtf-like formats
		return StreamContainsAllWordsDecidedWithCap(filePath, termsToCheck, capBytes)

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

		// Build plural/smart-forms aware whole-word regexes
		res := make([]*regexp.Regexp, 0, len(termsToCheck))
		for _, w := range termsToCheck {
			w = strings.TrimSpace(w)
			if w == "" {
				continue
			}
			res = append(res, buildWordRegexCI(w))
		}
		if len(res) == 0 {
			return true, true
		}

		// Stream the XML entry with a cap and overlap window
		const chunkSize = 64 * 1024
		const overlap = 128

		maxBytes := capBytes
		if maxBytes <= 0 {
			// Reasonable default cap for XML streaming
			maxBytes = 5 * 1024 * 1024
		}

		foundFlags := make([]bool, len(res))
		remaining := len(res)

		var total int64
		prev := make([]byte, 0, overlap)
		buf := make([]byte, chunkSize)

		for {
			if total >= maxBytes {
				// Budget reached; undecided
				return false, false
			}
			toRead := chunkSize
			if rem := maxBytes - total; rem < int64(toRead) {
				toRead = int(rem)
			}
			n, rErr := rc.Read(buf[:toRead])
			if n > 0 {
				combined := append(prev, buf[:n]...)
				for i, re := range res {
					if !foundFlags[i] && re.Match(combined) {
						foundFlags[i] = true
						remaining--
						if remaining == 0 {
							return true, true
						}
					}
				}

				// Maintain overlap
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
				// End of stream; conclusively absent
				return false, true
			}
			if rErr != nil {
				// I/O/read error on entry: undecided
				return false, false
			}
		}

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

		// Build plural/smart-forms aware whole-word regexes
		res := make([]*regexp.Regexp, 0, len(words))
		for _, w := range words {
			w = strings.TrimSpace(w)
			if w == "" {
				continue
			}
			res = append(res, buildWordRegexCI(w))
		}
		if len(res) == 0 {
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

		foundFlags := make([]bool, len(res))
		remaining := len(res)

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

			for i, re := range res {
				if !foundFlags[i] && re.MatchString(text) {
					foundFlags[i] = true
					remaining--
					if remaining == 0 {
						return true, true
					}
				}
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
	// Fast prefilter: require presence of all words before full distance check
	if !StreamContainsAllWords(filePath, words) {
		return false, nil
	}

	content, _, err := GetFileContent(filePath)
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
	return CheckTextContainsAllWords(cleaned, words, distance), nil
}

// CheckFileContainsRankedWords checks a text file using the ranked matching
// semantics after a streaming candidate prefilter.
func CheckFileContainsRankedWords(filePath string, words []string, distance int, strict bool, silent bool) (score int, termCount int, matchedTerms []string, spanLen int, ok bool, err error) {
	found, decided := StreamContainsRankedWordsDecided(filePath, words, strict)
	if decided && !found {
		return 0, 0, nil, 0, false, nil
	}

	content, _, err := GetFileContent(filePath)
	if err != nil {
		return 0, 0, nil, 0, false, err
	}
	cleaned := CleanContent(content)
	if config.IsCodeFile(filePath) {
		cleaned = CleanContentCode(content)
	}
	score, termCount, matchedTerms, spanLen, ok = CheckTextContainsRankedWords(cleaned, words, distance, strict)
	return score, termCount, matchedTerms, spanLen, ok, nil
}

// CheckFileContainsExcludeWords checks if a file contains any exclude words
func CheckFileContainsExcludeWords(filePath string, excludeWords []string) (bool, error) {
	if len(excludeWords) == 0 {
		return false, nil
	}

	file, err := os.Open(filePath)
	_ = adviseSequential(file)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Get file size for large file handling
	stat, err := file.Stat()
	if err != nil {
		return false, err
	}

	var reader io.Reader = file

	// Limit read size for large files
	if stat.Size() > 50*1024*1024 { // 50MB
		reader = io.LimitReader(file, 10*1024*1024) // Read first 10MB
	} else if stat.Size() > 10*1024*1024 { // 10MB
		reader = io.LimitReader(file, 5*1024*1024) // Read first 5MB
	}

	// Read content
	content, err := io.ReadAll(reader)
	if err != nil {
		return false, err
	}

	contentStr := strings.ToLower(string(content))

	// Check each exclude word
	for _, word := range excludeWords {
		if containsWholeWord(contentStr, strings.ToLower(word)) {
			return true, nil
		}
	}

	return false, nil
}

// GetFileContent reads and returns file content with size limits
func GetFileContent(filePath string) (string, int64, error) {
	file, err := os.Open(filePath)
	_ = adviseSequential(file)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}

	var reader io.Reader = file

	// Limit read size for large files
	if stat.Size() > 50*1024*1024 { // 50MB
		reader = io.LimitReader(file, 10*1024*1024) // Read first 10MB
	} else if stat.Size() > 10*1024*1024 { // 10MB
		reader = io.LimitReader(file, 5*1024*1024) // Read first 5MB
	}

	// Read content
	content, err := io.ReadAll(reader)
	if err != nil {
		return "", 0, err
	}

	return string(content), stat.Size(), nil
}

func asciiIndexWholeWordCI(buf []byte, wordLower []byte) bool {
	return asciiIndexPartialCI(buf, wordLower, PartialModeOff) >= 0
}

// asciiIndexPartialCI returns the first ASCII case-insensitive match for wordLower.
func asciiIndexPartialCI(buf []byte, wordLower []byte, mode PartialMode) int {
	if len(wordLower) == 0 {
		return -1
	}
	if len(wordLower) >= 3 && wordLower[len(wordLower)-1] == '*' {
		wordLower = wordLower[:len(wordLower)-1]
		if mode == PartialModeOff {
			mode = PartialModePrefix
		}
	}
	if len(wordLower) == 0 || len(buf) < len(wordLower) {
		return -1
	}

	isWordChar := func(b byte) bool {
		switch {
		case b >= 'A' && b <= 'Z':
			return true
		case b >= 'a' && b <= 'z':
			return true
		case b >= '0' && b <= '9':
			return true
		default:
			return b == '_'
		}
	}
	toLower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b | 0x20
		}
		return b
	}

	wl := len(wordLower)
	limit := len(buf) - wl
	for i := 0; i <= limit; i++ {
		if mode != PartialModeContains && i > 0 && isWordChar(buf[i-1]) && (mode == PartialModeOff || buf[i-1] != '_') {
			continue
		}

		j := 0
		for ; j < wl; j++ {
			if toLower(buf[i+j]) != wordLower[j] {
				break
			}
		}
		if j != wl {
			continue
		}
		if mode == PartialModeContains || mode == PartialModePrefix {
			return i
		}

		end := i + wl
		if end >= len(buf) || !isWordChar(buf[end]) {
			return i
		}
		if end < len(buf) && toLower(buf[end]) == 's' {
			endS := end + 1
			if endS >= len(buf) || !isWordChar(buf[endS]) {
				return i
			}
		}
		if end+1 < len(buf) && toLower(buf[end]) == 'e' && toLower(buf[end+1]) == 's' {
			endES := end + 2
			if endES >= len(buf) || !isWordChar(buf[endES]) {
				return i
			}
		}
	}
	return -1
}

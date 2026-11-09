package search

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"garp/config"
)

// ExcerptCharBudget allows the UI to provide an inner-width–based char budget for excerpts.
// If nil, the engine will use a safe default. The UI should set this to (innerWidth*5)
// clamped to [240, 600] to keep the window stable.
var ExcerptCharBudget func() int

// Compiled once at load: email header extractors used to enrich .eml/.msg results.
var (
	emailDateRe    = regexp.MustCompile(`(?mi)^Date:\s*(.+)$`)
	emailSubjectRe = regexp.MustCompile(`(?mi)^Subject:\s*(.+)$`)
)

// capForBinaryPrefilter returns the byte cap for the bounded streaming prefilter:
// a smaller window for EML/MSG, a conservative default for PDFs and other binaries.
func capForBinaryPrefilter(ext string) int64 {
	if strings.EqualFold(ext, ".eml") || strings.EqualFold(ext, ".msg") {
		return 256 * 1024
	}
	return 1024 * 1024
}

// SearchResult represents a file that matches all search criteria
type SearchResult struct {
	FilePath        string
	rankingPath     string
	rootOrder       int
	canonicalPath   string
	FileSize        int64
	Score           int      // Sum of distinct matched term lengths in the best window.
	TermCount       int      // Number of distinct query terms in the best window.
	PathMatchCount  int      `json:"-"`
	BoundaryQuality int      `json:"-"`
	MatchedTerms    []string // Distinct query terms from the best window.
	SpanLength      int      // Character span of the best window.
	Excerpts        []string       // ANSI-highlighted, for TUI display
	RawExcerpts     []string       // plain text before highlighting -- for machine-readable output (--json, --plain)
	ExcerptMatches  [][]MatchRange `json:"-"` // source-validated byte ranges per RawExcerpts entry
	StartLines      []int          // 1-based start line per excerpt (parallel to RawExcerpts); 0 = unknown
	// Per-chunk ranked quality (parallel to RawExcerpts), best-first within the
	// file. Zero for chunks that did not frame a ranked window.
	ChunkScores     []int
	ChunkTermCounts []int
	// TopQualityChunks is the Stage 2 count of non-overlapping best-quality
	// windows. Emitted excerpt limits do not affect it.
	TopQualityChunks int
	CleanContent     string
	EmailDate        string
	EmailSubject     string
}

// ExecutionResult reports bounded search results and adjusted Stage 3 metadata.
type ExecutionResult struct {
	Results   []SearchResult
	Matches   int
	Returned  int
	Truncated bool
}

// candidateMatch retains ranked metadata between filtering and result building.
type candidateMatch struct {
	filePath         string
	accessPath       string
	canonicalKey     string
	rankingPath      string
	rootOrder        int
	score            int
	termCount        int
	matchedTerms     []string
	pathMatchCount   int
	boundaryQuality  int
	spanLen          int
	topQualityChunks int
}

type discoveredCandidate struct {
	filePath     string
	canonicalKey string
	root         StartRoot
	rankingRoot  StartRoot
	rootOrder    int
}

func (match *candidateMatch) setAnalysis(analysis rankedTextAnalysis) {
	match.score = analysis.Score
	match.termCount = analysis.TermCount
	match.matchedTerms = analysis.MatchedTerms
	match.boundaryQuality = analysis.BoundaryQuality
	match.spanLen = analysis.SpanLength
	match.topQualityChunks = analysis.TopQualityChunks
}

// ProgressFunc is an optional callback to report progress like: processed, total, path
type ProgressFunc func(stage string, processed, total int, path string)

// ConcurrencyManager handles bounded concurrency for heavy operations
type ConcurrencyManager struct {
	sem chan struct{}
}

func NewConcurrencyManager(slots int) *ConcurrencyManager {
	return &ConcurrencyManager{sem: make(chan struct{}, slots)}
}

func (cm *ConcurrencyManager) Acquire() {
	cm.sem <- struct{}{}
}

func (cm *ConcurrencyManager) Release() {
	<-cm.sem
}

func (cm *ConcurrencyManager) ExecuteWithTimeout(fn func(), timeout time.Duration) error {
	done := make(chan struct{}, 1) // Buffered so goroutine can always send
	interrupted := make(chan struct{}, 1)

	go func() {
		defer func() { _ = recover() }()
		fn()
		select {
		case <-interrupted:
			// Function was interrupted, exit gracefully
		case done <- struct{}{}:
			// Function completed normally, signal done
		}
	}()

	select {
	case <-done:
		// Clean up any lingering goroutine state
		close(interrupted)
		return nil
	case <-time.After(timeout):
		// Signal interruption and return immediately
		close(interrupted)
		return fmt.Errorf("operation timed out")
	}
}

// heavySem is a single-slot semaphore to bound concurrent binary extractions
var heavySem = make(chan struct{}, 1)

// SearchEngine handles the multi-word search logic
type SearchEngine struct {
	SearchWords       []string
	ExcludeWords      []string
	FileTypes         []string
	IncludeCode       bool
	Strict            bool
	Partial           PartialMode
	CaseSensitive     bool
	SmartForms        bool
	PathOnly          bool
	Registry          *ExtractorRegistry
	Distance          int
	Silent            bool
	HeavyConcurrency  int
	FilterWorkers     int
	FileTimeoutBinary time.Duration
	MaxExcerpts       int
	MaxResults        int
	NoIgnore          bool
	MaxFileSize       int64
	PreflightCount    int

	// StartDir, if non-empty, sets the root directory for file walks.
	// When empty, the walk uses the current working directory.
	StartDir string
	// StartRoots contains roots prepared by PrepareStartRoots. When non-empty,
	// discovery and count must use these descriptors without reparsing.
	StartRoots []StartRoot
	// PathScope, if non-empty, restricts file walks to paths whose relative
	// path matches at least one simple glob pattern (e.g., "*/backend/*").
	PathScope                   []string
	UserGlobs                   []UserGlob
	PositiveGlobCodeEligibility bool

	// PDF metrics (atomic)
	pdfProcessed     int64 // atomic counter
	pdfSkippedBudget int64 // atomic counter

	// Metrics (atomic)
	emlPrefilterCount    int64
	emlPrefilterDurNanos int64
	emlExtractCount      int64
	emlExtractDurNanos   int64
	msgPrefilterCount    int64
	msgPrefilterDurNanos int64
	msgExtractCount      int64
	msgExtractDurNanos   int64

	// Optional progress callback (nil if unused)
	OnProgress ProgressFunc

	// Test observer for overlap-only progress key tracking.
	onProgressKeyTracking func()
}

// NewSearchEngine creates a new search engine instance
func NewSearchEngine(searchWords, excludeWords []string, fileTypes []string, includeCode bool, heavyConcurrency int, fileTimeoutBinary int) *SearchEngine {
	return &SearchEngine{
		SearchWords:       append([]string(nil), searchWords...),
		ExcludeWords:      excludeWords,
		FileTypes:         fileTypes,
		IncludeCode:       includeCode,
		Registry:          NewExtractorRegistry(),
		Distance:          5000,
		Silent:            false,
		HeavyConcurrency:  heavyConcurrency,
		FilterWorkers:     2,
		FileTimeoutBinary: time.Duration(fileTimeoutBinary) * time.Millisecond,
		MaxExcerpts:       1,
	}
}

func (se *SearchEngine) queryOptions() QueryOptions {
	return QueryOptions{
		CaseSensitive: se.CaseSensitive,
		SmartForms:    se.SmartForms,
	}
}

func (se *SearchEngine) walkOptions() WalkOptions {
	return WalkOptions{
		NoIgnore:                    se.NoIgnore,
		MaxFileSize:                 se.MaxFileSize,
		UserGlobs:                   se.UserGlobs,
		PositiveGlobCodeEligibility: se.PositiveGlobCodeEligibility,
	}
}

// NewSearchEngineWithWorkers creates a new search engine instance with an explicit filter worker count
func NewSearchEngineWithWorkers(searchWords, excludeWords []string, fileTypes []string, includeCode bool, heavyConcurrency int, fileTimeoutBinary int, filterWorkers int) *SearchEngine {
	se := NewSearchEngine(searchWords, excludeWords, fileTypes, includeCode, heavyConcurrency, fileTimeoutBinary)
	if filterWorkers > 0 {
		se.FilterWorkers = filterWorkers
	}
	return se
}

// DiscoverCandidates finds files containing the first search word
func (se *SearchEngine) DiscoverCandidates(fileCount int) ([]string, int, error) {
	roots, err := se.effectiveStartRoots()
	if err != nil {
		return nil, 0, err
	}
	candidates, total, err := se.discoverCandidates(compileQueryWithOptions(se.SearchWords, se.Partial, se.queryOptions()), fileCount, roots)
	paths := make([]string, len(candidates))
	for index := range candidates {
		paths[index] = candidates[index].filePath
	}
	return paths, total, err
}

func (se *SearchEngine) effectiveStartRoots() ([]StartRoot, error) {
	if len(se.StartRoots) > 0 {
		return se.StartRoots, nil
	}
	raw := []string{se.StartDir}
	if se.StartDir == "" {
		raw = nil
	}
	return PrepareStartRootsWithOptions(raw, StartRootOptions{
		NoIgnore:  se.NoIgnore,
		PathScope: se.PathScope,
		UserGlobs: se.UserGlobs,
	})
}

func (se *SearchEngine) discoverCandidates(query compiledQuery, fileCount int, roots []StartRoot) ([]discoveredCandidate, int, error) {
	if len(query.terms) == 0 {
		return nil, 0, nil
	}
	if !se.Silent {
		fmt.Printf("Finding files with %q...\n", query.terms[0])
	}
	candidates := make([]discoveredCandidate, 0, 128)
	candidateIndex := make(map[string]int)
	trackProgressKeys := se.OnProgress != nil && preparedPhysicalRootsOverlap(roots)
	var progressSeen map[string]struct{}
	if trackProgressKeys {
		progressSeen = make(map[string]struct{})
		if se.onProgressKeyTracking != nil {
			se.onProgressKeyTracking()
		}
	}
	processed := 0
	for _, root := range roots {
		var onProgress func(int, int, string)
		if se.OnProgress != nil {
			onProgress = func(rootProcessed, _ int, path string) {
				if rootProcessed == 0 {
					return
				}
				if trackProgressKeys {
					key := canonicalCandidateKey(root, path)
					if _, exists := progressSeen[key]; exists {
						return
					}
					progressSeen[key] = struct{}{}
				}
				processed++
				se.OnProgress("discovery", processed, fileCount, path)
			}
		}
		candidateFiles, err := findFilesWithFirstWordProgressQueryWithRoot(query, se.FileTypes, se.FilterWorkers, onProgress, root, se.PathScope, se.walkOptions())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to find files with first word: %w", err)
		}
		for _, filePath := range candidateFiles {
			key := canonicalCandidateKey(root, filePath)
			candidate := newDiscoveredCandidate(filePath, key, root, roots)
			if index, exists := candidateIndex[key]; exists {
				if discoveredCandidateProducerBetter(candidate, candidates[index]) {
					candidates[index] = candidate
				}
			} else {
				candidateIndex[key] = len(candidates)
				candidates = append(candidates, candidate)
			}
		}
	}
	total := len(candidates)
	if len(candidates) == 0 {
		if !se.Silent {
			fmt.Printf("No files found containing %q\n", query.terms[0])
		}
		return nil, total, nil
	}
	if !se.Silent {
		fmt.Printf("Found %s files containing %q\n", formatNumber(len(candidates)), query.terms[0])
	}
	return candidates, total, nil
}

// FilterCandidates filters candidates for all words and excludes
func (se *SearchEngine) FilterCandidates(candidateFiles []string, total int, startTime time.Time) ([]candidateMatch, error) {
	roots, err := se.effectiveStartRoots()
	if err != nil {
		return nil, err
	}
	traversals := make([]preparedRootTraversal, len(roots))
	for index, root := range roots {
		traversal, err := prepareRootTraversal(root, se.PathScope, se.UserGlobs, se.NoIgnore)
		if err != nil {
			return nil, err
		}
		traversals[index] = traversal
	}
	candidates := make([]discoveredCandidate, len(candidateFiles))
	for index, path := range candidateFiles {
		candidate, err := candidateProvenance(path, traversals)
		if err != nil {
			return nil, err
		}
		candidates[index] = candidate
	}
	return se.filterCandidates(compileQueryWithOptions(se.SearchWords, se.Partial, se.queryOptions()), candidates, total, startTime)
}

func candidateProvenance(path string, traversals []preparedRootTraversal) (discoveredCandidate, error) {
	roots := make([]StartRoot, len(traversals))
	for index := range traversals {
		roots[index] = traversals[index].root
	}
	var best discoveredCandidate
	found := false
	for _, traversal := range traversals {
		if !startRootCanProduceFile(traversal, path) {
			continue
		}
		root := traversal.root
		candidate := newDiscoveredCandidate(path, canonicalCandidateKey(root, path), root, roots)
		if !found || discoveredCandidateProducerBetter(candidate, best) {
			best = candidate
			found = true
		}
	}
	if found {
		return best, nil
	}
	if len(traversals) == 1 {
		root := traversals[0].root
		return newDiscoveredCandidate(path, canonicalPathKey(path), root, roots), nil
	}
	return discoveredCandidate{}, fmt.Errorf(
		"candidate %q is outside all prepared roots; multi-root FilterCandidates requires paths returned by DiscoverCandidates",
		path,
	)
}

func newDiscoveredCandidate(filePath, canonicalKey string, producer StartRoot, roots []StartRoot) discoveredCandidate {
	rankingRoot := candidateRankingRoot(producer, roots)
	return discoveredCandidate{
		filePath:     filePath,
		canonicalKey: canonicalKey,
		root:         producer,
		rankingRoot:  rankingRoot,
		rootOrder:    reportedRootOrder(rankingRoot, roots),
	}
}

func candidateRankingRoot(producer StartRoot, roots []StartRoot) StartRoot {
	if producer.Reported {
		return producer
	}
	rankingRoot := producer
	rankingDepth := -1
	for _, root := range roots {
		if !root.Reported || !strictPathDescendant(root.PhysicalPath, producer.PhysicalPath) {
			continue
		}
		if depth := physicalPathDepth(root.PhysicalPath); depth > rankingDepth {
			rankingRoot = root
			rankingDepth = depth
		}
	}
	return rankingRoot
}

func reportedRootOrder(target StartRoot, roots []StartRoot) int {
	order := 0
	for _, root := range roots {
		if !root.Reported {
			continue
		}
		if root.CanonicalKey == target.CanonicalKey {
			return order
		}
		order++
	}
	return target.SuppliedOrder
}

func discoveredCandidateProducerBetter(left, right discoveredCandidate) bool {
	if left.root.Reported != right.root.Reported {
		return left.root.Reported
	}
	if left.root.SuppliedOrder != right.root.SuppliedOrder {
		return left.root.SuppliedOrder < right.root.SuppliedOrder
	}
	leftPath := canonicalPathKey(left.root.LogicalPath)
	rightPath := canonicalPathKey(right.root.LogicalPath)
	return leftPath < rightPath
}

func projectedCandidatePath(candidate discoveredCandidate) (displayPath, rankingPath string) {
	producerRelative, err := filepath.Rel(candidate.root.LogicalPath, candidate.filePath)
	if err != nil {
		return candidate.filePath, mustRelativePath(candidate.rankingRoot.LogicalPath, candidate.filePath)
	}
	physicalPath := filepath.Join(candidate.root.PhysicalPath, producerRelative)
	rankingRelative, err := filepath.Rel(candidate.rankingRoot.PhysicalPath, physicalPath)
	if err != nil || rankingRelative == ".." ||
		strings.HasPrefix(rankingRelative, ".."+string(filepath.Separator)) {
		return candidate.filePath, mustRelativePath(candidate.rankingRoot.LogicalPath, candidate.filePath)
	}
	return filepath.Join(candidate.rankingRoot.LogicalPath, rankingRelative), rankingRelative
}

// extractBinaryFile gates file I/O and extraction, preferring random access for containers.
func (se *SearchEngine) extractBinaryFile(cm *ConcurrencyManager, filePath, ext string, extractor Extractor, wantEmailMeta bool) (extractedText string, fileSize int64, emailDate, emailSubject string, timedOut bool, readErr, extractErr error) {
	cm.Acquire()
	defer cm.Release()

	var extract func() (string, error)
	var metadata string
	if raExtractor, ok := extractor.(ReaderAtExtractor); ok {
		file, err := os.Open(filePath)
		if err != nil {
			readErr = err
			return
		}
		stat, err := file.Stat()
		if err != nil {
			file.Close()
			readErr = err
			return
		}
		fileSize = stat.Size()
		if se.MaxFileSize > 0 && fileSize > se.MaxFileSize {
			file.Close()
			readErr = fmt.Errorf("file size %d exceeds max-filesize %d", fileSize, se.MaxFileSize)
			return
		}
		if wantEmailMeta && strings.EqualFold(ext, ".msg") {
			prefix, err := io.ReadAll(io.LimitReader(io.NewSectionReader(file, 0, fileSize), 256*1024))
			if err != nil {
				file.Close()
				readErr = err
				return
			}
			metadata = string(prefix)
		}
		extract = func() (string, error) {
			// A timed-out worker still owns its handle until extraction finishes.
			defer file.Close()
			return raExtractor.ExtractReaderAt(file, stat.Size(), DefaultMaxExtractedBytes)
		}
	} else {
		stat, err := os.Stat(filePath)
		if err != nil {
			readErr = err
			return
		}
		fileSize = stat.Size()
		if se.MaxFileSize > 0 && fileSize > se.MaxFileSize {
			readErr = fmt.Errorf("file size %d exceeds max-filesize %d", fileSize, se.MaxFileSize)
			return
		}
		rawContent, size, err := GetFileContentWithLimit(filePath, se.MaxFileSize)
		if err != nil {
			readErr = err
			return
		}
		fileSize = size
		if se.MaxFileSize > 0 && size > se.MaxFileSize {
			readErr = fmt.Errorf("file size %d exceeds max-filesize %d", size, se.MaxFileSize)
			return
		}
		if wantEmailMeta && (strings.EqualFold(ext, ".eml") || strings.EqualFold(ext, ".msg")) {
			metadata = rawContent
		}
		extract = func() (string, error) {
			return extractor.ExtractText([]byte(rawContent))
		}
	}
	if m := emailDateRe.FindStringSubmatch(metadata); m != nil {
		emailDate = strings.TrimSpace(m[1])
	}
	if m := emailSubjectRe.FindStringSubmatch(metadata); m != nil {
		emailSubject = strings.TrimSpace(m[1])
	}

	// Only read worker-owned values after successful completion. On timeout,
	// the worker may still write them, but cannot mutate our return values.
	var text string
	var err error
	startXT := time.Now()
	timedOut = cm.ExecuteWithTimeout(func() {
		text, err = extract()
	}, se.FileTimeoutBinary) != nil
	durXT := time.Since(startXT)
	switch strings.ToLower(ext) {
	case ".eml":
		atomic.AddInt64(&se.emlExtractCount, 1)
		atomic.AddInt64(&se.emlExtractDurNanos, durXT.Nanoseconds())
	case ".msg":
		atomic.AddInt64(&se.msgExtractCount, 1)
		atomic.AddInt64(&se.msgExtractDurNanos, durXT.Nanoseconds())
	}
	if !timedOut {
		extractedText, extractErr = text, err
	}
	return
}

func (se *SearchEngine) filterCandidates(query compiledQuery, candidateFiles []discoveredCandidate, total int, startTime time.Time) ([]candidateMatch, error) {
	if len(query.terms) == 0 {
		return nil, nil
	}
	if !se.Silent {
		fmt.Println("Filtering for files containing ALL words...")
	}
	maxBytes := se.walkOptions().TextMaxBytes()

	// Separate excludes into extensions and words
	var extExcludes []string
	var wordExcludes []string
	for _, exclude := range se.ExcludeWords {
		if strings.HasPrefix(exclude, ".") {
			extExcludes = append(extExcludes, exclude)
		} else {
			wordExcludes = append(wordExcludes, exclude)
		}
	}

	// Print exclusion info
	if !se.Silent {
		if len(extExcludes) > 0 {
			fmt.Printf("Excluding types: %q\n", strings.Join(extExcludes, ", "))
		}
		if len(wordExcludes) > 0 {
			fmt.Printf("Excluding words: %q\n", strings.Join(wordExcludes, ", "))
		}
	}

	// Results and synchronization
	var matchingFiles []candidateMatch
	var mu sync.Mutex

	// Progress (atomic across workers)
	var processed int64

	// Concurrency manager for heavy extraction gating
	cm := NewConcurrencyManager(se.HeavyConcurrency)

	// Worker pool for Stage 2 text filtering
	workers := se.FilterWorkers
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan discoveredCandidate, workers*4)
	var wg sync.WaitGroup

	handleOne := func(candidate discoveredCandidate) bool {
		filePath := candidate.filePath
		displayPath, rankingPath := projectedCandidatePath(candidate)
		match := candidateMatch{
			filePath:     displayPath,
			accessPath:   filePath,
			canonicalKey: candidate.canonicalKey,
			rankingPath:  rankingPath,
			rootOrder:    candidate.rootOrder,
		}
		// Check for excluded extensions
		ext := filepath.Ext(filePath)
		for _, excluded := range extExcludes {
			if strings.EqualFold(excluded, ext) {
				return false
			}
		}

		// Check if file contains all search words
		hasAllWords := true
		var cleanedBinaryText string
		if len(query.terms) > 1 {
			if IsBinaryFormat(filePath) {
				ext := filepath.Ext(filePath)

				// Bounded streaming prefilter for supported binary types.
				cap := capForBinaryPrefilter(ext)
				startPF := time.Now()
				found, decided := binaryStreamingPrefilterDecidedQuery(filePath, query, cap, se.Strict)
				durPF := time.Since(startPF)
				switch strings.ToLower(ext) {
				case ".eml":
					atomic.AddInt64(&se.emlPrefilterCount, 1)
					atomic.AddInt64(&se.emlPrefilterDurNanos, durPF.Nanoseconds())
				case ".msg":
					atomic.AddInt64(&se.msgPrefilterCount, 1)
					atomic.AddInt64(&se.msgPrefilterDurNanos, durPF.Nanoseconds())
				}

				// Decided negative => safe skip
				if decided && !found {
					return false
				}

				// Extract and verify distance for binary formats, including PDFs.
				if extractor, exists := se.Registry.GetExtractor(ext); exists {
					extractedText, _, _, _, timedOut, err, extErr := se.extractBinaryFile(cm, filePath, ext, extractor, false)
					if err != nil {
						if !se.Silent {
							fmt.Printf("Warning: Error reading file %q: %q\n", filePath, err)
						}
						return false
					}
					if timedOut || extErr != nil {
						if !se.Silent {
							if extErr != nil {
								// underlying extractor error
								fmt.Printf("Warning: Error extracting text from %q: %q\n", filePath, extErr)
							} else {
								fmt.Printf("Warning: Extraction timeout for %q\n", filePath)
							}
						}
						return false
					}
					cleanedBinaryText = CleanContent(extractedText)
					analysis, ok := analyzeRankedText(cleanedBinaryText, query, se.Distance, se.Strict)
					if ok {
						match.setAnalysis(analysis)
					}
					hasAllWords = ok
				} else {
					if !se.Silent {
						fmt.Printf("Warning: No extractor for %q\n", ext)
					}
					return false
				}
			} else {
				// Text file: stream+distance
				analysis, ok, err := checkFileRankedAnalysisQuery(filePath, query, se.Distance, se.Strict, maxBytes)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error checking file %q: %q\n", filePath, err)
					}
					return false
				}
				if ok {
					match.setAnalysis(analysis)
				}
				hasAllWords = ok
			}
		} else {
			// Single-word presence check
			if IsBinaryFormat(filePath) {
				ext := filepath.Ext(filePath)
				// Run bounded prefilter for binary types before extraction.
				cap := capForBinaryPrefilter(ext)
				foundPF, decidedPF := binaryStreamingPrefilterDecidedQuery(filePath, query, cap, true)
				// Decided negative => safe skip
				if decidedPF && !foundPF {
					return false
				}

				// Bounded extraction fallback under semaphore + timeout.
				if extractor, exists := se.Registry.GetExtractor(ext); exists {
					extractedText, _, _, _, timedOut, err, extErr := se.extractBinaryFile(cm, filePath, ext, extractor, false)
					if err != nil {
						if !se.Silent {
							fmt.Printf("Warning: Error reading file %q: %q\n", filePath, err)
						}
						return false
					}
					if timedOut || extErr != nil {
						if !se.Silent {
							if extErr != nil {
								fmt.Printf("Warning: Error extracting text from %q: %q\n", filePath, extErr)
							} else {
								fmt.Printf("Warning: Extraction timeout for %q\n", filePath)
							}
						}
						return false
					}
					cleanedBinaryText = CleanContent(extractedText)
					analysis, ok := analyzeRankedText(cleanedBinaryText, query, se.Distance, se.Strict)
					if ok {
						match.setAnalysis(analysis)
					}
					hasAllWords = ok
				} else {
					if !se.Silent {
						fmt.Printf("Warning: No extractor for %q\n", ext)
					}
					return false
				}
			} else {
				analysis, ok, err := checkFileRankedAnalysisQuery(filePath, query, se.Distance, se.Strict, maxBytes)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error checking file %q: %q\n", filePath, err)
					}
					return false
				}
				if ok {
					match.setAnalysis(analysis)
				}
				hasAllWords = ok
			}
		}

		if !hasAllWords {
			return false
		}

		// Check if file contains any exclude words
		hasExcludeWords := false
		if len(wordExcludes) > 0 && IsBinaryFormat(filePath) {
			// Reuse the cleaned text already extracted for the query check.
			hasExcludeWords = CheckTextContainsExcludeWordsWithOptions(cleanedBinaryText, wordExcludes, se.queryOptions())
		} else if len(wordExcludes) > 0 {
			ok2, err := checkFileContainsExcludeWordsWithOptions(filePath, wordExcludes, maxBytes, se.queryOptions())
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error checking exclude words in %q: %q\n", filePath, err)
				}
				return false
			}
			hasExcludeWords = ok2
		}

		if hasExcludeWords {
			return false
		}

		match.pathMatchCount = pathMatchCount(displayPath, candidate.rankingRoot.LogicalPath, query)
		mu.Lock()
		matchingFiles = append(matchingFiles, match)
		mu.Unlock()
		return true
	}

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				maybePaceForMemory()
				matched := handleOne(candidate)

				_ = matched

				// Atomic progress update
				cur := atomic.AddInt64(&processed, 1)
				if se.OnProgress != nil {
					se.OnProgress("processing", int(cur), total, candidate.filePath)
				}
				// Optional periodic console progress
				if cur%500 == 0 && !se.Silent {
					elapsed := time.Since(startTime).Seconds()
					percent := float64(cur) * 100.0 / float64(len(candidateFiles))
					fmt.Printf("Progress: %d/%d files (%.1f%%) - %.0fs elapsed\n",
						cur, len(candidateFiles), percent, elapsed)
				}
			}
		}()
	}

	// Enqueue jobs
	for _, candidate := range candidateFiles {
		maybePaceForMemory()
		jobs <- candidate
	}
	close(jobs)
	wg.Wait()

	return matchingFiles, nil
}

type rankingKey struct {
	filePath         string
	rootOrder        int
	canonicalPath    string
	score            int
	termCount        int
	pathMatchCount   int
	boundaryQuality  int
	spanLen          int
	topQualityChunks int
}

func normalizedRankingPath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), `\`, "/")
}

func compareRankingPath(left, right string) int {
	leftPath := normalizedRankingPath(left)
	rightPath := normalizedRankingPath(right)
	leftFolded := strings.ToLower(leftPath)
	rightFolded := strings.ToLower(rightPath)
	if leftFolded < rightFolded {
		return -1
	}
	if leftFolded > rightFolded {
		return 1
	}
	if leftPath < rightPath {
		return -1
	}
	if leftPath > rightPath {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

// rankingBetter is the single file-order comparator used before and after
// Stage 3.
func rankingBetter(a, b rankingKey) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.termCount != b.termCount {
		return a.termCount > b.termCount
	}
	if a.pathMatchCount != b.pathMatchCount {
		return a.pathMatchCount > b.pathMatchCount
	}
	if a.boundaryQuality != b.boundaryQuality {
		return a.boundaryQuality > b.boundaryQuality
	}
	if a.spanLen != b.spanLen {
		return a.spanLen < b.spanLen
	}
	if a.topQualityChunks != b.topQualityChunks {
		return a.topQualityChunks > b.topQualityChunks
	}

	if comparison := compareRankingPath(a.filePath, b.filePath); comparison != 0 {
		return comparison < 0
	}
	if a.rootOrder != b.rootOrder {
		return a.rootOrder < b.rootOrder
	}
	return compareRankingPath(a.canonicalPath, b.canonicalPath) < 0
}

func candidateRankingKey(match candidateMatch) rankingKey {
	rankingPath := match.rankingPath
	if rankingPath == "" {
		rankingPath = match.filePath
	}
	return rankingKey{
		filePath:         rankingPath,
		rootOrder:        match.rootOrder,
		canonicalPath:    match.canonicalKey,
		score:            match.score,
		termCount:        match.termCount,
		pathMatchCount:   match.pathMatchCount,
		boundaryQuality:  match.boundaryQuality,
		spanLen:          match.spanLen,
		topQualityChunks: match.topQualityChunks,
	}
}

func resultRankingKey(result SearchResult) rankingKey {
	rankingPath := result.rankingPath
	if rankingPath == "" {
		rankingPath = result.FilePath
	}
	return rankingKey{
		filePath:         rankingPath,
		rootOrder:        result.rootOrder,
		canonicalPath:    result.canonicalPath,
		score:            result.Score,
		termCount:        result.TermCount,
		pathMatchCount:   result.PathMatchCount,
		boundaryQuality:  result.BoundaryQuality,
		spanLen:          result.SpanLength,
		topQualityChunks: result.TopQualityChunks,
	}
}

func candidateBetter(a, b candidateMatch) bool {
	return rankingBetter(candidateRankingKey(a), candidateRankingKey(b))
}

func sortAndDedupeCandidates(matches []candidateMatch) []candidateMatch {
	bestByPath := make(map[string]candidateMatch, len(matches))
	for _, match := range matches {
		if match.filePath == "" {
			continue
		}
		key := match.canonicalKey
		if key == "" {
			key = match.filePath
		}
		if current, exists := bestByPath[key]; !exists || candidateBetter(match, current) {
			bestByPath[key] = match
		}
	}

	ranked := make([]candidateMatch, 0, len(bestByPath))
	for _, match := range bestByPath {
		ranked = append(ranked, match)
	}
	sort.Slice(ranked, func(i, j int) bool {
		return candidateBetter(ranked[i], ranked[j])
	})
	return ranked
}

// sortSearchResults sorts results deterministically by ranked match quality and file path.
func sortSearchResults(results []SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		return rankingBetter(resultRankingKey(results[i]), resultRankingKey(results[j]))
	})
}

// buildPathOnlyResults constructs a distinct, deterministically ranked result
// list directly from candidate matches without re-reading files or extracting excerpts.
func (se *SearchEngine) buildPathOnlyResults(matchingFiles []candidateMatch) []SearchResult {
	ranked := sortAndDedupeCandidates(matchingFiles)
	if se.MaxResults > 0 && len(ranked) > se.MaxResults {
		ranked = ranked[:se.MaxResults]
	}
	results := make([]SearchResult, 0, len(ranked))
	for _, match := range ranked {
		results = append(results, SearchResult{
			FilePath:         match.filePath,
			rankingPath:      match.rankingPath,
			rootOrder:        match.rootOrder,
			canonicalPath:    match.canonicalKey,
			Score:            match.score,
			TermCount:        match.termCount,
			PathMatchCount:   match.pathMatchCount,
			BoundaryQuality:  match.boundaryQuality,
			MatchedTerms:     match.matchedTerms,
			SpanLength:       match.spanLen,
			TopQualityChunks: match.topQualityChunks,
		})
	}
	return results
}

// ExtractAndBuildResults extracts content and builds search results
func (se *SearchEngine) ExtractAndBuildResults(matchingFiles []candidateMatch) ([]SearchResult, error) {
	outcome, err := se.extractAndBuildResultsDetailed(compileQueryWithOptions(se.SearchWords, se.Partial, se.queryOptions()), matchingFiles)
	return outcome.Results, err
}

func (se *SearchEngine) extractAndBuildResults(query compiledQuery, matchingFiles []candidateMatch) ([]SearchResult, error) {
	outcome, err := se.extractAndBuildResultsDetailed(query, matchingFiles)
	return outcome.Results, err
}

func (se *SearchEngine) extractAndBuildResultsDetailed(query compiledQuery, matchingFiles []candidateMatch) (ExecutionResult, error) {
	matchingFiles = sortAndDedupeCandidates(matchingFiles)
	outcome := ExecutionResult{Matches: len(matchingFiles)}
	if se.PathOnly {
		outcome.Results = se.buildPathOnlyResults(matchingFiles)
		outcome.Returned = len(outcome.Results)
		outcome.Truncated = outcome.Returned < outcome.Matches
		return outcome, nil
	}

	resultCapacity := len(matchingFiles)
	if se.MaxResults > 0 {
		resultCapacity = min(resultCapacity, se.MaxResults)
	}
	results := make([]SearchResult, 0, resultCapacity)
	cm := NewConcurrencyManager(se.HeavyConcurrency)

	for _, match := range matchingFiles {
		if se.MaxResults > 0 && len(results) >= se.MaxResults {
			break
		}
		displayPath := match.filePath
		filePath := match.accessPath
		if filePath == "" {
			filePath = displayPath
		}
		maybePaceForMemory()
		var content string
		var fileSize int64
		var err error
		var emailDate, emailSubject string

		if IsBinaryFormat(filePath) {
			ext := filepath.Ext(filePath)
			if extractor, exists := se.Registry.GetExtractor(ext); exists {
				var timedOut bool
				var extractErr error
				content, fileSize, emailDate, emailSubject, timedOut, err, extractErr = se.extractBinaryFile(cm, filePath, ext, extractor, true)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error reading file %q: %q\n", filePath, err)
					}
					outcome.Matches--
					continue
				}
				if timedOut {
					err = fmt.Errorf("operation timed out")
				}
				if timedOut || extractErr != nil {
					if !se.Silent {
						if extractErr != nil {
							fmt.Printf("Warning: Error extracting text from %q: %q\n", filePath, extractErr)
						} else {
							fmt.Printf("Warning: Error extracting text from %q: %q\n", filePath, err)
						}
					}
					outcome.Matches--
					continue
				}
				if strings.EqualFold(ext, ".pdf") {
					atomic.AddInt64(&se.pdfProcessed, 1)
				}
			} else {
				if !se.Silent {
					fmt.Printf("Warning: No extractor for %q\n", ext)
				}
				outcome.Matches--
				continue
			}
		} else {
			content, fileSize, err = readTextPrefix(filePath, se.walkOptions().TextMaxBytes())
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error reading file %q: %q\n", filePath, err)
				}
				outcome.Matches--
				continue
			}
		}

		// Clean content and extract excerpts (make excerpt window reflect distance).
		// Code files use the minimal code-safe cleaner so source tokens survive (see CleanContentCode).
		isCode := config.IsCodeFile(filePath)
		var cleanContent string
		if isCode {
			cleanContent = CleanContentCode(content)
		} else {
			cleanContent = CleanContent(content)
		}
		boundedClean := cleanContent
		if len(boundedClean) > 64*1024 {
			boundedClean = boundedClean[:64*1024]
		}
		// Compute excerpt char budget from UI width (if provided) to keep the window stable.
		// Budget = innerWidth * 5, clamped to [240, 600]. Fallback to 400 if not provided.
		budget := 400
		if ExcerptCharBudget != nil {
			if b := ExcerptCharBudget(); b > 0 {
				budget = b
			}
		}
		budget = max(240, min(budget, 600))

		// Map the character budget to a context limit for excerpt generation (roughly half).
		SetExcerptContextLimit(budget / 2)

		maxExcerpts := se.MaxExcerpts
		if maxExcerpts <= 0 {
			maxExcerpts = 1
		}

		var excerpts []string
		var spans []excerptSpan
		// Per-file ranked subset: every coverage level from the query's minimum
		// match requirement up to the full term set can seed a chunk, so a file's
		// best cluster no longer suppresses separate lower-quality clusters.
		// minTerms mirrors Strategy A: relaxed 3+ term queries seed from 2-term
		// clusters up; strict (and 1-2 term) queries require the full set.
		minTerms := 0
		if len(query.terms) >= 3 && !se.Strict {
			minTerms = 2
		}
		if isCode {
			spans = extractRankedExcerptSpansQuery(cleanContent, query, maxExcerpts, match.termCount, minTerms, true)
			excerpts = excerptSpanTexts(spans)
		} else {
			spans = extractRankedExcerptSpansQuery(cleanContent, query, maxExcerpts, match.termCount, minTerms, false)
			excerpts = excerptSpanTexts(spans)
		}

		if len(excerpts) == 0 {
			outcome.Matches--
			continue
		}

		// If excerpts are very short (e.g., only a single terse sentence), expand the first excerpt
		// by pulling in neighboring sentences to provide more context. This helps fill the UI box
		// when there is little content returned.
		if len(excerpts) == 1 && len(excerpts[0]) < 160 {
			expanded := expandShortExcerptSpan(spans[0], 600)
			if len(expanded.text) > len(excerpts[0]) {
				spans[0] = expanded
				excerpts[0] = expanded.text
			}
		}

		// Highlight search terms in excerpts
		highlightedExcerpts := make([]string, len(excerpts))
		excerptMatches := make([][]MatchRange, len(spans))
		for i := range spans {
			highlightedExcerpts[i] = highlightExcerptSpan(spans[i])
			excerptMatches[i] = excerptSpanMatchRanges(spans[i])
		}

		// Resolve 1-based start line per excerpt by anchoring it back to the raw content.
		// Only text/code files have stable source lines; binary/extracted formats (PDF, DOCX,
		// email) do not, so we leave their line numbers unknown (nil/0).
		// Chunks are emitted best-first, but anchors are a positional fact: the
		// anchoring cursor walks document order, so we anchor spans in document
		// order (sorted by left edge) and map the results back to emission order.
		var startLines []int
		if !IsBinaryFormat(filePath) {
			docOrder := make([]int, len(excerpts))
			for i := range docOrder {
				docOrder[i] = i
			}
			sort.SliceStable(docOrder, func(a, b int) bool { return spans[docOrder[a]].left < spans[docOrder[b]].left })
			docSpans := make([]excerptSpan, len(docOrder))
			for i, idx := range docOrder {
				docSpans[i] = spans[idx]
			}
			docLines := computeExcerptSpanLinesQuery(content, docSpans, query, se.Distance)
			startLines = make([]int, len(excerpts))
			for i, idx := range docOrder {
				startLines[idx] = docLines[i]
			}
		}

		// Per-chunk ranked quality, parallel to the emitted excerpts.
		chunkScores := make([]int, len(spans))
		chunkTermCounts := make([]int, len(spans))
		for i, sp := range spans {
			chunkScores[i] = sp.score
			chunkTermCounts[i] = sp.termCount
		}

		result := SearchResult{
			FilePath:         displayPath,
			rankingPath:      match.rankingPath,
			rootOrder:        match.rootOrder,
			canonicalPath:    match.canonicalKey,
			FileSize:         fileSize,
			Score:            match.score,
			TermCount:        match.termCount,
			PathMatchCount:   match.pathMatchCount,
			BoundaryQuality:  match.boundaryQuality,
			MatchedTerms:     match.matchedTerms,
			SpanLength:       match.spanLen,
			Excerpts:         highlightedExcerpts,
			RawExcerpts:      excerpts,
			ExcerptMatches:   excerptMatches,
			StartLines:       startLines,
			ChunkScores:      chunkScores,
			ChunkTermCounts:  chunkTermCounts,
			TopQualityChunks: match.topQualityChunks,
			CleanContent:     boundedClean,
			EmailDate:        emailDate,
			EmailSubject:     emailSubject,
		}

		results = append(results, result)
	}
	sortSearchResults(results)
	outcome.Results = results
	outcome.Returned = len(results)
	outcome.Truncated = outcome.Returned < outcome.Matches
	return outcome, nil
}

// Execute performs the complete search operation
func (se *SearchEngine) Execute() ([]SearchResult, error) {
	outcome, err := se.ExecuteDetailed()
	return outcome.Results, err
}

// ExecuteDetailed performs the complete search operation and reports bounded
// result metadata adjusted for Stage 3 failures that were actually observed.
func (se *SearchEngine) ExecuteDetailed() (ExecutionResult, error) {
	startTime := time.Now()
	query := compileQueryWithOptions(se.SearchWords, se.Partial, se.queryOptions())
	if len(query.terms) == 0 {
		return ExecutionResult{}, nil
	}
	roots, err := se.effectiveStartRoots()
	if err != nil {
		return ExecutionResult{}, err
	}
	if err := ValidateRootPolicies(roots, se.PathScope, se.UserGlobs); err != nil {
		return ExecutionResult{}, err
	}
	se.StartRoots = roots

	// Emit initial progress with unknown total (0); discovery will update it
	if se.OnProgress != nil {
		se.OnProgress("discovery", 0, se.PreflightCount, "")
	}

	// Step 2: Discover candidates
	candidateFiles, total, err := se.discoverCandidates(query, se.PreflightCount, roots)
	if err != nil {
		return ExecutionResult{}, err
	}
	if candidateFiles == nil {
		if se.OnProgress != nil {
			se.OnProgress("discovery", se.PreflightCount, se.PreflightCount, "")
		}
		return ExecutionResult{}, nil
	}

	// Step 3: Filter candidates
	matchingFiles, err := se.filterCandidates(query, candidateFiles, total, startTime)
	if err != nil {
		return ExecutionResult{}, err
	}
	if len(matchingFiles) == 0 {
		if !se.Silent {
			fmt.Println("No files found containing all search terms.")
		}
		return ExecutionResult{}, nil
	}

	// Step 4: Extract and build results
	if !se.Silent {
		fmt.Printf("Found %s files containing all words. Extracting content...\n", formatNumber(len(matchingFiles)))
	}

	outcome, err := se.extractAndBuildResultsDetailed(query, matchingFiles)
	if err != nil {
		return ExecutionResult{}, err
	}

	totalTime := time.Since(startTime)
	if !se.Silent {
		fmt.Printf("Search completed in %.0f seconds!\n", totalTime.Seconds())

		// Latency metrics summary (averages in ms)
		if se.emlPrefilterCount > 0 || se.emlExtractCount > 0 || se.msgPrefilterCount > 0 || se.msgExtractCount > 0 {
			fmt.Println("Latency (avg ms):")
			if se.emlPrefilterCount > 0 {
				avg := float64(se.emlPrefilterDurNanos) / 1e6 / float64(se.emlPrefilterCount)
				fmt.Printf("  EML prefilter: %d • %.1fms\n", se.emlPrefilterCount, avg)
			}
			if se.emlExtractCount > 0 {
				avg := float64(se.emlExtractDurNanos) / 1e6 / float64(se.emlExtractCount)
				fmt.Printf("  EML extract:   %d • %.1fms\n", se.emlExtractCount, avg)
			}
			if se.msgPrefilterCount > 0 {
				avg := float64(se.msgPrefilterDurNanos) / 1e6 / float64(se.msgPrefilterCount)
				fmt.Printf("  MSG prefilter: %d • %.1fms\n", se.msgPrefilterCount, avg)
			}
			if se.msgExtractCount > 0 {
				avg := float64(se.msgExtractDurNanos) / 1e6 / float64(se.msgExtractCount)
				fmt.Printf("  MSG extract:   %d • %.1fms\n", se.msgExtractCount, avg)
			}
		}
		// PDF extraction summary
		if atomic.LoadInt64(&se.pdfProcessed) > 0 || atomic.LoadInt64(&se.pdfSkippedBudget) > 0 {
			fmt.Printf("  PDF extracted: %d • skipped (budget): %d • pages truncated: %d\n",
				atomic.LoadInt64(&se.pdfProcessed),
				atomic.LoadInt64(&se.pdfSkippedBudget),
				atomic.LoadInt64(&pdfPagesTruncated))
		}
	}

	return outcome, nil
}

// formatNumber formats a number with thousands separators
func formatNumber(n int) string {
	str := fmt.Sprintf("%d", n)
	if len(str) <= 3 {
		return str
	}

	var result strings.Builder
	for i, digit := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result.WriteString(",")
		}
		result.WriteRune(digit)
	}

	return result.String()
}

// GetPDFStatsDetailed returns PDF counters including truncated page count for UI/metrics.
func (se *SearchEngine) GetPDFStatsDetailed() (processed int64, skippedBudget int64, truncatedPages int64) {
	return atomic.LoadInt64(&se.pdfProcessed), atomic.LoadInt64(&se.pdfSkippedBudget), atomic.LoadInt64(&pdfPagesTruncated)
}

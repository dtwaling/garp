package search

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
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

// SearchResult represents a file that matches all search criteria
type SearchResult struct {
	FilePath     string
	FileSize     int64
	Excerpts     []string // ANSI-highlighted, for TUI display
	RawExcerpts  []string // plain text before highlighting -- for machine-readable output (--json, --plain)
	StartLines   []int    // 1-based start line per excerpt (parallel to RawExcerpts); 0 = unknown
	CleanContent string
	EmailDate    string
	EmailSubject string
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
	Registry          *ExtractorRegistry
	Distance          int
	Silent            bool
	HeavyConcurrency  int
	FilterWorkers     int
	FileTimeoutBinary time.Duration
	MaxExcerpts       int

	// StartDir, if non-empty, sets the root directory for file walks.
	// When empty, the walk uses the current working directory.
	StartDir string
	// PathScope, if non-empty, restricts file walks to paths whose relative
	// path matches at least one simple glob pattern (e.g., "*/backend/*").
	PathScope []string

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
}

// NewSearchEngine creates a new search engine instance
func NewSearchEngine(searchWords, excludeWords []string, fileTypes []string, includeCode bool, heavyConcurrency int, fileTimeoutBinary int) *SearchEngine {
	return &SearchEngine{
		SearchWords:       searchWords,
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
	if !se.Silent {
		fmt.Printf("Finding files with '%s'...\n", se.SearchWords[0])
	}
	candidateFiles, err := FindFilesWithFirstWordProgress(se.SearchWords, se.FileTypes, se.FilterWorkers, func(processed, total int, path string) {
		if se.OnProgress != nil {
			se.OnProgress("discovery", processed, total, path)
		}
	}, se.StartDir, se.PathScope)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to find files with first word: %w", err)
	}
	total := len(candidateFiles)
	if len(candidateFiles) == 0 {
		if !se.Silent {
			fmt.Printf("No files found containing '%s'\n", se.SearchWords[0])
		}
		return nil, total, nil
	}
	if !se.Silent {
		fmt.Printf("Found %s files containing '%s'\n", formatNumber(len(candidateFiles)), se.SearchWords[0])
	}
	return candidateFiles, total, nil
}

// FilterCandidates filters candidates for all words and excludes
func (se *SearchEngine) FilterCandidates(candidateFiles []string, total int, startTime time.Time) ([]string, error) {
	if !se.Silent {
		fmt.Println("Filtering for files containing ALL words...")
	}

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
			fmt.Printf("Excluding types: %s\n", strings.Join(extExcludes, ", "))
		}
		if len(wordExcludes) > 0 {
			fmt.Printf("Excluding words: %s\n", strings.Join(wordExcludes, ", "))
		}
	}

	// Results and synchronization
	var matchingFiles []string
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
	jobs := make(chan string, workers*4)
	var wg sync.WaitGroup

	handleOne := func(filePath string) bool {
		// Check for excluded extensions
		ext := filepath.Ext(filePath)
		if slices.Contains(extExcludes, ext) {
			return false
		}

		// Consolidated prefilter for text files: single streaming pass on rarest-two or both terms
		if !IsBinaryFormat(filePath) && len(se.SearchWords) >= 2 {
			termsToCheck := se.SearchWords
			if len(se.SearchWords) >= 3 {
				terms := make([]string, len(se.SearchWords))
				copy(terms, se.SearchWords)
				sort.Slice(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
				termsToCheck = terms[:2]
			}
			found, decided := StreamContainsAllWordsDecided(filePath, termsToCheck)
			if decided && !found {
				return false
			}
		}

		// Check if file contains all search words
		hasAllWords := true
		if len(se.SearchWords) > 1 {
			if IsBinaryFormat(filePath) {
				ext := filepath.Ext(filePath)

				// Bounded streaming prefilter for supported binary types.
				// EML/MSG use a smaller cap; PDFs and others use a conservative default.
				cap := int64(1024 * 1024)
				if strings.EqualFold(ext, ".eml") || strings.EqualFold(ext, ".msg") {
					cap = int64(256 * 1024)
				}
				startPF := time.Now()
				found, decided := BinaryStreamingPrefilterDecided(filePath, se.SearchWords, cap)
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
					content, _, err := GetFileContent(filePath)
					if err != nil {
						if !se.Silent {
							fmt.Printf("Warning: Error reading file %s: %v\n", filePath, err)
						}
						return false
					}
					var extractedText string
					var extErr error
					startXT := time.Now()
					cm.Acquire()
					err = cm.ExecuteWithTimeout(func() {
						extractedText, extErr = extractor.ExtractText([]byte(content))
					}, se.FileTimeoutBinary)
					cm.Release()
					durXT := time.Since(startXT)
					switch strings.ToLower(ext) {
					case ".eml":
						atomic.AddInt64(&se.emlExtractCount, 1)
						atomic.AddInt64(&se.emlExtractDurNanos, durXT.Nanoseconds())
					case ".msg":
						atomic.AddInt64(&se.msgExtractCount, 1)
						atomic.AddInt64(&se.msgExtractDurNanos, durXT.Nanoseconds())
					}
					if err != nil || extErr != nil {
						if !se.Silent {
							if extErr != nil {
								// underlying extractor error
								fmt.Printf("Warning: Error extracting text from %s: %v\n", filePath, extErr)
							} else {
								fmt.Printf("Warning: Extraction timeout for %s\n", filePath)
							}
						}
						return false
					}
					hasAllWords = CheckTextContainsAllWords(CleanContent(extractedText), se.SearchWords, se.Distance)
				} else {
					if !se.Silent {
						fmt.Printf("Warning: No extractor for %s\n", ext)
					}
					return false
				}
			} else {
				// Text file: stream+distance
				ok, err := CheckFileContainsAllWords(filePath, se.SearchWords, se.Distance, se.Silent)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error checking file %s: %v\n", filePath, err)
					}
					return false
				}
				hasAllWords = ok
			}
		} else {
			// Single-word presence check
			word := se.SearchWords[0]
			if IsBinaryFormat(filePath) {
				ext := filepath.Ext(filePath)
				// Run bounded prefilter for binary types before extraction.
				cap := int64(1024 * 1024)
				if strings.EqualFold(ext, ".eml") || strings.EqualFold(ext, ".msg") {
					cap = int64(256 * 1024)
				}
				foundPF, decidedPF := BinaryStreamingPrefilterDecided(filePath, []string{word}, cap)
				// Decided negative => safe skip
				if decidedPF && !foundPF {
					return false
				}

				// Bounded extraction fallback under semaphore + timeout.
				rawContent, _, err := GetFileContent(filePath)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error reading file %s: %v\n", filePath, err)
					}
					return false
				}
				if extractor, exists := se.Registry.GetExtractor(ext); exists {
					var extractedText string
					var extErr error
					startXT := time.Now()
					cm.Acquire()
					err = cm.ExecuteWithTimeout(func() {
						extractedText, extErr = extractor.ExtractText([]byte(rawContent))
					}, se.FileTimeoutBinary)
					cm.Release()
					durXT := time.Since(startXT)
					switch strings.ToLower(ext) {
					case ".eml":
						atomic.AddInt64(&se.emlExtractCount, 1)
						atomic.AddInt64(&se.emlExtractDurNanos, durXT.Nanoseconds())
					case ".msg":
						atomic.AddInt64(&se.msgExtractCount, 1)
						atomic.AddInt64(&se.msgExtractDurNanos, durXT.Nanoseconds())
					}
					if err != nil || extErr != nil {
						if !se.Silent {
							if extErr != nil {
								fmt.Printf("Warning: Error extracting text from %s: %v\n", filePath, extErr)
							} else {
								fmt.Printf("Warning: Extraction timeout for %s\n", filePath)
							}
						}
						return false
					}
					hasAllWords = CheckTextContainsAllWords(CleanContent(extractedText), []string{word}, se.Distance)
				} else {
					if !se.Silent {
						fmt.Printf("Warning: No extractor for %s\n", ext)
					}
					return false
				}
			} else {
				ok, err := CheckFileContainsAllWords(filePath, []string{word}, se.Distance, se.Silent)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error checking file %s: %v\n", filePath, err)
					}
					return false
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
			// For binary files, extract text (gated and timed)
			rawContent, _, err := GetFileContent(filePath)
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error reading file %s: %v\n", filePath, err)
				}
				return false
			}
			ext := filepath.Ext(filePath)
			if extractor, exists := se.Registry.GetExtractor(ext); exists {
				var out string
				var extErr error
				cm.Acquire()
				err := cm.ExecuteWithTimeout(func() {
					out, extErr = extractor.ExtractText([]byte(rawContent))
				}, se.FileTimeoutBinary)
				cm.Release()
				if err != nil || extErr != nil {
					if !se.Silent {
						if extErr != nil {
							fmt.Printf("Warning: Error extracting text from %s: %v\n", filePath, extErr)
						} else {
							fmt.Printf("Warning: Extraction timeout for %s\n", filePath)
						}
					}
					return false
				}
				// Compute exclude words from extracted text (cleaned)
				hasExcludeWords = CheckTextContainsExcludeWords(CleanContent(out), wordExcludes)
			} else {
				if !se.Silent {
					fmt.Printf("Warning: No extractor for %s\n", ext)
				}
				return false
			}
		} else if len(wordExcludes) > 0 {
			ok2, err := CheckFileContainsExcludeWords(filePath, wordExcludes)
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error checking exclude words in %s: %v\n", filePath, err)
				}
				return false
			}
			hasExcludeWords = ok2
		}

		if hasExcludeWords {
			return false
		}

		return true
	}

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for filePath := range jobs {
				maybePaceForMemory()
				matched := handleOne(filePath)

				// Append results if matched
				if matched {
					mu.Lock()
					matchingFiles = append(matchingFiles, filePath)
					mu.Unlock()
				}

				// Atomic progress update
				cur := atomic.AddInt64(&processed, 1)
				if se.OnProgress != nil {
					se.OnProgress("processing", int(cur), total, filePath)
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
	for _, p := range candidateFiles {
		maybePaceForMemory()
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	return matchingFiles, nil
}

// ExtractAndBuildResults extracts content and builds search results
func (se *SearchEngine) ExtractAndBuildResults(matchingFiles []string) ([]SearchResult, error) {
	results := make([]SearchResult, 0, len(matchingFiles))
	cm := NewConcurrencyManager(se.HeavyConcurrency)

	for _, filePath := range matchingFiles {
		maybePaceForMemory()
		var content string
		var fileSize int64
		var err error
		var emailDate, emailSubject string

		if IsBinaryFormat(filePath) {
			// For binary files, extract text
			rawContent, size, err := GetFileContent(filePath)
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error reading file %s: %v\n", filePath, err)
				}
				continue
			}
			fileSize = size
			ext := filepath.Ext(filePath)

			// Best-effort email metadata for EML/MSG from raw headers (without heavy parsing)
			if strings.EqualFold(ext, ".eml") || strings.EqualFold(ext, ".msg") {
				if m := regexp.MustCompile(`(?mi)^Date:\s*(.+)$`).FindStringSubmatch(rawContent); m != nil {
					emailDate = strings.TrimSpace(m[1])
				}
				if m := regexp.MustCompile(`(?mi)^Subject:\s*(.+)$`).FindStringSubmatch(rawContent); m != nil {
					emailSubject = strings.TrimSpace(m[1])
				}
			}

			if extractor, exists := se.Registry.GetExtractor(ext); exists {
				err = cm.ExecuteWithTimeout(func() {
					content, err = extractor.ExtractText([]byte(rawContent))
				}, se.FileTimeoutBinary)
				if err != nil {
					if !se.Silent {
						fmt.Printf("Warning: Error extracting text from %s: %v\n", filePath, err)
					}
					continue
				}
				if strings.EqualFold(ext, ".pdf") {
					atomic.AddInt64(&se.pdfProcessed, 1)
				}
			} else {
				if !se.Silent {
					fmt.Printf("Warning: No extractor for %s\n", ext)
				}
				continue
			}
		} else {
			content, fileSize, err = GetFileContent(filePath)
			if err != nil {
				if !se.Silent {
					fmt.Printf("Warning: Error reading file %s: %v\n", filePath, err)
				}
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
		if budget < 240 {
			budget = 240
		}
		if budget > 600 {
			budget = 600
		}

		// Map the character budget to a context limit for excerpt generation (roughly half).
		SetExcerptContextLimit(budget / 2)

		maxExcerpts := se.MaxExcerpts
		if maxExcerpts <= 0 {
			maxExcerpts = 1
		}

		var excerpts []string
		if isCode {
			excerpts = ExtractMeaningfulExcerptsCode(cleanContent, se.SearchWords, maxExcerpts)
		} else {
			excerpts = ExtractMeaningfulExcerpts(cleanContent, se.SearchWords, maxExcerpts)
		}

		// If excerpts are very short (e.g., only a single terse sentence), expand the first excerpt
		// by pulling in neighboring sentences to provide more context. This helps fill the UI box
		// when there is little content returned.
		if len(excerpts) == 1 && len(excerpts[0]) < 160 {
			// Local helper to expand context from surrounding sentences up to a target length.
			expandShort := func(clean, ex string, terms []string, target int) string {
				sents := splitIntoSentences(clean)
				if len(sents) == 0 {
					return ex
				}
				// Find a sentence that contains the excerpt, or the first that contains any term.
				best := -1
				for i, s := range sents {
					if strings.Contains(s, ex) {
						best = i
						break
					}
					if best == -1 && containsAnySearchTerm(s, terms) {
						best = i
					}
				}
				if best == -1 {
					return ex
				}

				out := []string{strings.TrimSpace(sents[best])}
				l, r := best-1, best+1

				// Grow context outward until we reach the target length or run out of sentences.
				for (l >= 0 || r < len(sents)) && len(strings.Join(out, " ")) < target {
					if l >= 0 {
						left := strings.TrimSpace(sents[l])
						if left != "" {
							out = append([]string{left}, out...)
						}
						l--
					}
					if r < len(sents) && len(strings.Join(out, " ")) < target {
						right := strings.TrimSpace(sents[r])
						if right != "" {
							out = append(out, right)
						}
						r++
					}
				}

				merged := strings.Join(out, " ")
				// Normalize whitespace
				merged = strings.Join(strings.Fields(merged), " ")
				return merged
			}

			expanded := expandShort(cleanContent, excerpts[0], se.SearchWords, 600)
			if len(expanded) > len(excerpts[0]) {
				excerpts[0] = expanded
			}
		}

		// Highlight search terms in excerpts
		highlightedExcerpts := make([]string, len(excerpts))
		for i, excerpt := range excerpts {
			highlightedExcerpts[i] = HighlightTerms(excerpt, se.SearchWords)
		}

		// Resolve 1-based start line per excerpt by anchoring it back to the raw content.
		// Only text/code files have stable source lines; binary/extracted formats (PDF, DOCX,
		// email) do not, so we leave their line numbers unknown (nil/0).
		var startLines []int
		if !IsBinaryFormat(filePath) {
			startLines = computeExcerptLines(content, excerpts, se.SearchWords, se.Distance)
		}

		result := SearchResult{
			FilePath:     filePath,
			FileSize:     fileSize,
			Excerpts:     highlightedExcerpts,
			RawExcerpts:  excerpts,
			StartLines:   startLines,
			CleanContent: boundedClean,
			EmailDate:    emailDate,
			EmailSubject: emailSubject,
		}

		results = append(results, result)
	}
	return results, nil
}

// Execute performs the complete search operation
func (se *SearchEngine) Execute() ([]SearchResult, error) {
	startTime := time.Now()

	// Emit initial progress with unknown total (0); discovery will update it
	if se.OnProgress != nil {
		se.OnProgress("discovery", 0, 0, "")
	}

	// Step 2: Discover candidates
	candidateFiles, total, err := se.DiscoverCandidates(0)
	if err != nil {
		return nil, err
	}
	if candidateFiles == nil {
		if se.OnProgress != nil {
			se.OnProgress("discovery", 0, 0, "")
		}
		return nil, nil
	}

	// Step 3: Filter candidates
	matchingFiles, err := se.FilterCandidates(candidateFiles, total, startTime)
	if err != nil {
		return nil, err
	}
	if len(matchingFiles) == 0 {
		if !se.Silent {
			fmt.Println("No files found containing all search terms.")
		}
		return nil, nil
	}

	// Step 4: Extract and build results
	if !se.Silent {
		fmt.Printf("Found %s files containing all words. Extracting content...\n", formatNumber(len(matchingFiles)))
	}

	results, err := se.ExtractAndBuildResults(matchingFiles)
	if err != nil {
		return nil, err
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

	return results, nil
}

// GetAbsolutePath returns the absolute path for a file
func GetAbsolutePath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}

	abs, err := filepath.Abs(filePath)
	if err != nil {
		return filePath
	}

	return abs
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

// GetPDFStats returns PDF processing counters: processed and skipped due to budget.
func (se *SearchEngine) GetPDFStats() (processed int64, skippedBudget int64) {
	return atomic.LoadInt64(&se.pdfProcessed), atomic.LoadInt64(&se.pdfSkippedBudget)
}

// GetPDFStatsDetailed returns PDF counters including truncated page count for UI/metrics.
func (se *SearchEngine) GetPDFStatsDetailed() (processed int64, skippedBudget int64, truncatedPages int64) {
	return atomic.LoadInt64(&se.pdfProcessed), atomic.LoadInt64(&se.pdfSkippedBudget), atomic.LoadInt64(&pdfPagesTruncated)
}

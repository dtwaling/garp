package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchEngineResourceBounds(t *testing.T) {
	for _, value := range []int{math.MinInt, -1, 0, 1, 2, MaxHeavyConcurrency, MaxWorkers, MaxFileTimeoutBinary, MaxFileTimeoutBinary + 1, math.MaxInt} {
		se := NewSearchEngineWithWorkers(nil, nil, nil, false, value, value, value)
		wantWorkers := min(value, MaxWorkers)
		if value <= 0 {
			wantWorkers = 2
		}
		if se.FilterWorkers != wantWorkers {
			t.Fatalf("workers(%d) = %d, want %d", value, se.FilterWorkers, wantWorkers)
		}
		wantHeavy := max(1, min(value, MaxHeavyConcurrency))
		if se.HeavyConcurrency != wantHeavy {
			t.Fatalf("heavy(%d) = %d, want %d", value, se.HeavyConcurrency, wantHeavy)
		}
		wantTimeout := time.Duration(max(0, min(value, MaxFileTimeoutBinary))) * time.Millisecond
		if se.FileTimeoutBinary != wantTimeout {
			t.Fatalf("timeout(%d) = %v, want %v", value, se.FileTimeoutBinary, wantTimeout)
		}
		cm := NewConcurrencyManager(value)
		if cap(cm.sem) != wantHeavy {
			t.Fatalf("semaphore(%d) capacity = %d, want %d", value, cap(cm.sem), wantHeavy)
		}
		if err := cm.ExecuteWithTimeout(func(context.Context) {}, time.Second); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMutatedEngineResourceBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{math.MinInt, 0, 1, MaxWorkers, MaxWorkers + 1, math.MaxInt} {
		se := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 1000)
		se.Silent = true
		se.FilterWorkers = workers
		se.HeavyConcurrency = math.MaxInt
		matches, err := se.FilterCandidates([]string{path}, 1, time.Now())
		if err != nil || len(matches) != 1 {
			t.Fatalf("workers %d: matches=%v error=%v", workers, matches, err)
		}
		if cap(se.extractionManager().sem) != MaxHeavyConcurrency {
			t.Fatal("mutated heavy concurrency was not bounded")
		}
	}
}

type deadlineObservingExtractor struct{ remaining time.Duration }

func (e *deadlineObservingExtractor) ExtractText([]byte) (string, error) { return "alpha", nil }

func (e *deadlineObservingExtractor) ExtractReaderAt(r io.ReaderAt, _, _ int64) (string, error) {
	deadline, ok := r.(contextReaderAt).ctx.Deadline()
	if !ok {
		return "", errors.New("missing deadline")
	}
	e.remaining = time.Until(deadline)
	return "alpha", nil
}

func TestMutatedEngineTimeoutBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.pdf")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	se := NewSearchEngine(nil, nil, nil, false, 1, 1000)
	se.FileTimeoutBinary = time.Duration(math.MaxInt64)
	extractor := &deadlineObservingExtractor{}
	text, _, _, _, timedOut, readErr, extractErr := se.extractBinaryFile(se.extractionManager(), path, ".pdf", extractor, false)
	if text != "alpha" || timedOut || readErr != nil || extractErr != nil {
		t.Fatalf("extraction: %q %v %v %v", text, timedOut, readErr, extractErr)
	}
	if extractor.remaining <= 0 || extractor.remaining > time.Duration(MaxFileTimeoutBinary)*time.Millisecond {
		t.Fatalf("unbounded deadline: %v", extractor.remaining)
	}
}

// Gates, rather than sleeps or process-wide goroutine counts, track precisely
// which extraction workers are alive. Deadlines are only bounded wait guards.
type timeoutGatedExtractor struct {
	entered chan struct{}
	release chan struct{}
	active  atomic.Int64
	calls   atomic.Int64
}

func (e *timeoutGatedExtractor) ExtractText([]byte) (string, error) {
	e.calls.Add(1)
	e.active.Add(1)
	defer e.active.Add(-1)
	e.entered <- struct{}{}
	<-e.release
	return "alpha beta", nil
}

type timeoutGatedReaderAtExtractor struct {
	*timeoutGatedExtractor
	files chan *os.File
}

func (e *timeoutGatedReaderAtExtractor) ExtractReaderAt(r io.ReaderAt, _, _ int64) (string, error) {
	e.files <- r.(contextReaderAt).r.(*os.File)
	return e.ExtractText(nil)
}

func waitExtractionSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("extraction did not reach expected gate")
	}
}

func waitExtractionIdle(t *testing.T, cm *ConcurrencyManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < cap(cm.sem); i++ {
		select {
		case cm.sem <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("extraction worker did not release its slot")
		}
	}
	for i := 0; i < cap(cm.sem); i++ {
		cm.Release()
	}
}

func TestTimedOutExtractionsStayBounded(t *testing.T) {
	for _, readerAt := range []bool{false, true} {
		name := "legacy"
		if readerAt {
			name = "reader-at"
		}
		t.Run(name, func(t *testing.T) {
			const bound = 2
			path := filepath.Join(t.TempDir(), "input.pdf")
			if err := os.WriteFile(path, []byte("alpha beta"), 0o600); err != nil {
				t.Fatal(err)
			}
			gate := &timeoutGatedExtractor{entered: make(chan struct{}, bound), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(gate.release) })
			ra := &timeoutGatedReaderAtExtractor{gate, make(chan *os.File, bound)}
			var extractor Extractor = gate
			if readerAt {
				extractor = ra
			}
			engine := NewSearchEngine(nil, nil, nil, false, bound, 100)
			cm := engine.extractionManager()
			for i := 0; i < bound; i++ {
				done := make(chan struct{})
				go func() {
					defer close(done)
					text, _, _, _, timedOut, readErr, extractErr := engine.extractBinaryFile(cm, path, ".pdf", extractor, false)
					if !timedOut || text != "" || readErr != nil || extractErr != nil {
						t.Errorf("timeout result: %q, %v, %v, %v", text, timedOut, readErr, extractErr)
					}
				}()
				waitExtractionSignal(t, gate.entered)
				waitExtractionSignal(t, done)
			}
			// Saturated admission must time out without starting another worker
			// or even attempting to open this nonexistent path.
			for i := 0; i < 4; i++ {
				_, _, _, _, timedOut, readErr, _ := engine.extractBinaryFile(cm, path+".missing", ".pdf", extractor, false)
				if !timedOut || readErr != nil {
					t.Fatalf("admission: timeout=%v, read=%v", timedOut, readErr)
				}
			}
			if gate.calls.Load() != bound || gate.active.Load() != bound || len(cm.sem) != bound {
				t.Fatalf("workers exceeded bound: calls=%d active=%d slots=%d", gate.calls.Load(), gate.active.Load(), len(cm.sem))
			}
			var files []*os.File
			if readerAt {
				for i := 0; i < bound; i++ {
					file := <-ra.files
					if _, err := file.Stat(); err != nil {
						t.Fatalf("active handle: %v", err)
					}
					files = append(files, file)
				}
			}
			release.Do(func() { close(gate.release) })
			waitExtractionIdle(t, cm)
			if gate.active.Load() != 0 {
				t.Fatalf("active workers leaked: %d", gate.active.Load())
			}
			for _, file := range files {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("finished handle not closed: %v", err)
				}
			}
			if err := cm.ExecuteWithTimeout(func(context.Context) {}, time.Second); err != nil {
				t.Fatalf("slot not reusable: %v", err)
			}
		})
	}
}

func TestExtractionBoundPersistsAcrossStages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.pdf")
	if err := os.WriteFile(path, []byte("opaque binary fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := &timeoutGatedExtractor{entered: make(chan struct{}, 1), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	engine := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 100)
	engine.Silent = true
	engine.Registry.extractors["pdf"] = gate
	done := make(chan struct{})
	go func() {
		defer close(done)
		matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
		if err != nil || len(matches) != 0 {
			t.Errorf("filter: %v, %v", matches, err)
		}
	}()
	waitExtractionSignal(t, gate.entered)
	waitExtractionSignal(t, done)
	for i := 0; i < 2; i++ {
		results, err := engine.ExtractAndBuildResults([]candidateMatch{{filePath: path}})
		if err != nil || len(results) != 0 {
			t.Fatalf("build: %v, %v", results, err)
		}
		matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
		if err != nil || len(matches) != 0 {
			t.Fatalf("repeat filter: %v, %v", matches, err)
		}
	}
	if gate.calls.Load() != 1 || gate.active.Load() != 1 {
		t.Fatalf("stage reset admission: calls=%d active=%d", gate.calls.Load(), gate.active.Load())
	}
	release.Do(func() { close(gate.release) })
	waitExtractionIdle(t, engine.extractionManager())
	if gate.active.Load() != 0 {
		t.Fatal("worker remained active")
	}
}

func TestExecuteWithTimeoutCancellationAndPanicCleanup(t *testing.T) {
	cm := NewConcurrencyManager(1)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := cm.ExecuteWithTimeout(func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
		}, 100*time.Millisecond)
		// Completion and deadline may become ready together.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("timeout: %v", err)
		}
	}()
	waitExtractionSignal(t, entered)
	waitExtractionSignal(t, cancelled)
	waitExtractionSignal(t, done)
	waitExtractionIdle(t, cm)
	if err := cm.ExecuteWithTimeout(func(context.Context) { panic("test panic") }, time.Second); err == nil {
		t.Fatal("panic reported as success")
	}
	waitExtractionIdle(t, cm)
	var calls atomic.Int64
	if err := cm.ExecuteWithTimeout(func(context.Context) { calls.Add(1) }, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired deadline: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("started worker after deadline")
	}
	if len(cm.sem) != 0 {
		t.Fatal("expired deadline leaked slot")
	}
}

type delayedContextExtractor struct {
	delay time.Duration
}

func (e *delayedContextExtractor) ExtractText([]byte) (string, error) {
	return "alpha beta", nil
}

func (e *delayedContextExtractor) ExtractReaderAt(r io.ReaderAt, _, _ int64) (string, error) {
	ctx := r.(contextReaderAt).ctx
	select {
	case <-time.After(e.delay):
		return "alpha beta", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestAdmissionWaitDoesNotConsumeExtractionDeadline(t *testing.T) {
	dir := t.TempDir()
	candidates := []string{
		filepath.Join(dir, "first.pdf"),
		filepath.Join(dir, "second.pdf"),
		filepath.Join(dir, "third.pdf"),
		filepath.Join(dir, "fourth.pdf"),
	}
	for _, p := range candidates {
		if err := os.WriteFile(p, []byte("alpha beta"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// With 1 heavy slot, 4 filter workers, and a 250ms timeout, four simultaneous
	// 150ms extractions take ~600ms wall-clock total (and the 3rd/4th wait >=300ms
	// before admission), but each slot turn finishes within 250ms. Stage 2 must
	// retain all four candidates.
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta"}, nil, nil, false, 1, 250, len(candidates))
	engine.Silent = true
	engine.Registry.extractors["pdf"] = &delayedContextExtractor{delay: 150 * time.Millisecond}

	matches, err := engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatalf("FilterCandidates failed: %v", err)
	}
	if len(matches) != len(candidates) {
		t.Fatalf("matched %d candidates, want %d (queued candidates timed out under multi-worker contention)", len(matches), len(candidates))
	}
}

func TestRankingComparatorUsesEveryTier(t *testing.T) {
	base := rankingKey{
		filePath:         "z.php",
		score:            10,
		termCount:        2,
		pathMatchCount:   1,
		boundaryQuality:  1,
		spanLen:          20,
		topQualityChunks: 1,
	}
	tests := []struct {
		name   string
		better rankingKey
		worse  rankingKey
	}{
		{name: "score", better: rankingKey{score: 11}, worse: rankingKey{score: 10}},
		{name: "term count", better: rankingKey{score: 10, termCount: 3}, worse: rankingKey{score: 10, termCount: 2}},
		{
			name:   "path match count",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 2},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1},
		},
		{
			name:   "boundary quality",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 2},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1},
		},
		{
			name:   "span length",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1, spanLen: 19},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1, spanLen: 20},
		},
		{name: "top quality chunks", better: base, worse: rankingKey{
			filePath:         base.filePath,
			score:            base.score,
			termCount:        base.termCount,
			pathMatchCount:   base.pathMatchCount,
			boundaryQuality:  base.boundaryQuality,
			spanLen:          base.spanLen,
			topQualityChunks: 0,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.better.filePath = base.filePath
			tt.worse.filePath = base.filePath
			if !rankingBetter(tt.better, tt.worse) {
				t.Fatalf("%s tier did not rank higher", tt.name)
			}
			if rankingBetter(tt.worse, tt.better) {
				t.Fatalf("%s tier comparison is not antisymmetric", tt.name)
			}
		})
	}
}

func TestRankingComparatorNormalizesAndOrdersPaths(t *testing.T) {
	results := []SearchResult{
		{FilePath: `dir\b.php`},
		{FilePath: "dir/b.php"},
		{FilePath: "dir/a.php"},
		{FilePath: "dir/A.php"},
	}
	sortSearchResults(results)

	got := make([]string, len(results))
	for i := range results {
		got[i] = results[i].FilePath
	}
	want := []string{"dir/A.php", "dir/a.php", "dir/b.php", `dir\b.php`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("path order = %q, want %q", got, want)
	}
}

func TestRankingComparatorUsesRootOrderThenCanonicalPath(t *testing.T) {
	base := rankingKey{filePath: "same.txt", score: 10, termCount: 1}
	firstRoot := base
	firstRoot.rootOrder = 0
	firstRoot.canonicalPath = "z-root/same.txt"
	secondRoot := base
	secondRoot.rootOrder = 1
	secondRoot.canonicalPath = "a-root/same.txt"
	if !rankingBetter(firstRoot, secondRoot) || rankingBetter(secondRoot, firstRoot) {
		t.Fatal("supplied root order did not break an equal relative-path tie")
	}

	canonicalFirst := base
	canonicalFirst.canonicalPath = "a-root/same.txt"
	canonicalSecond := base
	canonicalSecond.canonicalPath = "z-root/same.txt"
	if !rankingBetter(canonicalFirst, canonicalSecond) || rankingBetter(canonicalSecond, canonicalFirst) {
		t.Fatal("canonical absolute path did not break the final tie")
	}
}

func TestRankingComparatorRawPathTieIsDeterministicAndTransitive(t *testing.T) {
	want := []string{
		"dir/A.php",
		"dir/a.php",
		`dir\a.php`,
		"dir/b.php",
		`dir\b.php`,
	}

	for i := range want {
		for j := i + 1; j < len(want); j++ {
			a := rankingKey{filePath: want[i]}
			b := rankingKey{filePath: want[j]}
			if !rankingBetter(a, b) {
				t.Fatalf("rankingBetter(%q, %q) = false", want[i], want[j])
			}
			if rankingBetter(b, a) {
				t.Fatalf("rankingBetter(%q, %q) = true", want[j], want[i])
			}
		}
	}

	input := make([]candidateMatch, 0, len(want)*2)
	for i := len(want) - 1; i >= 0; i-- {
		input = append(input,
			candidateMatch{filePath: want[i]},
			candidateMatch{filePath: want[i]},
		)
	}
	for iteration := 0; iteration < 100; iteration++ {
		gotCandidates := sortAndDedupeCandidates(input)
		got := make([]string, len(gotCandidates))
		for i := range gotCandidates {
			got[i] = gotCandidates[i].filePath
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d candidate order = %q, want %q", iteration, got, want)
		}
	}
}

func TestSortAndDedupeCandidatesUsesSharedComparator(t *testing.T) {
	matches := []candidateMatch{
		{filePath: "same.php", score: 10, termCount: 2, spanLen: 20},
		{filePath: "same.php", score: 10, termCount: 2, pathMatchCount: 1, spanLen: 20},
		{filePath: "other.php", score: 10, termCount: 2, spanLen: 20},
	}

	got := sortAndDedupeCandidates(matches)
	if len(got) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(got))
	}
	if got[0].filePath != "same.php" || got[0].pathMatchCount != 1 {
		t.Fatalf("first candidate = %+v, want better same.php candidate", got[0])
	}
}

func TestEngineCanonicalTerms(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "service.txt")
	if err := os.WriteFile(fixture, []byte("ms-option-api"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	engine := NewSearchEngine(
		[]string{"ms-**-api", "MS-*-API"},
		nil,
		[]string{"-g", "*.txt"},
		false,
		1,
		1000,
	)
	engine.StartDir = root
	engine.Silent = true

	if got := engine.SearchWords; len(got) != 2 || got[0] != "ms-**-api" || got[1] != "MS-*-API" {
		t.Fatalf("SearchWords = %q, want authoritative caller input", got)
	}

	matches, err := engine.FilterCandidates([]string{fixture}, 1, time.Now())
	if err != nil {
		t.Fatalf("FilterCandidates() error: %v", err)
	}
	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatalf("ExtractAndBuildResults() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	if got := results[0]; got.Score != len("ms-*-api") ||
		got.TermCount != 1 ||
		len(got.MatchedTerms) != 1 ||
		got.MatchedTerms[0] != "ms-*-api" {
		t.Errorf("metadata = score %d, term count %d, terms %q; want %d, 1, [ms-*-api]",
			got.Score, got.TermCount, got.MatchedTerms, len("ms-*-api"))
	}
}

func TestEngineSearchWordsRemainAuthoritative(t *testing.T) {
	root := t.TempDir()
	upper := filepath.Join(root, "upper.txt")
	lower := filepath.Join(root, "lower.txt")
	if err := os.WriteFile(upper, []byte("API"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lower, []byte("api"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"unused"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.SearchWords = []string{"API"}
	engine.CaseSensitive = true
	engine.StartDir = root
	engine.Silent = true
	engine.PathOnly = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].FilePath != upper {
		t.Fatalf("modified SearchWords results = %#v", results)
	}
}

func TestEngineFilterCandidates(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"all.txt":     "alpha beta gamma",
		"relaxed.txt": "alpha gamma",
		"primary.txt": "alpha only",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tests := []struct {
		name      string
		strict    bool
		wantFiles int
	}{
		{
			name:      "relaxed matching retains partial ranked candidates",
			wantFiles: 2,
		},
		{
			name:      "strict matching requires every term",
			strict:    true,
			wantFiles: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewSearchEngineWithWorkers(
				[]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1,
			)
			engine.StartDir = root
			engine.Distance = 100
			engine.Silent = true
			engine.Strict = tt.strict

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != tt.wantFiles {
				t.Fatalf("result count = %d, want %d", len(results), tt.wantFiles)
			}
			byFile := make(map[string]SearchResult, len(results))
			for _, result := range results {
				byFile[filepath.Base(result.FilePath)] = result
			}
			all, ok := byFile["all.txt"]
			if !ok || all.Score != 14 || all.TermCount != 3 || len(all.MatchedTerms) != 3 || all.SpanLength <= 0 {
				t.Fatalf("all.txt metadata = (score %d, term count %d, terms %v, span %d), want score 14, 3 terms, and positive span", all.Score, all.TermCount, all.MatchedTerms, all.SpanLength)
			}
			if tt.strict {
				if _, found := byFile["relaxed.txt"]; found {
					t.Fatal("strict matching retained partial candidate")
				}
			} else if relaxed, found := byFile["relaxed.txt"]; !found || relaxed.Score != 10 || relaxed.TermCount != 2 || len(relaxed.MatchedTerms) != 2 || relaxed.SpanLength <= 0 {
				t.Fatalf("relaxed.txt metadata = (score %d, term count %d, terms %v, span %d), want score 10, 2 terms, and positive span", relaxed.Score, relaxed.TermCount, relaxed.MatchedTerms, relaxed.SpanLength)
			}
		})
	}
}

func TestEnginePathOnlySkipsExcerptsAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"all.txt":     "alpha beta gamma first cluster. alpha beta gamma second cluster.",
		"relaxed.txt": "alpha gamma",
		"primary.txt": "alpha only",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	engine := NewSearchEngineWithWorkers(
		[]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1,
	)
	engine.StartDir = root
	engine.Distance = 100
	engine.Silent = true
	engine.PathOnly = true
	engine.MaxExcerpts = 5

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if got := filepath.Base(results[0].FilePath); got != "all.txt" {
		t.Errorf("results[0] file = %q, want all.txt", got)
	}
	if got := filepath.Base(results[1].FilePath); got != "relaxed.txt" {
		t.Errorf("results[1] file = %q, want relaxed.txt", got)
	}
	for _, r := range results {
		if len(r.Excerpts) != 0 || len(r.RawExcerpts) != 0 || r.CleanContent != "" {
			t.Errorf("PathOnly populated excerpts/content for %s: Excerpts=%d RawExcerpts=%d CleanContent=%d",
				r.FilePath, len(r.Excerpts), len(r.RawExcerpts), len(r.CleanContent))
		}
	}
}

func TestStage3FileGrowthUsesTextPrefix(t *testing.T) {
	const limit int64 = 128
	root := t.TempDir()
	filename := filepath.Join(root, "growing.txt")
	prefix := "header\nline two\nalpha beta inside prefix\n"
	prefix += strings.Repeat(" ", int(limit)-len(prefix))
	if err := os.WriteFile(filename, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, nil, false, 1, 1000)
	engine.StartDir = root
	engine.MaxFileSize = limit
	engine.Strict = true
	engine.Silent = true

	candidates, total, err := engine.DiscoverCandidates(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discovery candidates = %v, want one", candidates)
	}

	suffix := "\nalpha beta OUTSIDE_PREFIX\n"
	if err := os.WriteFile(filename, []byte(prefix+suffix), 0o600); err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("filtered matches = %+v, want one", matches)
	}

	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	result := results[0]
	if result.FileSize != int64(len(prefix+suffix)) {
		t.Errorf("FileSize = %d, want original stat size %d", result.FileSize, len(prefix+suffix))
	}
	if strings.Contains(result.CleanContent, "OUTSIDE_PREFIX") {
		t.Error("CleanContent includes text beyond the active prefix")
	}
	for _, excerpt := range result.RawExcerpts {
		if strings.Contains(excerpt, "OUTSIDE_PREFIX") {
			t.Errorf("excerpt includes text beyond the active prefix: %q", excerpt)
		}
	}
	if len(result.StartLines) == 0 || result.StartLines[0] != 3 {
		t.Errorf("StartLines = %v, want first excerpt on line 3", result.StartLines)
	}
}

func TestStage3DeletedCandidateIsSkipped(t *testing.T) {
	engine, matches, filename := stage3Candidate(t)
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}

	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want deleted candidate skipped", results)
	}
}

func TestStage3UnreadableCandidateIsSkipped(t *testing.T) {
	engine, matches, filename := stage3Candidate(t)
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filename, 0o700); err != nil {
		t.Fatal(err)
	}

	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want replaced candidate skipped", results)
	}
}

type failingStage3Extractor struct{}

func (failingStage3Extractor) ExtractText([]byte) (string, error) {
	return "", errors.New("registered extractor failure")
}

func TestExtractAndBuildResultsFailureBackfill(t *testing.T) {
	for _, tt := range []struct {
		kind, extension string
	}{
		{"open", ".txt"},
		{"extractor", ".pdf"},
		{"zero excerpt", ".txt"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			engine, matches := stage3BackfillFixture(t, tt.extension)
			switch tt.kind {
			case "open":
				if err := os.Remove(matches[0].filePath); err != nil {
					t.Fatal(err)
				}
			case "extractor":
				engine.Registry.extractors["pdf"] = failingStage3Extractor{}
			case "zero excerpt":
				if err := os.WriteFile(matches[0].filePath, []byte("no matching content"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(matches[3].filePath); err != nil {
				t.Fatal(err)
			}
			outcome, err := engine.extractAndBuildResultsDetailed(
				compileQuery(engine.SearchWords, engine.Partial),
				matches,
			)
			if err != nil {
				t.Fatal(err)
			}
			assertStage3BackfillOutcome(t, outcome, matches[1:3])
		})
	}
}

func TestExecuteDetailedMaxResults(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 4; i++ {
		name := filepath.Join(root, "candidate-"+string(rune('0'+i))+".txt")
		if err := os.WriteFile(name, []byte("alpha"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"alpha"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.MaxResults = 2

	outcome, err := engine.ExecuteDetailed()
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matches != 4 || outcome.Returned != 2 || !outcome.Truncated || len(outcome.Results) != 2 {
		t.Fatalf("outcome = %+v, want 4 matches, 2 returned, truncated", outcome)
	}
}

func TestExtractAndBuildResultsFourCandidateSentinel(t *testing.T) {
	engine, matches := stage3BackfillFixture(t, ".txt")
	if err := os.Remove(matches[3].filePath); err != nil {
		t.Fatal(err)
	}

	outcome, err := engine.extractAndBuildResultsDetailed(
		compileQuery(engine.SearchWords, engine.Partial),
		matches,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matches != 4 || outcome.Returned != 2 || !outcome.Truncated {
		t.Fatalf("outcome = %+v, want 4 matches, 2 returned, truncated", outcome)
	}
	for i := range outcome.Results {
		if outcome.Results[i].FilePath != matches[i].filePath {
			t.Fatalf("result %d = %q, want %q", i, outcome.Results[i].FilePath, matches[i].filePath)
		}
	}
}

func TestExecuteDetailedPathOnlyCapDoesNotOpenFiles(t *testing.T) {
	engine := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 1000)
	engine.PathOnly = true
	engine.MaxResults = 2
	engine.Silent = true
	matches := []candidateMatch{
		{filePath: filepath.Join(t.TempDir(), "missing-3.txt"), score: 1, termCount: 1},
		{filePath: filepath.Join(t.TempDir(), "missing-1.txt"), score: 3, termCount: 1},
		{filePath: filepath.Join(t.TempDir(), "missing-2.txt"), score: 2, termCount: 1},
	}

	outcome, err := engine.extractAndBuildResultsDetailed(
		compileQuery(engine.SearchWords, engine.Partial),
		matches,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matches != 3 || outcome.Returned != 2 || !outcome.Truncated {
		t.Fatalf("outcome = %+v, want 3 matches, 2 returned, truncated", outcome)
	}
	if outcome.Results[0].Score != 3 || outcome.Results[1].Score != 2 {
		t.Fatalf("scores = %d, %d; want 3, 2", outcome.Results[0].Score, outcome.Results[1].Score)
	}
}

func stage3BackfillFixture(t *testing.T, firstExtension string) (*SearchEngine, []candidateMatch) {
	t.Helper()
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "candidate-1"+firstExtension),
		filepath.Join(root, "candidate-2.txt"),
		filepath.Join(root, "candidate-3.txt"),
		filepath.Join(root, "candidate-4.txt"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.MaxResults = 2
	matches := make([]candidateMatch, len(paths))
	for i, path := range paths {
		matches[i] = candidateMatch{
			filePath:     path,
			score:        len(paths) - i,
			termCount:    1,
			matchedTerms: []string{"alpha"},
			spanLen:      5,
		}
	}
	return engine, matches
}

func assertStage3BackfillOutcome(t *testing.T, outcome ExecutionResult, want []candidateMatch) {
	t.Helper()
	if outcome.Matches != 3 || outcome.Returned != 2 || !outcome.Truncated {
		t.Fatalf("outcome = %+v, want 3 matches, 2 returned, truncated", outcome)
	}
	if len(outcome.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(outcome.Results), len(want))
	}
	for i := range want {
		if outcome.Results[i].FilePath != want[i].filePath {
			t.Fatalf("result %d = %q, want %q", i, outcome.Results[i].FilePath, want[i].filePath)
		}
	}
}

func stage3Candidate(t *testing.T) (*SearchEngine, []candidateMatch, string) {
	t.Helper()
	root := t.TempDir()
	filename := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(filename, []byte("alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, nil, false, 1, 1000)
	engine.StartDir = root
	engine.Strict = true
	engine.Silent = true
	candidates, total, err := engine.DiscoverCandidates(0)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("filtered matches = %+v, want one", matches)
	}
	return engine, matches, filename
}

func writeMultiRootFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMultiRootDiscoveryAndGlobalRanking(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-root")
	secondRoot := filepath.Join(base, "a-root")
	firstFile := filepath.Join(firstRoot, "a.txt")
	secondFile := filepath.Join(secondRoot, "z.txt")
	writeMultiRootFixture(t, firstFile, "needle")
	writeMultiRootFixture(t, secondFile, "needle")

	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	engine.PathOnly = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].FilePath != firstFile {
		t.Fatalf("global root-relative order = %q, want %q first", results[0].FilePath, firstFile)
	}
}

func TestMultiRootEqualTieUsesSuppliedOrderDeterministically(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-first")
	secondRoot := filepath.Join(base, "a-second")
	firstFile := filepath.Join(firstRoot, "same.txt")
	secondFile := filepath.Join(secondRoot, "same.txt")
	writeMultiRootFixture(t, firstFile, "needle equal content")
	writeMultiRootFixture(t, secondFile, "needle equal content")

	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, pathOnly := range []bool{false, true} {
		for iteration := 0; iteration < 20; iteration++ {
			engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
			engine.StartRoots = roots
			engine.Silent = true
			engine.PathOnly = pathOnly
			engine.MaxResults = 1
			results, err := engine.Execute()
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].FilePath != firstFile {
				t.Fatalf("pathOnly=%v iteration=%d results=%v, want first supplied root %q", pathOnly, iteration, results, firstFile)
			}
		}
	}
}

func TestMultiRootSplitDiscoveryFilterPreservesProvenance(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-first")
	secondRoot := filepath.Join(base, "a-second")
	firstFile := filepath.Join(firstRoot, "same.txt")
	secondFile := filepath.Join(secondRoot, "same.txt")
	writeMultiRootFixture(t, firstFile, "needle equal content")
	writeMultiRootFixture(t, secondFile, "needle equal content")
	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	engine.PathOnly = true
	engine.MaxResults = 1
	candidates, total, err := engine.DiscoverCandidates(2)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].FilePath != firstFile {
		t.Fatalf("split API results = %v, want first supplied root %q", results, firstFile)
	}
}

func TestMultiRootFilterCandidatesRejectsUnknownProvenance(t *testing.T) {
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeMultiRootFixture(t, outside, "needle")
	if _, err := engine.FilterCandidates([]string{outside}, 1, time.Now()); err == nil {
		t.Fatal("multi-root FilterCandidates accepted a path with no root provenance")
	}
}

func TestMultiRootSplitAPIInfersTraversalOnlyChildProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "blocked", "child")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	pathScope := []string{"!blocked/**"}
	roots, err := PrepareStartRootsWithOptions(
		[]string{parent, child},
		StartRootOptions{PathScope: pathScope},
	)
	if err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = pathScope
	engine.Silent = true
	candidates, total, err := engine.DiscoverCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantRankingPath := filepath.Join("blocked", "child", "match.txt")
	if len(matches) != 1 || matches[0].rootOrder != 0 || matches[0].rankingPath != wantRankingPath {
		t.Fatalf("matches = %+v, want collapsed-parent ranking provenance", matches)
	}
}

func TestMultiRootTraversalOnlyOverlapUsesReportedRankingProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	overlap := filepath.Join(child, "ignored", "a.txt")
	parentOnly := filepath.Join(parent, "ignored", "b.txt")
	writeMultiRootFixture(t, overlap, "needle")
	writeMultiRootFixture(t, parentOnly, "needle")
	pathScope := []string{"ignored"}

	var baselinePaths []string
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}

		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		engine.PathOnly = true
		internalCandidates, _, err := engine.discoverCandidates(compileQuery([]string{"needle"}, PartialModeOff), 2, roots)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range internalCandidates {
			if candidate.filePath == overlap &&
				(!candidate.root.Reported || candidate.root.LogicalPath != parent ||
					candidate.rankingRoot.LogicalPath != parent || candidate.rootOrder != 0) {
				t.Fatalf("roots %q internal overlap provenance = %+v, want reported parent", raw, candidate)
			}
		}
		candidates, total, err := engine.DiscoverCandidates(2)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		results, err := engine.ExtractAndBuildResults(matches)
		if err != nil {
			t.Fatal(err)
		}
		gotPaths := make([]string, len(results))
		for index := range results {
			gotPaths[index] = results[index].FilePath
		}
		if baselinePaths == nil {
			baselinePaths = gotPaths
		} else if !reflect.DeepEqual(gotPaths, baselinePaths) {
			t.Fatalf("roots %q result order = %q, want stable %q", raw, gotPaths, baselinePaths)
		}
		foundOverlap := false
		for _, match := range matches {
			if match.filePath != overlap {
				continue
			}
			foundOverlap = true
			wantRankingPath := filepath.Join("c", "ignored", "a.txt")
			if match.rankingPath != wantRankingPath || match.rootOrder != 0 || match.pathMatchCount != 0 {
				t.Fatalf("roots %q overlap match = %+v, want parent ranking %q/order 0/path count 0", raw, match, wantRankingPath)
			}
		}
		if !foundOverlap {
			t.Fatalf("roots %q did not return overlap candidate", raw)
		}
	}
}

func TestMultiRootSplitAPIPathScopeExemptionUsesParentProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "ignored")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored/**"}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true

		candidates, total, err := engine.DiscoverCandidates(1)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parentOrder := 0
		if raw[0] == child {
			parentOrder = 1
		}
		wantRankingPath := filepath.Join("ignored", "match.txt")
		if len(matches) != 1 || matches[0].rootOrder != parentOrder || matches[0].rankingPath != wantRankingPath {
			t.Fatalf("roots=%q matches=%+v, want parent order %d and ranking path %q", raw, matches, parentOrder, wantRankingPath)
		}
	}
}

func TestMultiRootSplitAPIDescendantSymlinkProvenance(t *testing.T) {
	parent := t.TempDir()
	external := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(external, "match.txt"), "needle")
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	roots, err := PrepareStartRootsWithOptions([]string{parent, link}, StartRootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	candidates, total, err := engine.DiscoverCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].rootOrder != 1 || matches[0].rankingPath != "match.txt" {
		t.Fatalf("matches = %+v, want explicit symlink-root provenance", matches)
	}
}

func TestMultiRootAbsoluteGlobRetainsLogicalSymlinkChild(t *testing.T) {
	parent := t.TempDir()
	physicalChild := filepath.Join(parent, "physical")
	logicalChild := filepath.Join(parent, "logical")
	match := filepath.Join(physicalChild, "blocked.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.Symlink(physicalChild, logicalChild); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	fileTypes := []string{"-g", "*.txt"}
	tests := []struct {
		name       string
		segment    string
		ignoreCase bool
	}{
		{name: "case-sensitive", segment: "l*"},
		{name: "ignore-case", segment: "L*", ignoreCase: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			globs := []UserGlob{{
				Pattern:    filepath.Join(parent, test.segment, "blocked.txt"),
				IgnoreCase: test.ignoreCase,
			}}
			run := func(t *testing.T, raw []string) ([]StartRoot, []string) {
				t.Helper()
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{UserGlobs: globs})
				if err != nil {
					t.Fatal(err)
				}
				options := WalkOptions{UserGlobs: globs}
				count, err := GetDocumentFileCountForRoots(fileTypes, roots, nil, options)
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
				engine.StartRoots = roots
				engine.UserGlobs = globs
				engine.Silent = true
				engine.PathOnly = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != 1 {
					t.Fatalf("count/candidates = %d/%v, want one", count, candidates)
				}
				results, err := engine.Execute()
				if err != nil {
					t.Fatal(err)
				}
				paths := make([]string, len(results))
				for index := range results {
					paths[index] = results[index].FilePath
				}
				return roots, paths
			}

			_, childOnly := run(t, []string{logicalChild})
			for _, raw := range [][]string{{parent, logicalChild}, {logicalChild, parent}} {
				roots, combined := run(t, raw)
				reported := ReportedStartRoots(roots)
				if len(roots) != 2 || len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q = %+v, reported = %+v; want parent plus traversal child", raw, roots, reported)
				}
				if len(childOnly) != 1 || len(combined) != 1 {
					t.Fatalf("child/combined paths = %v/%v, want one each", childOnly, combined)
				}
				childInfo, err := os.Stat(childOnly[0])
				if err != nil {
					t.Fatal(err)
				}
				combinedInfo, err := os.Stat(combined[0])
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(childInfo, combinedInfo) {
					t.Fatalf("child/combined results = %q/%q, want same physical file", childOnly, combined)
				}
			}
		})
	}
}

func TestMultiRootTraversalOnlyPhysicalChildProjectsReportedSymlinkPath(t *testing.T) {
	physicalParent := t.TempDir()
	reportedParent := filepath.Join(t.TempDir(), "reported-parent")
	if err := os.Symlink(physicalParent, reportedParent); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	child := filepath.Join(physicalParent, "c")
	firstPhysical := filepath.Join(child, "ignored", "a.txt")
	secondPhysical := filepath.Join(child, "ignored", "b.txt")
	if err := os.MkdirAll(filepath.Join(physicalParent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiRootFixture(t, firstPhysical, "needle c")
	writeMultiRootFixture(t, secondPhysical, "needle c")
	if err := os.WriteFile(filepath.Join(physicalParent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored"}
	wantPaths := []string{
		filepath.Join(reportedParent, "c", "ignored", "a.txt"),
		filepath.Join(reportedParent, "c", "ignored", "b.txt"),
	}
	for _, raw := range [][]string{{reportedParent, child}, {child, reportedParent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != reportedParent {
			t.Fatalf("roots %q reported = %+v, want symlink parent", raw, reported)
		}

		engine := NewSearchEngine([]string{"needle", "c"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Strict = true
		engine.Silent = true
		candidates, total, err := engine.DiscoverCandidates(2)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if strings.Contains(match.rankingPath, "..") ||
				!strings.HasPrefix(filepath.Clean(match.filePath), filepath.Clean(reportedParent)+string(filepath.Separator)) ||
				match.pathMatchCount != 1 || match.rootOrder != 0 {
				t.Fatalf("roots %q projected match = %+v", raw, match)
			}
		}
		results, err := engine.ExtractAndBuildResults(matches)
		if err != nil {
			t.Fatal(err)
		}
		gotPaths := make([]string, len(results))
		for index := range results {
			gotPaths[index] = results[index].FilePath
		}
		if !reflect.DeepEqual(gotPaths, wantPaths) {
			t.Fatalf("roots %q result paths = %q, want reported-root-relative %q", raw, gotPaths, wantPaths)
		}
	}
}

func TestMultiRootCanonicalCandidateDedupPreservesFirstRoot(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	file := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, file, "needle")

	roots, err := PrepareStartRoots([]string{parent, child}, true)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = []string{"**/*.txt"}
	engine.Silent = true
	query := compileQuery([]string{"needle"}, PartialModeOff)
	candidates, _, err := engine.discoverCandidates(query, 0, roots)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	if candidates[0].root.CanonicalKey != roots[0].CanonicalKey {
		t.Fatalf("candidate root = %q, want first root %q", candidates[0].root.LogicalPath, roots[0].LogicalPath)
	}
}

func TestMultiRootNestedTraversalAvoidsSkippedChildDataLoss(t *testing.T) {
	tests := []struct {
		name      string
		childName string
		ignore    string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "reachable", childName: "child"},
		{name: "hard skipped", childName: "node_modules"},
		{name: "ignored", childName: "ignored", ignore: "ignored/\n"},
		{name: "negative pathscope", childName: filepath.Join("blocked", "child"), pathScope: []string{"!blocked/**"}},
		{name: "negative glob", childName: filepath.Join("blocked", "child"), userGlobs: []UserGlob{{Pattern: "blocked/**", Negated: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, test.childName)
			match := filepath.Join(child, "match.txt")
			writeMultiRootFixture(t, match, "needle")
			if test.ignore != "" {
				if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte(test.ignore), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{
					PathScope: test.pathScope,
					UserGlobs: test.userGlobs,
				})
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = test.pathScope
				engine.UserGlobs = test.userGlobs
				engine.Silent = true
				engine.PathOnly = true
				results, err := engine.Execute()
				if err != nil {
					t.Fatal(err)
				}
				if len(results) != 1 || results[0].FilePath != match {
					t.Fatalf("roots=%q results=%v, want retained match %q", raw, results, match)
				}
			}
		})
	}
}

func TestMultiRootThreeLevelCollapsePreservesGrandchildData(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	grandchild := filepath.Join(child, "ignored")
	match := filepath.Join(grandchild, "match.txt")
	if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored", "!zzz"}
	permutations := [][]string{
		{parent, child, grandchild},
		{parent, grandchild, child},
		{child, parent, grandchild},
		{child, grandchild, parent},
		{grandchild, parent, child},
		{grandchild, child, parent},
	}
	for _, raw := range permutations {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		engine.PathOnly = true
		candidates, total, err := engine.DiscoverCandidates(1)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		wantRankingPath := filepath.Join("c", "ignored", "match.txt")
		if len(matches) != 1 || matches[0].filePath != match || matches[0].rankingPath != wantRankingPath {
			t.Fatalf("roots %q matches = %+v, want canonical match with ranking %q", raw, matches, wantRankingPath)
		}
	}
}

func TestMultiRootDepthZeroExemptionRetainsChildAndCountParity(t *testing.T) {
	for _, pathScope := range [][]string{{"ignored"}, {"ignored/**"}} {
		t.Run(strings.Join(pathScope, ","), func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "c")
			match := filepath.Join(child, "ignored", "match.txt")
			if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeMultiRootFixture(t, match, "needle")
			if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
				if err != nil {
					t.Fatal(err)
				}
				reported := ReportedStartRoots(roots)
				if RetainNestedStartRoots(pathScope, nil) {
					if len(reported) != 2 || reported[0].LogicalPath != raw[0] || reported[1].LogicalPath != raw[1] {
						t.Fatalf("roots %q reported = %+v, want both in supplied order", raw, reported)
					}
				} else if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
				count, err := GetDocumentFileCountForRoots(nil, roots, pathScope, WalkOptions{})
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = pathScope
				engine.Silent = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != count || candidates[0] != match {
					t.Fatalf("roots %q count/candidates = %d/%v, want one retained match %q", raw, count, candidates, match)
				}
			}
		})
	}
}

func TestMultiRootOutsideRepositoryIgnoreScopeRetainsChild(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("*.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRoots(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 {
			t.Fatalf("roots %q traversal count = %d, want child retained for ignore-scope difference", raw, len(roots))
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}
		fileTypes := []string{"-g", "*.txt"}
		count, err := GetDocumentFileCountForRoots(fileTypes, roots, nil, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
		engine.StartRoots = roots
		engine.Silent = true
		candidates, _, err := engine.DiscoverCandidates(count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || len(candidates) != 1 || candidates[0] != match {
			t.Fatalf("roots %q count/candidates = %d/%v, want standalone-child match %q", raw, count, candidates, match)
		}
	}
}

func TestMultiRootRelativeAnchoredNegativeFilePolicyRetainsChild(t *testing.T) {
	tests := []struct {
		name      string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "pathscope", pathScope: []string{"!c/blocked.txt"}},
		{name: "user glob", userGlobs: []UserGlob{{Pattern: "c/blocked.txt", Negated: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "c")
			match := filepath.Join(child, "blocked.txt")
			writeMultiRootFixture(t, match, "needle")
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				options := StartRootOptions{PathScope: test.pathScope, UserGlobs: test.userGlobs}
				roots, err := PrepareStartRootsWithOptions(raw, options)
				if err != nil {
					t.Fatal(err)
				}
				reported := ReportedStartRoots(roots)
				if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
				fileTypes := []string{"-g", "*.txt"}
				walkOptions := WalkOptions{UserGlobs: test.userGlobs}
				count, err := GetDocumentFileCountForRoots(fileTypes, roots, test.pathScope, walkOptions)
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = test.pathScope
				engine.UserGlobs = test.userGlobs
				engine.Silent = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != 1 || candidates[0] != match {
					t.Fatalf("roots %q count/candidates = %d/%v, want child-only match %q", raw, count, candidates, match)
				}
			}
		})
	}
}

func TestMultiRootAbsolutePoliciesAcrossNestedRoots(t *testing.T) {
	t.Run("positive covers child root only", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		other := t.TempDir()
		childFile := filepath.Join(child, "match.txt")
		writeMultiRootFixture(t, childFile, "needle")
		writeMultiRootFixture(t, filepath.Join(other, "other.txt"), "needle")
		pathScope := []string{filepath.Join(parent, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child, other},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("absolute positive count = %d, want child-root file only", count)
		}
	})

	t.Run("negative remains active in child root", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		other := t.TempDir()
		writeMultiRootFixture(t, filepath.Join(child, "blocked.txt"), "needle")
		writeMultiRootFixture(t, filepath.Join(other, "allowed.txt"), "needle")
		pathScope := []string{"!" + filepath.Join(child, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child, other},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("absolute negative count = %d, want unrelated-root file only", count)
		}
	})

	t.Run("positive reaches ignored traversal child", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "ignored")
		match := filepath.Join(child, "match.txt")
		writeMultiRootFixture(t, match, "needle")
		if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pathScope := []string{filepath.Join(parent, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{parent, child},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 || len(ReportedStartRoots(roots)) != 1 {
			t.Fatalf("roots = %+v, want reported parent plus traversal child", roots)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("ignored traversal child count = %d, want 1", count)
		}
	})

	t.Run("logical and physical symlink forms", func(t *testing.T) {
		physical := t.TempDir()
		logical := filepath.Join(t.TempDir(), "logical")
		if err := os.Symlink(physical, logical); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		child := filepath.Join(physical, "child")
		writeMultiRootFixture(t, filepath.Join(child, "match.txt"), "needle")
		positive := []string{filepath.Join(logical, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child},
			StartRootOptions{PathScope: positive},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, positive, WalkOptions{})
		if err != nil || count != 1 {
			t.Fatalf("logical absolute pattern count/error = %d/%v, want 1/nil", count, err)
		}

		negative := []string{"!" + filepath.Join(physical, "child", "**")}
		roots, err = PrepareStartRootsWithOptions(
			[]string{logical},
			StartRootOptions{PathScope: negative},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err = GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, negative, WalkOptions{})
		if err != nil || count != 0 {
			t.Fatalf("physical absolute negative count/error = %d/%v, want 0/nil", count, err)
		}
	})
}

func TestMultiRootPOSIXAbsoluteGlobstarExemptionRetainsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX absolute path semantics")
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "a")
	match := filepath.Join(child, "ignored", "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(child, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(parent, "**", "a", "ignored", "**")}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(roots) != 2 || len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q = %+v, reported = %+v; want parent plus traversal child", raw, roots, reported)
		}
		fileTypes := []string{"-g", "*.txt"}
		count, err := GetDocumentFileCountForRoots(fileTypes, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		candidates, _, err := engine.DiscoverCandidates(count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || len(candidates) != 1 || candidates[0] != match {
			t.Fatalf("roots %q count/candidates = %d/%v, want %q", raw, count, candidates, match)
		}
	}
}

func TestMultiRootCountDiscoveryParity(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "scoped")
	other := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(child, "one.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(child, "skip.md"), "needle")
	writeMultiRootFixture(t, filepath.Join(other, "two.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(other, "ignored.txt"), "needle")
	if err := os.WriteFile(filepath.Join(other, ".ignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots, err := PrepareStartRoots([]string{parent, child, other}, true)
	if err != nil {
		t.Fatal(err)
	}
	options := WalkOptions{UserGlobs: []UserGlob{{Pattern: "*.txt"}}}
	pathScope := []string{"**/*.txt"}
	countKeyTracking := 0
	count, err := getDocumentFileCountForRoots(nil, roots, pathScope, options, func() {
		countKeyTracking++
	})
	if err != nil {
		t.Fatal(err)
	}
	if countKeyTracking != 1 {
		t.Fatalf("preflight key tracking = %d, want 1 for overlapping roots", countKeyTracking)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = pathScope
	engine.UserGlobs = options.UserGlobs
	engine.Silent = true
	engine.PathOnly = true
	progressKeyTracking := 0
	engine.onProgressKeyTracking = func() {
		progressKeyTracking++
	}
	candidatesWithoutProgress, _, err := engine.DiscoverCandidates(count)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidatesWithoutProgress) != count || progressKeyTracking != 0 {
		t.Fatalf("nil-progress candidates/key tracking = %d/%d, want %d/0", len(candidatesWithoutProgress), progressKeyTracking, count)
	}
	var progress []struct{ processed, total int }
	engine.OnProgress = func(stage string, processed, total int, _ string) {
		if stage == "discovery" && processed > 0 {
			progress = append(progress, struct{ processed, total int }{processed, total})
		}
	}
	candidates, _, err := engine.DiscoverCandidates(count)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != count {
		t.Fatalf("discovery candidates = %d, want preflight count %d", len(candidates), count)
	}
	if progressKeyTracking != 1 {
		t.Fatalf("progress key tracking = %d, want 1 for overlapping roots", progressKeyTracking)
	}
	if len(progress) != count || progress[len(progress)-1].processed != count || progress[len(progress)-1].total != count {
		t.Fatalf("deduplicated progress = %+v, want %d events ending at %d/%d", progress, count, count, count)
	}
	engine.OnProgress = nil
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(results) != count {
		t.Fatalf("count/results = %d/%d, want 2/2", count, len(results))
	}
}

func TestNonOverlappingRootsAvoidCountAndProgressKeyTracking(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(first, "one.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(second, "two.txt"), "needle")

	for _, test := range []struct {
		name string
		raw  []string
		want int
	}{
		{name: "single root", raw: []string{first}, want: 1},
		{name: "non-overlapping roots", raw: []string{first, second}, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, err := PrepareStartRoots(test.raw, true)
			if err != nil {
				t.Fatal(err)
			}
			countKeyTracking := 0
			count, err := getDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, nil, WalkOptions{}, func() {
				countKeyTracking++
			})
			if err != nil {
				t.Fatal(err)
			}
			if count != test.want || countKeyTracking != 0 {
				t.Fatalf("count/key tracking = %d/%d, want %d/0", count, countKeyTracking, test.want)
			}

			engine := NewSearchEngine([]string{"needle"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
			engine.StartRoots = roots
			engine.Silent = true
			progressKeyTracking := 0
			engine.onProgressKeyTracking = func() {
				progressKeyTracking++
			}
			progressEvents := 0
			engine.OnProgress = func(stage string, processed, total int, _ string) {
				if stage == "discovery" && processed > 0 {
					progressEvents++
					if total != count {
						t.Errorf("progress total = %d, want %d", total, count)
					}
				}
			}
			candidates, _, err := engine.DiscoverCandidates(count)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != test.want || progressEvents != test.want || progressKeyTracking != 0 {
				t.Fatalf(
					"candidates/progress/key tracking = %d/%d/%d, want %d/%d/0",
					len(candidates), progressEvents, progressKeyTracking, test.want, test.want,
				)
			}
		})
	}
}

func TestCapForBinaryPrefilter(t *testing.T) {
	for _, ext := range []string{".eml", ".msg", ".mbox", ".rtf", ".docx", ".odt", ".doc"} {
		want := int64(2 * 1024 * 1024)
		if ext == ".eml" || ext == ".msg" || ext == ".mbox" {
			want = 256 * 1024
		}
		for _, spelling := range []string{ext, strings.ToUpper(ext)} {
			if got := capForBinaryPrefilter(spelling); got != want {
				t.Errorf("cap(%q) = %d, want %d", spelling, got, want)
			}
		}
	}
}

func TestStage2BinaryPrefilterProvenance(t *testing.T) {
	for _, ext := range []string{".eml", ".mbox", ".rtf"} {
		for _, multi := range []bool{false, true} {
			t.Run(ext+"/multi="+map[bool]string{false: "false", true: "true"}[multi], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "sample"+ext)
				if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
					t.Fatal(err)
				}
				terms := []string{"alpha"}
				if multi {
					terms = append(terms, "beta")
				}
				engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
				engine.StartDir, engine.Silent, engine.Strict = dir, true, true
				extractor := &rankingCountingExtractor{text: "alpha beta"}
				engine.Registry.extractors[strings.TrimPrefix(ext, ".")] = extractor
				query := compileQuery(terms, PartialModeOff)
				roots, err := engine.effectiveStartRoots()
				if err != nil {
					t.Fatal(err)
				}
				candidates, total, err := engine.discoverCandidates(query, 1, roots)
				if err != nil || len(candidates) != 1 {
					t.Fatalf("discovery = %v, %v", candidates, err)
				}
				if !candidates[0].primaryPrefiltered {
					t.Fatal("discovery provenance missing")
				}
				// Turn a repeated raw scan into a decisive miss. The controlled extractor
				// still returns the matching text, so reaching extraction proves bypass.
				if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
					t.Fatal(err)
				}
				matches, err := engine.filterCandidates(query, candidates, total, time.Now())
				want := 1
				if multi {
					want = 0
				}
				if err != nil || len(matches) != want || extractor.calls.Load() != int64(want) {
					t.Fatalf("discovered: matches=%d calls=%d err=%v; want %d", len(matches), extractor.calls.Load(), err, want)
				}
				// Public path-only calls carry no query provenance, even on a reused engine.
				before := extractor.calls.Load()
				matches, err = engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil || len(matches) != 0 || extractor.calls.Load() != before {
					t.Fatalf("direct: matches=%d calls=%d err=%v", len(matches), extractor.calls.Load(), err)
				}
			})
		}
	}
}

func TestStage2MSGSkipsPrefilter(t *testing.T) {
	for _, terms := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(terms, "-"), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sample.MSG")
			if err := os.WriteFile(path, []byte("opaque container"), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
			engine.StartDir, engine.Silent = dir, true
			extractor := &rankingReaderAtExtractor{}
			extractor.text = "alpha beta"
			engine.Registry.extractors["msg"] = extractor
			matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
			if err != nil || len(matches) != 1 {
				t.Fatalf("matches=%d err=%v", len(matches), err)
			}
			if extractor.readerCalls.Load() != 1 || extractor.calls.Load() != 0 || engine.msgPrefilterCount != 0 {
				t.Fatalf("ReaderAt=%d legacy=%d prefilters=%d", extractor.readerCalls.Load(), extractor.calls.Load(), engine.msgPrefilterCount)
			}
		})
	}
}

func TestResultRanking(t *testing.T) {
	tests := []struct {
		name        string
		searchWords []string
		partial     PartialMode
		files       map[string]string
		wantOrder   []string
	}{
		{
			name:        "rarity score ranks first",
			searchWords: []string{"term", "synchronization", "go"},
			files: map[string]string{
				"a-lower.txt":  "term go",
				"z-higher.txt": "term synchronization",
			},
			wantOrder: []string{"z-higher.txt", "a-lower.txt"},
		},
		{
			name:        "term count breaks equal score",
			searchWords: []string{"a", "zz", "b", "c"},
			files: map[string]string{
				"a-fewer.txt": "a zz",
				"z-more.txt":  "a b c",
			},
			wantOrder: []string{"z-more.txt", "a-fewer.txt"},
		},
		{
			name:        "smaller span breaks equal score and term count",
			searchWords: []string{"a", "b"},
			files: map[string]string{
				"01-x.txt": "a " + strings.Repeat("x", 50) + " b",
				"02-y.txt": "a b",
			},
			wantOrder: []string{"02-y.txt", "01-x.txt"},
		},
		{
			name:        "path match count breaks equal content quality",
			searchWords: []string{"alpha", "beta"},
			files: map[string]string{
				"neutral/01.txt": "alpha beta",
				"alpha/99.txt":   "alpha beta",
			},
			wantOrder: []string{"alpha/99.txt", "neutral/01.txt"},
		},
		{
			name:        "boundary quality breaks equal score term and path quality",
			searchWords: []string{"api"},
			partial:     PartialModeContains,
			files: map[string]string{
				"01.txt": "xapiy",
				"02.txt": "api",
			},
			wantOrder: []string{"02.txt", "01.txt"},
		},
		{
			name:        "top quality chunks break otherwise equal metadata",
			searchWords: []string{"alpha", "beta"},
			files: map[string]string{
				"01.txt": "alpha beta",
				"02.txt": "alpha beta " + strings.Repeat("x", 150) + " alpha beta",
			},
			wantOrder: []string{"02.txt", "01.txt"},
		},
		{
			name:        "file path makes otherwise equal results deterministic",
			searchWords: []string{"a", "b"},
			files: map[string]string{
				"01.txt": "a b",
				"02.txt": "a b",
			},
			wantOrder: []string{"01.txt", "02.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("create parent for %s: %v", name, err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			engine := NewSearchEngineWithWorkers(
				tt.searchWords, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1,
			)
			engine.StartDir = root
			engine.Distance = 100
			engine.Silent = true
			engine.Partial = tt.partial

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != len(tt.wantOrder) {
				t.Fatalf("result count = %d, want %d", len(results), len(tt.wantOrder))
			}
			for i, want := range tt.wantOrder {
				rel, relErr := filepath.Rel(root, results[i].FilePath)
				if relErr != nil {
					t.Fatalf("relative result path: %v", relErr)
				}
				if got := filepath.ToSlash(rel); got != want {
					t.Fatalf("result %d = %q, want %q", i, got, want)
				}
			}

			engine.PathOnly = true
			pathOnly, err := engine.Execute()
			if err != nil {
				t.Fatalf("path-only Execute() error: %v", err)
			}
			if len(pathOnly) != len(results) {
				t.Fatalf("path-only result count = %d, want %d", len(pathOnly), len(results))
			}
			for i := range results {
				if pathOnly[i].FilePath != results[i].FilePath {
					t.Fatalf("path-only result %d = %q, want %q", i, pathOnly[i].FilePath, results[i].FilePath)
				}
			}
		})
	}
}

// buildPerFileSubsetFixture writes a corpus where clusters of distinct
// quality tiers exist across two files so engine-level per-file subsets and
// file ordering can be asserted.
func buildPerFileSubsetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// mixed.txt: two separated clusters -- a three-term cluster (best) and a
	// two-term cluster (lower quality). Both must survive in one per-file subset.
	mixed := strings.Join([]string{
		"First cluster begins. alpha beta gamma mixed first. First cluster ends.",
		skipAheadPadding("mixed-between", 90),
		"Second cluster begins. alpha beta mixed second. Second cluster ends.",
	}, " ")

	// all.txt: two separated three-term clusters (two top-quality chunks).
	all := strings.Join([]string{
		"First cluster begins. alpha beta gamma all first. First cluster ends.",
		skipAheadPadding("all-between", 90),
		"Second cluster begins. alpha beta gamma all second. Second cluster ends.",
	}, " ")

	if err := os.WriteFile(filepath.Join(root, "mixed.txt"), []byte(mixed), 0o600); err != nil {
		t.Fatalf("write mixed.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "all.txt"), []byte(all), 0o600); err != nil {
		t.Fatalf("write all.txt: %v", err)
	}
	return root
}

func TestEnginePerFileSubsetsRetainLowerQualityClusters(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = 3
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2 files", len(results))
	}

	mixed := skipAheadResultByFile(t, results, "mixed.txt")
	if len(mixed.RawExcerpts) != 2 {
		t.Fatalf("mixed.txt excerpts = %d, want 2 (both quality levels)", len(mixed.RawExcerpts))
	}
	if !strings.Contains(mixed.RawExcerpts[0], "gamma") {
		t.Fatalf("mixed.txt first excerpt should be the best cluster: %q", mixed.RawExcerpts[0])
	}
	if strings.Contains(mixed.RawExcerpts[1], "gamma") {
		t.Fatalf("mixed.txt second excerpt should be the lower-quality cluster: %q", mixed.RawExcerpts[1])
	}
	if mixed.TopQualityChunks != 1 {
		t.Fatalf("mixed.txt top-quality chunk count = %d, want 1", mixed.TopQualityChunks)
	}

	all := skipAheadResultByFile(t, results, "all.txt")
	if len(all.RawExcerpts) != 2 {
		t.Fatalf("all.txt excerpts = %d, want 2", len(all.RawExcerpts))
	}
	if all.TopQualityChunks != 2 {
		t.Fatalf("all.txt top-quality chunk count = %d, want 2", all.TopQualityChunks)
	}

	// File ordering: same file score and term count, but all.txt has more
	// top-quality chunks, so it ranks first.
	if got := filepath.Base(results[0].FilePath); got != "all.txt" {
		t.Fatalf("first result = %q, want all.txt (more top-quality chunks)", got)
	}
}

func TestEnginePerFileSubsetsExcerptLimitDoesNotChangeFileRank(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	var orders [][]string
	for _, maxExcerpts := range []int{1, 3} {
		engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
		engine.StartDir = root
		engine.Distance = 100
		engine.MaxExcerpts = maxExcerpts
		engine.Silent = true

		results, err := engine.Execute()
		if err != nil {
			t.Fatalf("Execute() with MaxExcerpts=%d error: %v", maxExcerpts, err)
		}
		order := make([]string, len(results))
		for i, result := range results {
			order[i] = filepath.Base(result.FilePath)
		}
		orders = append(orders, order)

		all := skipAheadResultByFile(t, results, "all.txt")
		if all.TopQualityChunks != 2 {
			t.Fatalf("MaxExcerpts=%d TopQualityChunks = %d, want Stage 2 count 2", maxExcerpts, all.TopQualityChunks)
		}
		if len(all.RawExcerpts) > maxExcerpts {
			t.Fatalf("MaxExcerpts=%d emitted %d excerpts", maxExcerpts, len(all.RawExcerpts))
		}
	}

	if strings.Join(orders[0], "\x00") != strings.Join(orders[1], "\x00") {
		t.Fatalf("file order changed with excerpt limit: %v vs %v", orders[0], orders[1])
	}
}

func TestEnginePerFileSubsetsStage3Regressions(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	noveltyCorpus := endpointNoveltyCorpus()
	for _, tc := range []struct {
		name     string
		content  string
		distance int
		markers  []string
		terms    []int
	}{
		{
			name:     "distant extra term cannot inflate excerpt quality",
			content:  "alpha beta near cluster\n" + strings.Repeat("padding ", 500) + "\ngamma",
			distance: 20,
			markers:  []string{"near cluster"},
			terms:    []int{2},
		},
		{
			name:     "sole endpoint is not reused or allowed to suppress middle cluster",
			content:  noveltyCorpus,
			distance: len(noveltyCorpus),
			markers:  []string{"final cluster", "first cluster", "middle cluster"},
			terms:    []int{3, 2, 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "clusters.txt"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
			engine.StartDir = root
			engine.Distance = tc.distance
			engine.MaxExcerpts = 3
			engine.Silent = true
			results, err := engine.Execute()
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d files, want 1", len(results))
			}
			result := results[0]
			wantScore := len("alpha") + len("beta")
			if tc.terms[0] == 3 {
				wantScore += len("gamma")
			}
			if result.Score != wantScore || result.TermCount != tc.terms[0] {
				t.Fatalf("file quality = (%d, %d), want (%d, %d)", result.Score, result.TermCount, wantScore, tc.terms[0])
			}
			if len(result.RawExcerpts) != len(tc.markers) || len(result.ChunkScores) != len(tc.markers) || len(result.ChunkTermCounts) != len(tc.markers) {
				t.Fatalf("excerpt/score/term counts = (%d, %d, %d), want %d each", len(result.RawExcerpts), len(result.ChunkScores), len(result.ChunkTermCounts), len(tc.markers))
			}
			for i, marker := range tc.markers {
				if result.ChunkScores[i] > result.Score || result.ChunkTermCounts[i] > result.TermCount {
					t.Fatalf("excerpt %d quality exceeds parent file", i)
				}
				if result.ChunkTermCounts[i] != tc.terms[i] || !strings.Contains(result.RawExcerpts[i], marker) {
					t.Fatalf("excerpt %d = %q (%d terms), want %s (%d terms)", i, result.RawExcerpts[i], result.ChunkTermCounts[i], marker, tc.terms[i])
				}
				if tc.terms[i] == 2 && strings.Contains(result.RawExcerpts[i], "gamma") {
					t.Fatalf("excerpt %d includes distant or reused gamma: %q", i, result.RawExcerpts[i])
				}
			}
		})
	}
}

func TestEnginePerFileSubsetsChunkOrderingWithinFile(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = 3
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	mixed := skipAheadResultByFile(t, results, "mixed.txt")
	if len(mixed.ChunkScores) != len(mixed.RawExcerpts) || len(mixed.ChunkTermCounts) != len(mixed.RawExcerpts) {
		t.Fatalf("chunk metadata lengths = (%d, %d), want %d each", len(mixed.ChunkScores), len(mixed.ChunkTermCounts), len(mixed.RawExcerpts))
	}
	for i := 1; i < len(mixed.ChunkScores); i++ {
		if mixed.ChunkScores[i] > mixed.ChunkScores[i-1] {
			t.Fatalf("chunk %d score %d exceeds prior chunk score %d (best-first violated)", i, mixed.ChunkScores[i], mixed.ChunkScores[i-1])
		}
	}
	// Anchors stay in document order regardless of chunk emission order.
	for i := 1; i < len(mixed.StartLines); i++ {
		if mixed.StartLines[i] < mixed.StartLines[i-1] {
			t.Fatalf("chunk %d start line %d before prior %d (document order violated)", i, mixed.StartLines[i], mixed.StartLines[i-1])
		}
	}
}

func TestCaseSensitiveTextAndBinaryDiscovery(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"upper.txt": "API",
		"lower.txt": "api",
		"upper.eml": "Subject: Upper\r\n\r\nAPI",
		"lower.eml": "Subject: Lower\r\n\r\napi",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"API"}, nil, []string{"-g", "*.txt", "-g", "*.eml"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.PathOnly = true
	engine.CaseSensitive = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, result := range results {
		got[filepath.Base(result.FilePath)] = true
	}
	if len(got) != 2 || !got["upper.txt"] || !got["upper.eml"] {
		t.Fatalf("case-sensitive results = %v", got)
	}
}

func TestCaseSensitiveExcludeWordsAndExtensionExclusions(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"upper.txt": "Needle BLOCK",
		"lower.txt": "Needle block",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"Needle"}, []string{"BLOCK"}, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.PathOnly = true
	engine.CaseSensitive = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "lower.txt" {
		t.Fatalf("word-exclusion results = %#v", results)
	}

	engine.ExcludeWords = []string{".TXT"}
	results, err = engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("case-insensitive extension exclusion retained %#v", results)
	}
}

func TestCaseSensitiveExclusionsStayLiteralWholeWords(t *testing.T) {
	sensitiveSmart := QueryOptions{CaseSensitive: true, SmartForms: true}
	for text, want := range map[string]bool{
		"BLOCK":  true,
		"BLOCKs": false,
		"BLOCKS": false,
	} {
		if got := CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK"}, sensitiveSmart); got != want {
			t.Errorf("case-sensitive exclusion for %q = %t, want %t", text, got, want)
		}
	}
	if !CheckTextContainsExcludeWordsWithOptions("Needle BLOCK", []string{"BLOCK"}, sensitiveSmart) {
		t.Fatal("exact-case exclusion did not match")
	}
	if CheckTextContainsExcludeWordsWithOptions("Needle wild*", []string{"wild*"}, sensitiveSmart) {
		t.Fatal("legacy word boundaries unexpectedly matched a trailing-punctuation exclusion")
	}
	for name, text := range map[string]string{
		"case mismatch":        "Needle block",
		"wildcard expansion":   "Needle wildcard",
		"smart form expansion": "Needle BLOCKED",
		"embedded literal":     "Needle xwild*y",
	} {
		if CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK", "wild*"}, sensitiveSmart) {
			t.Errorf("%s unexpectedly matched: %q", name, text)
		}
	}

	insensitive := QueryOptions{SmartForms: true}
	for _, text := range []string{"BLOCK", "BLOCKs", "BLOCKS"} {
		if !CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK"}, insensitive) {
			t.Errorf("case-insensitive compatibility exclusion rejected %q", text)
		}
	}
	for name, test := range map[string]struct {
		text    string
		exclude string
	}{
		"legacy trailing punctuation": {text: "Needle WILD*", exclude: "wild*"},
		"wildcard expansion":          {text: "Needle wildcard", exclude: "wild*"},
		"smart form expansion":        {text: "Needle blocked", exclude: "BLOCK"},
	} {
		if CheckTextContainsExcludeWordsWithOptions(test.text, []string{test.exclude}, insensitive) {
			t.Errorf("%s unexpectedly matched %q with %q", name, test.text, test.exclude)
		}
	}
}

func TestCaseInsensitiveExclusionsPreserveLegacyWordBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text    string
		exclude string
	}{
		{text: "(test), passed", exclude: "test"},
		{text: "bob@spam.com", exclude: "@spam.com"},
		{text: "message from bob@spam.com!", exclude: "@SPAM.COM"},
	} {
		if !CheckTextContainsExcludeWords(tt.text, []string{tt.exclude}) {
			t.Errorf("legacy exclusion %q did not match %q", tt.exclude, tt.text)
		}
	}
	for _, text := range []string{"contest", "testament"} {
		if CheckTextContainsExcludeWords(text, []string{"test"}) {
			t.Errorf("whole-word exclusion matched %q", text)
		}
	}
}

func TestFilterCandidatesWordExclusionsUseSearchableText(t *testing.T) {
	const page = `<style>.secret{}</style><script>const forbidden=1;</script><body class="draft"><p>Actual visible summary.</p></body>`
	for _, tt := range []struct {
		name, raw, exclude string
		kept, codeKept     bool
	}{
		{"style", page, "secret", true, false},
		{"script", page, "forbidden", true, false},
		{"attribute", page, "draft", true, false},
		{"visible word", page, "summary", false, false},
		{"quoted line", "visible summary\n> forbidden reply", "forbidden", true, false},
		{"split", "visible summary PDF32", "PDF", false, true},
		{"unsplit", "visible summary PDF32", "PDF32", true, false},
		{"phrase", "visible summary <b>red</b>\n\tflag", "red flag", false, true},
		{"whitespace", "visible summary red\n\tflag", "red flag", false, false},
	} {
		for _, terms := range [][]string{{"visible"}, {"visible", "summary"}} {
			for _, name := range []string{"doc.html", "doc.pdf", "doc.go"} {
				t.Run(tt.name+"/"+strings.Join(terms, "+")+"/"+name, func(t *testing.T) {
					root := t.TempDir()
					path := filepath.Join(root, name)
					if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
						t.Fatal(err)
					}
					engine := NewSearchEngine(terms, []string{tt.exclude}, nil, false, 1, 1000)
					engine.StartDir = root
					engine.Silent = true
					if name == "doc.pdf" {
						engine.Registry.extractors["pdf"] = &rankingCountingExtractor{text: tt.raw}
					}
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					want := tt.kept
					if name == "doc.go" {
						want = tt.codeKept
					}
					if got := len(matches) == 1; got != want {
						t.Errorf("kept = %t, want %t (exclude %q)", got, want, tt.exclude)
					}
				})
			}
		}
	}
}

func TestCaseSensitiveSmartFormsAndRankingMetadata(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"exact.txt":  "DeployED",
		"folded.txt": "deployED",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"Deploy"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.CaseSensitive = true
	engine.SmartForms = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "exact.txt" {
		t.Fatalf("smart-form results = %#v", results)
	}
	if results[0].TermCount != 1 || len(results[0].MatchedTerms) != 1 || results[0].MatchedTerms[0] != "Deploy" {
		t.Fatalf("ranking metadata = %#v", results[0])
	}
}

func TestPartialIntegration(t *testing.T) {
	root := t.TempDir()
	writeEngineFixtureFiles(t, root, map[string]string{
		"a_deployment.txt": "deployment in production",
		"b_redeploy.txt":   "redeploy the container",
		"c_substring.txt":  "application concatenate locate",
		"d_prefixes.txt":   "category catalog categorization",
		"e_whole_word.txt": "cat on the mat",
		"code_match.go":    "// deployment service\nfunc deployService() {}",
		"code_far.go":      "// deployment " + strings.Repeat("filler ", 20) + "service",
		"code_excluded.go": "// deployment service deprecated",
	})

	for _, tt := range []struct {
		name  string
		words []string
		mode  PartialMode
		want  []string
	}{
		{
			name:  "prefix returns deployment but not redeploy",
			words: []string{"deploy"},
			mode:  PartialModePrefix,
			want:  []string{"a_deployment.txt"},
		},
		{
			name:  "inline glob matches prefix with partial mode off",
			words: []string{"deploy*"},
			mode:  PartialModeOff,
			want:  []string{"a_deployment.txt"},
		},
		{
			name:  "contains includes redeploy",
			words: []string{"deploy"},
			mode:  PartialModeContains,
			want:  []string{"a_deployment.txt", "b_redeploy.txt"},
		},
		{
			name:  "prefix avoids catastrophic cat substrings",
			words: []string{"cat"},
			mode:  PartialModePrefix,
			want:  []string{"d_prefixes.txt", "e_whole_word.txt"},
		},
		{
			name:  "contains includes cat substrings",
			words: []string{"cat"},
			mode:  PartialModeContains,
			want:  []string{"c_substring.txt", "d_prefixes.txt", "e_whole_word.txt"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine := newPartialIntegrationEngine(root, tt.words, nil, []string{"-g", "*.txt"}, false, tt.mode, 100)
			assertPartialIntegrationFiles(t, engine, tt.want)
		})
	}

	t.Run("code distance and exclusions compose with prefix matching", func(t *testing.T) {
		engine := newPartialIntegrationEngine(
			root,
			[]string{"deploy", "service"},
			[]string{"deprecated"},
			[]string{"-g", "*.go"},
			true,
			PartialModePrefix,
			50,
		)
		assertPartialIntegrationFiles(t, engine, []string{"code_match.go"})
	})
}

func newPartialIntegrationEngine(root string, words, excludes, fileTypes []string, includeCode bool, mode PartialMode, distance int) *SearchEngine {
	engine := NewSearchEngineWithWorkers(words, excludes, fileTypes, includeCode, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = distance
	engine.Partial = mode
	engine.Silent = true
	return engine
}

func assertPartialIntegrationFiles(t *testing.T, engine *SearchEngine, want []string) {
	t.Helper()
	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	got := resultFileNames(results)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("result files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result files = %v, want %v", got, want)
		}
	}
}

func TestRankedMultiTermIntegration(t *testing.T) {
	const (
		primary   = "query"
		secondary = "synchronization"
		tertiary  = "mutex"
	)
	words := []string{primary, secondary, tertiary}

	t.Run("Strategy A ranks qualifying documents and exclusions override score", func(t *testing.T) {
		root := t.TempDir()
		writeEngineFixtureFiles(t, root, map[string]string{
			"doc_all.txt":      "query synchronization mutex",
			"doc_t1_t2.txt":    "query synchronization",
			"doc_t1_t3.txt":    "query mutex",
			"doc_t2_t3.txt":    "synchronization mutex",
			"doc_t1_only.txt":  "query",
			"doc_excluded.txt": "query synchronization mutex forbidden",
		})

		results := executeRankedIntegrationSearch(t, root, words, []string{"forbidden"}, false)
		want := []struct {
			file      string
			score     int
			termCount int
		}{
			{"doc_all.txt", 25, 3},
			{"doc_t1_t2.txt", 20, 2},
			{"doc_t1_t3.txt", 10, 2},
		}
		if len(results) != len(want) {
			t.Fatalf("result count = %d, want %d", len(results), len(want))
		}
		for i, want := range want {
			got := results[i]
			if file := filepath.Base(got.FilePath); file != want.file || got.Score != want.score || got.TermCount != want.termCount {
				t.Fatalf("result %d = (%q, score %d, term count %d), want (%q, score %d, term count %d)", i, file, got.Score, got.TermCount, want.file, want.score, want.termCount)
			}
		}
	})

	t.Run("exhaustive windows select later three-term domain cluster for metadata and excerpts", func(t *testing.T) {
		root := t.TempDir()
		writeEngineFixtureFiles(t, root, map[string]string{
			"windows.txt": "query mutex. " + strings.Repeat("padding ", 20) + "query synchronization mutex domain cluster.",
		})

		results := executeRankedIntegrationSearch(t, root, words, nil, false)
		if len(results) != 1 {
			t.Fatalf("result count = %d, want 1", len(results))
		}
		result := results[0]
		if result.Score != 25 || result.TermCount != 3 || strings.Join(result.MatchedTerms, ",") != strings.Join(words, ",") {
			t.Fatalf("selected window metadata = (score %d, term count %d, terms %v), want (25, 3, %v)", result.Score, result.TermCount, result.MatchedTerms, words)
		}
		if len(result.RawExcerpts) == 0 {
			t.Fatal("selected window produced no raw excerpt")
		}
		excerpt := strings.Join(result.RawExcerpts, " ")
		for _, term := range words {
			if !strings.Contains(strings.ToLower(excerpt), term) {
				t.Fatalf("selected window excerpt %q missing later-cluster term %q", excerpt, term)
			}
		}
	})

	t.Run("secondary term order is invariant", func(t *testing.T) {
		root := t.TempDir()
		writeEngineFixtureFiles(t, root, map[string]string{
			"all.txt":   "query synchronization mutex",
			"long.txt":  "query synchronization",
			"short.txt": "query mutex",
		})

		forward := executeRankedIntegrationSearch(t, root, words, nil, false)
		reordered := executeRankedIntegrationSearch(t, root, []string{primary, tertiary, secondary}, nil, false)
		if len(forward) != len(reordered) {
			t.Fatalf("reordered result count = %d, want %d", len(reordered), len(forward))
		}
		for i := range forward {
			if filepath.Base(forward[i].FilePath) != filepath.Base(reordered[i].FilePath) || forward[i].Score != reordered[i].Score || forward[i].TermCount != reordered[i].TermCount {
				t.Fatalf("reordered result %d = (%q, score %d, term count %d), want (%q, score %d, term count %d)", i, filepath.Base(reordered[i].FilePath), reordered[i].Score, reordered[i].TermCount, filepath.Base(forward[i].FilePath), forward[i].Score, forward[i].TermCount)
			}
		}
	})

	t.Run("strict three-term and invariant two-term queries require every term", func(t *testing.T) {
		root := t.TempDir()
		writeEngineFixtureFiles(t, root, map[string]string{
			"all.txt":   "query synchronization mutex",
			"long.txt":  "query synchronization",
			"short.txt": "query mutex",
		})

		strict := executeRankedIntegrationSearch(t, root, words, nil, true)
		if len(strict) != 1 || filepath.Base(strict[0].FilePath) != "all.txt" {
			t.Fatalf("strict three-term results = %v, want only all.txt", resultFileNames(strict))
		}

		twoTerm := executeRankedIntegrationSearch(t, root, []string{primary, secondary}, nil, false)
		if len(twoTerm) != 2 || filepath.Base(twoTerm[0].FilePath) != "all.txt" || filepath.Base(twoTerm[1].FilePath) != "long.txt" {
			t.Fatalf("two-term results = %v, want [all.txt long.txt]", resultFileNames(twoTerm))
		}
	})

	t.Run("heavy binary follows relaxed Strategy A and strict conjunction", func(t *testing.T) {
		root := t.TempDir()
		writeEngineFixtureFiles(t, root, map[string]string{
			"short-secondary.eml": "From: sender@example.com\nTo: recipient@example.com\nSubject: partial\nContent-Type: text/plain; charset=utf-8\n\nquery mutex",
			"primary-only.eml":    "From: sender@example.com\nTo: recipient@example.com\nSubject: primary\nContent-Type: text/plain; charset=utf-8\n\nquery",
		})

		engine := NewSearchEngineWithWorkers(words, nil, []string{"-g", "*.eml"}, false, 1, 1000, 1)
		engine.StartDir = root
		engine.Distance = 100
		engine.Silent = true
		results, err := engine.Execute()
		if err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
		if len(results) != 1 || filepath.Base(results[0].FilePath) != "short-secondary.eml" {
			t.Fatalf("relaxed heavy binary results = %v, want [short-secondary.eml]", resultFileNames(results))
		}

		engine.Strict = true
		strictResults, err := engine.Execute()
		if err != nil {
			t.Fatalf("strict Execute() error: %v", err)
		}
		if len(strictResults) != 0 {
			t.Fatalf("strict heavy binary results = %v, want none when a term is missing", resultFileNames(strictResults))
		}
	})
}

func executeRankedIntegrationSearch(t *testing.T, root string, words, excludes []string, strict bool) []SearchResult {
	t.Helper()
	engine := NewSearchEngineWithWorkers(words, excludes, []string{"-g", "*.txt"}, false, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.Silent = true
	engine.Strict = strict

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	return results
}

func writeEngineFixtureFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func resultFileNames(results []SearchResult) []string {
	names := make([]string, len(results))
	for i, result := range results {
		names[i] = filepath.Base(result.FilePath)
	}
	return names
}

type skipAheadJSONExcerpt struct {
	Text string `json:"text"`
}

func TestSkipAheadEngineRegressionCorpus(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	dense := skipAheadDenseCorpus("needle")
	separated := skipAheadSeparatedCorpus("needle")
	writeWalkFixture(t, root, "dense.txt", dense)
	writeWalkFixture(t, root, "separated.txt", separated)

	results := executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, false, 3)
	denseResult := skipAheadResultByFile(t, results, "dense.txt")
	separatedResult := skipAheadResultByFile(t, results, "separated.txt")

	if len(denseResult.RawExcerpts) != 1 {
		t.Fatalf("dense raw excerpts = %d, want 1 bounded-overlap excerpt", len(denseResult.RawExcerpts))
	}
	if len(separatedResult.RawExcerpts) != 3 {
		t.Fatalf("separated raw excerpts = %d, want 3 distinct excerpts", len(separatedResult.RawExcerpts))
	}
	requireBoundedEngineExcerpts(t, dense, denseResult.RawExcerpts, "needle", false)
	requireBoundedEngineExcerpts(t, separated, separatedResult.RawExcerpts, "needle", false)

	encoded, err := json.Marshal(skipAheadJSONExcerpts(separatedResult.RawExcerpts))
	if err != nil {
		t.Fatalf("marshal JSON excerpts: %v", err)
	}
	var decoded []skipAheadJSONExcerpt
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal JSON excerpts: %v", err)
	}
	jsonExcerpts := make([]string, len(decoded))
	for i, excerpt := range decoded {
		jsonExcerpts[i] = excerpt.Text
	}
	requireBoundedEngineExcerpts(t, separated, jsonExcerpts, "needle", false)
}

func TestSkipAheadEngineSingleChunkGolden(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	content := skipAheadSeparatedCorpus("needle")
	writeWalkFixture(t, root, "single.txt", content)

	result := skipAheadResultByFile(t, executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, false, 1), "single.txt")
	if len(result.RawExcerpts) != 1 {
		t.Fatalf("single-chunk raw excerpts = %d, want 1", len(result.RawExcerpts))
	}
	if len(result.RawExcerpts[0]) > 400 {
		t.Fatalf("single-chunk excerpt length = %d, exceeds budget 400", len(result.RawExcerpts[0]))
	}
	if !strings.Contains(result.RawExcerpts[0], "needle first marker") {
		t.Fatalf("single-chunk excerpt does not contain first cluster: %q", result.RawExcerpts[0])
	}

	wantGolden, err := os.ReadFile(filepath.Join("testdata", "skipahead_single_chunk.golden"))
	if err != nil {
		t.Fatalf("read single-chunk golden: %v", err)
	}
	if result.RawExcerpts[0] != string(wantGolden) {
		t.Fatalf("single-chunk excerpt changed from the new unified framing golden:\n got: %q\nwant: %q", result.RawExcerpts[0], wantGolden)
	}
}

func TestSkipAheadEngineCodeCorpus(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	content := strings.Join([]string{
		"package fixture", "", "func first() {", "\tneedle := \"first marker\"", "\t_ = needle", "}",
		numberedPadding("// between-first-second", 24, "\n"),
		"func second() {", "\tneedle := \"second marker\"", "\t_ = needle", "}",
		numberedPadding("// between-second-third", 24, "\n"),
		"func third() {", "\tneedle := \"third marker\"", "\t_ = needle", "}",
	}, "\n")
	writeWalkFixture(t, root, "fixture.go", content)

	result := skipAheadResultByFile(t, executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, true, 3), "fixture.go")
	if len(result.RawExcerpts) != 3 {
		t.Fatalf("code raw excerpts = %d, want 3 distinct excerpts", len(result.RawExcerpts))
	}
	requireBoundedEngineExcerpts(t, content, result.RawExcerpts, "needle", true)
}

func executeSkipAheadIntegrationSearch(t *testing.T, root string, words []string, includeCode bool, maxExcerpts int) []SearchResult {
	t.Helper()
	fileGlob := "*.txt"
	if includeCode {
		fileGlob = "*.go"
	}
	engine := NewSearchEngineWithWorkers(words, nil, []string{"-g", fileGlob}, includeCode, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = maxExcerpts
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	return results
}

func skipAheadResultByFile(t *testing.T, results []SearchResult, name string) SearchResult {
	t.Helper()
	for _, result := range results {
		if filepath.Base(result.FilePath) == name {
			return result
		}
	}
	t.Fatalf("result for %q not found among %v", name, skipAheadResultNames(results))
	return SearchResult{}
}

func skipAheadResultNames(results []SearchResult) []string {
	names := make([]string, len(results))
	for i, result := range results {
		names[i] = filepath.Base(result.FilePath)
	}
	return names
}

func skipAheadJSONExcerpts(excerpts []string) []skipAheadJSONExcerpt {
	out := make([]skipAheadJSONExcerpt, len(excerpts))
	for i, excerpt := range excerpts {
		out[i] = skipAheadJSONExcerpt{Text: excerpt}
	}
	return out
}

func requireBoundedEngineExcerpts(t *testing.T, content string, excerpts []string, term string, isCode bool) {
	t.Helper()
	cleaned := CleanContent(content)
	if isCode {
		cleaned = CleanContentCode(content)
	}
	spans := extractExcerptSpans(cleaned, []string{term}, len(excerpts), isCode)
	if len(spans) != len(excerpts) {
		t.Fatalf("extractor span count = %d, engine excerpt count = %d", len(spans), len(excerpts))
	}
	for i, span := range spans {
		if span.text != excerpts[i] {
			t.Fatalf("engine excerpt %d = %q, extractor span text = %q", i, excerpts[i], span.text)
		}
	}
	requireBoundedNovelSpans(t, cleaned, spans, term)
	for i := range excerpts {
		for j := 0; j < i; j++ {
			common := skipAheadLongestCommonRun(excerpts[i], excerpts[j])
			limit := len(excerpts[i])
			if len(excerpts[j]) < limit {
				limit = len(excerpts[j])
			}
			if common*5 >= limit {
				t.Fatalf("excerpts %d and %d share common run %d, want less than 20%% of shorter length %d", i, j, common, limit)
			}
		}
	}
}

func skipAheadLongestCommonRun(a, b string) int {
	previous := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				current[j] = previous[j-1] + 1
				if current[j] > best {
					best = current[j]
				}
			}
		}
		previous = current
	}
	return best
}

func skipAheadDenseCorpus(term string) string {
	return strings.Join([]string{
		"Dense regression corpus begins with context.",
		skipAheadPadding("dense-before", 35), term + " first marker.",
		skipAheadPadding("dense-first-second", 6), term + " second marker.",
		skipAheadPadding("dense-second-third", 6), term + " third marker.",
		skipAheadPadding("dense-after", 35) + " Dense regression corpus ends.",
	}, " ")
}

func skipAheadSeparatedCorpus(term string) string {
	return strings.Join([]string{
		"First cluster begins. " + term + " first marker. First cluster ends.",
		skipAheadPadding("between-first-second", 90),
		"Second cluster begins. " + term + " second marker. Second cluster ends.",
		skipAheadPadding("between-second-third", 90),
		"Third cluster begins. " + term + " third marker. Third cluster ends.",
	}, " ")
}

func skipAheadPadding(prefix string, count int) string {
	return numberedPadding(prefix, count, " ")
}

func numberedPadding(prefix string, count int, separator string) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return strings.Join(parts, separator)
}

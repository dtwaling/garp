package search

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
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
	first := filepath.Join(dir, "first.pdf")
	second := filepath.Join(dir, "second.pdf")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("alpha beta"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// With 1 heavy slot, 2 filter workers, and a 250ms timeout, two 150ms
	// extractions take ~300ms wall-clock total but each fits within 250ms once
	// admitted. Stage 2 must retain both candidates.
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta"}, nil, nil, false, 1, 250, 2)
	engine.Silent = true
	engine.Registry.extractors["pdf"] = &delayedContextExtractor{delay: 150 * time.Millisecond}

	matches, err := engine.FilterCandidates([]string{first, second}, 2, time.Now())
	if err != nil {
		t.Fatalf("FilterCandidates failed: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matched %d candidates, want 2 (second candidate timed out from admission wait)", len(matches))
	}
}

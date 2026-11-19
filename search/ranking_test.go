package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMultiAltProximityEvidence(t *testing.T) {
	for _, tc := range []struct {
		text           string
		alts           []string
		distance, span int
		winner         string
		mode           PartialMode
	}{
		{"alpha beta gamma delta", []string{"alpha beta gamma", "gamma"}, 6, 11, "gamma", PartialModeOff},
		{"delta alpha beta gamma", []string{"alpha beta gamma", "gamma"}, 8, 22, "alpha beta gamma", PartialModeOff},
		{"alpha beta gamma delta", []string{"alpha beta", "beta gamma"}, 11, 16, "beta gamma", PartialModeOff},
		{"delta alpha beta gamma", []string{"alpha beta", "beta gamma"}, 8, 16, "alpha beta", PartialModeOff},
		{"delta xalphax gamma", []string{"alpha", "gamma"}, 20, len("delta xalphax gamma"), "gamma", PartialModeContains},
		{"gamma xalphax delta", []string{"alpha", "gamma"}, 20, len("gamma xalphax delta"), "gamma", PartialModeContains},
	} {
		for _, alts := range [][]string{tc.alts, {tc.alts[1], tc.alts[0]}} {
			q := multiAltQuery([][]string{alts, {"delta"}}, tc.mode)
			an, ok := analyzeRankedText(tc.text, q, tc.distance, true)
			wantTF := 1
			if tc.mode == PartialModeContains {
				wantTF = 2
			}
			if !ok || an.TermCount != 2 || an.BoundaryQuality != 2 || an.SpanLength != tc.span || !reflect.DeepEqual(an.MatchedTerms, []string{tc.winner, "delta"}) || an.SlotTF[0] != wantTF {
				t.Errorf("%q %v: analysis=%+v admitted=%v", tc.text, alts, an, ok)
			}
			spans := extractRankedExcerptSpansQuery(tc.text, q, 1, 2, 2, true, tc.distance)
			if len(spans) != 1 || spans[0].termCount != 2 {
				t.Errorf("Stage 3 lost proximity evidence: %+v", spans)
			}
		}
	}
}

func TestMultiAltDisjointTFAndScoring(t *testing.T) {
	for _, alts := range [][]string{
		{"alpha beta", "beta gamma", "gamma delta"},
		{"alpha beta gamma delta", "alpha beta", "gamma delta"},
	} {
		var reference rankedTextAnalysis
		var referenceScore float64
		for n, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			q := multiAltQuery([][]string{{alts[order[0]], alts[order[1]], alts[order[2]]}}, PartialModeOff)
			text := "alpha beta gamma delta"
			an, ok := analyzeRankedText(text, q, 100, true)
			cat := analyzeRankedCatalog(text, q, 100, true, false)
			if !ok || an.TermCount != 1 || an.SlotTF[0] != 2 || cat.SlotTF[0] != 2 || len(q.slotFindAll(0, text)) != 3 {
				t.Fatalf("disjoint TF/evidence: %+v catalog=%+v", an, cat)
			}
			score := scoreAnalyzedDocument(an, q, nil, buildCorpusStats(1, 10, 40, []int{2}), 100)
			if n == 0 {
				reference, referenceScore = an, score
			} else if !reflect.DeepEqual(an, reference) || score != referenceScore {
				t.Fatalf("permutation changed analysis/score: %+v/%v vs %+v/%v", an, score, reference, referenceScore)
			}
		}
	}
	q := multiAltQuery([][]string{{"prune", "ignore", "skip", "exclude"}, {"walk"}, {"gitignore"}}, PartialModeOff)
	text := "prune ignore skip exclude"
	for _, strict := range []bool{false, true} {
		if _, ok := analyzeRankedText(text, q, 100, strict); ok {
			t.Fatal("alternatives counted as separate slots")
		}
	}
	an, ok := analyzeRankedTextWithOptions(text, q, 100, false, true, []bool{true, false, false}, nil)
	if !ok || an.TermCount != 1 || an.SlotTF[0] != 4 {
		t.Fatalf("fallback: %+v %v", an, ok)
	}
	if _, ok := analyzeRankedTextWithOptions(text, q, 100, false, true, []bool{false, true, true}, nil); ok {
		t.Fatal("common slot admitted rare fallback")
	}
}

func TestMultiAltSlotPermutationInvariance(t *testing.T) {
	text := "deploy prune walk ignore skip"
	groups := [][]string{{"prune", "ignore", "skip"}, {"walk"}, {"deploy", "nomatch"}}
	dfByKey := map[string]int{"prune": 2, "walk": 4, "deploy": 6}
	type result struct {
		score         float64
		term, quality int
		span, chunks  int
		pathCount     int
		tf0           int
	}
	var ref result
	have := false
	for _, slotOrder := range [][]int{{0, 1, 2}, {2, 0, 1}, {1, 2, 0}} {
		for _, flip := range []bool{false, true} {
			var ordered [][]string
			df := make([]int, len(groups))
			for i, g := range slotOrder {
				alts := append([]string(nil), groups[g]...)
				if flip {
					slices.Reverse(alts)
				}
				ordered = append(ordered, alts)
				df[i] = dfByKey[groups[g][0]]
			}
			q := multiAltQuery(ordered, PartialModeOff)
			an, ok := analyzeRankedText(text, q, 100, true)
			if !ok {
				t.Fatalf("order=%v flip=%v rejected", slotOrder, flip)
			}
			pathSlots, pathCount := pathMatchStats("/root/skip/walk.txt", "/root", q)
			score := scoreAnalyzedDocument(an, q, pathSlots, buildCorpusStats(3, 20, 200, df), 100)
			got := result{score, an.TermCount, an.BoundaryQuality, an.SpanLength, an.TopQualityChunks, pathCount, 0}
			for i, g := range slotOrder {
				if g == 0 {
					got.tf0 = an.SlotTF[i]
				}
			}
			if got.term != 3 || got.pathCount != 2 || got.tf0 != 3 {
				t.Fatalf("order=%v flip=%v: %+v (one path hit per slot, TF per slot)", slotOrder, flip, got)
			}
			if !have {
				ref, have = got, true
			} else if got != ref {
				t.Fatalf("order=%v flip=%v: %+v != %+v", slotOrder, flip, got, ref)
			}
		}
	}
}

func TestRoundAndFormatScore(t *testing.T) {
	for _, tc := range []struct {
		in, want float64
		text     string
	}{
		{math.NaN(), 0, "0"}, {math.Inf(1), 0, "0"}, {math.Inf(-1), 0, "0"},
		{math.Copysign(0, -1), 0, "0"}, {-2, 0, "0"}, {2, 2, "2"},
		{0.00004, 0, "0"}, {0.00005, 0.0001, "0.0001"}, {4.03276, 4.0328, "4.0328"},
	} {
		if got := RoundScore(tc.in); got != tc.want || math.Signbit(got) || FormatScore(tc.in) != tc.text {
			t.Errorf("round/format(%v) = %v/%q, want %v/%q", tc.in, got, FormatScore(tc.in), tc.want, tc.text)
		}
	}
}

func TestCorpusStatsRarityThresholds(t *testing.T) {
	for _, tc := range []struct{ n, threshold int }{{0, 0}, {1, 0}, {2, 1}, {39, 1}, {40, 2}, {139, 6}, {409, 20}} {
		df := []int{0, 1, tc.threshold, tc.threshold + 1}
		stats := buildCorpusStats(len(df), tc.n, float64(tc.n*3), df)
		for s, count := range df {
			want := tc.n >= 2 && count >= 1 && count <= tc.threshold
			if stats.RareSlot[s] != want {
				t.Errorf("N=%d df=%d rare=%v, want %v", tc.n, count, stats.RareSlot[s], want)
			}
		}
		wantAvg := 3.0
		if tc.n == 0 {
			wantAvg = 1
		}
		if stats.AvgDL != wantAvg {
			t.Errorf("avgdl=%v, want %v", stats.AvgDL, wantAvg)
		}
	}
	if buildCorpusStats(1, 2, 0, []int{0}).AvgDL != 1 {
		t.Fatal("empty readable documents need avgdl=1")
	}
}

func TestScoreAnalyzedDocumentAndSubsetCatalog(t *testing.T) {
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	gap := strings.Repeat(" x", 30) + " "
	for _, counts := range [][2]int{{2, 2}, {64, 64}, {65, 65}, {65, 2}} {
		text := strings.Repeat("alpha beta"+gap, counts[0]) + strings.Repeat("alpha gamma"+gap, counts[1])
		cat := analyzeRankedCatalog(text, query, 12, false, false)
		if cat.MaxTermCount != 2 || len(cat.Subsets) != 2 {
			t.Fatalf("catalog = %+v", cat)
		}
		for i, sub := range cat.Subsets {
			if counts[i] <= 64 && (len(sub.intervals) != counts[i] || sub.intervalOverflow) {
				t.Fatal("inline intervals lost")
			}
			if counts[i] > 64 && (!sub.intervalOverflow || sub.intervals != nil) {
				t.Fatal("overflow retained intervals")
			}
		}
		for _, weights := range [][]float64{buildCorpusStats(3, 100, 100, []int{20, 10, 1}).IDF, buildCorpusStats(3, 100, 100, []int{20, 1, 1}).IDF, {1, 2, 2 + 5e-13}} {
			reloads := 0
			got, ok := resolveRankedCatalog(cat, query, 12, false, false, nil, weights, func() (string, bool) { reloads++; return text, true })
			wantReloads, wantChunks := 0, counts[1]
			if math.Abs(weights[1]-weights[2]) <= 1e-12 {
				wantChunks += counts[0]
				if counts[0] > 64 || counts[1] > 64 {
					wantReloads = 1
				}
			}
			if !ok || reloads != wantReloads || got.TopQualityChunks != wantChunks {
				t.Fatalf("resolved=%+v ok=%v reloads=%d want=%d chunks=%d", got, ok, reloads, wantReloads, wantChunks)
			}
			if wantReloads > 0 {
				if _, ok := resolveRankedCatalog(cat, query, 12, false, false, nil, weights, func() (string, bool) { return "", false }); ok {
					t.Fatal("failed reload accepted")
				}
			}
		}
	}
	single := analyzeRankedCatalog("alpha beta"+gap+"alpha beta", query, 12, false, false)
	if len(single.Subsets) != 1 || single.Subsets[0].intervals != nil || single.Subsets[0].topQualityChunks != 2 {
		t.Fatalf("single subset retained intervals: %+v", single)
	}
	maximal := analyzeRankedCatalog("alpha beta"+gap+"alpha beta gamma", query, 20, false, true)
	if maximal.MaxTermCount != 3 || len(maximal.Subsets) != 1 {
		t.Fatalf("nonmaximal subsets retained: %+v", maximal)
	}
	fallbackQuery := compileQuery([]string{"conditions", "TEITAC"}, PartialModeOff)
	fallback := analyzeRankedCatalog("conditions"+gap+"TEITAC", fallbackQuery, 12, false, true)
	got, ok := resolveRankedCatalog(fallback, fallbackQuery, 12, false, true, []bool{false, true}, []float64{5, 1}, nil)
	if !ok || !reflect.DeepEqual(got.MatchedTerms, []string{"teitac"}) {
		t.Fatalf("rare gate: %+v %v", got, ok)
	}
	if _, ok := resolveRankedCatalog(fallback, fallbackQuery, 12, false, true, []bool{false, false}, []float64{5, 1}, nil); ok {
		t.Fatal("common-only fallback admitted")
	}
	// Independent arithmetic: tf=2, dl=4, avgdl=8, N=10, df=2; path contributes for all slots.
	stats := buildCorpusStats(3, 10, 80, []int{2, 5, 1})
	an := rankedTextAnalysis{TermCount: 1, SpanLength: 100, WordCount: 4, SlotTF: []int{2, 0, 0}, MatchedSlots: []bool{true, false, false}}
	idf := math.Log(1 + (10-2+0.5)/(2+0.5))
	if math.Abs(stats.IDF[0]-idf) > 1e-12 {
		t.Fatal("Robertson-Walker IDF mismatch")
	}
	lex := idf*(2*2.2)/(2+1.2*(0.25+0.75*4/8.0)) + 0.60*stats.IDF[2]
	want := math.Round(lex/3*1e4) / 1e4
	if score := scoreAnalyzedDocument(an, query, []bool{false, false, true}, stats, 20); score != want {
		t.Fatalf("single score=%v want %v", score, want)
	}
	an.TermCount = 2
	an.MatchedSlots[1] = true
	an.SlotTF[1] = 1
	lex += stats.IDF[1] * 2.2 / (1 + 1.2*(0.25+0.75*4/8.0))
	want = math.Round(lex*(1+0.35/(1+100/20.0))*2/3*1e4) / 1e4
	if score := scoreAnalyzedDocument(an, query, []bool{false, false, true}, stats, 20); score != want {
		t.Fatalf("multi score=%v want %v", score, want)
	}
	if canonicalFloatSum([]float64{1e16, 1, 1}, []string{"c", "a", "b"}) != canonicalFloatSum([]float64{1, 1e16, 1}, []string{"b", "c", "a"}) {
		t.Fatal("float sum depends on input order")
	}
	oneQuery := compileQuery([]string{"alpha"}, PartialModeOff)
	one, _ := analyzeRankedText("alpha alpha", oneQuery, 20, false)
	oneStats := buildCorpusStats(1, 2, 4, []int{1})
	for _, distance := range []int{0, 1, 20, 5000} {
		if score := scoreAnalyzedDocument(one, oneQuery, nil, oneStats, distance); score != RoundScore(math.Log(2)*4.4/3.2) {
			t.Fatalf("single-query proximity applied: %v", score)
		}
	}
	var baseline float64
	for _, terms := range [][]string{{"alpha", "beta", "gamma"}, {"alpha", "gamma", "beta"}, {"beta", "alpha", "gamma"}, {"beta", "gamma", "alpha"}, {"gamma", "alpha", "beta"}, {"gamma", "beta", "alpha"}} {
		q := compileQuery(terms, PartialModeOff)
		df := make([]int, 3)
		for s := range df {
			df[s] = map[string]int{"alpha": 2, "beta": 5, "gamma": 1}[q.slotLabel(s)]
		}
		stats := buildCorpusStats(3, 10, 80, df)
		a, ok := analyzeRankedTextWithOptions("alpha beta alpha", q, 20, false, false, nil, stats.IDF)
		if !ok {
			t.Fatal("weighted pair rejected")
		}
		path, count := pathMatchStats("/repo/gamma.txt", "/repo", q)
		if count != 1 {
			t.Fatalf("path count=%d", count)
		}
		score := scoreAnalyzedDocument(a, q, path, stats, 20)
		if baseline == 0 {
			baseline = score
		}
		if score != baseline {
			t.Fatalf("permutation %v score=%v want=%v", terms, score, baseline)
		}
	}
}

type rankingStaticExtractor struct {
	text string
}

func (e rankingStaticExtractor) ExtractText([]byte) (string, error) {
	return e.text, nil
}

type rankingCountingExtractor struct {
	text  string
	calls atomic.Int64
}

func (e *rankingCountingExtractor) ExtractText([]byte) (string, error) {
	e.calls.Add(1)
	return e.text, nil
}

type rankingReaderAtExtractor struct {
	rankingCountingExtractor
	readerCalls atomic.Int64
}

func (e *rankingReaderAtExtractor) ExtractReaderAt(r io.ReaderAt, size, limit int64) (string, error) {
	e.readerCalls.Add(1)
	reader, ok := r.(contextReaderAt)
	if !ok {
		return "", fmt.Errorf("expected cancellation-aware ReaderAt, got %T", r)
	}
	file := reader.r.(*os.File)
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	if size != stat.Size() || limit != DefaultMaxExtractedBytes {
		return "", fmt.Errorf("incorrect size or budget: %d, %d", size, limit)
	}
	return e.text, nil
}

func TestEngineBinaryExtractorDispatch(t *testing.T) {
	for _, readerAt := range []bool{false, true} {
		for _, terms := range [][]string{{"alpha"}, {"alpha", "beta"}} {
			for _, excluded := range []bool{false, true} {
				t.Run(fmt.Sprintf("readerAt=%v/%s/excluded=%v", readerAt, strings.Join(terms, "-"), excluded), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "binary.pdf")
					if err := os.WriteFile(path, []byte("opaque binary fixture"), 0o600); err != nil {
						t.Fatal(err)
					}
					legacy := &rankingCountingExtractor{text: "alpha beta"}
					if excluded {
						legacy.text += " forbidden"
					}
					ra := &rankingReaderAtExtractor{}
					ra.text = legacy.text
					engine := NewSearchEngine(terms, []string{"forbidden"}, nil, false, 1, 1000)
					engine.Silent = true
					engine.Registry.extractors["pdf"] = legacy
					if readerAt {
						engine.Registry.extractors["pdf"] = ra
					}
					checkCalls := func(want int64) {
						t.Helper()
						if readerAt {
							if ra.readerCalls.Load() != want || ra.calls.Load() != 0 {
								t.Fatalf("ReaderAt calls = %d, ExtractText calls = %d; want %d, 0", ra.readerCalls.Load(), ra.calls.Load(), want)
							}
						} else if legacy.calls.Load() != want {
							t.Fatalf("legacy calls = %d, want %d", legacy.calls.Load(), want)
						}
					}
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					checkCalls(1)
					if got := atomic.LoadInt64(&engine.pdfProcessed); got != 1 {
						t.Fatalf("Stage 2 PDF count = %d, want 1", got)
					}
					if excluded {
						if len(matches) != 0 {
							t.Fatal("excluded binary matched")
						}
						return
					}
					if len(matches) != 1 {
						t.Fatalf("Stage 2 matches = %d, want 1", len(matches))
					}
					results, err := engine.ExtractAndBuildResults(matches)
					if err != nil || len(results) != 1 {
						t.Fatalf("Stage 3 results = %+v, err = %v", results, err)
					}
					checkCalls(2)
					if results[0].CleanContent != "alpha beta" || len(results[0].RawExcerpts) == 0 || atomic.LoadInt64(&engine.pdfProcessed) != 1 {
						t.Fatalf("incorrect extracted result or PDF count: %+v", results[0])
					}
				})
			}
		}
	}
}

func TestLegacyBinaryExtractorRejectsOversizedFiles(t *testing.T) {
	const (
		limit        = 64 * 1024 * 1024
		oversize     = limit + 4096
		maxHeapDelta = 4 * 1024 * 1024
	)
	for _, terms := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		for _, stage := range []string{"filter", "extract"} {
			t.Run(strings.Join(terms, "-")+"/"+stage, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "oversized.eml")
				if err := os.WriteFile(path, []byte("opaque binary fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, oversize); err != nil {
					t.Fatal(err)
				}
				legacy := &rankingCountingExtractor{text: "alpha beta"}
				engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
				engine.Silent = true
				engine.MaxFileSize = limit
				engine.Registry.extractors["eml"] = legacy

				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				if stage == "filter" {
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil || len(matches) != 0 {
						t.Errorf("Stage 2 matches = %d, err = %v; want 0, nil", len(matches), err)
					}
				} else {
					results, err := engine.ExtractAndBuildResults([]candidateMatch{{filePath: path}})
					if err != nil || len(results) != 0 {
						t.Errorf("Stage 3 results = %d, err = %v; want 0, nil", len(results), err)
					}
				}
				runtime.ReadMemStats(&after)
				if delta := after.TotalAlloc - before.TotalAlloc; delta > maxHeapDelta {
					t.Errorf("oversized legacy binary check allocated %d bytes (> %d); expected descriptor stat rejection before read", delta, maxHeapDelta)
				}
				if calls := legacy.calls.Load(); calls != 0 {
					t.Errorf("legacy ExtractText calls = %d, want 0", calls)
				}
			})
		}
	}
}

func TestStage3BinaryEmailMetadata(t *testing.T) {
	for _, fixture := range []struct {
		name, ext, raw, date, subject string
		readerAt                      bool
	}{
		{"msg prefix", ".MSG", "Date: today\nSubject: prefix\n", "today", "prefix", true},
		{"msg beyond cap", ".msg", strings.Repeat(" ", 256*1024) + "\nDate: late\nSubject: late\n", "", "", true},
		{"legacy eml", ".eml", "Date: today\nSubject: legacy\n", "today", "legacy", false},
		{"legacy msg", ".msg", "Date: today\nSubject: legacy\n", "today", "legacy", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "email"+fixture.ext)
			if err := os.WriteFile(path, []byte(fixture.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 1000)
			engine.Silent = true
			var extractor Extractor = &rankingCountingExtractor{text: "alpha beta"}
			if fixture.readerAt {
				ra := &rankingReaderAtExtractor{}
				ra.text = "alpha beta"
				extractor = ra
			}
			engine.Registry.extractors[strings.TrimPrefix(strings.ToLower(fixture.ext), ".")] = extractor
			results, err := engine.ExtractAndBuildResults([]candidateMatch{{filePath: path}})
			if err != nil || len(results) != 1 {
				t.Fatalf("results = %+v, err = %v", results, err)
			}
			if results[0].EmailDate != fixture.date || results[0].EmailSubject != fixture.subject {
				t.Fatalf("metadata = %q, %q; want %q, %q", results[0].EmailDate, results[0].EmailSubject, fixture.date, fixture.subject)
			}
			if engine.emlExtractCount+engine.msgExtractCount != 1 {
				t.Fatal("Stage 3 email extraction was not counted")
			}
		})
	}
}

type rankingTimeoutReaderAtExtractor struct {
	rankingCountingExtractor
	entered chan *os.File
	release chan struct{}
	readErr chan error
}

func (e *rankingTimeoutReaderAtExtractor) ExtractReaderAt(r io.ReaderAt, _, _ int64) (string, error) {
	e.entered <- r.(contextReaderAt).r.(*os.File)
	<-e.release
	var buf [1]byte
	_, err := r.ReadAt(buf[:], 0)
	e.readErr <- err
	return "alpha", err
}

func TestExtractBinaryFileTimeoutKeepsReaderOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.pdf")
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	extractor := &rankingTimeoutReaderAtExtractor{
		entered: make(chan *os.File, 1), release: make(chan struct{}), readErr: make(chan error, 1),
	}
	defer close(extractor.release)
	engine := NewSearchEngine([]string{"alpha"}, nil, nil, false, 1, 1000)
	engine.FileTimeoutBinary = 20 * time.Millisecond
	cm := NewConcurrencyManager(1)
	text, _, _, _, timedOut, readErr, extractErr := engine.extractBinaryFile(cm, path, ".pdf", extractor, false)
	if !timedOut || readErr != nil || extractErr != nil || text != "" {
		t.Fatalf("timeout result = %q, %v, %v, %v", text, timedOut, readErr, extractErr)
	}
	if len(cm.sem) != 1 {
		t.Fatal("timeout released the active worker's heavy slot")
	}
	var file *os.File
	select {
	case file = <-extractor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("extractor never started")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("handle closed while extractor was active: %v", err)
	}
	extractor.release <- struct{}{}
	select {
	case err := <-extractor.readErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ReadAt after timeout: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extractor did not finish")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := file.Stat(); errors.Is(err, os.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed worker did not close its handle")
		}
		time.Sleep(time.Millisecond)
	}
}

type rankingBlockingExtractor struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (e *rankingBlockingExtractor) ExtractText([]byte) (string, error) {
	if e.calls.Add(1) == 1 {
		close(e.entered)
	}
	<-e.release
	return "alpha beta", nil
}

func TestFilterCandidatesBinaryReadHonorsHeavyConcurrency(t *testing.T) {
	for _, terms := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(terms, "-"), func(t *testing.T) {
			root := t.TempDir()
			first := filepath.Join(root, "first.pdf")
			gate := filepath.Join(root, "gate.txt")
			missing := filepath.Join(root, "missing.pdf")
			last := filepath.Join(root, "last.pdf")
			for _, path := range []string{first, gate, last} {
				if err := os.WriteFile(path, []byte("alpha beta"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			extractor := &rankingBlockingExtractor{
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			defer func() {
				select {
				case <-extractor.release:
				default:
					close(extractor.release)
				}
			}()
			engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
			engine.StartDir = root
			engine.Silent = true
			engine.FilterWorkers = 2
			engine.HeavyConcurrency = 1
			engine.FileTimeoutBinary = time.Minute
			engine.Registry.extractors["pdf"] = extractor
			gatePassed := make(chan struct{})
			missingProcessed := make(chan struct{})
			engine.OnProgress = func(_ string, _, _ int, path string) {
				switch path {
				case gate:
					// Hold the second worker until the first owns the heavy slot.
					<-extractor.entered
					close(gatePassed)
				case missing:
					close(missingProcessed)
				}
			}
			type result struct {
				matches []candidateMatch
				err     error
			}
			done := make(chan result, 1)
			go func() {
				matches, err := engine.FilterCandidates([]string{first, gate, missing, last}, 4, time.Now())
				done <- result{matches, err}
			}()
			select {
			case <-gatePassed:
			case <-time.After(5 * time.Second):
				t.Fatal("workers did not reach the extraction gate")
			}
			// A missing PDF has no streaming prefilter. Its read error must wait
			// for the occupied slot, not be processed before acquiring it.
			select {
			case <-missingProcessed:
				t.Error("binary read completed while the heavy slot was occupied")
			case <-time.After(100 * time.Millisecond):
			}
			close(extractor.release)
			select {
			case got := <-done:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if len(got.matches) != 3 {
					t.Errorf("matches = %d, want both PDFs and the text gate", len(got.matches))
				}
				if calls := extractor.calls.Load(); calls != 2 {
					t.Errorf("extraction calls = %d, want 2 (missing file must not extract)", calls)
				}
				select {
				case <-missingProcessed:
				default:
					t.Error("missing candidate was not processed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("filtering deadlocked: heavy slot leaked after read or extraction")
			}
		})
	}
}

func TestFilterCandidatesBinaryExcludeWordsExtractsOnce(t *testing.T) {
	for _, query := range []struct {
		name  string
		terms []string
	}{
		{name: "single term", terms: []string{"alpha"}},
		{name: "multiple terms", terms: []string{"alpha", "beta"}},
	} {
		for _, fixture := range []struct {
			name string
			text string
			want int
		}{
			{name: "allowed", text: "alpha beta", want: 1},
			{name: "excluded", text: "alpha beta forbidden", want: 0},
		} {
			t.Run(query.name+"/"+fixture.name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "binary.pdf")
				// Only the extractor's output should drive matching and exclusions.
				if err := os.WriteFile(path, []byte("opaque binary fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				extractor := &rankingCountingExtractor{text: fixture.text}
				engine := NewSearchEngine(query.terms, []string{"forbidden"}, nil, false, 1, 1000)
				engine.StartDir = root
				engine.Silent = true
				engine.Registry.extractors["pdf"] = extractor

				matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != fixture.want {
					t.Errorf("matches = %d, want %d", len(matches), fixture.want)
				}
				if got := extractor.calls.Load(); got != 1 {
					t.Errorf("ExtractText calls = %d, want 1", got)
				}
			})
		}
	}
}

func TestPathMatchCount(t *testing.T) {
	root := t.TempDir()
	query := compileQuery([]string{"ms-", "api"}, PartialModeContains)

	t.Run("absolute root control", func(t *testing.T) {
		tests := []struct {
			path string
			want int
		}{
			{path: filepath.Join(root, "app", "Services", "MsOptionApiClient.php"), want: 2},
			{path: filepath.Join(root, "app", "Utils", "ms_helper.php"), want: 1},
			{path: filepath.Join(root, "app", "Utils", "other.php"), want: 0},
		}
		for _, tt := range tests {
			if got := pathMatchCount(tt.path, root, query); got != tt.want {
				t.Errorf("pathMatchCount(%q) = %d, want %d", tt.path, got, tt.want)
			}
		}
	})

	t.Run("alphanumeric core fallback", func(t *testing.T) {
		path := filepath.Join(root, "MsOptionApiClient.php")
		if got := pathMatchCount(path, root, query); got != 2 {
			t.Fatalf("pathMatchCount(%q) = %d, want 2", path, got)
		}
		if got := pathMatchCount(filepath.Join(root, "xylophone.php"), root, compileQuery([]string{"x-"}, PartialModeContains)); got != 0 {
			t.Fatalf("one-byte core fallback count = %d, want 0", got)
		}
	})

	t.Run("case-sensitive fallback preserves case", func(t *testing.T) {
		caseQuery := compileQueryWithOptions(
			[]string{"API-"},
			PartialModeContains,
			QueryOptions{CaseSensitive: true},
		)
		if got := pathMatchCount(filepath.Join(root, "api-client.txt"), root, caseQuery); got != 0 {
			t.Fatalf("lowercase fallback count = %d, want 0", got)
		}
		if got := pathMatchCount(filepath.Join(root, "API-client.txt"), root, caseQuery); got != 1 {
			t.Fatalf("exact-case fallback count = %d, want 1", got)
		}
	})

	t.Run("default root uses current directory", func(t *testing.T) {
		cwd := t.TempDir()
		t.Chdir(cwd)
		path := filepath.Join(cwd, "app", "MsArea", "ApiClients", "other.php")
		if got := pathMatchCount(path, "", query); got != 2 {
			t.Fatalf("pathMatchCount(%q, default root) = %d, want 2", path, got)
		}
	})

	t.Run("relative root preserves descendants", func(t *testing.T) {
		cwd := t.TempDir()
		t.Chdir(cwd)
		const relativeRoot = "workspace"
		path := filepath.Join(cwd, relativeRoot, "app", "MsArea", "ApiClients", "other.php")
		if got := pathMatchCount(path, relativeRoot, query); got != 2 {
			t.Fatalf("pathMatchCount(%q, %q) = %d, want 2", path, relativeRoot, got)
		}
	})

	t.Run("outside root uses basename", func(t *testing.T) {
		base := t.TempDir()
		searchRoot := filepath.Join(base, "root")
		path := filepath.Join(base, "ms-api-parent", "other.php")
		if got := pathMatchCount(path, searchRoot, query); got != 0 {
			t.Fatalf("outside-root pathMatchCount(%q) = %d, want basename-only 0", path, got)
		}
	})

	t.Run("empty relative path uses basename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "MsApiRoot")
		if got := pathMatchCount(path, path, query); got != 2 {
			t.Fatalf("empty-relative pathMatchCount(%q) = %d, want basename count 2", path, got)
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("Windows separators and root case", func(t *testing.T) {
			base := t.TempDir()
			searchRoot := filepath.Join(base, "RepoRoot")
			path := filepath.Join(searchRoot, "app", "Services", "other.php")
			path = strings.ReplaceAll(path, `\`, "/")
			serviceQuery := compileQuery([]string{"services"}, PartialModeContains)
			if got := pathMatchCount(path, strings.ToUpper(searchRoot), serviceQuery); got != 1 {
				t.Fatalf("Windows pathMatchCount(%q) = %d, want 1", path, got)
			}
		})
	}
}

func TestTopQualityChunks(t *testing.T) {
	intervals := []rankedInterval{
		{start: 10, end: 30},
		{start: 20, end: 40},
		{start: 35, end: 50},
	}
	if got := topQualityChunkCount(intervals); got != 2 {
		t.Fatalf("topQualityChunkCount(%v) = %d, want 2", intervals, got)
	}
}

func TestTopQualityChunksFiltersScoreCollisionByTermCount(t *testing.T) {
	text := "aa bbbbbbbb " + strings.Repeat(" ", 30) +
		"aa bbb ccccc " + strings.Repeat(" ", 30) +
		"aa bbb ccccc"
	analysis, ok := analyzeRankedText(
		text,
		compileQuery([]string{"aa", "bbbbbbbb", "bbb", "ccccc"}, PartialModeOff),
		15,
		false,
	)
	if !ok {
		t.Fatal("analysis rejected qualifying text")
	}
	if analysis.Score != 10 || analysis.TermCount != 3 {
		t.Fatalf("best pair = (%v, %d), want (10, 3)", analysis.Score, analysis.TermCount)
	}
	if analysis.TopQualityChunks != 2 {
		t.Fatalf("TopQualityChunks = %d, want 2", analysis.TopQualityChunks)
	}
	if len(analysis.bestIntervals) != 2 {
		t.Fatalf("best intervals = %v, want only two three-term intervals", analysis.bestIntervals)
	}
}

func TestEngineStage2RankingMetadata(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	contentDir := filepath.Join(root, "MsArea", "ApiClients")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "ms-option-api" + strings.Repeat(" ", 40) + "ms-option-api"
	textPath := filepath.Join(contentDir, "text.txt")
	binaryPath := filepath.Join(contentDir, "binary.pdf")
	for _, path := range []string{textPath, binaryPath} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(parent)
	engine := NewSearchEngine([]string{"ms-", "api"}, nil, nil, false, 1, 1000)
	engine.StartDir = "workspace"
	engine.Partial = PartialModeContains
	engine.Distance = 20
	engine.Silent = true
	engine.Registry.extractors["pdf"] = rankingStaticExtractor{text: content}

	matches, err := engine.FilterCandidates([]string{textPath, binaryPath}, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want text and extracted binary", matches)
	}
	for _, match := range matches {
		if match.pathMatchCount != 2 ||
			match.boundaryQuality != 2 ||
			match.topQualityChunks != 2 {
			t.Errorf("%s metadata = path %d, boundary %d, top chunks %d; want 2, 2, 2",
				filepath.Base(match.filePath),
				match.pathMatchCount,
				match.boundaryQuality,
				match.topQualityChunks)
		}
	}
}

func TestAnalyzeRankedTextQualification(t *testing.T) {
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)

	if _, ok := analyzeRankedText("alpha beta", query, 20, true); ok {
		t.Fatal("strict analysis accepted a window missing gamma")
	}

	got, ok := analyzeRankedText("alpha beta", query, 20, false)
	if !ok {
		t.Fatal("relaxed analysis rejected primary plus one secondary")
	}
	if got.Score != 9 || got.TermCount != 2 || strings.Join(got.MatchedTerms, ",") != "alpha,beta" {
		t.Fatalf("relaxed analysis = (%v, %d, %v), want (9, 2, [alpha beta])", got.Score, got.TermCount, got.MatchedTerms)
	}

	if got, ok := analyzeRankedText("beta gamma", query, 20, false); !ok || got.Score != 9 || got.TermCount != 2 || strings.Join(got.MatchedTerms, ",") != "beta,gamma" {
		t.Fatalf("missing-first relaxed analysis = %+v, ok=%v", got, ok)
	}
}

func TestAnalyzeRankedTextTwoSidedMinimalWindows(t *testing.T) {
	t.Run("redundant trailing duplicate is not a minimal window", func(t *testing.T) {
		text := "ms-" + strings.Repeat(" ", 7) + "api" + strings.Repeat(" ", 87) + "api"
		got, ok := analyzeRankedText(text, compileQuery([]string{"ms-", "api"}, PartialModeContains), 100, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.SpanLength != 13 || got.start != 0 || got.end != 13 {
			t.Fatalf("representative = [%d,%d), span %d; want [0,13), span 13", got.start, got.end, got.SpanLength)
		}
		assertRankedIntervals(t, got.bestIntervals, []rankedInterval{{start: 0, end: 13}})
	})

	t.Run("later whole identifier occurrence contributes once", func(t *testing.T) {
		text := "anchor rapid ms-option-api tail"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"anchor", "api", "tail"}, PartialModeContains),
			100,
			true,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 3 {
			t.Fatalf("BoundaryQuality = %d, want 3", got.BoundaryQuality)
		}
	})
}

func TestAnalyzeRankedTextRepresentativePrecedence(t *testing.T) {
	t.Run("score before all other fields", func(t *testing.T) {
		text := "root x " + strings.Repeat("z", 30) + " root longword"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"root", "longword", "x"}, PartialModeOff),
			15,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.Score != 12 || strings.Join(got.MatchedTerms, ",") != "root,longword" {
			t.Fatalf("representative = score %v terms %v, want score 12 [root longword]", got.Score, got.MatchedTerms)
		}
	})

	t.Run("term count after equal score", func(t *testing.T) {
		text := "aa bbbbbbbb " + strings.Repeat("z", 30) + " aa bbb ccccc"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"aa", "bbbbbbbb", "bbb", "ccccc"}, PartialModeOff),
			15,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.Score != 10 || got.TermCount != 3 || strings.Join(got.MatchedTerms, ",") != "aa,bbb,ccccc" {
			t.Fatalf("representative = (%v, %d, %v), want (10, 3, [aa bbb ccccc])", got.Score, got.TermCount, got.MatchedTerms)
		}
	})

	t.Run("boundary quality before span and start", func(t *testing.T) {
		text := "cms-rapid " + strings.Repeat(" ", 20) +
			"cms-option-api " + strings.Repeat(" ", 20) +
			"ms-option-api"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"ms-", "api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		wantStart := strings.LastIndex(text, "ms-option-api")
		if got.BoundaryQuality != 2 || got.start != wantStart {
			t.Fatalf("representative = start %d quality %d, want start %d quality 2", got.start, got.BoundaryQuality, wantStart)
		}
		if len(got.bestIntervals) != 3 {
			t.Fatalf("best interval count = %d, want 3", len(got.bestIntervals))
		}
	})

	t.Run("span before start", func(t *testing.T) {
		text := "alpha-----beta " + strings.Repeat(" ", 20) + "alpha beta"
		got, ok := analyzeRankedText(text, compileQuery([]string{"alpha", "beta"}, PartialModeContains), 20, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		wantStart := strings.LastIndex(text, "alpha beta")
		if got.SpanLength != len("alpha beta") || got.start != wantStart {
			t.Fatalf("representative = start %d span %d, want start %d span %d", got.start, got.SpanLength, wantStart, len("alpha beta"))
		}
	})

	t.Run("earliest start breaks an exact representative tie", func(t *testing.T) {
		text := "alpha beta " + strings.Repeat(" ", 20) + "alpha beta"
		got, ok := analyzeRankedText(text, compileQuery([]string{"alpha", "beta"}, PartialModeOff), 20, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.start != 0 || got.end != len("alpha beta") {
			t.Fatalf("representative = [%d,%d), want [0,%d)", got.start, got.end, len("alpha beta"))
		}
		if len(got.bestIntervals) != 2 {
			t.Fatalf("best interval count = %d, want 2", len(got.bestIntervals))
		}
	})
}

func TestAnalyzeRankedTextBoundaryQualityUTF8(t *testing.T) {
	t.Run("non-ASCII surrounding byte qualifies", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"éapi",
			compileQuery([]string{"api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("non-ASCII following byte qualifies", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"x.apié",
			compileQuery([]string{".api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("invalid surrounding byte qualifies", func(t *testing.T) {
		text := string([]byte{0xff}) + "api"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("non-ASCII first byte is not self punctuation", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"aéx",
			compileQuery([]string{"éx"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 0 {
			t.Fatalf("BoundaryQuality = %d, want 0", got.BoundaryQuality)
		}
	})

	t.Run("ASCII punctuation qualifies both self edges", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"a.api-z",
			compileQuery([]string{".api-"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected ASCII-punctuation term")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})
}

func TestRankedBestWindowsRetainsOnlyCurrentBestPair(t *testing.T) {
	var best rankedBestWindows
	for i := 0; i < 100; i++ {
		start := i * 10
		best.observe(rankedWindow{score: 10, termCount: 2, start: start, end: start + 5})
	}
	best.observe(rankedWindow{score: 20, termCount: 2, start: 2_000, end: 2_010})
	if len(best.intervals) != 1 {
		t.Fatalf("retained %d intervals after a better pair, want 1", len(best.intervals))
	}

	const inferiorWindows = 10_000
	for i := 0; i < inferiorWindows; i++ {
		start := 3_000 + i*10
		best.observe(rankedWindow{score: 10, termCount: 2, start: start, end: start + 5})
	}
	if len(best.intervals) != 1 {
		t.Fatalf("retained %d intervals after %d inferior windows, want 1", len(best.intervals), inferiorWindows)
	}

	for i := 0; i < 3; i++ {
		start := 200_000 + i*10
		best.observe(rankedWindow{score: 20, termCount: 2, start: start, end: start + 5})
	}
	if len(best.intervals) != 4 {
		t.Fatalf("retained %d intervals, want one original plus three tied-best intervals", len(best.intervals))
	}
}

func assertRankedIntervals(t *testing.T, got, want []rankedInterval) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("intervals = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interval %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

package search

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestExtractAndBuildResultsOpenFailureBackfill(t *testing.T) {
	engine, matches := stage3BackfillFixture(t, ".txt")
	if err := os.Remove(matches[0].filePath); err != nil {
		t.Fatal(err)
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
}

type failingStage3Extractor struct{}

func (failingStage3Extractor) ExtractText([]byte) (string, error) {
	return "", errors.New("registered extractor failure")
}

func TestExtractAndBuildResultsExtractorFailureBackfill(t *testing.T) {
	engine, matches := stage3BackfillFixture(t, ".pdf")
	engine.Registry.extractors["pdf"] = failingStage3Extractor{}
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
}

func TestExtractAndBuildResultsZeroExcerptFailureBackfill(t *testing.T) {
	engine, matches := stage3BackfillFixture(t, ".txt")
	if err := os.WriteFile(matches[0].filePath, []byte("no matching content"), 0o600); err != nil {
		t.Fatal(err)
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

package search

import (
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

package search_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"garp/search"
)

func TestEngineCanonicalTerms(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "service.txt")
	if err := os.WriteFile(fixture, []byte("ms-option-api"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	engine := search.NewSearchEngine(
		[]string{"ms-**-api", "MS-*-API"},
		nil,
		[]string{"-g", "*.txt"},
		false,
		1,
		1000,
	)
	engine.StartDir = root
	engine.Silent = true

	if got := engine.SearchWords; len(got) != 1 || got[0] != "ms-*-api" {
		t.Fatalf("SearchWords = %q, want [ms-*-api]", got)
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
			engine := search.NewSearchEngineWithWorkers(
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
			byFile := make(map[string]search.SearchResult, len(results))
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

	engine := search.NewSearchEngineWithWorkers(
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

package search_test

import (
	"os"
	"path/filepath"
	"testing"

	"garp/search"
)

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

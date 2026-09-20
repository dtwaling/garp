package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestRankedMultiTermIntegration(t *testing.T) {
	const (
		primary   = "query"
		secondary = "synchronization"
		tertiary  = "mutex"
	)
	words := []string{primary, secondary, tertiary}

	t.Run("Strategy A ranks qualifying documents and exclusions override score", func(t *testing.T) {
		root := t.TempDir()
		writeRankedIntegrationFiles(t, root, map[string]string{
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
		writeRankedIntegrationFiles(t, root, map[string]string{
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
		writeRankedIntegrationFiles(t, root, map[string]string{
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
		writeRankedIntegrationFiles(t, root, map[string]string{
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

	t.Run("heavy binary with primary and short secondary is pruned without its longest secondary", func(t *testing.T) {
		root := t.TempDir()
		writeRankedIntegrationFiles(t, root, map[string]string{
			"common-primary.eml": "From: sender@example.com\nTo: recipient@example.com\nSubject: partial\nContent-Type: text/plain; charset=utf-8\n\nquery mutex",
		})

		engine := search.NewSearchEngineWithWorkers(words, nil, []string{"-g", "*.eml"}, false, 1, 1000, 1)
		engine.StartDir = root
		engine.Distance = 100
		engine.Silent = true
		results, err := engine.Execute()
		if err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
		if len(results) != 0 {
			t.Fatalf("heavy binary results = %v, want no extraction candidate without longest secondary", resultFileNames(results))
		}
	})
}

func executeRankedIntegrationSearch(t *testing.T, root string, words, excludes []string, strict bool) []search.SearchResult {
	t.Helper()
	engine := search.NewSearchEngineWithWorkers(words, excludes, []string{"-g", "*.txt"}, false, 1, 1000, 1)
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

func writeRankedIntegrationFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func resultFileNames(results []search.SearchResult) []string {
	names := make([]string, len(results))
	for i, result := range results {
		names[i] = filepath.Base(result.FilePath)
	}
	return names
}

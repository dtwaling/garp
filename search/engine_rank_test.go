package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestResultRanking(t *testing.T) {
	tests := []struct {
		name        string
		searchWords []string
		partial     search.PartialMode
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
			partial:     search.PartialModeContains,
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

			engine := search.NewSearchEngineWithWorkers(
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

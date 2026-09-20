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
				"a-wider.txt":   "a " + strings.Repeat("x", 50) + " b",
				"z-tighter.txt": "a b",
			},
			wantOrder: []string{"z-tighter.txt", "a-wider.txt"},
		},
		{
			name:        "file path makes otherwise equal results deterministic",
			searchWords: []string{"a", "b"},
			files: map[string]string{
				"a-first.txt": "a b",
				"z-last.txt":  "a b",
			},
			wantOrder: []string{"a-first.txt", "z-last.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			engine := search.NewSearchEngineWithWorkers(
				tt.searchWords, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1,
			)
			engine.StartDir = root
			engine.Distance = 100
			engine.Silent = true

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != len(tt.wantOrder) {
				t.Fatalf("result count = %d, want %d", len(results), len(tt.wantOrder))
			}
			for i, want := range tt.wantOrder {
				if got := filepath.Base(results[i].FilePath); got != want {
					t.Fatalf("result %d = %q, want %q; results must rank by score, term count, span, then path", i, got, want)
				}
			}
		})
	}
}

package search_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"garp/search"
)

func TestPartialIntegration(t *testing.T) {
	root := t.TempDir()
	writePartialIntegrationFiles(t, root, map[string]string{
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
		mode  search.PartialMode
		want  []string
	}{
		{
			name:  "prefix returns deployment but not redeploy",
			words: []string{"deploy"},
			mode:  search.PartialModePrefix,
			want:  []string{"a_deployment.txt"},
		},
		{
			name:  "inline glob matches prefix with partial mode off",
			words: []string{"deploy*"},
			mode:  search.PartialModeOff,
			want:  []string{"a_deployment.txt"},
		},
		{
			name:  "contains includes redeploy",
			words: []string{"deploy"},
			mode:  search.PartialModeContains,
			want:  []string{"a_deployment.txt", "b_redeploy.txt"},
		},
		{
			name:  "prefix avoids catastrophic cat substrings",
			words: []string{"cat"},
			mode:  search.PartialModePrefix,
			want:  []string{"d_prefixes.txt", "e_whole_word.txt"},
		},
		{
			name:  "contains includes cat substrings",
			words: []string{"cat"},
			mode:  search.PartialModeContains,
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
			search.PartialModePrefix,
			50,
		)
		assertPartialIntegrationFiles(t, engine, []string{"code_match.go"})
	})
}

func newPartialIntegrationEngine(root string, words, excludes, fileTypes []string, includeCode bool, mode search.PartialMode, distance int) *search.SearchEngine {
	engine := search.NewSearchEngineWithWorkers(words, excludes, fileTypes, includeCode, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = distance
	engine.Partial = mode
	engine.Silent = true
	return engine
}

func assertPartialIntegrationFiles(t *testing.T, engine *search.SearchEngine, want []string) {
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

func writePartialIntegrationFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

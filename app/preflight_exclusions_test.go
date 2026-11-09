package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreflightExtensionExclusionsCLIAndTUI(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"skip.TXT", "skip.EmL", "keep.md", ".venv/hidden.md", "venv/hidden.md"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("anchor forbidden"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, noIgnore := range []bool{false, true} {
		name := "ignore"
		if noIgnore {
			name = "no-ignore"
		}
		t.Run(name, func(t *testing.T) {
			argv := []string{"anchor", "--startdir", root, "--not", ".tXt", ".EML", "forbidden"}
			if noIgnore {
				argv = append(argv, "--no-ignore")
			}
			args := parseArguments(argv)
			if args.ParseErr != nil {
				t.Fatal(args.ParseErr)
			}
			m := model{searchWords: args.SearchWords, excludeWords: args.ExcludeWords, startDir: root, noIgnore: noIgnore}
			cliCount, err := preflightFileCount(args)
			if err != nil || cliCount != 1 {
				t.Fatalf("CLI count = %d, %v; want 1", cliCount, err)
			}
			tuiCount, err := m.preflightFileCount()
			if err != nil || tuiCount != 1 {
				t.Fatalf("TUI count = %d, %v; want 1", tuiCount, err)
			}
			for _, engine := range []struct {
				name     string
				discover func(int) ([]string, int, error)
			}{
				{"CLI", newSilentSearchEngine(args).DiscoverCandidates},
				{"TUI", m.newSearchEngine().DiscoverCandidates},
			} {
				paths, total, err := engine.discover(1)
				if err != nil || total != 1 || len(paths) != 1 || paths[0] != filepath.Join(root, "keep.md") {
					t.Fatalf("%s discovery = %v, %d, %v; want keep.md only", engine.name, paths, total, err)
				}
			}
		})
	}
}

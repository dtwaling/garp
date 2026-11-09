package search

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"garp/config"
)

func TestWalkPrunesVirtualEnvironments(t *testing.T) {
	for _, noIgnore := range []bool{false, true} {
		name := "ignore"
		if noIgnore {
			name = "no-ignore"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{".venv", "venv"} {
				if !config.ShouldSkipDirectory(dir) {
					t.Errorf("%q is not a hard skip", dir)
				}
				writeWalkFixture(t, root, dir+"/nested/hidden.txt", "anchor")
				writeWalkFixture(t, root, "project/"+dir+"/hidden.txt", "anchor")
			}
			writeWalkFixture(t, root, ".config/visible.txt", "anchor")
			writeWalkFixture(t, root, "project/visible.txt", "anchor")
			options := WalkOptions{NoIgnore: noIgnore}
			want := []string{".config/visible.txt", "project/visible.txt"}
			if got := collectCandidates(t, root, nil, nil, options); !reflect.DeepEqual(got, want) {
				t.Fatalf("walk = %v, want %v", got, want)
			}
			count, err := GetDocumentFileCountWithOptions(nil, root, nil, options)
			if err != nil || count != len(want) {
				t.Fatalf("count = %d, %v; want %d", count, err, len(want))
			}
		})
	}
}

func TestExtensionExclusionsBeforeDiscoveryAndCount(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"skip.txt", "skip.TXT", "skip.EmL", "skip.PDF", "skip.go", "keep.md", "nested/keep.md", "keep.txt.md"} {
		writeWalkFixture(t, root, name, "anchor forbidden")
	}
	excludes := []string{".tXt", ".EML", ".pdf", ".GO", "forbidden", "md", ""}
	options := WalkOptions{ExcludeWords: excludes, UserGlobs: []UserGlob{{Pattern: "*"}}, PositiveGlobCodeEligibility: true}
	want := []string{"keep.md", "keep.txt.md", "nested/keep.md"}
	if got := collectCandidates(t, root, nil, nil, options); !reflect.DeepEqual(got, want) {
		t.Fatalf("walk visitor received %v, want %v", got, want)
	}
	count, err := GetDocumentFileCountWithOptions(nil, root, nil, options)
	if err != nil || count != len(want) {
		t.Fatalf("count = %d, %v; want %d", count, err, len(want))
	}
	roots, err := PrepareStartRoots([]string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err = GetDocumentFileCountForRoots(nil, roots, nil, options)
	if err != nil || count != len(want) {
		t.Fatalf("root count = %d, %v; want %d", count, err, len(want))
	}
	paths, err := FindFilesWithFirstWordPartialWithOptions("anchor", nil, root, nil, PartialModeOff, options)
	if err != nil {
		t.Fatal(err)
	}
	assertExclusionPaths(t, root, paths, want)
	se := NewSearchEngine([]string{"anchor"}, excludes, nil, false, 1, 1000)
	se.StartDir, se.Silent = root, true
	se.UserGlobs, se.PositiveGlobCodeEligibility = options.UserGlobs, true
	var progressPaths []string
	se.OnProgress = func(stage string, processed, total int, path string) {
		if stage == "discovery" {
			progressPaths = append(progressPaths, path)
		}
	}
	paths, total, err := se.DiscoverCandidates(count)
	if err != nil || total != len(want) {
		t.Fatalf("discovery total = %d, %v; want %d", total, err, len(want))
	}
	assertExclusionPaths(t, root, paths, want)
	assertExclusionPaths(t, root, progressPaths, want)
	// Stage 2 still rejects extension exclusions for callers supplying candidates directly.
	se.ExcludeWords = []string{".tXt"}
	matches, err := se.FilterCandidates([]string{filepath.Join(root, "skip.TXT")}, 1, time.Now())
	if err != nil || len(matches) != 0 {
		t.Fatalf("direct Stage 2 = %v, %v; want empty", matches, err)
	}
}

func assertExclusionPaths(t *testing.T, root string, paths, want []string) {
	t.Helper()
	got := make([]string, len(paths))
	for i, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		got[i] = filepath.ToSlash(relative)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

package search

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"garp/config"
)

const issue2Limit int64 = 256

func TestIssue2IntegrationRepositoryPolicy(t *testing.T) {
	root := buildIssue2Repository(t)
	fileTypes := config.BuildRipgrepFileTypes(true)
	options := WalkOptions{MaxFileSize: issue2Limit}

	want := []string{".env.example", "cap.txt", "nested/main.txt", "visible.txt"}
	candidates := assertIssue2CountDiscoveryParity(t, root, fileTypes, nil, options, want)

	noIgnoreWant := append(append([]string{}, want...),
		"ancestor-ignored.txt",
		"local-ignored.txt",
		"nested/local-ignored.txt",
		"repository-excluded.txt",
		"scoped/main.txt",
	)
	assertIssue2CountDiscoveryParity(
		t, root, fileTypes, nil, WalkOptions{NoIgnore: true, MaxFileSize: issue2Limit}, noIgnoreWant,
	)

	scopedWant := []string{"scoped/main.txt"}
	assertIssue2CountDiscoveryParity(t, root, fileTypes, []string{"scoped/"}, options, scopedWant)

	capFile := filepath.Join(root, "cap.txt")
	if err := os.WriteFile(capFile, append(issue2Prefix(), []byte(" alpha beta OUTSIDE_PREFIX")...), 0o600); err != nil {
		t.Fatalf("grow cap fixture: %v", err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, fileTypes, true, 1, 1000)
	engine.StartDir = root
	engine.MaxFileSize = issue2Limit
	engine.Strict = true
	engine.Silent = true
	matches, err := engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatalf("filter candidates: %v", err)
	}
	if len(matches) != len(candidates) {
		t.Fatalf("filtered matches = %d, want all %d discovered candidates", len(matches), len(candidates))
	}
	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatalf("extract results: %v", err)
	}
	if got := issue2RelativeResults(t, root, results); !reflect.DeepEqual(got, want) {
		t.Fatalf("result paths = %v, want %v", got, want)
	}

	capResult := issue2Result(t, results, capFile)
	if strings.Contains(capResult.CleanContent, "OUTSIDE_PREFIX") {
		t.Fatal("clean content contains text beyond the configured prefix")
	}
	for _, excerpt := range capResult.RawExcerpts {
		if strings.Contains(excerpt, "OUTSIDE_PREFIX") {
			t.Fatalf("excerpt contains text beyond the configured prefix: %q", excerpt)
		}
	}
}

func TestIssue2IntegrationLinkedRoot(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	physicalRoot := filepath.Join(repository, "search-root")
	logicalRoot := filepath.Join(base, "logical-root")

	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatalf("create repository marker: %v", err)
	}
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "ignored.txt\n")
	writeWalkFixture(t, physicalRoot, "ignored.txt", "alpha beta")
	writeWalkFixture(t, physicalRoot, "visible.txt", "alpha beta")
	makeTestSymlink(t, physicalRoot, logicalRoot)

	fileTypes := []string{"-g", "*.txt"}
	options := WalkOptions{MaxFileSize: issue2Limit}
	candidates := assertIssue2CountDiscoveryParity(
		t, logicalRoot, fileTypes, nil, options, []string{"visible.txt"},
	)
	if len(candidates) != 1 || candidates[0] != filepath.Join(logicalRoot, "visible.txt") {
		t.Fatalf("linked-root candidates = %v, want logical result path", candidates)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, fileTypes, false, 1, 1000)
	engine.StartDir = logicalRoot
	engine.MaxFileSize = issue2Limit
	engine.Strict = true
	engine.Silent = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("linked-root execute: %v", err)
	}
	if len(results) != 1 || results[0].FilePath != filepath.Join(logicalRoot, "visible.txt") {
		t.Fatalf("linked-root results = %+v, want logical visible.txt path", results)
	}
}

func buildIssue2Repository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	root := filepath.Join(repository, "work")

	writeIgnoreFixture(t, filepath.Join(repository, ".git", "info", "exclude"), "repository-excluded.txt\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "work/ancestor-ignored.txt\nwork/nested/\nwork/scoped/\n")
	writeIgnoreFixture(t, filepath.Join(root, ".ignore"), "local-ignored.txt\n")
	writeIgnoreFixture(t, filepath.Join(root, "nested", ".ignore"), "local-ignored.txt\n")
	if err := os.MkdirAll(filepath.Join(root, "nested", ".git"), 0o755); err != nil {
		t.Fatalf("create nested repository: %v", err)
	}

	for _, relative := range []string{
		".env",
		".env.example",
		"ancestor-ignored.txt",
		"local-ignored.txt",
		"nested/local-ignored.txt",
		"nested/main.txt",
		"node_modules/hidden.txt",
		"repository-excluded.txt",
		"scoped/main.txt",
		"visible.txt",
	} {
		writeWalkFixture(t, root, relative, "alpha beta")
	}
	writeWalkFixture(t, root, "cap.txt", string(issue2Prefix()))
	writeSizedDiscoveryFixture(t, root, "over-limit.txt", "alpha beta", issue2Limit+1)
	writeSizedDiscoveryFixture(t, root, "over-limit.pdf", "alpha beta", issue2Limit+1)
	return root
}

func issue2Prefix() []byte {
	prefix := []byte("alpha " + strings.Repeat("x", 32) + " beta")
	return append(prefix, []byte(strings.Repeat(" ", int(issue2Limit)-len(prefix)))...)
}

func assertIssue2CountDiscoveryParity(
	t *testing.T,
	root string,
	fileTypes, pathScope []string,
	options WalkOptions,
	want []string,
) []string {
	t.Helper()
	sort.Strings(want)

	count, err := GetDocumentFileCountWithOptions(fileTypes, root, pathScope, options)
	if err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	candidates, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"alpha", "beta"}, fileTypes, 2, nil, root, pathScope, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("discover candidates: %v", err)
	}
	got := issue2RelativePaths(t, root, candidates)
	if count != len(candidates) {
		t.Fatalf("count/discovery = %d/%d, want parity", count, len(candidates))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	return candidates
}

func issue2RelativePaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	relative := make([]string, len(paths))
	for i, path := range paths {
		value, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative path for %q: %v", path, err)
		}
		relative[i] = filepath.ToSlash(value)
	}
	sort.Strings(relative)
	return relative
}

func issue2RelativeResults(t *testing.T, root string, results []SearchResult) []string {
	t.Helper()
	paths := make([]string, len(results))
	for i := range results {
		paths[i] = results[i].FilePath
	}
	return issue2RelativePaths(t, root, paths)
}

func issue2Result(t *testing.T, results []SearchResult, path string) SearchResult {
	t.Helper()
	for _, result := range results {
		if result.FilePath == path {
			return result
		}
	}
	t.Fatalf("missing result %q", path)
	return SearchResult{}
}

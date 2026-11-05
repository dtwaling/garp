package search

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"garp/config"
)

func writeWalkFixture(t *testing.T, root, relative, content string) string {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestWalkCandidatesRejectsMultiRootCompatibilityInput(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	err := walkCandidates(nil, first+","+second, nil, WalkOptions{}, func(string, os.FileInfo) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "single-root walk") {
		t.Fatalf("walkCandidates multi-root error = %v, want explicit rejection", err)
	}
}

func TestWalkAbsolutePositiveScopeExemptsIgnoredDirectChildAndPrunesSibling(t *testing.T) {
	root := t.TempDir()
	target := writeWalkFixture(t, root, "ignored/match.txt", "needle")
	writeWalkFixture(t, root, "ignored-sibling/other.txt", "needle")
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(root, "ignored", "**")}
	var visited []string
	err := walkCandidates(
		[]string{"-g", "*.txt"},
		root,
		pathScope,
		WalkOptions{},
		func(path string, _ os.FileInfo) error {
			visited = append(visited, path)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visited, []string{target}) {
		t.Fatalf("visited = %q, want ignored target only", visited)
	}
	policy, err := CompilePathPolicy(pathScope, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.exemptsDirectChild("ignored") {
		t.Fatal("absolute scope did not preserve root-relative direct-child metadata")
	}
	if policy.exemptsDirectChild("ignored-sibling") {
		t.Fatal("absolute scope falsely exempted sibling-name directory")
	}
	if policy.CouldMatchDirectory(filepath.Join(root, "ignored-sibling")) {
		t.Fatal("absolute scope did not prune unrelated sibling")
	}
}

func TestWalkAbsoluteWildcardAboveRootExemptsIgnoredDirectChild(t *testing.T) {
	root := t.TempDir()
	target := writeWalkFixture(t, root, "ignored/match.txt", "needle")
	writeWalkFixture(t, root, "other/miss.txt", "needle")
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(filepath.Dir(root), "*", "ignored", "**")}
	var visited []string
	err := walkCandidates(
		[]string{"-g", "*.txt"},
		root,
		pathScope,
		WalkOptions{},
		func(path string, _ os.FileInfo) error {
			visited = append(visited, path)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visited, []string{target}) {
		t.Fatalf("visited = %q, want ignored target only", visited)
	}
}

func TestWalkRelativeNegativePatternsDoNotPruneSyntheticRoot(t *testing.T) {
	for _, pattern := range []string{"!.*", "!.*/", "!*"} {
		t.Run(pattern, func(t *testing.T) {
			root := t.TempDir()
			visible := writeWalkFixture(t, root, "visible.txt", "needle")
			writeWalkFixture(t, root, ".hidden/inside.txt", "needle")
			policy, err := CompilePathPolicy([]string{pattern}, nil, root, root)
			if err != nil {
				t.Fatal(err)
			}
			if !policy.CouldMatchRoot() {
				t.Fatalf("relative negative %q pruned the synthetic root", pattern)
			}
			var visited []string
			err = walkCandidates(
				[]string{"-g", "*.txt"},
				root,
				[]string{pattern},
				WalkOptions{},
				func(path string, _ os.FileInfo) error {
					visited = append(visited, path)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if pattern != "!*" && !reflect.DeepEqual(visited, []string{visible}) {
				t.Fatalf("visited = %q, want visible root file only", visited)
			}
			if pattern == "!*" && len(visited) != 0 {
				t.Fatalf("visited = %q, want wildcard-negative descendants excluded", visited)
			}
		})
	}
}

func collectCandidates(t *testing.T, root string, fileTypes, pathScope []string, options WalkOptions) []string {
	t.Helper()
	var paths []string
	if err := walkCandidates(fileTypes, root, pathScope, options, func(path string, _ os.FileInfo) error {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatalf("walkCandidates() error = %v", err)
	}
	return paths
}

func TestCandidateWalkRootNamedVendorIsTraversed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vendor")
	writeWalkFixture(t, root, "document.md", "anchor")

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"document.md"}) {
		t.Fatalf("candidates = %v, want root file", got)
	}
}

func TestCandidateWalkSkipsHardcodedDescendants(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "keep/document.md", "anchor")
	writeWalkFixture(t, root, "vendor/hidden.md", "anchor")
	writeWalkFixture(t, root, "node_modules/hidden.md", "anchor")

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{NoIgnore: true})
	if !reflect.DeepEqual(got, []string{"keep/document.md"}) {
		t.Fatalf("candidates = %v, want only non-skipped descendant", got)
	}
}

func TestCandidateWalkAbsoluteScopeCannotOverrideHardSkip(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "vendor/hidden.md", "anchor")
	writeWalkFixture(t, root, "keep/visible.md", "anchor")

	got := collectCandidates(
		t,
		root,
		[]string{"-g", "*.md"},
		[]string{filepath.Join(root, "vendor")},
		WalkOptions{NoIgnore: true},
	)
	if len(got) != 0 {
		t.Fatalf("absolute vendor scope bypassed hard skip: %v", got)
	}
}

func TestCandidateWalkSkipsDescendantLinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	targetDir := filepath.Join(base, "target")
	targetFile := writeWalkFixture(t, targetDir, "linked.md", "anchor")
	writeWalkFixture(t, root, "keep.md", "anchor")
	makeTestSymlink(t, targetDir, filepath.Join(root, "linked-dir"))
	makeTestSymlink(t, targetFile, filepath.Join(root, "linked-file.md"))

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"keep.md"}) {
		t.Fatalf("candidates = %v, want descendant links skipped", got)
	}
}

func TestCandidateWalkSkipsDeletedDescendant(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	deleted := filepath.Join(root, "z-deleted")
	writeWalkFixture(t, deleted, "document.md", "anchor")

	var got []string
	err := walkCandidates(nil, root, nil, WalkOptions{}, func(path string, _ os.FileInfo) error {
		got = append(got, filepath.Base(path))
		if err := os.RemoveAll(deleted); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkCandidates() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("candidates = %v, want deleted descendant skipped", got)
	}
}

func TestCandidateWalkSkipsUnreadableDescendant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not make a directory unreadable")
	}

	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	unreadable := filepath.Join(root, "z-unreadable")
	writeWalkFixture(t, unreadable, "document.md", "anchor")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if directory, err := os.Open(unreadable); err == nil {
		_ = directory.Close()
		t.Skip("current user can still read a mode-zero directory")
	}

	got := collectCandidates(t, root, nil, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("candidates = %v, want unreadable descendant skipped", got)
	}
}

func TestCandidateWalkDeterministicDepthFirstOrder(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"z.md", "b/two.md", "a/three.md", "a/one.md"} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	want := []string{"a/one.md", "a/three.md", "b/two.md", "z.md"}
	for i := 0; i < 3; i++ {
		if got := collectCandidates(t, root, nil, nil, WalkOptions{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d candidates = %v, want %v", i, got, want)
		}
	}
}

func TestCandidateWalkFiltersScopeAndSize(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "docs/keep.md", "anchor")
	writeWalkFixture(t, root, "docs/wrong.txt", "anchor")
	writeWalkFixture(t, root, "other/out.md", "anchor")
	large := writeWalkFixture(t, root, "docs/large.md", "anchor")
	if err := os.Truncate(large, 32); err != nil {
		t.Fatal(err)
	}

	got := collectCandidates(
		t,
		root,
		[]string{"-g", "*.md"},
		[]string{"docs/"},
		WalkOptions{MaxFileSize: 16},
	)
	if !reflect.DeepEqual(got, []string{"docs/keep.md"}) {
		t.Fatalf("candidates = %v, want filtered file", got)
	}
}

func TestCandidateWalkDefaultSizePolicy(t *testing.T) {
	root := t.TempDir()
	largeText := writeWalkFixture(t, root, "large.md", "anchor")
	largeBinary := writeWalkFixture(t, root, "large.pdf", "anchor")
	for _, filename := range []string{largeText, largeBinary} {
		if err := os.Truncate(filename, defaultTextMaxBytes+1); err != nil {
			t.Fatal(err)
		}
	}

	got := collectCandidates(t, root, nil, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"large.pdf"}) {
		t.Fatalf("candidates = %v, want only default-uncapped binary", got)
	}
	if got := (WalkOptions{}).TextMaxBytes(); got != defaultTextMaxBytes {
		t.Fatalf("TextMaxBytes() = %d, want %d", got, defaultTextMaxBytes)
	}
}

func TestCountDiscoveryParity(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	writeWalkFixture(t, root, "nested/b.md", "anchor")
	writeWalkFixture(t, root, "nested/c.txt", "anchor")
	options := WalkOptions{MaxFileSize: 64}
	types := []string{"-g", "*.md"}

	count, err := GetDocumentFileCountWithOptions(types, root, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := FindFilesWithFirstWordPartialWithOptions("anchor", types, root, nil, PartialModeOff, options)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"anchor"}, types, 2, nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(plain) != count || len(progress) != count {
		t.Fatalf("count/plain/progress = %d/%d/%d, want parity", count, len(plain), len(progress))
	}
}

func TestPositiveGlobAddsKnownCodeEligibilityOnly(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "image.png", "anchor")
	writeWalkFixture(t, root, "arbitrary.zzz", "anchor")
	writeWalkFixture(t, root, "readme.md", "anchor")

	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*"}},
		PositiveGlobCodeEligibility: true,
	}
	got := collectCandidates(t, root, config.BuildRipgrepFileTypes(false), nil, options)
	want := []string{"main.go", "readme.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want documents plus known code only %v", got, want)
	}
}

func TestGlobPolicyRemainsSeparateFromTypeGate(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "main.md", "anchor")
	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*.go"}},
		PositiveGlobCodeEligibility: true,
	}
	if got := collectCandidates(t, root, config.BuildRipgrepFileTypes(false), nil, options); !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Fatalf("positive code glob candidates = %v, want [main.go]", got)
	}
	options.PositiveGlobCodeEligibility = false
	if got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, options); len(got) != 0 {
		t.Fatalf("--only override candidates = %v, want none", got)
	}
}

func TestCodeTypeGateIntersectsUserGlobs(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "main.ts", "anchor")
	writeWalkFixture(t, root, "readme.md", "anchor")

	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*.go"}},
		PositiveGlobCodeEligibility: true,
	}
	got := collectCandidates(t, root, config.BuildRipgrepFileTypes(true), nil, options)
	if !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Fatalf("--code with user glob candidates = %v, want [main.go]", got)
	}
}

func TestStartDirErrorsRemainFatal(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	file := writeWalkFixture(t, base, "file.md", "anchor")

	for _, root := range []string{missing, file} {
		if _, err := GetDocumentFileCount(nil, root, nil); err == nil {
			t.Errorf("GetDocumentFileCount(%q) accepted invalid root", root)
		}
		if _, err := FindFilesWithFirstWordPartial("anchor", nil, root, nil, PartialModeOff); err == nil {
			t.Errorf("FindFilesWithFirstWordPartial(%q) accepted invalid root", root)
		}
	}
}

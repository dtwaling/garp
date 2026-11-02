package search

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
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

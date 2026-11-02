package search

import (
	"os"
	"path/filepath"
	"testing"
)

func makeTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
}

func requireSameFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("%q and %q do not identify the same file", left, right)
	}
}

func TestRootLinkResolvesPhysicalDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "logical")
	makeTestSymlink(t, target, link)

	logicalRoot, physicalRoot, err := resolveWalkRoot(link)
	if err != nil {
		t.Fatalf("resolveWalkRoot() error = %v", err)
	}
	wantLogical, err := filepath.Abs(link)
	if err != nil {
		t.Fatal(err)
	}
	if logicalRoot != filepath.Clean(wantLogical) {
		t.Errorf("logical root = %q, want %q", logicalRoot, filepath.Clean(wantLogical))
	}
	requireSameFile(t, physicalRoot, target)
}

func TestRootLinkPreservesLogicalOutputPrefix(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "logical")
	makeTestSymlink(t, target, link)

	logicalRoot, physicalRoot, err := resolveWalkRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	const relative = "sub"
	logicalPath := filepath.Join(logicalRoot, relative)
	physicalPath := filepath.Join(physicalRoot, relative)
	requireSameFile(t, logicalPath, physicalPath)
	if filepath.Dir(logicalPath) != logicalRoot {
		t.Fatalf("logical path %q lost root prefix %q", logicalPath, logicalRoot)
	}
}

func TestRootLinkRejectsBrokenTarget(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(base, "broken")
	makeTestSymlink(t, filepath.Join(base, "missing"), link)

	if _, _, err := resolveWalkRoot(link); err == nil {
		t.Fatal("resolveWalkRoot() accepted a broken root link")
	}
}

func TestRootLinkRejectsFileTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "file.txt")
	if err := os.WriteFile(target, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "logical")
	makeTestSymlink(t, target, link)

	if _, _, err := resolveWalkRoot(link); err == nil {
		t.Fatal("resolveWalkRoot() accepted a file-target root link")
	}
}

func TestDescendantLinkDetectsFileAndDirectorySymlinks(t *testing.T) {
	base := t.TempDir()
	targetDir := filepath.Join(base, "target-dir")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(base, "target.txt")
	if err := os.WriteFile(targetFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirLink := filepath.Join(base, "dir-link")
	makeTestSymlink(t, targetDir, dirLink)
	fileLink := filepath.Join(base, "file-link")
	makeTestSymlink(t, targetFile, fileLink)
	brokenLink := filepath.Join(base, "broken-link")
	makeTestSymlink(t, filepath.Join(base, "missing"), brokenLink)

	for _, link := range []string{dirLink, fileLink, brokenLink} {
		got, err := isDescendantLink(link)
		if err != nil {
			t.Fatalf("isDescendantLink(%q) error = %v", link, err)
		}
		if !got {
			t.Errorf("isDescendantLink(%q) = false, want true", link)
		}
	}

	got, err := isDescendantLink(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("isDescendantLink() marked a regular file as a link")
	}
}

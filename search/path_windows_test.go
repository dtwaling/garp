//go:build windows

package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func makeTestJunction(t *testing.T, target, link string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("directory junctions are unavailable: %v: %s", err, output)
	}
}

func TestRootLinkFollowsJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "logical")
	makeTestJunction(t, target, junction)

	logicalRoot, physicalRoot, err := resolveWalkRoot(junction)
	if err != nil {
		t.Fatalf("resolveWalkRoot() error = %v", err)
	}
	wantLogical, err := filepath.Abs(junction)
	if err != nil {
		t.Fatal(err)
	}
	if logicalRoot != filepath.Clean(wantLogical) {
		t.Errorf("logical root = %q, want %q", logicalRoot, filepath.Clean(wantLogical))
	}
	requireSameFile(t, physicalRoot, target)
}

func TestRootLinkRejectsBrokenJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "logical")
	makeTestJunction(t, target, junction)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	if _, _, err := resolveWalkRoot(junction); err == nil {
		t.Fatal("resolveWalkRoot() accepted a broken junction root")
	}
}

func TestDescendantLinkDetectsJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "junction")
	makeTestJunction(t, target, junction)

	got, err := isDescendantLink(junction)
	if err != nil {
		t.Fatalf("isDescendantLink() error = %v", err)
	}
	if !got {
		t.Error("isDescendantLink() = false for a directory junction")
	}
}

func TestRootLinkNormalizesWindowsFinalPathPrefix(t *testing.T) {
	tests := map[string]string{
		`\\?\C:\repo`:             `C:\repo`,
		`\\?\UNC\server\share`:    `\\server\share`,
		`C:\already-normalized`:   `C:\already-normalized`,
		`\\server\already-normal`: `\\server\already-normal`,
	}
	for input, want := range tests {
		if got := normalizeWindowsFinalPath(input); got != want {
			t.Errorf("normalizeWindowsFinalPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtendedLengthWindowsPath(t *testing.T) {
	tests := map[string]string{
		`C:\repo\file.md`:              `\\?\C:\repo\file.md`,
		`\\server\share\file.md`:       `\\?\UNC\server\share\file.md`,
		`\\?\C:\already-extended`:      `\\?\C:\already-extended`,
		`\\?\UNC\server\share\file.md`: `\\?\UNC\server\share\file.md`,
		`\\.\PhysicalDrive0`:           `\\.\PhysicalDrive0`,
		`relative\file.md`:             `relative\file.md`,
	}
	for input, want := range tests {
		if got := extendedLengthWindowsPath(input); got != want {
			t.Errorf("extendedLengthWindowsPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveWalkRootBeyondMAXPATH(t *testing.T) {
	root := t.TempDir()
	longRoot := root
	for len(longRoot) <= 300 {
		longRoot = filepath.Join(longRoot, strings.Repeat("segment", 5))
		if err := os.Mkdir(longRoot, 0o755); err != nil {
			t.Fatalf("create long walk root: %v", err)
		}
	}
	if len(longRoot) <= 260 {
		t.Fatalf("fixture path length = %d, want beyond MAX_PATH", len(longRoot))
	}

	logicalRoot, physicalRoot, err := resolveWalkRoot(longRoot)
	if err != nil {
		t.Fatalf("resolveWalkRoot() error = %v", err)
	}
	wantLogical, err := filepath.Abs(longRoot)
	if err != nil {
		t.Fatal(err)
	}
	if logicalRoot != filepath.Clean(wantLogical) {
		t.Errorf("logical root = %q, want %q", logicalRoot, filepath.Clean(wantLogical))
	}
	requireSameFile(t, physicalRoot, longRoot)
}

func TestCandidateWalkDiscoversRegularFileBeyondMAXPATH(t *testing.T) {
	root := t.TempDir()
	deep := root
	for len(filepath.Join(deep, "document.md")) <= 300 {
		deep = filepath.Join(deep, strings.Repeat("segment", 5))
		if err := os.Mkdir(deep, 0o755); err != nil {
			t.Fatalf("create long-path directory: %v", err)
		}
	}
	filename := filepath.Join(deep, "document.md")
	if err := os.WriteFile(filename, []byte("anchor"), 0o644); err != nil {
		t.Fatalf("write long-path file: %v", err)
	}
	if len(filename) <= 260 {
		t.Fatalf("fixture path length = %d, want beyond MAX_PATH", len(filename))
	}

	relative, err := filepath.Rel(root, filename)
	if err != nil {
		t.Fatal(err)
	}
	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	want := []string{filepath.ToSlash(relative)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

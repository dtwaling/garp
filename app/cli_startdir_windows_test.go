//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestStartDirWindowsCaseEquivalentRootsDeduplicate(t *testing.T) {
	root := t.TempDir()
	roots, err := search.PrepareStartRoots([]string{root, strings.ToUpper(root)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 {
		t.Fatalf("root count = %d, want 1", len(roots))
	}
}

func TestStartDirPrepareStartRootsAcceptsJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v: %s", err, output)
	}

	if _, err := search.PrepareStartRoots([]string{junction}, false); err != nil {
		t.Fatalf("PrepareStartRoots(junction) error = %v", err)
	}
}

func TestStartDirPrepareStartRootsAcceptsPathBeyondMAXPATH(t *testing.T) {
	root := t.TempDir()
	longRoot := root
	for len(longRoot) <= 300 {
		longRoot = filepath.Join(longRoot, strings.Repeat("segment", 5))
		if err := os.Mkdir(longRoot, 0o755); err != nil {
			t.Fatalf("create long search root: %v", err)
		}
	}

	if _, err := search.PrepareStartRoots([]string{longRoot}, false); err != nil {
		t.Fatalf("PrepareStartRoots(long path) error = %v", err)
	}
}

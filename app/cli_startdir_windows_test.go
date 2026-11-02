//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSearchRootAcceptsJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("directory junctions are unavailable: %v: %s", err, output)
	}

	if err := validateSearchRoot(junction); err != nil {
		t.Fatalf("validateSearchRoot(junction) error = %v", err)
	}
}

func TestValidateSearchRootAcceptsPathBeyondMAXPATH(t *testing.T) {
	root := t.TempDir()
	longRoot := root
	for len(longRoot) <= 300 {
		longRoot = filepath.Join(longRoot, strings.Repeat("segment", 5))
		if err := os.Mkdir(longRoot, 0o755); err != nil {
			t.Fatalf("create long search root: %v", err)
		}
	}

	if err := validateSearchRoot(longRoot); err != nil {
		t.Fatalf("validateSearchRoot(long path) error = %v", err)
	}
}

//go:build windows

package search

import (
	"os"
	"os/exec"
	"path/filepath"
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

//go:build !windows

package search

import (
	"fmt"
	"os"
	"path/filepath"
)

func canonicalPathKey(path string) string {
	return canonicalPathKeyForPlatform(path, false)
}

// resolveWalkRoot returns the caller-facing root and its resolved filesystem
// target. The logical root stays unchanged when the explicit root is a link.
func resolveWalkRoot(root string) (logicalRoot, physicalRoot string, err error) {
	logicalRoot, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve walk root %q: %w", root, err)
	}
	logicalRoot = filepath.Clean(logicalRoot)

	physicalRoot, err = filepath.EvalSymlinks(logicalRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve walk root %q: %w", root, err)
	}
	physicalRoot, err = filepath.Abs(physicalRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve physical walk root %q: %w", root, err)
	}
	physicalRoot = filepath.Clean(physicalRoot)

	if err := validateWalkRoot(physicalRoot); err != nil {
		return "", "", fmt.Errorf("invalid walk root %q: %w", root, err)
	}
	return logicalRoot, physicalRoot, nil
}

func validateWalkRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	if _, err := os.ReadDir(root); err != nil {
		return fmt.Errorf("read directory: %w", err)
	}
	return nil
}

// isDescendantLink reports whether path is a link that a walk must skip.
func isDescendantLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

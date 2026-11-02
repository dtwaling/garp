//go:build windows

package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// resolveWalkRoot returns the caller-facing root and its resolved filesystem
// target. CreateFile follows an explicit symlink or junction by default.
func resolveWalkRoot(root string) (logicalRoot, physicalRoot string, err error) {
	logicalRoot, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve walk root %q: %w", root, err)
	}
	logicalRoot = filepath.Clean(logicalRoot)

	name, err := windows.UTF16PtrFromString(logicalRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve walk root %q: %w", root, err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", "", fmt.Errorf("resolve walk root %q: %w", root, err)
	}
	defer windows.CloseHandle(handle)

	physicalRoot, err = finalPathByHandle(handle)
	if err != nil {
		return "", "", fmt.Errorf("resolve physical walk root %q: %w", root, err)
	}
	physicalRoot = filepath.Clean(normalizeWindowsFinalPath(physicalRoot))

	if err := validateWalkRoot(physicalRoot); err != nil {
		return "", "", fmt.Errorf("invalid walk root %q: %w", root, err)
	}
	return logicalRoot, physicalRoot, nil
}

func finalPathByHandle(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 512)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, length+1)
	}
}

func normalizeWindowsFinalPath(path string) string {
	const (
		extendedUNC = `\\?\UNC\`
		extended    = `\\?\`
	)
	if strings.HasPrefix(path, extendedUNC) {
		return `\\` + strings.TrimPrefix(path, extendedUNC)
	}
	return strings.TrimPrefix(path, extended)
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

// isDescendantLink reports whether path is a reparse point that a walk must skip.
func isDescendantLink(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

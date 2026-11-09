//go:build !unix && !windows

package search

import (
	"fmt"
	"os"
)

func openIgnoreMetadataFile(filename string) (*os.File, error) {
	// Best effort on platforms without a no-follow open: reject known special
	// files before opening and verify identity afterward. Unlike Unix's
	// nonblocking open, this cannot prevent blocking on a concurrent FIFO swap.
	before, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: filename, Err: fmt.Errorf("not a regular file")}
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, &os.PathError{Op: "open", Path: filename, Err: fmt.Errorf("file changed during open")}
	}
	return file, nil
}

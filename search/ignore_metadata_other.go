//go:build !unix && !windows

package search

import "os"

func openIgnoreMetadataFile(filename string) (*os.File, error) {
	return os.Open(filename)
}

package search

import (
	"fmt"
	"os"
)

// openRegularSearchFile rejects final-component symlinks and special files
// before callers read any content. Ancestor symlinks remain supported for
// logical search roots; this pathname helper does not provide root confinement.
func openRegularSearchFile(filename string) (*os.File, os.FileInfo, error) {
	// Share the no-follow, nonblocking platform open used for ignore metadata.
	file, err := openIgnoreMetadataFile(filename)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, &os.PathError{Op: "open", Path: filename, Err: fmt.Errorf("not a regular file")}
	}
	return file, info, nil
}

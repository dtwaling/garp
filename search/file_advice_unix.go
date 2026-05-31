//go:build linux

package search

import (
	"os"

	"golang.org/x/sys/unix"
)

func adviseSequential(file *os.File) error {
	if file == nil {
		return nil
	}
	return unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_SEQUENTIAL)
}

func adviseDontNeed(file *os.File) error {
	if file == nil {
		return nil
	}
	return unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED)
}

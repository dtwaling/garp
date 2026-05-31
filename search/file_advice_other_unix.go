//go:build unix && !linux

package search

import "os"

func adviseSequential(file *os.File) error {
	return nil
}

func adviseDontNeed(file *os.File) error {
	return nil
}

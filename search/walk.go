package search

import (
	"fmt"
	"os"
	"path/filepath"

	"garp/config"
)

const defaultTextMaxBytes int64 = 5 << 20

// WalkOptions controls candidate traversal policy. A zero MaxFileSize applies
// the default text limit while leaving binary formats uncapped.
type WalkOptions struct {
	NoIgnore    bool
	MaxFileSize int64
}

// TextMaxBytes returns the active text and code file limit.
func (options WalkOptions) TextMaxBytes() int64 {
	if options.MaxFileSize > 0 {
		return options.MaxFileSize
	}
	return defaultTextMaxBytes
}

type candidateVisitor func(logicalPath string, info os.FileInfo) error

// walkCandidates applies the common traversal and candidate eligibility policy.
func walkCandidates(
	fileTypes []string,
	walkRoot string,
	pathScope []string,
	options WalkOptions,
	visit candidateVisitor,
) error {
	if walkRoot == "" {
		walkRoot = "."
	}

	logicalRoot, physicalRoot, err := resolveWalkRoot(walkRoot)
	if err != nil {
		return err
	}

	matcher := newFileTypeMatcher(fileTypes)
	normScope := normalizePathScopePatterns(pathScope, logicalRoot)

	var walkDirectory func(logicalDir, physicalDir string, depth int) error
	walkDirectory = func(logicalDir, physicalDir string, depth int) error {
		entries, readErr := os.ReadDir(physicalDir)
		if readErr != nil {
			if depth == 0 {
				return fmt.Errorf("read walk root %q: %w", logicalRoot, readErr)
			}
			return nil
		}

		// os.ReadDir returns entries sorted by filename. Recursing in this loop
		// therefore produces a deterministic depth-first traversal.
		for _, entry := range entries {
			logicalPath := filepath.Join(logicalDir, entry.Name())
			physicalPath := filepath.Join(physicalDir, entry.Name())

			linked, linkErr := isDescendantLink(physicalPath)
			if linkErr != nil || linked {
				continue
			}

			if entry.IsDir() {
				if config.ShouldSkipDirectory(entry.Name()) {
					continue
				}
				if len(normScope) > 0 && !dirCouldMatchPathScope(logicalPath, logicalRoot, normScope) {
					continue
				}
				if err := walkDirectory(logicalPath, physicalPath, depth+1); err != nil {
					return err
				}
				continue
			}

			if entry.Type() != 0 {
				continue
			}
			if !matcher.allows(logicalPath) {
				continue
			}
			if !matchesPathScope(logicalPath, logicalRoot, normScope) {
				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				continue
			}
			if options.MaxFileSize > 0 {
				if info.Size() > options.MaxFileSize {
					continue
				}
			} else if !IsBinaryFormat(logicalPath) && info.Size() > options.TextMaxBytes() {
				continue
			}

			if err := visit(logicalPath, info); err != nil {
				return err
			}
		}
		return nil
	}

	return walkDirectory(logicalRoot, physicalRoot, 0)
}

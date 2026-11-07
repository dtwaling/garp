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
	NoIgnore                    bool
	MaxFileSize                 int64
	UserGlobs                   []UserGlob
	PositiveGlobCodeEligibility bool
}

// TextMaxBytes returns the active text and code file limit.
func (options WalkOptions) TextMaxBytes() int64 {
	if options.MaxFileSize > 0 {
		return options.MaxFileSize
	}
	return defaultTextMaxBytes
}

type candidateVisitor func(logicalPath string, info os.FileInfo) error

type preparedRootTraversal struct {
	root           StartRoot
	policy         *PathPolicy
	noIgnore       bool
	rootChain      ignoreChain
	directoryCache map[string]cachedTraversalDirectory
}

type cachedTraversalDirectory struct {
	chain     ignoreChain
	reachable bool
}

func prepareRootTraversal(root StartRoot, pathScope []string, userGlobs []UserGlob, noIgnore bool) (preparedRootTraversal, error) {
	policy, err := CompilePathPolicy(pathScope, userGlobs, root.LogicalPath, root.PhysicalPath)
	if err != nil {
		return preparedRootTraversal{}, err
	}
	traversal := preparedRootTraversal{
		root:     root,
		policy:   policy,
		noIgnore: noIgnore,
	}
	if !noIgnore {
		traversal.rootChain = initialIgnoreChain(root.PhysicalPath)
	}
	traversal.directoryCache = map[string]cachedTraversalDirectory{
		canonicalPathKey("."): {chain: traversal.rootChain, reachable: true},
	}
	return traversal, nil
}

// decideTraversalDirectory is the single directory-admission contract used by
// real walks and provenance inference.
func decideTraversalDirectory(
	traversal preparedRootTraversal,
	logicalPath, physicalPath, name string,
	depth int,
	chain ignoreChain,
) (ignoreChain, bool) {
	linked, linkErr := isDescendantLink(physicalPath)
	if linkErr != nil || linked {
		return ignoreChain{}, false
	}
	if config.ShouldSkipDirectory(name) {
		return ignoreChain{}, false
	}

	childChain := chain
	if !traversal.noIgnore {
		switch {
		case hasRepositoryBoundary(physicalPath):
			childChain = initialIgnoreChain(physicalPath)
		case depth == 0 && traversal.policy.exemptsDirectChild(name):
			childChain = chain.withoutPathScopeContainerExcludes(physicalPath).withLocalFiles(physicalPath)
		default:
			if ignoredByRules(chain.rules, physicalPath, true) {
				return ignoreChain{}, false
			}
			childChain = chain.withLocalFiles(physicalPath)
		}
	}
	if !traversal.policy.CouldMatchDirectory(logicalPath) {
		return ignoreChain{}, false
	}
	return childChain, true
}

func candidateSizeEligible(path string, size int64, options WalkOptions) bool {
	if options.MaxFileSize > 0 {
		return size <= options.MaxFileSize
	}
	return IsBinaryFormat(path) || size <= options.TextMaxBytes()
}

// walkCandidates applies the common traversal and candidate eligibility policy.
func walkCandidates(
	fileTypes []string,
	walkRoot string,
	pathScope []string,
	options WalkOptions,
	visit candidateVisitor,
) error {
	raw := []string{walkRoot}
	if walkRoot == "" {
		raw = nil
	}
	roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{
		NoIgnore:  options.NoIgnore,
		PathScope: pathScope,
		UserGlobs: options.UserGlobs,
	})
	if err != nil {
		return err
	}
	if len(roots) != 1 {
		return fmt.Errorf("single-root walk received %d effective roots", len(roots))
	}
	return walkPreparedCandidates(fileTypes, roots[0], pathScope, options, visit)
}

func walkPreparedCandidates(
	fileTypes []string,
	root StartRoot,
	pathScope []string,
	options WalkOptions,
	visit candidateVisitor,
) error {
	logicalRoot := root.LogicalPath
	physicalRoot := root.PhysicalPath
	matcher := newFileTypeMatcher(fileTypes)
	traversal, err := prepareRootTraversal(root, pathScope, options.UserGlobs, options.NoIgnore)
	if err != nil {
		return err
	}
	policy := traversal.policy
	if !policy.CouldMatchRoot() {
		return nil
	}

	var walkDirectory func(logicalDir, physicalDir string, depth int, chain ignoreChain) error
	walkDirectory = func(logicalDir, physicalDir string, depth int, chain ignoreChain) error {
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

			if entry.IsDir() {
				childChain, descend := decideTraversalDirectory(
					traversal,
					logicalPath,
					physicalPath,
					entry.Name(),
					depth,
					chain,
				)
				if !descend {
					continue
				}
				if err := walkDirectory(logicalPath, physicalPath, depth+1, childChain); err != nil {
					return err
				}
				continue
			}

			linked, linkErr := isDescendantLink(physicalPath)
			if linkErr != nil || linked {
				continue
			}
			if entry.Type() != 0 {
				continue
			}
			typeEligible := matcher.allows(logicalPath)
			if !typeEligible && options.PositiveGlobCodeEligibility &&
				len(policy.globPositive) > 0 && config.IsCodeFile(logicalPath) {
				typeEligible = policy.MatchesPositiveGlob(logicalPath)
			}
			if !typeEligible {
				continue
			}
			if !policy.MatchesFile(logicalPath) {
				continue
			}
			if !options.NoIgnore && ignoredByRules(chain.rules, physicalPath, false) {
				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				continue
			}
			if !candidateSizeEligible(logicalPath, info.Size(), options) {
				continue
			}

			if err := visit(logicalPath, info); err != nil {
				return err
			}
		}
		return nil
	}

	return walkDirectory(logicalRoot, physicalRoot, 0, traversal.rootChain)
}

func mustRelativePath(root, filename string) string {
	relative, err := filepath.Rel(root, filename)
	if err != nil {
		return filename
	}
	return relative
}

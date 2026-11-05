package search

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// UserGlob is a validated user-supplied --glob or --iglob value.
type UserGlob struct {
	Pattern    string
	Negated    bool
	IgnoreCase bool
}

// ValidateUserGlob validates one glob without splitting commas.
func ValidateUserGlob(raw string, ignoreCase bool) (UserGlob, error) {
	value := stripMatchingQuotes(strings.TrimSpace(raw))
	if value == "" {
		return UserGlob{}, fmt.Errorf("user glob value must not be empty")
	}
	negated := strings.HasPrefix(value, "!")
	if negated {
		value = strings.TrimPrefix(value, "!")
		if value == "" {
			return UserGlob{}, fmt.Errorf("user glob negation must include a pattern")
		}
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if isBareSlashPattern(value) {
		return UserGlob{}, fmt.Errorf("user glob pattern %q has no pattern before its directory marker", raw)
	}
	for _, rejected := range pathScopeRejectedChars {
		if strings.Contains(value, rejected) {
			return UserGlob{}, fmt.Errorf("user glob pattern %q contains illegal character %q", raw, rejected)
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return UserGlob{}, fmt.Errorf("user glob pattern %q contains parent traversal", raw)
		}
	}
	return UserGlob{Pattern: value, Negated: negated, IgnoreCase: ignoreCase}, nil
}

type compiledPathPattern struct {
	pattern               string
	physicalPattern       string
	ignoreCase            bool
	absolute              bool
	basename              bool
	rootRelative          bool
	directory             bool
	anchored              bool
	active                bool
	matchAll              bool
	literalPrefix         string
	physicalLiteralPrefix string
	parts                 []string
	physicalParts         []string
	rootRelativePatterns  []compiledPathPattern
	windowsPaths          bool
	logicalRoot           string
	physicalRoot          string
}

// PathPolicy is a path-only policy compiled once for one logical/physical root.
// File-type eligibility remains a separate walk gate.
type PathPolicy struct {
	logicalRoot   string
	physicalRoot  string
	scopePositive []compiledPathPattern
	globPositive  []compiledPathPattern
	negative      []compiledPathPattern
}

// CompilePathPolicy compiles pathscope and user globs for one walk root.
func CompilePathPolicy(pathScope []string, userGlobs []UserGlob, logicalRoot, physicalRoot string) (*PathPolicy, error) {
	policy := &PathPolicy{
		logicalRoot:  filepath.Clean(logicalRoot),
		physicalRoot: filepath.Clean(physicalRoot),
	}
	for _, raw := range pathScope {
		negated := strings.HasPrefix(raw, "!")
		pattern := strings.TrimPrefix(raw, "!")
		compiled, err := compilePathPattern(pattern, runtime.GOOS == "windows", policy.logicalRoot, policy.physicalRoot)
		if err != nil {
			return nil, err
		}
		if negated {
			policy.negative = append(policy.negative, compiled)
		} else {
			policy.scopePositive = append(policy.scopePositive, compiled)
		}
	}
	for _, glob := range userGlobs {
		compiled, err := compilePathPattern(glob.Pattern, glob.IgnoreCase, policy.logicalRoot, policy.physicalRoot)
		if err != nil {
			return nil, err
		}
		if glob.Negated {
			policy.negative = append(policy.negative, compiled)
		} else {
			policy.globPositive = append(policy.globPositive, compiled)
		}
	}
	return policy, nil
}

func compilePathPattern(raw string, ignoreCase bool, logicalRoot, physicalRoot string) (compiledPathPattern, error) {
	return compilePathPatternWithAbsolute(raw, ignoreCase, logicalRoot, physicalRoot, filepath.IsAbs)
}

func compilePathPatternWithAbsolute(
	raw string,
	ignoreCase bool,
	logicalRoot, physicalRoot string,
	isAbsolute func(string) bool,
) (compiledPathPattern, error) {
	pattern := strings.ReplaceAll(stripMatchingQuotes(strings.TrimSpace(raw)), `\`, "/")
	directory := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return compiledPathPattern{}, fmt.Errorf("empty path pattern")
	}

	active := true
	native := filepath.FromSlash(pattern)
	absolute := isAbsolute(native)
	rootRelative := false
	physicalPattern := ""
	var rootRelativePatterns []compiledPathPattern
	if absolute {
		for _, relative := range relativeAbsolutePatterns(native, logicalRoot, physicalRoot) {
			relativeCompiled, err := compileRootRelativePathPattern(
				filepath.ToSlash(relative),
				ignoreCase,
				directory,
				logicalRoot,
				physicalRoot,
			)
			if err == nil {
				rootRelativePatterns = append(rootRelativePatterns, relativeCompiled)
			}
		}
		pattern = filepath.ToSlash(filepath.Clean(native))
		physicalPattern = filepath.ToSlash(resolvePatternForMatching(native))
	} else {
		pattern, rootRelative = normalizeRelativePathPattern(pattern)
	}
	anchored := absolute || rootRelative || directory || strings.Contains(pattern, "/")
	if ignoreCase {
		pattern = strings.ToLower(pattern)
		physicalPattern = strings.ToLower(physicalPattern)
	}
	matchAll := pattern == "."
	var parts []string
	if anchored {
		parts = splitPathScopeParts(pattern)
	}
	var physicalParts []string
	if absolute && physicalPattern != pattern {
		physicalParts = splitPathScopeParts(physicalPattern)
	}
	return compiledPathPattern{
		pattern:               pattern,
		physicalPattern:       physicalPattern,
		ignoreCase:            ignoreCase,
		absolute:              absolute,
		basename:              !anchored,
		directory:             directory,
		anchored:              anchored,
		active:                active,
		matchAll:              matchAll,
		literalPrefix:         pathScopeLiteralPrefix(pattern),
		physicalLiteralPrefix: pathScopeLiteralPrefix(physicalPattern),
		parts:                 parts,
		physicalParts:         physicalParts,
		rootRelativePatterns:  rootRelativePatterns,
		windowsPaths:          runtime.GOOS == "windows",
		logicalRoot:           filepath.ToSlash(logicalRoot),
		physicalRoot:          filepath.ToSlash(physicalRoot),
	}, nil
}

func compileRootRelativePathPattern(
	raw string,
	ignoreCase, directory bool,
	logicalRoot, physicalRoot string,
) (compiledPathPattern, error) {
	compiled, err := compilePathPatternWithAbsolute(
		raw,
		ignoreCase,
		logicalRoot,
		physicalRoot,
		func(string) bool { return false },
	)
	if err != nil {
		return compiledPathPattern{}, err
	}
	compiled.rootRelative = true
	compiled.basename = false
	compiled.anchored = true
	compiled.directory = directory
	compiled.parts = splitPathScopeParts(compiled.pattern)
	return compiled, nil
}

func normalizeRelativePathPattern(pattern string) (string, bool) {
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	anchored := strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "./")
	return strings.TrimLeft(pattern, "/"), anchored
}

func relativeAbsolutePatterns(pattern, logicalRoot, physicalRoot string) []string {
	resolved := resolvePatternForMatching(pattern)
	seen := make(map[string]struct{})
	var relatives []string
	for _, candidate := range []string{pattern, resolved} {
		for _, root := range []string{logicalRoot, physicalRoot} {
			relative, contained := relativeContainedPathForPlatform(
				root,
				candidate,
				runtime.GOOS == "windows",
			)
			if contained {
				key := canonicalPathKey(relative)
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					relatives = append(relatives, relative)
				}
			}
			for _, relative := range rootSpanningRelativePatterns(
				candidate,
				root,
				runtime.GOOS == "windows",
			) {
				key := canonicalPathKey(relative)
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					relatives = append(relatives, relative)
				}
			}
		}
	}
	return relatives
}

func resolvePatternForMatching(pattern string) string {
	resolved := resolvePatternPhysicalPath(pattern)
	return selectResolvedPattern(pattern, resolved, runtime.GOOS == "windows")
}

func selectResolvedPattern(pattern, resolved string, windows bool) string {
	if windows &&
		canonicalPathKeyForPlatform(pattern, true) == canonicalPathKeyForPlatform(resolved, true) {
		return filepath.Clean(pattern)
	}
	return resolved
}

func selectResolvedSegmentForPlatform(parent, userSegment, resolved string, windows bool) (string, bool) {
	expected := filepath.Join(parent, userSegment)
	if canonicalPathKeyForPlatform(expected, windows) == canonicalPathKeyForPlatform(resolved, windows) {
		return expected, false
	}
	return filepath.Clean(resolved), true
}

func rootSpanningRelativePatterns(pattern, root string, foldCase bool) []string {
	patternParts := splitPathScopeParts(filepath.ToSlash(pattern))
	rootParts := splitPathScopeParts(filepath.ToSlash(root))
	indices := patternIndicesAtRoot(patternParts, rootParts, 0, 0, foldCase)
	if len(indices) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(indices))
	relatives := make([]string, 0, len(indices))
	for _, patternIndex := range indices {
		relative := "."
		if patternIndex < len(patternParts) {
			relative = filepath.FromSlash(strings.Join(patternParts[patternIndex:], "/"))
		}
		key := canonicalPathKey(relative)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		relatives = append(relatives, relative)
	}
	return relatives
}

func patternIndicesAtRoot(
	patternParts, rootParts []string,
	patternIndex, rootIndex int,
	foldCase bool,
) []int {
	if rootIndex == len(rootParts) {
		return []int{patternIndex}
	}
	if patternIndex == len(patternParts) {
		return []int{patternIndex}
	}
	if patternParts[patternIndex] == "**" {
		indices := patternIndicesAtRoot(
			patternParts,
			rootParts,
			patternIndex+1,
			rootIndex,
			foldCase,
		)
		return append(
			indices,
			patternIndicesAtRoot(patternParts, rootParts, patternIndex, rootIndex+1, foldCase)...,
		)
	}
	patternPart := patternParts[patternIndex]
	rootPart := rootParts[rootIndex]
	if foldCase {
		patternPart = strings.ToLower(patternPart)
		rootPart = strings.ToLower(rootPart)
	}
	matched, err := path.Match(patternPart, rootPart)
	if err != nil || !matched {
		return nil
	}
	return patternIndicesAtRoot(patternParts, rootParts, patternIndex+1, rootIndex+1, foldCase)
}

func relativeContainedPathForPlatform(root, candidate string, windows bool) (string, bool) {
	if !windows {
		relative, err := filepath.Rel(root, candidate)
		return relative, err == nil && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	root = strings.TrimRight(filepath.ToSlash(filepath.Clean(strings.ReplaceAll(root, `\`, "/"))), "/")
	candidate = strings.TrimRight(filepath.ToSlash(filepath.Clean(strings.ReplaceAll(candidate, `\`, "/"))), "/")
	if strings.EqualFold(root, candidate) {
		return ".", true
	}
	prefix := root + "/"
	if len(candidate) <= len(prefix) || !strings.EqualFold(candidate[:len(prefix)], prefix) {
		return "", false
	}
	return filepath.FromSlash(candidate[len(prefix):]), true
}

func (policy *PathPolicy) MatchesFile(filename string) bool {
	relative, err := filepath.Rel(policy.logicalRoot, filename)
	if err != nil {
		return false
	}
	relativePath := filepath.ToSlash(relative)
	logicalPath := filepath.ToSlash(filepath.Clean(filename))
	physicalPath := filepath.ToSlash(filepath.Join(policy.physicalRoot, relative))
	return matchesPositiveCandidates(policy.scopePositive, relativePath, logicalPath, physicalPath) &&
		matchesPositiveCandidates(policy.globPositive, relativePath, logicalPath, physicalPath) &&
		!matchesAnyCandidate(policy.negative, relativePath, logicalPath, physicalPath, false)
}

func (policy *PathPolicy) MatchesPositiveGlob(filename string) bool {
	relative, err := filepath.Rel(policy.logicalRoot, filename)
	if err != nil {
		return false
	}
	return matchesAnyCandidate(
		policy.globPositive,
		filepath.ToSlash(relative),
		filepath.ToSlash(filepath.Clean(filename)),
		filepath.ToSlash(filepath.Join(policy.physicalRoot, relative)),
		false,
	)
}

func matchesPositiveCandidates(patterns []compiledPathPattern, relative, logical, physical string) bool {
	if len(patterns) == 0 {
		return true
	}
	return matchesAnyCandidate(patterns, relative, logical, physical, false)
}

func matchesAnyCandidate(patterns []compiledPathPattern, relative, logical, physical string, isDir bool) bool {
	for _, pattern := range patterns {
		if !pattern.active {
			continue
		}
		if !pattern.absolute && pattern.matches(relative, isDir) {
			return true
		}
		if pattern.absolute {
			for _, relativePattern := range pattern.rootRelativePatterns {
				if relativePattern.matches(relative, isDir) {
					return true
				}
			}
			if pattern.matchesAbsoluteCandidates(logical, physical, isDir) {
				return true
			}
		}
	}
	return false
}

func (pattern compiledPathPattern) matches(candidate string, isDir bool) bool {
	if pattern.matchAll {
		return true
	}
	candidate = strings.Trim(filepath.ToSlash(candidate), "/")
	if pattern.ignoreCase {
		candidate = strings.ToLower(candidate)
	}
	if pattern.basename {
		parts := splitPathScopeParts(candidate)
		limit := len(parts)
		if !isDir && !pattern.directory && limit > 0 {
			if matched, _ := path.Match(pattern.pattern, parts[limit-1]); matched {
				return true
			}
			limit--
		}
		for i := 0; i < limit; i++ {
			if matched, _ := path.Match(pattern.pattern, parts[i]); matched {
				return true
			}
		}
		return false
	}
	parts := splitPathScopeParts(candidate)
	ancestorLimit := len(parts)
	if !isDir {
		ancestorLimit--
	}
	if ancestorLimit < 0 {
		ancestorLimit = 0
	}
	for end := 1; end <= ancestorLimit; end++ {
		if pathScopePartsMatch(pattern.parts, parts[:end]) {
			return true
		}
	}
	if pattern.directory {
		return false
	}
	if pathScopePartsMatch(pattern.parts, parts) {
		return true
	}
	return scopePatternMatchesDirPrefix(pattern.pattern, candidate)
}

func (pattern compiledPathPattern) matchesPhysical(candidate string, isDir bool) bool {
	if pattern.physicalPattern == "" || pattern.physicalPattern == pattern.pattern {
		return pattern.matches(candidate, isDir)
	}
	physical := pattern
	physical.pattern = pattern.physicalPattern
	physical.parts = pattern.physicalParts
	return physical.matches(candidate, isDir)
}

func (pattern compiledPathPattern) matchesAbsoluteCandidates(logical, physical string, isDir bool) bool {
	if pattern.matches(logical, isDir) || pattern.matches(physical, isDir) {
		return true
	}
	if pattern.physicalPattern != "" && pattern.physicalPattern != pattern.pattern &&
		(pattern.matchesPhysical(logical, isDir) || pattern.matchesPhysical(physical, isDir)) {
		return true
	}
	if !pattern.windowsPaths || pattern.ignoreCase {
		return false
	}
	return pattern.matchesWithFoldedContainment(logical, isDir) ||
		pattern.matchesWithFoldedContainment(physical, isDir)
}

func (pattern compiledPathPattern) matchesWithFoldedContainment(candidate string, isDir bool) bool {
	type form struct {
		pattern string
		parts   []string
		prefix  string
	}
	forms := []form{
		{pattern.pattern, pattern.parts, pattern.literalPrefix},
		{pattern.physicalPattern, pattern.physicalParts, pattern.physicalLiteralPrefix},
	}
	for _, current := range forms {
		if current.pattern == "" || current.prefix == "" {
			continue
		}
		for _, root := range []string{pattern.logicalRoot, pattern.physicalRoot} {
			if !pathHasPrefixFold(root, current.prefix) {
				continue
			}
			normalized, ok := replacePathPrefixFold(candidate, current.prefix)
			if !ok {
				continue
			}
			compiled := pattern
			compiled.pattern = current.pattern
			compiled.parts = current.parts
			if compiled.matches(normalized, isDir) {
				return true
			}
		}
	}
	return false
}

func pathHasPrefixFold(pathValue, prefix string) bool {
	pathValue = strings.Trim(filepath.ToSlash(pathValue), "/")
	prefix = strings.Trim(filepath.ToSlash(prefix), "/")
	return strings.EqualFold(pathValue, prefix) ||
		(len(pathValue) > len(prefix) &&
			strings.EqualFold(pathValue[:len(prefix)], prefix) &&
			pathValue[len(prefix)] == '/')
}

func replacePathPrefixFold(candidate, prefix string) (string, bool) {
	candidate = strings.Trim(filepath.ToSlash(candidate), "/")
	prefix = strings.Trim(filepath.ToSlash(prefix), "/")
	if len(candidate) < len(prefix) || !strings.EqualFold(candidate[:len(prefix)], prefix) {
		return "", false
	}
	if len(candidate) > len(prefix) && candidate[len(prefix)] != '/' {
		return "", false
	}
	return prefix + candidate[len(prefix):], true
}

// CouldMatchDirectory reports whether descending can produce an eligible path.
func (policy *PathPolicy) CouldMatchDirectory(dirname string) bool {
	relative, err := filepath.Rel(policy.logicalRoot, dirname)
	if err != nil {
		return true
	}
	path := filepath.ToSlash(relative)
	logicalPath := filepath.ToSlash(filepath.Clean(dirname))
	physicalPath := filepath.ToSlash(filepath.Join(policy.physicalRoot, relative))
	if matchesAnyCandidate(policy.negative, path, logicalPath, physicalPath, true) {
		return false
	}
	return groupCouldMatchCandidateDirectory(policy.scopePositive, path, logicalPath, physicalPath) &&
		groupCouldMatchCandidateDirectory(policy.globPositive, path, logicalPath, physicalPath)
}

// CouldMatchRoot applies only absolute positive coverage. Relative negatives
// must not evaluate against the synthetic root-relative "." path.
func (policy *PathPolicy) CouldMatchRoot() bool {
	return absolutePositiveGroupCouldMatchRoot(policy.scopePositive, policy.logicalRoot, policy.physicalRoot) &&
		absolutePositiveGroupCouldMatchRoot(policy.globPositive, policy.logicalRoot, policy.physicalRoot)
}

func absolutePositiveGroupCouldMatchRoot(patterns []compiledPathPattern, logicalRoot, physicalRoot string) bool {
	if len(patterns) == 0 {
		return true
	}
	hasRelative := false
	for _, pattern := range patterns {
		if !pattern.active {
			continue
		}
		if !pattern.absolute {
			hasRelative = true
			continue
		}
		if pattern.absoluteCouldMatchDirectory(
			filepath.ToSlash(logicalRoot),
			filepath.ToSlash(physicalRoot),
		) {
			return true
		}
	}
	return hasRelative
}

func groupCouldMatchCandidateDirectory(patterns []compiledPathPattern, directory, logical, physical string) bool {
	if len(patterns) == 0 {
		return true
	}
	if directory == "." || directory == "" {
		for _, pattern := range patterns {
			if pattern.active && !pattern.absolute {
				return true
			}
		}
	}
	for _, pattern := range patterns {
		if !pattern.active {
			continue
		}
		if pattern.absolute {
			if pattern.absoluteCouldMatchDirectory(logical, physical) {
				return true
			}
			continue
		}
		if !pattern.anchored || pattern.matches(directory, true) {
			return true
		}
		candidate := directory
		if pattern.ignoreCase {
			candidate = strings.ToLower(candidate)
		}
		prefix := pattern.literalPrefix
		if prefix == "" || candidate == prefix ||
			strings.HasPrefix(candidate, prefix+"/") ||
			strings.HasPrefix(prefix, candidate+"/") {
			return true
		}
	}
	return false
}

func (pattern compiledPathPattern) absoluteCouldMatchDirectory(logical, physical string) bool {
	if pattern.matchesAbsoluteCandidates(logical, physical, true) {
		return true
	}
	type prefixCandidate struct {
		prefix    string
		candidate string
	}
	pairs := []prefixCandidate{
		{pattern.literalPrefix, logical},
		{pattern.literalPrefix, physical},
		{pattern.physicalLiteralPrefix, logical},
		{pattern.physicalLiteralPrefix, physical},
	}
	for _, pair := range pairs {
		if pathPrefixCouldIntersect(
			pair.prefix,
			pair.candidate,
			pattern.ignoreCase || pattern.windowsPaths,
		) {
			return true
		}
	}
	return false
}

func pathPrefixCouldIntersect(prefix, candidate string, ignoreCase bool) bool {
	prefix = strings.Trim(filepath.ToSlash(prefix), "/")
	candidate = strings.Trim(filepath.ToSlash(candidate), "/")
	if prefix == "" {
		return true
	}
	if candidate == "" {
		return true
	}
	if ignoreCase {
		prefix = strings.ToLower(prefix)
		candidate = strings.ToLower(candidate)
	}
	return candidate == prefix ||
		strings.HasPrefix(candidate, prefix+"/") ||
		strings.HasPrefix(prefix, candidate+"/")
}

func (policy *PathPolicy) exemptsDirectChild(name string) bool {
	for _, pattern := range policy.scopePositive {
		if !pattern.active {
			continue
		}
		var variants []compiledPathPattern
		if pattern.absolute {
			variants = pattern.rootRelativePatterns
		} else {
			variants = []compiledPathPattern{pattern}
		}
		for _, variant := range variants {
			if variant.exemptsDirectChild(name) {
				return true
			}
		}
	}
	return false
}

func (pattern compiledPathPattern) exemptsDirectChild(name string) bool {
	if pattern.basename {
		child := name
		if pattern.ignoreCase {
			child = strings.ToLower(child)
		}
		matched, _ := path.Match(pattern.pattern, child)
		return matched && pattern.pattern != "**" && !strings.Contains(pattern.pattern, ".")
	}
	first := pattern.parts
	if len(first) == 0 || first[0] == "**" {
		return false
	}
	child := name
	if pattern.ignoreCase {
		child = strings.ToLower(child)
	}
	matched, _ := path.Match(first[0], child)
	return matched
}

func pathScopeCanExemptDirectChild(pathScope []string) bool {
	for _, raw := range pathScope {
		raw = stripMatchingQuotes(strings.TrimSpace(raw))
		if raw == "" || strings.HasPrefix(raw, "!") {
			continue
		}
		pattern := strings.ReplaceAll(raw, `\`, "/")
		directory := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		native := filepath.FromSlash(pattern)
		absolute := filepath.IsAbs(native)
		if absolute {
			continue
		}
		pattern, rootRelative := normalizeRelativePathPattern(pattern)
		anchored := rootRelative || directory || strings.Contains(pattern, "/")
		if !anchored {
			if pattern != "**" && !strings.Contains(pattern, ".") {
				return true
			}
			continue
		}
		parts := splitPathScopeParts(pattern)
		if len(parts) > 0 && parts[0] != "**" {
			return true
		}
	}
	return false
}

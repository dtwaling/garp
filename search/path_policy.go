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
	pattern       string
	ignoreCase    bool
	basename      bool
	directory     bool
	anchored      bool
	active        bool
	matchAll      bool
	literalPrefix string
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
	if absolute {
		relative, contained := relativeAbsolutePattern(native, logicalRoot, physicalRoot)
		if !contained {
			active = false
		} else {
			pattern = filepath.ToSlash(relative)
		}
	} else {
		pattern, rootRelative = normalizeRelativePathPattern(pattern)
	}
	matchAll := pattern == "."
	anchored := absolute || rootRelative || directory || strings.Contains(pattern, "/")
	if ignoreCase {
		pattern = strings.ToLower(pattern)
	}
	return compiledPathPattern{
		pattern:       pattern,
		ignoreCase:    ignoreCase,
		basename:      !anchored,
		directory:     directory,
		anchored:      anchored,
		active:        active,
		matchAll:      matchAll,
		literalPrefix: pathScopeLiteralPrefix(pattern),
	}, nil
}

func normalizeRelativePathPattern(pattern string) (string, bool) {
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	anchored := strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "./")
	return strings.TrimLeft(pattern, "/"), anchored
}

func relativeAbsolutePattern(pattern, logicalRoot, physicalRoot string) (string, bool) {
	resolved := resolvePatternSymlinks(pattern)
	for _, candidate := range []string{pattern, resolved} {
		for _, root := range []string{logicalRoot, physicalRoot} {
			relative, err := filepath.Rel(root, candidate)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return relative, true
			}
		}
	}
	return "", false
}

// resolvePatternSymlinks resolves the longest existing prefix and preserves a
// non-existing or wildcard suffix.
func resolvePatternSymlinks(pattern string) string {
	current := filepath.Clean(pattern)
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(pattern)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func (policy *PathPolicy) MatchesFile(filename string) bool {
	relative, err := filepath.Rel(policy.logicalRoot, filename)
	if err != nil {
		return false
	}
	path := filepath.ToSlash(relative)
	return matchesPositiveGroup(policy.scopePositive, path) &&
		matchesPositiveGroup(policy.globPositive, path) &&
		!matchesAnyPattern(policy.negative, path, false)
}

func matchesPositiveGroup(patterns []compiledPathPattern, candidate string) bool {
	if len(patterns) == 0 {
		return true
	}
	return matchesAnyPattern(patterns, candidate, false)
}

func matchesAnyPattern(patterns []compiledPathPattern, candidate string, isDir bool) bool {
	for _, pattern := range patterns {
		if pattern.active && pattern.matches(candidate, isDir) {
			return true
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
		if anchoredPathPatternMatches(pattern.pattern, strings.Join(parts[:end], "/")) {
			return true
		}
	}
	if pattern.directory {
		return false
	}
	if anchoredPathPatternMatches(pattern.pattern, candidate) {
		return true
	}
	return scopePatternMatchesDirPrefix(pattern.pattern, candidate)
}

func anchoredPathPatternMatches(pattern, candidate string) bool {
	return pathScopePartsMatch(splitPathScopeParts(pattern), splitPathScopeParts(candidate))
}

// CouldMatchDirectory reports whether descending can produce an eligible path.
func (policy *PathPolicy) CouldMatchDirectory(dirname string) bool {
	relative, err := filepath.Rel(policy.logicalRoot, dirname)
	if err != nil {
		return true
	}
	path := filepath.ToSlash(relative)
	if matchesAnyPattern(policy.negative, path, true) {
		return false
	}
	return groupCouldMatchDirectory(policy.scopePositive, path) &&
		groupCouldMatchDirectory(policy.globPositive, path)
}

func groupCouldMatchDirectory(patterns []compiledPathPattern, directory string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if !pattern.active {
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

func (policy *PathPolicy) exemptsDirectChild(name string) bool {
	for _, pattern := range policy.scopePositive {
		if !pattern.active {
			continue
		}
		if pattern.basename {
			child := name
			if pattern.ignoreCase {
				child = strings.ToLower(child)
			}
			if matched, _ := path.Match(pattern.pattern, child); matched &&
				(pattern.directory || !strings.ContainsAny(pattern.pattern, "*?") && !strings.Contains(pattern.pattern, ".")) {
				return true
			}
			continue
		}
		first := splitPathScopeParts(pattern.pattern)
		if len(first) == 0 || first[0] == "**" {
			continue
		}
		child := name
		if pattern.ignoreCase {
			child = strings.ToLower(child)
		}
		if matched, _ := path.Match(first[0], child); matched {
			return true
		}
	}
	return false
}

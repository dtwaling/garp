package search

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var errMalformedIgnorePattern = errors.New("malformed ignore pattern")

// ignoreRule is one compiled line from an ignore source.
type ignoreRule struct {
	sourceDir        string
	sourcePrecedence int
	lineOrder        int
	pattern          string
	negated          bool
	directoryOnly    bool
	caseInsensitive  bool
	windowsPaths     bool
	matcher          *regexp.Regexp
}

// ignoreChain is immutable traversal state. Child chains share inherited rules
// until local rules require a copied backing slice.
type ignoreChain struct {
	rules          []ignoreRule
	nextPrecedence int
}

// initialIgnoreRules builds the ignore chain inherited by walkRoot.
func initialIgnoreRules(walkRoot string) []ignoreRule {
	return initialIgnoreChain(walkRoot).rules
}

func initialIgnoreChain(walkRoot string) ignoreChain {
	walkRoot = filepath.Clean(walkRoot)
	repositoryRoot, inRepository := findRepositoryRoot(walkRoot)
	chainRoot := walkRoot
	if inRepository {
		chainRoot = repositoryRoot
	}

	rules := make([]ignoreRule, 0)
	precedence := 0
	if inRepository {
		commonDir, worktreeDir := resolveRepositoryGitDirs(repositoryRoot)
		if commonDir != "" && !sameFilePath(commonDir, worktreeDir) {
			rules = appendIgnoreFile(rules, filepath.Join(commonDir, "info", "exclude"), repositoryRoot, precedence)
			precedence++
		}
		if worktreeDir != "" {
			rules = appendIgnoreFile(rules, filepath.Join(worktreeDir, "info", "exclude"), repositoryRoot, precedence)
			precedence++
		}
	}

	for _, directory := range pathFromAncestor(chainRoot, walkRoot) {
		rules = appendIgnoreFile(rules, filepath.Join(directory, ".gitignore"), directory, precedence)
		precedence++
		rules = appendIgnoreFile(rules, filepath.Join(directory, ".ignore"), directory, precedence)
		precedence++
	}
	return ignoreChain{rules: rules, nextPrecedence: precedence}
}

func (chain ignoreChain) withLocalFiles(directory string) ignoreChain {
	precedence := chain.nextPrecedence
	gitignoreRules := readIgnoreFile(filepath.Join(directory, ".gitignore"), directory, precedence)
	precedence++
	ignoreRules := readIgnoreFile(filepath.Join(directory, ".ignore"), directory, precedence)
	precedence++

	added := len(gitignoreRules) + len(ignoreRules)
	if added == 0 {
		return ignoreChain{rules: chain.rules, nextPrecedence: precedence}
	}

	rules := make([]ignoreRule, len(chain.rules), len(chain.rules)+added)
	copy(rules, chain.rules)
	rules = append(rules, gitignoreRules...)
	rules = append(rules, ignoreRules...)
	return ignoreChain{rules: rules, nextPrecedence: precedence}
}

func (chain ignoreChain) withoutDirectoryExcludes(candidate string) ignoreChain {
	if !ignoredByRules(chain.rules, candidate, true) {
		return chain
	}

	rules := make([]ignoreRule, 0, len(chain.rules))
	for _, rule := range chain.rules {
		if !rule.negated && rule.matches(candidate, true) {
			continue
		}
		rules = append(rules, rule)
	}
	return ignoreChain{rules: rules, nextPrecedence: chain.nextPrecedence}
}

func hasRepositoryBoundary(directory string) bool {
	info, err := os.Stat(filepath.Join(directory, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func pathScopeExemptsDirectChild(name string, normScope []string) bool {
	for _, pattern := range normScope {
		if pattern == "" {
			continue
		}
		hasSlash := strings.Contains(pattern, "/")
		parts := splitPathScopeParts(pattern)
		if len(parts) == 0 || parts[0] == "**" || (!hasSlash && strings.Contains(parts[0], ".")) {
			continue
		}
		first, child := normalizePathScopeMatchPair(parts[0], name)
		if matched, err := path.Match(first, child); err == nil && matched {
			return true
		}
	}
	return false
}

func findRepositoryRoot(start string) (string, bool) {
	current := filepath.Clean(start)
	for {
		info, err := os.Stat(filepath.Join(current, ".git"))
		if err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return current, true
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func resolveRepositoryGitDirs(repositoryRoot string) (commonDir, worktreeDir string) {
	gitEntry := filepath.Join(repositoryRoot, ".git")
	info, err := os.Stat(gitEntry)
	if err != nil {
		return "", ""
	}

	if info.IsDir() {
		worktreeDir = gitEntry
	} else if info.Mode().IsRegular() {
		contents, readErr := os.ReadFile(gitEntry)
		if readErr != nil {
			return "", ""
		}
		pointer, ok := parsePathPointer(string(contents), "gitdir:")
		if !ok {
			return "", ""
		}
		worktreeDir = resolveDirectoryPointer(repositoryRoot, pointer)
	}
	if worktreeDir == "" {
		return "", ""
	}

	contents, err := os.ReadFile(filepath.Join(worktreeDir, "commondir"))
	if err != nil {
		return "", worktreeDir
	}
	pointer := strings.TrimSpace(string(contents))
	if pointer == "" {
		return "", worktreeDir
	}
	commonDir = resolveDirectoryPointer(worktreeDir, pointer)
	return commonDir, worktreeDir
}

func parsePathPointer(contents, prefix string) (string, bool) {
	line := strings.TrimSuffix(strings.SplitN(contents, "\n", 2)[0], "\r")
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	pointer := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	return pointer, pointer != ""
}

func resolveDirectoryPointer(base, pointer string) string {
	if !filepath.IsAbs(pointer) {
		pointer = filepath.Join(base, pointer)
	}
	pointer = filepath.Clean(pointer)
	info, err := os.Stat(pointer)
	if err != nil || !info.IsDir() {
		return ""
	}
	return pointer
}

func pathFromAncestor(ancestor, descendant string) []string {
	relative, err := filepath.Rel(ancestor, descendant)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}

	directories := []string{ancestor}
	if relative == "." {
		return directories
	}
	current := ancestor
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		directories = append(directories, current)
	}
	return directories
}

func appendIgnoreFile(rules []ignoreRule, filename, sourceDir string, precedence int) []ignoreRule {
	return append(rules, readIgnoreFile(filename, sourceDir, precedence)...)
}

func readIgnoreFile(filename, sourceDir string, precedence int) []ignoreRule {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}
	return parseIgnoreRules(string(contents), sourceDir, precedence)
}

func sameFilePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func parseIgnoreRules(contents, sourceDir string, sourcePrecedence int) []ignoreRule {
	return parseIgnoreRulesForOS(contents, sourceDir, sourcePrecedence, runtime.GOOS)
}

func parseIgnoreRulesForOS(contents, sourceDir string, sourcePrecedence int, goos string) []ignoreRule {
	lines := strings.Split(contents, "\n")
	rules := make([]ignoreRule, 0, len(lines))
	windowsPaths := goos == "windows"
	sourceDir = normalizeIgnorePath(sourceDir, windowsPaths)
	caseInsensitive := windowsPaths

	for i, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		line = trimUnescapedTrailingSpaces(line)
		if line == "" || line[0] == '#' {
			continue
		}

		rule := ignoreRule{
			sourceDir:        sourceDir,
			sourcePrecedence: sourcePrecedence,
			lineOrder:        i + 1,
			pattern:          line,
			caseInsensitive:  caseInsensitive,
			windowsPaths:     windowsPaths,
		}
		if line[0] == '!' {
			rule.negated = true
			line = line[1:]
		}
		if line == "" {
			continue
		}

		if strings.HasSuffix(line, "/") {
			rule.directoryOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}

		anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")

		expression, err := ignorePatternRegexp(line, anchored, caseInsensitive)
		if err != nil {
			continue
		}
		rule.matcher = expression
		rules = append(rules, rule)
	}

	return rules
}

func trimUnescapedTrailingSpaces(line string) string {
	for len(line) > 0 && line[len(line)-1] == ' ' {
		backslashes := 0
		for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

func (rule ignoreRule) matches(candidate string, isDir bool) bool {
	if rule.directoryOnly && !isDir {
		return false
	}

	relative, ok := pathRelativeToIgnoreSource(candidate, rule.sourceDir, rule.windowsPaths, rule.caseInsensitive)
	if !ok || relative == "" {
		return false
	}
	return rule.matcher.MatchString(relative)
}

func ignoredByRules(rules []ignoreRule, candidate string, isDir bool) bool {
	ignored := false
	for _, rule := range rules {
		if rule.matches(candidate, isDir) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func pathRelativeToIgnoreSource(candidate, sourceDir string, windowsPaths, caseInsensitive bool) (string, bool) {
	candidate = normalizeIgnorePath(candidate, windowsPaths)
	if sourceDir == "" {
		return strings.TrimPrefix(candidate, "/"), true
	}
	if equalIgnorePath(candidate, sourceDir, caseInsensitive) {
		return "", true
	}

	prefix := sourceDir
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if len(candidate) < len(prefix) {
		return "", false
	}
	candidatePrefix := candidate[:len(prefix)]
	if !equalIgnorePath(candidatePrefix, prefix, caseInsensitive) {
		return "", false
	}
	return candidate[len(prefix):], true
}

func equalIgnorePath(left, right string, caseInsensitive bool) bool {
	if caseInsensitive {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func normalizeIgnorePath(value string, windowsPaths bool) string {
	if windowsPaths {
		value = strings.ReplaceAll(value, `\`, "/")
	}
	value = path.Clean(value)
	if value == "." {
		return ""
	}
	return strings.TrimPrefix(value, "./")
}

func ignorePatternRegexp(pattern string, anchored, caseInsensitive bool) (*regexp.Regexp, error) {
	translated, err := translateIgnoreGlob(pattern)
	if err != nil {
		return nil, err
	}

	var expression strings.Builder
	if caseInsensitive {
		expression.WriteString("(?i)")
	}
	expression.WriteByte('^')
	if !anchored {
		expression.WriteString("(?:.*/)?")
	}
	expression.WriteString(translated)
	expression.WriteByte('$')
	return regexp.Compile(expression.String())
}

func translateIgnoreGlob(pattern string) (string, error) {
	runes := []rune(pattern)
	var translated strings.Builder

	for i := 0; i < len(runes); {
		switch runes[i] {
		case '\\':
			if i+1 == len(runes) {
				translated.WriteString(regexp.QuoteMeta(`\`))
				i++
				continue
			}
			if runes[i+1] == '\\' {
				translated.WriteString(regexp.QuoteMeta(`\`))
				i += 2
				continue
			}
			translated.WriteString(regexp.QuoteMeta(string(runes[i+1])))
			i += 2

		case '*':
			end := i + 1
			for end < len(runes) && runes[end] == '*' {
				end++
			}
			isGlobstar := end-i >= 2 && (i == 0 || runes[i-1] == '/')
			if isGlobstar && end < len(runes) && runes[end] == '/' {
				translated.WriteString("(?:[^/]+/)*")
				i = end + 1
				continue
			}
			if isGlobstar && end == len(runes) {
				translated.WriteString(".*")
				i = end
				continue
			}
			translated.WriteString("[^/]*")
			i = end

		case '?':
			translated.WriteString("[^/]")
			i++

		case '[':
			class, next, err := translateIgnoreClass(runes, i)
			if err != nil {
				return "", err
			}
			translated.WriteString(class)
			i = next

		case '/':
			if i+3 == len(runes) && runes[i+1] == '*' && runes[i+2] == '*' {
				translated.WriteString("/.*")
				i += 3
				continue
			}
			translated.WriteByte('/')
			i++

		default:
			translated.WriteString(regexp.QuoteMeta(string(runes[i])))
			i++
		}
	}

	return translated.String(), nil
}

func translateIgnoreClass(pattern []rune, start int) (string, int, error) {
	end := start + 1
	escaped := false
	for ; end < len(pattern); end++ {
		if escaped {
			escaped = false
			continue
		}
		if pattern[end] == '\\' {
			escaped = true
			continue
		}
		if pattern[end] == ']' {
			break
		}
	}
	if end == len(pattern) {
		return "", 0, errMalformedIgnorePattern
	}

	content := pattern[start+1 : end]
	if len(content) == 0 {
		return "", 0, errMalformedIgnorePattern
	}

	negated := content[0] == '!' || content[0] == '^'
	if negated {
		content = content[1:]
		if len(content) == 0 {
			return "", 0, errMalformedIgnorePattern
		}
	}

	var class strings.Builder
	class.WriteByte('[')
	if negated {
		class.WriteString("^/")
	}
	for i := 0; i < len(content); i++ {
		current := content[i]
		if current == '\\' && i+1 < len(content) {
			i++
			if content[i] == '/' {
				continue
			}
			class.WriteString(`\x{`)
			class.WriteString(strconv.FormatInt(int64(content[i]), 16))
			class.WriteByte('}')
			continue
		}
		if current == '/' {
			continue
		}
		if current == '^' || current == '[' || current == ']' {
			class.WriteByte('\\')
		}
		class.WriteRune(current)
	}
	class.WriteByte(']')

	return class.String(), end + 1, nil
}

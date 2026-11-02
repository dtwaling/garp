package search

import (
	"errors"
	"path"
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

	prefix := sourceDir + "/"
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

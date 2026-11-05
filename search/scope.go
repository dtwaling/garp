package search

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const maxFileSizeBytes uint64 = 100 * 1024 * 1024

// ParseMaxFileSize parses a positive byte size with an optional binary unit.
// Supported suffixes are B, K/KB/KiB, M/MB/MiB, and G/GB/GiB.
func ParseMaxFileSize(raw string) (int64, error) {
	digitEnd := 0
	for digitEnd < len(raw) && raw[digitEnd] >= '0' && raw[digitEnd] <= '9' {
		digitEnd++
	}
	if digitEnd == 0 {
		return 0, fmt.Errorf("--max-filesize: %q is not a positive integer size", raw)
	}

	value, err := strconv.ParseUint(raw[:digitEnd], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--max-filesize: invalid value %q: %w", raw, err)
	}
	if value == 0 {
		return 0, fmt.Errorf("--max-filesize: value must be greater than zero")
	}

	var multiplier uint64
	switch strings.ToUpper(raw[digitEnd:]) {
	case "", "B":
		multiplier = 1
	case "K", "KB", "KIB":
		multiplier = 1024
	case "M", "MB", "MIB":
		multiplier = 1024 * 1024
	case "G", "GB", "GIB":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("--max-filesize: unknown unit in %q", raw)
	}

	if value > ^uint64(0)/multiplier {
		return 0, fmt.Errorf("--max-filesize: value %q overflows bytes", raw)
	}
	bytes := value * multiplier
	if bytes > maxFileSizeBytes {
		return 0, fmt.Errorf("--max-filesize: value %q exceeds the 100M maximum", raw)
	}
	return int64(bytes), nil
}

// wildcardAndInjectionChars are characters rejected in --startdir to prevent
// shell/command injection and unintended glob expansion.
var wildcardAndInjectionChars = []string{
	"*", "?", "[", "]", "{", "}", "|", ";", "&",
	"$", "`", ">", "<", "\n", "\r",
}

// ValidateStartDir validates and normalizes a --startdir argument.
// Returns the cleaned path or an error describing the rejection reason.
// Accepts Windows (C:\, C:/), macOS, and Linux paths.
// Trailing slashes are consumed by filepath.Clean.
// The caller is responsible for verifying the path is an existing directory.
func ValidateStartDir(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("--startdir: path must not be empty")
	}
	for _, ch := range wildcardAndInjectionChars {
		if strings.Contains(raw, ch) {
			return "", fmt.Errorf(
				"--startdir: rejected -- path contains illegal character %q (wildcards and shell metacharacters are not allowed)",
				ch,
			)
		}
	}
	// Normalize: clean the path (removes redundant separators, .., trailing slash)
	cleaned := filepath.Clean(raw)
	return cleaned, nil
}

// pathScopeRejectedChars are characters rejected in --pathscope segments.
// Allows only simple glob wildcards (* and ?), path separators, alphanumeric,
// dash, underscore, and space. Regex metacharacters and injection chars are
// all rejected.
var pathScopeRejectedChars = []string{
	"[", "]", "{", "}", "|", ";", "&", "$", "`",
	">", "<", "\n", "\r", "(", ")", "^", "+",
}

// ValidatePathScope validates and parses a --pathscope argument.
// Input is a comma-separated list of simple glob patterns for path matching.
// Rules:
//   - Simple wildcards (* and ?) are allowed.
//   - Regex metacharacters are rejected.
//   - Dots (.) in segments are treated as part of a directory or filename --
//     file extension filtering belongs to --not / --only, not --pathscope.
//     The CLI help directs users accordingly, but garp always runs the search.
//   - Empty segments and parent traversal segments are rejected.
//
// Returns validated normalized segments.
func ValidatePathScope(raw string) ([]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("--pathscope: value must not be empty")
	}
	raw = stripMatchingQuotes(strings.TrimSpace(raw))
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		seg := stripMatchingQuotes(strings.TrimSpace(p))
		seg = strings.ReplaceAll(seg, "\\", "/")
		if seg == "" {
			return nil, fmt.Errorf("--pathscope: empty comma-separated segment")
		}
		// Reject regex and injection chars
		for _, ch := range pathScopeRejectedChars {
			if strings.Contains(seg, ch) {
				return nil, fmt.Errorf(
					"--pathscope: segment %q contains illegal character %q -- "+
						"use simple wildcards (* and ?) only; regex patterns are not supported",
					seg, ch,
				)
			}
		}
		pattern := strings.TrimPrefix(seg, "!")
		if pattern == "" {
			return nil, fmt.Errorf("--pathscope: negation must include a pattern")
		}
		if isBareSlashPattern(pattern) {
			return nil, fmt.Errorf("--pathscope: segment %q has no pattern before its directory marker", seg)
		}
		for _, component := range strings.Split(pattern, "/") {
			if component == ".." {
				return nil, fmt.Errorf("--pathscope: segment %q contains parent traversal", seg)
			}
		}
		result = append(result, seg)
	}
	return result, nil
}

func isBareSlashPattern(pattern string) bool {
	return strings.Trim(pattern, "/") == ""
}

func stripMatchingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first := s[0]
	last := s[len(s)-1]
	if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

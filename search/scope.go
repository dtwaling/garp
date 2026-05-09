package search

import (
	"fmt"
	"path/filepath"
	"strings"
)

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
	">", "<", "\n", "\r", "(", ")", "^", "+", "\\",
}

// ValidatePathScope validates and parses a --pathscope argument.
// Input is a comma-separated list of simple glob patterns for path matching.
// Rules:
//   - Simple wildcards (* and ?) are allowed.
//   - Regex metacharacters are rejected.
//   - Dots (.) are rejected in all segments -- file extensions belong to
//     --not / --only, not --pathscope.
//   - Empty segments (from trailing commas etc.) are silently skipped.
//
// Returns the parsed segments, or nil if input is empty (feature disabled).
func ValidatePathScope(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		seg := strings.TrimSpace(p)
		if seg == "" {
			continue
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
		// Reject extension-like patterns -- any segment containing '.'
		if strings.Contains(seg, ".") {
			return nil, fmt.Errorf(
				"--pathscope: segment %q contains '.' -- "+
					"do not include file extensions; use --not or --only for extension filtering",
				seg,
			)
		}
		result = append(result, seg)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

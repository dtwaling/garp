package search

import (
	"path"
	"reflect"
	"testing"
)

func TestPathScopePartsMatch(t *testing.T) {
	for _, tt := range []struct {
		pattern, candidate string
		want               bool
	}{
		{"", "", true},
		{"", "a", false},
		{"a", "", false},
		{"**/**", "", true},
		{"a/**/**/b", "a/b", true},
		{"a/**/**/b", "a/x/y/b", true},
		{"a/**/**/b", "a/x/y/c", false},
		{"**/a/**/b", "x/a/y/a/b", true},
		{"a/**", "a", true},
		{"a", "a/b", false},
		{"*/b", "a/x/b", false},
		{"**/[ab]?.go", "src/a1.go", true},
		{"**/[ab]?.go", "src/c1.go", false},
		{"**/[", "a/b", false},
		{"A/**", "a/b", false},
		{`\*\*/x`, "**/x", true},
		{`\*\*/x`, "**/**/x", false},
	} {
		t.Run(tt.pattern+":"+tt.candidate, func(t *testing.T) {
			pattern := splitPathScopeParts(tt.pattern)
			candidate := splitPathScopeParts(tt.candidate)
			beforePattern := append([]string(nil), pattern...)
			beforeCandidate := append([]string(nil), candidate...)
			if got := pathScopePartsMatch(pattern, candidate); got != tt.want {
				t.Fatalf("match(%q, %q) = %v, want %v", tt.pattern, tt.candidate, got, tt.want)
			}
			if !reflect.DeepEqual(pattern, beforePattern) || !reflect.DeepEqual(candidate, beforeCandidate) {
				t.Fatal("matching mutated input parts")
			}
		})
	}
}

// Keep the exhaustive oracle small: it deliberately uses the original recursive
// semantics to check all combinations of short patterns and literal path parts.
func TestPathScopePartsMatchExhaustive(t *testing.T) {
	var reference func([]string, []string) bool
	reference = func(pattern, candidate []string) bool {
		if len(pattern) == 0 {
			return len(candidate) == 0
		}
		if pattern[0] == "**" {
			for i := 0; i <= len(candidate); i++ {
				if reference(pattern[1:], candidate[i:]) {
					return true
				}
			}
			return false
		}
		if len(candidate) == 0 {
			return false
		}
		matched, err := path.Match(pattern[0], candidate[0])
		return err == nil && matched && reference(pattern[1:], candidate[1:])
	}
	sequences := func(alphabet []string, depth int) [][]string {
		result := [][]string{nil}
		start := 0
		for i := 0; i < depth; i++ {
			end := len(result)
			for _, prefix := range result[start:end] {
				for _, part := range alphabet {
					next := append([]string(nil), prefix...)
					result = append(result, append(next, part))
				}
			}
			start = end
		}
		return result
	}
	patterns := sequences([]string{"a", "b", "*", "**", "?", "[ab]", "["}, 4)
	candidates := sequences([]string{"a", "b", "**"}, 3)
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			want := reference(pattern, candidate)
			if got := pathScopePartsMatch(pattern, candidate); got != want {
				t.Fatalf("match(%q, %q) = %v, want %v", pattern, candidate, got, want)
			}
		}
	}
	t.Logf("verified %d pattern/path combinations", len(patterns)*len(candidates))
}

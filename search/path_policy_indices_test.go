package search

import (
	"path"
	"slices"
	"strings"
	"testing"
)

func TestPatternIndicesAtRoot(t *testing.T) {
	for _, tt := range []struct {
		name, pattern, root     string
		patternIndex, rootIndex int
		foldCase                bool
		want                    []int
	}{
		{name: "empty", want: []int{0}},
		{name: "root exhausted", pattern: "a/**/**", root: "a", want: []int{1}},
		{name: "pattern exhausted", pattern: "a", root: "a/b", want: []int{1}},
		{name: "globstar offsets", pattern: "**/**/b", root: "a", want: []int{1, 0}},
		{name: "globstar and literal", pattern: "**/**/b", root: "a/b", want: []int{3, 1, 0}},
		{name: "trailing globstars", pattern: "**/**", root: "a/b", want: []int{2, 1, 0}},
		{name: "mismatch", pattern: "a/b", root: "a/c"},
		{name: "invalid", pattern: "**/[", root: "a/b", want: []int{0}},
		{name: "fold case", pattern: "A/[B]", root: "a/b", foldCase: true, want: []int{2}},
		{name: "case sensitive", pattern: "A/[B]", root: "a/b"},
		{name: "offsets", pattern: "skip/**/**/b", root: "skip/a/b", patternIndex: 1, rootIndex: 1, want: []int{4, 2, 1}},
		{name: "discovery order", pattern: "repo/**/a/b/**", root: "repo/a", want: []int{3, 1}},
		{name: "escaped literal", pattern: `\*\*/x`, root: "**/x", want: []int{2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pattern, root := splitPathScopeParts(tt.pattern), splitPathScopeParts(tt.root)
			beforePattern, beforeRoot := slices.Clone(pattern), slices.Clone(root)
			got := patternIndicesAtRoot(pattern, root, tt.patternIndex, tt.rootIndex, tt.foldCase)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("indices = %v, want %v", got, tt.want)
			}
			if !slices.Equal(pattern, beforePattern) || !slices.Equal(root, beforeRoot) {
				t.Fatal("matching mutated inputs")
			}
		})
	}
}

// Compare unique terminal offsets in first-discovery order with the original recursion.
// Only short inputs reach the oracle so its exponential branching stays bounded.
func TestPatternIndicesAtRootExhaustive(t *testing.T) {
	var reference func([]string, []string, int, int, bool) []int
	reference = func(pattern, root []string, pi, ri int, fold bool) []int {
		if ri == len(root) || pi == len(pattern) {
			return []int{pi}
		}
		if pattern[pi] == "**" {
			return append(reference(pattern, root, pi+1, ri, fold), reference(pattern, root, pi, ri+1, fold)...)
		}
		p, r := pattern[pi], root[ri]
		if fold {
			p, r = strings.ToLower(p), strings.ToLower(r)
		}
		matched, err := path.Match(p, r)
		if err != nil || !matched {
			return nil
		}
		return reference(pattern, root, pi+1, ri+1, fold)
	}
	sequences := func(alphabet []string, depth int) [][]string {
		result := [][]string{nil}
		start := 0
		for i := 0; i < depth; i++ {
			end := len(result)
			for _, prefix := range result[start:end] {
				for _, part := range alphabet {
					result = append(result, append(slices.Clone(prefix), part))
				}
			}
			start = end
		}
		return result
	}
	patterns := sequences([]string{"a", "A", "**", "*", "[ab]", "["}, 4)
	roots := sequences([]string{"a", "b", "**"}, 3)
	checks := 0
	for _, pattern := range patterns {
		for _, root := range roots {
			for _, fold := range []bool{false, true} {
				var want []int
				for _, index := range reference(pattern, root, 0, 0, fold) {
					if !slices.Contains(want, index) {
						want = append(want, index)
					}
				}
				got := patternIndicesAtRoot(pattern, root, 0, 0, fold)
				if !slices.Equal(got, want) {
					t.Fatalf("indices(%q, %q, fold=%v) = %v, want %v", pattern, root, fold, got, want)
				}
				checks++
			}
		}
	}
	t.Logf("verified %d pattern/root/case combinations", checks)
}

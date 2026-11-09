package search

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Share fixtures between correctness checks and benchmarks so timing cannot hide
// a fast but incorrect result. Inputs are prepared outside the timed region.
type pathologicalGlobCase struct {
	name        string
	pattern     []string
	candidate   []string
	wantMatch   bool
	wantIndices []int
}

func pathologicalGlobCases(depth int) []pathologicalGlobCase {
	var cases []pathologicalGlobCase
	for _, match := range []bool{false, true} {
		terminal := "missing"
		if match {
			terminal = "z"
		}
		// Multiple occurrences of each letter offer many ways to partition
		// the path among globstars before the last literal succeeds or fails.
		var pattern, candidate []string
		var indices []int
		for letter := 'a'; letter < 'z'; letter++ {
			pattern = append(pattern, "**", string(letter))
			for range depth {
				candidate = append(candidate, string(letter))
			}
		}
		pattern = append(pattern, "**", "z")
		candidate = append(candidate, terminal)
		if match {
			indices = append(indices, len(pattern))
		}
		for i := len(pattern) - 2; i >= 0; i -= 2 {
			indices = append(indices, i)
		}
		cases = append(cases, pathologicalGlobCase{
			name:    fmt.Sprintf("alphabet/match=%v", match),
			pattern: pattern, candidate: candidate, wantMatch: match, wantIndices: indices,
		})

		terminal = "missing"
		if match {
			terminal = "nonexistent"
		}
		candidate = splitPathScopeParts(strings.Repeat("dir/", 25*depth) + terminal)
		indices = nil
		if match {
			indices = append(indices, 6)
		}
		indices = append(indices, 4, 3, 2, 1, 0)
		cases = append(cases, pathologicalGlobCase{
			name:      fmt.Sprintf("adjacent/match=%v", match),
			pattern:   splitPathScopeParts("**/**/**/**/**/nonexistent"),
			candidate: candidate, wantMatch: match, wantIndices: indices,
		})
	}
	return cases
}

func TestPathologicalGlobs(t *testing.T) {
	for _, depth := range []int{2, 4, 8} {
		for _, tc := range pathologicalGlobCases(depth) {
			t.Run(fmt.Sprintf("repetitions=%d/%s", depth, tc.name), func(t *testing.T) {
				if got := pathScopePartsMatch(tc.pattern, tc.candidate); got != tc.wantMatch {
					t.Fatalf("match = %v, want %v", got, tc.wantMatch)
				}
				// A failed full match still has root-spanning globstar offsets.
				// Check their exact order, not merely whether any offset exists.
				if got := patternIndicesAtRoot(tc.pattern, tc.candidate, 0, 0, false); !slices.Equal(got, tc.wantIndices) {
					t.Fatalf("indices = %v, want %v", got, tc.wantIndices)
				}
			})
		}
	}
}

func TestPathologicalGlobsLongPattern(t *testing.T) {
	// Exercise pattern depth as well as path depth without a recursive oracle.
	const globstars = 4096
	pattern := splitPathScopeParts(strings.Repeat("**/", globstars) + "nonexistent")
	candidate := splitPathScopeParts(strings.Repeat("dir/", 4096) + "missing")
	if pathScopePartsMatch(pattern, candidate) {
		t.Fatal("unexpected match for missing terminal literal")
	}
	want := make([]int, globstars)
	for i := range want {
		want[i] = globstars - 1 - i
	}
	if got := patternIndicesAtRoot(pattern, candidate, 0, 0, false); !slices.Equal(got, want) {
		t.Fatalf("unexpected offsets for %d consecutive globstars: got %d offsets", globstars, len(got))
	}
}

func BenchmarkPathologicalGlobs(b *testing.B) {
	for _, depth := range []int{2, 4, 8} {
		for _, tc := range pathologicalGlobCases(depth) {
			name := fmt.Sprintf("segments=%d/%s", len(tc.candidate), tc.name)
			b.Run("PathScope/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if got := pathScopePartsMatch(tc.pattern, tc.candidate); got != tc.wantMatch {
						b.Fatalf("match = %v, want %v", got, tc.wantMatch)
					}
				}
			})
			b.Run("RootIndices/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if got := patternIndicesAtRoot(tc.pattern, tc.candidate, 0, 0, false); !slices.Equal(got, tc.wantIndices) {
						b.Fatalf("indices = %v, want %v", got, tc.wantIndices)
					}
				}
			})
		}
	}
}

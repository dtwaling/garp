package search

import (
	"reflect"
	"testing"
)

func TestRankingComparatorUsesEveryTier(t *testing.T) {
	base := rankingKey{
		filePath:         "z.php",
		score:            10,
		termCount:        2,
		pathMatchCount:   1,
		boundaryQuality:  1,
		spanLen:          20,
		topQualityChunks: 1,
	}
	tests := []struct {
		name   string
		better rankingKey
		worse  rankingKey
	}{
		{name: "score", better: rankingKey{score: 11}, worse: rankingKey{score: 10}},
		{name: "term count", better: rankingKey{score: 10, termCount: 3}, worse: rankingKey{score: 10, termCount: 2}},
		{
			name:   "path match count",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 2},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1},
		},
		{
			name:   "boundary quality",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 2},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1},
		},
		{
			name:   "span length",
			better: rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1, spanLen: 19},
			worse:  rankingKey{score: 10, termCount: 2, pathMatchCount: 1, boundaryQuality: 1, spanLen: 20},
		},
		{name: "top quality chunks", better: base, worse: rankingKey{
			filePath:         base.filePath,
			score:            base.score,
			termCount:        base.termCount,
			pathMatchCount:   base.pathMatchCount,
			boundaryQuality:  base.boundaryQuality,
			spanLen:          base.spanLen,
			topQualityChunks: 0,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.better.filePath = base.filePath
			tt.worse.filePath = base.filePath
			if !rankingBetter(tt.better, tt.worse) {
				t.Fatalf("%s tier did not rank higher", tt.name)
			}
			if rankingBetter(tt.worse, tt.better) {
				t.Fatalf("%s tier comparison is not antisymmetric", tt.name)
			}
		})
	}
}

func TestRankingComparatorNormalizesAndOrdersPaths(t *testing.T) {
	results := []SearchResult{
		{FilePath: `dir\b.php`},
		{FilePath: "dir/b.php"},
		{FilePath: "dir/a.php"},
		{FilePath: "dir/A.php"},
	}
	sortSearchResults(results)

	got := make([]string, len(results))
	for i := range results {
		got[i] = results[i].FilePath
	}
	want := []string{"dir/A.php", "dir/a.php", "dir/b.php", `dir\b.php`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("path order = %q, want %q", got, want)
	}
}

func TestRankingComparatorUsesRootOrderThenCanonicalPath(t *testing.T) {
	base := rankingKey{filePath: "same.txt", score: 10, termCount: 1}
	firstRoot := base
	firstRoot.rootOrder = 0
	firstRoot.canonicalPath = "z-root/same.txt"
	secondRoot := base
	secondRoot.rootOrder = 1
	secondRoot.canonicalPath = "a-root/same.txt"
	if !rankingBetter(firstRoot, secondRoot) || rankingBetter(secondRoot, firstRoot) {
		t.Fatal("supplied root order did not break an equal relative-path tie")
	}

	canonicalFirst := base
	canonicalFirst.canonicalPath = "a-root/same.txt"
	canonicalSecond := base
	canonicalSecond.canonicalPath = "z-root/same.txt"
	if !rankingBetter(canonicalFirst, canonicalSecond) || rankingBetter(canonicalSecond, canonicalFirst) {
		t.Fatal("canonical absolute path did not break the final tie")
	}
}

func TestRankingComparatorRawPathTieIsDeterministicAndTransitive(t *testing.T) {
	want := []string{
		"dir/A.php",
		"dir/a.php",
		`dir\a.php`,
		"dir/b.php",
		`dir\b.php`,
	}

	for i := range want {
		for j := i + 1; j < len(want); j++ {
			a := rankingKey{filePath: want[i]}
			b := rankingKey{filePath: want[j]}
			if !rankingBetter(a, b) {
				t.Fatalf("rankingBetter(%q, %q) = false", want[i], want[j])
			}
			if rankingBetter(b, a) {
				t.Fatalf("rankingBetter(%q, %q) = true", want[j], want[i])
			}
		}
	}

	input := make([]candidateMatch, 0, len(want)*2)
	for i := len(want) - 1; i >= 0; i-- {
		input = append(input,
			candidateMatch{filePath: want[i]},
			candidateMatch{filePath: want[i]},
		)
	}
	for iteration := 0; iteration < 100; iteration++ {
		gotCandidates := sortAndDedupeCandidates(input)
		got := make([]string, len(gotCandidates))
		for i := range gotCandidates {
			got[i] = gotCandidates[i].filePath
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d candidate order = %q, want %q", iteration, got, want)
		}
	}
}

func TestSortAndDedupeCandidatesUsesSharedComparator(t *testing.T) {
	matches := []candidateMatch{
		{filePath: "same.php", score: 10, termCount: 2, spanLen: 20},
		{filePath: "same.php", score: 10, termCount: 2, pathMatchCount: 1, spanLen: 20},
		{filePath: "other.php", score: 10, termCount: 2, spanLen: 20},
	}

	got := sortAndDedupeCandidates(matches)
	if len(got) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(got))
	}
	if got[0].filePath != "same.php" || got[0].pathMatchCount != 1 {
		t.Fatalf("first candidate = %+v, want better same.php candidate", got[0])
	}
}

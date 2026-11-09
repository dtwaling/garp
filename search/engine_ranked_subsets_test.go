package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPerFileSubsetFixture writes a corpus where clusters of distinct
// quality tiers exist across two files so engine-level per-file subsets and
// file ordering can be asserted.
func buildPerFileSubsetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// mixed.txt: two separated clusters -- a three-term cluster (best) and a
	// two-term cluster (lower quality). Both must survive in one per-file subset.
	mixed := strings.Join([]string{
		"First cluster begins. alpha beta gamma mixed first. First cluster ends.",
		perFileSubsetPadding("mixed-between", 90),
		"Second cluster begins. alpha beta mixed second. Second cluster ends.",
	}, " ")

	// all.txt: two separated three-term clusters (two top-quality chunks).
	all := strings.Join([]string{
		"First cluster begins. alpha beta gamma all first. First cluster ends.",
		perFileSubsetPadding("all-between", 90),
		"Second cluster begins. alpha beta gamma all second. Second cluster ends.",
	}, " ")

	if err := os.WriteFile(filepath.Join(root, "mixed.txt"), []byte(mixed), 0o600); err != nil {
		t.Fatalf("write mixed.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "all.txt"), []byte(all), 0o600); err != nil {
		t.Fatalf("write all.txt: %v", err)
	}
	return root
}

func TestEnginePerFileSubsetsRetainLowerQualityClusters(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = 3
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2 files", len(results))
	}

	mixed := skipAheadResultByFile(t, results, "mixed.txt")
	if len(mixed.RawExcerpts) != 2 {
		t.Fatalf("mixed.txt excerpts = %d, want 2 (both quality levels)", len(mixed.RawExcerpts))
	}
	if !strings.Contains(mixed.RawExcerpts[0], "gamma") {
		t.Fatalf("mixed.txt first excerpt should be the best cluster: %q", mixed.RawExcerpts[0])
	}
	if strings.Contains(mixed.RawExcerpts[1], "gamma") {
		t.Fatalf("mixed.txt second excerpt should be the lower-quality cluster: %q", mixed.RawExcerpts[1])
	}
	if mixed.TopQualityChunks != 1 {
		t.Fatalf("mixed.txt top-quality chunk count = %d, want 1", mixed.TopQualityChunks)
	}

	all := skipAheadResultByFile(t, results, "all.txt")
	if len(all.RawExcerpts) != 2 {
		t.Fatalf("all.txt excerpts = %d, want 2", len(all.RawExcerpts))
	}
	if all.TopQualityChunks != 2 {
		t.Fatalf("all.txt top-quality chunk count = %d, want 2", all.TopQualityChunks)
	}

	// File ordering: same file score and term count, but all.txt has more
	// top-quality chunks, so it ranks first.
	if got := filepath.Base(results[0].FilePath); got != "all.txt" {
		t.Fatalf("first result = %q, want all.txt (more top-quality chunks)", got)
	}
}

func TestEnginePerFileSubsetsExcerptLimitDoesNotChangeFileRank(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	var orders [][]string
	for _, maxExcerpts := range []int{1, 3} {
		engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
		engine.StartDir = root
		engine.Distance = 100
		engine.MaxExcerpts = maxExcerpts
		engine.Silent = true

		results, err := engine.Execute()
		if err != nil {
			t.Fatalf("Execute() with MaxExcerpts=%d error: %v", maxExcerpts, err)
		}
		order := make([]string, len(results))
		for i, result := range results {
			order[i] = filepath.Base(result.FilePath)
		}
		orders = append(orders, order)

		all := skipAheadResultByFile(t, results, "all.txt")
		if all.TopQualityChunks != 2 {
			t.Fatalf("MaxExcerpts=%d TopQualityChunks = %d, want Stage 2 count 2", maxExcerpts, all.TopQualityChunks)
		}
		if len(all.RawExcerpts) > maxExcerpts {
			t.Fatalf("MaxExcerpts=%d emitted %d excerpts", maxExcerpts, len(all.RawExcerpts))
		}
	}

	if strings.Join(orders[0], "\x00") != strings.Join(orders[1], "\x00") {
		t.Fatalf("file order changed with excerpt limit: %v vs %v", orders[0], orders[1])
	}
}

func TestEnginePerFileSubsetsStage3Regressions(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	for _, tc := range []struct {
		name     string
		content  string
		distance int
		markers  []string
		terms    []int
	}{
		{
			name:     "distant extra term cannot inflate excerpt quality",
			content:  "alpha beta near cluster\n" + strings.Repeat("padding ", 500) + "\ngamma",
			distance: 20,
			markers:  []string{"near cluster"},
			terms:    []int{2},
		},
		{
			name:     "sole endpoint is not reused or allowed to suppress middle cluster",
			content:  endpointNoveltyCorpus(),
			distance: len(endpointNoveltyCorpus()),
			markers:  []string{"final cluster", "first cluster", "middle cluster"},
			terms:    []int{3, 2, 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "clusters.txt"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
			engine.StartDir = root
			engine.Distance = tc.distance
			engine.MaxExcerpts = 3
			engine.Silent = true
			results, err := engine.Execute()
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d files, want 1", len(results))
			}
			result := results[0]
			wantScore := len("alpha") + len("beta")
			if tc.terms[0] == 3 {
				wantScore += len("gamma")
			}
			if result.Score != wantScore || result.TermCount != tc.terms[0] {
				t.Fatalf("file quality = (%d, %d), want (%d, %d)", result.Score, result.TermCount, wantScore, tc.terms[0])
			}
			if len(result.RawExcerpts) != len(tc.markers) || len(result.ChunkScores) != len(tc.markers) || len(result.ChunkTermCounts) != len(tc.markers) {
				t.Fatalf("excerpt/score/term counts = (%d, %d, %d), want %d each", len(result.RawExcerpts), len(result.ChunkScores), len(result.ChunkTermCounts), len(tc.markers))
			}
			for i, marker := range tc.markers {
				if result.ChunkScores[i] > result.Score || result.ChunkTermCounts[i] > result.TermCount {
					t.Fatalf("excerpt %d quality exceeds parent file", i)
				}
				if result.ChunkTermCounts[i] != tc.terms[i] || !strings.Contains(result.RawExcerpts[i], marker) {
					t.Fatalf("excerpt %d = %q (%d terms), want %s (%d terms)", i, result.RawExcerpts[i], result.ChunkTermCounts[i], marker, tc.terms[i])
				}
				if tc.terms[i] == 2 && strings.Contains(result.RawExcerpts[i], "gamma") {
					t.Fatalf("excerpt %d includes distant or reused gamma: %q", i, result.RawExcerpts[i])
				}
			}
		})
	}
}

func TestEnginePerFileSubsetsChunkOrderingWithinFile(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	root := buildPerFileSubsetFixture(t)
	engine := NewSearchEngineWithWorkers([]string{"alpha", "beta", "gamma"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = 3
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	mixed := skipAheadResultByFile(t, results, "mixed.txt")
	if len(mixed.ChunkScores) != len(mixed.RawExcerpts) || len(mixed.ChunkTermCounts) != len(mixed.RawExcerpts) {
		t.Fatalf("chunk metadata lengths = (%d, %d), want %d each", len(mixed.ChunkScores), len(mixed.ChunkTermCounts), len(mixed.RawExcerpts))
	}
	for i := 1; i < len(mixed.ChunkScores); i++ {
		if mixed.ChunkScores[i] > mixed.ChunkScores[i-1] {
			t.Fatalf("chunk %d score %d exceeds prior chunk score %d (best-first violated)", i, mixed.ChunkScores[i], mixed.ChunkScores[i-1])
		}
	}
	// Anchors stay in document order regardless of chunk emission order.
	for i := 1; i < len(mixed.StartLines); i++ {
		if mixed.StartLines[i] < mixed.StartLines[i-1] {
			t.Fatalf("chunk %d start line %d before prior %d (document order violated)", i, mixed.StartLines[i], mixed.StartLines[i-1])
		}
	}
}

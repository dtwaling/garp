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

package search

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type rankingStaticExtractor struct {
	text string
}

func (e rankingStaticExtractor) ExtractText([]byte) (string, error) {
	return e.text, nil
}

func TestPathMatchCount(t *testing.T) {
	root := t.TempDir()
	query := compileQuery([]string{"ms-", "api"}, PartialModeContains)

	t.Run("absolute root control", func(t *testing.T) {
		tests := []struct {
			path string
			want int
		}{
			{path: filepath.Join(root, "app", "Services", "MsOptionApiClient.php"), want: 2},
			{path: filepath.Join(root, "app", "Utils", "ms_helper.php"), want: 1},
			{path: filepath.Join(root, "app", "Utils", "other.php"), want: 0},
		}
		for _, tt := range tests {
			if got := pathMatchCount(tt.path, root, query); got != tt.want {
				t.Errorf("pathMatchCount(%q) = %d, want %d", tt.path, got, tt.want)
			}
		}
	})

	t.Run("alphanumeric core fallback", func(t *testing.T) {
		path := filepath.Join(root, "MsOptionApiClient.php")
		if got := pathMatchCount(path, root, query); got != 2 {
			t.Fatalf("pathMatchCount(%q) = %d, want 2", path, got)
		}
		if got := pathMatchCount(filepath.Join(root, "xylophone.php"), root, compileQuery([]string{"x-"}, PartialModeContains)); got != 0 {
			t.Fatalf("one-byte core fallback count = %d, want 0", got)
		}
	})

	t.Run("default root uses current directory", func(t *testing.T) {
		cwd := t.TempDir()
		t.Chdir(cwd)
		path := filepath.Join(cwd, "app", "MsArea", "ApiClients", "other.php")
		if got := pathMatchCount(path, "", query); got != 2 {
			t.Fatalf("pathMatchCount(%q, default root) = %d, want 2", path, got)
		}
	})

	t.Run("relative root preserves descendants", func(t *testing.T) {
		cwd := t.TempDir()
		t.Chdir(cwd)
		const relativeRoot = "workspace"
		path := filepath.Join(cwd, relativeRoot, "app", "MsArea", "ApiClients", "other.php")
		if got := pathMatchCount(path, relativeRoot, query); got != 2 {
			t.Fatalf("pathMatchCount(%q, %q) = %d, want 2", path, relativeRoot, got)
		}
	})

	t.Run("outside root uses basename", func(t *testing.T) {
		base := t.TempDir()
		searchRoot := filepath.Join(base, "root")
		path := filepath.Join(base, "ms-api-parent", "other.php")
		if got := pathMatchCount(path, searchRoot, query); got != 0 {
			t.Fatalf("outside-root pathMatchCount(%q) = %d, want basename-only 0", path, got)
		}
	})

	t.Run("empty relative path uses basename", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "MsApiRoot")
		if got := pathMatchCount(path, path, query); got != 2 {
			t.Fatalf("empty-relative pathMatchCount(%q) = %d, want basename count 2", path, got)
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("Windows separators and root case", func(t *testing.T) {
			base := t.TempDir()
			searchRoot := filepath.Join(base, "RepoRoot")
			path := filepath.Join(searchRoot, "app", "Services", "other.php")
			path = strings.ReplaceAll(path, `\`, "/")
			serviceQuery := compileQuery([]string{"services"}, PartialModeContains)
			if got := pathMatchCount(path, strings.ToUpper(searchRoot), serviceQuery); got != 1 {
				t.Fatalf("Windows pathMatchCount(%q) = %d, want 1", path, got)
			}
		})
	}
}

func TestTopQualityChunks(t *testing.T) {
	intervals := []rankedInterval{
		{start: 10, end: 30},
		{start: 20, end: 40},
		{start: 35, end: 50},
	}
	if got := topQualityChunkCount(intervals); got != 2 {
		t.Fatalf("topQualityChunkCount(%v) = %d, want 2", intervals, got)
	}
}

func TestTopQualityChunksFiltersScoreCollisionByTermCount(t *testing.T) {
	text := "aa bbbbbbbb " + strings.Repeat(" ", 30) +
		"aa bbb ccccc " + strings.Repeat(" ", 30) +
		"aa bbb ccccc"
	analysis, ok := analyzeRankedText(
		text,
		compileQuery([]string{"aa", "bbbbbbbb", "bbb", "ccccc"}, PartialModeOff),
		15,
		false,
	)
	if !ok {
		t.Fatal("analysis rejected qualifying text")
	}
	if analysis.Score != 10 || analysis.TermCount != 3 {
		t.Fatalf("best pair = (%d, %d), want (10, 3)", analysis.Score, analysis.TermCount)
	}
	if analysis.TopQualityChunks != 2 {
		t.Fatalf("TopQualityChunks = %d, want 2", analysis.TopQualityChunks)
	}
	if len(analysis.bestIntervals) != 2 {
		t.Fatalf("best intervals = %v, want only two three-term intervals", analysis.bestIntervals)
	}
}

func TestEngineStage2RankingMetadata(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	contentDir := filepath.Join(root, "MsArea", "ApiClients")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "ms-option-api" + strings.Repeat(" ", 40) + "ms-option-api"
	textPath := filepath.Join(contentDir, "text.txt")
	binaryPath := filepath.Join(contentDir, "binary.pdf")
	for _, path := range []string{textPath, binaryPath} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(parent)
	engine := NewSearchEngine([]string{"ms-", "api"}, nil, nil, false, 1, 1000)
	engine.StartDir = "workspace"
	engine.Partial = PartialModeContains
	engine.Distance = 20
	engine.Silent = true
	engine.Registry.extractors["pdf"] = rankingStaticExtractor{text: content}

	matches, err := engine.FilterCandidates([]string{textPath, binaryPath}, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want text and extracted binary", matches)
	}
	for _, match := range matches {
		if match.pathMatchCount != 2 ||
			match.boundaryQuality != 2 ||
			match.topQualityChunks != 2 {
			t.Errorf("%s metadata = path %d, boundary %d, top chunks %d; want 2, 2, 2",
				filepath.Base(match.filePath),
				match.pathMatchCount,
				match.boundaryQuality,
				match.topQualityChunks)
		}
	}
}

func TestAnalyzeRankedTextQualification(t *testing.T) {
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)

	if _, ok := analyzeRankedText("alpha beta", query, 20, true); ok {
		t.Fatal("strict analysis accepted a window missing gamma")
	}

	got, ok := analyzeRankedText("alpha beta", query, 20, false)
	if !ok {
		t.Fatal("relaxed analysis rejected primary plus one secondary")
	}
	if got.Score != 9 || got.TermCount != 2 || strings.Join(got.MatchedTerms, ",") != "alpha,beta" {
		t.Fatalf("relaxed analysis = (%d, %d, %v), want (9, 2, [alpha beta])", got.Score, got.TermCount, got.MatchedTerms)
	}

	if _, ok := analyzeRankedText("beta gamma", query, 20, false); ok {
		t.Fatal("relaxed analysis accepted secondaries without primary term zero")
	}
}

func TestAnalyzeRankedTextTwoSidedMinimalWindows(t *testing.T) {
	t.Run("redundant trailing duplicate is not a minimal window", func(t *testing.T) {
		text := "ms-" + strings.Repeat(" ", 7) + "api" + strings.Repeat(" ", 87) + "api"
		got, ok := analyzeRankedText(text, compileQuery([]string{"ms-", "api"}, PartialModeContains), 100, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.SpanLength != 13 || got.start != 0 || got.end != 13 {
			t.Fatalf("representative = [%d,%d), span %d; want [0,13), span 13", got.start, got.end, got.SpanLength)
		}
		assertRankedIntervals(t, got.bestIntervals, []rankedInterval{{start: 0, end: 13}})
	})

	t.Run("later whole identifier occurrence contributes once", func(t *testing.T) {
		text := "anchor rapid ms-option-api tail"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"anchor", "api", "tail"}, PartialModeContains),
			100,
			true,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 3 {
			t.Fatalf("BoundaryQuality = %d, want 3", got.BoundaryQuality)
		}
	})
}

func TestAnalyzeRankedTextRepresentativePrecedence(t *testing.T) {
	t.Run("score before all other fields", func(t *testing.T) {
		text := "root x " + strings.Repeat("z", 30) + " root longword"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"root", "longword", "x"}, PartialModeOff),
			15,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.Score != 12 || strings.Join(got.MatchedTerms, ",") != "root,longword" {
			t.Fatalf("representative = score %d terms %v, want score 12 [root longword]", got.Score, got.MatchedTerms)
		}
	})

	t.Run("term count after equal score", func(t *testing.T) {
		text := "aa bbbbbbbb " + strings.Repeat("z", 30) + " aa bbb ccccc"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"aa", "bbbbbbbb", "bbb", "ccccc"}, PartialModeOff),
			15,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.Score != 10 || got.TermCount != 3 || strings.Join(got.MatchedTerms, ",") != "aa,bbb,ccccc" {
			t.Fatalf("representative = (%d, %d, %v), want (10, 3, [aa bbb ccccc])", got.Score, got.TermCount, got.MatchedTerms)
		}
	})

	t.Run("boundary quality before span and start", func(t *testing.T) {
		text := "cms-rapid " + strings.Repeat(" ", 20) +
			"cms-option-api " + strings.Repeat(" ", 20) +
			"ms-option-api"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"ms-", "api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		wantStart := strings.LastIndex(text, "ms-option-api")
		if got.BoundaryQuality != 2 || got.start != wantStart {
			t.Fatalf("representative = start %d quality %d, want start %d quality 2", got.start, got.BoundaryQuality, wantStart)
		}
		if len(got.bestIntervals) != 3 {
			t.Fatalf("best interval count = %d, want 3", len(got.bestIntervals))
		}
	})

	t.Run("span before start", func(t *testing.T) {
		text := "alpha-----beta " + strings.Repeat(" ", 20) + "alpha beta"
		got, ok := analyzeRankedText(text, compileQuery([]string{"alpha", "beta"}, PartialModeContains), 20, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		wantStart := strings.LastIndex(text, "alpha beta")
		if got.SpanLength != len("alpha beta") || got.start != wantStart {
			t.Fatalf("representative = start %d span %d, want start %d span %d", got.start, got.SpanLength, wantStart, len("alpha beta"))
		}
	})

	t.Run("earliest start breaks an exact representative tie", func(t *testing.T) {
		text := "alpha beta " + strings.Repeat(" ", 20) + "alpha beta"
		got, ok := analyzeRankedText(text, compileQuery([]string{"alpha", "beta"}, PartialModeOff), 20, false)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.start != 0 || got.end != len("alpha beta") {
			t.Fatalf("representative = [%d,%d), want [0,%d)", got.start, got.end, len("alpha beta"))
		}
		if len(got.bestIntervals) != 2 {
			t.Fatalf("best interval count = %d, want 2", len(got.bestIntervals))
		}
	})
}

func TestAnalyzeRankedTextBoundaryQualityUTF8(t *testing.T) {
	t.Run("non-ASCII surrounding byte qualifies", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"éapi",
			compileQuery([]string{"api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("non-ASCII following byte qualifies", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"x.apié",
			compileQuery([]string{".api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("invalid surrounding byte qualifies", func(t *testing.T) {
		text := string([]byte{0xff}) + "api"
		got, ok := analyzeRankedText(
			text,
			compileQuery([]string{"api"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})

	t.Run("non-ASCII first byte is not self punctuation", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"aéx",
			compileQuery([]string{"éx"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected qualifying text")
		}
		if got.BoundaryQuality != 0 {
			t.Fatalf("BoundaryQuality = %d, want 0", got.BoundaryQuality)
		}
	})

	t.Run("ASCII punctuation qualifies both self edges", func(t *testing.T) {
		got, ok := analyzeRankedText(
			"a.api-z",
			compileQuery([]string{".api-"}, PartialModeContains),
			20,
			false,
		)
		if !ok {
			t.Fatal("analysis rejected ASCII-punctuation term")
		}
		if got.BoundaryQuality != 1 {
			t.Fatalf("BoundaryQuality = %d, want 1", got.BoundaryQuality)
		}
	})
}

func TestRankedBestWindowsRetainsOnlyCurrentBestPair(t *testing.T) {
	var best rankedBestWindows
	for i := 0; i < 100; i++ {
		start := i * 10
		best.observe(rankedWindow{score: 10, termCount: 2, start: start, end: start + 5})
	}
	best.observe(rankedWindow{score: 20, termCount: 2, start: 2_000, end: 2_010})
	if len(best.intervals) != 1 {
		t.Fatalf("retained %d intervals after a better pair, want 1", len(best.intervals))
	}

	const inferiorWindows = 10_000
	for i := 0; i < inferiorWindows; i++ {
		start := 3_000 + i*10
		best.observe(rankedWindow{score: 10, termCount: 2, start: start, end: start + 5})
	}
	if len(best.intervals) != 1 {
		t.Fatalf("retained %d intervals after %d inferior windows, want 1", len(best.intervals), inferiorWindows)
	}

	for i := 0; i < 3; i++ {
		start := 200_000 + i*10
		best.observe(rankedWindow{score: 20, termCount: 2, start: start, end: start + 5})
	}
	if len(best.intervals) != 4 {
		t.Fatalf("retained %d intervals, want one original plus three tied-best intervals", len(best.intervals))
	}
}

func assertRankedIntervals(t *testing.T, got, want []rankedInterval) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("intervals = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interval %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

package search

import (
	"strings"
	"testing"
)

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

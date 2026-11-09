package search

import (
	"strings"
	"testing"
)

// perFileSubsetCorpus has two separated clusters of unequal quality:
// the first holds three distinct terms (higher quality), the second only
// two (lower quality). Per-file subset selection must emit both clusters,
// best-first, instead of collapsing the file to its single best window.
func perFileSubsetCorpus() string {
	return strings.Join([]string{
		"First cluster begins. alpha beta gamma first cluster text. First cluster ends.",
		perFileSubsetPadding("between-first-second", 90),
		"Second cluster begins. alpha beta second cluster text. Second cluster ends.",
	}, " ")
}

func perFileSubsetPadding(prefix string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = prefix + "-" + threeDigit(i)
	}
	return strings.Join(parts, " ")
}

func threeDigit(i int) string {
	if i < 10 {
		return "00" + string(rune('0'+i))
	}
	if i < 100 {
		return "0" + string(rune('0'+i/10)) + string(rune('0'+i%10))
	}
	return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}

func TestRankedExcerptSpansEmitBothQualityLevelsBestFirst(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := perFileSubsetCorpus()
	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 3, 3, 2, false, PartialModeOff)

	if len(spans) != 2 {
		t.Fatalf("span count = %d, want 2 (both quality levels)", len(spans))
	}
	if spans[0].termCount != 3 || spans[1].termCount != 2 {
		t.Fatalf("span quality order = (%d, %d) terms, want best-first (3, 2)", spans[0].termCount, spans[1].termCount)
	}
	if !strings.Contains(spans[0].text, "gamma") {
		t.Fatalf("first span should cover the three-term cluster: %q", spans[0].text)
	}
	if strings.Contains(spans[1].text, "gamma") {
		t.Fatalf("second span should be the two-term cluster: %q", spans[1].text)
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansDenseLowerQualityNeighborhoodCollapses(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := strings.Join([]string{
		"opening context", strings.Repeat("z", 400),
		"alpha beta gamma dense marker",
		strings.Repeat("a", 130),
		"alpha beta dense marker",
		strings.Repeat("b", 130),
		"alpha beta dense marker",
		strings.Repeat("c", 130),
		"closing context",
	}, " ")

	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 3, 3, 2, false, PartialModeOff)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 (dense neighborhood collapses to the best chunk)", len(spans))
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansRespectDistance(t *testing.T) {
	content := "alpha beta " + strings.Repeat("padding ", 150) + "gamma"
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	const distance = 20
	analysis, ok := analyzeRankedText(content, query, distance, false)
	if !ok || analysis.TermCount != 2 {
		t.Fatalf("Stage 2 = %#v, %v; want two-term match", analysis, ok)
	}
	// Also use a higher target to prove distance rejection independently of the cap.
	for _, target := range []int{analysis.TermCount, 3} {
		for _, isCode := range []bool{false, true} {
			spans := extractRankedExcerptSpansQuery(content, query, 3, target, 2, isCode, distance)
			if len(spans) != 1 {
				t.Fatalf("target=%d code=%v: got %d spans, want 1", target, isCode, len(spans))
			}
			if spans[0].termCount != 2 || spans[0].score != analysis.Score || strings.Contains(spans[0].text, "gamma") {
				t.Fatalf("target=%d code=%v: stitched distant term or wrong quality: %#v", target, isCode, spans[0])
			}
		}
	}
}

func TestRankedExcerptSpansCapCoverageAtTarget(t *testing.T) {
	content := "alpha beta gamma"
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, false, len(content))
	if len(spans) != 1 || spans[0].termCount != 2 || spans[0].score != len("alpha")+len("beta") {
		t.Fatalf("spans = %#v, want one two-term candidate capped at target", spans)
	}
}

func TestRankedExcerptSpansRequirePrimaryTerm(t *testing.T) {
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	for _, content := range []string{
		"beta gamma",
		"alpha " + strings.Repeat("padding ", 150) + "beta gamma",
	} {
		spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, false, 20)
		if len(spans) != 0 {
			t.Fatalf("spans = %#v, want no relaxed candidates without nearby primary term", spans)
		}
	}
}

func TestRankedExcerptSpansDistanceBoundary(t *testing.T) {
	const content = "alpha beta"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	for _, distance := range []int{0, 5, 6} {
		_, qualifies := analyzeRankedText(content, query, distance, true)
		spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, false, distance)
		if (len(spans) == 1) != qualifies {
			t.Fatalf("distance=%d: got %d spans, Stage 2 qualifies=%v", distance, len(spans), qualifies)
		}
	}
}

// A wide three-term candidate from either early cluster to the sole gamma
// overlaps less than 10% of its length with the final cluster. It must not
// reuse that endpoint and reserve the intervening two-term clusters.
func endpointNoveltyCorpus() string {
	return "alpha beta first cluster\n" + strings.Repeat("padding ", 500) +
		"\nalpha beta middle cluster\n" + strings.Repeat("padding ", 500) +
		"\nalpha beta gamma final cluster"
}

func TestRankedExcerptSpansRejectReusedRightEndpoint(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	content := endpointNoveltyCorpus()
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	for _, isCode := range []bool{false, true} {
		spans := extractRankedExcerptSpansQuery(content, query, 3, 3, 2, isCode, len(content))
		if len(spans) != 3 {
			t.Fatalf("code=%v: got %d spans, want all three clusters", isCode, len(spans))
		}
		for i, marker := range []string{"final cluster", "first cluster", "middle cluster"} {
			wantTerms := 2
			if i == 0 {
				wantTerms = 3
			}
			if spans[i].termCount != wantTerms || !strings.Contains(spans[i].text, marker) {
				t.Fatalf("code=%v span %d = %#v, want %s with %d terms", isCode, i, spans[i], marker, wantTerms)
			}
			if i > 0 && strings.Contains(spans[i].text, "gamma") {
				t.Fatalf("code=%v span %d reuses sole gamma: %q", isCode, i, spans[i].text)
			}
		}
		requireBoundedNovelSpans(t, content, spans, "alpha")
	}
}

func TestRankedExcerptSpansAllowContextOnlyOverlap(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	content := strings.Repeat("x ", 200) + "alpha beta first" + strings.Repeat(" x", 182) + " alpha beta other" + strings.Repeat(" x", 200)
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, true, 20)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want two distinct clusters", len(spans))
	}
	if overlap := spanOverlap(spans[0], spans[1]); overlap <= 0 {
		t.Fatal("fixture must exercise positive context overlap")
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansRespectMaxExcerpts(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := perFileSubsetCorpus()
	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 1, 3, 2, false, PartialModeOff)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 for maxExcerpts = 1", len(spans))
	}
	if spans[0].termCount != 3 {
		t.Fatalf("single span should be the best cluster, got %d terms", spans[0].termCount)
	}
}

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

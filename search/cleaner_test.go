package search

import (
	"strings"
	"testing"
)

func TestExtractMeaningfulExcerpts_ReturnsMultipleNonOverlappingMatches(t *testing.T) {
	content := strings.Join([]string{
		"alpha provider access_token one.",
		strings.Repeat("filler ", 120),
		"second provider access_token two.",
		strings.Repeat("filler ", 120),
		"third provider access_token three.",
	}, " ")

	excerpts := ExtractMeaningfulExcerpts(content, []string{"provider", "access_token"}, 3, 2)
	if len(excerpts) != 3 {
		t.Fatalf("expected 3 excerpts, got %d: %#v", len(excerpts), excerpts)
	}

	for i, excerpt := range excerpts {
		if !strings.Contains(excerpt, "provider") || !strings.Contains(excerpt, "access_token") {
			t.Fatalf("excerpt %d should contain all terms, got %q", i+1, excerpt)
		}
	}
	if excerpts[0] == excerpts[1] || excerpts[1] == excerpts[2] || excerpts[0] == excerpts[2] {
		t.Fatalf("expected distinct non-overlapping excerpts, got %#v", excerpts)
	}
}

func TestExtractMeaningfulExcerpts_UsesTargetScoreForPartialMatchCluster(t *testing.T) {
	content := "alpha beta gamma form one cohesive ranked cluster. " +
		strings.Repeat("unrelated filler ", 80)

	excerpts := ExtractMeaningfulExcerpts(content, []string{"alpha", "beta", "gamma", "delta"}, 1, 3)
	if len(excerpts) != 1 {
		t.Fatalf("expected one excerpt for the partial-match cluster, got %d: %#v", len(excerpts), excerpts)
	}

	for _, term := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(excerpts[0], term) {
			t.Fatalf("excerpt should contain ranked cluster term %q, got %q", term, excerpts[0])
		}
	}
}

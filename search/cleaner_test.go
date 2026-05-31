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

	excerpts := ExtractMeaningfulExcerpts(content, []string{"provider", "access_token"}, 3)
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

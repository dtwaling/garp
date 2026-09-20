package search

import (
	"strings"
	"testing"
)

func TestCleanerPartialExcerptUsesCapturedTokenOffsets(t *testing.T) {
	content := "system _deployment in progress"
	excerpts := ExtractMeaningfulExcerptsPartial(content, []string{"deploy"}, 1, 1, PartialModePrefix)
	if len(excerpts) != 1 {
		t.Fatalf("expected one partial-match excerpt, got %d: %#v", len(excerpts), excerpts)
	}
	if got, want := excerpts[0], "system _deployment in progress"; got != want {
		t.Fatalf("excerpt = %q, want %q", got, want)
	}

	loc := buildTermRegexCI("deploy", PartialModePrefix).FindStringSubmatchIndex(content)
	if len(loc) < 4 || loc[2] != strings.Index(content, "deployment") {
		t.Fatalf("captured token offset = %v, want %d (the d in deployment)", loc, strings.Index(content, "deployment"))
	}
}

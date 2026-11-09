package search

import (
	"strings"
	"testing"
)

func TestMultipartExcerptDocumentOrderAndMergedWindows(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	for _, adjacent := range []bool{false, true} {
		gap := " " + strings.Repeat("filler ", 100)
		if adjacent {
			gap = " shared-context "
		}
		raw := strings.Repeat("prefix ", 30) + "alpha" + gap + "beta " + strings.Repeat("filler ", 100) + "gamma " + strings.Repeat("suffix ", 30)
		spans := extractRankedExcerptSpans(raw, []string{"gamma", "beta", "alpha"}, 1, 3, 0, false, PartialModeOff)
		if len(spans) != 1 {
			t.Fatalf("adjacent=%v: got %d spans", adjacent, len(spans))
		}
		text := spans[0].text
		previous := -1
		for _, term := range []string{"alpha", "beta", "gamma"} {
			pos := strings.Index(text, term)
			if pos <= previous || strings.Count(text, term) != 1 {
				t.Fatalf("adjacent=%v: term %q duplicated, missing, or out of order: %q", adjacent, term, text)
			}
			previous = pos
		}
		if adjacent && strings.Count(text, "shared-context") != 1 {
			t.Fatalf("overlapping context duplicated: %q", text)
		}
		for _, match := range spans[0].matches {
			if text[match.start:match.end] != raw[match.source.start:match.source.end] {
				t.Fatalf("incorrect shifted match: %+v in %q", match, text)
			}
		}
	}
}

func TestMultipartExcerptTruncationPreservesRawAnchor(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	terms := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"}
	raw := "> alpha beta gamma delta epsilon zeta eta theta\nheader\n" + strings.Repeat("padding ", 10)
	anchor := len(raw)
	raw += strings.Join(terms, " "+strings.Repeat("filler ", 100)) + " " + strings.Repeat("tail ", 30)
	query := compileQuery(terms, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(raw, query, 1, len(terms), 0, false, unconstrainedDistance)
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	span := &spans[0]
	if len(span.text) != 400 || len(span.matches) >= len(terms) || strings.Contains(span.text, "theta") {
		t.Fatalf("fixture must truncate later matches: text=%q matches=%+v", span.text, span.matches)
	}
	if !span.rawStartValid || span.rawStart != anchor || len(span.sourceMatches) != len(terms) {
		t.Fatalf("lost seed identity: %+v", span)
	}
	if lines := computeExcerptSpanLinesQuery(raw, spans, query, 5000); lines[0] != 3 {
		t.Fatalf("multipart anchor line=%v, want 3", lines)
	}
	span.text, span.matches = truncateExcerpt(span.text, span.matches, 1)
	if len(span.matches) != 0 || span.rawStart != anchor || !span.rawStartValid {
		t.Fatalf("direct truncation lost seed identity: %+v", span)
	}
	if lines := computeExcerptSpanLinesQuery(raw, spans, query, 5000); lines[0] != 3 {
		t.Fatalf("anchor without displayed matches=%v, want 3", lines)
	}
}

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

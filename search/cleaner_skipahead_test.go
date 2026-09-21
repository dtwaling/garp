package search

import (
	"strings"
	"testing"
)

func priorOverlap(span excerptSpan, prior []excerptSpan) int {
	overlap := 0
	for _, earlier := range prior {
		overlap += spanOverlap(span, earlier)
	}
	return overlap
}

func requireBoundedNovelSpans(t *testing.T, content string, spans []excerptSpan, term string) {
	t.Helper()
	if len(spans) == 0 {
		t.Fatal("expected at least one excerpt span")
	}
	for i, span := range spans {
		if span.right <= span.left {
			t.Fatalf("span %d has invalid range [%d, %d)", i, span.left, span.right)
		}
		if overlap := priorOverlap(span, spans[:i]); overlap > int(excerptOverlapTolerance*float64(span.right-span.left)) {
			t.Fatalf("span %d overlap = %d, exceeds tolerance %d for [%d, %d)", i, overlap, int(excerptOverlapTolerance*float64(span.right-span.left)), span.left, span.right)
		}
		foundNewTerm := false
		for start := strings.Index(content[span.left:span.right], term); start >= 0; {
			absoluteStart := span.left + start
			insidePrior := false
			for _, earlier := range spans[:i] {
				if absoluteStart >= earlier.left && absoluteStart < earlier.right {
					insidePrior = true
					break
				}
			}
			if !insidePrior {
				foundNewTerm = true
				break
			}
			next := strings.Index(content[absoluteStart+len(term):span.right], term)
			if next < 0 {
				break
			}
			start = absoluteStart + len(term) + next - span.left
		}
		if !foundNewTerm {
			t.Fatalf("span %d has no %q occurrence starting outside prior emitted spans", i, term)
		}
	}
}

func TestSkipAheadDenseSingleTerm(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := strings.Join([]string{
		"opening context", strings.Repeat("z", 400), strings.Repeat("a", 130), "needle first marker",
		strings.Repeat("b", 130), "needle second marker", strings.Repeat("c", 130),
		"needle third marker", strings.Repeat("d", 130), "closing context",
	}, " ")

	spans := extractExcerptSpans(content, []string{"needle"}, 3, false)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 bounded-overlap chunk", len(spans))
	}
	requireBoundedNovelSpans(t, content, spans, "needle")
}

func TestSkipAheadKeepsSeparatedClusters(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := strings.Join([]string{
		strings.Repeat("a", 350), "needle first", strings.Repeat("b", 500),
		"needle second", strings.Repeat("c", 500), "needle third", strings.Repeat("d", 350),
	}, " ")

	spans := extractExcerptSpans(content, []string{"needle"}, 3, false)
	if len(spans) != 3 {
		t.Fatalf("span count = %d, want 3 separated chunks", len(spans))
	}
	requireBoundedNovelSpans(t, content, spans, "needle")
}

func TestSkipAheadDenseMultiTerm(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := strings.Join([]string{
		"opening context", strings.Repeat("z", 400), strings.Repeat("a", 130), "deploy config first marker",
		strings.Repeat("b", 130), "deploy config second marker", strings.Repeat("c", 130),
		"deploy config third marker", strings.Repeat("d", 130), "closing context",
	}, " ")

	spans := extractExcerptSpans(content, []string{"deploy", "config"}, 3, false)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 bounded-overlap chunk", len(spans))
	}
	requireBoundedNovelSpans(t, content, spans, "deploy")
	if !strings.Contains(spans[0].text, "config") {
		t.Fatalf("excerpt does not contain config: %q", spans[0].text)
	}
}

func TestSkipAheadHonorsMaxExcerptsAndBudget(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 240 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	content := strings.Join([]string{
		strings.Repeat("a", 350), "needle first", strings.Repeat("b", 500),
		"needle second", strings.Repeat("c", 500), "needle third", strings.Repeat("d", 350),
	}, " ")

	spans := extractExcerptSpans(content, []string{"needle"}, 1, false)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 for maxExcerpts = 1", len(spans))
	}
	if len(spans[0].text) > 240 {
		t.Fatalf("excerpt length = %d, exceeds budget 240", len(spans[0].text))
	}
	if !strings.Contains(spans[0].text, "needle first") {
		t.Fatalf("single excerpt does not contain the first cluster: %q", spans[0].text)
	}
}

func TestSkipAheadOverlapToleranceBoundary(t *testing.T) {
	prior := excerptSpan{left: 100, right: 200}
	tests := []struct {
		name string
		span excerptSpan
		want bool
	}{
		{name: "exactly ten percent overlap is accepted", span: excerptSpan{left: 190, right: 290}, want: true},
		{name: "more than ten percent overlap is rejected", span: excerptSpan{left: 180, right: 280}, want: false},
		{name: "deep overlap is rejected", span: excerptSpan{left: 120, right: 220}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excerptSpanWithinOverlapTolerance(tt.span, []excerptSpan{prior}); got != tt.want {
				t.Fatalf("within overlap tolerance = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSkipAheadOverlapUsesEmittedUnion(t *testing.T) {
	emitted := []excerptSpan{
		{left: 100, right: 200},
		{left: 190, right: 290},
	}
	// The candidate intersects the union by 95 bytes. The two emitted spans
	// share 5 of those bytes, which must not be counted twice.
	candidate := excerptSpan{left: 195, right: 1155}
	if !excerptSpanWithinOverlapTolerance(candidate, emitted) {
		t.Fatal("candidate with 95 bytes of union overlap should fit its 96-byte tolerance")
	}
}

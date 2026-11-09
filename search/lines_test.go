package search

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestEmittedExcerptLinesKeepClusterIdentity(t *testing.T) {
	for _, isCode := range []bool{false, true} {
		for _, limit := range []int{1, 2} {
			t.Run(fmt.Sprintf("code=%t/limit=%d", isCode, limit), func(t *testing.T) {
				raw := "header\nalpha " + strings.Repeat("loose ", 30) + "beta.\n" +
					strings.Repeat("padding line.\n", 100) + "alpha beta tight cluster.\n"
				query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
				content := CleanContent(raw)
				if isCode {
					content = CleanContentCode(raw)
				}
				spans := extractRankedExcerptSpansQuery(content, query, limit, 2, 2, isCode, 5000)
				if len(spans) != limit || !strings.Contains(spans[0].text, "tight cluster") {
					t.Fatalf("expected tighter cluster ranked first in %d spans: %#v", limit, spans)
				}
				want := []int{103}
				if limit == 2 {
					want = append(want, 2)
				}
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, want) {
					t.Fatalf("ranked anchors = %v, want %v", got, want)
				}
				slices.SortFunc(spans, func(a, b excerptSpan) int { return a.left - b.left })
				slices.Sort(want)
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, want) {
					t.Fatalf("document-order anchors = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestExcerptLineWindowHonorsCallerDistance(t *testing.T) {
	raw := "alpha\n" + strings.Repeat("filler ", 200) + "\nbeta\nalpha\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	// The two-term cluster starts on line 1, not the rare beta on line 3.
	// A second alpha prevents choosing alpha simply because it is rarest.
	raw = strings.Replace(raw, "\nbeta\nalpha\n", "\nbeta\n"+strings.Repeat("filler ", 800)+"\nalpha\n", 1)
	for _, window := range []int{5000, 6000} {
		if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0, 1}), query, window); !slices.Equal(got, []int{1}) {
			t.Fatalf("window %d: anchors = %v, want [1]", window, got)
		}
	}
	if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0, 1}), query, 800); !slices.Equal(got, []int{3}) {
		t.Fatalf("window 800: anchors = %v, want [3]", got)
	}
}

func lineSpans(termSets ...[]int) []excerptSpan {
	spans := make([]excerptSpan, len(termSets))
	for i, terms := range termSets {
		for _, term := range terms {
			spans[i].matches = append(spans[i].matches, excerptMatch{term: term})
		}
	}
	return spans
}

func TestLineIndexerLineOf(t *testing.T) {
	raw := "alpha\nbeta\ngamma\ndelta"
	li := newLineIndexer(raw)
	cases := []struct {
		off  int
		want int
	}{
		{0, 1},              // 'a' of alpha
		{4, 1},              // 'a' of alpha (end of line 1)
		{6, 2},              // 'b' of beta
		{11, 3},             // 'g' of gamma
		{len(raw) - 1, 4},   // 'a' of delta
		{len(raw) + 100, 4}, // past end clamps to last line
		{-5, 1},             // negative clamps to first line
	}
	for _, c := range cases {
		if got := li.lineOf(c.off); got != c.want {
			t.Errorf("lineOf(%d) = %d, want %d", c.off, got, c.want)
		}
	}
}

func TestComputeExcerptLinesSingleTerm(t *testing.T) {
	// 'needle' lives on line 3 (1-based).
	raw := "line one\nline two\nhere is the needle right here\nline four"
	query := compileQuery([]string{"needle"}, PartialModeOff)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}), query, 0)
	if len(starts) != 1 {
		t.Fatalf("expected 1 start line, got %d", len(starts))
	}
	if starts[0] != 3 {
		t.Errorf("start line = %d, want 3", starts[0])
	}
}

func TestComputeExcerptLinesMultiTermStartsAtEarliest(t *testing.T) {
	// 'alpha' on line 2, 'omega' on line 4 -> start line is the earliest term (line 2).
	raw := "intro\nthe alpha begins\nmiddle filler line\nthe omega ends\noutro"
	query := compileQuery([]string{"alpha", "omega"}, PartialModeOff)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0, 1}), query, 5000)
	if starts[0] != 2 {
		t.Errorf("start line = %d, want 2", starts[0])
	}
}

func TestComputeExcerptLinesDisambiguatesRepeats(t *testing.T) {
	// 'config' appears on lines 1 and 5; the rare term 'payload' (line 5) should pin the
	// cluster to the second occurrence.
	raw := "config header here\nbody\nmore body\nfiller\nconfig with payload inside\ntail"
	query := compileQuery([]string{"config", "payload"}, PartialModeOff)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0, 1}), query, 5000)
	if starts[0] != 5 {
		t.Errorf("start line = %d, want 5 (second config cluster)", starts[0])
	}
}

func TestComputeExcerptLinesMultipleExcerptsInOrder(t *testing.T) {
	// Two clusters of 'target'; excerpts should map to them in document order.
	raw := "target near top\nx\nx\nx\nx\nx\nx\nx\nx\nx\ntarget near bottom"
	query := compileQuery([]string{"target"}, PartialModeOff)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}, []int{0}), query, 200)
	if starts[0] != 1 {
		t.Errorf("excerpt 0 start line = %d, want 1", starts[0])
	}
	if starts[1] != 11 {
		t.Errorf("excerpt 1 start line = %d, want 11", starts[1])
	}
}

func TestComputeExcerptLinesNoTermLeavesUnknown(t *testing.T) {
	raw := "nothing relevant here\nstill nothing"
	query := compileQuery([]string{"absentword"}, PartialModeOff)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans(nil), query, 0)
	if starts[0] != 0 {
		t.Errorf("expected unknown (0), got %d", starts[0])
	}
}

func TestComputeExcerptLinesNormalizedWhitespace(t *testing.T) {
	raw := "alpha\nbeta\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := lineSpans([]int{0, 1})
	spans[0].text = "alpha beta"

	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); len(got) != 1 || got[0] != 1 {
		t.Fatalf("normalized whitespace line anchor = %v, want [1]", got)
	}
}

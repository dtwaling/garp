package search

import (
	"slices"
	"strings"
	"testing"
)

func TestMultipartExcerptLinesKeepWideClusterIdentity(t *testing.T) {
	raw := "header\nalpha " + strings.Repeat("loose ", 300) + "beta.\n" +
		strings.Repeat("padding line.\n", 100) + "alpha " + strings.Repeat("tight ", 180) + "beta.\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(CleanContentCode(raw), query, 2, 2, 2, true, 5000)
	if len(spans) != 2 || !strings.Contains(spans[0].text, " ... ") || !strings.Contains(spans[0].text, "tight") {
		t.Fatalf("expected later multipart cluster first: %#v", spans)
	}
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{103, 2}) {
		t.Fatalf("multipart anchors = %v, want [103 2]", got)
	}
}

func TestExcerptLineAnchorsFollowDistinctRawClusters(t *testing.T) {
	raw := strings.Join([]string{
		"header",
		"needle deploy first cluster",
		"between first and second",
		"needle deploy second cluster",
		"between second and third",
		"needle deploy third cluster",
	}, "\n")
	query := compileQuery([]string{"needle", "deploy"}, PartialModeOff)
	spans := lineSpans([]int{0, 1}, []int{0, 1}, []int{0, 1})
	starts := computeExcerptSpanLinesQuery(raw, spans, query, 200)
	want := []int{2, 4, 6}
	if len(starts) != len(want) {
		t.Fatalf("got %d anchors, want %d", len(starts), len(want))
	}
	lines := strings.Split(raw, "\n")
	for i, start := range starts {
		if start != want[i] {
			t.Errorf("excerpt %d anchor = line %d, want line %d", i, start, want[i])
		}
		if i > 0 && start < starts[i-1] {
			t.Errorf("excerpt %d anchor = line %d, before prior line %d", i, start, starts[i-1])
		}
		if i > 0 && start == starts[i-1] {
			t.Errorf("excerpt %d reused line %d despite distinct raw clusters", i, start)
		}
		if start == 0 || start > len(lines) {
			t.Errorf("excerpt %d has invalid anchor line %d", i, start)
			continue
		}
		line := lines[start-1]
		if !strings.Contains(line, "needle") && !strings.Contains(line, "deploy") {
			t.Errorf("excerpt %d anchor line %d does not contain an excerpt term: %q", i, start, line)
		}
	}
}

func TestExcerptLineAnchorsMayRepeatOnSingleLine(t *testing.T) {
	raw := "needle deploy first cluster; needle deploy second cluster; needle deploy third cluster"
	query := compileQuery([]string{"needle", "deploy"}, PartialModeOff)
	spans := lineSpans([]int{0, 1}, []int{0, 1}, []int{0, 1})
	starts := computeExcerptSpanLinesQuery(raw, spans, query, 200)
	if len(starts) != len(spans) {
		t.Fatalf("got %d anchors, want %d", len(starts), len(spans))
	}
	for i, start := range starts {
		if start != 1 {
			t.Errorf("excerpt %d anchor = line %d, want line 1 for a single-line file", i, start)
		}
		if i > 0 && start < starts[i-1] {
			t.Errorf("excerpt %d anchor = line %d, before prior line %d", i, start, starts[i-1])
		}
	}
}

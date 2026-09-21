package search

import (
	"strings"
	"testing"
)

func TestExcerptLineAnchorsFollowDistinctRawClusters(t *testing.T) {
	raw := strings.Join([]string{
		"header",
		"needle deploy first cluster",
		"between first and second",
		"needle deploy second cluster",
		"between second and third",
		"needle deploy third cluster",
	}, "\n")
	excerpts := []string{
		"needle deploy first cluster",
		"needle deploy second cluster",
		"needle deploy third cluster",
	}
	terms := []string{"needle", "deploy"}

	starts := computeExcerptLines(raw, excerpts, terms, 200)
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
	excerpts := []string{
		"needle deploy first cluster",
		"needle deploy second cluster",
		"needle deploy third cluster",
	}

	starts := computeExcerptLines(raw, excerpts, []string{"needle", "deploy"}, 200)
	if len(starts) != len(excerpts) {
		t.Fatalf("got %d anchors, want %d", len(starts), len(excerpts))
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

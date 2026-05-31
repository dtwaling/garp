package search

import "testing"

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
	excerpts := []string{"here is the needle right here"}
	starts := computeExcerptLines(raw, excerpts, []string{"needle"}, 0)
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
	excerpts := []string{"the alpha begins ... the omega ends"}
	starts := computeExcerptLines(raw, excerpts, []string{"alpha", "omega"}, 5000)
	if starts[0] != 2 {
		t.Errorf("start line = %d, want 2", starts[0])
	}
}

func TestComputeExcerptLinesDisambiguatesRepeats(t *testing.T) {
	// 'config' appears on lines 1 and 5; the rare term 'payload' (line 5) should pin the
	// cluster to the second occurrence.
	raw := "config header here\nbody\nmore body\nfiller\nconfig with payload inside\ntail"
	excerpts := []string{"config with payload inside"}
	starts := computeExcerptLines(raw, excerpts, []string{"config", "payload"}, 5000)
	if starts[0] != 5 {
		t.Errorf("start line = %d, want 5 (second config cluster)", starts[0])
	}
}

func TestComputeExcerptLinesMultipleExcerptsInOrder(t *testing.T) {
	// Two clusters of 'target'; excerpts should map to them in document order.
	raw := "target near top\nx\nx\nx\nx\nx\nx\nx\nx\nx\ntarget near bottom"
	excerpts := []string{"target near top", "target near bottom"}
	starts := computeExcerptLines(raw, excerpts, []string{"target"}, 200)
	if starts[0] != 1 {
		t.Errorf("excerpt 0 start line = %d, want 1", starts[0])
	}
	if starts[1] != 11 {
		t.Errorf("excerpt 1 start line = %d, want 11", starts[1])
	}
}

func TestComputeExcerptLinesNoTermLeavesUnknown(t *testing.T) {
	raw := "nothing relevant here\nstill nothing"
	excerpts := []string{"some excerpt text without the term"}
	starts := computeExcerptLines(raw, excerpts, []string{"absentword"}, 0)
	if starts[0] != 0 {
		t.Errorf("expected unknown (0), got %d", starts[0])
	}
}

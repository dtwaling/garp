package search

import (
	"strings"
	"testing"
)

func TestLinesPartialAnchorsDerivativeOnSourceLine(t *testing.T) {
	raw := strings.Repeat("filler\n", 41) + "func run_deployment() {\n"
	excerpts := []string{"func run_deployment() {"}

	starts := computeExcerptLinesPartial(raw, excerpts, []string{"deploy"}, 0, PartialModePrefix)
	if len(starts) != 1 {
		t.Fatalf("expected one start line, got %d", len(starts))
	}
	if starts[0] != 42 {
		t.Errorf("start line = %d, want 42", starts[0])
	}
}

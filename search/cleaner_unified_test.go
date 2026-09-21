package search

import (
	"strings"
	"testing"
)

func TestUnifiedExcerptSelection(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	t.Run("single term uses budget-padded selector for separated occurrences", func(t *testing.T) {
		content := strings.Join([]string{
			strings.Repeat("a", 350), "needle first", strings.Repeat("b", 500),
			"needle second", strings.Repeat("c", 500), "needle third", strings.Repeat("d", 350),
		}, " ")

		spans := extractExcerptSpans(content, []string{"needle"}, 3, false)
		if len(spans) != 3 {
			t.Fatalf("span count = %d, want 3", len(spans))
		}
		for i, span := range spans {
			if i > 0 && span.left <= spans[i-1].left {
				t.Fatalf("spans are not in document order: span %d left %d, previous left %d", i, span.left, spans[i-1].left)
			}
			if !strings.Contains(span.text, "needle") {
				t.Fatalf("span %d does not contain needle: %q", i, span.text)
			}
			if len(span.text) < 350 || len(span.text) > 400 {
				t.Fatalf("span %d length = %d, want budget-padded excerpt in [350, 400]", i, len(span.text))
			}
		}
	})

	t.Run("dense single term is reduced by skip ahead", func(t *testing.T) {
		content := strings.Join([]string{
			"opening", strings.Repeat("z", 400), strings.Repeat("a", 130), "needle first", strings.Repeat("b", 130),
			"needle second", strings.Repeat("c", 130), "needle third", strings.Repeat("d", 130), "closing",
		}, " ")

		spans := extractExcerptSpans(content, []string{"needle"}, 3, false)
		if len(spans) != 1 {
			t.Fatalf("span count = %d, want 1 bounded-overlap chunk", len(spans))
		}
		for i, span := range spans {
			if i > 0 && span.left <= spans[i-1].left {
				t.Fatalf("spans are not in document order: span %d left %d, previous left %d", i, span.left, spans[i-1].left)
			}
			if !strings.Contains(span.text, "needle") {
				t.Fatalf("span %d does not contain needle", i)
			}
		}
	})

	t.Run("quoted phrase remains atomic", func(t *testing.T) {
		content := strings.Join([]string{
			strings.Repeat("a", 350), "deploy config first", strings.Repeat("b", 500),
			"deploy config second", strings.Repeat("c", 350),
		}, " ")

		spans := extractExcerptSpans(content, []string{"deploy config"}, 2, false)
		if len(spans) != 2 {
			t.Fatalf("span count = %d, want 2", len(spans))
		}
		for i, span := range spans {
			if !strings.Contains(span.text, "deploy config") {
				t.Fatalf("span %d does not contain complete phrase: %q", i, span.text)
			}
		}
	})
}

package search

import (
	"slices"
	"strings"
	"testing"
)

func TestStage3MappedCleaning(t *testing.T) {
	raw := "<style>hidden</style>\n<script>hidden</script>\n> hidden\n-----\n<b>é</b> A1,2B\tend\xff\n"
	for _, isCode := range []bool{false, true} {
		mapped := cleanStage3Content(raw, isCode)
		want := "é A 1, 2 B end\xff"
		if isCode {
			want = CleanContentCode(raw)
		}
		if mapped.text != want || len(mapped.offsets) != len(mapped.text) {
			t.Fatalf("code=%t: mapped=%q offsets=%d, want %q", isCode, mapped.text, len(mapped.offsets), want)
		}
		for i, offset := range mapped.offsets {
			if offset < 0 || offset >= len(raw) || i > 0 && offset < mapped.offsets[i-1] {
				t.Fatalf("code=%t: invalid/nonmonotonic offset at %d: %v", isCode, i, mapped.offsets)
			}
			if mapped.text[i] != ' ' && mapped.text[i] != raw[offset] {
				t.Fatalf("code=%t: byte %d lost identity at raw %d", isCode, i, offset)
			}
		}
	}
}

func TestStage3RawAnchorSurvivesDisplayChanges(t *testing.T) {
	for _, isCode := range []bool{false, true} {
		raw := "<script>alpha beta</script>\n> alpha beta\n" +
			"beta " + strings.Repeat("wide ", 150) + "alpha.\n"
		if isCode {
			raw = "// header\n\n" + "beta " + strings.Repeat("wide ", 150) + "alpha.\n"
		}
		query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
		spans := extractRankedExcerptSpansQuery(raw, query, 1, 2, 2, isCode, 5000)
		if len(spans) != 1 || !spans[0].rawStartValid || spans[0].rawStart != strings.LastIndex(raw, "beta") {
			t.Fatalf("code=%t: missing raw seed: %#v", isCode, spans)
		}
		anchor := spans[0].rawStart
		spans[0] = expandShortExcerptSpan(spans[0], 1000)
		if spans[0].rawStart != anchor || !spans[0].rawStartValid {
			t.Fatal("expansion changed the seed anchor")
		}
		spans[0].text, spans[0].matches = truncateExcerpt(spans[0].text, spans[0].matches, 0)
		if len(spans[0].matches) != 0 {
			t.Fatal("fixture must delete all displayed matches")
		}
		if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{3}) {
			t.Fatalf("code=%t: truncated anchor=%v, want [3]", isCode, got)
		}
	}
}

func TestStage3RawAnchorForCreatedOCRMatch(t *testing.T) {
	raw := "<script>ALPHA</script>\n> ALPHA\n-----\nALPHA2 beta\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(raw, query, 1, 2, 2, false, 200)
	if len(spans) != 1 || !spans[0].rawStartValid {
		t.Fatalf("expected OCR-created cluster: %#v", spans)
	}
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{4}) {
		t.Fatalf("OCR anchor=%v, want [4]", got)
	}
}

func TestStage3LegacyFallbackStrippedAndExhausted(t *testing.T) {
	raw := "<style>needle</style>\n<script>needle</script>\n> needle\n-----\nneedle\nneedle\n"
	query := compileQuery([]string{"needle"}, PartialModeOff)
	spans := lineSpans([]int{0}, []int{0}, []int{0})
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{5, 6, 0}) {
		t.Fatalf("legacy anchors=%v, want [5 6 0]", got)
	}
}

func TestStage3RawAnchorValidity(t *testing.T) {
	raw := "header\nneedle\n"
	query := compileQuery([]string{"needle"}, PartialModeOff)
	span := lineSpans([]int{0})[0]
	span.rawStartValid = true
	if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{span}, query, 200); !slices.Equal(got, []int{1}) {
		t.Fatalf("valid zero anchor=%v, want [1]", got)
	}
	span.rawStartValid = false
	if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{span}, query, 200); !slices.Equal(got, []int{2}) {
		t.Fatalf("unknown zero must use fallback: %v, want [2]", got)
	}
}

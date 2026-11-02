package search

import (
	"slices"
	"strings"
	"testing"
)

func TestLinesPartialAnchorsDerivativeOnSourceLine(t *testing.T) {
	raw := strings.Repeat("filler\n", 41) + "func run_deployment() {\n"
	query := compileQuery([]string{"deploy"}, PartialModePrefix)
	starts := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}), query, 0)
	if len(starts) != 1 {
		t.Fatalf("expected one start line, got %d", len(starts))
	}
	if starts[0] != 42 {
		t.Errorf("start line = %d, want 42", starts[0])
	}
}

func TestInlineWildcardLineAnchorsUseExactRanges(t *testing.T) {
	raw := "header\ncall ms-option-api\nuse MyOptionApiClient\ncall ms-x-api\n"
	tests := []struct {
		name    string
		excerpt string
		term    string
		want    int
	}{
		{name: "internal star", excerpt: "call ms-option-api", term: "ms-*-api", want: 2},
		{name: "leading and trailing stars", excerpt: "use MyOptionApiClient", term: "*OptionApi*", want: 3},
		{name: "single byte question", excerpt: "call ms-x-api", term: "ms-?-api", want: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := compileQuery([]string{tt.term}, PartialModeOff)
			loc, ok := query.matchers[0].find(raw, 0)
			if !ok {
				t.Fatalf("authoritative match missing for %q", tt.term)
			}
			spans := []excerptSpan{{
				text: tt.excerpt,
				matches: []excerptMatch{{
					matchRange: matchRange{start: 0, end: loc.end - loc.start},
					source:     loc,
					term:       0,
				}},
			}}
			got := computeExcerptSpanLinesQuery(raw, spans, query, 200)
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("line anchors = %v, want [%d]", got, tt.want)
			}
		})
	}
}

func TestLineAnchorsDistinguishInvalidUTF8FromReplacementRune(t *testing.T) {
	invalid := string([]byte{0xff})
	raw := "invalid " + invalid + "\nreplacement \uFFFD\n"

	invalidQuery := compileQuery([]string{invalid}, PartialModeOff)
	invalidRange, ok := invalidQuery.matchers[0].find(raw, 0)
	if !ok {
		t.Fatal("invalid UTF-8 authoritative match missing")
	}
	invalidSpan := excerptSpan{matches: []excerptMatch{{source: invalidRange, term: 0}}}
	if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{invalidSpan}, invalidQuery, 200); len(got) != 1 || got[0] != 1 {
		t.Fatalf("invalid UTF-8 line anchors = %v, want [1]", got)
	}

	replacementQuery := compileQuery([]string{"\uFFFD"}, PartialModeOff)
	replacementRange, ok := replacementQuery.matchers[0].find(raw, 0)
	if !ok {
		t.Fatal("U+FFFD authoritative match missing")
	}
	replacementSpan := excerptSpan{matches: []excerptMatch{{source: replacementRange, term: 0}}}
	if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{replacementSpan}, replacementQuery, 200); len(got) != 1 || got[0] != 2 {
		t.Fatalf("U+FFFD line anchors = %v, want [2]", got)
	}
}

func TestLineAnchorsRetainAdjacentPunctuationMatches(t *testing.T) {
	raw := ".\n..\n"
	query := compileQuery([]string{"."}, PartialModeOff)
	ranges := query.matchers[0].findAll(raw)
	if len(ranges) != 3 {
		t.Fatalf("authoritative punctuation ranges = %#v, want 3", ranges)
	}
	spans := make([]excerptSpan, len(ranges))
	for i, loc := range ranges {
		spans[i].matches = []excerptMatch{{source: loc, term: 0}}
	}
	got := computeExcerptSpanLinesQuery(raw, spans, query, 200)
	want := []int{1, 2, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("punctuation line anchors = %v, want %v", got, want)
		}
	}
}

func TestExcerptLineAnchorsDoNotCreateBoundaryAtSliceStart(t *testing.T) {
	raw := "myapi\napi\n"
	query := compileQuery([]string{"api"}, PartialModeOff)
	spans := []excerptSpan{
		{text: "api"},
		{
			text: "api",
			matches: []excerptMatch{{
				matchRange: matchRange{start: 0, end: 3},
				term:       0,
			}},
		},
	}
	if got, want := computeExcerptSpanLinesQuery(raw, spans, query, 200), []int{0, 2}; !slices.Equal(got, want) {
		t.Fatalf("excerpt line anchors = %v, want %v", got, want)
	}
}

func TestAuthoritativeLineAnchorSkipsSubstringOccurrence(t *testing.T) {
	raw := "myapi\napi\n"
	query := compileQuery([]string{"api"}, PartialModeOff)
	if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}), query, 200); len(got) != 1 || got[0] != 2 {
		t.Fatalf("authoritative line anchor = %v, want [2]", got)
	}
}

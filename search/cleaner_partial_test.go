package search

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func testExcerptSpan(clean string, query compiledQuery, anchorTerm int, text string) excerptSpan {
	var sourceMatches []excerptMatch
	var anchor excerptMatch
	for term, matcher := range query.matchers {
		for _, loc := range matcher.findAll(clean) {
			match := excerptMatch{matchRange: loc, source: loc, term: term}
			sourceMatches = append(sourceMatches, match)
			if term == anchorTerm && anchor.source.end == 0 {
				anchor = match
			}
		}
	}
	return excerptSpan{
		text:          text,
		left:          anchor.source.start,
		right:         anchor.source.end,
		matches:       []excerptMatch{anchor},
		sourceText:    clean,
		sourceMatches: sourceMatches,
	}
}

func TestCleanerPartialExcerptUsesCapturedTokenOffsets(t *testing.T) {
	content := "system _deployment in progress"
	excerpts := ExtractMeaningfulExcerptsPartial(content, []string{"deploy"}, 1, 1, PartialModePrefix)
	if len(excerpts) != 1 {
		t.Fatalf("expected one partial-match excerpt, got %d: %#v", len(excerpts), excerpts)
	}
	if got, want := excerpts[0], "system _deployment in progress"; got != want {
		t.Fatalf("excerpt = %q, want %q", got, want)
	}

	loc := buildTermRegexCI("deploy", PartialModePrefix).FindStringSubmatchIndex(content)
	if len(loc) < 4 || loc[2] != strings.Index(content, "deployment") {
		t.Fatalf("captured token offset = %v, want %d (the d in deployment)", loc, strings.Index(content, "deployment"))
	}
}

func TestInlineWildcardExcerptUsesExactRanges(t *testing.T) {
	tests := []struct {
		name string
		term string
		text string
	}{
		{name: "internal star", term: "ms-*-api", text: "call ms-option-api now"},
		{name: "leading and trailing stars", term: "*OptionApi*", text: "use MyOptionApiClient now"},
		{name: "single byte question", term: "ms-?-api", text: "call ms-x-api now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := compileQuery([]string{tt.term}, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(tt.text, query, 1, 1, 0, true)
			if len(spans) != 1 {
				t.Fatalf("excerpt spans = %#v, want one", spans)
			}
			if !strings.Contains(spans[0].text, tt.text) {
				t.Fatalf("excerpt = %q, want content %q", spans[0].text, tt.text)
			}
			if spans[0].score != len(tt.term) || spans[0].termCount != 1 {
				t.Fatalf("excerpt quality = (%d, %d), want (%d, 1)", spans[0].score, spans[0].termCount, len(tt.term))
			}
		})
	}
}

func TestExcerptMatcherRangeRegressions(t *testing.T) {
	punctuation := compileQuery([]string{"."}, PartialModeOff)
	ranges := punctuation.matchers[0].findAll("..")
	if len(ranges) != 2 || ranges[0] != (matchRange{start: 0, end: 1}) || ranges[1] != (matchRange{start: 1, end: 2}) {
		t.Fatalf("adjacent punctuation ranges = %#v, want [0,1) and [1,2)", ranges)
	}

	invalid := string([]byte{0xff})
	text := invalid + "\uFFFD"
	invalidSpans := extractRankedExcerptSpansQuery(text, compileQuery([]string{invalid}, PartialModeOff), 1, 1, 0, true)
	replacementSpans := extractRankedExcerptSpansQuery(text, compileQuery([]string{"\uFFFD"}, PartialModeOff), 1, 1, 0, true)
	if len(invalidSpans) != 1 || len(replacementSpans) != 1 {
		t.Fatalf("byte-exact excerpts missing: invalid=%#v replacement=%#v", invalidSpans, replacementSpans)
	}
}

func TestInlineWildcardFallbackSentenceUsesCompiledQuery(t *testing.T) {
	clean := "Before. Call ms-option-api now. After."
	query := compileQuery([]string{"ms-*-api"}, PartialModeOff)
	span := testExcerptSpan(clean, query, 0, "Call ms-option-api now.")
	got := expandShortExcerptSpan(span, 100)
	if !strings.Contains(got.text, "Call ms-option-api now.") {
		t.Fatalf("expanded fallback = %q, want wildcard-matching sentence", got.text)
	}
}

func TestMultipartExcerptUsesFullTextRangesAtCandidateEdge(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 240 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	content := "xfoo" + strings.Repeat(" z", 160) + " omega"
	query := compileQuery([]string{"*foo*", "foo", "omega"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, true)
	if len(spans) != 1 {
		t.Fatalf("multipart excerpts = %#v, want one", spans)
	}
	if got := strings.Count(spans[0].text, " ... "); got != 1 {
		t.Fatalf("multipart excerpt = %q, separators = %d, want 1; substring edge must not create a foo match", spans[0].text, got)
	}
}

func TestMultipartExcerptClampsVisibleWildcardMatch(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 200 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	token := "ms-" + strings.Repeat("a", 120) + "-api"
	content := token + strings.Repeat(" z", 120) + " omega"
	query := compileQuery([]string{"ms-*-api", "omega"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, true)
	if len(spans) != 1 {
		t.Fatalf("multipart excerpts = %#v, want one", spans)
	}

	var wildcard *excerptMatch
	for i := range spans[0].matches {
		if spans[0].matches[i].term == 0 {
			wildcard = &spans[0].matches[i]
			break
		}
	}
	if wildcard == nil {
		t.Fatalf("multipart matches = %#v, want visible wildcard metadata", spans[0].matches)
	}
	if wildcard.start != 0 || wildcard.end <= wildcard.start || wildcard.end > len(spans[0].text) {
		t.Fatalf("clamped wildcard range = [%d,%d), excerpt length %d", wildcard.start, wildcard.end, len(spans[0].text))
	}
	if got := highlightExcerptSpan(spans[0]); !strings.HasPrefix(got, "\033[1;31m") {
		t.Fatalf("visible wildcard fragment was not highlighted: %q", got)
	}
}

func TestFinalExcerptCutClampsOnlyVisibleMatches(t *testing.T) {
	matches := []excerptMatch{
		{matchRange: matchRange{start: 2, end: 8}, term: 0},
		{matchRange: matchRange{start: 8, end: 10}, term: 1},
	}

	text, got := truncateExcerpt("abcdefghij", matches, 5)
	if text != "abcde" {
		t.Fatalf("truncated text = %q, want %q", text, "abcde")
	}
	if len(got) != 1 || got[0].term != 0 || got[0].start != 2 || got[0].end != 5 {
		t.Fatalf("truncated metadata = %#v, want visible range [2,5)", got)
	}
}

func TestOrdinaryExcerptRejectsClippedNeighborMatch(t *testing.T) {
	matches := []excerptMatch{{
		matchRange: matchRange{start: 0, end: 6},
		source:     matchRange{start: 0, end: 6},
		term:       0,
	}}

	text, got := formatExcerptText("prefix suffix", 3, len("prefix suffix"), 0, matches)
	if text != "fix suffix" {
		t.Fatalf("excerpt text = %q, want %q", text, "fix suffix")
	}
	if len(got) != 0 {
		t.Fatalf("ordinary excerpt retained clipped neighboring match: %#v", got)
	}
}

func TestShortExcerptExpansionCarriesNeighborMatches(t *testing.T) {
	clean := "Anchor api. Neighbor omega."
	query := compileQuery([]string{"api", "omega"}, PartialModeOff)
	span := testExcerptSpan(clean, query, 0, "Anchor api.")

	expanded := expandShortExcerptSpan(span, 100)
	if got, want := len(expanded.matches), 2; got != want {
		t.Fatalf("expanded matches = %#v, want %d exact matches", expanded.matches, want)
	}
	if got, want := highlightExcerptSpan(expanded), "Anchor \033[1;31mapi\033[0m. Neighbor \033[1;31momega\033[0m."; got != want {
		t.Fatalf("expanded highlight = %q, want %q", got, want)
	}
}

func TestShortExcerptExpansionUsesSourceRangeForRepeatedText(t *testing.T) {
	clean := "Repeat api. Repeat api. Neighbor omega."
	query := compileQuery([]string{"api", "omega"}, PartialModeOff)
	span := testExcerptSpan(clean, query, 0, "Repeat api.")
	secondAPI := span.sourceMatches[1]
	span.left = secondAPI.source.start
	span.right = secondAPI.source.end
	span.matches = []excerptMatch{secondAPI}

	expanded := expandShortExcerptSpan(span, 1)
	if want := strings.Index(clean, "Repeat api.") + len("Repeat api. "); expanded.left != want {
		t.Fatalf("expanded left = %d, want second repeated sentence at %d", expanded.left, want)
	}
}

func TestShortExcerptExpansionRejectsMyAPISubstring(t *testing.T) {
	clean := "myapi context. True api."
	query := compileQuery([]string{"api"}, PartialModeOff)
	span := testExcerptSpan(clean, query, 0, "True api.")

	expanded := expandShortExcerptSpan(span, 100)
	if got, want := highlightExcerptSpan(expanded), "myapi context. True \033[1;31mapi\033[0m."; got != want {
		t.Fatalf("expanded highlight = %q, want %q", got, want)
	}
}

func TestShortExcerptExpansionMapsDividerNormalization(t *testing.T) {
	clean := "Anchor api. ----- Neighbor omega."
	query := compileQuery([]string{"api", "-----", "omega"}, PartialModeOff)
	span := testExcerptSpan(clean, query, 0, "Anchor api.")

	expanded := expandShortExcerptSpan(span, 100)
	if strings.Contains(expanded.text, "-----") {
		t.Fatalf("expanded text retained divider: %q", expanded.text)
	}
	if got := highlightExcerptSpan(expanded); !strings.Contains(got, "\033[1;31momega\033[0m") {
		t.Fatalf("normalized neighbor match lost: %q", got)
	}
	for _, match := range expanded.matches {
		if match.term == 1 {
			t.Fatalf("removed divider retained fabricated match metadata: %#v", expanded.matches)
		}
	}

	notFound := excerptSpan{text: "original", sourceText: "no matches here."}
	if got := expandShortExcerptSpan(notFound, 100).text; got != "original" {
		t.Fatalf("not-found expansion = %q, want original", got)
	}
}

func TestSourceSentenceSpansTrimUTF8OnRuneBoundaries(t *testing.T) {
	tests := []struct {
		name string
		text string
		want matchRange
	}{
		{name: "latin end of text", text: "voilà", want: matchRange{start: 0, end: len("voilà")}},
		{name: "cyrillic and CJK end of text", text: "Привет世界", want: matchRange{start: 0, end: len("Привет世界")}},
		{
			name: "Unicode whitespace",
			text: "\u2003voilà\u3000",
			want: matchRange{start: len("\u2003"), end: len("\u2003voilà")},
		},
		{
			name: "invalid UTF-8 is preserved",
			text: string([]byte{0xff, ' '}),
			want: matchRange{start: 0, end: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceSentenceSpans(tt.text)
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("sourceSentenceSpans(%q) = %#v, want [%#v]", tt.text, got, tt.want)
			}
			if !utf8.ValidString(tt.text) {
				return
			}
			if !utf8.ValidString(tt.text[got[0].start:got[0].end]) {
				t.Fatalf("span split valid UTF-8: %#v", got[0])
			}
		})
	}
}

func TestTruncateExcerptUsesUTF8Boundaries(t *testing.T) {
	text := "abcédef"
	matches := []excerptMatch{
		{matchRange: matchRange{start: 0, end: 3}, term: 0},
		{matchRange: matchRange{start: 3, end: 5}, term: 1},
	}

	got, gotMatches := truncateExcerpt(text, matches, 4)
	if got != "abc" || !utf8.ValidString(got) {
		t.Fatalf("UTF-8 truncation = %q, want valid %q", got, "abc")
	}
	if len(gotMatches) != 1 || gotMatches[0].term != 0 {
		t.Fatalf("truncated metadata = %#v, want only retained match", gotMatches)
	}

	invalid := string([]byte{'a', 0xff, 'b'})
	got, _ = truncateExcerpt(invalid, nil, 2)
	if !bytes.Equal([]byte(got), []byte{'a', 0xff}) {
		t.Fatalf("invalid-byte truncation = %v, want [97 255]", []byte(got))
	}
}

func TestNormalizeExcerptBoundsUsesUTF8Boundaries(t *testing.T) {
	source := "aéz"
	if left, right := normalizeExcerptBounds(source, 2, len(source)); left != 3 || right != len(source) {
		t.Fatalf("left bounds = [%d,%d), want [3,%d)", left, right, len(source))
	}
	if left, right := normalizeExcerptBounds(source, 0, 2); left != 0 || right != 1 {
		t.Fatalf("right bounds = [%d,%d), want [0,1)", left, right)
	}
}

func TestExcerptInitialBudgetPreservesUTF8(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 200 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	content := "api " + strings.Repeat("a", 195) + "érest"
	query := compileQuery([]string{"api"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 1, 1, 0, true)
	if len(spans) != 1 {
		t.Fatalf("excerpt spans = %#v, want one", spans)
	}
	if len(spans[0].text) > 200 || !utf8.ValidString(spans[0].text) {
		t.Fatalf("budgeted excerpt length/UTF-8 = %d/%v", len(spans[0].text), utf8.ValidString(spans[0].text))
	}
}

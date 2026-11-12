package search

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestMultipartExcerptDocumentOrderAndMergedWindows(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	for _, adjacent := range []bool{false, true} {
		gap := " " + strings.Repeat("filler ", 100)
		if adjacent {
			gap = " shared-context "
		}
		raw := strings.Repeat("prefix ", 30) + "alpha" + gap + "beta " + strings.Repeat("filler ", 100) + "gamma " + strings.Repeat("suffix ", 30)
		spans := extractRankedExcerptSpans(raw, []string{"gamma", "beta", "alpha"}, 1, 3, 0, false, PartialModeOff)
		if len(spans) != 1 {
			t.Fatalf("adjacent=%v: got %d spans", adjacent, len(spans))
		}
		text := spans[0].text
		previous := -1
		for _, term := range []string{"alpha", "beta", "gamma"} {
			pos := strings.Index(text, term)
			if pos <= previous || strings.Count(text, term) != 1 {
				t.Fatalf("adjacent=%v: term %q duplicated, missing, or out of order: %q", adjacent, term, text)
			}
			previous = pos
		}
		if adjacent && strings.Count(text, "shared-context") != 1 {
			t.Fatalf("overlapping context duplicated: %q", text)
		}
		for _, match := range spans[0].matches {
			if text[match.start:match.end] != raw[match.source.start:match.source.end] {
				t.Fatalf("incorrect shifted match: %+v in %q", match, text)
			}
		}
	}
}

func TestMultipartExcerptTruncationPreservesRawAnchor(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	terms := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"}
	raw := "> alpha beta gamma delta epsilon zeta eta theta\nheader\n" + strings.Repeat("padding ", 10)
	anchor := len(raw)
	raw += strings.Join(terms, " "+strings.Repeat("filler ", 100)) + " " + strings.Repeat("tail ", 30)
	query := compileQuery(terms, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(raw, query, 1, len(terms), 0, false, unconstrainedDistance)
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	span := &spans[0]
	if len(span.text) != 400 || len(span.matches) >= len(terms) || strings.Contains(span.text, "theta") {
		t.Fatalf("fixture must truncate later matches: text=%q matches=%+v", span.text, span.matches)
	}
	if !span.rawStartValid || span.rawStart != anchor || len(span.sourceMatches) != len(terms) {
		t.Fatalf("lost seed identity: %+v", span)
	}
	if lines := computeExcerptSpanLinesQuery(raw, spans, query, 5000); lines[0] != 3 {
		t.Fatalf("multipart anchor line=%v, want 3", lines)
	}
	span.text, span.matches = truncateExcerpt(span.text, span.matches, 1)
	if len(span.matches) != 0 || span.rawStart != anchor || !span.rawStartValid {
		t.Fatalf("direct truncation lost seed identity: %+v", span)
	}
	if lines := computeExcerptSpanLinesQuery(raw, spans, query, 5000); lines[0] != 3 {
		t.Fatalf("anchor without displayed matches=%v, want 3", lines)
	}
}

func TestMultipartExcerptQueryOrderInvariance(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	raw := "> alpha beta gamma hidden\nheader\nalpha " + strings.Repeat("filler ", 100) +
		"beta " + strings.Repeat("filler ", 100) + "gamma end."
	var reference string
	for _, terms := range [][]string{
		{"alpha", "beta", "gamma"}, {"alpha", "gamma", "beta"},
		{"beta", "alpha", "gamma"}, {"beta", "gamma", "alpha"},
		{"gamma", "alpha", "beta"}, {"gamma", "beta", "alpha"},
	} {
		t.Run(strings.Join(terms, "/"), func(t *testing.T) {
			query := compileQuery(terms, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(raw, query, 1, 3, 0, false, unconstrainedDistance)
			if len(spans) != 1 || !strings.Contains(spans[0].text, " ... ") {
				t.Fatalf("expected one multipart excerpt: %#v", spans)
			}
			span := spans[0]
			if reference == "" {
				reference = span.text
			}
			if span.text != reference {
				t.Fatalf("query order changed excerpt: %q, want %q", span.text, reference)
			}
			previous := -1
			for _, term := range []string{"alpha", "beta", "gamma"} {
				pos := strings.Index(span.text, term)
				if pos <= previous || strings.Count(span.text, term) != 1 {
					t.Fatalf("term %q missing, duplicated, or out of order: %q", term, span.text)
				}
				previous = pos
			}
			if len(span.matches) != 3 {
				t.Fatalf("expected three displayed matches: %+v", span.matches)
			}
			for _, match := range span.matches {
				if span.text[match.start:match.end] != terms[match.term] {
					t.Fatalf("query term identity lost: %+v", match)
				}
			}
			if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); len(got) != 1 || got[0] != 3 {
				t.Fatalf("anchors = %v, want [3]", got)
			}
		})
	}
}

func TestExtractMeaningfulExcerpts_ReturnsMultipleNonOverlappingMatches(t *testing.T) {
	content := strings.Join([]string{
		"alpha provider access_token one.",
		strings.Repeat("filler ", 120),
		"second provider access_token two.",
		strings.Repeat("filler ", 120),
		"third provider access_token three.",
	}, " ")

	excerpts := ExtractMeaningfulExcerpts(content, []string{"provider", "access_token"}, 3, 2)
	if len(excerpts) != 3 {
		t.Fatalf("expected 3 excerpts, got %d: %#v", len(excerpts), excerpts)
	}

	for i, excerpt := range excerpts {
		if !strings.Contains(excerpt, "provider") || !strings.Contains(excerpt, "access_token") {
			t.Fatalf("excerpt %d should contain all terms, got %q", i+1, excerpt)
		}
	}
	if excerpts[0] == excerpts[1] || excerpts[1] == excerpts[2] || excerpts[0] == excerpts[2] {
		t.Fatalf("expected distinct non-overlapping excerpts, got %#v", excerpts)
	}
}

func TestExtractMeaningfulExcerpts_UsesTargetScoreForPartialMatchCluster(t *testing.T) {
	content := "alpha beta gamma form one cohesive ranked cluster. " +
		strings.Repeat("unrelated filler ", 80)

	excerpts := ExtractMeaningfulExcerpts(content, []string{"alpha", "beta", "gamma", "delta"}, 1, 3)
	if len(excerpts) != 1 {
		t.Fatalf("expected one excerpt for the partial-match cluster, got %d: %#v", len(excerpts), excerpts)
	}

	for _, term := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(excerpts[0], term) {
			t.Fatalf("excerpt should contain ranked cluster term %q, got %q", term, excerpts[0])
		}
	}
}

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
			spans := extractRankedExcerptSpansQuery(tt.text, query, 1, 1, 0, true, unconstrainedDistance)
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
	invalidSpans := extractRankedExcerptSpansQuery(text, compileQuery([]string{invalid}, PartialModeOff), 1, 1, 0, true, unconstrainedDistance)
	replacementSpans := extractRankedExcerptSpansQuery(text, compileQuery([]string{"\uFFFD"}, PartialModeOff), 1, 1, 0, true, unconstrainedDistance)
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
	spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, true, unconstrainedDistance)
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
	spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, true, unconstrainedDistance)
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

func TestShortExcerptExpansionPreservesMatchesAndBounds(t *testing.T) {
	tests := []struct {
		name, clean, want string
		terms             []string
		target            int
		all, bounded      bool
	}{
		{name: "all visible matches", clean: "Long preceding sentence. First api. Bridge. Last omega.", terms: []string{"api", "omega"}, target: 30, all: true, want: "First \033[1;31mapi\033[0m. Bridge. Last \033[1;31momega\033[0m."},
		{name: "dot-leading match", clean: "Before. Use .NET now. After.", terms: []string{".NET"}, target: len("Use .NET now."), want: "Use \033[1;31m.NET\033[0m now."},
		{name: "distant sentences", clean: "First api. " + strings.Repeat("Middle sentence. ", 20) + "Last omega.", terms: []string{"api", "omega"}, target: 100, all: true, bounded: true},
		{name: "single merged sentence", clean: "Use .NET " + strings.Repeat("context ", 20) + "end.", terms: []string{".NET"}, target: 100, bounded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := testExcerptSpan(tt.clean, compileQuery(tt.terms, PartialModeOff), 0, strings.Join(tt.terms, " ... "))
			if tt.all {
				span.matches = append([]excerptMatch(nil), span.sourceMatches...)
			}
			expanded := expandShortExcerptSpan(span, tt.target)
			if tt.bounded && !reflect.DeepEqual(expanded, span) {
				t.Fatalf("oversized covering range replaced bounded excerpt: %#v", expanded)
			}
			if !tt.bounded && highlightExcerptSpan(expanded) != tt.want {
				t.Fatalf("expanded excerpt = %q, want %q", highlightExcerptSpan(expanded), tt.want)
			}
		})
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

	expanded := expandShortExcerptSpan(span, len("Repeat api."))
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
			got := sourceSentenceSpans(tt.text, nil)
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

func TestSourceSentenceSpansMatchedBoundaries(t *testing.T) {
	match := func(text, term string) excerptMatch {
		start := strings.Index(text, term)
		loc := matchRange{start: start, end: start + len(term)}
		return excerptMatch{matchRange: loc, source: loc}
	}
	paragraph, overlap := "Use alpha\n\nBeta now. After.", "Use aa.BB!CC now. Next."
	short := matchRange{start: strings.Index(overlap, ".BB"), end: strings.Index(overlap, "!CC") + 1}
	tests := []struct {
		name, text string
		matches    []excerptMatch
		want       []string
	}{
		{name: "normal punctuation", text: "First.Second!Third?Fourth", want: []string{"First.", "Second!", "Third?", "Fourth"}},
		{name: "matched paragraph", text: paragraph, matches: []excerptMatch{match(paragraph, "alpha\n\nBeta")}, want: []string{"Use alpha\n\nBeta now.", "After."}},
		{name: "unsorted overlapping matches", text: overlap, matches: []excerptMatch{match(overlap, "Next"), {matchRange: short, source: short}, match(overlap, "aa.BB!CC")}, want: []string{"Use aa.BB!CC now.", "Next."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges := sourceSentenceSpans(tt.text, tt.matches)
			got := make([]string, len(ranges))
			for i, span := range ranges {
				got[i] = tt.text[span.start:span.end]
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("sentence spans = %#v, want %#v", got, tt.want)
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
	spans := extractRankedExcerptSpansQuery(content, query, 1, 1, 0, true, unconstrainedDistance)
	if len(spans) != 1 {
		t.Fatalf("excerpt spans = %#v, want one", spans)
	}
	if len(spans[0].text) > 200 || !utf8.ValidString(spans[0].text) {
		t.Fatalf("budgeted excerpt length/UTF-8 = %d/%v", len(spans[0].text), utf8.ValidString(spans[0].text))
	}
}

// perFileSubsetCorpus has two separated clusters of unequal quality:
// the first holds three distinct terms (higher quality), the second only
// two (lower quality). Per-file subset selection must emit both clusters,
// best-first, instead of collapsing the file to its single best window.
func perFileSubsetCorpus() string {
	return strings.Join([]string{
		"First cluster begins. alpha beta gamma first cluster text. First cluster ends.",
		skipAheadPadding("between-first-second", 90),
		"Second cluster begins. alpha beta second cluster text. Second cluster ends.",
	}, " ")
}

func TestRankedExcerptSpansEmitBothQualityLevelsBestFirst(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := perFileSubsetCorpus()
	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 3, 3, 2, false, PartialModeOff)

	if len(spans) != 2 {
		t.Fatalf("span count = %d, want 2 (both quality levels)", len(spans))
	}
	if spans[0].termCount != 3 || spans[1].termCount != 2 {
		t.Fatalf("span quality order = (%d, %d) terms, want best-first (3, 2)", spans[0].termCount, spans[1].termCount)
	}
	if !strings.Contains(spans[0].text, "gamma") {
		t.Fatalf("first span should cover the three-term cluster: %q", spans[0].text)
	}
	if strings.Contains(spans[1].text, "gamma") {
		t.Fatalf("second span should be the two-term cluster: %q", spans[1].text)
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansDenseLowerQualityNeighborhoodCollapses(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := strings.Join([]string{
		"opening context", strings.Repeat("z", 400),
		"alpha beta gamma dense marker",
		strings.Repeat("a", 130),
		"alpha beta dense marker",
		strings.Repeat("b", 130),
		"alpha beta dense marker",
		strings.Repeat("c", 130),
		"closing context",
	}, " ")

	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 3, 3, 2, false, PartialModeOff)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 (dense neighborhood collapses to the best chunk)", len(spans))
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansRespectDistance(t *testing.T) {
	content := "alpha beta " + strings.Repeat("padding ", 150) + "gamma"
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	const distance = 20
	analysis, ok := analyzeRankedText(content, query, distance, false)
	if !ok || analysis.TermCount != 2 {
		t.Fatalf("Stage 2 = %#v, %v; want two-term match", analysis, ok)
	}
	// Also use a higher target to prove distance rejection independently of the cap.
	for _, target := range []int{analysis.TermCount, 3} {
		for _, isCode := range []bool{false, true} {
			spans := extractRankedExcerptSpansQuery(content, query, 3, target, 2, isCode, distance)
			if len(spans) != 1 {
				t.Fatalf("target=%d code=%v: got %d spans, want 1", target, isCode, len(spans))
			}
			if spans[0].termCount != 2 || spans[0].score != analysis.Score || strings.Contains(spans[0].text, "gamma") {
				t.Fatalf("target=%d code=%v: stitched distant term or wrong quality: %#v", target, isCode, spans[0])
			}
		}
	}
}

func TestRankedExcerptSpansCapCoverageAtTarget(t *testing.T) {
	content := "alpha beta gamma"
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, false, len(content))
	if len(spans) != 1 || spans[0].termCount != 2 || spans[0].score != len("alpha")+len("beta") {
		t.Fatalf("spans = %#v, want one two-term candidate capped at target", spans)
	}
}

func TestRankedExcerptSpansRequirePrimaryTerm(t *testing.T) {
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	for _, content := range []string{
		"beta gamma",
		"alpha " + strings.Repeat("padding ", 150) + "beta gamma",
	} {
		spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, false, 20)
		if len(spans) != 0 {
			t.Fatalf("spans = %#v, want no relaxed candidates without nearby primary term", spans)
		}
	}
}

func TestRankedExcerptSpansDistanceBoundary(t *testing.T) {
	const content = "alpha beta"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	for _, distance := range []int{0, 5, 6} {
		_, qualifies := analyzeRankedText(content, query, distance, true)
		spans := extractRankedExcerptSpansQuery(content, query, 1, 2, 0, false, distance)
		if (len(spans) == 1) != qualifies {
			t.Fatalf("distance=%d: got %d spans, Stage 2 qualifies=%v", distance, len(spans), qualifies)
		}
	}
}

// A wide three-term candidate from either early cluster to the sole gamma
// overlaps less than 10% of its length with the final cluster. It must not
// reuse that endpoint and reserve the intervening two-term clusters.
func endpointNoveltyCorpus() string {
	return "alpha beta first cluster\n" + strings.Repeat("padding ", 500) +
		"\nalpha beta middle cluster\n" + strings.Repeat("padding ", 500) +
		"\nalpha beta gamma final cluster"
}

func TestRankedExcerptSpansRejectReusedRightEndpoint(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	content := endpointNoveltyCorpus()
	query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)
	for _, isCode := range []bool{false, true} {
		spans := extractRankedExcerptSpansQuery(content, query, 3, 3, 2, isCode, len(content))
		if len(spans) != 3 {
			t.Fatalf("code=%v: got %d spans, want all three clusters", isCode, len(spans))
		}
		for i, marker := range []string{"final cluster", "first cluster", "middle cluster"} {
			wantTerms := 2
			if i == 0 {
				wantTerms = 3
			}
			if spans[i].termCount != wantTerms || !strings.Contains(spans[i].text, marker) {
				t.Fatalf("code=%v span %d = %#v, want %s with %d terms", isCode, i, spans[i], marker, wantTerms)
			}
			if i > 0 && strings.Contains(spans[i].text, "gamma") {
				t.Fatalf("code=%v span %d reuses sole gamma: %q", isCode, i, spans[i].text)
			}
		}
		requireBoundedNovelSpans(t, content, spans, "alpha")
	}
}

func TestRankedExcerptSpansAllowContextOnlyOverlap(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })
	content := strings.Repeat("x ", 200) + "alpha beta first" + strings.Repeat(" x", 182) + " alpha beta other" + strings.Repeat(" x", 200)
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(content, query, 3, 2, 2, true, 20)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want two distinct clusters", len(spans))
	}
	if overlap := spanOverlap(spans[0], spans[1]); overlap <= 0 {
		t.Fatal("fixture must exercise positive context overlap")
	}
	requireBoundedNovelSpans(t, content, spans, "alpha")
}

func TestRankedExcerptSpansRespectMaxExcerpts(t *testing.T) {
	SetExcerptContextLimit(100)
	t.Cleanup(func() { SetExcerptContextLimit(0) })

	content := perFileSubsetCorpus()
	spans := extractRankedExcerptSpans(content, []string{"alpha", "beta", "gamma"}, 1, 3, 2, false, PartialModeOff)
	if len(spans) != 1 {
		t.Fatalf("span count = %d, want 1 for maxExcerpts = 1", len(spans))
	}
	if spans[0].termCount != 3 {
		t.Fatalf("single span should be the best cluster, got %d terms", spans[0].termCount)
	}
}

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

func spanTexts(spans []excerptSpan) []string {
	texts := make([]string, len(spans))
	for i, span := range spans {
		texts[i] = span.text
	}
	return texts
}

func spanOverlap(a, b excerptSpan) int {
	left := max(a.left, b.left)
	right := min(a.right, b.right)
	return max(0, right-left)
}

func TestExtractExcerptSpansWrapperParity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		code  bool
		terms []string
	}{
		{
			name:  "document",
			input: "The deployment config contains the production endpoint. Another deployment config is documented here.",
			terms: []string{"deployment", "config"},
		},
		{
			name:  "code",
			input: "func deployConfig() { return config.Deployment } // deploy config",
			code:  true,
			terms: []string{"deploy", "config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans := extractExcerptSpans(tt.input, tt.terms, 3, tt.code)
			var got []string
			if tt.code {
				got = ExtractMeaningfulExcerptsCode(tt.input, tt.terms, 3, len(tt.terms))
			} else {
				got = ExtractMeaningfulExcerpts(tt.input, tt.terms, 3, len(tt.terms))
			}
			if !reflect.DeepEqual(got, spanTexts(spans)) {
				t.Fatalf("wrapper output = %#v, span texts = %#v", got, spanTexts(spans))
			}
		})
	}
}

func TestExtractExcerptSpansBoundsDenseOverlap(t *testing.T) {
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
	for i, span := range spans {
		if span.left < 0 || span.right <= span.left || span.right > len(content) {
			t.Fatalf("span %d has invalid range [%d, %d) for content length %d", i, span.left, span.right, len(content))
		}
		if !strings.Contains(span.text, "needle") {
			t.Fatalf("span %d does not contain the search term: %q", i, span.text)
		}
		matchOffset := strings.Index(content, "needle")
		if matchOffset < span.left || matchOffset >= span.right {
			t.Fatalf("span %d [%d, %d) does not cover needle at %d", i, span.left, span.right, matchOffset)
		}
	}
	requireBoundedNovelSpans(t, content, spans, "needle")
}

func TestExpandToBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		cleaned    string
		match      string
		maxContext int
		want       string
	}{
		{
			name:       "sentence punctuation",
			cleaned:    "Before sentence. Match stays here! After sentence.",
			match:      "Match",
			maxContext: 100,
			want:       " Match stays here!",
		},
		{
			name:       "email header",
			cleaned:    "Intro sentence.\nFrom: sender\nTarget body ends.\nSubject: later header",
			match:      "Target",
			maxContext: 100,
			want:       "\nFrom: sender\nTarget body ends.",
		},
		{
			name:       "paragraph fallback",
			cleaned:    "prefix words\n\nparagraph target words\n\nsuffix words",
			match:      "target",
			maxContext: 100,
			want:       "paragraph target words",
		},
		{
			name:       "single newline fallback",
			cleaned:    "prefix words\nline target words\nsuffix words",
			match:      "target",
			maxContext: 100,
			want:       "line target words",
		},
		{
			name:       "scan clamp",
			cleaned:    strings.Repeat("a", 30) + " target " + strings.Repeat("b", 30),
			match:      "target",
			maxContext: 20,
			want:       strings.Repeat("a", 9) + " target " + strings.Repeat("b", 9),
		},
		{
			name:       "string boundaries",
			cleaned:    "target ends.",
			match:      "target",
			maxContext: 100,
			want:       "target ends.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := strings.Index(tt.cleaned, tt.match)
			if left < 0 {
				t.Fatalf("match %q missing from corpus", tt.match)
			}
			right := left + len(tt.match)

			gotLeft, gotRight := expandToBoundaries(tt.cleaned, left, right, tt.maxContext)
			if gotLeft < 0 || gotRight < gotLeft || gotRight > len(tt.cleaned) {
				t.Fatalf("bounds = [%d, %d), content length = %d", gotLeft, gotRight, len(tt.cleaned))
			}
			if got := tt.cleaned[gotLeft:gotRight]; got != tt.want {
				t.Fatalf("expanded text = %q, want %q", got, tt.want)
			}
		})
	}
}

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

func TestNormalizeMappedWhitespacePreservesOffsets(t *testing.T) {
	// Cover every byte (including invalid UTF-8), all C1 control runes, and
	// multibyte text/whitespace. Compare against the former two-pass mapping.
	var all strings.Builder
	for i := 0; i < 256; i++ {
		all.WriteByte(byte(i))
		all.WriteString("x \t")
	}
	for r := rune(0x7f); r <= 0x9f; r++ {
		all.WriteRune(r)
		all.WriteString("\n \x00")
	}
	for _, raw := range []string{"", "plain", " \x00\t\r\n\u0085 ", "\u00a0é\u2003\t\u009f界\xff\x80", all.String()} {
		m := mappedContent{text: raw, offsets: make([]int, len(raw))}
		for i := range m.offsets {
			m.offsets[i] = 7 + 3*i
		}
		want := m.replace(controlCharRegex, " ").replace(whitespaceRegex, " ")
		got := normalizeMappedWhitespace(m)
		if got.text != want.text || !slices.Equal(got.offsets, want.offsets) {
			t.Fatalf("raw=%q: got=%+v want=%+v", raw, got, want)
		}
		textOnly := normalizeMappedWhitespace(mappedContent{text: raw})
		if textOnly.text != want.text || textOnly.offsets != nil {
			t.Fatalf("text-only normalization differs or allocated offsets: %+v", textOnly)
		}
	}
}

func TestEngineCleaningConsistency(t *testing.T) {
	const raw = "header\n> alpha beta gamma hidden\nalpha beta visible\n"
	for _, kind := range []string{"txt", "go", "pdf", "readerAt"} {
		for _, terms := range [][]string{{"alpha", "beta", "gamma"}, {"gamma"}} {
			for _, strict := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/strict=%v", kind, strings.Join(terms, "-"), strict), func(t *testing.T) {
					ext := kind
					if kind == "readerAt" {
						ext = "pdf"
					}
					path := filepath.Join(t.TempDir(), "fixture."+ext)
					if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
					engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
					engine.Silent = true
					engine.Strict = strict
					if kind == "pdf" {
						engine.Registry.extractors["pdf"] = rankingStaticExtractor{text: raw}
					}
					if kind == "readerAt" {
						extractor := &rankingReaderAtExtractor{}
						extractor.text = raw
						engine.Registry.extractors["pdf"] = extractor
					}
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					wantTerms := terms
					if kind != "go" {
						if strict || len(terms) == 1 {
							wantTerms = nil
						} else {
							wantTerms = []string{"alpha", "beta"}
						}
					}
					if len(wantTerms) == 0 {
						if len(matches) != 0 {
							t.Fatalf("hidden-only match survived Stage 2: %+v", matches)
						}
						return
					}
					if len(matches) != 1 {
						t.Fatalf("matches = %+v, want one", matches)
					}
					score := 0
					for _, term := range wantTerms {
						score += len(term)
					}
					if matches[0].score != score || matches[0].termCount != len(wantTerms) || !slices.Equal(matches[0].matchedTerms, wantTerms) {
						t.Fatalf("Stage 2 metadata includes stripped terms: %+v; want %v", matches[0], wantTerms)
					}
					results, err := engine.ExtractAndBuildResults(matches)
					if err != nil || len(results) != 1 {
						t.Fatalf("results=%+v err=%v", results, err)
					}
					result := results[0]
					if result.Score != score || result.TermCount != len(wantTerms) || !slices.Equal(result.MatchedTerms, wantTerms) || result.ChunkScores[0] != score || result.ChunkTermCounts[0] != len(wantTerms) {
						t.Fatalf("Stage 2/3 metadata disagree: %+v", result)
					}
					if kind != "go" && (strings.Contains(result.CleanContent, "gamma") || strings.Contains(strings.Join(result.RawExcerpts, " "), "hidden")) {
						t.Fatalf("hidden content survived: %+v", result)
					}
					if kind == "txt" && !slices.Equal(result.StartLines, []int{3}) {
						t.Fatalf("lines=%v, want [3]", result.StartLines)
					}
				})
			}
		}
	}
}

func TestFileAllWordsCleaningConsistency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte("header\n> alpha beta gamma hidden\nalpha beta visible\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, terms := range [][]string{{"alpha", "beta"}, {"alpha", "gamma"}, {"gamma"}} {
		got, err := CheckFileContainsAllWords(path, terms, 1000, true)
		want := slices.Equal(terms, []string{"alpha", "beta"})
		if err != nil || got != want {
			t.Fatalf("terms=%v: got %v, %v; want %v", terms, got, err, want)
		}
	}
}

func TestCleanContentKeepsTokensAroundStrippedBlocks(t *testing.T) {
	for _, tag := range []string{"style", "script"} {
		t.Run(tag, func(t *testing.T) {
			if got := CleanContent("foo<" + tag + ">x</" + tag + ">bar"); got != "foo bar" {
				t.Fatalf("got %q, want foo bar", got)
			}
		})
	}
}

func TestCleanContentStripsOnlyTagSyntax(t *testing.T) {
	// Plain text must not reserve per-byte endpoint storage, at either size.
	for _, size := range []int{1 << 20, 5 << 20} {
		raw := strings.Repeat("x", size)
		if ends := markupTagEnds(raw); cap(ends) != 0 {
			t.Fatalf("%d plain bytes reserved %d endpoints", size, cap(ends))
		}
		allocs := testing.AllocsPerRun(3, func() {
			if len(markupDeclarationSpans(raw)) != 0 || stripDelimitedMarkup(mappedContent{text: raw}, " ", htmlTagRegex).text != raw {
				t.Fatal("plain text changed")
			}
		})
		if allocs > 1 {
			t.Fatalf("%d plain bytes: %g markup allocations", size, allocs)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{`<?audit secret < 80% ?>alpha`, "alpha"},
		{`<?audit secret </x ?>alpha`, "alpha"},
		{`<?audit secret?>alpha`, "alpha"},
		{"<?audit secret <\n80% ?>alpha", "<?audit secret < 80% ? alpha"},
		{"<?open\n<?audit secret?>alpha", "<?open alpha"},
		{`<?audit secret`, "<?audit secret"},
		{`<?audit secret>alpha`, "alpha"},
		{`<?a>one<?b>two<?c?>three`, "one two three"},
		{`<p>Hi<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" /><o:p></o:p></p><p>budget visible</p><?php echo 1 ?>tail`, "Hi budget visible tail"},
		{`<?audit secret>alpha<b>x</b>?>tail`, "alpha x ? tail"},
		{`<?audit > < 80% ?>alpha`, "alpha"},
		{`<?audit < nested ?>alpha`, "alpha"},
		{`<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" />budget`, "budget"},
		{`<?no closer at all`, "<?no closer at all"},
		{`<?php if ($a > 1) ?>alpha`, "alpha"},
		{`<?audit < nested>alpha`, "alpha"},
		{"<?audit secret\nvalue>alpha", "<?audit secret value alpha"},
		{"<?audit secret>alpha\n?>tail", "alpha ? tail"},
		{"<?open\r<?audit secret>alpha", "<?open alpha"},
		{`<?first>alpha <?second>tail <?unclosed`, "alpha tail <?unclosed"},
		{`<img src="secret" alt="load </b> value">alpha`, "alpha"},
		{`<img src='secret' alt='load </b> value'>alpha`, "alpha"},
		{`<title>a <!-- b</title>alpha<!-- x -->tail`, "a <!-- b alpha tail"},
		{`<textarea>a <!-- b</textarea>alpha<!-- x -->tail`, "a <!-- b alpha tail"},
		{`<title>heading</title>alpha`, "heading alpha"},
		{`<script>secret</script foo>alpha`, "alpha"},
		{`<script>secret</script >alpha`, "alpha"},
		{`<script>secret</script/>alpha`, "alpha"},
		{`<style>secret</style foo>alpha`, "alpha"},
		{`<script/>alpha<script>secret</script>tail`, "alpha tail"},
		{`<style type="text/css"/>alpha<style>secret</style>tail`, "alpha tail"},
		{"If <80% memory is free\nsession state stays warm\nuntil >90% utilization.", "If <80% memory is free session state stays warm until 90% utilization."},
		{"client <-- proxy\nsession memory\nserver --> sink", "client <-- proxy session memory server -- sink"},
		{"a < b and c > d", "a < b and c d"},
		{"x <= 5 and y >= 3", "x <= 5 and y = 3"},
		{"keep <80% this <b>alpha</b>", "keep <80% this alpha"},
		{"<p class=\"lead\">alpha</p>", "alpha"},
		{"<img src=\"a.png\" alt=\"b\">alpha", "alpha"},
		{"cat <<EOF\nsession memory warm\nEOF\necho done > log", "cat <<EOF session memory warm EOF echo done log"},
		{"<What an attacker gains.\nBe specific.> alpha", "<What an attacker gains. Be specific. alpha"},
		{"<img src=\"a.png\"\n  alt=\"b\">alpha", "<img src=\"a.png\" alt=\"b\" alpha"},
		{"<!-- note\nspans lines -->alpha", "alpha"},
		{"<?xml version=\"1.0\"?><!DOCTYPE html>alpha", "alpha"},
		{"</div>alpha<br/>", "alpha"},
		{`<p class="x">hi</p>`, "hi"},
		{`<img src="secret" alt="load <80%">alpha`, "alpha"},
		{`<img src='secret' alt='load <80% >20% "quoted"'>alpha`, "alpha"},
		{"<!-- secret < 80%\ninternal note -->alpha", "alpha"},
		{"<!-- first > note -->alpha<!-- second < note -->beta", "alpha beta"},
		{"<![CDATA[secret < 80%\ninternal > note]]>alpha", "alpha"},
		{`<!DOCTYPE html SYSTEM "secret<file>">alpha`, "alpha"},
		{"<!DOCTYPE root [\n<!ENTITY note 'secret < 80% >20%'>\n]>alpha", "alpha"},
		{`<!DOCTYPE root [<!ENTITY note "[ ] ' > <">]>alpha`, "alpha"},
		{`<!DOCTYPE secret [<!-- don't -->]><secret>alpha</secret>`, "alpha"},
		{`<!DOCTYPE secret [<!-- " ] > < -->]><secret>alpha</secret>`, "alpha"},
		{"<script>const s = \"<!--\";</script>\nalpha\n<!-- footer -->tail", "alpha tail"},
		{"<style>a::after {content: \"<!--\"}</style>\nalpha\n<!-- footer -->tail", "alpha tail"},
		{`<SCRIPT title="a>b">"<![CDATA["</SCRIPT>alpha<![CDATA[footer]]>tail`, "alpha tail"},
		{`<!-- <script> -->alpha<script>hidden</script>tail`, "alpha tail"},
		{`<!-- <a x=" --> <script>hidden <b> "> hidden</script>alpha`, "alpha"},
		{`<!DOCTYPE secret [<!--> ' -->]><secret>alpha</secret>`, "alpha"},
		{`<img alt="<!--">alpha<!-- footer -->tail`, "alpha tail"},
		{`<img alt='<![CDATA['>alpha<![CDATA[footer]]>tail`, "alpha tail"},
		{`<script>hidden</script>alpha<style>hidden</style>tail`, "alpha tail"},
		{`<!X "[" ['['] ']' [" ] "]>alpha`, "alpha"},
		{"<!a [\n<b>alpha</b>\nbeta", "<!a [ alpha beta"},
		{"<!DOCTYPE x [\n<b>alpha</b>\nbeta", "<!DOCTYPE x [ alpha beta"},
		{"<!a [\n<!ENTITY note 'hidden'>\n<b>alpha</b>", "<!a [ alpha"},
		{"<b says \"hi>\nalpha\nends here >", "<b says \"hi alpha ends here"},
		{"<b says 'hi>\nalpha\nends here >", "<b says 'hi alpha ends here"},
		{"<b says \"hi>\nalpha\n\">tail", "<b says \"hi alpha \" tail"},
		{"<b says 'hi>\ralpha\r'>tail", "<b says 'hi alpha ' tail"},
		{"<!-- note >\n<b>alpha</b>\nbeta", "<!-- note alpha beta"},
		{"<![CDATA[\n<b>alpha</b>\nbeta", "<![CDATA[ alpha beta"},
		{"<!-- open\n<![CDATA[hidden]]><b>alpha</b>", "<!-- open alpha"},
		{"<![CDATA[open\n<!-- hidden --><b>alpha</b>", "<![CDATA[open alpha"},
		{`<a title="a<b">text</a>`, "text"},
		{`<div data-x="a>b">text</div>`, "text"},
		{`<p class="x>alpha</p>`, `<p class="x alpha`},
	} {
		if got := CleanContent(tc.raw); got != tc.want {
			t.Errorf("CleanContent(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if got := cleanStage3Content(tc.raw, false).text; got != tc.want {
			t.Errorf("cleanStage3Content(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		for _, match := range textTagRegex.FindAllString(tc.raw, -1) {
			if end := markupTagEnds(match)[0]; end != len(match) {
				t.Errorf("text tag %q is not a complete HTML tag match: end=%d", match, end)
			}
		}
	}
}

func TestUnterminatedMarkupScaling(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"instruction", "<?audit secret \n", "<?audit secret "},
		{"instruction_flood", "<? <? ", "<? <? "},
		{"instruction_angles", "<?audit < </x ", "<?audit < </x "},
		{"instruction_complete", "<?audit < </x ?>alpha ", "alpha "},
		{"instruction_fallback", "<?audit < nested>alpha ", "alpha "},
		{"instruction_bounded", "<?a>one<?b>two<?c?>three ", "one two three "},
		{"closing_attribute", "<img alt=\"</b></b>\">alpha ", "alpha "},
		{"closing_attribute_single", "<img alt='</b></b>'>alpha ", "alpha "},
		{"title", "<title><b>bold</b>\n", "bold "},
		{"comment", "<!-- note <b>bold</b>\n", "<!-- note bold "},
		{"cdata", "<![CDATA[<b>\n", "<![CDATA[ "},
		{"subset", "<!a [ <b>bold</b>\n", "<!a [ bold "},
		{"doctype_subset", "<!DOCTYPE x [ <b>bold</b>\n", "<!DOCTYPE x [ bold "},
		{"double_quote", "<!a \"<b>bold</b>\n", "<!a \" bold "},
		{"single_quote", "<!a '<b>bold</b>\n", "<!a ' bold "},
		{"subset_comment", "<!a [<!-- don't \" ] > < --> <b>bold</b>\n", "<!a [ bold "},
		{"script", "<script><b>bold</b>\n", "bold "},
		{"style", "<STYLE><b>bold</b>\n", "bold "},
		{"attribute_double", "<img alt=\"<<\n<b>bold</b>\n", "<img alt=\"<< bold "},
		{"attribute_single", "<img alt='</ \n<b>bold</b>\n", "<img alt='</ bold "},
		{"attribute_whitespace", "<img alt=\n \"<<\n<b>bold</b>\n", "<img alt= \"<< bold "},
	} {
		for _, stage := range []struct {
			name  string
			clean func(string) string
		}{
			{"analysis", CleanContent},
			{"mapped", func(raw string) string { return cleanStage3Content(raw, false).text }},
			{"correspondence", func(raw string) string {
				return strings.Join(strings.Fields(documentCorrespondence(raw, false).text), " ")
			}},
			{"html", func(raw string) string { return strings.Join(strings.Fields(stripHTMLTags(raw)), " ") }},
		} {
			t.Run(tc.name+"/"+stage.name, func(t *testing.T) {
				var small time.Duration
				for _, size := range []int{50_000, 100_000, 200_000} {
					n := size / len(tc.raw)
					tail := "<b>tail</b>"
					if strings.Contains(tc.raw, "<?") {
						// A tagged tail would close otherwise unclosed instructions.
						tail = "tail"
					}
					raw := strings.Repeat(tc.raw, n) + tail
					start := time.Now()
					got := stage.clean(raw)
					elapsed := time.Since(start)
					if want := strings.Repeat(tc.want, n) + "tail"; got != want {
						t.Fatalf("%d bytes: unterminated markup lost text or retained tags", len(raw))
					}
					t.Logf("%d bytes: %s", len(raw), elapsed)
					// Slack permits race instrumentation and scheduler noise, but
					// rejects the multi-second quadratic tail rescans.
					if elapsed > 5*time.Second || small > 0 && elapsed > 3*small+500*time.Millisecond {
						t.Fatalf("nonlinear markup cleaning: small=%s, current=%s", small, elapsed)
					}
					small = elapsed
				}
			})
		}
	}
}

func TestEngineIgnoresMarkupInExclusionsAndExcerpts(t *testing.T) {
	for _, raw := range []string{
		`<?audit secret < 80% ?>alpha`,
		`<?audit secret?>alpha`,
		`<?audit secret>alpha`,
		`<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" />alpha`,
		`<img src="secret" alt="load <80%">alpha`,
		"<!-- secret < 80%\ninternal note -->alpha",
		`<p class="x">alpha</p>`,
		`<!DOCTYPE secret [<!-- don't -->]><secret>alpha</secret>`,
		"<!-- secret\ninternal note -->alpha",
		"<![CDATA[secret < 80%\ninternal note]]>alpha",
		"<!DOCTYPE root [\n<!ENTITY note 'secret < 80%'>\n]>alpha",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "notes.md")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			for _, excludes := range [][]string{nil, {"secret"}} {
				engine := NewSearchEngine([]string{"alpha"}, excludes, nil, false, 1, 1000)
				engine.Silent = true
				matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil || len(matches) != 1 {
					t.Fatalf("excludes=%v: matches=%+v err=%v, want one", excludes, matches, err)
				}
				results, err := engine.ExtractAndBuildResults(matches)
				wantLine := 1 + strings.Count(raw[:strings.Index(raw, "alpha")], "\n")
				if err != nil || len(results) != 1 || results[0].CleanContent != "alpha" ||
					!slices.Equal(results[0].RawExcerpts, []string{"alpha"}) || !slices.Equal(results[0].StartLines, []int{wantLine}) {
					t.Fatalf("excludes=%v: results=%+v err=%v, want alpha at line %d", excludes, results, err, wantLine)
				}
			}
		})
	}
}

func TestEngineSearchesTextBetweenStrayAngleBrackets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("alpha <80% load\nsecret beta\nuntil >90% gamma\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		terms, excludes []string
		want            int
	}{
		{[]string{"alpha", "beta"}, nil, 1},
		{[]string{"alpha", "gamma"}, []string{"secret"}, 0},
	} {
		engine := NewSearchEngine(tc.terms, tc.excludes, nil, false, 1, 1000)
		engine.Silent = true
		matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
		if err != nil || len(matches) != tc.want {
			t.Fatalf("terms=%v excludes=%v: matches=%+v err=%v, want %d", tc.terms, tc.excludes, matches, err, tc.want)
		}
		if tc.want == 0 {
			continue
		}
		results, err := engine.ExtractAndBuildResults(matches)
		if err != nil || len(results) != 1 || !slices.Equal(results[0].StartLines, []int{1}) || !strings.Contains(strings.Join(results[0].RawExcerpts, " "), "secret beta") {
			t.Fatalf("results=%+v err=%v, want one excerpt with secret beta at line 1", results, err)
		}
	}
}

func TestCleaningStageParity(t *testing.T) {
	for _, raw := range []string{"<?php secret < 80; ?>alpha", "<?php secret <\n80; ?>alpha", "<?php if ($a > 1)", "header\n> alpha beta hidden\nvisible\n", "\n-----\nvisible", "<style>hidden</style>\n<script>hidden</script>\n<b>A1,2B</b> &amp;\tend", "\u00a0\x00é\u0085\tX\xff\u2003", "", " \n\t"} {
		for _, code := range []bool{false, true} {
			got := CleanContent(raw)
			if code {
				got = CleanContentCode(raw)
				if strings.HasPrefix(raw, "<?php") && got != strings.Join(strings.Fields(raw), " ") {
					t.Errorf("PHP source changed: %q", got)
				}
			}
			if want := cleanStage3Content(raw, code).text; got != want {
				t.Errorf("raw=%q code=%v: Stage 2=%q Stage 3=%q", raw, code, got, want)
			}
		}
	}
}

func TestMappedWhitespaceAllocationGrowth(t *testing.T) {
	allocs := func(n int) float64 {
		raw := strings.Repeat("word\x00\t \u0085é\n", n)
		return testing.AllocsPerRun(10, func() {
			got := cleanStage3Content(raw, true)
			if len(got.text) == 0 || len(got.offsets) != len(got.text) {
				panic("invalid mapped result")
			}
		})
	}
	small, large := allocs(10), allocs(1000)
	if large-small > 2 {
		t.Fatalf("whitespace allocations scale per run: small=%.0f large=%.0f", small, large)
	}
}

func TestCleanerSpacingReaderReproducesCleanSearchContent(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"A", "B", "1", "2", "x", ",", ",,", " ", "\t", "\n", "\x00", "\x7f", "é", "\xc2\x85", "\xc2\xa0", "\xc2", "\x85"}
	cases := []string{"", ",\xc2", ",\xc2\x85A1", ",,x", "A1B2", strings.Repeat("x", 65535) + ",\xc2\x85A1"}
	for range 2000 {
		var raw strings.Builder
		for n := rng.Intn(80); n > 0; n-- {
			raw.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		cases = append(cases, raw.String())
	}
	for _, raw := range cases {
		want := CleanContent(raw)
		for _, wrap := range []func(io.Reader) io.Reader{func(r io.Reader) io.Reader { return r }, iotest.OneByteReader, iotest.HalfReader} {
			for size := 1; size <= 8; size++ {
				r := &cleanerSpacingReader{r: wrap(strings.NewReader(raw))}
				var out bytes.Buffer
				buf := make([]byte, size)
				for {
					n, err := r.Read(buf)
					out.Write(buf[:n])
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				got := strings.TrimFunc(normalizeMappedWhitespace(mappedContent{text: out.String()}).text, unicode.IsSpace)
				if got != want || r.raw != int64(len(raw)) {
					t.Fatalf("raw=%q size=%d: got %q (%d bytes), want %q (%d bytes)", raw, size, got, r.raw, want, len(raw))
				}
			}
		}
	}
}

func BenchmarkCleanerSpacingReader(b *testing.B) {
	raw := strings.Repeat("Please review PDF32 and 2026Report balance details, 1,000 items.\n", 20000)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := io.Copy(io.Discard, &cleanerSpacingReader{r: strings.NewReader(raw)}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMappedWhitespace(b *testing.B) {
	raw := strings.Repeat("word\x00\t \u0085é\n", 100000)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		_ = cleanStage3Content(raw, true)
	}
}

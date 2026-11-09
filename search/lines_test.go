package search

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestMappedContentReplaceCaptureGroups(t *testing.T) {
	for _, tc := range []struct {
		name, pattern, replacement, want string
		offsets                          []int
	}{
		{"missing", "b", "[$1]", "a[]c", []int{0, 1, 1, 2}},
		{"out of range", "(b)", "$9$1", "abc", []int{0, 1, 2}},
		{"optional unmatched", "(x)?b", "[$1]", "a[]c", []int{0, 1, 1, 2}},
		{"optional matched", "(b)?", "$1", "abc", []int{0, 1, 2}},
		{"empty capture", "()b", "$1", "ac", []int{0, 2}},
		{"whole match", "b", "$0", "abc", []int{0, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := mappedContent{"abc", []int{0, 1, 2}}
			got := m.replace(regexp.MustCompile(tc.pattern), tc.replacement)
			if got.text != tc.want || !slices.Equal(got.offsets, tc.offsets) {
				t.Fatalf("replace = %#v, want text %q offsets %v", got, tc.want, tc.offsets)
			}
		})
	}
}

func TestExcerptLinesScratchAllocations(t *testing.T) {
	query := compileQuery([]string{"needle"}, PartialModeOff)
	spans := make([]excerptSpan, 200)
	for i := range spans {
		spans[i] = excerptSpan{rawStart: 0, rawStartValid: true}
	}
	measure := func(spans []excerptSpan) float64 {
		return testing.AllocsPerRun(10, func() {
			got := computeExcerptSpanLinesQuery("needle", spans, query, 1)
			for _, line := range got {
				if line != 1 {
					t.Fatalf("anchor = %d, want 1", line)
				}
			}
		})
	}
	// Both output slices exceed the compiler's small-slice stack optimization.
	fewer, many := measure(spans[:100]), measure(spans)
	// Allow incidental process-wide allocation noise, but not even one extra
	// allocation per span (which would add 100 allocations here).
	if many-fewer > 2 {
		t.Fatalf("allocations grew with spans: hundred=%g, two hundred=%g", fewer, many)
	}
	t.Logf("allocations: hundred spans=%g, two hundred spans=%g", fewer, many)
}

func TestExcerptLinesScratchResetAndLazyIdentity(t *testing.T) {
	raw := "alpha\nbeta\nalpha\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := lineSpans([]int{0}, []int{1}, nil, []int{0})
	// A synthetic span precedes an exact span, which must initialize lazily.
	cleaned := CleanContentCode(raw)
	identities := []excerptMatch{
		{term: 0, source: matchRange{start: 0, end: 5}, occurrence: 1},
		{term: 1, source: matchRange{start: 6, end: 10}, occurrence: 1},
		{term: 0, source: matchRange{start: 11, end: 16}, occurrence: 2},
	}
	spans[1].sourceText = cleaned
	spans[1].sourceMatches = identities
	spans[1].matches = identities[1:2]
	spans[3].sourceText = cleaned
	spans[3].sourceMatches = identities
	spans[3].matches = identities[2:]
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 1); !slices.Equal(got, []int{1, 2, 0, 3}) {
		t.Fatalf("mixed anchors = %v, want [1 2 0 3]", got)
	}
	// Disjoint fallback terms and an empty span must not retain prior membership.
	if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}, []int{1}, nil, []int{0}), query, 1); !slices.Equal(got, []int{1, 2, 0, 3}) {
		t.Fatalf("fallback anchors = %v, want [1 2 0 3]", got)
	}
}

func TestEmittedExcerptLinesKeepClusterIdentity(t *testing.T) {
	for _, isCode := range []bool{false, true} {
		for _, limit := range []int{1, 2} {
			t.Run(fmt.Sprintf("code=%t/limit=%d", isCode, limit), func(t *testing.T) {
				raw := "header\nalpha " + strings.Repeat("loose ", 30) + "beta.\n" +
					strings.Repeat("padding line.\n", 100) + "alpha beta tight cluster.\n"
				query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
				spans := extractRankedExcerptSpansQuery(raw, query, limit, 2, 2, isCode, 5000)
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

func TestExcerptLinesBalancedRemovedAndCreatedMatches(t *testing.T) {
	raw := "<script>alpha</script>\n" + strings.Repeat("padding.\n", 100) +
		"alpha beta\n" + strings.Repeat("padding.\n", 100) + "ALPHA2\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	cleaned := CleanContent(raw)
	if len(query.matchers[0].findAll(raw)) != 2 || len(query.matchers[0].findAll(cleaned)) != 2 {
		t.Fatal("fixture must balance removed and created alpha occurrences")
	}
	spans := extractRankedExcerptSpansQuery(raw, query, 1, 2, 2, false, 200)
	if len(spans) != 1 || !strings.Contains(spans[0].text, "alpha beta") {
		t.Fatalf("expected alpha beta cluster: %#v", spans)
	}
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{102}) {
		t.Fatalf("balanced-transform anchors = %v, want [102]", got)
	}
}

func TestExcerptLinesCursorPassesEntireCluster(t *testing.T) {
	for _, markup := range []bool{false, true} {
		t.Run(fmt.Sprintf("markup=%t", markup), func(t *testing.T) {
			middle := "gamma"
			if markup {
				middle = "<script>gamma</script>"
			}
			raw := "alpha " + middle + " beta\n" + strings.Repeat("padding.\n", 100) +
				"alpha gamma\n" + strings.Repeat("padding.\n", 100) + "alpha\n"
			query := compileQuery([]string{"alpha", "beta", "gamma"}, PartialModeOff)

			// Markup exercises the reported removed-gamma case. Plain text keeps
			// the first cluster exact; clear the second cluster's identities below
			// to independently exercise the exact-to-fallback cursor.
			spans := extractRankedExcerptSpansQuery(raw, query, 2, 2, 2, false, 200)
			if len(spans) != 2 {
				t.Fatalf("expected two clusters: %#v", spans)
			}
			slices.SortFunc(spans, func(a, b excerptSpan) int { return a.left - b.left })
			if !markup {
				spans[1].rawStartValid = false
				for j := range spans[1].matches {
					spans[1].matches[j].occurrence = 0
				}
			}
			if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{1, 102}) {
				t.Fatalf("cluster cursor anchors = %v, want [1 102]", got)
			}
		})
	}
}

func TestExcerptLinesStage3Correspondence(t *testing.T) {
	for _, isCode := range []bool{false, true} {
		t.Run(fmt.Sprintf("code=%t", isCode), func(t *testing.T) {
			raw := "<style>noise</style><script>noise</script>\nnoise > quote\n-----\n" +
				"<b>alpha</b> " + strings.Repeat("loose ", 30) + "beta.\n" +
				strings.Repeat("padding.\n", 100) + "alpha beta tight cluster. OB1,2NEXT &amp;\n"
			query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(raw, query, 2, 2, 2, isCode, 5000)
			if len(spans) != 2 || !strings.Contains(spans[0].text, "tight cluster") {
				t.Fatalf("expected tight then loose excerpts: %#v", spans)
			}
			mapped := cleanStage3Content(raw, isCode)
			if mapped.text != spans[0].sourceText {
				t.Fatal("correspondence must reproduce the actual excerpt cleaner")
			}
			for _, match := range spans[0].sourceMatches {
				locs := query.matchers[match.term].findAll(raw)
				if got := mapped.offsets[match.source.start]; got != locs[match.occurrence-1].start {
					t.Fatalf("mapped offset = %d, want %d", got, locs[match.occurrence-1].start)
				}
			}
			// Ranked order deliberately goes backwards; a heuristic tightest-cluster
			// fallback would reuse the tight cluster instead of resolving the loose one.
			if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{105, 4}) {
				t.Fatalf("document anchors = %v, want [105 4]", got)
			}
		})
	}
}

func TestExcerptLinesFallbackWideToTight(t *testing.T) {
	wide := "<b>beta</b> " + strings.Repeat("wide ", 24) + "alpha."
	tight := "alpha beta tight cluster."
	raw := wide + "\n" + strings.Repeat("gap ", 10) + "\n" + tight + "\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	// Supply two separately rendered clusters: the anchoring fallback must not
	// depend on whether the renderer's context budget merges nearby excerpts.
	spans := extractRankedExcerptSpansQuery(CleanContent(wide), query, 1, 2, 2, false, 200)
	spans = append(spans, extractRankedExcerptSpansQuery(CleanContent(tight), query, 1, 2, 2, false, 200)...)
	if len(spans) != 2 {
		t.Fatalf("expected two emitted clusters: %#v", spans)
	}
	// Remove ordinal metadata to exercise fallback independently of the new
	// document exact path. Retain source match order from real emitted excerpts.
	for i := range spans {
		spans[i].rawStartValid = false
		for j := range spans[i].matches {
			spans[i].matches[j].occurrence = 0
		}
	}
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("wide-to-tight fallback = %v, want [1 3]", got)
	}
	if got := computeExcerptSpanLinesQuery(raw, spans[1:], query, 200); !slices.Equal(got, []int{3}) {
		t.Fatalf("single tight fallback = %v, want [3]", got)
	}
	// Without source ordering, equal coverage must still prefer tighter width.
	if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0, 1}), query, 200); !slices.Equal(got, []int{3}) {
		t.Fatalf("width tie-break = %v, want [3]", got)
	}
}

func TestExcerptLinesFallbackPrefersUnusedMatches(t *testing.T) {
	raw := "alpha beta\nalpha\n" + strings.Repeat("gap ", 10) + "beta\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := lineSpans([]int{0, 1}, []int{0, 1})
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("unused fallback = %v, want [1 2]", got)
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

// Check every byte offset, including prefixes ending inside UTF-8 runes.
func checkCodePrefixCorrespondence(t *testing.T, raw string) {
	t.Helper()
	prefixes := make(map[int]codePrefix, len(raw)+1)
	for off := 0; off <= len(raw); off++ {
		prefixes[off] = codePrefix{}
	}
	text := codePrefixCorrespondence(raw, prefixes)
	normalized := strings.TrimSpace(text)
	if want := CleanContentCode(raw); normalized != want {
		t.Fatalf("full normalization of %q = %q, want %q", raw, normalized, want)
	}
	for off := 0; off <= len(raw); off++ {
		p := prefixes[off]
		want := CleanContentCode(raw[:off])
		if got := text[:p.end] + p.tail; got != want {
			t.Fatalf("prefix %q at %d = %q, want %q", raw, off, got, want)
		}
		for end := 0; end <= len(normalized); end++ {
			source := strings.TrimSpace(normalized[:end])
			if got := p.corresponds(normalized, len(source)); got != (want == source) {
				t.Fatalf("correspondence %q at raw=%d/source=%d = %t, want %t", raw, off, end, got, want == source)
			}
		}
	}
}

func FuzzCodePrefixCorrespondence(f *testing.F) {
	for _, raw := range []string{
		"", "   \t\r\n", " \talpha\r\n  \t beta \n ",
		"\x00a\x01\x7fb\u0080\u009fc", "a \t\x00  b",
		"\u00a0\u2003alpha\u2003 \u00a0beta\u3000",
		"\u0085\u00a0\u2003", "a\u0085\u00a0\u2003", "é界🙂",
		"\xff\xc2 \xe2\x80x\xf0\x9f", "a\u2003\x00\tb",
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		// The reference intentionally rescans every prefix; bound its test cost.
		if len(raw) > 256 {
			t.Skip()
		}
		checkCodePrefixCorrespondence(t, raw)
	})
}

// Vary both bytes and match count to expose repeated prefix normalization or
// whole-prefix comparisons. All matches sit near the end of the input.
func BenchmarkExcerptLinesManyLateMatches(b *testing.B) {
	for _, size := range []int{1 << 16, 1 << 20} {
		for _, matches := range []int{1, 100} {
			b.Run(fmt.Sprintf("bytes=%d/matches=%d", size, matches), func(b *testing.B) {
				raw := strings.Repeat("x", size) + "\n" + strings.Repeat("needle ", matches)
				query := compileQuery([]string{"needle"}, PartialModeOff)
				// Construct a complete cluster without the excerpt renderer's text
				// budget truncating away the late occurrences being benchmarked.
				cleaned := CleanContentCode(raw)
				var identities []excerptMatch
				for ordinal, loc := range query.matchers[0].findAll(cleaned) {
					identities = append(identities, excerptMatch{matchRange: loc, source: loc, term: 0, occurrence: ordinal + 1})
				}
				spans := []excerptSpan{{sourceText: cleaned, sourceMatches: identities, matches: identities}}
				if len(identities) != matches {
					b.Fatalf("fixture must contain %d matches in one span", matches)
				}
				b.SetBytes(int64(len(raw)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					got := computeExcerptSpanLinesQuery(raw, spans, query, 5000)
					if len(got) != 1 || got[0] != 2 {
						b.Fatalf("anchors = %v, want [2]", got)
					}
				}
			})
		}
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

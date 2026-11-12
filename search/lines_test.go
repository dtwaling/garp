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

func TestComputeExcerptLines(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		words     []string
		termSets  [][]int
		distance  int
		want      []int
	}{
		{"SingleTerm", "line one\nline two\nhere is the needle right here\nline four", []string{"needle"}, [][]int{{0}}, 0, []int{3}},
		{"MultiTermStartsAtEarliest", "intro\nthe alpha begins\nmiddle filler line\nthe omega ends\noutro", []string{"alpha", "omega"}, [][]int{{0, 1}}, 5000, []int{2}},
		{"DisambiguatesRepeats", "config header here\nbody\nmore body\nfiller\nconfig with payload inside\ntail", []string{"config", "payload"}, [][]int{{0, 1}}, 5000, []int{5}},
		{"MultipleExcerptsInOrder", "target near top\nx\nx\nx\nx\nx\nx\nx\nx\nx\ntarget near bottom", []string{"target"}, [][]int{{0}, {0}}, 200, []int{1, 11}},
		{"NoTermLeavesUnknown", "nothing relevant here\nstill nothing", []string{"absentword"}, [][]int{nil}, 0, []int{0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query := compileQuery(tt.words, PartialModeOff)
			starts := computeExcerptSpanLinesQuery(tt.raw, lineSpans(tt.termSets...), query, tt.distance)
			if !slices.Equal(starts, tt.want) {
				t.Errorf("start lines = %v, want %v", starts, tt.want)
			}
		})
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

// Removed occurrences and OCR-created tokens must not redirect a
// visible cluster to an unrelated raw occurrence or a stripped opening block.
func TestStage3TransformedLineAnchors(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		terms     []string
		want      int
	}{
		{"blockquote", "# Header\n\n> alpha beta hidden\n\nalpha beta visible\n", []string{"alpha", "beta"}, 5},
		{"style and script", "<style>\nalpha beta\n</style>\n<script>\nalpha beta\n</script>\nalpha beta visible\n", []string{"alpha", "beta"}, 7},
		{"OCR split", "ITEM header\n" + strings.Repeat("padding.\n", 100) + "ITEM01 beta visible\n", []string{"ITEM", "beta"}, 102},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := compileQuery(tc.terms, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(tc.raw, query, 1, 2, 2, false, 200)
			if len(spans) != 1 || !strings.Contains(spans[0].text, "visible") || strings.Contains(spans[0].text, "hidden") {
				t.Fatalf("expected visible cluster: %#v", spans)
			}
			if got := computeExcerptSpanLinesQuery(tc.raw, spans, query, 200); !slices.Equal(got, []int{tc.want}) {
				t.Fatalf("anchors = %v, want [%d]", got, tc.want)
			}
		})
	}
}

func TestStage3DistinctRankedChunkAnchors(t *testing.T) {
	raw := "> proximity search hidden\n"
	wantByMarker := make(map[string]int)
	for i := 0; i < 5; i++ {
		marker := fmt.Sprintf("cluster-%c", 'a'+i)
		wantByMarker[marker] = strings.Count(raw, "\n") + 1
		// Later clusters are tighter, forcing ranking to run backwards through
		// the document rather than accidentally passing via a forward cursor.
		raw += "proximity " + strings.Repeat("loose ", (4-i)*5) + "search " + marker + ".\n"
		raw += strings.Repeat("padding line.\n", 100)
	}
	query := compileQuery([]string{"proximity", "search"}, PartialModeOff)
	for _, limit := range []int{1, 3, 5} {
		spans := extractRankedExcerptSpansQuery(raw, query, limit, 2, 2, false, 200)
		if len(spans) != limit {
			t.Fatalf("limit %d: got %d spans", limit, len(spans))
		}
		lines := computeExcerptSpanLinesQuery(raw, spans, query, 200)
		seen := make(map[int]bool)
		for i, span := range spans {
			want := 0
			for marker, line := range wantByMarker {
				if strings.Contains(span.text, marker) {
					if want != 0 {
						t.Fatalf("multiple clusters in excerpt: %q", span.text)
					}
					want = line
				}
			}
			if want == 0 || lines[i] != want || seen[lines[i]] {
				t.Fatalf("limit %d excerpt %q: line %d, want distinct %d (all %v)", limit, span.text, lines[i], want, lines)
			}
			seen[lines[i]] = true
		}
		if lines[0] != wantByMarker["cluster-e"] {
			t.Fatalf("tightest final cluster must rank first: %v", lines)
		}
	}
}

// Benchmark only line resolution, not cleaning/excerpt extraction. The stripped
// script makes raw and cleaned occurrence counts differ, triggering the former
// quadratic fallback. Keep setup outside the timer and assert every result.
func BenchmarkComputeExcerptSpanLines5MB(b *testing.B) {
	const size = 5_000_000
	const prefix = "<script>alpha beta</script>\n"
	const row = "alpha beta frequent terms padding.\n"
	raw := prefix + strings.Repeat(row, (size-len(prefix))/len(row))
	raw += strings.Repeat("x", size-len(raw))
	if len(raw) != size || strings.Count(raw, "alpha") <= 100_000 {
		b.Fatal("fixture requires 5 MB and over 100k occurrences per term")
	}
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	// One real emitted span suffices to expose the former quadratic anchoring
	// scan; repeated identical rows are intentionally not distinct excerpts.
	spans := extractRankedExcerptSpansQuery(raw, query, 1, 2, 2, false, 200)
	if len(spans) != 1 {
		b.Fatalf("expected one emitted span, got %d", len(spans))
	}
	want := make([]int, len(spans))
	for i, span := range spans {
		if !span.rawStartValid || span.rawStart < len(prefix) {
			b.Fatalf("invalid visible anchor: %d", span.rawStart)
		}
		want[i] = 2 + (span.rawStart-len(prefix))/len(row)
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, want) {
			b.Fatalf("anchors = %v, want %v", got, want)
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

func TestPartialEmittedLinesKeepOccurrenceThroughExpansion(t *testing.T) {
	raw := "header\nrun_deployment " + strings.Repeat("loose ", 30) + "ms-first-api.\n" +
		strings.Repeat("padding line.\n", 100) + "run_deployment ms-last-api tight.\n"
	query := compileQuery([]string{"deploy", "ms-*-api"}, PartialModePrefix)
	spans := extractRankedExcerptSpansQuery(raw, query, 1, 2, 2, true, 5000)
	if len(spans) != 1 || !strings.Contains(spans[0].text, "ms-last-api") {
		t.Fatalf("expected later partial-match cluster: %#v", spans)
	}
	spans[0] = expandShortExcerptSpan(spans[0], 600)
	if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{103}) {
		t.Fatalf("expanded partial anchors = %v, want [103]", got)
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

func TestMultipartExcerptLinesKeepWideClusterIdentity(t *testing.T) {
	raw := "header\nalpha " + strings.Repeat("loose ", 300) + "beta.\n" +
		strings.Repeat("padding line.\n", 100) + "alpha " + strings.Repeat("tight ", 180) + "beta.\n"
	query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery(raw, query, 2, 2, 2, true, 5000)
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

func TestStage3MappedCleaning(t *testing.T) {
	for _, tc := range []struct{ block, prefix string }{
		{`<?audit secret < 80% ?>`, ""},
		{`<?audit secret?>`, ""},
		{`<?audit secret>`, ""},
		{`<?a>one<?b>two<?c?>three `, "one two three "},
		{`<p>Hi<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" /><o:p></o:p></p><p>budget visible</p><?php echo 1 ?>last `, "Hi budget visible last "},
		{`<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" />`, ""},
		{`<?no closer at all `, "<?no closer at all "},
		{`<?php if ($a > 1) ?>`, ""},
		{`<img src="secret" alt="load </b> value">`, ""},
		{`<script>secret</script foo>`, ""},
		{`<script>secret</script >`, ""},
		{`<script/>`, ""},
		{`<title>a <!-- b</title>`, "a <!-- b "},
		{`<textarea>a <!-- b</textarea>`, "a <!-- b "},
		{`<title>heading</title>`, "heading "},
		{`<script>const s = "<!--";</script>`, ""},
		{`<style>a::after {content: "<!--"}</style>`, ""},
		{`<SCRIPT title="a>b">"<![CDATA["</SCRIPT>`, ""},
		{`<!DOCTYPE secret [<!-- don't -->]>`, ""},
		{`<script>hidden</script><style>hidden</style>`, ""},
	} {
		raw := tc.block + "\nalpha\n<!-- footer -->tail"
		for _, mapped := range []mappedContent{cleanStage3Content(raw, false), documentCorrespondence(raw, true), documentCorrespondence(raw, false)} {
			if got := strings.Join(strings.Fields(mapped.text), " "); got != tc.prefix+"alpha tail" {
				t.Fatalf("%q: mapped text = %q", tc.block, got)
			}
			for _, term := range []string{"alpha", "tail"} {
				if got := mapped.offsets[strings.Index(mapped.text, term)]; got != strings.Index(raw, term) {
					t.Fatalf("%q: %s offset = %d", tc.block, term, got)
				}
			}
		}
	}
	raw := "<!-- hidden <b>\n<![CDATA[ hidden -->\n<![CDATA[<!-- hidden\n]]>\n" +
		"<!DOCTYPE root [\n<!ENTITY note 'hidden ] < >'>\n]>\n" +
		"<style>hidden</style>\n<script>hidden</script>\n> hidden\n-----\n<b>é</b> A1,2B\x09end\xff\n"
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
	query := compileQuery([]string{"needle"}, PartialModeOff)
	spans := lineSpans([]int{0}, []int{0}, []int{0})
	for _, tc := range []struct {
		raw  string
		want []int
	}{
		{"<style>needle</style>\n<script>needle</script>\n> needle\n-----\nneedle\nneedle\n", []int{5, 6, 0}},
		{"<img src=\"needle\" alt=\"load <80%\">\nneedle\nneedle", []int{2, 3, 0}},
		{"<!-- needle < 80%\ninternal note -->needle\nneedle", []int{2, 3, 0}},
		{"<!-- open\n<b>needle</b>\nneedle", []int{2, 3, 0}},
		{"<![CDATA[open\n<b>needle</b>\nneedle", []int{2, 3, 0}},
		{"<!a [\n<b>needle</b>\nneedle", []int{2, 3, 0}},
		{"<!DOCTYPE x [\n<b>needle</b>\nneedle", []int{2, 3, 0}},
		{"<script>const s = \"<!-- needle\";</script>\nneedle\n<!-- footer -->needle", []int{2, 3, 0}},
		{"<style>a {content: '<!-- needle'}</style>\nneedle\n<!-- footer -->needle", []int{2, 3, 0}},
	} {
		if got := computeExcerptSpanLinesQuery(tc.raw, spans, query, 200); !slices.Equal(got, tc.want) {
			t.Errorf("raw=%q: legacy anchors=%v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestStage3LegacyFallbackKeepsTextBetweenStrayBrackets(t *testing.T) {
	raw := "If <80% free\nneedle\nuntil >90%\nneedle\n"
	query := compileQuery([]string{"needle"}, PartialModeOff)
	if got := computeExcerptSpanLinesQuery(raw, lineSpans([]int{0}, []int{0}), query, 200); !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("legacy anchors=%v, want [2 4]", got)
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

package search

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func byteRange(start int) matchRange { return matchRange{start: start, end: start + 1} }

func TestExcerptLinesLargePrefixRange(t *testing.T) {
	for _, size := range []int{1 << 16, 2 << 20} {
		for _, mode := range []string{"code", "clean document", "raw document"} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				raw := "header.\n" + "needle" + strings.Repeat("x", size) + " tail.\n"
				code := mode == "code"
				if !code {
					raw = "<b>" + raw
				}
				content := raw
				if code {
					content = CleanContentCode(raw)
				} else if mode == "clean document" {
					content = CleanContent(raw)
				}
				query := compileQuery([]string{"needle"}, PartialModePrefix)
				spans := extractRankedExcerptSpansQuery(content, query, 1, 1, 1, code, 200)
				if len(spans) != 1 || len(spans[0].matches) != 1 {
					t.Fatal("expected one emitted match")
				}
				match := spans[0].matches[0].source
				if match.end-match.start != size+len("needle") || len(spans[0].text) >= size {
					t.Fatal("fixture must retain full source token but emit only a fragment")
				}
				offsets := requestedDocumentOffsets(append(spans, spans...))
				if len(offsets) != 1 {
					t.Fatalf("large token requested %d ranges", len(offsets))
				}
				if code {
					codeCorrespondence(raw, offsets)
				} else {
					documentCorrespondence(raw, mode == "clean document", offsets)
				}
				if got, ok := mappedMatchOffset(raw, spans[0].sourceText, match, offsets); !ok || got != strings.Index(raw, "needle") {
					t.Fatalf("range offset=%d valid=%t", got, ok)
				}
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{2}) {
					t.Fatalf("anchors=%v", got)
				}
			})
		}
	}
}

func TestDocumentRangeInteriorIdentity(t *testing.T) {
	for _, tc := range []struct {
		pattern, replacement string
		valid                bool
	}{
		{"(b)", "$1", true},
		{"(b)(c)", "$1$2", true},
		{"b", "x", false},
		{"(b)(c)", "$2$1", false},
		{"b", "", false},
		{"(b)", "$1$1", false},
	} {
		text, step := documentReplace("abcd", regexp.MustCompile(tc.pattern), tc.replacement)
		whole := matchRange{start: 0, end: len(text)}
		offsets := sourceOffsets{whole: 0, byteRange(0): 0, byteRange(len(text) - 1): 0}
		resolveDocumentOffsets(text, []docStep{step}, offsets)
		if offsets[byteRange(0)] != 0 || offsets[byteRange(len(text)-1)] != 3 {
			t.Fatal("endpoints must remain intact")
		}
		if (offsets[whole] == 0) != tc.valid {
			t.Fatalf("%s -> %s: range identity=%d", tc.pattern, tc.replacement, offsets[whole])
		}
	}
}

func BenchmarkRequestedLargePrefixRange(b *testing.B) {
	for _, size := range []int{1 << 16, 2 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			source := "needle" + strings.Repeat("x", size)
			spans := []excerptSpan{{sourceText: source, matches: []excerptMatch{{source: matchRange{start: 0, end: len(source)}}}}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if offsets := requestedDocumentOffsets(spans); len(offsets) != 1 {
					b.Fatal(len(offsets))
				}
			}
		})
	}
}

func TestExcerptLinesNormalizedPhraseSeparators(t *testing.T) {
	for _, code := range []bool{false, true} {
		for _, sep := range []string{"  ", "\t", "\n", "\r\n", "\x00", "\u0085", " \t\n "} {
			t.Run(fmt.Sprintf("code=%t/sep=%q", code, sep), func(t *testing.T) {
				header := "header\n"
				if !code {
					header = "<b>header</b>\n"
				}
				raw := header + "alpha" + sep + "beta " + strings.Repeat("loose ", 30) + "gamma.\n" +
					strings.Repeat("padding.\n", 100) + "alpha beta gamma tight cluster.\n"
				content := CleanContent(raw)
				if code {
					content = CleanContentCode(raw)
				}
				query := compileQuery([]string{"alpha beta", "gamma"}, PartialModeOff)
				spans := extractRankedExcerptSpansQuery(content, query, 2, 2, 2, code, 5000)
				if len(spans) != 2 || !strings.Contains(spans[0].text, "tight cluster") {
					t.Fatalf("expected two ranked clusters: %#v", spans)
				}
				want := []int{103 + strings.Count(sep, "\n"), 2}
				for range 2 {
					if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, want) {
						t.Fatalf("anchors = %v, want %v", got, want)
					}
					slices.Reverse(spans)
					slices.Reverse(want)
				}
			})
		}
	}
}

func TestCodeSparseOffsetsManyOccurrences(t *testing.T) {
	for _, count := range []int{100, 10000} {
		raw := strings.Repeat("needle\n", count)
		source := CleanContentCode(raw)
		start := strings.LastIndex(source, "needle")
		match := excerptMatch{term: 0, source: matchRange{start: start, end: start + 6}, occurrence: count}
		spans := []excerptSpan{{sourceText: source, matches: []excerptMatch{match}}}
		offsets := requestedDocumentOffsets(append(spans, spans...))
		if got := codeCorrespondence(raw, offsets); got != source || len(offsets) != 1 {
			t.Fatalf("correspondence mismatch or non-sparse map: %d entries", len(offsets))
		}
		if offset, ok := mappedMatchOffset(raw, source, match.source, offsets); !ok || offset != strings.LastIndex(raw, "needle") {
			t.Fatalf("mapped offset = %d, valid=%t", offset, ok)
		}
		query := compileQuery([]string{"needle"}, PartialModeOff)
		if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{count}) {
			t.Fatalf("anchors = %v, want [%d]", got, count)
		}
	}
}

func TestExcerptLinesDocumentPhraseSpaces(t *testing.T) {
	for _, clean := range []bool{false, true} {
		t.Run(fmt.Sprintf("clean=%t", clean), func(t *testing.T) {
			raw := "<b>header</b>\nalpha beta " + strings.Repeat("loose ", 30) + "gamma.\n" +
				strings.Repeat("padding.\n", 100) + "alpha beta gamma tight cluster.\n"
			content := raw
			if clean {
				content = CleanContent(raw)
			}
			query := compileQuery([]string{"alpha beta", "gamma"}, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(content, query, 2, 2, 2, false, 5000)
			if len(spans) != 2 || !strings.Contains(spans[0].text, "tight cluster") {
				t.Fatalf("expected tight then loose excerpts: %#v", spans)
			}
			if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{103, 2}) {
				t.Errorf("ranked phrase anchors = %v, want [103 2]", got)
			}
			slices.Reverse(spans)
			if got := computeExcerptSpanLinesQuery(raw, spans, query, 5000); !slices.Equal(got, []int{2, 103}) {
				t.Errorf("document-order phrase anchors = %v, want [2 103]", got)
			}
		})
	}
}

func TestDocumentWhitespaceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, separator, normalized string
		retained                    bool
	}{
		{"space", " ", " ", true},
		{"spaces", "  ", " ", false},
		{"tab", "\t", " ", false},
		{"newline", "\n", " ", false},
		{"crlf", "\r\n", " ", false},
		{"form feed", "\f", " ", false},
		{"vertical tab", "\v", " ", false},
		{"control", "\x00", " ", false},
		{"c1 control", "\u0085", " ", false},
		{"mixed", " \t\r\n ", " ", false},
		{"nbsp", "\u00a0", "\u00a0", true},
		{"unicode space", "\u2003", "\u2003", true},
		{"tag", "<b>", " ", false},
		{"entity", "&nbsp;", " ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "\u2003<b>header</b>\nalpha" + tc.separator + "beta\u00a0"
			text := documentCorrespondence(raw, true, nil)
			phrase := "alpha" + tc.normalized + "beta"
			start := strings.Index(text, phrase)
			if start < 0 || text != CleanContent(raw) {
				t.Fatalf("unexpected cleaned text %q", text)
			}
			offsets := make(sourceOffsets)
			for j := range len(phrase) {
				offsets[byteRange(start+j)] = -1
			}
			documentCorrespondence(raw, true, offsets)
			for j := range len(phrase) {
				want := -1
				switch {
				case j < len("alpha") || tc.retained:
					want = strings.Index(raw, "alpha") + j
				case j >= len("alpha")+len(tc.normalized):
					want = strings.Index(raw, "beta") + j - len("alpha") - len(tc.normalized)
				}
				if got := offsets[byteRange(start+j)]; got != want {
					t.Errorf("byte %d maps to %d, want %d", j, got, want)
				}
			}
		})
	}
}

func TestDocumentSparseOffsetsLarge(t *testing.T) {
	for _, size := range []int{1 << 16, 1 << 20} {
		raw := "<b>header</b>\n" + strings.Repeat("x", size) + "\nALPHA2 needle\n"
		query := compileQuery([]string{"alpha", "needle"}, PartialModeOff)
		spans := extractRankedExcerptSpansQuery(CleanContent(raw), query, 1, 2, 2, false, 200)
		if len(spans) != 1 {
			t.Fatalf("expected one span, got %d", len(spans))
		}
		// Duplicating a span must not increase the requested range set.
		offsets := requestedDocumentOffsets(append(spans, spans...))
		if len(offsets) != 2 {
			t.Fatalf("requested %d offsets for %d raw bytes", len(offsets), len(raw))
		}
		if got := documentCorrespondence(raw, true, offsets); got != spans[0].sourceText {
			t.Fatal("cleaner mismatch")
		}
		if len(offsets) != 2 {
			t.Fatalf("resolution grew map to %d entries", len(offsets))
		}
		for _, match := range spans[0].matches {
			start := strings.Index(raw, spans[0].sourceText[match.source.start:match.source.end])
			if offsets[match.source] != start {
				t.Fatalf("offset[%v] = %d", match.source, offsets[match.source])
			}
		}
		if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{3}) {
			t.Fatalf("anchors = %v", got)
		}
	}
}

func TestDocumentReverseSteps(t *testing.T) {
	// Reverse capture order, duplicate captures, a synthetic byte, and a
	// preceding deletion require sorting surviving requests between steps.
	text, first := documentReplace("!abc", regexp.MustCompile("!"), "")
	text, second := documentReplace(text, regexp.MustCompile("(a)(b)"), "$2-$1$2")
	if text != "b-abc" {
		t.Fatalf("text = %q", text)
	}
	for _, keys := range [][]int{{0}, {1}, {4}, {0, 1, 2, 3, 4}, {-1, 5}} {
		offsets := make(sourceOffsets)
		for _, k := range keys {
			offsets[byteRange(k)] = 99
		}
		resolveDocumentOffsets(text, []docStep{first, second}, offsets)
		want := map[int]int{-1: -1, 0: 2, 1: -1, 2: 1, 3: 2, 4: 3, 5: -1}
		for _, k := range keys {
			if offsets[byteRange(k)] != want[k] {
				t.Fatalf("key %d = %d, want %d", k, offsets[byteRange(k)], want[k])
			}
		}
	}
}

func TestDocumentReplaceByteIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, pattern, replacement, want string
		offsets                          []int
	}{
		{"missing", "b", "[$1]", "a[]c", []int{0, -1, -1, 2}},
		{"out of range", "(b)", "$9$1", "abc", []int{0, 1, 2}},
		{"optional unmatched", "(x)?b", "[$1]", "a[]c", []int{0, -1, -1, 2}},
		{"optional matched", "(b)?", "$1", "abc", []int{0, 1, 2}},
		{"empty capture", "()b", "$1", "ac", []int{0, 2}},
		{"whole match", "b", "$0", "abc", []int{0, 1, 2}},
		{"literal no-op", "b", "b", "abc", []int{0, 1, 2}},
		{"multibyte-range no-op", "ab", "ab", "abc", []int{0, 1, 2}},
		{"empty no-op", "^", "", "abc", []int{0, 1, 2}},
		{"mixed no-op and change", "[ab]", "b", "bbc", []int{-1, 1, 2}},
		{"equal text reordered captures", "(b)()", "$2$1", "abc", []int{0, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, step := documentReplace("abc", regexp.MustCompile(tc.pattern), tc.replacement)
			offsets := sourceOffsets{byteRange(-1): 0, byteRange(len(got)): 0}
			for k := range tc.offsets {
				offsets[byteRange(k)] = 0
			}
			resolveDocumentOffsets(got, []docStep{step}, offsets)
			if got != tc.want || offsets[byteRange(-1)] != -1 || offsets[byteRange(len(got))] != -1 {
				t.Fatalf("replace = %q, offsets %v, want %q", got, offsets, tc.want)
			}
			for k, want := range tc.offsets {
				if offsets[byteRange(k)] != want {
					t.Fatalf("offset[%d] = %d, want %d", k, offsets[byteRange(k)], want)
				}
			}
		})
	}
}

func TestExcerptLinesScratchAllocations(t *testing.T) {
	query := compileQuery([]string{"needle"}, PartialModeOff)
	for _, mode := range []string{"fallback", "code", "document"} {
		t.Run(mode, func(t *testing.T) {
			raw := "needle"
			span := lineSpans([]int{0})[0]
			if mode != "fallback" {
				span.sourceText = "needle"
				span.matches[0].source = matchRange{start: 0, end: 6}
			}
			if mode == "document" {
				raw = "<b></b>needle"
			}
			spans := make([]excerptSpan, 200)
			for i := range spans {
				spans[i] = span
			}
			measure := func(spans []excerptSpan) float64 {
				return testing.AllocsPerRun(10, func() {
					got := computeExcerptSpanLinesQuery(raw, spans, query, 1)
					for _, line := range got {
						if line != 1 {
							t.Fatalf("anchor = %d, want 1", line)
						}
					}
				})
			}
			// Both output slices exceed the compiler's small-slice stack optimization.
			fewer, many := measure(spans[:100]), measure(spans)
			// Allow incidental process-wide noise, not per-span allocations.
			if many-fewer > 2 {
				t.Fatalf("allocations grew with spans: hundred=%g, two hundred=%g", fewer, many)
			}
			t.Logf("allocations: hundred spans=%g, two hundred spans=%g", fewer, many)
		})
	}
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
				content := CleanContent(raw)
				if isCode {
					content = CleanContentCode(raw)
				}
				spans := extractRankedExcerptSpansQuery(content, query, limit, 2, 2, isCode, 5000)
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
	spans := extractRankedExcerptSpansQuery(cleaned, query, 1, 2, 2, false, 200)
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
			cleaned := CleanContent(raw)
			// Markup exercises the reported removed-gamma case. Plain text keeps
			// the first cluster exact; clear the second cluster's identities below
			// to independently exercise the exact-to-fallback cursor.
			spans := extractRankedExcerptSpansQuery(cleaned, query, 2, 2, 2, false, 200)
			if len(spans) != 2 {
				t.Fatalf("expected two clusters: %#v", spans)
			}
			slices.SortFunc(spans, func(a, b excerptSpan) int { return a.left - b.left })
			if !markup {
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

func TestExcerptLinesDocumentCorrespondence(t *testing.T) {
	for _, clean := range []bool{false, true} {
		t.Run(fmt.Sprintf("clean=%t", clean), func(t *testing.T) {
			raw := "<style>noise</style><script>noise</script>\nnoise > quote\n-----\n" +
				"<b>alpha</b> " + strings.Repeat("loose ", 30) + "beta.\n" +
				strings.Repeat("padding.\n", 100) + "alpha beta tight cluster. OB1,2NEXT &amp;\n"
			content := raw
			if clean {
				content = CleanContent(raw)
			}
			query := compileQuery([]string{"alpha", "beta"}, PartialModeOff)
			spans := extractRankedExcerptSpansQuery(content, query, 2, 2, 2, false, 5000)
			if len(spans) != 2 || !strings.Contains(spans[0].text, "tight cluster") {
				t.Fatalf("expected tight then loose excerpts: %#v", spans)
			}
			offsets := requestedDocumentOffsets(spans)
			mapped := documentCorrespondence(raw, clean, offsets)
			if mapped != spans[0].sourceText {
				t.Fatal("correspondence must reproduce the actual excerpt cleaner")
			}
			for _, match := range spans[0].sourceMatches {
				locs := query.matchers[match.term].findAll(raw)
				if got := offsets[match.source]; got != locs[match.occurrence-1].start {
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

func TestExcerptLinesDocumentRemovedOccurrence(t *testing.T) {
	for _, removed := range []string{"<script>alpha</script>"} {
		for _, clean := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/clean=%t", removed, clean), func(t *testing.T) {
				raw := removed + "\n" + strings.Repeat("padding.\n", 100) +
					"alpha first.\n" + strings.Repeat("padding.\n", 100) + "alpha second.\n"
				content := raw
				if clean {
					content = CleanContent(raw)
				}
				query := compileQuery([]string{"alpha"}, PartialModeOff)
				spans := extractRankedExcerptSpansQuery(content, query, 2, 1, 1, false, 200)
				if len(spans) != 2 {
					t.Fatalf("expected two surviving excerpts: %#v", spans)
				}
				slices.SortFunc(spans, func(a, b excerptSpan) int { return a.left - b.left })
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{102, 203}) {
					t.Fatalf("surviving anchors = %v, want [102 203]", got)
				}
				// Exact identities must also work in reverse ranking order.
				slices.Reverse(spans)
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{203, 102}) {
					t.Fatalf("reverse anchors = %v, want [203 102]", got)
				}
			})
		}
	}
}

func TestExcerptLinesDocumentInvalidMatchStart(t *testing.T) {
	raw := "<b>noise</b>\nALPHA2\nalpha\n"
	query := compileQuery([]string{"alpha"}, PartialModeOff)
	mapped := documentCorrespondence(raw, true, nil)
	start := strings.Index(mapped, "ALPHA")
	offsets := sourceOffsets{byteRange(start + 5): 0}
	documentCorrespondence(raw, true, offsets)
	if offsets[byteRange(start+5)] != -1 {
		t.Fatal("fixture must insert a synthetic space after ALPHA")
	}
	for _, tc := range []struct {
		name             string
		start, end, want int
	}{
		{"retained word", start, start + 5, 2},
		{"negative start", -1, 4, 0}, // Fallback treats -1 as absent metadata.
		{"past end", len(mapped), len(mapped) + 5, 3},
		{"synthetic start", start + 5, start + 6, 3},
		{"synthetic interior", start, start + 7, 3},
		{"empty match", start, start, 3},
		{"reversed range", start, start - 1, 3},
		{"end out of bounds", start, len(mapped) + 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match := excerptMatch{term: 0, source: matchRange{start: tc.start, end: tc.end}, occurrence: 1}
			span := excerptSpan{sourceText: mapped, sourceMatches: []excerptMatch{match}, matches: []excerptMatch{match}}
			if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{span}, query, 200); !slices.Equal(got, []int{tc.want}) {
				t.Fatalf("anchors = %v, want [%d]", got, tc.want)
			}
		})
	}
}

func TestExcerptLinesDocumentCleaningInducedWordBoundary(t *testing.T) {
	// Lowercase identifiers remain unchanged; OCR splitting is uppercase-only.
	if got := CleanContent("2alpha"); got != "2alpha" {
		t.Fatalf("lowercase identifier = %q, want 2alpha", got)
	}
	for _, term := range []string{"ALPHA2", "2ALPHA", "--alpha--"} {
		for _, later := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later=%t", term, later), func(t *testing.T) {
				raw := "<b>noise</b>\n" + term + "\n"
				want := []int{2}
				if later {
					raw += strings.Repeat("padding.\n", 100) + "alpha\n"
					want = append(want, 103)
				}
				query := compileQuery([]string{"alpha"}, PartialModeOff)
				if !later && term != "--alpha--" && len(query.matchers[0].findAll(raw)) != 0 {
					t.Fatal("fixture must have no raw whole-word matches")
				}
				spans := extractRankedExcerptSpansQuery(CleanContent(raw), query, 2, 1, 1, false, 200)
				if len(spans) != len(want) {
					t.Fatalf("got %d excerpts, want %d", len(spans), len(want))
				}
				slices.SortFunc(spans, func(a, b excerptSpan) int { return a.left - b.left })
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, want) {
					t.Fatalf("anchors = %v, want %v", got, want)
				}
				slices.Reverse(spans)
				slices.Reverse(want)
				if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, want) {
					t.Fatalf("reverse anchors = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestExcerptLinesDocumentNoncontiguousMatch(t *testing.T) {
	// Removing a script joins retained bytes without inserting a synthetic byte.
	raw := "<b>noise</b>\nal<script>noise</script>pha\nalpha\n"
	query := compileQuery([]string{"alpha"}, PartialModeOff)
	mapped := documentCorrespondence(raw, true, nil)
	start := strings.Index(mapped, "alpha")
	offsets := sourceOffsets{byteRange(start): 0, byteRange(start + 2): 0}
	documentCorrespondence(raw, true, offsets)
	if offsets[byteRange(start+2)] < 0 || offsets[byteRange(start+2)] == offsets[byteRange(start)]+2 {
		t.Fatal("fixture must join noncontiguous retained bytes")
	}
	match := excerptMatch{term: 0, source: matchRange{start: start, end: start + 5}, occurrence: 1}
	span := excerptSpan{sourceText: mapped, sourceMatches: []excerptMatch{match}, matches: []excerptMatch{match}}
	if got := computeExcerptSpanLinesQuery(raw, []excerptSpan{span}, query, 200); !slices.Equal(got, []int{3}) {
		t.Fatalf("noncontiguous fallback = %v, want [3]", got)
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

func BenchmarkExcerptLinesLargeDocument(b *testing.B) {
	for _, size := range []int{1 << 16, 1 << 20} {
		for _, clean := range []bool{true, false} {
			b.Run(fmt.Sprintf("bytes=%d/clean=%t", size, clean), func(b *testing.B) {
				raw := "<b>header</b>\n" + strings.Repeat("x", size) + "\nALPHA2 needle\n"
				content := raw
				if clean {
					content = CleanContent(raw)
				}
				query := compileQuery([]string{"needle"}, PartialModeOff)
				spans := extractRankedExcerptSpansQuery(content, query, 1, 1, 1, false, 200)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if got := computeExcerptSpanLinesQuery(raw, spans, query, 200); !slices.Equal(got, []int{3}) {
						b.Fatalf("anchors = %v, want [3]", got)
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

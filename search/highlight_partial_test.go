package search

import "testing"

func TestHighlightTermsPartial(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	tests := []struct {
		name  string
		text  string
		terms []string
		mode  PartialMode
		want  string
	}{
		{
			name:  "prefix preserves underscore boundary",
			text:  "Production _deployment active",
			terms: []string{"deploy"},
			mode:  PartialModePrefix,
			want:  "Production _" + hi + "deployment" + nc + " active",
		},
		{
			name:  "glob expands prefix while off",
			text:  "Production deploy* active",
			terms: []string{"deploy*"},
			mode:  PartialModeOff,
			want:  "Production " + hi + "deploy" + nc + "* active",
		},
		{
			name:  "contains highlights internal substring",
			text:  "redeploy now",
			terms: []string{"deploy"},
			mode:  PartialModeContains,
			want:  "re" + hi + "deploy" + nc + " now",
		},
		{
			name:  "prefix rejects internal substring",
			text:  "redeploy now",
			terms: []string{"deploy"},
			mode:  PartialModePrefix,
			want:  "redeploy now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HighlightTermsPartial(tt.text, tt.terms, tt.mode); got != tt.want {
				t.Errorf("HighlightTermsPartial() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaseSensitiveHighlightTerms(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"
	got := HighlightTermsWithOptions(
		"API api APIs",
		[]string{"API"},
		PartialModeOff,
		QueryOptions{CaseSensitive: true},
	)
	if want := hi + "API" + nc + " api APIs"; got != want {
		t.Fatalf("highlight = %q, want %q", got, want)
	}
}

func TestInlineWildcardHighlightUsesExactRanges(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	tests := []struct {
		name string
		text string
		term string
		want string
	}{
		{
			name: "internal star",
			text: "call ms-option-api now",
			term: "ms-*-api",
			want: "call " + hi + "ms-option-api" + nc + " now",
		},
		{
			name: "leading and trailing stars",
			text: "use MyOptionApiClient",
			term: "*OptionApi*",
			want: "use My" + hi + "OptionApi" + nc + "Client",
		},
		{
			name: "single byte question",
			text: "call ms-x-api now",
			term: "ms-?-api",
			want: "call " + hi + "ms-x-api" + nc + " now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HighlightTermsPartial(tt.text, []string{tt.term}, PartialModeOff); got != tt.want {
				t.Fatalf("HighlightTermsPartial() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHighlightTermsExactRangeRegressions(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	if got, want := HighlightTerms("..", []string{"."}), hi+"."+nc+hi+"."+nc; got != want {
		t.Fatalf("adjacent punctuation highlight = %q, want %q", got, want)
	}

	invalid := string([]byte{0xff})
	replacement := "\uFFFD"
	text := invalid + replacement
	if got, want := HighlightTerms(text, []string{invalid}), hi+invalid+nc+replacement; got != want {
		t.Fatalf("invalid UTF-8 highlight bytes = %q, want %q", got, want)
	}
	if got, want := HighlightTerms(text, []string{replacement}), invalid+hi+replacement+nc; got != want {
		t.Fatalf("U+FFFD highlight bytes = %q, want %q", got, want)
	}
}

func TestHighlightTermsMergesOverlappingRanges(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	got := HighlightTermsPartial("foobar", []string{"foob", "obar"}, PartialModeContains)
	if want := hi + "foobar" + nc; got != want {
		t.Fatalf("overlapping highlight = %q, want %q", got, want)
	}
}

func TestExcerptHighlightDoesNotCreateBoundaryAtSliceStart(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	span := excerptSpan{
		text: "api then api",
		matches: []excerptMatch{{
			matchRange: matchRange{start: 9, end: 12},
			term:       0,
		}},
	}
	if got, want := highlightExcerptSpan(span), "api then "+hi+"api"+nc; got != want {
		t.Fatalf("excerpt highlight = %q, want %q", got, want)
	}
}

func TestExtractedExcerptHighlightsOnlyFullTextMatches(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	query := compileQuery([]string{"api"}, PartialModeOff)
	spans := extractRankedExcerptSpansQuery("myapi then api", query, 1, 1, 0, true, unconstrainedDistance)
	if len(spans) != 1 {
		t.Fatalf("excerpt spans = %#v, want one", spans)
	}
	if got, want := highlightExcerptSpan(spans[0]), "myapi then "+hi+"api"+nc; got != want {
		t.Fatalf("extracted excerpt highlight = %q, want %q", got, want)
	}
}

package search

import (
	"reflect"
	"strings"
	"testing"
)

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

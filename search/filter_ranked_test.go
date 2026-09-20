package search_test

import (
	"strings"
	"testing"

	"garp/search"
)

func TestCheckTextContainsRankedWords(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		words     []string
		distance  int
		strict    bool
		wantScore int
		wantCount int
		wantTerms []string
		wantSpan  int
		wantOK    bool
	}{
		{
			name:      "one term matches",
			text:      "alpha",
			words:     []string{"alpha"},
			distance:  20,
			wantScore: 5,
			wantCount: 1,
			wantTerms: []string{"alpha"},
			wantSpan:  5,
			wantOK:    true,
		},
		{
			name:     "one term missing",
			text:     "beta",
			words:    []string{"alpha"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:      "two terms require conjunction within distance",
			text:      "alpha beta",
			words:     []string{"alpha", "beta"},
			distance:  20,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
		{
			name:     "two terms outside distance fail",
			text:     "alpha " + strings.Repeat("x", 30) + " beta",
			words:    []string{"alpha", "beta"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:     "two terms with one missing fail",
			text:     "alpha",
			words:    []string{"alpha", "beta"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:      "relaxed three terms accepts primary and secondary",
			text:      "alpha beta",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  20,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
		{
			name:      "relaxed three terms favors long secondary",
			text:      "alpha synchronization",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 20,
			wantCount: 2,
			wantTerms: []string{"alpha", "synchronization"},
			wantSpan:  21,
			wantOK:    true,
		},
		{
			name:     "relaxed three terms rejects missing primary",
			text:     "beta synchronization",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			wantOK:   false,
		},
		{
			name:     "relaxed three terms rejects primary alone",
			text:     "alpha",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			wantOK:   false,
		},
		{
			name:      "relaxed three terms accepts all terms",
			text:      "alpha beta synchronization",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 24,
			wantCount: 3,
			wantTerms: []string{"alpha", "beta", "synchronization"},
			wantSpan:  26,
			wantOK:    true,
		},
		{
			name:     "strict mode requires every term",
			text:     "alpha beta",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			strict:   true,
			wantOK:   false,
		},
		{
			name:      "distinct terms are not inflated by repeats",
			text:      "alpha beta beta beta",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, count, terms, span, ok := search.CheckTextContainsRankedWords(tt.text, tt.words, tt.distance, tt.strict)
			if score != tt.wantScore || count != tt.wantCount || span != tt.wantSpan || ok != tt.wantOK {
				t.Fatalf("CheckTextContainsRankedWords() = (%d, %d, %v, %d, %v), want (%d, %d, %v, %d, %v)", score, count, terms, span, ok, tt.wantScore, tt.wantCount, tt.wantTerms, tt.wantSpan, tt.wantOK)
			}
			if strings.Join(terms, "\x00") != strings.Join(tt.wantTerms, "\x00") {
				t.Fatalf("matched terms = %v, want %v", terms, tt.wantTerms)
			}
		})
	}
}

func TestCheckTextContainsRankedWordsChoosesBestWindow(t *testing.T) {
	t.Run("rarity weighting beats a denser short-term cluster", func(t *testing.T) {
		text := "term go to " + strings.Repeat("x", 40) + " term synchronization"
		score, count, terms, span, ok := search.CheckTextContainsRankedWords(text, []string{"term", "synchronization", "go", "to"}, 30, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 19 || count != 2 || strings.Join(terms, ",") != "term,synchronization" || span != 20 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (19, 2, [term synchronization], 20)", score, count, terms, span)
		}
	})

	t.Run("exhaustive scan favors later higher scoring cluster", func(t *testing.T) {
		text := "term " + strings.Repeat("x", 190) + " go " + strings.Repeat("y", 210) + " term synchronization beta"
		score, count, terms, span, ok := search.CheckTextContainsRankedWords(text, []string{"term", "synchronization", "beta", "go"}, 200, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 23 || count != 3 || strings.Join(terms, ",") != "term,synchronization,beta" || span != 25 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (23, 3, [term synchronization beta], 25)", score, count, terms, span)
		}
	})

	t.Run("equal score and term count favor tighter span", func(t *testing.T) {
		text := "alpha " + strings.Repeat("x", 90) + " beta alpha beta"
		score, count, terms, span, ok := search.CheckTextContainsRankedWords(text, []string{"alpha", "beta"}, 200, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 9 || count != 2 || strings.Join(terms, ",") != "alpha,beta" || span != 10 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (9, 2, [alpha beta], 10)", score, count, terms, span)
		}
	})
}

func TestCheckTextContainsAllWordsDelegatesToStrictRankedMatching(t *testing.T) {
	if !search.CheckTextContainsAllWords("alpha beta", []string{"alpha", "beta"}, 20) {
		t.Fatal("strict compatibility wrapper rejected a valid two-term match")
	}
	if search.CheckTextContainsAllWords("alpha beta", []string{"alpha", "beta", "gamma"}, 20) {
		t.Fatal("strict compatibility wrapper accepted a partial three-term match")
	}
}

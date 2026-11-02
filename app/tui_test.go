package app

import (
	"strings"
	"testing"

	"garp/search"
)

func TestTUIFormatting(t *testing.T) {
	lastExcerptInnerWidth = 0
	lastContentHeight = 0

	m := model{
		results: []search.SearchResult{{
			FilePath:     "relaxed.txt",
			FileSize:     1024,
			Score:        10,
			TermCount:    2,
			MatchedTerms: []string{"query", "mutex"},
			Excerpts:     []string{"query and mutex appear together."},
			CleanContent: "query and mutex appear together. phantom synchronization context must not be appended.",
		}},
		width:           200,
		height:          60,
		searchWords:     []string{"query", "synchronization", "mutex"},
		totalFiles:      1,
		confirmSelected: "yes",
	}

	view := m.View()
	for _, want := range []string{
		"Score: 10 (2/3 terms)",
		"Result [ 1 / 1 ] (Score 10, 2/3 terms) -- Continue?",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("TUI view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "phantom ") {
		t.Errorf("TUI augmented excerpt with unmatched query term:\n%s", view)
	}
}

func TestTUIPartialExcerptDoesNotAugmentPresentTerm(t *testing.T) {
	lastExcerptInnerWidth = 0
	lastContentHeight = 0

	m := model{
		results: []search.SearchResult{{
			FilePath:     "partial.txt",
			FileSize:     1024,
			Score:        6,
			TermCount:    1,
			MatchedTerms: []string{"deploy"},
			Excerpts:     []string{"_deployment is active."},
			CleanContent: "_deployment is active. phantom deploy context must not be appended.",
		}},
		width:           200,
		height:          60,
		searchWords:     []string{"deploy"},
		partial:         search.PartialModePrefix,
		totalFiles:      1,
		confirmSelected: "yes",
	}

	if view := m.View(); strings.Contains(view, "phantom deploy") {
		t.Errorf("TUI augmented excerpt despite partial term already being present:\n%s", view)
	}
}

func TestTUIPropagatesMaxResultsAndExcerpts(t *testing.T) {
	m := model{
		searchWords:       []string{"needle"},
		heavyConcurrency:  1,
		fileTimeoutBinary: 1000,
		filterWorkers:     1,
		maxResults:        17,
		maxExcerpts:       4,
	}

	engine := m.newSearchEngine()
	if engine.MaxResults != 17 {
		t.Errorf("MaxResults = %d, want 17", engine.MaxResults)
	}
	if engine.MaxExcerpts != 4 {
		t.Errorf("MaxExcerpts = %d, want 4", engine.MaxExcerpts)
	}
}

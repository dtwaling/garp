package app

import (
	"reflect"
	"strings"
	"testing"

	"garp/search"
)

func TestTUIORGroupRendering(t *testing.T) {
	lastExcerptInnerWidth, lastContentHeight = 0, 0
	m := model{
		results: []search.SearchResult{{
			FilePath: "groups.txt", Score: 2, TermCount: 2,
			MatchedTerms: []string{"ignore", "walk"},
			Excerpts:     []string{"prune ignore walk"},
		}},
		searchWords:  []string{"prune|ignore", "walk"},
		excludeWords: []string{".pdf|.docx", "mock|stub"},
		width:        200, height: 60,
	}
	view := m.View()
	for _, want := range []string{
		"Score: 2 (2/2 terms)", "Result [ 1 / 1 ] (Score 2, 2/2 terms)",
		"\x1b[1;31mprune\x1b[0m", "\x1b[1;31mignore\x1b[0m", "\x1b[1;31mwalk\x1b[0m",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q: %s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Searching:") && (!strings.Contains(line, "mock|stub") || strings.Contains(line, "pdf")) {
			t.Errorf("incorrect word exclusions: %s", line)
		}
		if strings.Contains(line, "Target:") && (!strings.Contains(line, "pdf, docx") || strings.Contains(line, "mock")) {
			t.Errorf("incorrect extension exclusions: %s", line)
		}
	}
}

func TestTUILiteralPipeExcerptPresence(t *testing.T) {
	clipped := "ms-" + strings.Repeat("a", 40)
	for _, tc := range []struct{ term, excerpt, content string }{
		{"alpha|beta", "alpha|beta is active.", "alpha|beta is active. phantom alpha|beta tail context must not be appended."},
		{"ms-*-api|v2", clipped, clipped + "-api|v2 phantom tail context must not be appended."},
	} {
		t.Run(tc.term, func(t *testing.T) {
			for _, named := range []bool{false, true} {
				lastExcerptInnerWidth, lastContentHeight = 0, 0
				end := len(tc.excerpt)
				if tc.term == "alpha|beta" {
					end = len(tc.term)
				}
				r := search.MatchRange{Start: 0, End: end}
				if named {
					r.Term = tc.term
				}
				result := search.SearchResult{
					FilePath: "literal.txt", TermCount: 2,
					MatchedTerms:   []string{tc.term, "omega"},
					RawExcerpts:    []string{tc.excerpt + " omega"},
					CleanContent:   tc.content + " omega",
					ExcerptMatches: [][]search.MatchRange{{r, {Start: len(tc.excerpt) + 1, End: len(tc.excerpt) + 6, Term: "omega"}}},
				}
				if !excerptContainsTerm(result.RawExcerpts[0], result.ExcerptMatches[0], true, result, tc.term, search.PartialModeOff, search.QueryOptions{}) {
					t.Errorf("named=%t: validated literal-pipe range not recognized", named)
				}
				m := model{results: []search.SearchResult{result}, searchWords: []string{search.EscapeLiteralTerm(tc.term), "omega"}, width: 200, height: 60}
				view := m.View()
				want := "\x1b[1;31m" + tc.excerpt[:end] + "\x1b[0m"
				if strings.Count(view, want) != 1 || strings.Contains(view, "phantom") {
					t.Errorf("named=%t: expected one highlight and no augmentation: %s", named, view)
				}
			}
		})
	}
}

func TestTUILiteralPipeAugmentation(t *testing.T) {
	lastExcerptInnerWidth, lastContentHeight = 0, 0
	result := search.SearchResult{
		FilePath: "literal.txt", TermCount: 2,
		MatchedTerms: []string{"anchor", "alpha|beta"},
		RawExcerpts:  []string{"anchor"},
		CleanContent: "anchor standalone alpha " + strings.Repeat("padding ", 40) + "alpha|beta target",
	}
	for _, excerpt := range []string{"alpha", "alpha|beta"} {
		want := excerpt == "alpha|beta"
		if got := excerptContainsTerm(excerpt, nil, false, result, "alpha|beta", search.PartialModeOff, search.QueryOptions{}); got != want {
			t.Errorf("unvalidated excerpt %q: presence = %t, want %t", excerpt, got, want)
		}
	}
	m := model{results: []search.SearchResult{result}, searchWords: []string{"anchor", `alpha\|beta`}, width: 200, height: 60}
	view := m.View()
	if !strings.Contains(view, "\x1b[1;31malpha|beta\x1b[0m target") || strings.Contains(view, "standalone") {
		t.Errorf("augmentation did not select the literal-pipe window: %s", view)
	}
	for _, tc := range []struct {
		rangeTerm       string
		sensitive, want bool
	}{
		{"alpha*beta|v2", false, true},
		{"Alpha*Beta|v2", true, true},
		{"alpha*beta|v2", true, false},
	} {
		if got := matchRangeMatchesTerm(tc.rangeTerm, "Alpha**Beta|v2", search.QueryOptions{CaseSensitive: tc.sensitive}); got != tc.want {
			t.Errorf("matchRangeMatchesTerm(%q, sensitive=%t) = %t", tc.rangeTerm, tc.sensitive, got)
		}
	}
}

func TestTUIFormatsFractionalScore(t *testing.T) {
	lastExcerptInnerWidth, lastContentHeight = 0, 0
	m := model{results: []search.SearchResult{{FilePath: "rare.txt", Score: 4.0328, TermCount: 1, Excerpts: []string{"rare"}}}, width: 200, height: 60, searchWords: []string{"rare", "missing", "absent"}, totalFiles: 1, confirmSelected: "yes"}
	for _, want := range []string{"Score: 4.0328 (1/3 terms)", "Result [ 1 / 1 ] (Score 4.0328, 1/3 terms)"} {
		if view := m.View(); !strings.Contains(view, want) {
			t.Errorf("missing %q: %s", want, view)
		}
	}
}

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
		pdfScanned:      3,
		pdfTruncated:    2,
		timedOut:        1,
		confirmSelected: "yes",
	}

	view := m.View()
	for _, want := range []string{
		"Score: 10 (2/3 terms)",
		"PDFs Scanned 3 • Truncated 2 • Timed out 1",
		"Result [ 1 / 1 ] (Score 10, 2/3 terms) -- Continue?",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("TUI view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "phantom ") {
		t.Errorf("TUI augmented excerpt with unmatched query term:\n%s", view)
	}
	m.timedOut = 0
	if strings.Contains(m.View(), "Timed out") {
		t.Error("TUI shows timeout suffix with zero timeouts")
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

func TestTUITargetDescriptionShowsOrderedTypeUnion(t *testing.T) {
	lastExcerptInnerWidth = 0
	lastContentHeight = 0

	m := model{
		width:       200,
		height:      60,
		loading:     true,
		searchWords: []string{"needle"},
		onlyType:    "pdf",
		onlyTypes:   []string{"pdf", "md", "dockerfile"},
	}
	const want = "only .pdf, .md, .dockerfile"
	if got := m.targetDescription(); got != want {
		t.Fatalf("targetDescription() = %q, want %q", got, want)
	}
	if view := m.View(); !strings.Contains(view, want) {
		t.Fatalf("TUI target header missing %q:\n%s", want, view)
	}

	singular := model{onlyType: "pdf"}
	if got := singular.targetDescription(); got != "only .pdf" {
		t.Fatalf("singular targetDescription() = %q", got)
	}
}

func TestTUIPropagatesMaxResultsAndExcerpts(t *testing.T) {
	root := t.TempDir()
	args := parseArguments([]string{
		"needle",
		"--startdir", root,
		"--pathscope", "docs",
		"--no-ignore",
		"--max-filesize", "1MiB",
		"--max-results", "17",
		"--max-excerpts", "4",
	})
	m := newTUIModel(args)

	engine := m.newSearchEngine()
	if engine.MaxResults != 17 {
		t.Errorf("MaxResults = %d, want 17", engine.MaxResults)
	}
	if engine.MaxExcerpts != 4 {
		t.Errorf("MaxExcerpts = %d, want 4", engine.MaxExcerpts)
	}
	if engine.StartDir != root || !slicesEqual(engine.PathScope, []string{"docs"}) {
		t.Errorf("search scope = %q / %v, want %q / [docs]", engine.StartDir, engine.PathScope, root)
	}
	if !engine.NoIgnore || engine.MaxFileSize != 1024*1024 {
		t.Errorf("walk options = no-ignore %t, max-file-size %d; want true, 1048576", engine.NoIgnore, engine.MaxFileSize)
	}
}

func TestTUIEnginePropagation(t *testing.T) {
	root := t.TempDir()
	args := parseArguments([]string{
		"Needle", "--startdir", root, "-t", "md,pdf",
		"--glob", "docs/**", "--iglob", "*.MD",
		"-s", "--smart-forms",
	})
	roots, err := search.PrepareStartRootsWithOptions(args.StartDirs, search.StartRootOptions{
		PathScope: args.PathScope, UserGlobs: args.UserGlobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	args.StartRoots = roots
	m := newTUIModel(args)
	engine := m.newSearchEngine()
	if !reflect.DeepEqual(m.startRoots, roots) ||
		!reflect.DeepEqual(engine.StartRoots, roots) ||
		!reflect.DeepEqual(engine.UserGlobs, args.UserGlobs) ||
		!engine.CaseSensitive || !engine.SmartForms ||
		!slicesEqual(engine.FileTypes, buildFileTypes(false, "md", []string{"md", "pdf"})) {
		t.Fatalf("TUI engine did not preserve arguments: %#v", engine)
	}
}

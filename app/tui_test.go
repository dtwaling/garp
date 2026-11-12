package app

import (
	"reflect"
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
		pdfScanned:      3,
		pdfTruncated:    2,
		confirmSelected: "yes",
	}

	view := m.View()
	for _, want := range []string{
		"Score: 10 (2/3 terms)",
		"PDFs Scanned 3 • Truncated 2",
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

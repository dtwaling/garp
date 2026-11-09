package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestCLIArgumentsPartial(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		partial search.PartialMode
		wantErr bool
	}{
		{
			name:    "default is off",
			argv:    []string{"deploy"},
			partial: search.PartialModeOff,
		},
		{
			name:    "bare flag enables prefix",
			argv:    []string{"deploy", "--partial"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "prefix mode",
			argv:    []string{"deploy", "--partial=prefix"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "contains mode",
			argv:    []string{"deploy", "--partial=contains"},
			partial: search.PartialModeContains,
		},
		{
			name:    "off mode",
			argv:    []string{"deploy", "--partial=off"},
			partial: search.PartialModeOff,
		},
		{
			name:    "invalid mode reports validation error",
			argv:    []string{"deploy", "--partial=fuzzy"},
			wantErr: true,
		},
		{
			name:    "combines with other flags",
			argv:    []string{"deploy", "--partial=prefix", "--code", "--distance", "200", "--json"},
			partial: search.PartialModePrefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if tt.wantErr {
				if args.PartialErr == nil {
					t.Fatal("PartialErr = nil, want validation error")
				}
				return
			}
			if args.PartialErr != nil {
				t.Fatalf("PartialErr = %v, want nil", args.PartialErr)
			}
			if args.Partial != tt.partial {
				t.Errorf("Partial = %q, want %q", args.Partial, tt.partial)
			}
		})
	}
}

func TestCLIArgumentsCanonicalTerms(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "service.md")

	args := parseArguments([]string{
		"ms-**-api",
		"MS-*-API",
		"--startdir", root,
		"--only", "md",
	})
	if !slicesEqual(args.SearchWords, []string{"ms-*-api"}) {
		t.Fatalf("SearchWords = %q, want [ms-*-api]", args.SearchWords)
	}

	engine := newSilentSearchEngine(args)
	if !slicesEqual(engine.SearchWords, []string{"ms-*-api"}) {
		t.Fatalf("engine SearchWords = %q, want [ms-*-api]", engine.SearchWords)
	}

	out := captureCLIStdout(t, func() int { return runJSON(args) })
	var result jsonOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if !slicesEqual(result.Query.Terms, []string{"ms-*-api"}) {
		t.Errorf("JSON query terms = %q, want [ms-*-api]", result.Query.Terms)
	}

	lastExcerptInnerWidth = 0
	lastContentHeight = 0
	view := (model{
		results: []search.SearchResult{{
			FilePath:     fixture,
			Score:        len("ms-*-api"),
			TermCount:    1,
			MatchedTerms: []string{"ms-*-api"},
			Excerpts:     []string{"ms-option-api"},
		}},
		width:           200,
		height:          60,
		searchWords:     args.SearchWords,
		totalFiles:      1,
		confirmSelected: "yes",
	}).View()
	if !strings.Contains(view, "(1/1 terms)") {
		t.Errorf("TUI did not use canonical term denominator:\n%s", view)
	}
}

func TestRunPlainPartialConfiguresSearchEngine(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "deployment.md")
	if err := os.WriteFile(fixture, []byte("deployment is active"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := parseArguments([]string{
		"deploy",
		"--partial=prefix",
		"--startdir", root,
		"--only", "md",
		"--plain",
	})
	out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
	if !strings.Contains(out, "FILE: "+escapeOutputLine(fixture)) {
		t.Fatalf("partial search did not return derivative-form fixture:\n%s", out)
	}
}

func TestShowUsageDocumentsPartial(t *testing.T) {
	out := string(captureCLIStdout(t, func() int {
		showUsage()
		return 0
	}))
	for _, want := range []string{
		"--partial[=MODE]",
		"prefix, contains",
		"Tip: valid query terms support inline * and ?.",
		"Invalid or over-limit wildcard terms match literally. See README for bounds.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
}

func TestShowUsageDocumentsBoundedOverlapExcerpts(t *testing.T) {
	out := string(captureCLIStdout(t, func() int {
		showUsage()
		return 0
	}))
	for _, want := range []string{"up to 10% overlap", ">=90% new content"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
}

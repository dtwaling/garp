package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgumentsMaxResults(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		want     int
		provided bool
		wantErr  bool
	}{
		{name: "positive", argv: []string{"needle", "--max-results", "12"}, want: 12, provided: true},
		{name: "explicit zero", argv: []string{"needle", "--max-results", "0"}, provided: true},
		{name: "missing value", argv: []string{"needle", "--max-results"}, provided: true, wantErr: true},
		{name: "negative value", argv: []string{"needle", "--max-results", "-1"}, provided: true, wantErr: true},
		{name: "non-integer value", argv: []string{"needle", "--max-results", "many"}, provided: true, wantErr: true},
		{name: "latest repeated value wins", argv: []string{"needle", "--max-results", "3", "--max-results", "7"}, want: 7, provided: true},
		{name: "latest valid value clears prior error", argv: []string{"needle", "--max-results", "bad", "--max-results", "5"}, want: 5, provided: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if args.MaxResults != tt.want {
				t.Errorf("MaxResults = %d, want %d", args.MaxResults, tt.want)
			}
			if args.MaxResultsProvided != tt.provided {
				t.Errorf("MaxResultsProvided = %t, want %t", args.MaxResultsProvided, tt.provided)
			}
			if (args.MaxResultsErr != nil) != tt.wantErr {
				t.Fatalf("MaxResultsErr = %v, want error %t", args.MaxResultsErr, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(args.MaxResultsErr.Error(), "--max-results") {
				t.Errorf("MaxResultsErr = %q, want --max-results", args.MaxResultsErr)
			}
			if len(args.SearchWords) != 1 || args.SearchWords[0] != "needle" {
				t.Errorf("SearchWords = %q, max-results value was not consumed", args.SearchWords)
			}
		})
	}
}

func TestRunRejectsInvalidMaxResults(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing", args: []string{"garp", "needle", "--startdir", missingRoot, "--max-results"}},
		{name: "negative", args: []string{"garp", "needle", "--max-results", "-1", "--startdir", missingRoot}},
		{name: "non-integer", args: []string{"garp", "needle", "--max-results", "many", "--startdir", missingRoot}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := runWithArgsAndStderr(t, tt.args)
			if code != 1 {
				t.Errorf("Run() = %d, want 1", code)
			}
			if !strings.Contains(stderr, "--max-results") {
				t.Errorf("stderr = %q, want --max-results error", stderr)
			}
			if strings.Contains(stderr, "invalid search root") {
				t.Errorf("stderr = %q, search-root validation ran before max-results validation", stderr)
			}
		})
	}
}

func TestMaxResultsDefaults(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want int
	}{
		{name: "JSON default", argv: []string{"needle", "--json"}, want: 50},
		{name: "plain default", argv: []string{"needle", "--plain"}, want: 50},
		{name: "path-only default", argv: []string{"needle", "--pathonly"}, want: 0},
		{name: "TUI default", argv: []string{"needle"}, want: 0},
		{name: "explicit JSON override", argv: []string{"needle", "--json", "--max-results", "9"}, want: 9},
		{name: "explicit plain unlimited", argv: []string{"needle", "--plain", "--max-results", "0"}, want: 0},
		{name: "path-only precedence", argv: []string{"needle", "--json", "--pathonly"}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if got := resolveMaxResults(args); got != tt.want {
				t.Errorf("resolveMaxResults() = %d, want %d", got, tt.want)
			}
			if got := newSilentSearchEngine(args).MaxResults; got != tt.want {
				t.Errorf("SearchEngine.MaxResults = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShowUsageDocumentsMaxResults(t *testing.T) {
	out := string(captureCLIStdout(t, func() int {
		showUsage()
		return 0
	}))
	for _, want := range []string{
		"[--max-excerpts N] [--max-results N]",
		"0 = unlimited",
		"--json 50",
		"--plain 50",
		"--pathonly 0",
		"TUI 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
	if maxResults, excludes := strings.Index(out, "[--max-results N]"), strings.Index(out, "[--not <exclude1>"); maxResults < 0 || excludes < 0 || maxResults >= excludes {
		t.Errorf("--max-results must precede the trailing --not list:\n%s", out)
	}
}

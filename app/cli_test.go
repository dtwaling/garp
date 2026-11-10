package app

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"garp/config"
	"garp/search"
)

func TestConcurrencyFlagBounds(t *testing.T) {
	for _, tc := range []struct {
		flag    string
		maximum int
		get     func(*Arguments) int
	}{
		{"--workers", search.MaxWorkers, func(a *Arguments) int { return a.FilterWorkers }},
		{"-workers", search.MaxWorkers, func(a *Arguments) int { return a.FilterWorkers }},
		{"--heavy-concurrency", search.MaxHeavyConcurrency, func(a *Arguments) int { return a.HeavyConcurrency }},
		{"--file-timeout-binary", search.MaxFileTimeoutBinary, func(a *Arguments) int { return a.FileTimeoutBinary }},
	} {
		for _, value := range []int{-1, 0, 1, tc.maximum / 2, tc.maximum, tc.maximum + 1, math.MaxInt} {
			for _, attached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/attached=%v", tc.flag, value, attached), func(t *testing.T) {
					args := []string{tc.flag, strconv.Itoa(value), "alpha"}
					if attached {
						args = []string{tc.flag + "=" + strconv.Itoa(value), "alpha"}
					}
					got := parseArguments(args)
					if value < 1 || value > tc.maximum {
						if got.ParseErr == nil || !strings.Contains(got.ParseErr.Error(), tc.flag) {
							t.Fatalf("expected flag-specific error, got %v", got.ParseErr)
						}
						if value > tc.maximum && !strings.Contains(got.ParseErr.Error(), fmt.Sprintf("at most %d", tc.maximum)) {
							t.Fatalf("missing upper bound: %v", got.ParseErr)
						}
						if tc.get(got) > tc.maximum {
							t.Fatalf("invalid value retained: %d", tc.get(got))
						}
					} else if got.ParseErr != nil || tc.get(got) != value {
						t.Fatalf("valid value: got %d, error %v; want %d", tc.get(got), got.ParseErr, value)
					}
				})
			}
		}
	}
}

func TestConcurrencyFlagDefaults(t *testing.T) {
	got := parseArguments([]string{"alpha"})
	if got.ParseErr != nil || got.FilterWorkers != 4 || got.FileTimeoutBinary != 1000 || got.HeavyConcurrency < 1 || got.HeavyConcurrency > search.MaxHeavyConcurrency {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestCLIDefaultTypesAndGlobCodeEligibility(t *testing.T) {
	if got := len(config.BuildRipgrepFileTypes(false)) / 2; got != 28 {
		t.Fatalf("default document glob count = %d, want 28", got)
	}
	root := t.TempDir()
	writeCLISearchFixture(t, root, "guide.htm", "needle")
	writeCLISearchFixture(t, root, "src/main.go", "needle")
	writeCLISearchFixture(t, root, "src/logo.png", "needle")
	writeCLISearchFixture(t, root, "vendor/hidden.go", "needle")
	writeCLISearchFixture(t, root, "vendor/hidden.htm", "needle")

	defaultResults := runCLIParsedSearch(t, []string{"needle", "--startdir", root, "--pathonly"})
	defaultNames := cliResultBasenames(defaultResults)
	if len(defaultResults) != 1 || !defaultNames["guide.htm"] ||
		defaultNames["main.go"] || defaultNames["logo.png"] ||
		defaultNames["hidden.go"] || defaultNames["hidden.htm"] {
		t.Fatalf("default discovery admitted code, image, or hard-skipped files: %#v", defaultResults)
	}
	globResults := runCLIParsedSearch(t, []string{
		"needle", "--startdir", root, "-g", "*.go", "--pathonly",
	})
	got := cliResultBasenames(globResults)
	if len(got) != 1 || !got["main.go"] {
		t.Fatalf("positive code-glob results = %#v", globResults)
	}
}

func TestCLIPathScopeAndGlobIntersection(t *testing.T) {
	root := t.TempDir()
	writeCLISearchFixture(t, root, "src/main.go", "needle")
	writeCLISearchFixture(t, root, "src/main.php", "needle")
	writeCLISearchFixture(t, root, "src/generated/skip.go", "needle")
	writeCLISearchFixture(t, root, "other/main.go", "needle")
	results := runCLIParsedSearch(t, []string{
		"needle", "--startdir", root, "--pathscope", "src/",
		"-g", "*.go", "-g", "!src/generated/**", "--pathonly",
	})
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "main.go" ||
		filepath.Base(filepath.Dir(results[0].FilePath)) != "src" {
		t.Fatalf("scope/glob results = %#v", results)
	}
}

func TestCLIMultiRootJSONTraversalAndReporting(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "z-root")
	second := filepath.Join(base, "a-root")
	writeCLISearchFixture(t, first, "same.txt", "needle")
	writeCLISearchFixture(t, second, "same.txt", "needle")
	out := captureCLIRunJSON(t, []string{
		"garp", "needle", "--startdir", first + "," + second, "--only", "txt", "--json",
	})
	if out.Query.StartDir != first || !slicesEqual(out.Query.StartDirs, []string{first, second}) {
		t.Fatalf("multi-root JSON = %#v", out.Query)
	}
	if len(out.Results) != 2 || !strings.HasPrefix(out.Results[0].File, first) {
		t.Fatalf("global ranking results = %#v", out.Results)
	}

	parent := filepath.Join(base, "parent")
	child := filepath.Join(parent, "blocked", "child")
	writeCLISearchFixture(t, child, "retained.txt", "needle")
	nestedOut := captureCLIRunJSON(t, []string{
		"garp", "needle", "--startdir", parent + "," + child,
		"--pathscope", "!blocked/**", "--only", "txt", "--json",
	})
	if len(nestedOut.Results) != 1 || nestedOut.Query.StartDir != parent ||
		nestedOut.Query.StartDirs != nil ||
		!strings.HasPrefix(nestedOut.Results[0].File, parent) {
		t.Fatalf("nested divergence output = %#v", nestedOut)
	}

	workingDir := t.TempDir()
	writeCLISearchFixture(t, workingDir, "default.txt", "needle")
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	omitted := captureCLIRunJSON(t, []string{"garp", "needle", "--only", "txt", "--json"})
	raw, err := json.Marshal(omitted.Query)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "start_dir") || len(omitted.Results) != 1 {
		t.Fatalf("default-root JSON output = %s / %#v", raw, omitted.Results)
	}
}

func TestCLICaseSensitiveSmartForms(t *testing.T) {
	root := t.TempDir()
	writeCLISearchFixture(t, root, "exact.txt", "DeployING")
	writeCLISearchFixture(t, root, "folded.txt", "deploying")
	results := runCLIParsedSearch(t, []string{
		"Deploy", "--startdir", root, "--only", "txt",
		"-s", "--smart-forms", "--pathonly",
	})
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "exact.txt" {
		t.Fatalf("case-sensitive smart-form results = %#v", results)
	}
	query := buildJSONOutput(
		parseArguments([]string{"Deploy", "-s"}),
		search.ExecutionResult{},
	).Query
	if !query.CaseSensitive || query.Terms[0] != "Deploy" {
		t.Fatalf("case-sensitive JSON = %#v", query)
	}
}

func runCLIParsedSearch(t *testing.T, argv []string) []search.SearchResult {
	t.Helper()
	args := parseArguments(argv)
	if args.ParseErr != nil {
		t.Fatalf("parse arguments: %v", args.ParseErr)
	}
	if err := prepareArgumentStartRoots(args); err != nil {
		t.Fatalf("prepare roots: %v", err)
	}
	results, err := newSilentSearchEngine(args).Execute()
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return results
}

func writeCLISearchFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cliResultBasenames(results []search.SearchResult) map[string]bool {
	got := make(map[string]bool, len(results))
	for _, result := range results {
		got[filepath.Base(result.FilePath)] = true
	}
	return got
}

func TestCLIRejectsUnsupportedFlagsAfterExclusions(t *testing.T) {
	for _, token := range []string{"-C", "--context=3", "-A2", "-B2", "--bogus"} {
		if got := parseArguments([]string{"needle", "--not", token}); got.ParseErr == nil {
			t.Errorf("%q after --not was accepted", token)
		}
	}
}

func captureCLIRunJSON(t *testing.T, argv []string) jsonOutput {
	t.Helper()
	originalArgs := os.Args
	os.Args = argv
	defer func() { os.Args = originalArgs }()

	out := captureCLIStdout(t, Run)
	var decoded jsonOutput
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode Run JSON output: %v\n%s", err, out)
	}
	return decoded
}

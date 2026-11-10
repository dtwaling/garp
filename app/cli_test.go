package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
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

func TestNoIgnoreAndMaxFileSizePropagateToSearchEngines(t *testing.T) {
	root := t.TempDir()
	scopedDir := filepath.Join(root, "scoped")
	otherDir := filepath.Join(root, "other")
	for _, dir := range []string{scopedDir, otherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedDir, "keep.md"), []byte("assembly"), 0o644); err != nil {
		t.Fatalf("write keep.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedDir, "ignored.md"), []byte("assembly"), 0o644); err != nil {
		t.Fatalf("write ignored.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedDir, "oversize.md"), []byte(strings.Repeat("a", 2048)), 0o644); err != nil {
		t.Fatalf("write oversize.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "out.md"), []byte("assembly"), 0o644); err != nil {
		t.Fatalf("write out.md: %v", err)
	}

	args := parseArguments([]string{
		"assembly",
		"--startdir", root,
		"--pathscope", "scoped/",
		"--only", "md",
		"--no-ignore",
		"--max-filesize", "1KiB",
	})
	if args.MaxFileSizeErr != nil {
		t.Fatalf("MaxFileSizeErr = %v, want nil", args.MaxFileSizeErr)
	}

	silent := newSilentSearchEngine(args)
	if !silent.NoIgnore || silent.MaxFileSize != 1024 || silent.StartDir != root || len(silent.PathScope) != 1 {
		t.Errorf("silent engine options = NoIgnore %v, MaxFileSize %d, StartDir %q, PathScope %v", silent.NoIgnore, silent.MaxFileSize, silent.StartDir, silent.PathScope)
	}

	m := model{
		searchWords:       args.SearchWords,
		excludeWords:      args.ExcludeWords,
		includeCode:       args.IncludeCode,
		onlyType:          args.OnlyType,
		heavyConcurrency:  args.HeavyConcurrency,
		fileTimeoutBinary: args.FileTimeoutBinary,
		filterWorkers:     args.FilterWorkers,
		startDir:          args.StartDir,
		pathScope:         args.PathScope,
		noIgnore:          args.NoIgnore,
		maxFileSize:       args.MaxFileSize,
	}
	tui := m.newSearchEngine()
	if !tui.NoIgnore || tui.MaxFileSize != 1024 || tui.StartDir != root || len(tui.PathScope) != 1 {
		t.Errorf("TUI engine options = NoIgnore %v, MaxFileSize %d, StartDir %q, PathScope %v", tui.NoIgnore, tui.MaxFileSize, tui.StartDir, tui.PathScope)
	}

	opts := walkOptions(args)
	if !opts.NoIgnore || opts.MaxFileSize != 1024 {
		t.Errorf("walkOptions = %+v, want NoIgnore=true MaxFileSize=1024", opts)
	}

	cliCount, err := preflightFileCount(args)
	if err != nil {
		t.Fatalf("preflightFileCount() error = %v", err)
	}
	tuiCount, err := m.preflightFileCount()
	if err != nil {
		t.Fatalf("m.preflightFileCount() error = %v", err)
	}
	results, err := silent.Execute()
	if err != nil {
		t.Fatalf("silent.Execute() error = %v", err)
	}
	if cliCount != 2 || tuiCount != 2 || len(results) != 2 {
		t.Errorf("no-ignore preflight/TUI/results = %d/%d/%d, want 2/2/2", cliCount, tuiCount, len(results))
	}

	defaultIgnoreArgs := parseArguments([]string{
		"assembly",
		"--startdir", root,
		"--pathscope", "scoped/",
		"--only", "md",
		"--max-filesize", "1KiB",
	})
	m.noIgnore = false
	defaultCLICount, err := preflightFileCount(defaultIgnoreArgs)
	if err != nil {
		t.Fatalf("default preflightFileCount() error = %v", err)
	}
	defaultTUICount, err := m.preflightFileCount()
	if err != nil {
		t.Fatalf("default m.preflightFileCount() error = %v", err)
	}
	defaultResults, err := newSilentSearchEngine(defaultIgnoreArgs).Execute()
	if err != nil {
		t.Fatalf("default silent.Execute() error = %v", err)
	}
	if defaultCLICount != 1 || defaultTUICount != 1 || len(defaultResults) != 1 {
		t.Errorf("default-ignore preflight/TUI/results = %d/%d/%d, want 1/1/1", defaultCLICount, defaultTUICount, len(defaultResults))
	}
}

func TestLegacyOnlyTypeDisablesPositiveGlobCodeEligibility(t *testing.T) {
	globs := []search.UserGlob{{Pattern: "*.go"}}
	args := &Arguments{
		SearchWords:       []string{"anchor"},
		OnlyType:          "md",
		UserGlobs:         globs,
		HeavyConcurrency:  1,
		FilterWorkers:     1,
		FileTimeoutBinary: 1000,
	}
	silent := newSilentSearchEngine(args)
	if silent.PositiveGlobCodeEligibility {
		t.Fatal("legacy Arguments.OnlyType enabled positive-glob code eligibility")
	}
	if walkOptions(args).PositiveGlobCodeEligibility {
		t.Fatal("legacy Arguments.OnlyType enabled preflight code eligibility")
	}

	tuiModel := model{
		searchWords:       []string{"anchor"},
		onlyType:          "md",
		userGlobs:         globs,
		heavyConcurrency:  1,
		filterWorkers:     1,
		fileTimeoutBinary: 1000,
	}
	tui := tuiModel.newSearchEngine()
	if tui.PositiveGlobCodeEligibility {
		t.Fatal("legacy model.onlyType enabled positive-glob code eligibility")
	}
	if positiveGlobCodeEligibility(tuiModel.onlyType, tuiModel.onlyTypes) {
		t.Fatal("legacy model.onlyType enabled TUI preflight code eligibility")
	}
}

func TestShowUsageDocumentsFlagsAndCompatibilityLimits(t *testing.T) {
	out := string(captureCLIStdout(t, func() int {
		showUsage()
		return 0
	}))
	for _, want := range []string{
		"[--startdir PATH]",
		"[--no-ignore]",
		"[--max-filesize SIZE]",
		"--no-ignore            Do not apply repository ignore files",
		"--max-filesize SIZE    Maximum file size",
		"-l, --files-with-matches",
		"-m, --max-count N",
		"not an rg line-count limit",
		"-t, --type, --only TYPE",
		"-g, --glob PATTERN",
		"--iglob PATTERN",
		"-i, --ignore-case",
		"-s, --case-sensitive",
		"Comma-separated search roots; specify this flag once",
		"Comma-separated positive scopes form a union",
		"Use repeatable -g for additional path globs",
		"Bare name matches basenames at any depth",
		"name/, ./name, and patterns containing / are root-anchored",
		"* and ? stay within one segment; ** crosses segments",
		"Use ./src for explicit root-relative matching",
		"Absolute pathscope and user-glob patterns must overlap a search root",
		"Examples: POSIX /repo/src/**; Windows C:/repo/src/**",
		"A pattern can select inside a root or cover it as an ancestor",
		"On Windows, a non-absolute leading slash is root-relative",
		"If no root intersects it, validation fails before search",
		"Prefix with ! to exclude a path",
		"Pathscope intersects with positive --glob and --iglob patterns",
		"both --flag VALUE and --flag=VALUE",
		"Stop flag parsing",
		"context flags are not supported",
		"not a drop-in rg replacement",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
	defaultWorkers := parseArguments([]string{"needle"}).FilterWorkers
	if defaultWorkers != 4 {
		t.Fatalf("parser default workers = %d, want 4", defaultWorkers)
	}
	workersHelp := fmt.Sprintf(
		"--workers N             Stage 2 text filter workers (default %d)",
		defaultWorkers,
	)
	if !strings.Contains(out, workersHelp) {
		t.Errorf("usage worker default differs from parser default; missing %q:\n%s", workersHelp, out)
	}
}

func TestRunJSON_PathScopeUsesPlatformSeparators(t *testing.T) {
	root := t.TempDir()
	buildDir := filepath.Join(root, "doc", "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(buildDir, "deployment.md"),
		[]byte("The approved parent AMI produces the shared base AMI."),
		0o644,
	); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := parseArguments([]string{
		"ami", "parent", "base",
		"--startdir", root,
		"--pathscope", `doc\`,
		"--only", "md",
		"--distance", "200",
		"--json",
	})

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	code := runJSON(args)
	_ = writer.Close()
	os.Stdout = originalStdout
	defer reader.Close()

	if code != 0 {
		t.Fatalf("runJSON() exit code = %d, want 0", code)
	}
	var out jsonOutput
	if err := json.NewDecoder(reader).Decode(&out); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	wantScope := filepath.FromSlash("doc/")
	if len(out.Query.PathScope) != 1 || out.Query.PathScope[0] != wantScope {
		t.Errorf("JSON path_scope = %v, want [%q]", out.Query.PathScope, wantScope)
	}
	if out.Matches != 1 {
		t.Errorf("JSON matches = %d, want 1", out.Matches)
	}
}

func TestRunRejectsInvalidStartDirBeforeEveryMode(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write file root fixture: %v", err)
	}

	roots := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name: "missing",
			setup: func(t *testing.T) string {
				return filepath.Join(base, "missing")
			},
		},
		{
			name: "file",
			setup: func(t *testing.T) string {
				return file
			},
		},
		{
			name: "broken-link",
			setup: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "broken-link")
				if err := os.Symlink(filepath.Join(base, "missing-target"), link); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				return link
			},
		},
		{
			name: "file-target-link",
			setup: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "file-link")
				if err := os.Symlink(file, link); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				return link
			},
		},
	}

	modes := []struct {
		name string
		flag string
	}{
		{name: "JSON", flag: "--json"},
		{name: "plain", flag: "--plain"},
		{name: "path-only", flag: "--pathonly"},
		{name: "TUI"},
	}
	for _, rootCase := range roots {
		t.Run(rootCase.name, func(t *testing.T) {
			target := rootCase.setup(t)
			for _, mode := range modes {
				t.Run(mode.name, func(t *testing.T) {
					argv := []string{"garp", "needle", "--startdir", target}
					if mode.flag != "" {
						argv = append(argv, mode.flag)
					}
					code, stderr := runWithArgsAndStderr(t, argv)
					if code != 1 {
						t.Errorf("Run() exit code = %d, want 1", code)
					}
					if !strings.Contains(stderr, "invalid search root") {
						t.Errorf("stderr = %q, want invalid search root error", stderr)
					}
				})
			}
		})
	}
}

func TestRunRejectsAbsolutePolicyOutsideAllStartDirs(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "*.txt")
	code, stderr := runWithArgsAndStderr(t, []string{
		"garp", "needle", "--json",
		"--startdir", root,
		"--pathscope", outside,
	})
	if code != 1 {
		t.Fatalf("Run() exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "outside every search root") {
		t.Fatalf("stderr = %q, want containment error", stderr)
	}
}

func TestStartDirJSONPreservesRelativeReportedRoot(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "c", "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)

	for _, test := range []struct {
		name      string
		raw       string
		want      string
		pathScope string
	}{
		{name: "single relative root", raw: filepath.Join("workspace", "."), want: "workspace"},
		{name: "child then parent", raw: "workspace/child,workspace", want: "workspace"},
		{name: "parent then child", raw: "workspace,workspace/child", want: "workspace"},
		{name: "exemption parent then child", raw: "workspace,workspace/c", want: "workspace", pathScope: "ignored"},
		{name: "exemption child then parent", raw: "workspace/c,workspace", want: "workspace", pathScope: "ignored"},
		{
			name:      "three-level child order",
			raw:       "workspace/c/ignored,workspace/c,workspace",
			want:      "workspace",
			pathScope: "ignored,!zzz",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalArgs := os.Args
			os.Args = []string{"garp", "missing-needle", "--json", "--startdir", test.raw}
			if test.pathScope != "" {
				os.Args = append(os.Args, "--pathscope", test.pathScope)
			}
			defer func() { os.Args = originalArgs }()

			output := captureCLIStdout(t, Run)
			var decoded jsonOutput
			if err := json.Unmarshal(output, &decoded); err != nil {
				t.Fatalf("decode JSON: %v\n%s", err, output)
			}
			if decoded.Query.StartDir != filepath.Clean(test.want) {
				t.Fatalf("query.start_dir = %q, want relative %q", decoded.Query.StartDir, filepath.Clean(test.want))
			}
		})
	}
}

func TestRunRejectsMaxFileSizeErrorBeforeEveryMode(t *testing.T) {
	modes := []string{"--json", "--plain", "--pathonly", ""}
	for _, mode := range modes {
		name := mode
		if name == "" {
			name = "TUI"
		}
		t.Run(name, func(t *testing.T) {
			argv := []string{"garp", "needle"}
			if mode != "" {
				argv = append(argv, mode)
			}
			argv = append(argv, "--max-filesize", "0")
			code, stderr := runWithArgsAndStderr(t, argv)
			if code != 1 {
				t.Errorf("Run() exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr, "--max-filesize") {
				t.Errorf("stderr = %q, want max-filesize error", stderr)
			}
		})
	}
}

func TestStartDirPrepareStartRootsRejectsInvalidRoots(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write file root fixture: %v", err)
	}

	for name, path := range map[string]string{
		"missing": filepath.Join(root, "missing"),
		"file":    file,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := search.PrepareStartRoots([]string{path}, false); err == nil {
				t.Fatalf("PrepareStartRoots(%q) error = nil, want rejection", path)
			}
		})
	}

	t.Run("broken link", func(t *testing.T) {
		link := filepath.Join(root, "broken-link")
		if err := os.Symlink(filepath.Join(root, "missing-target"), link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if _, err := search.PrepareStartRoots([]string{link}, false); err == nil {
			t.Fatal("PrepareStartRoots(broken link) error = nil, want rejection")
		}
	})

	t.Run("file-target link", func(t *testing.T) {
		link := filepath.Join(root, "file-target-link")
		if err := os.Symlink(file, link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if _, err := search.PrepareStartRoots([]string{link}, false); err == nil {
			t.Fatal("PrepareStartRoots(file-target link) error = nil, want rejection")
		}
	})

	t.Run("unreadable directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod does not reliably remove directory read access on Windows")
		}
		unreadable := filepath.Join(root, "unreadable")
		if err := os.Mkdir(unreadable, 0o700); err != nil {
			t.Fatalf("create unreadable directory: %v", err)
		}
		if err := os.Chmod(unreadable, 0); err != nil {
			t.Fatalf("remove directory permissions: %v", err)
		}
		defer os.Chmod(unreadable, 0o700)
		if _, err := os.ReadDir(unreadable); err == nil {
			t.Skip("current user can still read permission-free directory")
		}
		if _, err := search.PrepareStartRoots([]string{unreadable}, false); err == nil {
			t.Fatal("PrepareStartRoots(unreadable directory) error = nil, want rejection")
		}
	})
}

func runWithArgsAndStderr(t *testing.T, argv []string) (int, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	originalArgs := os.Args
	originalStderr := os.Stderr
	os.Args = argv
	os.Stderr = writer
	defer func() {
		os.Args = originalArgs
		os.Stderr = originalStderr
	}()

	code := Run()
	_ = writer.Close()
	out, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return code, string(out)
}

func TestPreflightExtensionExclusionsCLIAndTUI(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"skip.TXT", "skip.EmL", "keep.md", ".venv/hidden.md", "venv/hidden.md"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("anchor forbidden"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, noIgnore := range []bool{false, true} {
		name := "ignore"
		if noIgnore {
			name = "no-ignore"
		}
		t.Run(name, func(t *testing.T) {
			argv := []string{"anchor", "--startdir", root, "--not", ".tXt", ".EML", "forbidden"}
			if noIgnore {
				argv = append(argv, "--no-ignore")
			}
			args := parseArguments(argv)
			if args.ParseErr != nil {
				t.Fatal(args.ParseErr)
			}
			m := model{searchWords: args.SearchWords, excludeWords: args.ExcludeWords, startDir: root, noIgnore: noIgnore}
			cliCount, err := preflightFileCount(args)
			if err != nil || cliCount != 1 {
				t.Fatalf("CLI count = %d, %v; want 1", cliCount, err)
			}
			tuiCount, err := m.preflightFileCount()
			if err != nil || tuiCount != 1 {
				t.Fatalf("TUI count = %d, %v; want 1", tuiCount, err)
			}
			for _, engine := range []struct {
				name     string
				discover func(int) ([]string, int, error)
			}{
				{"CLI", newSilentSearchEngine(args).DiscoverCandidates},
				{"TUI", m.newSearchEngine().DiscoverCandidates},
			} {
				paths, total, err := engine.discover(1)
				if err != nil || total != 1 || len(paths) != 1 || paths[0] != filepath.Join(root, "keep.md") {
					t.Fatalf("%s discovery = %v, %d, %v; want keep.md only", engine.name, paths, total, err)
				}
			}
		})
	}
}

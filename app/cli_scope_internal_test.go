package app

// cli_scope_internal_test.go: white-box tests for --startdir and --pathscope
// CLI argument parsing. Uses package app (not app_test) to access unexported
// parseArguments directly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestParseArguments_StartDir_Accepted(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/project", "bar"})
	expected := filepath.Clean("/home/user/project")
	if args.StartDir != expected {
		t.Errorf("expected StartDir=%q, got %q", expected, args.StartDir)
	}
	if len(args.SearchWords) != 2 {
		t.Errorf("expected 2 search words, got %v", args.SearchWords)
	}
}

func TestParseArguments_StartDir_NormalizesTrailingSlash(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/project/", "bar"})
	// filepath.Clean should remove trailing slash
	if args.StartDir == "/home/user/project/" {
		t.Errorf("expected trailing slash to be cleaned, got %q", args.StartDir)
	}
}

func TestParseArguments_StartDir_RejectsWildcard(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/proj*"})
	// Invalid startdir should not be stored; StartDirErr should be set
	if args.StartDirErr == nil {
		t.Error("expected StartDirErr to be set for wildcard path, got nil")
	}
}

func TestParseArguments_StartDir_RejectsSemicolon(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home;bad"})
	if args.StartDirErr == nil {
		t.Error("expected StartDirErr to be set for semicolon injection, got nil")
	}
}

func TestParseArguments_StartDir_NotProvided(t *testing.T) {
	args := parseArguments([]string{"foo", "bar"})
	if args.StartDir != "" {
		t.Errorf("expected empty StartDir when not provided, got %q", args.StartDir)
	}
	if args.StartDirErr != nil {
		t.Errorf("expected nil StartDirErr when not provided, got %v", args.StartDirErr)
	}
}

func TestParseArguments_PathScope_Accepted(t *testing.T) {
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/*,tests/*"})
	if len(args.PathScope) != 2 {
		t.Errorf("expected 2 path scope segments, got %v", args.PathScope)
	}
	if args.PathScope[0] != "*/backend/*" {
		t.Errorf("expected first segment '*/backend/*', got %q", args.PathScope[0])
	}
	if args.PathScope[1] != "tests/*" {
		t.Errorf("expected second segment 'tests/*', got %q", args.PathScope[1])
	}
}

func TestParseArguments_PathScope_RejectsExtension(t *testing.T) {
	// Dots are valid in pathscope -- treated as part of dir/filename.
	// garp always runs; CLI help warns users to use --not/--only instead.
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/*.cs"})
	if args.PathScopeErr != nil {
		t.Errorf("unexpected PathScopeErr for dotted segment: %v", args.PathScopeErr)
	}
	if len(args.PathScope) != 1 {
		t.Errorf("expected 1 scope segment, got %v", args.PathScope)
	}
}

func TestParseArguments_PathScope_RejectsRegex(t *testing.T) {
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/[Aa]ssembly"})
	if args.PathScopeErr == nil {
		t.Error("expected PathScopeErr for regex bracket, got nil")
	}
}

func TestParseArguments_PathScope_NotProvided(t *testing.T) {
	args := parseArguments([]string{"foo", "bar"})
	if len(args.PathScope) != 0 {
		t.Errorf("expected empty PathScope when not provided, got %v", args.PathScope)
	}
	if args.PathScopeErr != nil {
		t.Errorf("expected nil PathScopeErr when not provided, got %v", args.PathScopeErr)
	}
}

func TestParseArguments_BothFlags_WorkTogether(t *testing.T) {
	args := parseArguments([]string{
		"assembly", "main",
		"--startdir", "/home/user/project",
		"--pathscope", "*/backend/*/Assembly",
		"--code",
	})
	expectedStartDir := filepath.Clean("/home/user/project")
	if args.StartDir != expectedStartDir {
		t.Errorf("unexpected StartDir: %q", args.StartDir)
	}
	if len(args.PathScope) != 1 || args.PathScope[0] != "*/backend/*/Assembly" {
		t.Errorf("unexpected PathScope: %v", args.PathScope)
	}
	if !args.IncludeCode {
		t.Error("expected IncludeCode=true")
	}
	if len(args.SearchWords) != 2 {
		t.Errorf("expected 2 search words, got %v", args.SearchWords)
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
		"repeat or use comma-separated paths",
		"Comma-separated positive scopes form a union",
		"* and ? stay within one segment; ** crosses segments",
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

func TestParseArguments_StartDir_WithEquals(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir=/home/user/project"})
	if args.ParseErr != nil {
		t.Fatalf("ParseErr = %v, want nil", args.ParseErr)
	}
	if want := filepath.Clean("/home/user/project"); args.StartDir != want {
		t.Errorf("StartDir = %q, want %q", args.StartDir, want)
	}
}

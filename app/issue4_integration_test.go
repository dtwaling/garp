package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/config"
	"garp/search"
)

func TestIssue4Acceptance(t *testing.T) {
	t.Run("AC-1 value forms and strict validation", func(t *testing.T) {
		space := parseArguments([]string{
			"Needle", "--distance", "200", "--max-excerpts", "3",
			"--only", "md", "--workers", "2", "--partial=none",
		})
		equals := parseArguments([]string{
			"Needle", "--distance=200", "--max-excerpts=3",
			"--only=md", "--workers=2", "--partial=off",
		})
		if space.ParseErr != nil || equals.ParseErr != nil ||
			space.Distance != equals.Distance ||
			space.MaxExcerpts != equals.MaxExcerpts ||
			space.OnlyType != equals.OnlyType ||
			space.FilterWorkers != equals.FilterWorkers ||
			space.Partial != equals.Partial {
			t.Fatalf("space/equals forms differ: %#v / %#v", space, equals)
		}
		for _, argv := range [][]string{
			{"needle", "--only="},
			{"needle", "--partial=wrong"},
			{"needle", "--code=true"},
			{"needle", "--distance", "-l"},
		} {
			if got := parseArguments(argv); got.ParseErr == nil {
				t.Errorf("parseArguments(%q) accepted invalid input", argv)
			}
		}
	})

	t.Run("AC-2 aliases", func(t *testing.T) {
		args := parseArguments([]string{"needle", "-l", "-m", "99"})
		if args.ParseErr != nil || !args.PathOnly || args.MaxExcerpts != 50 {
			t.Fatalf("alias parse = %#v", args)
		}
		for _, value := range []string{"0", "bad"} {
			if got := parseArguments([]string{"needle", "--max-count", value}); got.ParseErr == nil {
				t.Errorf("--max-count %s was accepted", value)
			}
		}
	})

	t.Run("AC-3 type union and JSON compatibility", func(t *testing.T) {
		args := parseArguments([]string{"needle", "-t", ".PHP,md", "--type=php"})
		out := buildJSONOutput(args, search.ExecutionResult{})
		if args.ParseErr != nil || out.Query.OnlyType != "php" ||
			!slicesEqual(out.Query.OnlyTypes, []string{"php", "md"}) {
			t.Fatalf("type JSON = %#v", out.Query)
		}
		for _, value := range []string{"php,,md", "*.php"} {
			if got := parseArguments([]string{"needle", "--only", value}); got.ParseErr == nil {
				t.Errorf("--only %q was accepted", value)
			}
		}
	})

	t.Run("AC-4 defaults glob code eligibility and hard skips", func(t *testing.T) {
		if got := len(config.BuildRipgrepFileTypes(false)) / 2; got != 28 {
			t.Fatalf("default document glob count = %d, want 28", got)
		}
		root := t.TempDir()
		writeIssue4Fixture(t, root, "guide.htm", "needle")
		writeIssue4Fixture(t, root, "src/main.go", "needle")
		writeIssue4Fixture(t, root, "src/logo.png", "needle")
		writeIssue4Fixture(t, root, "vendor/hidden.go", "needle")
		writeIssue4Fixture(t, root, "vendor/hidden.htm", "needle")

		defaultResults := runIssue4Search(t, []string{"needle", "--startdir", root, "--pathonly"})
		defaultNames := issue4Basenames(defaultResults)
		if len(defaultResults) != 1 || !defaultNames["guide.htm"] ||
			defaultNames["main.go"] || defaultNames["logo.png"] ||
			defaultNames["hidden.go"] || defaultNames["hidden.htm"] {
			t.Fatalf("default discovery admitted code, image, or hard-skipped files: %#v", defaultResults)
		}
		globResults := runIssue4Search(t, []string{
			"needle", "--startdir", root, "-g", "*.go", "--pathonly",
		})
		got := issue4Basenames(globResults)
		if len(got) != 1 || !got["main.go"] {
			t.Fatalf("positive code-glob results = %#v", globResults)
		}
	})

	t.Run("AC-5 path and glob policy intersection", func(t *testing.T) {
		root := t.TempDir()
		writeIssue4Fixture(t, root, "src/main.go", "needle")
		writeIssue4Fixture(t, root, "src/main.php", "needle")
		writeIssue4Fixture(t, root, "src/generated/skip.go", "needle")
		writeIssue4Fixture(t, root, "other/main.go", "needle")
		results := runIssue4Search(t, []string{
			"needle", "--startdir", root, "--pathscope", "src/",
			"-g", "*.go", "-g", "!src/generated/**", "--pathonly",
		})
		if len(results) != 1 || filepath.Base(results[0].FilePath) != "main.go" ||
			filepath.Base(filepath.Dir(results[0].FilePath)) != "src" {
			t.Fatalf("scope/glob results = %#v", results)
		}
	})

	t.Run("AC-6 multi-root JSON and nested no-data-loss divergence", func(t *testing.T) {
		base := t.TempDir()
		first := filepath.Join(base, "z-root")
		second := filepath.Join(base, "a-root")
		writeIssue4Fixture(t, first, "same.txt", "needle")
		writeIssue4Fixture(t, second, "same.txt", "needle")
		out := captureIssue4RunJSON(t, []string{
			"garp", "needle", "--startdir", first, "--startdir", second, "--only", "txt", "--json",
		})
		if out.Query.StartDir != first || !slicesEqual(out.Query.StartDirs, []string{first, second}) {
			t.Fatalf("multi-root JSON = %#v", out.Query)
		}
		if len(out.Results) != 2 || !strings.HasPrefix(out.Results[0].File, first) {
			t.Fatalf("global ranking results = %#v", out.Results)
		}

		parent := filepath.Join(base, "parent")
		child := filepath.Join(parent, "blocked", "child")
		writeIssue4Fixture(t, child, "retained.txt", "needle")
		nestedOut := captureIssue4RunJSON(t, []string{
			"garp", "needle", "--startdir", parent, "--startdir", child,
			"--pathscope", "!blocked/**", "--only", "txt", "--json",
		})
		if len(nestedOut.Results) != 1 || nestedOut.Query.StartDir != parent ||
			nestedOut.Query.StartDirs != nil ||
			!strings.HasPrefix(nestedOut.Results[0].File, parent) {
			t.Fatalf("nested divergence output = %#v", nestedOut)
		}

		workingDir := t.TempDir()
		writeIssue4Fixture(t, workingDir, "default.txt", "needle")
		originalDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(workingDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(originalDir) })
		omitted := captureIssue4RunJSON(t, []string{"garp", "needle", "--only", "txt", "--json"})
		raw, err := json.Marshal(omitted.Query)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "start_dir") || len(omitted.Results) != 1 {
			t.Fatalf("default-root JSON output = %s / %#v", raw, omitted.Results)
		}
	})

	t.Run("AC-7 case-sensitive smart forms", func(t *testing.T) {
		root := t.TempDir()
		writeIssue4Fixture(t, root, "exact.txt", "DeployING")
		writeIssue4Fixture(t, root, "folded.txt", "deploying")
		results := runIssue4Search(t, []string{
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
	})

	t.Run("AC-8 separator and fail-fast ordering", func(t *testing.T) {
		literals := parseArguments([]string{"needle", "--", "-v", "-l", "--context", "-C3"})
		if literals.ParseErr != nil ||
			!slicesEqual(literals.SearchWords, []string{"needle", "-v", "-l", "--context", "-c3"}) {
			t.Fatalf("separator parse = %#v", literals)
		}
		excludes := parseArguments([]string{"needle", "--not", "--", "-v", "-C3"})
		if excludes.ParseErr != nil || !slicesEqual(excludes.ExcludeWords, []string{"-v", "-C3"}) {
			t.Fatalf("exclude separator parse = %#v", excludes)
		}
		for _, token := range []string{"-C", "--context=3", "-A2", "-B2", "--bogus"} {
			got := parseArguments([]string{"needle", "--not", token})
			if got.ParseErr == nil {
				t.Errorf("%q after --not was accepted", token)
			}
		}

		missingRoot := filepath.Join(t.TempDir(), "missing")
		stderr, code := captureIssue4RunStderr(t, []string{
			"garp", "needle", "--glob", "../bad", "--startdir", missingRoot, "--json",
		})
		if code != 1 || !strings.Contains(stderr, "glob") || strings.Contains(stderr, "invalid search root") {
			t.Fatalf("fail-fast stderr/code = %q / %d", stderr, code)
		}
	})
}

func prepareAndRunIssue4(t *testing.T, argv []string) (*Arguments, []search.SearchResult) {
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
	return args, results
}

func runIssue4Search(t *testing.T, argv []string) []search.SearchResult {
	t.Helper()
	_, results := prepareAndRunIssue4(t, argv)
	return results
}

func writeIssue4Fixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func issue4Basenames(results []search.SearchResult) map[string]bool {
	got := make(map[string]bool, len(results))
	for _, result := range results {
		got[filepath.Base(result.FilePath)] = true
	}
	return got
}

func captureIssue4RunStderr(t *testing.T, argv []string) (string, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalArgs, originalStderr := os.Args, os.Stderr
	os.Args, os.Stderr = argv, writer
	code := Run()
	_ = writer.Close()
	os.Args, os.Stderr = originalArgs, originalStderr
	out, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	_ = reader.Close()
	return string(out), code
}

func captureIssue4RunJSON(t *testing.T, argv []string) jsonOutput {
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

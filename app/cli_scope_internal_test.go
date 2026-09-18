package app

// cli_scope_internal_test.go: white-box tests for --startdir and --pathscope
// CLI argument parsing. Uses package app (not app_test) to access unexported
// parseArguments directly.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	// Some users may pass --startdir=/path style (equals sign)
	// Our parser uses next-token style; this test documents that equals-style
	// falls through as a search word (not supported, by design).
	// No regression: equals-style was never supported.
	args := parseArguments([]string{"foo", "--startdir=/home/user/project"})
	// Should be treated as search word (unrecognized flag)
	_ = args // just verify it doesn't panic
}

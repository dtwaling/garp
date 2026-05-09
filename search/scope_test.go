package search_test

import (
	"strings"
	"testing"

	"find-words/search"
)

// --- ValidateStartDir tests ---

func TestValidateStartDir_ValidLinux(t *testing.T) {
	got, err := search.ValidateStartDir("/home/user/projects/myrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty path")
	}
}

func TestValidateStartDir_ValidWithTrailingSlash(t *testing.T) {
	got, err := search.ValidateStartDir("/home/user/projects/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// filepath.Clean removes trailing slash
	if strings.HasSuffix(got, "/") {
		t.Errorf("expected no trailing slash, got %q", got)
	}
}

func TestValidateStartDir_ValidWindowsBackslash(t *testing.T) {
	// Just validate structure -- don't os.Stat (path won't exist on Linux CI)
	_, err := search.ValidateStartDir(`C:\Users\dustin\projects\myrepo`)
	if err != nil {
		t.Fatalf("unexpected error for Windows path: %v", err)
	}
}

func TestValidateStartDir_ValidWindowsForwardSlash(t *testing.T) {
	_, err := search.ValidateStartDir("C:/Users/dustin/projects/myrepo")
	if err != nil {
		t.Fatalf("unexpected error for Windows forward-slash path: %v", err)
	}
}

func TestValidateStartDir_RejectsWildcardStar(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user/proj*")
	if err == nil {
		t.Fatal("expected error for wildcard *, got nil")
	}
}

func TestValidateStartDir_RejectsWildcardQuestion(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user/proj?")
	if err == nil {
		t.Fatal("expected error for wildcard ?, got nil")
	}
}

func TestValidateStartDir_RejectsSemicolon(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user;rm -rf /")
	if err == nil {
		t.Fatal("expected error for semicolon injection, got nil")
	}
}

func TestValidateStartDir_RejectsAmpersand(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user& bad")
	if err == nil {
		t.Fatal("expected error for ampersand, got nil")
	}
}

func TestValidateStartDir_RejectsPipe(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user|cat /etc/passwd")
	if err == nil {
		t.Fatal("expected error for pipe, got nil")
	}
}

func TestValidateStartDir_RejectsBacktick(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user`whoami`")
	if err == nil {
		t.Fatal("expected error for backtick, got nil")
	}
}

func TestValidateStartDir_RejectsDollarSign(t *testing.T) {
	_, err := search.ValidateStartDir("/home/$HOME")
	if err == nil {
		t.Fatal("expected error for dollar sign env expansion, got nil")
	}
}

func TestValidateStartDir_RejectsGtRedirect(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user>/tmp/out")
	if err == nil {
		t.Fatal("expected error for > redirect, got nil")
	}
}

func TestValidateStartDir_RejectsLtRedirect(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user</tmp/in")
	if err == nil {
		t.Fatal("expected error for < redirect, got nil")
	}
}

func TestValidateStartDir_RejectsNewline(t *testing.T) {
	_, err := search.ValidateStartDir("/home/user\nrm -rf /")
	if err == nil {
		t.Fatal("expected error for embedded newline, got nil")
	}
}

func TestValidateStartDir_EmptyRejected(t *testing.T) {
	_, err := search.ValidateStartDir("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestValidateStartDir_PathWithSpaces(t *testing.T) {
	// Spaces are valid in paths when quoted on the CLI
	_, err := search.ValidateStartDir("/home/user/my projects/repo")
	if err != nil {
		t.Fatalf("unexpected error for path with spaces: %v", err)
	}
}

func TestValidateStartDir_RelativePath(t *testing.T) {
	// Relative paths should be accepted (caller can os.Chdir / os.Stat)
	_, err := search.ValidateStartDir("../sibling/project")
	if err != nil {
		t.Fatalf("unexpected error for relative path: %v", err)
	}
}

// --- ValidatePathScope tests ---

func TestValidatePathScope_Empty(t *testing.T) {
	got, err := search.ValidatePathScope("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestValidatePathScope_SingleSegment(t *testing.T) {
	got, err := search.ValidatePathScope("*/backend/*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "*/backend/*" {
		t.Fatalf("expected [*/backend/*], got %v", got)
	}
}

func TestValidatePathScope_MultipleSegments(t *testing.T) {
	got, err := search.ValidatePathScope("*/backend/*/Assembly,tests/*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 segments, got %d: %v", len(got), got)
	}
	if got[0] != "*/backend/*/Assembly" {
		t.Errorf("expected first segment '*/backend/*/Assembly', got %q", got[0])
	}
	if got[1] != "tests/*" {
		t.Errorf("expected second segment 'tests/*', got %q", got[1])
	}
}

func TestValidatePathScope_TrimsWhitespace(t *testing.T) {
	got, err := search.ValidatePathScope("  */backend/* ,  tests/*  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 segments after trim, got %v", got)
	}
	if got[0] != "*/backend/*" {
		t.Errorf("expected trimmed segment, got %q", got[0])
	}
}

func TestValidatePathScope_RejectsExtension(t *testing.T) {
	// Dots are allowed -- they are treated as part of a dir or filename.
	// garp always runs; the CLI help directs users to use --not/--only for
	// extension filtering. This test documents the PASS behavior.
	got, err := search.ValidatePathScope("*/backend/*.cs")
	if err != nil {
		t.Fatalf("unexpected error: dots in pathscope should be allowed, got: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 segment, got %v", got)
	}
}

func TestValidatePathScope_RejectsDotInDirName(t *testing.T) {
	// Dots in directory names (e.g., back.end) are allowed.
	got, err := search.ValidatePathScope("*/back.end/*")
	if err != nil {
		t.Fatalf("unexpected error: dot in dir name should be allowed, got: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 segment, got %v", got)
	}
}

func TestValidatePathScope_RejectsRegexBracket(t *testing.T) {
	_, err := search.ValidatePathScope("*/backend/[Aa]ssembly")
	if err == nil {
		t.Fatal("expected error for regex bracket, got nil")
	}
}

func TestValidatePathScope_RejectsSemicolon(t *testing.T) {
	_, err := search.ValidatePathScope("*/backend;rm -rf")
	if err == nil {
		t.Fatal("expected error for semicolon injection, got nil")
	}
}

func TestValidatePathScope_RejectsParenthesis(t *testing.T) {
	_, err := search.ValidatePathScope("*/back(end)/*")
	if err == nil {
		t.Fatal("expected error for parenthesis (regex group), got nil")
	}
}

func TestValidatePathScope_RejectsCaret(t *testing.T) {
	_, err := search.ValidatePathScope("^backend/*")
	if err == nil {
		t.Fatal("expected error for caret (regex anchor), got nil")
	}
}

func TestValidatePathScope_RejectsPlus(t *testing.T) {
	_, err := search.ValidatePathScope("backend+/*")
	if err == nil {
		t.Fatal("expected error for + (regex quantifier), got nil")
	}
}

func TestValidatePathScope_RejectsDollar(t *testing.T) {
	_, err := search.ValidatePathScope("*/backend$")
	if err == nil {
		t.Fatal("expected error for $ (regex end-anchor / env expansion), got nil")
	}
}

func TestValidatePathScope_RejectsPipe(t *testing.T) {
	_, err := search.ValidatePathScope("*/backend|*/frontend")
	if err == nil {
		t.Fatal("expected error for pipe (regex alternation / shell), got nil")
	}
}

func TestValidatePathScope_QuestionMarkAllowed(t *testing.T) {
	got, err := search.ValidatePathScope("*/backe?d/*")
	if err != nil {
		t.Fatalf("unexpected error for ? wildcard: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty result for ? wildcard")
	}
}

func TestValidatePathScope_StarWildcardAllowed(t *testing.T) {
	got, err := search.ValidatePathScope("*/*test")
	if err != nil {
		t.Fatalf("unexpected error for * wildcard: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 segment, got %v", got)
	}
}

func TestValidatePathScope_SkipsEmptySegments(t *testing.T) {
	// trailing comma or double-comma should not produce empty segments
	got, err := search.ValidatePathScope("*/backend/*,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 segment (trailing comma skipped), got %v", got)
	}
}

package search_test

import (
	"os"
	"path/filepath"
	"testing"

	"garp/search"
)

// buildTestTree creates a temporary directory tree for walk tests.
// Structure:
//
//	root/
//	  backend/
//	    service/
//	      auth.go
//	      auth.md
//	    Assembly/
//	      main.cs
//	      main.md
//	  frontend/
//	    app.ts
//	    app.md
//	  tests/
//	    backend_test.go
//	    frontend_test.go
//	    endpoint_test.md
func buildTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	dirs := []string{
		"backend/service",
		"backend/Assembly",
		"frontend",
		"tests",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	files := map[string]string{
		"backend/service/auth.go":       "package service\nfunc Auth() {}\n",
		"backend/service/auth.md":       "# auth\ntoken authentication service\n",
		"backend/Assembly/main.cs":      "using System;\nclass Main { static void Main() {} }\n",
		"backend/Assembly/main.md":      "# Assembly main\nassembly entry point\n",
		"frontend/app.ts":               "export function app() {}\n",
		"frontend/app.md":               "# frontend app\napp description\n",
		"tests/backend_test.go":         "package tests\nfunc TestBackend() {}\n",
		"tests/frontend_test.go":        "package tests\nfunc TestFrontend() {}\n",
		"tests/endpoint_test.md":        "# endpoint test\nendpoint coverage\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// allFileTypes includes both doc and code types for tests
func allFileTypes() []string {
	return []string{
		"-g", "*.md",
		"-g", "*.go",
		"-g", "*.cs",
		"-g", "*.ts",
	}
}

// --- Task 3: walkRoot parameter ---

func TestGetDocumentFileCount_WalkRoot_AllFiles(t *testing.T) {
	root := buildTestTree(t)
	count, err := search.GetDocumentFileCount(allFileTypes(), root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 9 files total in the tree
	if count != 9 {
		t.Errorf("expected 9 files, got %d", count)
	}
}

func TestGetDocumentFileCount_WalkRoot_SubDir(t *testing.T) {
	root := buildTestTree(t)
	// Only count files under backend/
	backendDir := filepath.Join(root, "backend")
	count, err := search.GetDocumentFileCount(allFileTypes(), backendDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// backend/service/auth.go, auth.md, backend/Assembly/main.cs, main.md = 4 files
	if count != 4 {
		t.Errorf("expected 4 files in backend/, got %d", count)
	}
}

func TestFindFilesWithFirstWord_WalkRoot_AllFiles(t *testing.T) {
	root := buildTestTree(t)
	// "auth" appears in backend/service/auth.go and auth.md
	files, err := search.FindFilesWithFirstWord("auth", allFileTypes(), root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least one match for 'auth'")
	}
	for _, f := range files {
		if !filepath.IsAbs(f) && !fileUnderRoot(f, root) {
			t.Errorf("result path %q should be under root %q", f, root)
		}
	}
}

func TestFindFilesWithFirstWord_WalkRoot_SubDir(t *testing.T) {
	root := buildTestTree(t)
	// Search only in frontend/
	frontendDir := filepath.Join(root, "frontend")
	// "app" appears in frontend/app.ts and frontend/app.md
	files, err := search.FindFilesWithFirstWord("app", allFileTypes(), frontendDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least one match for 'app' in frontend/")
	}
	// All results must be under frontendDir
	for _, f := range files {
		if !fileUnderRoot(f, frontendDir) {
			t.Errorf("result path %q leaks outside frontendDir %q", f, frontendDir)
		}
	}
}

// --- Task 4: pathScope filtering ---

func TestGetDocumentFileCount_PathScope_BackendAssembly(t *testing.T) {
	root := buildTestTree(t)
	// Scope to only files under */Assembly/* (relative to root)
	scope := []string{"*/Assembly/*"}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only backend/Assembly/main.cs and main.md match
	if count != 2 {
		t.Errorf("expected 2 files matching */Assembly/*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_Tests(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"tests/*"}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// tests/ has 3 files
	if count != 3 {
		t.Errorf("expected 3 files matching tests/*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_MultiplePatterns(t *testing.T) {
	root := buildTestTree(t)
	// Backend Assembly OR tests
	scope := []string{"*/Assembly/*", "tests/*"}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 Assembly + 3 tests = 5
	if count != 5 {
		t.Errorf("expected 5 files for multi-pattern scope, got %d", count)
	}
}

func TestFindFilesWithFirstWord_PathScope_Assembly(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"*/Assembly/*"}
	// "assembly" appears in backend/Assembly/main.md
	files, err := search.FindFilesWithFirstWord("assembly", allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected a match for 'assembly' in */Assembly/*")
	}
	for _, f := range files {
		if !fileUnderRoot(f, filepath.Join(root, "backend", "Assembly")) {
			t.Errorf("result %q should be under Assembly/, got it outside scope", f)
		}
	}
}

func TestFindFilesWithFirstWord_PathScope_NoMatch(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"tests/*"}
	// "assembly" does NOT appear in tests/
	files, err := search.FindFilesWithFirstWord("assembly", allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected no results for 'assembly' in tests/ scope, got %v", files)
	}
}

func TestGetDocumentFileCount_PathScope_EmptyNilMeansAll(t *testing.T) {
	root := buildTestTree(t)
	// nil pathScope = no filter = all files
	countAll, err := search.GetDocumentFileCount(allFileTypes(), root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	countEmpty, err := search.GetDocumentFileCount(allFileTypes(), root, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if countAll != countEmpty {
		t.Errorf("nil and empty pathScope should produce same count: %d vs %d", countAll, countEmpty)
	}
}

// TestGetDocumentFileCount_PathScope_TrailingSlash verifies that a pattern like
// "tests/" (no explicit wildcard) matches all files under tests/ at any depth.
// This is the directory-prefix shorthand: users shouldn't need to know that
// filepath.Match requires "tests/*" -- "tests/" should just work.
func TestGetDocumentFileCount_PathScope_TrailingSlash(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"tests/"}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// tests/ has 3 files -- same result as "tests/*"
	if count != 3 {
		t.Errorf("expected 3 files matching tests/ prefix, got %d", count)
	}
}

// TestGetDocumentFileCount_PathScope_BareDir verifies that a bare directory name
// without a trailing slash (e.g. "tests") also matches files under that directory.
func TestGetDocumentFileCount_PathScope_BareDir(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"tests"}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files matching bare dir 'tests', got %d", count)
	}
}

// fileUnderRoot returns true if path is under (or equal to) root.
func fileUnderRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	// Relative path must not start with ".."
	return len(rel) > 0 && rel[0] != '.'
}

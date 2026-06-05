package search_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		"app/Http/Controllers/Auth",
		"app/Http/Requests",
		"frontend",
		"tests",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	files := map[string]string{
		"backend/service/auth.go":                       "package service\nfunc Auth() {}\n",
		"backend/service/auth.md":                       "# auth\ntoken authentication service\n",
		"backend/Assembly/main.cs":                      "using System;\nclass Main { static void Main() {} }\n",
		"backend/Assembly/main.md":                      "# Assembly main\nassembly entry point\n",
		"app/Http/Controllers/Auth/OAuthController.php": "provider access_token controller\n",
		"app/Http/Requests/LoginRequest.php":            "provider access_token request\n",
		"frontend/app.ts":                               "export function app() {}\n",
		"frontend/app.md":                               "# frontend app\napp description\n",
		"tests/backend_test.go":                         "package tests\nfunc TestBackend() {}\n",
		"tests/frontend_test.go":                        "package tests\nfunc TestFrontend() {}\n",
		"tests/endpoint_test.md":                        "# endpoint test\nendpoint coverage\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// TestFindFilesWithFirstWordProgress_MultiChunkBoundary exercises the streaming
// (>64KiB) candidate-filter path, which previously had no coverage. The needle's
// only occurrence straddles the 64KiB chunk boundary, so finding it requires the
// reader's overlap+multi-read logic (the loop that carried the buf[:toRead] vs
// buf[:n] slicing). A large non-matching file must not be reported.
func TestFindFilesWithFirstWordProgress_MultiChunkBoundary(t *testing.T) {
	root := t.TempDir()
	const chunk = 64 * 1024

	// big.md: a single long token fills chunk 1, a word boundary lands at the
	// 64KiB edge, then "needlematch" straddles it, then trailing filler keeps
	// the file multi-chunk (~73KB).
	var b strings.Builder
	b.Grow(80 * 1024)
	b.WriteString(strings.Repeat("x", chunk-4)) // 65532 bytes: one big token
	b.WriteByte(' ')                            // word boundary at offset 65532
	b.WriteString("needlematch")                // starts at 65533, crosses 65536
	b.WriteByte(' ')
	b.WriteString(strings.Repeat("x", 8*1024)) // trailing filler -> 2+ chunks
	if err := os.WriteFile(filepath.Join(root, "big.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write big.md: %v", err)
	}

	// ctrl.md: large, multi-chunk, no needle anywhere.
	ctrl := strings.Repeat("filler ", 12000) // ~84KB
	if err := os.WriteFile(filepath.Join(root, "ctrl.md"), []byte(ctrl), 0o644); err != nil {
		t.Fatalf("write ctrl.md: %v", err)
	}

	files, err := search.FindFilesWithFirstWordProgress(
		[]string{"needlematch"}, []string{"-g", "*.md"}, 2, nil, root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotBig, gotCtrl bool
	for _, f := range files {
		switch filepath.Base(f) {
		case "big.md":
			gotBig = true
		case "ctrl.md":
			gotCtrl = true
		}
	}
	if !gotBig {
		t.Errorf("needle straddling the 64KiB boundary not found in big.md; matches=%v", files)
	}
	if gotCtrl {
		t.Errorf("control file with no needle was incorrectly reported as a match")
	}
}

// allFileTypes includes both doc and code types for tests
func allFileTypes() []string {
	return []string{
		"-g", "*.md",
		"-g", "*.go",
		"-g", "*.cs",
		"-g", "*.ts",
		"-g", "*.php",
	}
}

// --- Task 3: walkRoot parameter ---

func TestGetDocumentFileCount_WalkRoot_AllFiles(t *testing.T) {
	root := buildTestTree(t)
	count, err := search.GetDocumentFileCount(allFileTypes(), root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 11 files total in the tree
	if count != 11 {
		t.Errorf("expected 11 files, got %d", count)
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

func TestGetDocumentFileCount_PathScope_BackendAssemblyWindowsSeparators(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{`backend\Assembly`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files matching Windows-style backend\\Assembly, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_GlobstarController(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{`app\Http\**\*Controller.php`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 controller file matching globstar scope, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_LeadingGlobstarController(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{`**\*Controller.php`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 controller file matching leading globstar scope, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_BasenamePatternMatchesAnywhere(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{`*Controller.php`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 controller file matching basename scope, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_GlobstarLiteralPrefix(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{`app\Http\**\*.php`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 PHP files under app/Http via globstar, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_QuotedCommaSeparatedGlobstars(t *testing.T) {
	root := buildTestTree(t)
	scope, err := search.ValidatePathScope(`'app\Http\**\*.php,app\**\*Controller.php'`)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected count error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 PHP files matching quoted comma-separated globstars, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_DirWildcardMatchesNestedFiles(t *testing.T) {
	root := buildTestTree(t)
	nestedDir := filepath.Join(root, "backend", "Assembly", "Nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "nested.md"), []byte("# nested assembly\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	scope := []string{`backend\Assembly\*`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files under backend\\Assembly\\*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_QuotedDirWildcardMatchesNestedFiles(t *testing.T) {
	root := buildTestTree(t)
	nestedDir := filepath.Join(root, "app", "Http", "Controllers", "Auth")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "OAuthController.md"), []byte("provider access_token\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	scope := []string{`'app\Http\*'`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files under quoted app\\Http\\*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filesystems are matched case-insensitively")
	}

	root := buildTestTree(t)
	scope := []string{`backend\assembly`}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files matching Windows case-insensitive pathscope, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_AbsoluteDir(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{filepath.Join(root, "tests")}
	count, err := search.GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files matching absolute tests path, got %d", count)
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

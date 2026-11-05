package search

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateUserGlob(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		ignoreCase bool
		want       UserGlob
		wantErr    bool
	}{
		{name: "literal comma", value: "docs/a,b/*.md", want: UserGlob{Pattern: "docs/a,b/*.md"}},
		{name: "negated", value: "!**/generated/**", want: UserGlob{Pattern: "**/generated/**", Negated: true}},
		{name: "ignore case", value: "*.GO", ignoreCase: true, want: UserGlob{Pattern: "*.GO", IgnoreCase: true}},
		{name: "empty", wantErr: true},
		{name: "only negation", value: "!", wantErr: true},
		{name: "bare slash", value: "/", wantErr: true},
		{name: "negated bare slash", value: "!/", wantErr: true},
		{name: "bare backslash", value: `\`, wantErr: true},
		{name: "traversal", value: "../*.md", wantErr: true},
		{name: "rejected character", value: "[ab].md", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateUserGlob(tt.value, tt.ignoreCase)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUserGlob() error = %v, wantErr %t", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("ValidateUserGlob() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateUserGlobErrorIsStandalone(t *testing.T) {
	_, err := ValidateUserGlob("!", false)
	const want = "user glob negation must include a pattern"
	if err == nil || err.Error() != want {
		t.Fatalf("ValidateUserGlob() error = %q, want %q", err, want)
	}
}

func TestCompilePathPolicyMatching(t *testing.T) {
	root := t.TempDir()
	globs := []UserGlob{
		{Pattern: "*.md"},
		{Pattern: "**/draft/**", Negated: true},
	}
	policy, err := CompilePathPolicy([]string{"docs/**", "tests/**"}, globs, root, root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{path: "docs/readme.md", want: true},
		{path: "tests/unit/readme.md", want: true},
		{path: "docs/readme.txt", want: false},
		{path: "src/readme.md", want: false},
		{path: "docs/draft/readme.md", want: false},
	}
	for _, tt := range tests {
		if got := policy.MatchesFile(filepath.Join(root, filepath.FromSlash(tt.path))); got != tt.want {
			t.Errorf("MatchesFile(%q) = %t, want %t", tt.path, got, tt.want)
		}
	}
}

func TestPathPolicyBasenameDirectoryAndPruning(t *testing.T) {
	root := t.TempDir()
	policy, err := CompilePathPolicy([]string{"target", "!**/target/private/"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.MatchesFile(filepath.Join(root, "one", "target", "keep.md")) {
		t.Error("bare directory pattern did not match descendants")
	}
	if policy.MatchesFile(filepath.Join(root, "one", "target", "private", "hidden.md")) {
		t.Error("negative directory pattern admitted a descendant")
	}
	if policy.CouldMatchDirectory(filepath.Join(root, "one", "target", "private")) {
		t.Error("negative directory did not prune its subtree")
	}

	rooted, err := CompilePathPolicy([]string{"docs/api/**"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if rooted.CouldMatchDirectory(filepath.Join(root, "other")) {
		t.Error("literal prefix allowed an unrelated subtree")
	}
	if !rooted.CouldMatchDirectory(filepath.Join(root, "docs")) {
		t.Error("literal prefix pruned a required ancestor")
	}
}

func TestPathPolicyNegativeBasenameWildcardsPruneDescendants(t *testing.T) {
	root := t.TempDir()
	for _, pattern := range []string{"!build*", "!*_cache", "!**/build*", "!**/*_cache"} {
		t.Run(pattern, func(t *testing.T) {
			policy, err := CompilePathPolicy([]string{pattern}, nil, root, root)
			if err != nil {
				t.Fatal(err)
			}
			var directory string
			if pattern == "!build*" || pattern == "!**/build*" {
				directory = filepath.Join(root, "nested", "build-output")
			} else {
				directory = filepath.Join(root, "nested", "module_cache")
			}
			if policy.MatchesFile(filepath.Join(directory, "hidden.md")) {
				t.Errorf("%q admitted a matching directory descendant", pattern)
			}
			if policy.CouldMatchDirectory(directory) {
				t.Errorf("%q did not prune a matching directory", pattern)
			}
		})
	}

	positive, err := CompilePathPolicy([]string{"build*"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "nested", "build-output")
	if !positive.CouldMatchDirectory(directory) {
		t.Error("positive basename wildcard pruned a potentially useful directory")
	}
	if !positive.MatchesFile(filepath.Join(directory, "nested.md")) {
		t.Error("positive basename wildcard did not include matching directory descendants")
	}
}

func TestPathPolicyTrailingSlashIsRootAnchored(t *testing.T) {
	root := t.TempDir()
	policy, err := CompilePathPolicy([]string{"src/"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.MatchesFile(filepath.Join(root, "src", "main.go")) {
		t.Error("src/ did not include root src descendants")
	}
	if policy.MatchesFile(filepath.Join(root, "tests", "src", "main.go")) {
		t.Error("src/ included a nested src directory")
	}
	if policy.CouldMatchDirectory(filepath.Join(root, "tests")) {
		t.Error("src/ did not safely prune an unrelated root subtree")
	}
	if !policy.exemptsDirectChild("src") {
		t.Error("src/ did not retain direct-child ignore exemption")
	}

	repo, err := CompilePathPolicy([]string{"RepoA/"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !repo.exemptsDirectChild("RepoA") {
		t.Error("RepoA/ did not exempt its direct-child ignore container")
	}
}

func TestPathPolicyPositiveAnchoredDirectoryMatchesDescendants(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		pattern string
		file    string
	}{
		{pattern: "*/backend", file: "app/backend/deep/main.go"},
		{pattern: "src/*/api", file: "src/service/api/deep/main.go"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			policy, err := CompilePathPolicy([]string{tt.pattern}, nil, root, root)
			if err != nil {
				t.Fatal(err)
			}
			if !policy.MatchesFile(filepath.Join(root, filepath.FromSlash(tt.file))) {
				t.Errorf("%q did not include descendants of its matching directory", tt.pattern)
			}
		})
	}
}

func TestPathPolicyDirectoryOnlyDoesNotMatchRegularFile(t *testing.T) {
	root := t.TempDir()
	positive, err := CompilePathPolicy([]string{"cache/"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if positive.MatchesFile(filepath.Join(root, "cache")) {
		t.Error("positive directory-only pattern matched a regular file")
	}
	if !positive.MatchesFile(filepath.Join(root, "cache", "inside.md")) {
		t.Error("positive directory-only pattern missed a directory descendant")
	}

	negative, err := CompilePathPolicy([]string{"!cache/"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !negative.MatchesFile(filepath.Join(root, "cache")) {
		t.Error("negative directory-only pattern excluded a regular file")
	}
	if negative.MatchesFile(filepath.Join(root, "cache", "inside.md")) {
		t.Error("negative directory-only pattern admitted a directory descendant")
	}
}

func TestPathPolicyRootRelativeShorthandTable(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		pattern string
	}{
		{pattern: "./src"},
		{pattern: "/src"},
		{pattern: `\src`},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			compiled, err := compilePathPatternWithAbsolute(
				tt.pattern,
				false,
				root,
				root,
				func(string) bool { return false },
			)
			if err != nil {
				t.Fatal(err)
			}
			policy := &PathPolicy{
				logicalRoot:   root,
				physicalRoot:  root,
				scopePositive: []compiledPathPattern{compiled},
			}
			if !policy.MatchesFile(filepath.Join(root, "src", "main.go")) {
				t.Errorf("%q did not match root src descendants", tt.pattern)
			}
			if policy.MatchesFile(filepath.Join(root, "tests", "src", "main.go")) {
				t.Errorf("%q matched nested tests/src descendants", tt.pattern)
			}
			if policy.CouldMatchDirectory(filepath.Join(root, "tests")) {
				t.Errorf("%q did not prune unrelated tests subtree", tt.pattern)
			}
		})
	}
}

func TestPathPolicyCaseModeIndependentOfHost(t *testing.T) {
	root := t.TempDir()
	exact, err := CompilePathPolicy(nil, []UserGlob{{Pattern: "*.GO"}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := CompilePathPolicy(nil, []UserGlob{{Pattern: "*.GO", IgnoreCase: true}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(root, "main.go")
	if exact.MatchesFile(lower) {
		t.Error("case-sensitive glob was lowercased by the host")
	}
	if !folded.MatchesFile(lower) {
		t.Error("case-insensitive glob did not fold case")
	}
}

func TestPathPolicyAbsoluteAndRootRelative(t *testing.T) {
	root := t.TempDir()
	inRoot := filepath.Join(root, "src", "*.go")
	outRoot := filepath.Join(filepath.Dir(root), "elsewhere", "*.go")
	for _, pattern := range []string{"src/*.go", "./src/*.go", inRoot} {
		policy, err := CompilePathPolicy([]string{pattern}, nil, root, root)
		if err != nil {
			t.Fatal(err)
		}
		if !policy.MatchesFile(filepath.Join(root, "src", "main.go")) {
			t.Errorf("pattern %q did not match root-relative file", pattern)
		}
	}
	policy, err := CompilePathPolicy([]string{outRoot}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MatchesFile(filepath.Join(root, "src", "main.go")) {
		t.Error("out-of-root absolute pattern became active")
	}

	matchAll, err := CompilePathPolicy([]string{root}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !matchAll.MatchesFile(filepath.Join(root, "any", "depth", "readme.md")) {
		t.Error("absolute pattern equal to root did not match all descendants")
	}
	if !matchAll.CouldMatchDirectory(filepath.Join(root, "any")) {
		t.Error("absolute pattern equal to root pruned a descendant")
	}
}

func TestPathPolicyAbsoluteThroughSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	tempBase := t.TempDir()
	base, err := filepath.EvalSymlinks(tempBase)
	if err != nil {
		t.Fatalf("canonicalize temp base: %v", err)
	}
	logicalRoot := filepath.Join(base, "root")
	unrelatedPhysicalRoot := filepath.Join(base, "unrelated")
	linkedParent := filepath.Join(base, "linked-parent")
	if err := os.MkdirAll(logicalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unrelatedPhysicalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(logicalRoot, linkedParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	pattern := filepath.Join(linkedParent, "missing", "*.md")
	policy, err := CompilePathPolicy([]string{pattern}, nil, logicalRoot, unrelatedPhysicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.MatchesFile(filepath.Join(logicalRoot, "missing", "readme.md")) {
		t.Error("absolute pattern did not resolve its symlinked parent before containment")
	}
}

func TestPathPolicyBasenameDoesNotMatchExplicitRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ignored")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := CompilePathPolicy([]string{"ignored"}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MatchesFile(filepath.Join(root, "match.txt")) {
		t.Fatal("basename pathscope implicitly matched the explicit root itself")
	}
}

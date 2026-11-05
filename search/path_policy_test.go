package search

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
	if !policy.CouldMatchDirectory(filepath.Join(logicalRoot, "missing")) {
		t.Error("resolved absolute wildcard prefix pruned its matching logical directory")
	}
	if policy.CouldMatchDirectory(filepath.Join(logicalRoot, "unrelated")) {
		t.Error("resolved absolute wildcard prefix retained an unrelated logical directory")
	}
}

func TestCompiledAbsolutePatternCrossPairsPlatformNeutral(t *testing.T) {
	pattern := compiledPathPattern{
		pattern:         "logical/missing/*.md",
		physicalPattern: "physical/missing/*.md",
		absolute:        true,
		anchored:        true,
		active:          true,
		parts:           splitPathScopeParts("logical/missing/*.md"),
		physicalParts:   splitPathScopeParts("physical/missing/*.md"),
	}
	tests := []struct {
		name     string
		logical  string
		physical string
		want     bool
	}{
		{name: "native pair", logical: "logical/missing/readme.md", physical: "physical/missing/readme.md", want: true},
		{name: "crossed pair", logical: "physical/missing/readme.md", physical: "logical/missing/readme.md", want: true},
		{name: "logical form in physical candidate", logical: "other/readme.md", physical: "logical/missing/readme.md", want: true},
		{name: "unrelated", logical: "other/readme.md", physical: "elsewhere/readme.md", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pattern.matchesAbsoluteCandidates(test.logical, test.physical, false); got != test.want {
				t.Fatalf("matchesAbsoluteCandidates() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWindowsContainmentPreservesRelativePatternCase(t *testing.T) {
	relative, contained := relativeContainedPathForPlatform(
		`C:\Root`,
		`c:\root\Case\*.GO`,
		true,
	)
	if !contained || filepath.ToSlash(relative) != "Case/*.GO" {
		t.Fatalf("relative/contained = %q/%v, want Case/*.GO/true", relative, contained)
	}
	if _, contained := relativeContainedPathForPlatform(`C:\Root`, `c:\RootSibling\*.GO`, true); contained {
		t.Fatal("Windows containment accepted a sibling-name prefix")
	}
	exact, err := compilePathPatternWithAbsolute(
		filepath.ToSlash(relative),
		false,
		".",
		".",
		func(string) bool { return false },
	)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := compilePathPatternWithAbsolute(
		filepath.ToSlash(relative),
		true,
		".",
		".",
		func(string) bool { return false },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !exact.matches("Case/main.GO", false) || exact.matches("case/main.go", false) {
		t.Fatal("case-sensitive descendant matching was weakened by Windows containment folding")
	}
	if !folded.matches("case/main.go", false) {
		t.Fatal("case-insensitive descendant matching did not fold relative segments")
	}
}

func TestSelectResolvedPatternPreservesWindowsUserCase(t *testing.T) {
	tests := []struct {
		name     string
		original string
		resolved string
		windows  bool
		want     string
	}{
		{
			name:     "Windows case only",
			original: `C:/Root/Case/*.GO`,
			resolved: `c:/root/case/*.go`,
			windows:  true,
			want:     `C:/Root/Case/*.GO`,
		},
		{
			name:     "Windows reparse target",
			original: `C:/Link/Case/*.GO`,
			resolved: `D:/Target/case/*.GO`,
			windows:  true,
			want:     `D:/Target/case/*.GO`,
		},
		{
			name:     "POSIX case change is physical",
			original: `/Root/Case/*.GO`,
			resolved: `/root/case/*.go`,
			want:     `/root/case/*.go`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := filepath.ToSlash(selectResolvedPattern(test.original, test.resolved, test.windows)); got != test.want {
				t.Fatalf("selectResolvedPattern() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSelectResolvedSegmentForPlatform(t *testing.T) {
	tests := []struct {
		name        string
		parent      string
		userSegment string
		resolved    string
		want        string
		redirected  bool
	}{
		{
			name:        "case-only descendant",
			parent:      `C:/Target`,
			userSegment: "Case",
			resolved:    `c:/target/case`,
			want:        `C:/Target/Case`,
		},
		{
			name:        "junction target",
			parent:      `C:/Base`,
			userSegment: "Link",
			resolved:    `D:/Target`,
			want:        `D:/Target`,
			redirected:  true,
		},
		{
			name:        "case-only after redirect",
			parent:      `D:/Target`,
			userSegment: "Case",
			resolved:    `d:/target/case`,
			want:        `D:/Target/Case`,
		},
		{
			name:        "short-name alias",
			parent:      `C:/`,
			userSegment: "PROGRA~1",
			resolved:    `C:/Program Files`,
			want:        `C:/Program Files`,
			redirected:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, redirected := selectResolvedSegmentForPlatform(
				test.parent,
				test.userSegment,
				test.resolved,
				true,
			)
			got = filepath.ToSlash(got)
			if got != test.want || redirected != test.redirected {
				t.Fatalf("selectResolvedSegmentForPlatform() = %q/%v, want %q/%v", got, redirected, test.want, test.redirected)
			}
		})
	}
}

func TestRootSpanningRelativePatternTable(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		root     string
		foldCase bool
		want     []string
	}{
		{
			name:     "wildcards span root",
			pattern:  `c:/*/x/temp/**`,
			root:     `C:/Users/X/Temp`,
			foldCase: true,
			want:     []string{`**`},
		},
		{
			name:     "descendant case remains",
			pattern:  `c:/*/x/temp/**/Case/*.GO`,
			root:     `C:/Users/X/Temp`,
			foldCase: true,
			want:     []string{`**/Case/*.GO`},
		},
		{
			name:     "root mismatch",
			pattern:  `c:/Admin/**`,
			root:     `C:/Users/X/Temp`,
			foldCase: true,
			want:     nil,
		},
		{
			name:     "Windows pattern covers deeper root",
			pattern:  `c:/*/x`,
			root:     `C:/Users/X/Temp`,
			foldCase: true,
			want:     []string{"."},
		},
		{
			name:    "POSIX wildcard above root",
			pattern: `/repo/*/ignored/**`,
			root:    `/repo/a`,
			want:    []string{`ignored/**`},
		},
		{
			name:    "POSIX literal case mismatch",
			pattern: `/repo/X/ignored/**`,
			root:    `/repo/x`,
			want:    nil,
		},
		{
			name:    "POSIX pattern covers deeper root",
			pattern: `/repo/*/x`,
			root:    `/repo/a/x/deeper`,
			want:    []string{"."},
		},
		{
			name:    "globstar yields every alignment",
			pattern: `/repo/**/a/b/**`,
			root:    `/repo/a`,
			want:    []string{`b/**`, `**/a/b/**`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rootSpanningRelativePatterns(test.pattern, test.root, test.foldCase)
			for index := range got {
				got[index] = strings.ReplaceAll(filepath.ToSlash(got[index]), `\`, "/")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("rootSpanningRelativePatterns() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPathPolicyAbsoluteLiteralPrefixPruning(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target", "**", "*.md")
	policy, err := CompilePathPolicy([]string{target}, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{path: filepath.Join(root, "target"), want: true},
		{path: filepath.Join(root, "target", "nested"), want: true},
		{path: filepath.Join(root, "unrelated"), want: false},
	}
	for _, test := range tests {
		if got := policy.CouldMatchDirectory(test.path); got != test.want {
			t.Errorf("CouldMatchDirectory(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestPathPolicyDerivedAbsoluteVariantsRemainRootAnchored(t *testing.T) {
	root := t.TempDir()
	positive, err := CompilePathPolicy(
		[]string{filepath.Join(root, "tests")},
		nil,
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !positive.MatchesFile(filepath.Join(root, "tests", "match.md")) {
		t.Fatal("absolute positive missed root-anchored directory")
	}
	if positive.MatchesFile(filepath.Join(root, "nested", "tests", "match.md")) {
		t.Fatal("absolute positive matched nested same-name directory")
	}

	negative, err := CompilePathPolicy(
		[]string{"!" + filepath.Join(root, "build")},
		nil,
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if negative.MatchesFile(filepath.Join(root, "build", "blocked.md")) {
		t.Fatal("absolute negative admitted root-anchored directory")
	}
	if !negative.MatchesFile(filepath.Join(root, "nested", "build", "allowed.md")) {
		t.Fatal("absolute negative excluded nested same-name directory")
	}

	directFiles, err := CompilePathPolicy(
		[]string{filepath.Join(root, "*.md")},
		nil,
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !directFiles.MatchesFile(filepath.Join(root, "direct.md")) ||
		directFiles.MatchesFile(filepath.Join(root, "nested", "deep.md")) {
		t.Fatal("single-segment absolute wildcard lost root anchoring")
	}

	directoryOnly, err := CompilePathPolicy(
		[]string{filepath.Join(root, "build") + string(filepath.Separator)},
		nil,
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if directoryOnly.MatchesFile(filepath.Join(root, "build")) ||
		!directoryOnly.MatchesFile(filepath.Join(root, "build", "inside.md")) {
		t.Fatal("derived absolute trailing-directory semantics were not preserved")
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

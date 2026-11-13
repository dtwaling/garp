package search

import (
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"garp/config"
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

func TestEmptyPathPolicyAllocations(t *testing.T) {
	root := t.TempDir()
	compiled, err := CompilePathPolicy(nil, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "nested", "file.md")
	for _, policy := range []*PathPolicy{nil, {}, compiled} {
		for _, check := range []struct {
			match func(string) bool
			want  bool
		}{
			{policy.MatchesFile, true},
			{policy.CouldMatchDirectory, true},
			{policy.MatchesPositiveGlob, false},
		} {
			allocs := testing.AllocsPerRun(100, func() {
				if got := check.match(filename); got != check.want {
					t.Fatalf("empty policy match = %t, want %t", got, check.want)
				}
			})
			if allocs != 0 {
				t.Errorf("empty policy allocated %g times, want zero", allocs)
			}
		}
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

func TestPatternIndicesAtRoot(t *testing.T) {
	for _, tt := range []struct {
		name, pattern, root     string
		patternIndex, rootIndex int
		foldCase                bool
		want                    []int
	}{
		{name: "empty", want: []int{0}},
		{name: "root exhausted", pattern: "a/**/**", root: "a", want: []int{1}},
		{name: "pattern exhausted", pattern: "a", root: "a/b", want: []int{1}},
		{name: "globstar offsets", pattern: "**/**/b", root: "a", want: []int{1, 0}},
		{name: "globstar and literal", pattern: "**/**/b", root: "a/b", want: []int{3, 1, 0}},
		{name: "trailing globstars", pattern: "**/**", root: "a/b", want: []int{2, 1, 0}},
		{name: "mismatch", pattern: "a/b", root: "a/c"},
		{name: "invalid", pattern: "**/[", root: "a/b", want: []int{0}},
		{name: "fold case", pattern: "A/[B]", root: "a/b", foldCase: true, want: []int{2}},
		{name: "case sensitive", pattern: "A/[B]", root: "a/b"},
		{name: "offsets", pattern: "skip/**/**/b", root: "skip/a/b", patternIndex: 1, rootIndex: 1, want: []int{4, 2, 1}},
		{name: "discovery order", pattern: "repo/**/a/b/**", root: "repo/a", want: []int{3, 1}},
		{name: "escaped literal", pattern: `\*\*/x`, root: "**/x", want: []int{2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pattern, root := splitPathScopeParts(tt.pattern), splitPathScopeParts(tt.root)
			beforePattern, beforeRoot := slices.Clone(pattern), slices.Clone(root)
			got := patternIndicesAtRoot(pattern, root, tt.patternIndex, tt.rootIndex, tt.foldCase)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("indices = %v, want %v", got, tt.want)
			}
			if !slices.Equal(pattern, beforePattern) || !slices.Equal(root, beforeRoot) {
				t.Fatal("matching mutated inputs")
			}
		})
	}
}

// Compare unique terminal offsets in first-discovery order with the original recursion.
// Only short inputs reach the oracle so its exponential branching stays bounded.
func TestPatternIndicesAtRootExhaustive(t *testing.T) {
	var reference func([]string, []string, int, int, bool) []int
	reference = func(pattern, root []string, pi, ri int, fold bool) []int {
		if ri == len(root) || pi == len(pattern) {
			return []int{pi}
		}
		if pattern[pi] == "**" {
			return append(reference(pattern, root, pi+1, ri, fold), reference(pattern, root, pi, ri+1, fold)...)
		}
		p, r := pattern[pi], root[ri]
		if fold {
			p, r = strings.ToLower(p), strings.ToLower(r)
		}
		matched, err := path.Match(p, r)
		if err != nil || !matched {
			return nil
		}
		return reference(pattern, root, pi+1, ri+1, fold)
	}
	sequences := func(alphabet []string, depth int) [][]string {
		result := [][]string{nil}
		start := 0
		for i := 0; i < depth; i++ {
			end := len(result)
			for _, prefix := range result[start:end] {
				for _, part := range alphabet {
					result = append(result, append(slices.Clone(prefix), part))
				}
			}
			start = end
		}
		return result
	}
	patterns := sequences([]string{"a", "A", "**", "*", "[ab]", "["}, 4)
	roots := sequences([]string{"a", "b", "**"}, 3)
	checks := 0
	for _, pattern := range patterns {
		for _, root := range roots {
			for _, fold := range []bool{false, true} {
				var want []int
				for _, index := range reference(pattern, root, 0, 0, fold) {
					if !slices.Contains(want, index) {
						want = append(want, index)
					}
				}
				got := patternIndicesAtRoot(pattern, root, 0, 0, fold)
				if !slices.Equal(got, want) {
					t.Fatalf("indices(%q, %q, fold=%v) = %v, want %v", pattern, root, fold, got, want)
				}
				checks++
			}
		}
	}
	t.Logf("verified %d pattern/root/case combinations", checks)
}

const policyFixtureLimit int64 = 256

func TestRepositoryPolicyCountDiscoveryAndExtraction(t *testing.T) {
	root := buildPolicyRepository(t)
	fileTypes := config.BuildRipgrepFileTypes(true)
	options := WalkOptions{MaxFileSize: policyFixtureLimit}

	want := []string{".env.example", "cap.txt", "visible.txt"}
	candidates := assertPolicyCountDiscoveryParity(t, root, fileTypes, nil, options, want)

	noIgnoreWant := append(append([]string{}, want...),
		"ancestor-ignored.txt",
		"local-ignored.txt",
		"nested/local-ignored.txt",
		"nested/main.txt",
		"repository-excluded.txt",
		"scoped/main.txt",
	)
	assertPolicyCountDiscoveryParity(
		t, root, fileTypes, nil, WalkOptions{NoIgnore: true, MaxFileSize: policyFixtureLimit}, noIgnoreWant,
	)

	scopedWant := []string{"scoped/main.txt"}
	assertPolicyCountDiscoveryParity(t, root, fileTypes, []string{"scoped/"}, options, scopedWant)

	capFile := filepath.Join(root, "cap.txt")
	if err := os.WriteFile(capFile, append(policyFixturePrefix(), []byte(" alpha beta OUTSIDE_PREFIX")...), 0o600); err != nil {
		t.Fatalf("grow cap fixture: %v", err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, fileTypes, true, 1, 1000)
	engine.StartDir = root
	engine.MaxFileSize = policyFixtureLimit
	engine.Strict = true
	engine.Silent = true
	matches, err := engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatalf("filter candidates: %v", err)
	}
	if len(matches) != len(candidates) {
		t.Fatalf("filtered matches = %d, want all %d discovered candidates", len(matches), len(candidates))
	}
	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatalf("extract results: %v", err)
	}
	if got := relativePolicyResultPaths(t, root, results); !reflect.DeepEqual(got, want) {
		t.Fatalf("result paths = %v, want %v", got, want)
	}

	capResult := policyResultByPath(t, results, capFile)
	if strings.Contains(capResult.CleanContent, "OUTSIDE_PREFIX") {
		t.Fatal("clean content contains text beyond the configured prefix")
	}
	for _, excerpt := range capResult.RawExcerpts {
		if strings.Contains(excerpt, "OUTSIDE_PREFIX") {
			t.Fatalf("excerpt contains text beyond the configured prefix: %q", excerpt)
		}
	}
}

func buildPolicyRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	root := filepath.Join(repository, "work")

	writeIgnoreFixture(t, filepath.Join(repository, ".git", "info", "exclude"), "repository-excluded.txt\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "work/ancestor-ignored.txt\nwork/nested/\nwork/scoped/\n")
	writeIgnoreFixture(t, filepath.Join(root, ".ignore"), "local-ignored.txt\n")
	writeIgnoreFixture(t, filepath.Join(root, "nested", ".ignore"), "local-ignored.txt\n")
	if err := os.MkdirAll(filepath.Join(root, "nested", ".git"), 0o755); err != nil {
		t.Fatalf("create nested repository: %v", err)
	}

	for _, relative := range []string{
		".env",
		".env.example",
		"ancestor-ignored.txt",
		"local-ignored.txt",
		"nested/local-ignored.txt",
		"nested/main.txt",
		"node_modules/hidden.txt",
		"repository-excluded.txt",
		"scoped/main.txt",
		"visible.txt",
	} {
		writeWalkFixture(t, root, relative, "alpha beta")
	}
	writeWalkFixture(t, root, "cap.txt", string(policyFixturePrefix()))
	writeSizedDiscoveryFixture(t, root, "over-limit.txt", "alpha beta", policyFixtureLimit+1)
	writeSizedDiscoveryFixture(t, root, "over-limit.pdf", "alpha beta", policyFixtureLimit+1)
	return root
}

func policyFixturePrefix() []byte {
	prefix := []byte("alpha " + strings.Repeat("x", 32) + " beta")
	return append(prefix, []byte(strings.Repeat(" ", int(policyFixtureLimit)-len(prefix)))...)
}

func assertPolicyCountDiscoveryParity(
	t *testing.T,
	root string,
	fileTypes, pathScope []string,
	options WalkOptions,
	want []string,
) []string {
	t.Helper()
	sort.Strings(want)

	count, err := GetDocumentFileCountWithOptions(fileTypes, root, pathScope, options)
	if err != nil {
		t.Fatalf("count candidates: %v", err)
	}
	candidates, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"alpha", "beta"}, fileTypes, 2, nil, root, pathScope, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("discover candidates: %v", err)
	}
	got := relativeCandidatePaths(t, root, candidates)
	if count != len(candidates) {
		t.Fatalf("count/discovery = %d/%d, want parity", count, len(candidates))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	return candidates
}

func relativeCandidatePaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	relative := make([]string, len(paths))
	for i, path := range paths {
		value, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative path for %q: %v", path, err)
		}
		relative[i] = filepath.ToSlash(value)
	}
	sort.Strings(relative)
	return relative
}

func relativePolicyResultPaths(t *testing.T, root string, results []SearchResult) []string {
	t.Helper()
	paths := make([]string, len(results))
	for i := range results {
		paths[i] = results[i].FilePath
	}
	return relativeCandidatePaths(t, root, paths)
}

func policyResultByPath(t *testing.T, results []SearchResult, path string) SearchResult {
	t.Helper()
	for _, result := range results {
		if result.FilePath == path {
			return result
		}
	}
	t.Fatalf("missing result %q", path)
	return SearchResult{}
}

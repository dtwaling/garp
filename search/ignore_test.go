package search

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeIgnoreFixture(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ignorePatterns(rules []ignoreRule) []string {
	patterns := make([]string, len(rules))
	for i := range rules {
		patterns[i] = rules[i].pattern
	}
	return patterns
}

func TestIgnoreRuleParsing(t *testing.T) {
	const input = "first\r\n\r\n# comment\r\nsecond   \r\nliteral\\ \r\n\\#hash\r\n\\!bang\r\n"
	rules := parseIgnoreRulesForOS(input, "repo/sub", 7, "linux")

	if len(rules) != 5 {
		t.Fatalf("parseIgnoreRulesForOS() returned %d rules, want 5", len(rules))
	}

	want := []struct {
		pattern   string
		lineOrder int
	}{
		{pattern: "first", lineOrder: 1},
		{pattern: "second", lineOrder: 4},
		{pattern: `literal\ `, lineOrder: 5},
		{pattern: `\#hash`, lineOrder: 6},
		{pattern: `\!bang`, lineOrder: 7},
	}
	for i, expected := range want {
		if rules[i].pattern != expected.pattern {
			t.Errorf("rule %d pattern = %q, want %q", i, rules[i].pattern, expected.pattern)
		}
		if rules[i].lineOrder != expected.lineOrder {
			t.Errorf("rule %d lineOrder = %d, want %d", i, rules[i].lineOrder, expected.lineOrder)
		}
		if rules[i].sourceDir != "repo/sub" || rules[i].sourcePrecedence != 7 {
			t.Errorf("rule %d source metadata = (%q, %d), want (%q, 7)", i, rules[i].sourceDir, rules[i].sourcePrecedence, "repo/sub")
		}
	}
}

func TestIgnorePatternEscapes(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
	}{
		{name: "space", pattern: `name\ `, candidate: "name "},
		{name: "hash", pattern: `\#name`, candidate: "#name"},
		{name: "bang", pattern: `\!name`, candidate: "!name"},
		{name: "star", pattern: `file\*`, candidate: "file*"},
		{name: "question", pattern: `file\?`, candidate: "file?"},
		{name: "open bracket", pattern: `file\[`, candidate: "file["},
		{name: "close bracket", pattern: `file\]`, candidate: "file]"},
		{name: "backslash", pattern: `file\\name`, candidate: `file\name`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseIgnoreRulesForOS(tt.pattern+"\n", "", 0, "linux")
			if len(rules) != 1 {
				t.Fatalf("parsed %d rules, want 1", len(rules))
			}
			if !rules[0].matches(tt.candidate, false) {
				t.Errorf("pattern %q did not match %q", tt.pattern, tt.candidate)
			}
		})
	}
}

func TestIgnorePatternBackslashIsPlatformAware(t *testing.T) {
	t.Run("POSIX literal backslash", func(t *testing.T) {
		rules := parseIgnoreRulesForOS("file\\\\name\n", "", 0, "linux")
		if len(rules) != 1 {
			t.Fatalf("parsed %d rules, want 1", len(rules))
		}
		if !rules[0].matches(`file\name`, false) {
			t.Error(`escaped backslash did not match a POSIX filename containing a literal backslash`)
		}
		if rules[0].matches("file/name", false) {
			t.Error(`escaped backslash matched a POSIX path separator`)
		}
	})

	t.Run("Windows separator normalization", func(t *testing.T) {
		rules := parseIgnoreRulesForOS("a/build\n", `repo`, 0, "windows")
		if len(rules) != 1 {
			t.Fatalf("parsed %d rules, want 1", len(rules))
		}
		if !rules[0].matches(`repo\a\build`, true) {
			t.Error(`Windows candidate separators were not normalized`)
		}
	})
}

func TestIgnorePatternAnchoringAndDirectoryOnly(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
		goos      string
		isDir     bool
		want      bool
	}{
		{name: "basename at source", pattern: "build", candidate: "repo/build", goos: "linux", isDir: true, want: true},
		{name: "basename nested", pattern: "build", candidate: "repo/a/build", goos: "linux", isDir: true, want: true},
		{name: "leading slash anchored", pattern: "/build", candidate: "repo/build", goos: "linux", isDir: true, want: true},
		{name: "leading slash rejects nested", pattern: "/build", candidate: "repo/a/build", goos: "linux", isDir: true, want: false},
		{name: "internal slash anchored", pattern: "a/build", candidate: "repo/a/build", goos: "linux", isDir: true, want: true},
		{name: "internal slash rejects deeper", pattern: "a/build", candidate: "repo/x/a/build", goos: "linux", isDir: true, want: false},
		{name: "directory only accepts directory", pattern: "cache/", candidate: "repo/a/cache", goos: "linux", isDir: true, want: true},
		{name: "directory only rejects file", pattern: "cache/", candidate: "repo/a/cache", goos: "linux", isDir: false, want: false},
		{name: "outside source directory", pattern: "build", candidate: "other/build", goos: "linux", isDir: true, want: false},
		{name: "normalizes Windows candidate separators", pattern: "a/build", candidate: `repo\a\build`, goos: "windows", isDir: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseIgnoreRulesForOS(tt.pattern+"\n", "repo", 0, tt.goos)
			if len(rules) != 1 {
				t.Fatalf("parsed %d rules, want 1", len(rules))
			}
			if got := rules[0].matches(tt.candidate, tt.isDir); got != tt.want {
				t.Errorf("matches(%q, %t) = %t, want %t", tt.candidate, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestIgnorePatternWildcards(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
		want      bool
	}{
		{name: "star", pattern: "*.log", candidate: "deep/app.log", want: true},
		{name: "star does not cross slash", pattern: "logs/*.log", candidate: "logs/deep/app.log", want: false},
		{name: "question", pattern: "file?.txt", candidate: "file1.txt", want: true},
		{name: "question requires one", pattern: "file?.txt", candidate: "file.txt", want: false},
		{name: "class", pattern: "file[abc].txt", candidate: "fileb.txt", want: true},
		{name: "range", pattern: "file[a-z].txt", candidate: "fileq.txt", want: true},
		{name: "bang negated class", pattern: "file[!a-z].txt", candidate: "file7.txt", want: true},
		{name: "caret negated class", pattern: "file[^0-9].txt", candidate: "filex.txt", want: true},
		{name: "negated class rejects member", pattern: "file[!a-z].txt", candidate: "fileq.txt", want: false},
		{name: "leading globstar zero levels", pattern: "**/target", candidate: "target", want: true},
		{name: "leading globstar many levels", pattern: "**/target", candidate: "a/b/target", want: true},
		{name: "middle globstar zero levels", pattern: "a/**/target", candidate: "a/target", want: true},
		{name: "middle globstar many levels", pattern: "a/**/target", candidate: "a/b/c/target", want: true},
		{name: "trailing globstar does not match parent", pattern: "a/**", candidate: "a", want: false},
		{name: "trailing globstar direct child", pattern: "a/**", candidate: "a/target", want: true},
		{name: "trailing globstar many levels", pattern: "a/**", candidate: "a/b/c", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseIgnoreRulesForOS(tt.pattern+"\n", "", 0, "linux")
			if len(rules) != 1 {
				t.Fatalf("parsed %d rules, want 1", len(rules))
			}
			if got := rules[0].matches(tt.candidate, false); got != tt.want {
				t.Errorf("matches(%q) = %t, want %t", tt.candidate, got, tt.want)
			}
		})
	}
}

func TestIgnorePatternEscapesInsideClasses(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
		rejected  string
	}{
		{name: "shorthand letter is literal", pattern: `file[\d].txt`, candidate: "filed.txt", rejected: "file5.txt"},
		{name: "hyphen is literal", pattern: `file[\-].txt`, candidate: "file-.txt", rejected: "filea.txt"},
		{name: "closing bracket is literal", pattern: `file[\]].txt`, candidate: "file].txt", rejected: "filex.txt"},
		{name: "backslash is literal", pattern: `file[\\].txt`, candidate: `file\.txt`, rejected: "filex.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseIgnoreRulesForOS(tt.pattern+"\n", "", 0, "linux")
			if len(rules) != 1 {
				t.Fatalf("parsed %d rules, want 1", len(rules))
			}
			if !rules[0].matches(tt.candidate, false) {
				t.Errorf("pattern %q did not match literal candidate %q", tt.pattern, tt.candidate)
			}
			if rules[0].matches(tt.rejected, false) {
				t.Errorf("pattern %q unexpectedly matched %q", tt.pattern, tt.rejected)
			}
		})
	}
}

func TestIgnorePatternLastMatchWins(t *testing.T) {
	rules := parseIgnoreRulesForOS("*.sql\n!schema.sql\nschema.sql\n!keep.sql\n", "", 3, "linux")
	tests := []struct {
		candidate string
		want      bool
	}{
		{candidate: "data.sql", want: true},
		{candidate: "schema.sql", want: true},
		{candidate: "keep.sql", want: false},
		{candidate: "readme.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.candidate, func(t *testing.T) {
			if got := ignoredByRules(rules, tt.candidate, false); got != tt.want {
				t.Errorf("ignoredByRules(%q) = %t, want %t", tt.candidate, got, tt.want)
			}
		})
	}

	if !rules[1].negated || !rules[3].negated {
		t.Error("leading ! did not set negation metadata")
	}
}

func TestIgnorePatternMalformedBracketIsDiscarded(t *testing.T) {
	rules := parseIgnoreRulesForOS("good\nbad[\nnext\nbad[z-a]\n", "", 0, "linux")
	if len(rules) != 2 {
		t.Fatalf("parsed %d rules, want only 2 valid rules", len(rules))
	}
	if rules[0].pattern != "good" || rules[1].pattern != "next" {
		t.Errorf("valid surrounding rules were not retained: %#v", rules)
	}
}

func TestIgnorePatternPlatformCaseRules(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		value string
		want  bool
	}{
		{name: "linux exact", goos: "linux", value: "Résumé.TXT", want: true},
		{name: "linux case sensitive", goos: "linux", value: "résumé.txt", want: false},
		{name: "darwin case sensitive", goos: "darwin", value: "résumé.txt", want: false},
		{name: "windows unicode fold", goos: "windows", value: "résumé.txt", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseIgnoreRulesForOS("Résumé.TXT\n", "", 0, tt.goos)
			if len(rules) != 1 {
				t.Fatalf("parsed %d rules, want 1", len(rules))
			}
			if got := rules[0].matches(tt.value, false); got != tt.want {
				t.Errorf("matches(%q) on %s = %t, want %t", tt.value, tt.goos, got, tt.want)
			}
		})
	}
}

func TestRepoRootNearestAncestorAndIgnoreOrder(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	repository := filepath.Join(outer, "nested")
	walkRoot := filepath.Join(repository, "src", "service")

	writeIgnoreFixture(t, filepath.Join(outer, ".git", "info", "exclude"), "outer-exclude\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".git", "info", "exclude"), "repo-exclude\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "root-gitignore\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".ignore"), "root-ignore\n")
	writeIgnoreFixture(t, filepath.Join(repository, "src", ".gitignore"), "src-gitignore\n")
	writeIgnoreFixture(t, filepath.Join(walkRoot, ".ignore"), "walk-ignore\n")

	found, ok := findRepositoryRoot(walkRoot)
	if !ok || found != repository {
		t.Fatalf("findRepositoryRoot() = (%q, %t), want (%q, true)", found, ok, repository)
	}

	rules := initialIgnoreRules(walkRoot)
	want := []string{
		"repo-exclude",
		"root-gitignore",
		"root-ignore",
		"src-gitignore",
		"walk-ignore",
	}
	if got := ignorePatterns(rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("initialIgnoreRules() patterns = %q, want %q", got, want)
	}
	for i := 1; i < len(rules); i++ {
		if rules[i].sourcePrecedence <= rules[i-1].sourcePrecedence {
			t.Fatalf("precedence did not increase at rule %d: %#v", i, rules)
		}
	}
}

func TestRepoRootOutsideRepositoryStartsAtWalkRoot(t *testing.T) {
	root := t.TempDir()
	walkRoot := filepath.Join(root, "parent", "walk")
	writeIgnoreFixture(t, filepath.Join(root, "parent", ".gitignore"), "parent-rule\n")
	writeIgnoreFixture(t, filepath.Join(walkRoot, ".gitignore"), "walk-rule\n")

	if found, ok := findRepositoryRoot(walkRoot); ok {
		t.Fatalf("findRepositoryRoot() unexpectedly found %q", found)
	}
	if got := ignorePatterns(initialIgnoreRules(walkRoot)); !reflect.DeepEqual(got, []string{"walk-rule"}) {
		t.Fatalf("initialIgnoreRules() patterns = %q, want only walk-root rules", got)
	}
}

func TestGitDirRelativeAndAbsolutePointers(t *testing.T) {
	tests := []struct {
		name    string
		pointer func(repository, gitDir string) string
	}{
		{
			name: "relative",
			pointer: func(repository, gitDir string) string {
				value, err := filepath.Rel(repository, gitDir)
				if err != nil {
					t.Fatal(err)
				}
				return value
			},
		},
		{
			name: "absolute",
			pointer: func(_, gitDir string) string {
				return gitDir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			repository := filepath.Join(root, "repository")
			gitDir := filepath.Join(root, "gitdirs", tt.name)
			writeIgnoreFixture(t, filepath.Join(gitDir, "info", "exclude"), tt.name+"-exclude\n")
			writeIgnoreFixture(t, filepath.Join(repository, ".git"), "gitdir: "+tt.pointer(repository, gitDir)+"\r\n")

			common, worktree := resolveRepositoryGitDirs(repository)
			if common != "" || worktree != gitDir {
				t.Fatalf("resolveRepositoryGitDirs() = (%q, %q), want (%q, %q)", common, worktree, "", gitDir)
			}
			if got := ignorePatterns(initialIgnoreRules(repository)); !reflect.DeepEqual(got, []string{tt.name + "-exclude"}) {
				t.Fatalf("initialIgnoreRules() patterns = %q", got)
			}
		})
	}
}

func TestCommonDirExcludePrecedence(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	commonDir := filepath.Join(root, "main.git")
	worktreeDir := filepath.Join(commonDir, "worktrees", "repository")
	writeIgnoreFixture(t, filepath.Join(repository, ".git"), "gitdir: "+worktreeDir+"\n")
	writeIgnoreFixture(t, filepath.Join(worktreeDir, "commondir"), "../..\n")
	writeIgnoreFixture(t, filepath.Join(commonDir, "info", "exclude"), "shared\ncommon-only\n")
	writeIgnoreFixture(t, filepath.Join(worktreeDir, "info", "exclude"), "!shared\nworktree-only\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "!worktree-only\nroot-only\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".ignore"), "!root-only\nlocal-only\n")

	common, worktree := resolveRepositoryGitDirs(repository)
	if common != commonDir || worktree != worktreeDir {
		t.Fatalf("resolveRepositoryGitDirs() = (%q, %q), want (%q, %q)", common, worktree, commonDir, worktreeDir)
	}

	rules := initialIgnoreRules(repository)
	want := []string{"shared", "common-only", "!shared", "worktree-only", "!worktree-only", "root-only", "!root-only", "local-only"}
	if got := ignorePatterns(rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("initialIgnoreRules() patterns = %q, want %q", got, want)
	}

	checks := map[string]bool{
		"shared":        false,
		"common-only":   true,
		"worktree-only": false,
		"root-only":     false,
		"local-only":    true,
	}
	for name, wantIgnored := range checks {
		candidate := filepath.Join(repository, name)
		if got := ignoredByRules(rules, candidate, false); got != wantIgnored {
			t.Errorf("ignoredByRules(%q) = %t, want %t", name, got, wantIgnored)
		}
	}
}

func TestGitDirMissingAndMalformedPointersAreEmpty(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "missing target", contents: "gitdir: ../missing\n"},
		{name: "malformed", contents: "not-a-gitdir\n"},
		{name: "empty", contents: "gitdir: \n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := filepath.Join(t.TempDir(), "repository")
			writeIgnoreFixture(t, filepath.Join(repository, ".git"), tt.contents)
			writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "local-rule\n")

			if common, worktree := resolveRepositoryGitDirs(repository); common != "" || worktree != "" {
				t.Fatalf("resolveRepositoryGitDirs() = (%q, %q), want empty", common, worktree)
			}
			if got := ignorePatterns(initialIgnoreRules(repository)); !reflect.DeepEqual(got, []string{"local-rule"}) {
				t.Fatalf("initialIgnoreRules() patterns = %q, want local rule", got)
			}
		})
	}
}

func TestCommonDirAbsentAndUnreadableIgnoreSourcesAreEmpty(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktreeDir := filepath.Join(root, "worktree.git")
	writeIgnoreFixture(t, filepath.Join(repository, ".git"), "gitdir: "+worktreeDir+"\n")
	writeIgnoreFixture(t, filepath.Join(worktreeDir, "info", "exclude"), "worktree-rule\n")
	writeIgnoreFixture(t, filepath.Join(repository, ".gitignore"), "valid-before\nbad[\nvalid-after\n")
	if err := os.MkdirAll(filepath.Join(repository, ".ignore"), 0o755); err != nil {
		t.Fatal(err)
	}

	rules := initialIgnoreRules(repository)
	want := []string{"worktree-rule", "valid-before", "valid-after"}
	if got := ignorePatterns(rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("initialIgnoreRules() patterns = %q, want %q", got, want)
	}
}

package search

import "testing"

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

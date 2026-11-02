package search

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWalkIgnoreInheritancePrecedenceAndPruning(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".git", "info", "exclude"), "excluded.md\nrestored.md\n")
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "!restored.md\n*.log\n!keep.log\nsame.md\npruned/\n!pruned/recovered.md\n")
	writeIgnoreFixture(t, filepath.Join(root, ".ignore"), "!same.md\noverride.md\n")
	writeIgnoreFixture(t, filepath.Join(root, "child", ".gitignore"), "!debug.log\n")
	writeIgnoreFixture(t, filepath.Join(root, "child", ".ignore"), "local.md\n")

	for _, relative := range []string{
		"excluded.md",
		"restored.md",
		"keep.log",
		"same.md",
		"override.md",
		"visible.md",
		"pruned/recovered.md",
		"child/debug.log",
		"child/local.md",
		"child/visible.md",
	} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	got := collectCandidates(t, root, []string{"-g", "*.md", "-g", "*.log"}, nil, WalkOptions{})
	want := []string{"child/debug.log", "child/visible.md", "keep.log", "restored.md", "same.md", "visible.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestWalkIgnoreNoIgnoreStillUsesHardcodedSkips(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "ignored.md\n")
	writeIgnoreFixture(t, filepath.Join(root, ".ignore"), "also-ignored.md\n")
	writeIgnoreFixture(t, filepath.Join(root, ".git", "info", "exclude"), "excluded.md\n")
	for _, relative := range []string{
		"ignored.md",
		"also-ignored.md",
		"excluded.md",
		"vendor/hidden.md",
		"node_modules/hidden.md",
	} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{NoIgnore: true})
	want := []string{"also-ignored.md", "excluded.md", "ignored.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestNestedRepoDirectoryResetsParentIgnoreChain(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "nested/\n*.bak\n")
	if err := os.MkdirAll(filepath.Join(root, "nested", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeIgnoreFixture(t, filepath.Join(root, "nested", ".gitignore"), "local.md\n")
	for _, relative := range []string{"nested/main.md", "nested/temp.bak", "nested/local.md"} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	got := collectCandidates(t, root, []string{"-g", "*.md", "-g", "*.bak"}, nil, WalkOptions{})
	want := []string{"nested/main.md", "nested/temp.bak"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestNestedRepoGitfileResetsAndLoadsOwnExclude(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	gitDir := filepath.Join(root, "gitdirs", "nested")
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "nested/\nparent-only.md\n")
	writeIgnoreFixture(t, filepath.Join(nested, ".git"), "gitdir: "+gitDir+"\n")
	writeIgnoreFixture(t, filepath.Join(gitDir, "info", "exclude"), "excluded.md\n")
	for _, relative := range []string{"nested/main.md", "nested/parent-only.md", "nested/excluded.md"} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	want := []string{"nested/main.md", "nested/parent-only.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestPathScopeExemptionSuppressesContainerRules(t *testing.T) {
	for name, containerRules := range map[string]string{
		"directory only":     "RepoA/\nRepoB/\n",
		"literal basename":   "RepoA\nRepoB\n",
		"anchored literal":   "/RepoA\n/RepoB\n",
		"wildcard directory": "RepoA*/\nRepoB*/\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), containerRules+"*.bak\n")
			writeIgnoreFixture(t, filepath.Join(root, "RepoA", ".gitignore"), "child.md\n")
			for _, relative := range []string{
				"RepoA/main.md",
				"RepoA/RepoA/nested.md",
				"RepoA/temp.bak",
				"RepoA/child.md",
				"RepoB/main.md",
			} {
				writeWalkFixture(t, root, relative, "anchor")
			}

			got := collectCandidates(
				t,
				root,
				[]string{"-g", "*.md", "-g", "*.bak"},
				[]string{"RepoA/"},
				WalkOptions{},
			)
			if !reflect.DeepEqual(got, []string{"RepoA/RepoA/nested.md", "RepoA/main.md"}) {
				t.Fatalf("candidates = %v, want exempt child with general and child rules retained", got)
			}
		})
	}
}

func TestPathScopeExemptionRetainsRulesThatMatchChildName(t *testing.T) {
	tests := []struct {
		name      string
		ignore    string
		child     string
		candidate string
	}{
		{
			name:      "basename glob",
			ignore:    "*.bak\n",
			child:     "app.bak",
			candidate: "app.bak/temp.bak",
		},
		{
			name:      "match all",
			ignore:    "*\n",
			child:     "app",
			candidate: "app/visible.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), tt.ignore)
			writeWalkFixture(t, root, tt.candidate, "anchor")

			got := collectCandidates(
				t,
				root,
				[]string{"-g", "*.md", "-g", "*.bak"},
				[]string{tt.child + "/"},
				WalkOptions{},
			)
			if len(got) != 0 {
				t.Fatalf("candidates = %v, want inherited ignore rule retained", got)
			}
		})
	}
}

func TestPathScopeExemptionWildcardFirstSegment(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "TeamOne/\nTeamTwo/\nOther/\n")
	for _, relative := range []string{"TeamOne/src/main.go", "TeamTwo/src/main.go", "Other/src/main.go"} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	got := collectCandidates(t, root, []string{"-g", "*.go"}, []string{"Team*/src/main.go"}, WalkOptions{})
	want := []string{"TeamOne/src/main.go", "TeamTwo/src/main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestPathScopeExemptionPreservesWhitelistChain(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "*\n!*/\n!*.go\n")
	writeWalkFixture(t, root, "RepoA/allowed.go", "anchor")
	writeWalkFixture(t, root, "RepoA/ignored.md", "anchor")

	unscoped := collectCandidates(t, root, []string{"-g", "*.go", "-g", "*.md"}, nil, WalkOptions{})
	scoped := collectCandidates(t, root, []string{"-g", "*.go", "-g", "*.md"}, []string{"RepoA/"}, WalkOptions{})
	want := []string{"RepoA/allowed.go"}
	if !reflect.DeepEqual(unscoped, want) || !reflect.DeepEqual(scoped, unscoped) {
		t.Fatalf("unscoped/scoped candidates = %v/%v, want identical whitelist result %v", unscoped, scoped, want)
	}
}

func TestPathScopeExemptionNonExemptPatternsDoNotBypassParentIgnore(t *testing.T) {
	tests := []struct {
		name  string
		scope string
	}{
		{name: "globstar first segment", scope: "**/app/"},
		{name: "dotted basename", scope: "*.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "RepoA/\n")
			writeWalkFixture(t, root, "RepoA/app/main.go", "anchor")

			if got := collectCandidates(t, root, []string{"-g", "*.go"}, []string{tt.scope}, WalkOptions{}); len(got) != 0 {
				t.Fatalf("candidates = %v, want ignored child to remain pruned", got)
			}
		})
	}
}

func TestIgnoreChainSharesUntilLocalRulesRequireCopy(t *testing.T) {
	root := t.TempDir()
	emptyChild := filepath.Join(root, "empty")
	localChild := filepath.Join(root, "local")
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "parent.md\n")
	if err := os.MkdirAll(emptyChild, 0o755); err != nil {
		t.Fatal(err)
	}
	writeIgnoreFixture(t, filepath.Join(localChild, ".gitignore"), "child.md\n")

	parent := initialIgnoreChain(root)
	shared := parent.withLocalFiles(emptyChild)
	if len(parent.rules) != 1 || len(shared.rules) != 1 {
		t.Fatalf("parent/shared rule counts = %d/%d, want 1/1", len(parent.rules), len(shared.rules))
	}
	if &parent.rules[0] != &shared.rules[0] {
		t.Fatalf("empty child did not share inherited rules: parent=%p shared=%p", &parent.rules[0], &shared.rules[0])
	}

	derived := shared.withLocalFiles(localChild)
	if &derived.rules[0] == &parent.rules[0] {
		t.Fatal("local rules reused the inherited backing slice")
	}
	if got := ignorePatterns(parent.rules); !reflect.DeepEqual(got, []string{"parent.md"}) {
		t.Fatalf("deriving child chain mutated parent rules: %v", got)
	}
	if got := ignorePatterns(shared.rules); !reflect.DeepEqual(got, []string{"parent.md"}) {
		t.Fatalf("deriving child chain mutated shared sibling rules: %v", got)
	}
	if got := ignorePatterns(derived.rules); !reflect.DeepEqual(got, []string{"parent.md", "child.md"}) {
		t.Fatalf("derived rules = %v, want inherited plus local", got)
	}
}

func TestWalkIgnoreUsesPhysicalPathsForLinkedRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	target := filepath.Join(repo, "sub")
	logical := filepath.Join(base, "logical")
	writeIgnoreFixture(t, filepath.Join(repo, ".git", "info", "exclude"), "excluded.md\n")
	writeIgnoreFixture(t, filepath.Join(repo, ".gitignore"), "ignored.md\n")
	writeWalkFixture(t, target, "excluded.md", "anchor")
	writeWalkFixture(t, target, "ignored.md", "anchor")
	writeWalkFixture(t, target, "visible.md", "anchor")
	makeTestSymlink(t, target, logical)

	var visited []string
	if err := walkCandidates([]string{"-g", "*.md"}, logical, nil, WalkOptions{}, func(path string, _ os.FileInfo) error {
		visited = append(visited, path)
		return nil
	}); err != nil {
		t.Fatalf("walkCandidates() error = %v", err)
	}
	want := []string{filepath.Join(logical, "visible.md")}
	if !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited = %v, want physical-root ignore rules with logical output %v", visited, want)
	}
}

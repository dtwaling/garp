package search

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"garp/config"
)

func writeWalkFixture(t *testing.T, root, relative, content string) string {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestWalkCandidatesRejectsMultiRootCompatibilityInput(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	err := walkCandidates(nil, first+","+second, nil, WalkOptions{}, func(string, os.FileInfo) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "single-root walk") {
		t.Fatalf("walkCandidates multi-root error = %v, want explicit rejection", err)
	}
}

func TestWalkAbsolutePositiveScopeExemptsIgnoredDirectChildAndPrunesSibling(t *testing.T) {
	root := t.TempDir()
	target := writeWalkFixture(t, root, "ignored/match.txt", "needle")
	writeWalkFixture(t, root, "ignored-sibling/other.txt", "needle")
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(root, "ignored", "**")}
	var visited []string
	err := walkCandidates(
		[]string{"-g", "*.txt"},
		root,
		pathScope,
		WalkOptions{},
		func(path string, _ os.FileInfo) error {
			visited = append(visited, path)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visited, []string{target}) {
		t.Fatalf("visited = %q, want ignored target only", visited)
	}
	policy, err := CompilePathPolicy(pathScope, nil, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.exemptsDirectChild("ignored") {
		t.Fatal("absolute scope did not preserve root-relative direct-child metadata")
	}
	if policy.exemptsDirectChild("ignored-sibling") {
		t.Fatal("absolute scope falsely exempted sibling-name directory")
	}
	if policy.CouldMatchDirectory(filepath.Join(root, "ignored-sibling")) {
		t.Fatal("absolute scope did not prune unrelated sibling")
	}
}

func TestWalkAbsoluteWildcardAboveRootExemptsIgnoredDirectChild(t *testing.T) {
	root := t.TempDir()
	target := writeWalkFixture(t, root, "ignored/match.txt", "needle")
	writeWalkFixture(t, root, "other/miss.txt", "needle")
	if err := os.WriteFile(filepath.Join(root, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(filepath.Dir(root), "*", "ignored", "**")}
	var visited []string
	err := walkCandidates(
		[]string{"-g", "*.txt"},
		root,
		pathScope,
		WalkOptions{},
		func(path string, _ os.FileInfo) error {
			visited = append(visited, path)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visited, []string{target}) {
		t.Fatalf("visited = %q, want ignored target only", visited)
	}
}

func TestWalkRelativeNegativePatternsDoNotPruneSyntheticRoot(t *testing.T) {
	for _, pattern := range []string{"!.*", "!.*/", "!*"} {
		t.Run(pattern, func(t *testing.T) {
			root := t.TempDir()
			visible := writeWalkFixture(t, root, "visible.txt", "needle")
			writeWalkFixture(t, root, ".hidden/inside.txt", "needle")
			policy, err := CompilePathPolicy([]string{pattern}, nil, root, root)
			if err != nil {
				t.Fatal(err)
			}
			if !policy.CouldMatchRoot() {
				t.Fatalf("relative negative %q pruned the synthetic root", pattern)
			}
			var visited []string
			err = walkCandidates(
				[]string{"-g", "*.txt"},
				root,
				[]string{pattern},
				WalkOptions{},
				func(path string, _ os.FileInfo) error {
					visited = append(visited, path)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if pattern != "!*" && !reflect.DeepEqual(visited, []string{visible}) {
				t.Fatalf("visited = %q, want visible root file only", visited)
			}
			if pattern == "!*" && len(visited) != 0 {
				t.Fatalf("visited = %q, want wildcard-negative descendants excluded", visited)
			}
		})
	}
}

func collectCandidates(t *testing.T, root string, fileTypes, pathScope []string, options WalkOptions) []string {
	t.Helper()
	var paths []string
	if err := walkCandidates(fileTypes, root, pathScope, options, func(path string, _ os.FileInfo) error {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatalf("walkCandidates() error = %v", err)
	}
	return paths
}

func TestCandidateWalkRootNamedVendorIsTraversed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vendor")
	writeWalkFixture(t, root, "document.md", "anchor")

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"document.md"}) {
		t.Fatalf("candidates = %v, want root file", got)
	}
}

func TestCandidateWalkSkipsHardcodedDescendants(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "keep/document.md", "anchor")
	writeWalkFixture(t, root, "vendor/hidden.md", "anchor")
	writeWalkFixture(t, root, "node_modules/hidden.md", "anchor")

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{NoIgnore: true})
	if !reflect.DeepEqual(got, []string{"keep/document.md"}) {
		t.Fatalf("candidates = %v, want only non-skipped descendant", got)
	}
}

func TestCandidateWalkAbsoluteScopeCannotOverrideHardSkip(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "vendor/hidden.md", "anchor")
	writeWalkFixture(t, root, "keep/visible.md", "anchor")

	got := collectCandidates(
		t,
		root,
		[]string{"-g", "*.md"},
		[]string{filepath.Join(root, "vendor")},
		WalkOptions{NoIgnore: true},
	)
	if len(got) != 0 {
		t.Fatalf("absolute vendor scope bypassed hard skip: %v", got)
	}
}

func TestCandidateWalkSkipsDescendantLinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	targetDir := filepath.Join(base, "target")
	targetFile := writeWalkFixture(t, targetDir, "linked.md", "anchor")
	writeWalkFixture(t, root, "keep.md", "anchor")
	makeTestSymlink(t, targetDir, filepath.Join(root, "linked-dir"))
	makeTestSymlink(t, targetFile, filepath.Join(root, "linked-file.md"))

	got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"keep.md"}) {
		t.Fatalf("candidates = %v, want descendant links skipped", got)
	}
}

func TestDescendantLinkEntryTypeHints(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, hint := range [][]os.FileMode{nil, {0}, {os.ModeDir}, {os.ModeIrregular}, {os.ModeSymlink}} {
		_, err := isDescendantLink(missing, hint...)
		wantErr := runtime.GOOS == "windows" || len(hint) == 0 || hint[0] == os.ModeIrregular || hint[0] == os.ModeSymlink
		if (err != nil) != wantErr {
			t.Errorf("hint %v: error = %v, want error %t", hint, err, wantErr)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	makeTestSymlink(t, missing, link)
	for _, hint := range [][]os.FileMode{nil, {os.ModeIrregular}, {os.ModeSymlink}} {
		if linked, err := isDescendantLink(link, hint...); err != nil || !linked {
			t.Errorf("link with hint %v: linked = %t, error = %v", hint, linked, err)
		}
	}
}

func TestCandidateWalkSkipsDeletedDescendant(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	deleted := filepath.Join(root, "z-deleted")
	writeWalkFixture(t, deleted, "document.md", "anchor")

	var got []string
	err := walkCandidates(nil, root, nil, WalkOptions{}, func(path string, _ os.FileInfo) error {
		got = append(got, filepath.Base(path))
		if err := os.RemoveAll(deleted); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walkCandidates() error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("candidates = %v, want deleted descendant skipped", got)
	}
}

func TestCandidateWalkSkipsUnreadableDescendant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows mode bits do not make a directory unreadable")
	}

	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	unreadable := filepath.Join(root, "z-unreadable")
	writeWalkFixture(t, unreadable, "document.md", "anchor")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if directory, err := os.Open(unreadable); err == nil {
		_ = directory.Close()
		t.Skip("current user can still read a mode-zero directory")
	}

	got := collectCandidates(t, root, nil, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("candidates = %v, want unreadable descendant skipped", got)
	}
}

func TestCandidateWalkDeterministicDepthFirstOrder(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"z.md", "b/two.md", "a/three.md", "a/one.md"} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	want := []string{"a/one.md", "a/three.md", "b/two.md", "z.md"}
	for i := 0; i < 3; i++ {
		if got := collectCandidates(t, root, nil, nil, WalkOptions{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d candidates = %v, want %v", i, got, want)
		}
	}
}

func TestCandidateWalkFiltersScopeAndSize(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "docs/keep.md", "anchor")
	writeWalkFixture(t, root, "docs/wrong.txt", "anchor")
	writeWalkFixture(t, root, "other/out.md", "anchor")
	large := writeWalkFixture(t, root, "docs/large.md", "anchor")
	if err := os.Truncate(large, 32); err != nil {
		t.Fatal(err)
	}

	got := collectCandidates(
		t,
		root,
		[]string{"-g", "*.md"},
		[]string{"docs/"},
		WalkOptions{MaxFileSize: 16},
	)
	if !reflect.DeepEqual(got, []string{"docs/keep.md"}) {
		t.Fatalf("candidates = %v, want filtered file", got)
	}
}

func TestCandidateWalkDefaultSizePolicy(t *testing.T) {
	root := t.TempDir()
	largeText := writeWalkFixture(t, root, "large.md", "anchor")
	largeBinary := writeWalkFixture(t, root, "large.pdf", "anchor")
	for _, filename := range []string{largeText, largeBinary} {
		if err := os.Truncate(filename, defaultTextMaxBytes+1); err != nil {
			t.Fatal(err)
		}
	}

	got := collectCandidates(t, root, nil, nil, WalkOptions{})
	if !reflect.DeepEqual(got, []string{"large.pdf"}) {
		t.Fatalf("candidates = %v, want only default-uncapped binary", got)
	}
	if got := (WalkOptions{}).TextMaxBytes(); got != defaultTextMaxBytes {
		t.Fatalf("TextMaxBytes() = %d, want %d", got, defaultTextMaxBytes)
	}
}

func TestCountDiscoveryParity(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "a.md", "anchor")
	writeWalkFixture(t, root, "nested/b.md", "anchor")
	writeWalkFixture(t, root, "nested/c.txt", "anchor")
	options := WalkOptions{MaxFileSize: 64}
	types := []string{"-g", "*.md"}

	count, err := GetDocumentFileCountWithOptions(types, root, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := FindFilesWithFirstWordPartialWithOptions("anchor", types, root, nil, PartialModeOff, options)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"anchor"}, types, 2, nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(plain) != count || len(progress) != count {
		t.Fatalf("count/plain/progress = %d/%d/%d, want parity", count, len(plain), len(progress))
	}
}

func TestPositiveGlobAddsKnownCodeEligibilityOnly(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "image.png", "anchor")
	writeWalkFixture(t, root, "arbitrary.zzz", "anchor")
	writeWalkFixture(t, root, "readme.md", "anchor")

	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*"}},
		PositiveGlobCodeEligibility: true,
	}
	got := collectCandidates(t, root, config.BuildRipgrepFileTypes(false), nil, options)
	want := []string{"main.go", "readme.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want documents plus known code only %v", got, want)
	}
}

func TestGlobPolicyRemainsSeparateFromTypeGate(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "main.md", "anchor")
	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*.go"}},
		PositiveGlobCodeEligibility: true,
	}
	if got := collectCandidates(t, root, config.BuildRipgrepFileTypes(false), nil, options); !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Fatalf("positive code glob candidates = %v, want [main.go]", got)
	}
	options.PositiveGlobCodeEligibility = false
	if got := collectCandidates(t, root, []string{"-g", "*.md"}, nil, options); len(got) != 0 {
		t.Fatalf("--only override candidates = %v, want none", got)
	}
}

func TestCodeTypeGateIntersectsUserGlobs(t *testing.T) {
	root := t.TempDir()
	writeWalkFixture(t, root, "main.go", "anchor")
	writeWalkFixture(t, root, "main.ts", "anchor")
	writeWalkFixture(t, root, "readme.md", "anchor")

	options := WalkOptions{
		UserGlobs:                   []UserGlob{{Pattern: "*.go"}},
		PositiveGlobCodeEligibility: true,
	}
	got := collectCandidates(t, root, config.BuildRipgrepFileTypes(true), nil, options)
	if !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Fatalf("--code with user glob candidates = %v, want [main.go]", got)
	}
}

func TestStartDirErrorsRemainFatal(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	file := writeWalkFixture(t, base, "file.md", "anchor")

	for _, root := range []string{missing, file} {
		if _, err := GetDocumentFileCount(nil, root, nil); err == nil {
			t.Errorf("GetDocumentFileCount(%q) accepted invalid root", root)
		}
		if _, err := FindFilesWithFirstWordPartial("anchor", nil, root, nil, PartialModeOff); err == nil {
			t.Errorf("FindFilesWithFirstWordPartial(%q) accepted invalid root", root)
		}
	}
}

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

func TestWalkFileTypeAndPolicyGatedBeforeIgnore(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "ignored.*\n!restored.md\n*.png\n!keep.png\npruned/\n")
	for _, relative := range []string{
		"docs/ignored.md",
		"docs/ignored.go",
		"docs/keep.md",
		"docs/keep.png",
		"docs/main.go",
		"docs/notes.txt",
		"docs/policy-excluded.md",
		"docs/pruned/keep.md",
		"docs/restored.md",
		"docs/sub/keep.md",
		"outside/keep.md",
		"root.md",
	} {
		writeWalkFixture(t, root, relative, "anchor")
	}

	for _, tt := range []struct {
		name     string
		noIgnore bool
		code     bool
		want     []string
	}{
		{
			name: "ignore enabled",
			want: []string{"docs/keep.md", "docs/restored.md", "docs/sub/keep.md"},
		},
		{
			name: "positive glob code eligibility",
			code: true,
			want: []string{"docs/keep.md", "docs/main.go", "docs/restored.md", "docs/sub/keep.md"},
		},
		{
			name:     "no ignore retains type and policy gates",
			noIgnore: true,
			want:     []string{"docs/ignored.md", "docs/keep.md", "docs/pruned/keep.md", "docs/restored.md", "docs/sub/keep.md"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := collectCandidates(t, root, []string{"-g", "*.md"}, []string{"docs/"}, WalkOptions{
				NoIgnore:                    tt.noIgnore,
				PositiveGlobCodeEligibility: tt.code,
				UserGlobs: []UserGlob{
					{Pattern: "*.md"},
					{Pattern: "*.go"},
					{Pattern: "*.png"},
					{Pattern: "policy-excluded.md", Negated: true},
				},
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("candidates = %v, want %v", got, tt.want)
			}
		})
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

func TestPathScopeExemptionBareWildcardDirectChild(t *testing.T) {
	tests := []struct {
		scope string
		want  []string
	}{
		{scope: "Team*", want: []string{"Team1/main.md", "TeamA/main.md", "TeamLong/main.md"}},
		{scope: "Team?", want: []string{"Team1/main.md", "TeamA/main.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			root := t.TempDir()
			writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "Team1/\nTeamA/\nTeamLong/\n")
			for _, relative := range []string{"Team1/main.md", "TeamA/main.md", "TeamLong/main.md"} {
				writeWalkFixture(t, root, relative, "anchor")
			}

			got := collectCandidates(t, root, []string{"-g", "*.md"}, []string{tt.scope}, WalkOptions{})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("candidates = %v, want bare wildcard direct-child exemptions %v", got, tt.want)
			}
		})
	}
}

func TestNegativeBareWildcardDoesNotExemptIgnoredChild(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "TeamA/\n")
	writeWalkFixture(t, root, "TeamA/main.md", "anchor")

	got := collectCandidates(t, root, []string{"-g", "*.md"}, []string{"!Team*"}, WalkOptions{})
	if len(got) != 0 {
		t.Fatalf("candidates = %v, want negative bare wildcard to preserve ignore pruning", got)
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

func TestNegativePathScopeDoesNotExemptIgnoredContainer(t *testing.T) {
	root := t.TempDir()
	writeIgnoreFixture(t, filepath.Join(root, ".gitignore"), "RepoA/\n")
	writeWalkFixture(t, root, "RepoA/main.md", "anchor")

	got := collectCandidates(
		t,
		root,
		[]string{"-g", "*.md"},
		[]string{"!RepoA/**"},
		WalkOptions{},
	)
	if len(got) != 0 {
		t.Fatalf("candidates = %v, want ignored container to remain pruned", got)
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

func TestWalkPrunesVirtualEnvironments(t *testing.T) {
	for _, noIgnore := range []bool{false, true} {
		name := "ignore"
		if noIgnore {
			name = "no-ignore"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{".venv", "venv"} {
				if !config.ShouldSkipDirectory(dir) {
					t.Errorf("%q is not a hard skip", dir)
				}
				writeWalkFixture(t, root, dir+"/nested/hidden.txt", "anchor")
				writeWalkFixture(t, root, "project/"+dir+"/hidden.txt", "anchor")
			}
			writeWalkFixture(t, root, ".config/visible.txt", "anchor")
			writeWalkFixture(t, root, "project/visible.txt", "anchor")
			options := WalkOptions{NoIgnore: noIgnore}
			want := []string{".config/visible.txt", "project/visible.txt"}
			if got := collectCandidates(t, root, nil, nil, options); !reflect.DeepEqual(got, want) {
				t.Fatalf("walk = %v, want %v", got, want)
			}
			count, err := GetDocumentFileCountWithOptions(nil, root, nil, options)
			if err != nil || count != len(want) {
				t.Fatalf("count = %d, %v; want %d", count, err, len(want))
			}
		})
	}
}

func TestExtensionExclusionsBeforeDiscoveryAndCount(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"skip.txt", "skip.TXT", "skip.EmL", "skip.PDF", "skip.go", "keep.md", "nested/keep.md", "keep.txt.md"} {
		writeWalkFixture(t, root, name, "anchor forbidden")
	}
	excludes := []string{".tXt", ".EML", ".pdf", ".GO", "forbidden", "md", ""}
	options := WalkOptions{ExcludeWords: excludes, UserGlobs: []UserGlob{{Pattern: "*"}}, PositiveGlobCodeEligibility: true}
	want := []string{"keep.md", "keep.txt.md", "nested/keep.md"}
	if got := collectCandidates(t, root, nil, nil, options); !reflect.DeepEqual(got, want) {
		t.Fatalf("walk visitor received %v, want %v", got, want)
	}
	count, err := GetDocumentFileCountWithOptions(nil, root, nil, options)
	if err != nil || count != len(want) {
		t.Fatalf("count = %d, %v; want %d", count, err, len(want))
	}
	roots, err := PrepareStartRoots([]string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err = GetDocumentFileCountForRoots(nil, roots, nil, options)
	if err != nil || count != len(want) {
		t.Fatalf("root count = %d, %v; want %d", count, err, len(want))
	}
	paths, err := FindFilesWithFirstWordPartialWithOptions("anchor", nil, root, nil, PartialModeOff, options)
	if err != nil {
		t.Fatal(err)
	}
	assertExclusionPaths(t, root, paths, want)
	se := NewSearchEngine([]string{"anchor"}, excludes, nil, false, 1, 1000)
	se.StartDir, se.Silent = root, true
	se.UserGlobs, se.PositiveGlobCodeEligibility = options.UserGlobs, true
	var progressPaths []string
	se.OnProgress = func(stage string, processed, total int, path string) {
		if stage == "discovery" {
			progressPaths = append(progressPaths, path)
		}
	}
	paths, total, err := se.DiscoverCandidates(count)
	if err != nil || total != len(want) {
		t.Fatalf("discovery total = %d, %v; want %d", total, err, len(want))
	}
	assertExclusionPaths(t, root, paths, want)
	assertExclusionPaths(t, root, progressPaths, want)
	// Stage 2 still rejects extension exclusions for callers supplying candidates directly.
	se.ExcludeWords = []string{".tXt"}
	matches, err := se.FilterCandidates([]string{filepath.Join(root, "skip.TXT")}, 1, time.Now())
	if err != nil || len(matches) != 0 {
		t.Fatalf("direct Stage 2 = %v, %v; want empty", matches, err)
	}
}

func assertExclusionPaths(t *testing.T, root string, paths, want []string) {
	t.Helper()
	got := relativeCandidatePaths(t, root, paths)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

package search

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareStartRoots(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	other := filepath.Join(t.TempDir(), "other")
	comma := filepath.Join(t.TempDir(), "comma,root")
	for _, directory := range []string{child, other, comma} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("omitted defaults to current directory", func(t *testing.T) {
		roots, err := PrepareStartRoots(nil, false)
		if err != nil || len(roots) != 1 || roots[0].Supplied {
			t.Fatalf("roots = %+v, err = %v", roots, err)
		}
	})
	t.Run("repeated and comma separated", func(t *testing.T) {
		roots, err := PrepareStartRoots([]string{parent + "," + other, parent}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 {
			t.Fatalf("root count = %d, want 2", len(roots))
		}
	})
	t.Run("quoted comma segments", func(t *testing.T) {
		roots, err := PrepareStartRoots([]string{`"` + parent + `","` + other + `"`}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 || roots[0].LogicalPath != parent || roots[1].LogicalPath != other {
			t.Fatalf("roots = %+v, want quoted parent and other", roots)
		}
	})
	t.Run("existing comma name stays whole", func(t *testing.T) {
		roots, err := PrepareStartRoots([]string{comma}, false)
		if err != nil || len(roots) != 1 || roots[0].LogicalPath != comma {
			t.Fatalf("roots = %+v, err = %v", roots, err)
		}
	})
	t.Run("nested collapse is input order independent", func(t *testing.T) {
		for _, raw := range [][]string{{parent, child}, {child, parent}} {
			roots, err := PrepareStartRoots(raw, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(roots) != 1 || roots[0].LogicalPath != parent {
				t.Fatalf("roots = %+v, want parent only", roots)
			}
		}
	})
	t.Run("nested retained for relative policy", func(t *testing.T) {
		roots, err := PrepareStartRoots([]string{parent, child}, true)
		if err != nil || len(roots) != 2 {
			t.Fatalf("roots = %+v, err = %v", roots, err)
		}
		if len(ReportedStartRoots(roots)) != 2 {
			t.Fatalf("positive relative policy roots = %+v, want both reported", roots)
		}
	})
	t.Run("invalid segments fail the set", func(t *testing.T) {
		for _, raw := range [][]string{{parent + ",," + other}, {parent, filepath.Join(parent, "missing")}} {
			if _, err := PrepareStartRoots(raw, false); err == nil {
				t.Fatalf("PrepareStartRoots(%q) error = nil", raw)
			}
		}
	})
}

func TestPrepareStartRootsCanonicalDuplicates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	roots, err := PrepareStartRoots([]string{link, root}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].LogicalPath != link {
		t.Fatalf("roots = %+v, want first logical root", roots)
	}
}

func TestPrepareStartRootsNestedTraversalAndReporting(t *testing.T) {
	tests := []struct {
		name          string
		childName     string
		ignore        string
		wantTraversal int
	}{
		{name: "reachable", childName: "child", wantTraversal: 1},
		{name: "hard skipped", childName: "node_modules", wantTraversal: 2},
		{name: "hidden hard skipped", childName: ".cache", wantTraversal: 2},
		{name: "ignored", childName: "ignored", ignore: "ignored/\n", wantTraversal: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, test.childName)
			if err := os.MkdirAll(child, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.ignore != "" {
				if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte(test.ignore), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRoots(raw, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(roots) != test.wantTraversal {
					t.Fatalf("PrepareStartRoots(%q) traversal count = %d, want %d", raw, len(roots), test.wantTraversal)
				}
				reported := ReportedStartRoots(roots)
				if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("PrepareStartRoots(%q) reported roots = %+v, want parent only", raw, reported)
				}
				if test.wantTraversal == 2 {
					foundTraversalChild := false
					for _, root := range roots {
						if root.LogicalPath == child && !root.Reported {
							foundTraversalChild = true
						}
					}
					if !foundTraversalChild {
						t.Fatalf("PrepareStartRoots(%q) did not retain child for traversal", raw)
					}
				}
			}
		})
	}
}

func TestPrepareStartRootsRetainsChildrenPrunedByNegativePolicies(t *testing.T) {
	tests := []struct {
		name      string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "negative pathscope", pathScope: []string{"!blocked/**"}},
		{name: "negative glob", userGlobs: []UserGlob{{Pattern: "blocked/**", Negated: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "blocked", "child")
			if err := os.MkdirAll(child, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(test.pathScope, test.userGlobs))
				if err != nil {
					t.Fatal(err)
				}
				roots = ApplyStartRootOptions(roots, false, StartRootOptions{
					PathScope: test.pathScope,
					UserGlobs: test.userGlobs,
				})
				if len(roots) != 2 {
					t.Fatalf("roots %q traversal count = %d, want 2", raw, len(roots))
				}
				reported := ReportedStartRoots(roots)
				if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
			}
		})
	}
}

func TestPrepareStartRootsNoIgnoreCollapsesIgnoredChild(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "ignored")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := PrepareStartRoots([]string{parent, child}, false)
	if err != nil {
		t.Fatal(err)
	}
	roots = ApplyStartRootOptions(roots, false, StartRootOptions{NoIgnore: true})
	if len(roots) != 1 || roots[0].LogicalPath != parent {
		t.Fatalf("roots = %+v, want reachable parent only with no-ignore", roots)
	}
}

func TestPrepareStartRootsThreeLevelCollapseUsesOnlyKeptAncestors(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	grandchild := filepath.Join(child, "ignored")
	if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(grandchild, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored", "!zzz"}
	permutations := [][]string{
		{parent, child, grandchild},
		{parent, grandchild, child},
		{child, parent, grandchild},
		{child, grandchild, parent},
		{grandchild, parent, child},
		{grandchild, child, parent},
	}
	for _, raw := range permutations {
		roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
		if err != nil {
			t.Fatal(err)
		}
		roots = ApplyStartRootOptions(roots, RetainNestedStartRoots(pathScope, nil), StartRootOptions{PathScope: pathScope})
		if len(roots) != 3 {
			t.Fatalf("roots %q traversal count = %d, want all exemption-sensitive roots", raw, len(roots))
		}
		got := map[string]bool{}
		for _, root := range roots {
			got[root.LogicalPath] = true
		}
		if !got[parent] || !got[child] || !got[grandchild] {
			t.Fatalf("roots %q traversal set = %+v, want parent, child, and grandchild", raw, roots)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want stable parent only", raw, reported)
		}
	}
}

func TestPrepareStartRootsRetainsDepthZeroExemptionChildren(t *testing.T) {
	for _, pathScope := range [][]string{{"ignored"}, {"ignored/**"}} {
		t.Run(strings.Join(pathScope, ","), func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "c")
			if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(child, "ignored"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
				if err != nil {
					t.Fatal(err)
				}
				roots = ApplyStartRootOptions(
					roots,
					RetainNestedStartRoots(pathScope, nil),
					StartRootOptions{PathScope: pathScope},
				)
				if len(roots) != 2 {
					t.Fatalf("roots %q traversal count = %d, want 2", raw, len(roots))
				}
				reported := ReportedStartRoots(roots)
				if RetainNestedStartRoots(pathScope, nil) {
					if len(reported) != 2 || reported[0].LogicalPath != raw[0] || reported[1].LogicalPath != raw[1] {
						t.Fatalf("roots %q reported = %+v, want both in supplied order", raw, reported)
					}
				} else if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
			}
		})
	}
}

func TestApplyStartRootOptionsReportsRelativePolicyRootsInSuppliedOrder(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "anchored pathscope", pathScope: []string{"docs/**"}},
		{name: "anchored user glob", userGlobs: []UserGlob{{Pattern: "src/**"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				retainNested := RetainNestedStartRoots(test.pathScope, test.userGlobs)
				if !retainNested {
					t.Fatal("relative policy did not request nested-root retention")
				}
				roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(test.pathScope, test.userGlobs))
				if err != nil {
					t.Fatal(err)
				}
				roots = ApplyStartRootOptions(roots, retainNested, StartRootOptions{
					PathScope: test.pathScope,
					UserGlobs: test.userGlobs,
				})
				reported := ReportedStartRoots(roots)
				if len(reported) != 2 {
					t.Fatalf("roots %q reported count = %d, want 2", raw, len(reported))
				}
				for index := range raw {
					if reported[index].LogicalPath != raw[index] {
						t.Fatalf("roots %q reported[%d] = %q, want supplied order", raw, index, reported[index].LogicalPath)
					}
				}
			}
		})
	}
}

func TestPrepareStartRootsCanonicalPlatformRules(t *testing.T) {
	path := filepath.Join("Root", "Child")
	if canonicalPathKeyForPlatform(path, false) == canonicalPathKeyForPlatform(strings.ToLower(path), false) {
		t.Fatal("POSIX canonical keys folded case")
	}
	if canonicalPathKeyForPlatform(path, true) != canonicalPathKeyForPlatform(strings.ToLower(path), true) {
		t.Fatal("Windows canonical keys did not fold case")
	}
}

func TestRetainNestedStartRoots(t *testing.T) {
	if RetainNestedStartRoots([]string{"*.md"}, nil) {
		t.Fatal("basename scope retained nested roots")
	}
	if !RetainNestedStartRoots([]string{"docs/*.md"}, nil) {
		t.Fatal("relative anchored scope did not retain nested roots")
	}
	if !RetainNestedStartRoots(nil, []UserGlob{{Pattern: "./src/**"}}) {
		t.Fatal("relative anchored glob did not retain nested roots")
	}
	if RetainNestedStartRoots([]string{"!blocked/**"}, nil) {
		t.Fatal("negative policy became a reported-root retention policy")
	}
	if !PreserveNestedStartRoots([]string{"!blocked/**"}, nil) {
		t.Fatal("negative policy did not preserve roots for reachability")
	}
	if !PreserveNestedStartRoots([]string{"ignored"}, nil) {
		t.Fatal("basename depth-zero exemption did not preserve nested roots")
	}
	if !PreserveNestedStartRoots([]string{"ignored/**"}, nil) {
		t.Fatal("first-segment depth-zero exemption did not preserve nested roots")
	}
}

func TestValidateRootPoliciesAbsoluteContainment(t *testing.T) {
	root := t.TempDir()
	roots, err := PrepareStartRoots([]string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRootPolicies(roots, []string{filepath.Join(root, "docs", "*.md")}, nil); err != nil {
		t.Fatalf("contained pattern rejected: %v", err)
	}
	if err := ValidateRootPolicies(roots, []string{filepath.Join(t.TempDir(), "*.md")}, nil); err == nil {
		t.Fatal("out-of-root absolute pattern accepted")
	}
	if err := ValidateRootPolicies(roots, nil, []UserGlob{{Pattern: filepath.Join(t.TempDir(), "*.md")}}); err == nil {
		t.Fatal("out-of-root absolute user glob accepted")
	}
}

func TestStartRootTraversalCachesRelativeDirectoryStatePerContext(t *testing.T) {
	rootPath := t.TempDir()
	directory := filepath.Join(rootPath, "one", "two")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("needle"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := PrepareStartRoots([]string{rootPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	traversal, err := prepareRootTraversal(roots[0], nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !startRootCanProduceFile(traversal, filepath.Join(directory, "first.txt")) {
		t.Fatal("first candidate was not reachable")
	}
	cachedAfterFirst := len(traversal.directoryCache)
	if !startRootCanProduceFile(traversal, filepath.Join(directory, "second.txt")) {
		t.Fatal("second candidate was not reachable")
	}
	if len(traversal.directoryCache) != cachedAfterFirst {
		t.Fatalf("cache entries grew from %d to %d for the same relative directory", cachedAfterFirst, len(traversal.directoryCache))
	}
	fresh, err := prepareRootTraversal(roots[0], nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.directoryCache) != 1 {
		t.Fatalf("fresh traversal cache size = %d, want root entry only", len(fresh.directoryCache))
	}
}

func TestPrepareStartRootsRootNamePatternsDoNotMatchRootItself(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "match.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(rootPath); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)

	rootName := filepath.Base(rootPath)
	for _, rootCase := range []struct {
		name string
		raw  []string
	}{
		{name: "default current root", raw: nil},
		{name: "supplied root", raw: []string{rootPath}},
	} {
		t.Run(rootCase.name, func(t *testing.T) {
			roots, err := PrepareStartRoots(rootCase.raw, false)
			if err != nil {
				t.Fatal(err)
			}
			tests := []struct {
				name      string
				pathScope []string
				userGlobs []UserGlob
				want      int
			}{
				{name: "negative pathscope", pathScope: []string{"!" + rootName}, want: 1},
				{name: "negative glob", userGlobs: []UserGlob{{Pattern: rootName, Negated: true}}, want: 1},
				{name: "positive pathscope", pathScope: []string{rootName}, want: 0},
				{name: "positive glob", userGlobs: []UserGlob{{Pattern: rootName}}, want: 0},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					count, err := GetDocumentFileCountForRoots(
						nil,
						roots,
						test.pathScope,
						WalkOptions{UserGlobs: test.userGlobs},
					)
					if err != nil {
						t.Fatal(err)
					}
					if count != test.want {
						t.Fatalf("count = %d, want %d", count, test.want)
					}
				})
			}
		})
	}
}

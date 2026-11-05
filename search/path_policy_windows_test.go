//go:build windows

package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsAbsoluteGlobFoldsRootButPreservesDescendantCase(t *testing.T) {
	root := t.TempDir()
	actualLowercase := filepath.Join(root, "case", "main.GO")
	if err := os.MkdirAll(filepath.Dir(actualLowercase), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actualLowercase, []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	lowerRoot := strings.ToLower(root)
	pattern := filepath.Join(lowerRoot, "Case", "*.GO")
	exact, err := CompilePathPolicy(nil, []UserGlob{{Pattern: pattern}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := CompilePathPolicy(nil, []UserGlob{{Pattern: pattern, IgnoreCase: true}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !exact.MatchesFile(filepath.Join(root, "Case", "main.GO")) {
		t.Fatal("case-sensitive absolute glob rejected lowercase root spelling")
	}
	if exact.MatchesFile(actualLowercase) {
		t.Fatal("case-sensitive absolute glob folded descendant segments")
	}
	if !folded.MatchesFile(actualLowercase) {
		t.Fatal("case-insensitive absolute glob did not fold descendant segments")
	}
	negative, err := CompilePathPolicy(
		nil,
		[]UserGlob{{Pattern: pattern, Negated: true}},
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !negative.MatchesFile(actualLowercase) {
		t.Fatal("case-sensitive absolute negative glob excluded lowercase on-disk descendant")
	}
}

func TestWindowsValidateRootPoliciesUsesPolicyContainment(t *testing.T) {
	root := t.TempDir()
	roots, err := PrepareStartRoots([]string{root}, false)
	if err != nil {
		t.Fatal(err)
	}
	lowerRoot := strings.ToLower(root)
	tests := []struct {
		name      string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "above-root globstar", pathScope: []string{filepath.Join(lowerRoot, "**", "*.md")}},
		{name: "anchored negative", pathScope: []string{"!" + filepath.Join(lowerRoot, "*", "x.md")}},
		{
			name:      "case-sensitive descendant",
			userGlobs: []UserGlob{{Pattern: filepath.Join(lowerRoot, "Case", "*.GO")}},
		},
		{
			name:      "case-insensitive descendant",
			userGlobs: []UserGlob{{Pattern: filepath.Join(lowerRoot, "Case", "*.GO"), IgnoreCase: true}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateRootPolicies(roots, test.pathScope, test.userGlobs); err != nil {
				t.Fatalf("ValidateRootPolicies() error = %v", err)
			}
		})
	}
}

func TestWindowsDriveRootAbsoluteCoverage(t *testing.T) {
	root := StartRoot{
		LogicalPath:  `C:\`,
		SuppliedPath: `C:\`,
		PhysicalPath: `C:\`,
		CanonicalKey: `c:\`,
		Supplied:     true,
		Reported:     true,
	}
	pathScope := []string{`C:\Users\**`}
	if err := ValidateRootPolicies([]StartRoot{root}, pathScope, nil); err != nil {
		t.Fatalf("drive-root validation rejected contained pattern: %v", err)
	}
	policy, err := CompilePathPolicy(pathScope, nil, root.LogicalPath, root.PhysicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.CouldMatchRoot() {
		t.Fatal("Windows drive root was pruned before contained absolute pattern")
	}
}

func TestWindowsAbsoluteGlobWildcardsAboveRootPreserveDescendantCase(t *testing.T) {
	root := `C:\Users\X\Temp`
	covering := `c:\*\x`
	coveringPositive, err := CompilePathPolicy(nil, []UserGlob{{Pattern: covering}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !coveringPositive.MatchesFile(filepath.Join(root, "nested", "match.txt")) {
		t.Fatal("ancestor absolute glob did not cover deeper root descendants")
	}
	coveringNegative, err := CompilePathPolicy(
		nil,
		[]UserGlob{{Pattern: covering, Negated: true}},
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if coveringNegative.MatchesFile(filepath.Join(root, "nested", "match.txt")) {
		t.Fatal("ancestor absolute negative glob admitted deeper root descendant")
	}

	spanning := `c:\*\x\temp\**`
	policy, err := CompilePathPolicy(nil, []UserGlob{{Pattern: spanning}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.CouldMatchRoot() || !policy.MatchesFile(filepath.Join(root, "nested", "match.txt")) {
		t.Fatal("case-sensitive absolute glob did not fold root-spanning Windows segments")
	}

	descendant := `c:\*\x\temp\**\Case\*.GO`
	exact, err := CompilePathPolicy(nil, []UserGlob{{Pattern: descendant}}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if !exact.MatchesFile(filepath.Join(root, "nested", "Case", "main.GO")) {
		t.Fatal("case-sensitive descendant glob missed exact-case match")
	}
	if exact.MatchesFile(filepath.Join(root, "nested", "case", "main.go")) {
		t.Fatal("case-sensitive descendant glob folded below-root segments")
	}
	negative, err := CompilePathPolicy(
		nil,
		[]UserGlob{{Pattern: descendant, Negated: true}},
		root,
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if negative.MatchesFile(filepath.Join(root, "nested", "Case", "main.GO")) {
		t.Fatal("case-sensitive absolute negative glob admitted exact descendant")
	}
	if !negative.MatchesFile(filepath.Join(root, "nested", "case", "main.go")) {
		t.Fatal("case-sensitive absolute negative glob folded descendant mismatch")
	}
}

func TestWindowsAbsoluteGlobResolvesJunctionTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	candidate := filepath.Join(target, "case", "main.GO")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("junction creation unavailable: %v: %s", err, output)
	}
	logicalRoot, physicalRoot, err := resolveWalkRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	candidate = filepath.Join(logicalRoot, "case", "main.GO")
	pattern := filepath.Join(link, "Case", "*.GO")
	policy, err := CompilePathPolicy(nil, []UserGlob{{Pattern: pattern}}, logicalRoot, physicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if policy.MatchesFile(candidate) {
		t.Fatal("case-sensitive junction glob matched lowercase descendant")
	}
	negative, err := CompilePathPolicy(
		nil,
		[]UserGlob{{Pattern: pattern, Negated: true}},
		logicalRoot,
		physicalRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !negative.MatchesFile(candidate) {
		t.Fatal("case-sensitive negative junction glob excluded lowercase descendant")
	}
	matchingPattern := filepath.Join(link, "case", "*.GO")
	matching, err := CompilePathPolicy(nil, []UserGlob{{Pattern: matchingPattern}}, logicalRoot, physicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !matching.MatchesFile(candidate) {
		t.Fatal("absolute user glob did not match through resolved junction target")
	}
}

func TestWindowsNestedRootRetentionPreservesAbsoluteGlobBoundaryCase(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	match := filepath.Join(child, "blocked.txt")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(match, []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(strings.ToLower(parent), "C", "blocked.txt")
	positive := []UserGlob{{Pattern: pattern}}
	fileTypes := []string{"-g", "*.txt"}

	assertCountAndDiscovery := func(t *testing.T, roots []StartRoot, globs []UserGlob, want int) {
		t.Helper()
		options := WalkOptions{UserGlobs: globs}
		count, err := GetDocumentFileCountForRoots(fileTypes, roots, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
		engine.StartRoots = roots
		engine.UserGlobs = globs
		engine.Silent = true
		candidates, _, err := engine.DiscoverCandidates(count)
		if err != nil {
			t.Fatal(err)
		}
		if count != want || len(candidates) != want {
			t.Fatalf("count/candidates = %d/%v, want %d", count, candidates, want)
		}
	}

	childOnly, err := PrepareStartRootsWithOptions(
		[]string{child},
		StartRootOptions{UserGlobs: positive},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertCountAndDiscovery(t, childOnly, positive, 1)

	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{UserGlobs: positive})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(roots) != 2 || len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q = %+v, reported = %+v; want parent plus traversal child", raw, roots, reported)
		}
		assertCountAndDiscovery(t, roots, positive, 1)
	}

	negative := []UserGlob{{Pattern: pattern, Negated: true}}
	childNegative, err := PrepareStartRootsWithOptions(
		[]string{child},
		StartRootOptions{UserGlobs: negative},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The child root re-anchors the absolute negative below the case-folded
	// boundary, so child-only intentionally excludes the file.
	assertCountAndDiscovery(t, childNegative, negative, 0)
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		combined, err := PrepareStartRootsWithOptions(
			raw,
			StartRootOptions{UserGlobs: negative},
		)
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(combined)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("negative roots %q reported = %+v, want parent only", raw, reported)
		}
		// With the parent supplied, its case-sensitive absolute view does not
		// exclude the lowercase child path; the combined union intentionally
		// includes the file in either root order.
		assertCountAndDiscovery(t, combined, negative, 1)
	}
}

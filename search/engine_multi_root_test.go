package search

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeMultiRootFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMultiRootDiscoveryAndGlobalRanking(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-root")
	secondRoot := filepath.Join(base, "a-root")
	firstFile := filepath.Join(firstRoot, "a.txt")
	secondFile := filepath.Join(secondRoot, "z.txt")
	writeMultiRootFixture(t, firstFile, "needle")
	writeMultiRootFixture(t, secondFile, "needle")

	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	engine.PathOnly = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].FilePath != firstFile {
		t.Fatalf("global root-relative order = %q, want %q first", results[0].FilePath, firstFile)
	}
}

func TestMultiRootEqualTieUsesSuppliedOrderDeterministically(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-first")
	secondRoot := filepath.Join(base, "a-second")
	firstFile := filepath.Join(firstRoot, "same.txt")
	secondFile := filepath.Join(secondRoot, "same.txt")
	writeMultiRootFixture(t, firstFile, "needle equal content")
	writeMultiRootFixture(t, secondFile, "needle equal content")

	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, pathOnly := range []bool{false, true} {
		for iteration := 0; iteration < 20; iteration++ {
			engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
			engine.StartRoots = roots
			engine.Silent = true
			engine.PathOnly = pathOnly
			engine.MaxResults = 1
			results, err := engine.Execute()
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].FilePath != firstFile {
				t.Fatalf("pathOnly=%v iteration=%d results=%v, want first supplied root %q", pathOnly, iteration, results, firstFile)
			}
		}
	}
}

func TestMultiRootSplitDiscoveryFilterPreservesProvenance(t *testing.T) {
	base := t.TempDir()
	firstRoot := filepath.Join(base, "z-first")
	secondRoot := filepath.Join(base, "a-second")
	firstFile := filepath.Join(firstRoot, "same.txt")
	secondFile := filepath.Join(secondRoot, "same.txt")
	writeMultiRootFixture(t, firstFile, "needle equal content")
	writeMultiRootFixture(t, secondFile, "needle equal content")
	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	engine.PathOnly = true
	engine.MaxResults = 1
	candidates, total, err := engine.DiscoverCandidates(2)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	results, err := engine.ExtractAndBuildResults(matches)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].FilePath != firstFile {
		t.Fatalf("split API results = %v, want first supplied root %q", results, firstFile)
	}
}

func TestMultiRootFilterCandidatesRejectsUnknownProvenance(t *testing.T) {
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	roots, err := PrepareStartRoots([]string{firstRoot, secondRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeMultiRootFixture(t, outside, "needle")
	if _, err := engine.FilterCandidates([]string{outside}, 1, time.Now()); err == nil {
		t.Fatal("multi-root FilterCandidates accepted a path with no root provenance")
	}
}

func TestMultiRootSplitAPIInfersTraversalOnlyChildProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "blocked", "child")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	pathScope := []string{"!blocked/**"}
	roots, err := PrepareStartRootsWithOptions(
		[]string{parent, child},
		StartRootOptions{PathScope: pathScope},
	)
	if err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = pathScope
	engine.Silent = true
	candidates, total, err := engine.DiscoverCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantRankingPath := filepath.Join("blocked", "child", "match.txt")
	if len(matches) != 1 || matches[0].rootOrder != 0 || matches[0].rankingPath != wantRankingPath {
		t.Fatalf("matches = %+v, want collapsed-parent ranking provenance", matches)
	}
}

func TestMultiRootTraversalOnlyOverlapUsesReportedRankingProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	overlap := filepath.Join(child, "ignored", "a.txt")
	parentOnly := filepath.Join(parent, "ignored", "b.txt")
	writeMultiRootFixture(t, overlap, "needle")
	writeMultiRootFixture(t, parentOnly, "needle")
	pathScope := []string{"ignored"}

	var baselinePaths []string
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}

		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		engine.PathOnly = true
		internalCandidates, _, err := engine.discoverCandidates(compileQuery([]string{"needle"}, PartialModeOff), 2, roots)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range internalCandidates {
			if candidate.filePath == overlap &&
				(!candidate.root.Reported || candidate.root.LogicalPath != parent ||
					candidate.rankingRoot.LogicalPath != parent || candidate.rootOrder != 0) {
				t.Fatalf("roots %q internal overlap provenance = %+v, want reported parent", raw, candidate)
			}
		}
		candidates, total, err := engine.DiscoverCandidates(2)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		results, err := engine.ExtractAndBuildResults(matches)
		if err != nil {
			t.Fatal(err)
		}
		gotPaths := make([]string, len(results))
		for index := range results {
			gotPaths[index] = results[index].FilePath
		}
		if baselinePaths == nil {
			baselinePaths = gotPaths
		} else if !reflect.DeepEqual(gotPaths, baselinePaths) {
			t.Fatalf("roots %q result order = %q, want stable %q", raw, gotPaths, baselinePaths)
		}
		foundOverlap := false
		for _, match := range matches {
			if match.filePath != overlap {
				continue
			}
			foundOverlap = true
			wantRankingPath := filepath.Join("c", "ignored", "a.txt")
			if match.rankingPath != wantRankingPath || match.rootOrder != 0 || match.pathMatchCount != 0 {
				t.Fatalf("roots %q overlap match = %+v, want parent ranking %q/order 0/path count 0", raw, match, wantRankingPath)
			}
		}
		if !foundOverlap {
			t.Fatalf("roots %q did not return overlap candidate", raw)
		}
	}
}

func TestMultiRootSplitAPIPathScopeExemptionUsesParentProvenance(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "ignored")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored/**"}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true

		candidates, total, err := engine.DiscoverCandidates(1)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parentOrder := 0
		if raw[0] == child {
			parentOrder = 1
		}
		wantRankingPath := filepath.Join("ignored", "match.txt")
		if len(matches) != 1 || matches[0].rootOrder != parentOrder || matches[0].rankingPath != wantRankingPath {
			t.Fatalf("roots=%q matches=%+v, want parent order %d and ranking path %q", raw, matches, parentOrder, wantRankingPath)
		}
	}
}

func TestMultiRootSplitAPIDescendantSymlinkProvenance(t *testing.T) {
	parent := t.TempDir()
	external := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(external, "match.txt"), "needle")
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	roots, err := PrepareStartRootsWithOptions([]string{parent, link}, StartRootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.Silent = true
	candidates, total, err := engine.DiscoverCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.FilterCandidates(candidates, total, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].rootOrder != 1 || matches[0].rankingPath != "match.txt" {
		t.Fatalf("matches = %+v, want explicit symlink-root provenance", matches)
	}
}

func TestMultiRootAbsoluteGlobRetainsLogicalSymlinkChild(t *testing.T) {
	parent := t.TempDir()
	physicalChild := filepath.Join(parent, "physical")
	logicalChild := filepath.Join(parent, "logical")
	match := filepath.Join(physicalChild, "blocked.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.Symlink(physicalChild, logicalChild); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	fileTypes := []string{"-g", "*.txt"}
	tests := []struct {
		name       string
		segment    string
		ignoreCase bool
	}{
		{name: "case-sensitive", segment: "l*"},
		{name: "ignore-case", segment: "L*", ignoreCase: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			globs := []UserGlob{{
				Pattern:    filepath.Join(parent, test.segment, "blocked.txt"),
				IgnoreCase: test.ignoreCase,
			}}
			run := func(t *testing.T, raw []string) ([]StartRoot, []string) {
				t.Helper()
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{UserGlobs: globs})
				if err != nil {
					t.Fatal(err)
				}
				options := WalkOptions{UserGlobs: globs}
				count, err := GetDocumentFileCountForRoots(fileTypes, roots, nil, options)
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
				engine.StartRoots = roots
				engine.UserGlobs = globs
				engine.Silent = true
				engine.PathOnly = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != 1 {
					t.Fatalf("count/candidates = %d/%v, want one", count, candidates)
				}
				results, err := engine.Execute()
				if err != nil {
					t.Fatal(err)
				}
				paths := make([]string, len(results))
				for index := range results {
					paths[index] = results[index].FilePath
				}
				return roots, paths
			}

			_, childOnly := run(t, []string{logicalChild})
			for _, raw := range [][]string{{parent, logicalChild}, {logicalChild, parent}} {
				roots, combined := run(t, raw)
				reported := ReportedStartRoots(roots)
				if len(roots) != 2 || len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q = %+v, reported = %+v; want parent plus traversal child", raw, roots, reported)
				}
				if len(childOnly) != 1 || len(combined) != 1 {
					t.Fatalf("child/combined paths = %v/%v, want one each", childOnly, combined)
				}
				childInfo, err := os.Stat(childOnly[0])
				if err != nil {
					t.Fatal(err)
				}
				combinedInfo, err := os.Stat(combined[0])
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(childInfo, combinedInfo) {
					t.Fatalf("child/combined results = %q/%q, want same physical file", childOnly, combined)
				}
			}
		})
	}
}

func TestMultiRootTraversalOnlyPhysicalChildProjectsReportedSymlinkPath(t *testing.T) {
	physicalParent := t.TempDir()
	reportedParent := filepath.Join(t.TempDir(), "reported-parent")
	if err := os.Symlink(physicalParent, reportedParent); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	child := filepath.Join(physicalParent, "c")
	firstPhysical := filepath.Join(child, "ignored", "a.txt")
	secondPhysical := filepath.Join(child, "ignored", "b.txt")
	if err := os.MkdirAll(filepath.Join(physicalParent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiRootFixture(t, firstPhysical, "needle c")
	writeMultiRootFixture(t, secondPhysical, "needle c")
	if err := os.WriteFile(filepath.Join(physicalParent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{"ignored"}
	wantPaths := []string{
		filepath.Join(reportedParent, "c", "ignored", "a.txt"),
		filepath.Join(reportedParent, "c", "ignored", "b.txt"),
	}
	for _, raw := range [][]string{{reportedParent, child}, {child, reportedParent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != reportedParent {
			t.Fatalf("roots %q reported = %+v, want symlink parent", raw, reported)
		}

		engine := NewSearchEngine([]string{"needle", "c"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Strict = true
		engine.Silent = true
		candidates, total, err := engine.DiscoverCandidates(2)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if strings.Contains(match.rankingPath, "..") ||
				!strings.HasPrefix(filepath.Clean(match.filePath), filepath.Clean(reportedParent)+string(filepath.Separator)) ||
				match.pathMatchCount != 1 || match.rootOrder != 0 {
				t.Fatalf("roots %q projected match = %+v", raw, match)
			}
		}
		results, err := engine.ExtractAndBuildResults(matches)
		if err != nil {
			t.Fatal(err)
		}
		gotPaths := make([]string, len(results))
		for index := range results {
			gotPaths[index] = results[index].FilePath
		}
		if !reflect.DeepEqual(gotPaths, wantPaths) {
			t.Fatalf("roots %q result paths = %q, want reported-root-relative %q", raw, gotPaths, wantPaths)
		}
	}
}

func TestMultiRootCanonicalCandidateDedupPreservesFirstRoot(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	file := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, file, "needle")

	roots, err := PrepareStartRoots([]string{parent, child}, true)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = []string{"**/*.txt"}
	engine.Silent = true
	query := compileQuery([]string{"needle"}, PartialModeOff)
	candidates, _, err := engine.discoverCandidates(query, 0, roots)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	if candidates[0].root.CanonicalKey != roots[0].CanonicalKey {
		t.Fatalf("candidate root = %q, want first root %q", candidates[0].root.LogicalPath, roots[0].LogicalPath)
	}
}

func TestMultiRootNestedTraversalAvoidsSkippedChildDataLoss(t *testing.T) {
	tests := []struct {
		name      string
		childName string
		ignore    string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "reachable", childName: "child"},
		{name: "hard skipped", childName: "node_modules"},
		{name: "ignored", childName: "ignored", ignore: "ignored/\n"},
		{name: "negative pathscope", childName: filepath.Join("blocked", "child"), pathScope: []string{"!blocked/**"}},
		{name: "negative glob", childName: filepath.Join("blocked", "child"), userGlobs: []UserGlob{{Pattern: "blocked/**", Negated: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, test.childName)
			match := filepath.Join(child, "match.txt")
			writeMultiRootFixture(t, match, "needle")
			if test.ignore != "" {
				if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte(test.ignore), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{
					PathScope: test.pathScope,
					UserGlobs: test.userGlobs,
				})
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = test.pathScope
				engine.UserGlobs = test.userGlobs
				engine.Silent = true
				engine.PathOnly = true
				results, err := engine.Execute()
				if err != nil {
					t.Fatal(err)
				}
				if len(results) != 1 || results[0].FilePath != match {
					t.Fatalf("roots=%q results=%v, want retained match %q", raw, results, match)
				}
			}
		})
	}
}

func TestMultiRootThreeLevelCollapsePreservesGrandchildData(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	grandchild := filepath.Join(child, "ignored")
	match := filepath.Join(grandchild, "match.txt")
	if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiRootFixture(t, match, "needle")
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
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		engine.PathOnly = true
		candidates, total, err := engine.DiscoverCandidates(1)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := engine.FilterCandidates(candidates, total, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		wantRankingPath := filepath.Join("c", "ignored", "match.txt")
		if len(matches) != 1 || matches[0].filePath != match || matches[0].rankingPath != wantRankingPath {
			t.Fatalf("roots %q matches = %+v, want canonical match with ranking %q", raw, matches, wantRankingPath)
		}
	}
}

func TestMultiRootDepthZeroExemptionRetainsChildAndCountParity(t *testing.T) {
	for _, pathScope := range [][]string{{"ignored"}, {"ignored/**"}} {
		t.Run(strings.Join(pathScope, ","), func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "c")
			match := filepath.Join(child, "ignored", "match.txt")
			if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeMultiRootFixture(t, match, "needle")
			if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
				if err != nil {
					t.Fatal(err)
				}
				reported := ReportedStartRoots(roots)
				if RetainNestedStartRoots(pathScope, nil) {
					if len(reported) != 2 || reported[0].LogicalPath != raw[0] || reported[1].LogicalPath != raw[1] {
						t.Fatalf("roots %q reported = %+v, want both in supplied order", raw, reported)
					}
				} else if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
				count, err := GetDocumentFileCountForRoots(nil, roots, pathScope, WalkOptions{})
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = pathScope
				engine.Silent = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != count || candidates[0] != match {
					t.Fatalf("roots %q count/candidates = %d/%v, want one retained match %q", raw, count, candidates, match)
				}
			}
		})
	}
}

func TestMultiRootOutsideRepositoryIgnoreScopeRetainsChild(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "c")
	match := filepath.Join(child, "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("*.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRoots(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 {
			t.Fatalf("roots %q traversal count = %d, want child retained for ignore-scope difference", raw, len(roots))
		}
		reported := ReportedStartRoots(roots)
		if len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
		}
		fileTypes := []string{"-g", "*.txt"}
		count, err := GetDocumentFileCountForRoots(fileTypes, roots, nil, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
		engine.StartRoots = roots
		engine.Silent = true
		candidates, _, err := engine.DiscoverCandidates(count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || len(candidates) != 1 || candidates[0] != match {
			t.Fatalf("roots %q count/candidates = %d/%v, want standalone-child match %q", raw, count, candidates, match)
		}
	}
}

func TestMultiRootRelativeAnchoredNegativeFilePolicyRetainsChild(t *testing.T) {
	tests := []struct {
		name      string
		pathScope []string
		userGlobs []UserGlob
	}{
		{name: "pathscope", pathScope: []string{"!c/blocked.txt"}},
		{name: "user glob", userGlobs: []UserGlob{{Pattern: "c/blocked.txt", Negated: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			child := filepath.Join(parent, "c")
			match := filepath.Join(child, "blocked.txt")
			writeMultiRootFixture(t, match, "needle")
			for _, raw := range [][]string{{parent, child}, {child, parent}} {
				options := StartRootOptions{PathScope: test.pathScope, UserGlobs: test.userGlobs}
				roots, err := PrepareStartRootsWithOptions(raw, options)
				if err != nil {
					t.Fatal(err)
				}
				reported := ReportedStartRoots(roots)
				if len(reported) != 1 || reported[0].LogicalPath != parent {
					t.Fatalf("roots %q reported = %+v, want parent only", raw, reported)
				}
				fileTypes := []string{"-g", "*.txt"}
				walkOptions := WalkOptions{UserGlobs: test.userGlobs}
				count, err := GetDocumentFileCountForRoots(fileTypes, roots, test.pathScope, walkOptions)
				if err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
				engine.StartRoots = roots
				engine.PathScope = test.pathScope
				engine.UserGlobs = test.userGlobs
				engine.Silent = true
				candidates, _, err := engine.DiscoverCandidates(count)
				if err != nil {
					t.Fatal(err)
				}
				if count != 1 || len(candidates) != 1 || candidates[0] != match {
					t.Fatalf("roots %q count/candidates = %d/%v, want child-only match %q", raw, count, candidates, match)
				}
			}
		})
	}
}

func TestMultiRootAbsolutePoliciesAcrossNestedRoots(t *testing.T) {
	t.Run("positive covers child root only", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		other := t.TempDir()
		childFile := filepath.Join(child, "match.txt")
		writeMultiRootFixture(t, childFile, "needle")
		writeMultiRootFixture(t, filepath.Join(other, "other.txt"), "needle")
		pathScope := []string{filepath.Join(parent, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child, other},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("absolute positive count = %d, want child-root file only", count)
		}
	})

	t.Run("negative remains active in child root", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		other := t.TempDir()
		writeMultiRootFixture(t, filepath.Join(child, "blocked.txt"), "needle")
		writeMultiRootFixture(t, filepath.Join(other, "allowed.txt"), "needle")
		pathScope := []string{"!" + filepath.Join(child, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child, other},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("absolute negative count = %d, want unrelated-root file only", count)
		}
	})

	t.Run("positive reaches ignored traversal child", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "ignored")
		match := filepath.Join(child, "match.txt")
		writeMultiRootFixture(t, match, "needle")
		if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pathScope := []string{filepath.Join(parent, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{parent, child},
			StartRootOptions{PathScope: pathScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(roots) != 2 || len(ReportedStartRoots(roots)) != 1 {
			t.Fatalf("roots = %+v, want reported parent plus traversal child", roots)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("ignored traversal child count = %d, want 1", count)
		}
	})

	t.Run("logical and physical symlink forms", func(t *testing.T) {
		physical := t.TempDir()
		logical := filepath.Join(t.TempDir(), "logical")
		if err := os.Symlink(physical, logical); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		child := filepath.Join(physical, "child")
		writeMultiRootFixture(t, filepath.Join(child, "match.txt"), "needle")
		positive := []string{filepath.Join(logical, "**")}
		roots, err := PrepareStartRootsWithOptions(
			[]string{child},
			StartRootOptions{PathScope: positive},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err := GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, positive, WalkOptions{})
		if err != nil || count != 1 {
			t.Fatalf("logical absolute pattern count/error = %d/%v, want 1/nil", count, err)
		}

		negative := []string{"!" + filepath.Join(physical, "child", "**")}
		roots, err = PrepareStartRootsWithOptions(
			[]string{logical},
			StartRootOptions{PathScope: negative},
		)
		if err != nil {
			t.Fatal(err)
		}
		count, err = GetDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, negative, WalkOptions{})
		if err != nil || count != 0 {
			t.Fatalf("physical absolute negative count/error = %d/%v, want 0/nil", count, err)
		}
	})
}

func TestMultiRootPOSIXAbsoluteGlobstarExemptionRetainsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX absolute path semantics")
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "a")
	match := filepath.Join(child, "ignored", "match.txt")
	writeMultiRootFixture(t, match, "needle")
	if err := os.WriteFile(filepath.Join(child, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathScope := []string{filepath.Join(parent, "**", "a", "ignored", "**")}
	for _, raw := range [][]string{{parent, child}, {child, parent}} {
		roots, err := PrepareStartRootsWithOptions(raw, StartRootOptions{PathScope: pathScope})
		if err != nil {
			t.Fatal(err)
		}
		reported := ReportedStartRoots(roots)
		if len(roots) != 2 || len(reported) != 1 || reported[0].LogicalPath != parent {
			t.Fatalf("roots %q = %+v, reported = %+v; want parent plus traversal child", raw, roots, reported)
		}
		fileTypes := []string{"-g", "*.txt"}
		count, err := GetDocumentFileCountForRoots(fileTypes, roots, pathScope, WalkOptions{})
		if err != nil {
			t.Fatal(err)
		}
		engine := NewSearchEngine([]string{"needle"}, nil, fileTypes, false, 1, 1000)
		engine.StartRoots = roots
		engine.PathScope = pathScope
		engine.Silent = true
		candidates, _, err := engine.DiscoverCandidates(count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || len(candidates) != 1 || candidates[0] != match {
			t.Fatalf("roots %q count/candidates = %d/%v, want %q", raw, count, candidates, match)
		}
	}
}

func TestMultiRootCountDiscoveryParity(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "scoped")
	other := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(child, "one.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(child, "skip.md"), "needle")
	writeMultiRootFixture(t, filepath.Join(other, "two.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(other, "ignored.txt"), "needle")
	if err := os.WriteFile(filepath.Join(other, ".ignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots, err := PrepareStartRoots([]string{parent, child, other}, true)
	if err != nil {
		t.Fatal(err)
	}
	options := WalkOptions{UserGlobs: []UserGlob{{Pattern: "*.txt"}}}
	pathScope := []string{"**/*.txt"}
	countKeyTracking := 0
	count, err := getDocumentFileCountForRoots(nil, roots, pathScope, options, func() {
		countKeyTracking++
	})
	if err != nil {
		t.Fatal(err)
	}
	if countKeyTracking != 1 {
		t.Fatalf("preflight key tracking = %d, want 1 for overlapping roots", countKeyTracking)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = pathScope
	engine.UserGlobs = options.UserGlobs
	engine.Silent = true
	engine.PathOnly = true
	progressKeyTracking := 0
	engine.onProgressKeyTracking = func() {
		progressKeyTracking++
	}
	candidatesWithoutProgress, _, err := engine.DiscoverCandidates(count)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidatesWithoutProgress) != count || progressKeyTracking != 0 {
		t.Fatalf("nil-progress candidates/key tracking = %d/%d, want %d/0", len(candidatesWithoutProgress), progressKeyTracking, count)
	}
	var progress []struct{ processed, total int }
	engine.OnProgress = func(stage string, processed, total int, _ string) {
		if stage == "discovery" && processed > 0 {
			progress = append(progress, struct{ processed, total int }{processed, total})
		}
	}
	candidates, _, err := engine.DiscoverCandidates(count)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != count {
		t.Fatalf("discovery candidates = %d, want preflight count %d", len(candidates), count)
	}
	if progressKeyTracking != 1 {
		t.Fatalf("progress key tracking = %d, want 1 for overlapping roots", progressKeyTracking)
	}
	if len(progress) != count || progress[len(progress)-1].processed != count || progress[len(progress)-1].total != count {
		t.Fatalf("deduplicated progress = %+v, want %d events ending at %d/%d", progress, count, count, count)
	}
	engine.OnProgress = nil
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(results) != count {
		t.Fatalf("count/results = %d/%d, want 2/2", count, len(results))
	}
}

func TestNonOverlappingRootsAvoidCountAndProgressKeyTracking(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeMultiRootFixture(t, filepath.Join(first, "one.txt"), "needle")
	writeMultiRootFixture(t, filepath.Join(second, "two.txt"), "needle")

	for _, test := range []struct {
		name string
		raw  []string
		want int
	}{
		{name: "single root", raw: []string{first}, want: 1},
		{name: "non-overlapping roots", raw: []string{first, second}, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, err := PrepareStartRoots(test.raw, true)
			if err != nil {
				t.Fatal(err)
			}
			countKeyTracking := 0
			count, err := getDocumentFileCountForRoots([]string{"-g", "*.txt"}, roots, nil, WalkOptions{}, func() {
				countKeyTracking++
			})
			if err != nil {
				t.Fatal(err)
			}
			if count != test.want || countKeyTracking != 0 {
				t.Fatalf("count/key tracking = %d/%d, want %d/0", count, countKeyTracking, test.want)
			}

			engine := NewSearchEngine([]string{"needle"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
			engine.StartRoots = roots
			engine.Silent = true
			progressKeyTracking := 0
			engine.onProgressKeyTracking = func() {
				progressKeyTracking++
			}
			progressEvents := 0
			engine.OnProgress = func(stage string, processed, total int, _ string) {
				if stage == "discovery" && processed > 0 {
					progressEvents++
					if total != count {
						t.Errorf("progress total = %d, want %d", total, count)
					}
				}
			}
			candidates, _, err := engine.DiscoverCandidates(count)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) != test.want || progressEvents != test.want || progressKeyTracking != 0 {
				t.Fatalf(
					"candidates/progress/key tracking = %d/%d/%d, want %d/%d/0",
					len(candidates), progressEvents, progressKeyTracking, test.want, test.want,
				)
			}
		})
	}
}

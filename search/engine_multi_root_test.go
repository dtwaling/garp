package search

import (
	"os"
	"path/filepath"
	"reflect"
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
	roots, err := PrepareStartRoots([]string{parent, child}, PreserveNestedStartRoots(pathScope, nil))
	if err != nil {
		t.Fatal(err)
	}
	roots = ApplyStartRootOptions(roots, false, StartRootOptions{PathScope: pathScope})

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
		roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
		if err != nil {
			t.Fatal(err)
		}
		roots = ApplyStartRootOptions(roots, false, StartRootOptions{PathScope: pathScope})
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
		roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
		if err != nil {
			t.Fatal(err)
		}
		roots = ApplyStartRootOptions(roots, true, StartRootOptions{PathScope: pathScope})
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
	roots, err := PrepareStartRoots([]string{parent, link}, false)
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
		roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
		if err != nil {
			t.Fatal(err)
		}
		roots = ApplyStartRootOptions(roots, false, StartRootOptions{PathScope: pathScope})
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
				retainNested := RetainNestedStartRoots(test.pathScope, test.userGlobs)
				roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(test.pathScope, test.userGlobs))
				if err != nil {
					t.Fatal(err)
				}
				roots = ApplyStartRootOptions(roots, retainNested, StartRootOptions{
					PathScope: test.pathScope,
					UserGlobs: test.userGlobs,
				})
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
		roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
		if err != nil {
			t.Fatal(err)
		}
		roots = ApplyStartRootOptions(roots, false, StartRootOptions{PathScope: pathScope})
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
				roots, err := PrepareStartRoots(raw, PreserveNestedStartRoots(pathScope, nil))
				if err != nil {
					t.Fatal(err)
				}
				roots = ApplyStartRootOptions(
					roots,
					RetainNestedStartRoots(pathScope, nil),
					StartRootOptions{PathScope: pathScope},
				)
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
	count, err := GetDocumentFileCountForRoots(nil, roots, pathScope, options)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewSearchEngine([]string{"needle"}, nil, nil, false, 1, 1000)
	engine.StartRoots = roots
	engine.PathScope = pathScope
	engine.UserGlobs = options.UserGlobs
	engine.Silent = true
	engine.PathOnly = true
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

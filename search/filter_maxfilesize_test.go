package search

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var binarySizePolicyExtensions = []string{
	".pdf", ".doc", ".docx", ".odt", ".rtf", ".eml", ".mbox", ".msg",
}

func writeSizedDiscoveryFixture(t *testing.T, root, name, content string, size int64) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Truncate(filename, size); err != nil {
		t.Fatalf("resize %s: %v", name, err)
	}
}

func discoveryBasenames(paths []string) []string {
	names := make([]string, len(paths))
	for i, filename := range paths {
		names[i] = filepath.Base(filename)
	}
	sort.Strings(names)
	return names
}

func assertStage1Discovery(
	t *testing.T,
	root string,
	options WalkOptions,
	want []string,
) {
	t.Helper()
	sort.Strings(want)

	plain, err := FindFilesWithFirstWordPartialWithOptions(
		"anchor", nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("plain Stage 1 discovery: %v", err)
	}
	if got := discoveryBasenames(plain); !reflect.DeepEqual(got, want) {
		t.Errorf("plain Stage 1 candidates = %v, want %v", got, want)
	}

	progress, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"anchor"}, nil, 2, nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("progress Stage 1 discovery: %v", err)
	}
	if got := discoveryBasenames(progress); !reflect.DeepEqual(got, want) {
		t.Errorf("progress Stage 1 candidates = %v, want %v", got, want)
	}
}

func TestBinarySizePolicyDefaultAndExplicitLimits(t *testing.T) {
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "text-at-limit.txt", "anchor", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "text-over-limit.txt", "anchor", defaultTextMaxBytes+1)
	for _, ext := range binarySizePolicyExtensions {
		name := "large" + ext
		if !IsBinaryFormat(name) {
			t.Fatalf("IsBinaryFormat(%q) = false", name)
		}
		writeSizedDiscoveryFixture(t, root, name, "anchor", defaultTextMaxBytes+1)
	}

	count, err := GetDocumentFileCountWithOptions(nil, root, nil, WalkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + len(binarySizePolicyExtensions); count != want {
		t.Errorf("default candidate count = %d, want %d", count, want)
	}

	count, err = GetDocumentFileCountWithOptions(
		nil, root, nil, WalkOptions{MaxFileSize: defaultTextMaxBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("explicit-limit candidate count = %d, want 1", count)
	}
}

func TestStage1DefaultMaxFileSizeBoundaries(t *testing.T) {
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "matched-at-limit.txt", "anchor", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "unmatched-at-limit.txt", "absent", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "matched-over-limit.txt", "anchor", defaultTextMaxBytes+1)

	want := []string{"matched-at-limit.txt"}
	for _, ext := range binarySizePolicyExtensions {
		name := "large" + ext
		writeSizedDiscoveryFixture(t, root, name, "anchor", defaultTextMaxBytes+1)
		want = append(want, name)
	}

	assertStage1Discovery(t, root, WalkOptions{}, want)
}

func TestStage1ExplicitMaxFileSizeRejectsEveryFormat(t *testing.T) {
	const limit int64 = 32
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "small.txt", "anchor", limit)
	writeSizedDiscoveryFixture(t, root, "large.txt", "anchor", limit+1)
	for _, ext := range binarySizePolicyExtensions {
		writeSizedDiscoveryFixture(t, root, "large"+ext, "anchor", limit+1)
	}

	assertStage1Discovery(t, root, WalkOptions{MaxFileSize: limit}, []string{"small.txt"})
}

func TestStage2ReadCap(t *testing.T) {
	const limit int64 = 96
	root := t.TempDir()

	t.Run("streaming terms after cap", func(t *testing.T) {
		filename := filepath.Join(root, "stream-post-cap.txt")
		content := strings.Repeat(" ", int(limit)) + "alpha beta"
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		found, decided := streamContainsAllWordsDecidedPartial(
			filename, []string{"alpha", "beta"}, PartialModeOff, limit,
		)
		if found || !decided {
			t.Fatalf("all-words result = (%v, %v), want (false, true)", found, decided)
		}

		found, decided = streamContainsRankedWordsDecidedPartial(
			filename, []string{"alpha", "beta"}, true, PartialModeOff, limit,
		)
		if found || !decided {
			t.Fatalf("ranked result = (%v, %v), want (false, true)", found, decided)
		}
	})

	t.Run("distance checks ignore post-cap cluster", func(t *testing.T) {
		filename := filepath.Join(root, "distance-post-cap.txt")
		prefix := "alpha " + strings.Repeat("pad ", 18) + "beta"
		prefix += strings.Repeat(" ", int(limit)-len(prefix))
		if err := os.WriteFile(filename, []byte(prefix+" alpha beta"), 0o600); err != nil {
			t.Fatal(err)
		}

		ok, err := checkFileContainsAllWordsPartial(
			filename, []string{"alpha", "beta"}, 16, true, PartialModeOff, limit,
		)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("all-words distance check matched a cluster after the cap")
		}

		_, _, _, _, ok, err = checkFileContainsRankedWordsPartial(
			filename, []string{"alpha", "beta"}, 16, true, true, PartialModeOff, limit,
		)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("ranked distance check matched a cluster after the cap")
		}
	})

	t.Run("exclusion after cap", func(t *testing.T) {
		filename := filepath.Join(root, "exclude-post-cap.txt")
		content := strings.Repeat(" ", int(limit)) + "forbidden"
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		found, err := checkFileContainsExcludeWords(filename, []string{"forbidden"}, limit)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Fatal("exclusion check matched a term after the cap")
		}
	})
}

func TestStage2FileGrowthUsesReadCap(t *testing.T) {
	const limit int64 = 64
	root := t.TempDir()
	filename := filepath.Join(root, "growing.txt")
	prefix := "alpha" + strings.Repeat(" ", int(limit)-len("alpha"))
	if err := os.WriteFile(filename, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}

	candidates, err := FindFilesWithFirstWordPartialWithOptions(
		"alpha", nil, root, nil, PartialModeOff, WalkOptions{MaxFileSize: limit},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discovery candidates = %v, want one", candidates)
	}
	if err := os.WriteFile(filename, []byte(prefix+" beta forbidden"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, nil, false, 1, 1000)
	engine.MaxFileSize = limit
	engine.Silent = true
	matches, err := engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("grown file matched terms after cap: %+v", matches)
	}

	engine = NewSearchEngine([]string{"alpha"}, []string{"forbidden"}, nil, false, 1, 1000)
	engine.MaxFileSize = limit
	engine.Silent = true
	matches, err = engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("grown file excluded by term after cap; matches = %+v", matches)
	}
}

package search

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
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

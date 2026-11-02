package search_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestStreamContainsRankedWordsDecided(t *testing.T) {
	dir := t.TempDir()
	words := []string{"alpha", "beta", "gamma"}
	tests := []struct {
		name   string
		text   string
		strict bool
		found  bool
	}{
		{
			name:  "relaxed mode accepts primary and tertiary",
			text:  "alpha gamma",
			found: true,
		},
		{
			name:  "relaxed mode rejects primary alone",
			text:  "alpha",
			found: false,
		},
		{
			name:  "relaxed mode rejects missing primary",
			text:  "beta gamma",
			found: false,
		},
		{
			name:   "strict mode rejects missing secondary",
			text:   "alpha gamma",
			strict: true,
			found:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".txt")
			if err := os.WriteFile(path, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}

			found, decided := search.StreamContainsRankedWordsDecided(path, words, tt.strict)
			if !decided {
				t.Fatal("small text file prefilter was inconclusive")
			}
			if found != tt.found {
				t.Fatalf("found = %v, want %v", found, tt.found)
			}
		})
	}
}

func TestCheckFileContainsRankedWords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.txt")
	if err := os.WriteFile(path, []byte("alpha gamma"), 0o600); err != nil {
		t.Fatal(err)
	}

	score, count, terms, span, ok, err := search.CheckFileContainsRankedWords(path, []string{"alpha", "beta", "gamma"}, 20, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || score != 10 || count != 2 || strings.Join(terms, ",") != "alpha,gamma" || span != 11 {
		t.Fatalf("ranked file match = (%d, %d, %v, %d, %v), want (10, 2, [alpha gamma], 11, true)", score, count, terms, span, ok)
	}
}

func TestStreamReadersMatchAcrossChunkBoundary(t *testing.T) {
	const chunkSize = 64 * 1024
	path := filepath.Join(t.TempDir(), "boundary.txt")
	content := strings.Repeat("x", chunkSize-3) + " alpha beta"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	found, decided := search.StreamContainsAllWordsDecided(path, []string{"alpha", "beta"})
	if !found || !decided {
		t.Fatalf("all-words boundary match = (%v, %v), want (true, true)", found, decided)
	}

	found, decided = search.StreamContainsRankedWordsDecided(path, []string{"alpha", "beta"}, true)
	if !found || !decided {
		t.Fatalf("ranked boundary match = (%v, %v), want (true, true)", found, decided)
	}
}

func TestStreamReadersStopAtDefaultTextCap(t *testing.T) {
	const defaultTextCap = 5 << 20
	path := filepath.Join(t.TempDir(), "post-cap.txt")
	content := strings.Repeat(" ", defaultTextCap) + "alpha beta"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	found, decided := search.StreamContainsAllWordsDecided(path, []string{"alpha", "beta"})
	if found || !decided {
		t.Fatalf("all-words post-cap match = (%v, %v), want (false, true)", found, decided)
	}

	found, decided = search.StreamContainsRankedWordsDecided(path, []string{"alpha", "beta"}, true)
	if found || !decided {
		t.Fatalf("ranked post-cap match = (%v, %v), want (false, true)", found, decided)
	}
}

func TestStreamWildcardChunkBoundaryStrictAndRelaxed(t *testing.T) {
	const chunkSize = 64 * 1024
	shortTerm, shortMatch := wildcardExpansionFixture(120)
	maxTerm, maxMatch := maximumWildcardFixture()

	tests := []struct {
		name    string
		words   []string
		strict  bool
		content string
		want    bool
	}{
		{
			name:    "strict primary 120 byte expansion",
			words:   []string{shortTerm, "anchor"},
			strict:  true,
			content: wildcardAcrossBoundary(shortMatch, chunkSize) + " anchor",
			want:    true,
		},
		{
			name:    "strict secondary maximum 272 bytes",
			words:   []string{"anchor", maxTerm},
			strict:  true,
			content: wildcardAcrossBoundaryWithLeadingText("anchor ", maxMatch, chunkSize),
			want:    true,
		},
		{
			name:    "relaxed primary maximum 272 bytes",
			words:   []string{maxTerm, "unavailable-secondary", "short"},
			content: wildcardAcrossBoundary(maxMatch, chunkSize) + " short",
			want:    true,
		},
		{
			name:    "relaxed secondary 120 byte expansion",
			words:   []string{"anchor", "unavailable-secondary", shortTerm},
			content: wildcardAcrossBoundaryWithLeadingText("anchor ", shortMatch, chunkSize),
			want:    true,
		},
		{
			name:    "rejects maximum 272 byte match attached to leading word byte across boundary",
			words:   []string{maxTerm, "anchor"},
			strict:  true,
			content: wildcardAttachedAcrossBoundary(maxMatch, chunkSize) + " anchor",
			want:    false,
		},
		{
			name:    "rejects artificial leading boundary at retained overlap start",
			words:   []string{maxTerm, "anchor"},
			strict:  true,
			content: wildcardAttachedAtOverlapStart(maxMatch, chunkSize, len(maxMatch)+1) + " anchor",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate.txt")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			found, decided := search.StreamContainsRankedWordsDecidedPartial(
				path, tt.words, tt.strict, search.PartialModeOff,
			)
			if found != tt.want || !decided {
				t.Fatalf("wildcard stream result = (%v, %v), want (%v, true)", found, decided, tt.want)
			}
		})
	}
}

func TestStreamWildcardChunkBoundaryWithCap(t *testing.T) {
	const chunkSize = 64 * 1024
	term, match := maximumWildcardFixture()
	content := wildcardAcrossBoundary(match, chunkSize) + " anchor"
	path := filepath.Join(t.TempDir(), "candidate.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	found, decided := search.StreamContainsAllWordsDecidedWithCapPartial(
		path, []string{term, "anchor"}, int64(len(content)), search.PartialModeOff,
	)
	if !found || !decided {
		t.Fatalf("complete capped wildcard result = (%v, %v), want (true, true)", found, decided)
	}

	found, decided = search.StreamContainsAllWordsDecidedWithCapPartial(
		path, []string{term, "missing"}, chunkSize, search.PartialModeOff,
	)
	if found || decided {
		t.Fatalf("incomplete capped result = (%v, %v), want (false, false)", found, decided)
	}
}

func wildcardExpansionFixture(expansion int) (term string, match string) {
	return "left*right", "left" + strings.Repeat("x", expansion) + "right"
}

func maximumWildcardFixture() (term string, match string) {
	segments := []string{
		strings.Repeat("a", 26),
		strings.Repeat("b", 26),
		strings.Repeat("c", 26),
		strings.Repeat("d", 25),
		strings.Repeat("e", 25),
	}
	questions := strings.Repeat("?", 16)
	questionMatch := strings.Repeat("q", 16)
	term = segments[0] + "*" + segments[1] + "*" + segments[2] + "*" +
		segments[3] + "*" + questions + segments[4]
	match = segments[0] + strings.Repeat("1", 32) +
		segments[1] + strings.Repeat("2", 32) +
		segments[2] + strings.Repeat("3", 32) +
		segments[3] + strings.Repeat("4", 32) +
		questionMatch + segments[4]
	return term, match
}

func wildcardAcrossBoundary(match string, boundary int) string {
	return wildcardAcrossBoundaryWithLeadingText("", match, boundary)
}

func wildcardAcrossBoundaryWithLeadingText(leading, match string, boundary int) string {
	start := boundary - len(match) + 1
	return leading + strings.Repeat("z", start-1-len(leading)) + "!" + match + "!"
}

func wildcardAttachedAcrossBoundary(match string, boundary int) string {
	start := boundary - len(match) + 1
	return strings.Repeat("z", start-1) + "x" + match + "!"
}

func wildcardAttachedAtOverlapStart(match string, boundary, overlap int) string {
	start := boundary - overlap
	return strings.Repeat("z", start-1) + "x" + match + "!" + strings.Repeat("z", boundary-start-len(match))
}

func writeXMLContainer(t *testing.T, filePath, entryName, content string) {
	t.Helper()
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create(entryName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

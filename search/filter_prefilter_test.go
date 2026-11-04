package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestBinaryPrefilterStrictnessAndRelaxedStrategyA(t *testing.T) {
	words := []string{"the", "ms-*-api", "synchronization"}

	tests := []struct {
		name    string
		content string
		strict  bool
		found   bool
	}{
		{
			name:    "strict requires every term",
			content: "the ms-option-api synchronization",
			strict:  true,
			found:   true,
		},
		{
			name:    "strict rejects a missing term",
			content: "the ms-option-api",
			strict:  true,
			found:   false,
		},
		{
			name:    "relaxed accepts shorter wildcard secondary when longest secondary is absent",
			content: "the ms-option-api",
			found:   true,
		},
		{
			name:    "relaxed rejects secondary without primary",
			content: "ms-option-api synchronization",
			found:   false,
		},
		{
			name:    "relaxed rejects primary when every secondary is absent",
			content: "the only",
			found:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate.eml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write candidate: %v", err)
			}

			found, decided := search.BinaryStreamingPrefilterRankedDecided(path, words, 0, tt.strict)
			if !decided {
				t.Fatal("prefilter was inconclusive for a small candidate")
			}
			if found != tt.found {
				t.Errorf("found = %v, want %v", found, tt.found)
			}
		})
	}
}

func TestBinaryPrefilterCompatibilityWrapperIsStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.eml")
	if err := os.WriteFile(path, []byte("primary secondary"), 0o600); err != nil {
		t.Fatal(err)
	}

	found, decided := search.BinaryStreamingPrefilterDecided(
		path, []string{"primary", "secondary", "missing"}, 0,
	)
	if found || !decided {
		t.Fatalf("compatibility wrapper result = (%v, %v), want (false, true)", found, decided)
	}
}

func TestBinaryWildcardChunkBoundaryAndIncompleteBudget(t *testing.T) {
	const chunkSize = 64 * 1024
	term, match := maximumWildcardFixture()
	content := wildcardAcrossBoundary(match, chunkSize) + " short"
	path := filepath.Join(t.TempDir(), "candidate.eml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	found, decided := search.BinaryStreamingPrefilterRankedDecidedPartial(
		path,
		[]string{term, "unavailable-secondary", "short"},
		int64(len(content)),
		false,
		search.PartialModeOff,
	)
	if !found || !decided {
		t.Fatalf("relaxed wildcard binary result = (%v, %v), want (true, true)", found, decided)
	}

	found, decided = search.BinaryStreamingPrefilterRankedDecidedPartial(
		path,
		[]string{term, "missing"},
		chunkSize,
		true,
		search.PartialModeOff,
	)
	if found || decided {
		t.Fatalf("incomplete binary budget = (%v, %v), want (false, false)", found, decided)
	}
}

func TestBinaryXMLWildcardChunkBoundary(t *testing.T) {
	const chunkSize = 64 * 1024
	shortTerm, shortMatch := wildcardExpansionFixture(120)
	maxTerm, maxMatch := maximumWildcardFixture()

	tests := []struct {
		name      string
		extension string
		entry     string
		term      string
		match     string
	}{
		{name: "DOCX 120 byte expansion", extension: ".docx", entry: "word/document.xml", term: shortTerm, match: shortMatch},
		{name: "ODT maximum 272 bytes", extension: ".odt", entry: "content.xml", term: maxTerm, match: maxMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := wildcardAcrossBoundary(tt.match, chunkSize) + " anchor"
			path := filepath.Join(t.TempDir(), "candidate"+tt.extension)
			writeXMLContainer(t, path, tt.entry, content)

			found, decided := search.BinaryStreamingPrefilterRankedDecidedPartial(
				path, []string{tt.term, "anchor"}, int64(len(content)), true, search.PartialModeOff,
			)
			if !found || !decided {
				t.Fatalf("XML wildcard result = (%v, %v), want (true, true)", found, decided)
			}
		})
	}
}

func TestBinaryXMLMissingTermCapDecision(t *testing.T) {
	content := "<document>" + strings.Repeat("available ", 32) + "</document>"
	formats := []struct {
		name      string
		extension string
		entry     string
	}{
		{name: "DOCX", extension: ".docx", entry: "word/document.xml"},
		{name: "ODT", extension: ".odt", entry: "content.xml"},
	}
	caps := []struct {
		name        string
		bytes       int64
		wantDecided bool
	}{
		{name: "below entry size", bytes: int64(len(content) - 1), wantDecided: false},
		{name: "at entry size", bytes: int64(len(content)), wantDecided: true},
		{name: "above entry size", bytes: int64(len(content) + 1), wantDecided: true},
	}

	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate"+format.extension)
			writeXMLContainer(t, path, format.entry, content)

			for _, cap := range caps {
				t.Run(cap.name, func(t *testing.T) {
					found, decided := search.BinaryStreamingPrefilterDecided(
						path, []string{"available", "missing"}, cap.bytes,
					)
					if found || decided != cap.wantDecided {
						t.Fatalf(
							"XML missing-term result = (%v, %v), want (false, %v)",
							found,
							decided,
							cap.wantDecided,
						)
					}
				})
			}
		})
	}
}

func TestBinaryDiscoveryPrefiltersOnlyPrimary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.eml")
	if err := os.WriteFile(path, []byte("primary only"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := search.FindFilesWithFirstWordProgressPartial(
		[]string{"primary", "missing-secondary"},
		nil,
		1,
		nil,
		dir,
		nil,
		search.PartialModeOff,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != path {
		t.Fatalf("binary discovery files = %v, want [%s]", files, path)
	}
}

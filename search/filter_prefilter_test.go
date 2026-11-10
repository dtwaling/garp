package search_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"garp/search"
)

// These fixtures contain encoded terms invisible to raw-byte matching. Reuse the real
// compound-file fixture so MSG exercises its UTF-16LE subject and body streams.
func encodedBinaryFixtures() map[string][]byte {
	encoded := base64.StdEncoding.EncodeToString([]byte("<html><body>subject body</body></html>"))
	eml := "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"
	return map[string][]byte{
		"base64.eml":           []byte(eml),
		"quoted-printable.eml": []byte("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nsu=\r\nbject bo=64y\r\n"),

		"encoded.mbox":      []byte("From sender@example.com Mon Jan 1 00:00:00 2024\n" + eml),
		"unicode.msg":       readerAtMSG(),
		"control-words.rtf": []byte(`{\rtf1\ansi su{\b}bject bo{\i}dy}`),
	}
}

func TestEncodedBinarySearchPipeline(t *testing.T) {
	for name, data := range encodedBinaryFixtures() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name           string
				words, exclude []string
				partial        search.PartialMode
				want           int
			}{
				{name: "subject", words: []string{"subject"}, want: 1},
				{name: "body", words: []string{"body"}, want: 1},
				{name: "case insensitive", words: []string{"SUBJECT", "BODY"}, want: 1},
				{name: "wildcards", words: []string{"subj*", "bo?y"}, want: 1},
				{name: "partial", words: []string{"subj", "bod"}, partial: search.PartialModePrefix, want: 1},
				{name: "absent primary", words: []string{"unfindableterm"}},
				{name: "absent secondary", words: []string{"subject", "unfindableterm"}},
				{name: "decoded exclusion", words: []string{"subject"}, exclude: []string{"body"}},
			} {
				for _, strict := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/strict=%t", tc.name, strict), func(t *testing.T) {
						engine := search.NewSearchEngineWithWorkers(tc.words, tc.exclude, nil, false, 1, 5000, 1)
						engine.StartDir, engine.Silent = root, true
						engine.Strict, engine.Partial, engine.Distance = strict, tc.partial, 10000
						// Explicit Stage 2 assertions prevent Stage 3 from masking a
						// dropped candidate or a spurious prefilter acceptance.
						matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
						if err != nil || len(matches) != tc.want {
							t.Fatalf("Stage 2: matches=%v err=%v, want %d", matches, err, tc.want)
						}
						results, err := engine.Execute()
						if err != nil || len(results) != tc.want {
							t.Fatalf("Execute: results=%v err=%v, want %d", results, err, tc.want)
						}
						if tc.want == 1 && results[0].FilePath != path {
							t.Fatalf("wrong result path: %s", results[0].FilePath)
						}
					})
				}
			}
		})
	}
}

func TestEncodedBinaryCLIOutputParity(t *testing.T) {
	// Build once and invoke the actual CLI, including argument parsing and all
	// three output paths, against the same encoded fixtures as the engine test.
	binary := filepath.Join(t.TempDir(), "garp.exe")
	if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for name, data := range encodedBinaryFixtures() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"SUBJECT", "BODY", "subj*", "bo?y", "unfindableterm"} {
				t.Run(query, func(t *testing.T) {
					want := 1
					if query == "unfindableterm" {
						want = 0
					}
					for _, mode := range []string{"--json", "--plain", "--pathonly"} {
						out, err := exec.Command(binary, query, "--startdir", root, mode).Output()
						if err != nil {
							t.Fatalf("%s: %v", mode, err)
						}
						switch mode {
						case "--json":
							var result struct {
								Matches int `json:"matches"`
								Results []struct {
									File string `json:"file"`
								} `json:"results"`
							}
							if err := json.Unmarshal(out, &result); err != nil {
								t.Fatal(err)
							}
							if result.Matches != want || len(result.Results) != want {
								t.Fatalf("JSON: %s, want %d", out, want)
							}
							if want == 1 && result.Results[0].File != path {
								t.Fatalf("JSON path: %s", out)
							}
						case "--plain":
							quotedPath := strconv.Quote(path)
							if strings.Count(string(out), "MATCH 1/1 ") != want || strings.Contains(string(out), quotedPath[1:len(quotedPath)-1]) != (want == 1) {
								t.Fatalf("plain: %s, want %d", out, want)
							}
						case "--pathonly":
							wantOutput := ""
							if want == 1 {
								quotedPath := strconv.Quote(path)
								wantOutput = quotedPath[1:len(quotedPath)-1] + "\n"
							}
							if string(out) != wantOutput {
								t.Fatalf("pathonly: %q, want %q", out, wantOutput)
							}
						}
					}
				})
			}
		})
	}
}

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
				t.Fatal("complete unencoded mail scan must be decisive")
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

func TestBinaryPrefilterEncodingGates(t *testing.T) {
	for _, ext := range []string{".eml", ".mbox"} {
		for _, marker := range []string{"BaSe64", "QUOTED-PRINTABLE", "=?", "ChArSeT=iso-8859-1", "TeXt/HtMl", "UTF-16", "UcS-2", "\x00"} {
			for _, offset := range []int{0, 64*1024 - 1} {
				t.Run(fmt.Sprintf("%s/%q/offset=%d", ext, marker, offset), func(t *testing.T) {
					content := strings.Repeat(" ", offset) + marker + " available"
					path := filepath.Join(t.TempDir(), "candidate"+ext)
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
					for _, capBytes := range []int64{0, int64(len(content))} {
						found, decided := search.BinaryStreamingPrefilterDecided(path, []string{"missing"}, capBytes)
						if found || decided {
							t.Fatalf("encoded miss = (%v, %v), want (false, false)", found, decided)
						}
						found, decided = search.BinaryStreamingPrefilterDecided(path, []string{"available"}, capBytes)
						if !found || !decided {
							t.Fatalf("encoded raw hit = (%v, %v), want (true, true)", found, decided)
						}
					}
				})
			}
		}
	}
}

func TestBinaryPrefilterPlainAndRTFDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, content, word string
		found, decided      bool
	}{
		{"plain.eml", "available", "missing", false, true},
		{"plain.mbox", "available", "missing", false, true},
		{"plain.rtf", `{\rtf1\ansi available}`, "missing", false, true},
		{"cleaned.rtf", `{\rtf1\ansi PDF32}`, "PDF", true, true},
		{"html.eml", "Content-Type: text/html\r\n\r\n<p>R&amp;D</p>", "R&D", false, false},
		{"html.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nContent-Type: text/html\r\n\r\n<p>R&amp;D</p>", "R&D", false, false},
		{"collapsed.eml", "Content-Type: text/plain\r\n\r\nintro\r\n>PDF32\r\n", "PDF", false, false},
		{"collapsed.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nContent-Type: text/plain\r\n\r\nintro\r\n>PDF32\r\n", "PDF", false, false},
		{"cleaned.eml", "Content-Type: text/plain\r\n\r\nPDF32", "PDF", false, false},
		{"cleaned.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nContent-Type: text/plain\n\nPDF32", "PDF", false, false},
		{"latin1.eml", "Content-Type: text/plain; charset=iso-8859-1\r\n\r\ncaf\xe9", "café", false, false},
		{"latin1.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nContent-Type: text/plain; charset=iso-8859-1\n\ncaf\xe9", "café", false, false},
		{"controls.rtf", `{\rtf1\ansi su{\b}bject bo{\i}dy}`, "subject", true, true},
		{"hex.rtf", `{\rtf1 \'61}`, "missing", false, false},
		{"unicode.rtf", `{\rtf1 \u97}`, "missing", false, false},
		{"binary.rtf", `{\rtf1 \bin3 abc}`, "missing", false, false},
		{"raw.msg", "available", "available", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, capBytes := range []int64{0, int64(len(tc.content)), int64(len(tc.content) + 1)} {
				found, decided := search.BinaryStreamingPrefilterDecided(path, []string{tc.word}, capBytes)
				if found != tc.found || decided != tc.decided {
					t.Fatalf("cap=%d: got (%v, %v), want (%v, %v)", capBytes, found, decided, tc.found, tc.decided)
				}
			}
			found, decided := search.BinaryStreamingPrefilterDecided(path, []string{"missing"}, 1)
			if found || decided {
				t.Fatalf("capped miss = (%v, %v), want (false, false)", found, decided)
			}
			if tc.word == "PDF" || tc.word == "café" || tc.word == "R&D" {
				for _, strict := range []bool{false, true} {
					engine := search.NewSearchEngineWithWorkers([]string{tc.word}, nil, nil, false, 1, 5000, 1)
					engine.StartDir, engine.Silent, engine.Strict = filepath.Dir(path), true, strict
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil || len(matches) != 1 {
						t.Fatalf("Stage 2 strict=%t: matches=%v err=%v", strict, matches, err)
					}
					results, err := engine.Execute()
					if err != nil || len(results) != 1 || results[0].FilePath != path {
						t.Fatalf("Execute strict=%t: results=%v err=%v", strict, results, err)
					}
				}
			}
		})
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

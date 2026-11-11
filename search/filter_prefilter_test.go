package search

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"garp/config"
)

// These fixtures contain terms that need decoding or extraction. Reuse the real
// compound-file layout for ANSI and UTF-16LE DOC and MSG streams.
func encodedBinaryFixtures() map[string][]byte {
	encoded := base64.StdEncoding.EncodeToString([]byte("<html><body>subject body</body></html>"))
	eml := "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n"
	return map[string][]byte{
		"base64.eml":           []byte(eml),
		"quoted-printable.eml": []byte("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nsu=\r\nbject bo=64y\r\n"),
		"encoded-subject.eml":  []byte("Subject: =?UTF-8?B?c3ViamVjdA==?=\r\n\r\nbody\r\n"),

		"encoded.mbox":      []byte("From sender@example.com Mon Jan 1 00:00:00 2024\n" + eml),
		"subject.mbox":      []byte("From sender@example.com Mon Jan 1 00:00:00 2024\nSubject: other\n\nfiller\nFrom sender@example.com Mon Jan 1 00:00:00 2024\nSubject: subject\n\nbody\n"),
		"unicode.msg":       readerAtMSG(),
		"ansi.msg":          ansiMSG(),
		"eight-bit.doc":     docWithWordStream([]byte("subject body")),
		"utf16.doc":         docWithWordStream(utf16LE("subject body")),
		"control-words.rtf": []byte(`{\rtf1\ansi su{\b}bject bo{\i}dy}`),
	}
}

func TestEncodedBinarySearchPipeline(t *testing.T) {
	mail := "Subject: Alpha\r\nFrom: sender@example.com\r\n\r\nBeta\r\n"
	joinedMail := map[string][]byte{
		"subject-body.eml":  []byte(mail),
		"subject-body.mbox": []byte("From sender@example.com Mon Jan 1 00:00:00 2024\n" + mail),
		"later-message.mbox": []byte("From sender@example.com Mon Jan 1 00:00:00 2024\nSubject: other\n\nfiller\n" +
			"From sender@example.com Mon Jan 1 00:00:00 2024\n" + mail),
	}
	for _, tc := range []struct {
		name           string
		words, exclude []string
		partial        PartialMode
		want           int
		fixtures       map[string][]byte
	}{
		{name: "subject", words: []string{"subject"}, want: 1},
		{name: "body", words: []string{"body"}, want: 1},
		{name: "case insensitive", words: []string{"SUBJECT", "BODY"}, want: 1},
		{name: "wildcards", words: []string{"subj*", "bo?y"}, want: 1},
		{name: "partial", words: []string{"subj", "bod"}, partial: PartialModePrefix, want: 1},
		{name: "absent primary", words: []string{"unfindableterm"}},
		{name: "absent secondary", words: []string{"subject", "unfindableterm"}},
		{name: "decoded exclusion", words: []string{"subject"}, exclude: []string{"body"}},
		{name: "subject exclusion", words: []string{"body"}, exclude: []string{"subject"}},
		{name: "subject body phrase", words: []string{"alpha beta"}, want: 1, fixtures: joinedMail},
		{name: "secondary subject body phrase", words: []string{"alpha", "alpha beta"}, want: 1, fixtures: joinedMail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtures := tc.fixtures
			if fixtures == nil {
				fixtures = encodedBinaryFixtures()
			}
			for name, data := range fixtures {
				root := t.TempDir()
				path := filepath.Join(root, name)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				for _, strict := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/strict=%t", name, strict), func(t *testing.T) {
						engine := NewSearchEngineWithWorkers(tc.words, tc.exclude, nil, false, 1, 5000, 1)
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

			found, decided := BinaryStreamingPrefilterRankedDecided(path, words, 0, tt.strict)
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

	found, decided := BinaryStreamingPrefilterDecided(
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
						found, decided := BinaryStreamingPrefilterDecided(path, []string{"missing"}, capBytes)
						if found || decided {
							t.Fatalf("encoded miss = (%v, %v), want (false, false)", found, decided)
						}
						found, decided = BinaryStreamingPrefilterDecided(path, []string{"available"}, capBytes)
						if !found || !decided {
							t.Fatalf("encoded raw hit = (%v, %v), want (true, true)", found, decided)
						}
					}
					paths, err := FindFilesWithFirstWordProgress([]string{"missing"}, nil, 1, nil, filepath.Dir(path), nil)
					if err != nil || len(paths) != 1 || paths[0] != path {
						t.Fatalf("encoded admission=%v err=%v", paths, err)
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
		{"phrase.eml", "Subject: Alpha\r\nFrom: sender@example.com\r\n\r\nBeta\r\n", "alpha beta", false, false},
		{"phrase.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nSubject: Alpha\r\nFrom: sender@example.com\r\n\r\nBeta\r\n", "alpha beta", false, false},
		{"tab-phrase.eml", "available", "alpha	beta", false, false},
		{"unicode-space-phrase.mbox", "available", "alpha\u00a0beta", false, false},
		{"raw-phrase.eml", "Subject: Alpha Beta\r\n\r\n", "alpha beta", true, true},
		{"raw-phrase.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nSubject: Alpha Beta\r\n\r\n", "alpha beta", true, true},
		{"phrase.rtf", `{\rtf1\ansi available}`, "alpha beta", false, true},
		{"plain.rtf", `{\rtf1\ansi available}`, "missing", false, true},
		{"cleaned.rtf", `{\rtf1\ansi PDF32}`, "PDF", true, true},
		// Cleaning removes the literal token, so only the raw streaming path
		// can accept it before the escape marker beyond the first 64 KiB.
		{"raw-hit.rtf", `{\rtf1\ansi <available> ` + strings.Repeat(" ", 64*1024) + `\'73}`, "available", true, true},
		{"comparison.eml", "Subject: Estimate < 100\r\n\r\nPDF32 > 50", "PDF", false, false},
		{"comparison.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nSubject: Estimate < 100\r\n\r\nPDF32 > 50", "PDF", false, false},
		{"address.eml", "From: Alice <alice@example.com>\r\n\r\navailable", "missing", false, true},
		{"address.mbox", "From sender@example.com Mon Jan 1 00:00:00 2024\nFrom: Alice <alice@example.com>\r\n\r\navailable", "missing", false, true},
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
				found, decided := BinaryStreamingPrefilterDecided(path, []string{tc.word}, capBytes)
				if found != tc.found || decided != tc.decided {
					t.Fatalf("cap=%d: got (%v, %v), want (%v, %v)", capBytes, found, decided, tc.found, tc.decided)
				}
			}
			found, decided := BinaryStreamingPrefilterDecided(path, []string{"missing"}, 1)
			if found || decided {
				t.Fatalf("capped miss = (%v, %v), want (false, false)", found, decided)
			}
			if !tc.decided {
				paths, err := FindFilesWithFirstWordProgress([]string{tc.word}, nil, 1, nil, filepath.Dir(path), nil)
				if err != nil || len(paths) != 1 || paths[0] != path {
					t.Fatalf("undecided admission=%v err=%v", paths, err)
				}
			}
			if tc.word == "PDF" || tc.word == "café" || tc.word == "R&D" {
				for _, strict := range []bool{false, true} {
					engine := NewSearchEngineWithWorkers([]string{tc.word}, nil, nil, false, 1, 5000, 1)
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

	found, decided := BinaryStreamingPrefilterRankedDecidedPartial(
		path,
		[]string{term, "unavailable-secondary", "short"},
		int64(len(content)),
		false,
		PartialModeOff,
	)
	if !found || !decided {
		t.Fatalf("relaxed wildcard binary result = (%v, %v), want (true, true)", found, decided)
	}

	found, decided = BinaryStreamingPrefilterRankedDecidedPartial(
		path,
		[]string{term, "missing"},
		chunkSize,
		true,
		PartialModeOff,
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

			found, decided := BinaryStreamingPrefilterRankedDecidedPartial(
				path, []string{tt.term, "anchor"}, int64(len(content)), true, PartialModeOff,
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
					found, decided := BinaryStreamingPrefilterDecided(
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

	files, err := FindFilesWithFirstWordProgressPartial(
		[]string{"primary", "missing-secondary"},
		nil,
		1,
		nil,
		dir,
		nil,
		PartialModeOff,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != path {
		t.Fatalf("binary discovery files = %v, want [%s]", files, path)
	}
}

func TestBinaryPrefilterPlainMissCapBoundary(t *testing.T) {
	for _, ext := range []string{".eml", ".mbox", ".rtf"} {
		capBytes := 256 * 1024
		if ext == ".rtf" {
			capBytes = 2 * 1024 * 1024
		}
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/size=cap%+d", ext, delta), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "plain"+ext)
				content := strings.Repeat(" ", capBytes+delta)
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				found, decided := BinaryStreamingPrefilterDecided(path, []string{"missing"}, int64(capBytes))
				if found || decided != (delta <= 0) {
					t.Fatalf("decision=(%v,%v), delta=%d", found, decided, delta)
				}
				engine := NewSearchEngineWithWorkers([]string{"missing"}, nil, nil, false, 1, 5000, 1)
				engine.StartDir, engine.Silent, engine.Strict = root, true, true
				paths, total, err := engine.DiscoverCandidates(1)
				want := 0
				if delta > 0 {
					want = 1
				}
				if err != nil || len(paths) != want {
					t.Fatalf("discovery=%v err=%v, want %d candidates", paths, err, want)
				}
				matches, err := engine.FilterCandidates(paths, total, time.Now())
				if err != nil || len(matches) != 0 {
					t.Fatalf("Stage 2 matches=%v err=%v, want no matches", matches, err)
				}
			})
		}
	}
}

func TestCheckTextContainsAllWordsPartial(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		words    []string
		distance int
		partial  PartialMode
		want     bool
	}{
		{name: "prefix uses captured token start for distance", text: "_deployment service", words: []string{"deploy", "service"}, distance: 11, partial: PartialModePrefix, want: true},
		{name: "off rejects derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: PartialModeOff, want: false},
		{name: "prefix accepts derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: PartialModePrefix, want: true},
		{name: "contains accepts derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: PartialModeContains, want: true},
		{name: "inline glob expands while off", text: "deployment service", words: []string{"deploy*", "service"}, distance: 50, partial: PartialModeOff, want: true},
		{name: "prefix rejects internal substring", text: "redeploy service", words: []string{"deploy", "service"}, distance: 50, partial: PartialModePrefix, want: false},
		{name: "contains accepts internal substring", text: "redeploy service", words: []string{"deploy", "service"}, distance: 50, partial: PartialModeContains, want: true},
		{name: "distance remains bounded", text: "deploying " + strings.Repeat("x", 300) + " service", words: []string{"deploy", "service"}, distance: 100, partial: PartialModePrefix, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckTextContainsAllWordsPartial(tt.text, tt.words, tt.distance, tt.partial); got != tt.want {
				t.Fatalf("CheckTextContainsAllWordsPartial() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPartialStreamingPrefilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.eml")
	if err := os.WriteFile(path, []byte(".deployment service"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	for _, tt := range []struct {
		name    string
		words   []string
		partial PartialMode
		want    bool
	}{
		{name: "off rejects derivative", words: []string{"deploy", "service"}, partial: PartialModeOff, want: false},
		{name: "prefix accepts derivative", words: []string{"deploy", "service"}, partial: PartialModePrefix, want: true},
		{name: "glob expands while off", words: []string{"deploy*", "service"}, partial: PartialModeOff, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found, decided := BinaryStreamingPrefilterDecidedPartial(path, tt.words, 0, tt.partial)
			if !decided {
				t.Fatal("complete unencoded mail scan must be decisive")
			}
			if found != tt.want {
				t.Fatalf("BinaryStreamingPrefilterDecidedPartial() = %v, want %v", found, tt.want)
			}
		})
	}

	underscorePath := filepath.Join(t.TempDir(), "underscore.eml")
	if err := os.WriteFile(underscorePath, []byte("_deployment service"), 0o600); err != nil {
		t.Fatalf("write underscore candidate: %v", err)
	}
	found, decided := BinaryStreamingPrefilterDecidedPartial(
		underscorePath,
		[]string{"deploy", "service"},
		0,
		PartialModePrefix,
	)
	if !decided || !found {
		t.Fatalf("plain prefix underscore fixture = found:%v decided:%v, want true/true", found, decided)
	}
}

func TestSearchEngineThreadsPartialModeThroughDiscoveryAndFiltering(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "candidate.txt"), []byte("_deployment service"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	for _, tt := range []struct {
		name    string
		partial PartialMode
		want    int
	}{
		{name: "off rejects derivative at discovery", partial: PartialModeOff, want: 0},
		{name: "prefix retains derivative through stage two", partial: PartialModePrefix, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewSearchEngineWithWorkers([]string{"deploy", "service"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
			engine.StartDir = root
			engine.Distance = 50
			engine.Partial = tt.partial
			engine.Silent = true

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != tt.want {
				t.Fatalf("result count = %d, want %d", len(results), tt.want)
			}
		})
	}
}

type errorAfterDataReader struct {
	data []byte
	err  error
	read bool
}

func (r *errorAfterDataReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	return copy(p, r.data), r.err
}

func TestStreamQueryReadErrorDecision(t *testing.T) {
	readErr := errors.New("injected read failure")

	t.Run("unproven match remains undecided", func(t *testing.T) {
		query := compileQuery([]string{"missing"}, PartialModeOff)
		found, complete, err := streamMatchesQuery(
			&errorAfterDataReader{data: []byte("partial data"), err: readErr},
			query,
			true,
			1024,
		)
		gotFound, gotDecided := decideStreamQueryResult(found, complete, err, true)
		if gotFound || gotDecided {
			t.Fatalf("read error result = (%v, %v), want (false, false)", gotFound, gotDecided)
		}
	})

	t.Run("proven match survives read error", func(t *testing.T) {
		query := compileQuery([]string{"needle"}, PartialModeOff)
		found, complete, err := streamMatchesQuery(
			&errorAfterDataReader{data: []byte("needle"), err: readErr},
			query,
			true,
			1024,
		)
		gotFound, gotDecided := decideStreamQueryResult(found, complete, err, true)
		if !gotFound || !gotDecided {
			t.Fatalf("proven match result = (%v, %v), want (true, true)", gotFound, gotDecided)
		}
	})
}

func TestReaderValidatedEOF(t *testing.T) {
	readErr := errors.New("injected checksum failure")

	t.Run("validated EOF", func(t *testing.T) {
		if !readerValidatedEOF(strings.NewReader("")) {
			t.Fatal("empty reader did not validate EOF")
		}
	})
	t.Run("understated size exposes more data", func(t *testing.T) {
		if readerValidatedEOF(strings.NewReader("x")) {
			t.Fatal("reader with remaining data validated EOF")
		}
	})
	t.Run("checksum failure remains undecided", func(t *testing.T) {
		if readerValidatedEOF(&errorAfterDataReader{err: readErr}) {
			t.Fatal("reader error validated EOF")
		}
	})
}

func TestStreamPrefilterOpenFailureIsUndecided(t *testing.T) {
	missingText := filepath.Join(t.TempDir(), "missing.txt")
	missingEmail := filepath.Join(t.TempDir(), "missing.eml")
	missingRTF := filepath.Join(t.TempDir(), "missing.rtf")

	tests := []struct {
		name string
		run  func() (bool, bool)
	}{
		{
			name: "all words text",
			run: func() (bool, bool) {
				return StreamContainsAllWordsDecided(missingText, []string{"needle"})
			},
		},
		{
			name: "ranked text",
			run: func() (bool, bool) {
				return StreamContainsRankedWordsDecided(missingText, []string{"needle", "anchor"}, true)
			},
		},
		{
			name: "capped stream",
			run: func() (bool, bool) {
				return StreamContainsAllWordsDecidedWithCap(missingText, []string{"needle"}, 1024)
			},
		},
		{
			name: "email binary",
			run: func() (bool, bool) {
				return BinaryStreamingPrefilterDecided(missingEmail, []string{"needle"}, 1024)
			},
		},
		{
			name: "RTF binary",
			run: func() (bool, bool) {
				return BinaryStreamingPrefilterDecided(missingRTF, []string{"needle"}, 1024)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, decided := tt.run()
			if found || decided {
				t.Fatalf("open failure result = (%v, %v), want (false, false)", found, decided)
			}
		})
	}
}

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

			found, decided := StreamContainsRankedWordsDecided(path, words, tt.strict)
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

	score, count, terms, span, ok, err := CheckFileContainsRankedWords(path, []string{"alpha", "beta", "gamma"}, 20, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || score != 10 || count != 2 || strings.Join(terms, ",") != "alpha,gamma" || span != 11 {
		t.Fatalf("ranked file match = (%d, %d, %v, %d, %v), want (10, 2, [alpha gamma], 11, true)", score, count, terms, span, ok)
	}
}

func TestStreamReaders(t *testing.T) {
	const chunkSize = 64 * 1024
	const defaultTextCap = 5 << 20
	for _, tt := range []struct {
		name, content  string
		found, decided bool
	}{
		{"MatchAcrossChunkBoundary", strings.Repeat("x", chunkSize-3) + " alpha beta", true, true},
		{"StopAtDefaultTextCap", strings.Repeat(" ", defaultTextCap) + "alpha beta", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate.txt")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			words := []string{"alpha", "beta"}
			for _, reader := range []string{"all-words", "ranked"} {
				t.Run(reader, func(t *testing.T) {
					var found, decided bool
					if reader == "all-words" {
						found, decided = StreamContainsAllWordsDecided(path, words)
					} else {
						found, decided = StreamContainsRankedWordsDecided(path, words, true)
					}
					if found != tt.found || decided != tt.decided {
						t.Fatalf("match = (%v, %v), want (%v, %v)", found, decided, tt.found, tt.decided)
					}
				})
			}
		})
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
			found, decided := StreamContainsRankedWordsDecidedPartial(
				path, tt.words, tt.strict, PartialModeOff,
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

	found, decided := StreamContainsAllWordsDecidedWithCapPartial(
		path, []string{term, "anchor"}, int64(len(content)), PartialModeOff,
	)
	if !found || !decided {
		t.Fatalf("complete capped wildcard result = (%v, %v), want (true, true)", found, decided)
	}

	found, decided = StreamContainsAllWordsDecidedWithCapPartial(
		path, []string{term, "missing"}, chunkSize, PartialModeOff,
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

const literalAuditPhrase = "can you confirm that the quarterly reconciliation for the northwind account, including every adjustment, was approved? the auditors asked"

func cleanedAdmissionFixtures(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	text := "Please review PDF32 for 2026Report balance\ndetails, foo<script>x</script>bar R&amp;D\n" +
		literalAuditPhrase + " again\nx a*b c*d e*f g*h i*j y\n"
	fixtures := make(map[string]string)
	for _, ext := range []string{"txt", "md", "html", "go", "docx", "odt", "eml", "mbox", "rtf", "doc"} {
		path := filepath.Join(dir, "sample."+ext)
		data := []byte(text)
		switch ext {
		case "docx", "odt":
			entry := "word/document.xml"
			if ext == "odt" {
				entry = "content.xml"
			}
			writeXMLContainer(t, path, entry, "<document><p>"+text+"</p></document>")
		case "eml", "mbox":
			data = []byte("Subject: ordinary\nContent-Type: text/plain\n\n" + text)
			if ext == "mbox" {
				data = append([]byte("From sender@example.com Mon Jan 1 00:00:00 2024\n"), data...)
			}
		case "rtf":
			data = []byte(`{\rtf1 ` + text + `}`)
		case "doc":
			data = docWithWordStream(data)
		}
		if ext != "docx" && ext != "odt" {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		fixtures[ext] = path
	}
	return fixtures
}

func TestPrefiltersAdmitEveryCleanedMatch(t *testing.T) {
	if len(literalAuditPhrase) != 137 {
		t.Fatal("literal phrase must exercise the wildcard byte limit")
	}
	for ext, path := range cleanedAdmissionFixtures(t) {
		t.Run(ext, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if IsBinaryFormat(path) {
				extractor, ok := NewExtractorRegistry().GetExtractor(ext)
				if !ok {
					t.Fatal("missing extractor")
				}
				text, err = extractor.ExtractText(data)
				if err != nil {
					t.Fatal(err)
				}
			}
			cleaned := cleanSearchContent(mappedContent{text: text}, config.IsCodeFile(path)).text
			for _, tc := range []struct {
				terms []string
				mode  PartialMode
				opts  QueryOptions
			}{
				{[]string{"pdf"}, PartialModeOff, QueryOptions{}},
				{[]string{"report"}, PartialModeOff, QueryOptions{}},
				{[]string{"32"}, PartialModeOff, QueryOptions{}},
				{[]string{"PDF"}, PartialModeOff, QueryOptions{CaseSensitive: true}},
				{[]string{"balance details"}, PartialModeOff, QueryOptions{}},
				{[]string{"pdf 32"}, PartialModeOff, QueryOptions{}},
				{[]string{"details, foo"}, PartialModeOff, QueryOptions{}},
				{[]string{"review", "pdf"}, PartialModeOff, QueryOptions{}},
				{[]string{"pdf", "zzqqx", "report"}, PartialModeOff, QueryOptions{}},
				{[]string{"foobar"}, PartialModeOff, QueryOptions{}},
				{[]string{"rep"}, PartialModePrefix, QueryOptions{}},
				{[]string{"P?F"}, PartialModeOff, QueryOptions{}},
				{[]string{literalAuditPhrase}, PartialModeOff, QueryOptions{}},
				{[]string{"a*b c*d e*f g*h i*j"}, PartialModeOff, QueryOptions{}},
			} {
				for _, strict := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/strict=%v", strings.Join(tc.terms, "+"), strict), func(t *testing.T) {
						query := compileQueryWithOptions(tc.terms, tc.mode, tc.opts)
						_, matches := analyzeRankedText(cleaned, query, 5000, strict)
						if (tc.terms[0] == literalAuditPhrase || tc.terms[0] == "a*b c*d e*f g*h i*j") && !matches {
							t.Fatal("literal phrase fixture must match authoritative text")
						}
						if !matches {
							return
						}
						found, decided := streamContainsRankedWordsDecidedQuery(path, query, strict, defaultTextMaxBytes)
						if IsBinaryFormat(path) {
							found, decided = binaryStreamingPrefilterDecidedQuery(path, query, defaultTextMaxBytes, strict)
						}
						if decided && !found {
							t.Error("Stage 2 rejected authoritative match")
						}
						files, err := findFilesWithFirstWordProgressQueryWithOptions(query, []string{ext}, 1, nil, filepath.Dir(path), nil, tc.mode, WalkOptions{})
						if err != nil || !slices.Contains(files, path) {
							t.Errorf("Stage 1: files=%v err=%v", files, err)
						}
						files, err = FindFilesWithFirstWordPartial(tc.terms[0], []string{ext}, filepath.Dir(path), nil, tc.mode)
						if err != nil || !slices.Contains(files, path) {
							t.Errorf("legacy Stage 1: files=%v err=%v", files, err)
						}
					})
				}
			}
		})
	}
}

func TestPrefiltersStayDecisiveOnOrdinaryMisses(t *testing.T) {
	for ext, path := range cleanedAdmissionFixtures(t) {
		if ext == "doc" {
			continue
		}
		for _, terms := range [][]string{{"zzqqx"}, {"review zzqqx"}, {"review", "zzqqx"}} {
			t.Run(ext+"/"+strings.Join(terms, "+"), func(t *testing.T) {
				if len(terms) == 1 && strings.Contains(terms[0], " ") && (ext == "eml" || ext == "mbox") {
					return
				}
				q := compileQuery(terms, PartialModeOff)
				found, decided := streamContainsRankedWordsDecidedQuery(path, q, true, defaultTextMaxBytes)
				if IsBinaryFormat(path) {
					found, decided = binaryStreamingPrefilterDecidedQuery(path, q, defaultTextMaxBytes, true)
				}
				if found || !decided {
					t.Errorf("Stage 2=(%v,%v), want (false,true)", found, decided)
				}
				// Discovery only considers the primary term, not absent secondary terms.
				files, err := findFilesWithFirstWordProgressQueryWithOptions(q, []string{ext}, 1, nil, filepath.Dir(path), nil, PartialModeOff, WalkOptions{})
				if err != nil || slices.Contains(files, path) != (len(terms) > 1) {
					t.Errorf("Stage 1: files=%v err=%v", files, err)
				}
			})
		}
	}
}

func writeXMLContainer(t *testing.T, filePath, entryName, content string) {
	t.Helper()
	if err := os.WriteFile(filePath, readerAtZIP(t, entryName, content, 0), 0o600); err != nil {
		t.Fatal(err)
	}
}

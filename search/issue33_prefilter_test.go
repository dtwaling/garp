package search_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func issue33Engine(root string, words []string) *search.SearchEngine {
	e := search.NewSearchEngineWithWorkers(words, nil, nil, false, 1, 5000, 1)
	e.StartDir, e.Silent, e.Strict, e.Distance = root, true, true, 10000
	return e
}

func TestIssue33PlainMissCapBoundary(t *testing.T) {
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
				found, decided := search.BinaryStreamingPrefilterDecided(path, []string{"missing"}, int64(capBytes))
				if found || decided != (delta <= 0) {
					t.Fatalf("decision=(%v,%v), delta=%d", found, decided, delta)
				}
				matches, _, err := search.Issue33StagesForTest(issue33Engine(root, []string{"missing"}), func(n int) {
					want := 0
					if delta > 0 {
						want = 1
					}
					if n != want {
						t.Fatalf("Stage 1 candidates=%d, want %d", n, want)
					}
				})
				if err != nil || matches != 0 {
					t.Fatalf("Stage 2 matches=%d err=%v", matches, err)
				}
			})
		}
	}
}

func TestIssue33MarkerAdmission(t *testing.T) {
	markers := map[string][]string{
		".eml":  {"BaSe64", "QUOTED-PRINTABLE", "=?UTF-8?B?c3ViamVjdA==?=", "UTF-16", "UcS-2", "\x00"},
		".mbox": {"BaSe64", "QUOTED-PRINTABLE", "=?UTF-8?B?c3ViamVjdA==?=", "UTF-16", "UcS-2", "\x00"},
		".rtf":  {`\'73`, `\u115?`, `\bin3 abc`},
	}
	for ext, values := range markers {
		for _, marker := range values {
			t.Run(fmt.Sprintf("%s/%q", ext, marker), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "encoded"+ext)
				if err := os.WriteFile(path, []byte(marker), 0600); err != nil {
					t.Fatal(err)
				}
				found, decided := search.BinaryStreamingPrefilterDecided(path, []string{"subject"}, int64(len(marker)))
				if found || decided {
					t.Fatalf("decision=(%v,%v), want undecided", found, decided)
				}
				// Admission only: RFC 2047 headers and RTF escapes are not decoded
				// by the current extractors. Do not claim Stage 2 recall for them.
				paths, err := search.FindFilesWithFirstWord("subject", nil, root, nil)
				if err != nil || len(paths) != 1 || paths[0] != path {
					t.Fatalf("admission=%v err=%v", paths, err)
				}
			})
		}
	}
}

func TestIssue33EncodedStagesRecall(t *testing.T) {
	for name, data := range encodedBinaryFixtures() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, words := range [][]string{{"subject"}, {"subject", "body"}} {
				matches, readerCalls, err := search.Issue33StagesForTest(issue33Engine(root, words), func(n int) {
					if n != 1 {
						t.Fatalf("Stage 1 candidates=%d, want 1", n)
					}
				})
				if err != nil || matches != 1 {
					t.Fatalf("words=%v matches=%d err=%v", words, matches, err)
				}
				if name == "unicode.msg" && readerCalls != 1 {
					t.Fatalf("MSG ExtractReaderAt calls=%d, want 1", readerCalls)
				}
			}
		})
	}
}

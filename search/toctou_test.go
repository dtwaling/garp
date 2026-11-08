package search

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const toctouContent = "alpha beta searchable content"

type toctouReplacement func(t *testing.T, candidate string, content []byte)

func TestTOCTOURegularFiles(t *testing.T) {
	runTOCTOUStages(t, nil)
}

func TestTOCTOUSymlinkReplacement(t *testing.T) {
	// Skip before discovery starts workers on systems without link privileges.
	probeDir := t.TempDir()
	target := filepath.Join(probeDir, "target")
	if err := os.WriteFile(target, []byte(toctouContent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(probeDir, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runTOCTOUStages(t, func(t *testing.T, candidate string, content []byte) {
		outside := filepath.Join(t.TempDir(), "outside"+filepath.Ext(candidate))
		if err := os.WriteFile(outside, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(candidate); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, candidate); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTOCTOUDirectoryReplacement(t *testing.T) {
	runTOCTOUStages(t, func(t *testing.T, candidate string, _ []byte) {
		if err := os.Remove(candidate); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(candidate, 0700); err != nil {
			t.Fatal(err)
		}
	})
}

// Exercise actual stage boundaries, not a concurrent rename lottery. Stage 1's
// callback runs after walk eligibility checks and before enqueue/open. Stages
// 2 and 3 consume the candidates produced by the preceding, completed stage.
func runTOCTOUStages(t *testing.T, replace toctouReplacement) {
	t.Helper()
	for _, format := range []string{"txt", "eml", "docx"} {
		for stage := 1; stage <= 3; stage++ {
			// Binary discovery is deliberately conservative on an open error; it may
			// retain a candidate, but Stage 2 must reject it before extraction.
			if stage == 1 && format != "txt" {
				continue
			}
			t.Run(fmt.Sprintf("%s/stage%d", format, stage), func(t *testing.T) {
				root := t.TempDir()
				candidate := filepath.Join(root, "candidate."+format)
				content := []byte(toctouContent)
				if format == "docx" {
					var buf bytes.Buffer
					zw := zip.NewWriter(&buf)
					w, err := zw.Create("word/document.xml")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write(content); err != nil {
						t.Fatal(err)
					}
					if err := zw.Close(); err != nil {
						t.Fatal(err)
					}
					content = buf.Bytes()
				}
				if err := os.WriteFile(candidate, content, 0600); err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"alpha", "beta"}, nil, nil, false, 1, 5000)
				engine.StartDir, engine.Silent, engine.NoIgnore = root, true, true
				engine.FilterWorkers = 1
				legacy := &rankingCountingExtractor{text: toctouContent}
				ra := &rankingReaderAtExtractor{}
				ra.text = toctouContent
				engine.Registry.extractors["eml"] = legacy
				engine.Registry.extractors["docx"] = ra
				calls := func() int64 { return legacy.calls.Load() + ra.calls.Load() + ra.readerCalls.Load() }
				swapped := false
				swap := func() {
					t.Helper()
					info, err := os.Lstat(candidate)
					if err != nil || !info.Mode().IsRegular() {
						t.Fatalf("candidate before replacement: %v, %v", info, err)
					}
					if replace != nil {
						replace(t, candidate, content)
					}
					swapped = true
				}
				if stage == 1 {
					engine.OnProgress = func(s string, _, _ int, path string) {
						if s == "discovery" && path == candidate {
							swap()
						}
					}
				}
				paths, total, err := engine.DiscoverCandidates(1)
				if err != nil {
					t.Fatal(err)
				}
				if stage == 1 && replace != nil {
					if !swapped || len(paths) != 0 || total != 0 {
						t.Fatalf("Stage 1: swapped=%v paths=%v total=%d", swapped, paths, total)
					}
					return
				}
				if len(paths) != 1 || paths[0] != candidate || total != 1 {
					t.Fatalf("discovery = %v, %d", paths, total)
				}
				engine.OnProgress = nil
				if stage == 2 {
					swap()
				}
				matches, err := engine.FilterCandidates(paths, total, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if stage == 2 && replace != nil {
					if len(matches) != 0 || calls() != 0 {
						t.Fatalf("Stage 2: matches=%v extractor calls=%d", matches, calls())
					}
					return
				}
				if len(matches) != 1 {
					t.Fatalf("filter matches = %v", matches)
				}
				before := calls()
				if stage == 3 {
					swap()
				}
				outcome, err := engine.extractAndBuildResultsDetailed(compileQuery(engine.SearchWords, PartialModeOff), matches)
				if err != nil {
					t.Fatal(err)
				}
				if stage == 3 && replace != nil {
					if len(outcome.Results) != 0 || outcome.Matches != 0 || outcome.Returned != 0 || outcome.Truncated || calls() != before {
						t.Fatalf("Stage 3: outcome=%+v extractor calls=%d (before %d)", outcome, calls(), before)
					}
					return
				}
				if !swapped || len(outcome.Results) != 1 || outcome.Matches != 1 || outcome.Returned != 1 {
					t.Fatalf("regular outcome = %+v, boundary reached=%v", outcome, swapped)
				}
				result := outcome.Results[0]
				if result.FilePath != candidate || result.FileSize != int64(len(content)) || !strings.Contains(result.CleanContent, toctouContent) || len(result.RawExcerpts) == 0 {
					t.Fatalf("regular result = %+v", result)
				}
				if format != "txt" && calls() != 2 {
					t.Fatalf("extractor calls = %d, want 2", calls())
				}
			})
		}
	}
}

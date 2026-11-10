package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapForBinaryPrefilter(t *testing.T) {
	for _, ext := range []string{".eml", ".msg", ".mbox", ".rtf", ".docx", ".odt", ".doc"} {
		want := int64(2 * 1024 * 1024)
		if ext == ".eml" || ext == ".msg" || ext == ".mbox" {
			want = 256 * 1024
		}
		for _, spelling := range []string{ext, strings.ToUpper(ext)} {
			if got := capForBinaryPrefilter(spelling); got != want {
				t.Errorf("cap(%q) = %d, want %d", spelling, got, want)
			}
		}
	}
}

func TestStage2BinaryPrefilterProvenance(t *testing.T) {
	for _, ext := range []string{".eml", ".mbox", ".rtf"} {
		for _, multi := range []bool{false, true} {
			t.Run(ext+"/multi="+map[bool]string{false: "false", true: "true"}[multi], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "sample"+ext)
				if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
					t.Fatal(err)
				}
				terms := []string{"alpha"}
				if multi {
					terms = append(terms, "beta")
				}
				engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
				engine.StartDir, engine.Silent, engine.Strict = dir, true, true
				extractor := &rankingCountingExtractor{text: "alpha beta"}
				engine.Registry.extractors[strings.TrimPrefix(ext, ".")] = extractor
				query := compileQuery(terms, PartialModeOff)
				roots, err := engine.effectiveStartRoots()
				if err != nil {
					t.Fatal(err)
				}
				candidates, total, err := engine.discoverCandidates(query, 1, roots)
				if err != nil || len(candidates) != 1 {
					t.Fatalf("discovery = %v, %v", candidates, err)
				}
				if !candidates[0].primaryPrefiltered {
					t.Fatal("discovery provenance missing")
				}
				// Turn a repeated raw scan into a decisive miss. The controlled extractor
				// still returns the matching text, so reaching extraction proves bypass.
				if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
					t.Fatal(err)
				}
				matches, err := engine.filterCandidates(query, candidates, total, time.Now())
				want := 1
				if multi {
					want = 0
				}
				if err != nil || len(matches) != want || extractor.calls.Load() != int64(want) {
					t.Fatalf("discovered: matches=%d calls=%d err=%v; want %d", len(matches), extractor.calls.Load(), err, want)
				}
				// Public path-only calls carry no query provenance, even on a reused engine.
				before := extractor.calls.Load()
				matches, err = engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil || len(matches) != 0 || extractor.calls.Load() != before {
					t.Fatalf("direct: matches=%d calls=%d err=%v", len(matches), extractor.calls.Load(), err)
				}
			})
		}
	}
}

func TestStage2MSGSkipsPrefilter(t *testing.T) {
	for _, terms := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(terms, "-"), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sample.MSG")
			if err := os.WriteFile(path, []byte("opaque container"), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
			engine.StartDir, engine.Silent = dir, true
			extractor := &rankingReaderAtExtractor{}
			extractor.text = "alpha beta"
			engine.Registry.extractors["msg"] = extractor
			matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
			if err != nil || len(matches) != 1 {
				t.Fatalf("matches=%d err=%v", len(matches), err)
			}
			if extractor.readerCalls.Load() != 1 || extractor.calls.Load() != 0 || engine.msgPrefilterCount != 0 {
				t.Fatalf("ReaderAt=%d legacy=%d prefilters=%d", extractor.readerCalls.Load(), extractor.calls.Load(), engine.msgPrefilterCount)
			}
		})
	}
}

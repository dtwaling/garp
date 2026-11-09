package search

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEngineCleaningConsistency(t *testing.T) {
	const raw = "header\n> alpha beta gamma hidden\nalpha beta visible\n"
	for _, kind := range []string{"txt", "go", "pdf", "readerAt"} {
		for _, terms := range [][]string{{"alpha", "beta", "gamma"}, {"gamma"}} {
			for _, strict := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/strict=%v", kind, strings.Join(terms, "-"), strict), func(t *testing.T) {
					ext := kind
					if kind == "readerAt" {
						ext = "pdf"
					}
					path := filepath.Join(t.TempDir(), "fixture."+ext)
					if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
					engine := NewSearchEngine(terms, nil, nil, false, 1, 1000)
					engine.Silent = true
					engine.Strict = strict
					if kind == "pdf" {
						engine.Registry.extractors["pdf"] = rankingStaticExtractor{text: raw}
					}
					if kind == "readerAt" {
						extractor := &rankingReaderAtExtractor{}
						extractor.text = raw
						engine.Registry.extractors["pdf"] = extractor
					}
					matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					wantTerms := terms
					if kind != "go" {
						if strict || len(terms) == 1 {
							wantTerms = nil
						} else {
							wantTerms = []string{"alpha", "beta"}
						}
					}
					if len(wantTerms) == 0 {
						if len(matches) != 0 {
							t.Fatalf("hidden-only match survived Stage 2: %+v", matches)
						}
						return
					}
					if len(matches) != 1 {
						t.Fatalf("matches = %+v, want one", matches)
					}
					score := 0
					for _, term := range wantTerms {
						score += len(term)
					}
					if matches[0].score != score || matches[0].termCount != len(wantTerms) || !slices.Equal(matches[0].matchedTerms, wantTerms) {
						t.Fatalf("Stage 2 metadata includes stripped terms: %+v; want %v", matches[0], wantTerms)
					}
					results, err := engine.ExtractAndBuildResults(matches)
					if err != nil || len(results) != 1 {
						t.Fatalf("results=%+v err=%v", results, err)
					}
					result := results[0]
					if result.Score != score || result.TermCount != len(wantTerms) || !slices.Equal(result.MatchedTerms, wantTerms) || result.ChunkScores[0] != score || result.ChunkTermCounts[0] != len(wantTerms) {
						t.Fatalf("Stage 2/3 metadata disagree: %+v", result)
					}
					if kind != "go" && (strings.Contains(result.CleanContent, "gamma") || strings.Contains(strings.Join(result.RawExcerpts, " "), "hidden")) {
						t.Fatalf("hidden content survived: %+v", result)
					}
					if kind == "txt" && !slices.Equal(result.StartLines, []int{3}) {
						t.Fatalf("lines=%v, want [3]", result.StartLines)
					}
				})
			}
		}
	}
}

func TestFileAllWordsCleaningConsistency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte("header\n> alpha beta gamma hidden\nalpha beta visible\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, terms := range [][]string{{"alpha", "beta"}, {"alpha", "gamma"}, {"gamma"}} {
		got, err := CheckFileContainsAllWords(path, terms, 1000, true)
		want := slices.Equal(terms, []string{"alpha", "beta"})
		if err != nil || got != want {
			t.Fatalf("terms=%v: got %v, %v; want %v", terms, got, err, want)
		}
	}
}

func TestCleaningStageParity(t *testing.T) {
	for _, raw := range []string{"header\n> alpha beta hidden\nvisible\n", "\n-----\nvisible", "<style>hidden</style>\n<script>hidden</script>\n<b>A1,2B</b> &amp;\tend", "\u00a0\x00é\u0085\tX\xff\u2003", "", " \n\t"} {
		for _, code := range []bool{false, true} {
			got := CleanContent(raw)
			if code {
				got = CleanContentCode(raw)
			}
			if want := cleanStage3Content(raw, code).text; got != want {
				t.Errorf("raw=%q code=%v: Stage 2=%q Stage 3=%q", raw, code, got, want)
			}
		}
	}
}

func TestMappedWhitespaceAllocationGrowth(t *testing.T) {
	allocs := func(n int) float64 {
		raw := strings.Repeat("word\x00\t \u0085é\n", n)
		return testing.AllocsPerRun(10, func() {
			got := cleanStage3Content(raw, true)
			if len(got.text) == 0 || len(got.offsets) != len(got.text) {
				panic("invalid mapped result")
			}
		})
	}
	small, large := allocs(10), allocs(1000)
	if large-small > 2 {
		t.Fatalf("whitespace allocations scale per run: small=%.0f large=%.0f", small, large)
	}
}

func BenchmarkMappedWhitespace(b *testing.B) {
	raw := strings.Repeat("word\x00\t \u0085é\n", 100000)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		_ = cleanStage3Content(raw, true)
	}
}

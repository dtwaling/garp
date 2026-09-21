package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type skipAheadJSONExcerpt struct {
	Text string `json:"text"`
}

func TestSkipAheadEngineRegressionCorpus(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	dense := skipAheadDenseCorpus("needle")
	separated := skipAheadSeparatedCorpus("needle")
	writeSkipAheadIntegrationFile(t, root, "dense.txt", dense)
	writeSkipAheadIntegrationFile(t, root, "separated.txt", separated)

	results := executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, false, 3)
	denseResult := skipAheadResultByFile(t, results, "dense.txt")
	separatedResult := skipAheadResultByFile(t, results, "separated.txt")

	if len(denseResult.RawExcerpts) != 1 {
		t.Fatalf("dense raw excerpts = %d, want 1 bounded-overlap excerpt", len(denseResult.RawExcerpts))
	}
	if len(separatedResult.RawExcerpts) != 3 {
		t.Fatalf("separated raw excerpts = %d, want 3 distinct excerpts", len(separatedResult.RawExcerpts))
	}
	requireBoundedEngineExcerpts(t, dense, denseResult.RawExcerpts, "needle", false)
	requireBoundedEngineExcerpts(t, separated, separatedResult.RawExcerpts, "needle", false)

	encoded, err := json.Marshal(skipAheadJSONExcerpts(separatedResult.RawExcerpts))
	if err != nil {
		t.Fatalf("marshal JSON excerpts: %v", err)
	}
	var decoded []skipAheadJSONExcerpt
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal JSON excerpts: %v", err)
	}
	jsonExcerpts := make([]string, len(decoded))
	for i, excerpt := range decoded {
		jsonExcerpts[i] = excerpt.Text
	}
	requireBoundedEngineExcerpts(t, separated, jsonExcerpts, "needle", false)
}

func TestSkipAheadEngineSingleChunkGolden(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	content := skipAheadSeparatedCorpus("needle")
	writeSkipAheadIntegrationFile(t, root, "single.txt", content)

	result := skipAheadResultByFile(t, executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, false, 1), "single.txt")
	if len(result.RawExcerpts) != 1 {
		t.Fatalf("single-chunk raw excerpts = %d, want 1", len(result.RawExcerpts))
	}
	if len(result.RawExcerpts[0]) > 400 {
		t.Fatalf("single-chunk excerpt length = %d, exceeds budget 400", len(result.RawExcerpts[0]))
	}
	if !strings.Contains(result.RawExcerpts[0], "needle first marker") {
		t.Fatalf("single-chunk excerpt does not contain first cluster: %q", result.RawExcerpts[0])
	}

	wantGolden, err := os.ReadFile(filepath.Join("testdata", "skipahead_single_chunk.golden"))
	if err != nil {
		t.Fatalf("read single-chunk golden: %v", err)
	}
	if result.RawExcerpts[0] != string(wantGolden) {
		t.Fatalf("single-chunk excerpt changed from the new unified framing golden:\n got: %q\nwant: %q", result.RawExcerpts[0], wantGolden)
	}
}

func TestSkipAheadEngineCodeCorpus(t *testing.T) {
	priorBudget := ExcerptCharBudget
	ExcerptCharBudget = func() int { return 400 }
	t.Cleanup(func() { ExcerptCharBudget = priorBudget })

	root := t.TempDir()
	content := strings.Join([]string{
		"package fixture", "", "func first() {", "\tneedle := \"first marker\"", "\t_ = needle", "}",
		skipAheadCodePadding("between-first-second", 24),
		"func second() {", "\tneedle := \"second marker\"", "\t_ = needle", "}",
		skipAheadCodePadding("between-second-third", 24),
		"func third() {", "\tneedle := \"third marker\"", "\t_ = needle", "}",
	}, "\n")
	writeSkipAheadIntegrationFile(t, root, "fixture.go", content)

	result := skipAheadResultByFile(t, executeSkipAheadIntegrationSearch(t, root, []string{"needle"}, true, 3), "fixture.go")
	if len(result.RawExcerpts) != 3 {
		t.Fatalf("code raw excerpts = %d, want 3 distinct excerpts", len(result.RawExcerpts))
	}
	requireBoundedEngineExcerpts(t, content, result.RawExcerpts, "needle", true)
}

func executeSkipAheadIntegrationSearch(t *testing.T, root string, words []string, includeCode bool, maxExcerpts int) []SearchResult {
	t.Helper()
	fileGlob := "*.txt"
	if includeCode {
		fileGlob = "*.go"
	}
	engine := NewSearchEngineWithWorkers(words, nil, []string{"-g", fileGlob}, includeCode, 1, 1000, 1)
	engine.StartDir = root
	engine.Distance = 100
	engine.MaxExcerpts = maxExcerpts
	engine.Silent = true

	results, err := engine.Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	return results
}

func skipAheadResultByFile(t *testing.T, results []SearchResult, name string) SearchResult {
	t.Helper()
	for _, result := range results {
		if filepath.Base(result.FilePath) == name {
			return result
		}
	}
	t.Fatalf("result for %q not found among %v", name, skipAheadResultNames(results))
	return SearchResult{}
}

func skipAheadResultNames(results []SearchResult) []string {
	names := make([]string, len(results))
	for i, result := range results {
		names[i] = filepath.Base(result.FilePath)
	}
	return names
}

func skipAheadJSONExcerpts(excerpts []string) []skipAheadJSONExcerpt {
	out := make([]skipAheadJSONExcerpt, len(excerpts))
	for i, excerpt := range excerpts {
		out[i] = skipAheadJSONExcerpt{Text: excerpt}
	}
	return out
}

func requireBoundedEngineExcerpts(t *testing.T, content string, excerpts []string, term string, isCode bool) {
	t.Helper()
	cleaned := CleanContent(content)
	if isCode {
		cleaned = CleanContentCode(content)
	}
	spans := extractExcerptSpans(cleaned, []string{term}, len(excerpts), isCode)
	if len(spans) != len(excerpts) {
		t.Fatalf("extractor span count = %d, engine excerpt count = %d", len(spans), len(excerpts))
	}
	for i, span := range spans {
		if span.text != excerpts[i] {
			t.Fatalf("engine excerpt %d = %q, extractor span text = %q", i, excerpts[i], span.text)
		}
	}
	requireBoundedNovelSpans(t, cleaned, spans, term)
	for i := range excerpts {
		for j := 0; j < i; j++ {
			common := skipAheadLongestCommonRun(excerpts[i], excerpts[j])
			limit := len(excerpts[i])
			if len(excerpts[j]) < limit {
				limit = len(excerpts[j])
			}
			if common*5 >= limit {
				t.Fatalf("excerpts %d and %d share common run %d, want less than 20%% of shorter length %d", i, j, common, limit)
			}
		}
	}
}

func skipAheadLongestCommonRun(a, b string) int {
	previous := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				current[j] = previous[j-1] + 1
				if current[j] > best {
					best = current[j]
				}
			}
		}
		previous = current
	}
	return best
}

func skipAheadDenseCorpus(term string) string {
	return strings.Join([]string{
		"Dense regression corpus begins with context.",
		skipAheadPadding("dense-before", 35), term + " first marker.",
		skipAheadPadding("dense-first-second", 6), term + " second marker.",
		skipAheadPadding("dense-second-third", 6), term + " third marker.",
		skipAheadPadding("dense-after", 35) + " Dense regression corpus ends.",
	}, " ")
}

func skipAheadSeparatedCorpus(term string) string {
	return strings.Join([]string{
		"First cluster begins. " + term + " first marker. First cluster ends.",
		skipAheadPadding("between-first-second", 90),
		"Second cluster begins. " + term + " second marker. Second cluster ends.",
		skipAheadPadding("between-second-third", 90),
		"Third cluster begins. " + term + " third marker. Third cluster ends.",
	}, " ")
}

func skipAheadPadding(prefix string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return strings.Join(parts, " ")
}

func skipAheadCodePadding(prefix string, count int) string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf("// %s-%03d", prefix, i)
	}
	return strings.Join(lines, "\n")
}

func writeSkipAheadIntegrationFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

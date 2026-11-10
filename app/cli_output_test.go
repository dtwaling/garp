package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"garp/search"
)

func TestCLIOutputFormats(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"full.md":    "alpha beta gamma appear together in this ranked fixture.",
		"partial.md": "alpha beta appear together in this ranked fixture.",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %q: %v", name, err)
		}
	}

	args := parseArguments([]string{
		"alpha", "beta", "gamma",
		"--startdir", root,
		"--only", "md",
		"--distance", "200",
	})

	t.Run("JSON includes ranked match metadata", func(t *testing.T) {
		out := captureCLIStdout(t, func() int { return runJSON(args) })

		var result jsonOutput
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("decode JSON output: %v", err)
		}
		if result.Matches != 2 || len(result.Results) != 2 {
			t.Fatalf("JSON matches/results = %d/%d, want 2/2", result.Matches, len(result.Results))
		}
		if got := result.Results[0]; got.Score != 14 || got.TermCount != 3 || got.SpanLength <= 0 {
			t.Errorf("first JSON ranked metadata = score %d, term_count %d, span_length %d; want 14, 3, positive", got.Score, got.TermCount, got.SpanLength)
		}
		if got := result.Results[0].MatchedTerms; !slicesEqual(got, []string{"alpha", "beta", "gamma"}) {
			t.Errorf("first JSON matched_terms = %v, want [alpha beta gamma]", got)
		}
		if got := result.Results[1]; got.Score != 9 || got.TermCount != 2 || got.SpanLength <= 0 {
			t.Errorf("second JSON ranked metadata = score %d, term_count %d, span_length %d; want 9, 2, positive", got.Score, got.TermCount, got.SpanLength)
		}
		if got := result.Results[1].MatchedTerms; !slicesEqual(got, []string{"alpha", "beta"}) {
			t.Errorf("second JSON matched_terms = %v, want [alpha beta]", got)
		}
	})

	t.Run("plain header includes score and term count", func(t *testing.T) {
		out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
		for _, header := range []string{
			"MATCH 1/2 [Score: 14 (3/3 terms)]\n",
			"MATCH 2/2 [Score: 9 (2/3 terms)]\n",
		} {
			if !strings.Contains(out, header) {
				t.Errorf("plain output missing ranked match header %q:\n%s", header, out)
			}
		}
	})

	t.Run("pathonly emits distinct ranked paths one per line", func(t *testing.T) {
		pathArgs := parseArguments([]string{
			"alpha", "beta", "gamma",
			"--startdir", root,
			"--only", "md",
			"--distance", "200",
			"--pathonly",
		})
		if !pathArgs.PathOnly {
			t.Fatal("PathOnly = false with --pathonly, want true")
		}
		out := strings.TrimRight(string(captureCLIStdout(t, func() int { return runPathOnly(pathArgs) })), "\n")
		got := strings.Split(out, "\n")
		want := []string{
			escapeOutputLine(filepath.Join(root, "full.md")),
			escapeOutputLine(filepath.Join(root, "partial.md")),
		}
		if !slicesEqual(got, want) {
			t.Errorf("pathonly output = %v, want %v", got, want)
		}
	})

	t.Run("pathonly emits empty stdout on zero matches", func(t *testing.T) {
		missArgs := parseArguments([]string{
			"nonexistent_term_xyz",
			"--startdir", root,
			"--only", "md",
			"--pathonly",
		})
		out := string(captureCLIStdout(t, func() int { return runPathOnly(missArgs) }))
		if out != "" {
			t.Errorf("pathonly output on zero matches = %q, want empty string", out)
		}
	})
}

func TestJSONTraversalOptionsOmissionAndPresence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.md"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	tests := []struct {
		name      string
		extraArgs []string
		want      map[string]string
		omitted   []string
	}{
		{
			name:    "omitted by default",
			omitted: []string{"no_ignore", "max_filesize"},
		},
		{
			name:      "no_ignore only",
			extraArgs: []string{"--no-ignore"},
			want: map[string]string{
				"no_ignore": "true",
			},
			omitted: []string{"max_filesize"},
		},
		{
			name:      "max_filesize only including explicit 5M",
			extraArgs: []string{"--max-filesize", "5M"},
			want: map[string]string{
				"max_filesize": "5242880",
			},
			omitted: []string{"no_ignore"},
		},
		{
			name:      "present when explicitly set",
			extraArgs: []string{"--no-ignore", "--max-filesize", "1MiB"},
			want: map[string]string{
				"no_ignore":    "true",
				"max_filesize": "1048576",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := []string{"alpha", "--startdir", root, "--only", "md", "--json"}
			argv = append(argv, tt.extraArgs...)
			args := parseArguments(argv)
			out := captureCLIStdout(t, func() int { return runJSON(args) })
			var envelope struct {
				Query map[string]json.RawMessage `json:"query"`
			}
			if err := json.Unmarshal(out, &envelope); err != nil {
				t.Fatalf("decode JSON output: %v", err)
			}
			for _, key := range tt.omitted {
				if _, exists := envelope.Query[key]; exists {
					t.Errorf("query unexpectedly contains %q: %s", key, out)
				}
			}
			for key, want := range tt.want {
				if got := string(envelope.Query[key]); got != want {
					t.Errorf("query %s = %s, want %s", key, got, want)
				}
			}
		})
	}
}

func TestJSONOnlyTypes(t *testing.T) {
	one := buildJSONOutput(parseArguments([]string{"Needle", "--type", "PDF"}), search.ExecutionResult{})
	if one.Query.OnlyType != "pdf" || one.Query.OnlyTypes != nil || one.Query.Terms[0] != "needle" {
		t.Fatalf("single-type query = %#v", one.Query)
	}

	many := buildJSONOutput(parseArguments([]string{"Needle", "-s", "-t", "PDF,md,pdf"}), search.ExecutionResult{})
	if many.Query.OnlyType != "pdf" || !slicesEqual(many.Query.OnlyTypes, []string{"pdf", "md"}) {
		t.Fatalf("multi-type query = %#v", many.Query)
	}
	if !many.Query.CaseSensitive || many.Query.Terms[0] != "Needle" {
		t.Fatalf("case query = %#v", many.Query)
	}

	unicodeDefault := buildJSONOutput(
		parseArguments([]string{"\u0130", "API", "--type", "txt"}),
		search.ExecutionResult{},
	)
	if !slicesEqual(unicodeDefault.Query.Terms, []string{"\u0130", "api"}) {
		t.Fatalf("Unicode default query terms = %q", unicodeDefault.Query.Terms)
	}
}

func TestNewSilentSearchEnginePropagation(t *testing.T) {
	root := t.TempDir()
	args := parseArguments([]string{
		"Needle", "--startdir", root, "-t", "md,pdf",
		"--glob", "docs/**", "--iglob", "*.MD",
		"-s", "--smart-forms", "--pathonly",
	})
	roots, err := search.PrepareStartRootsWithOptions(args.StartDirs, search.StartRootOptions{
		PathScope: args.PathScope, UserGlobs: args.UserGlobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	args.StartRoots = roots
	engine := newSilentSearchEngine(args)
	if !reflect.DeepEqual(engine.StartRoots, roots) ||
		!reflect.DeepEqual(engine.UserGlobs, args.UserGlobs) ||
		!engine.CaseSensitive || !engine.SmartForms || !engine.PathOnly ||
		!slicesEqual(engine.FileTypes, buildFileTypes(false, "md", []string{"md", "pdf"})) {
		t.Fatalf("silent engine did not preserve arguments: %#v", engine)
	}
}

func TestJSONStartDirsCompatibilityAndOmission(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	args := parseArguments([]string{
		"needle", "--startdir", first + "," + second,
	})
	roots, err := search.PrepareStartRoots(args.StartDirs, false)
	if err != nil {
		t.Fatal(err)
	}
	args.StartRoots = roots
	out := buildJSONOutput(args, search.ExecutionResult{})
	if out.Query.StartDir != first || !slicesEqual(out.Query.StartDirs, []string{first, second}) {
		t.Fatalf("multi-root query = %#v", out.Query)
	}

	single := parseArguments([]string{"needle", "--startdir", first})
	single.StartRoots, err = search.PrepareStartRoots(single.StartDirs, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := buildJSONOutput(single, search.ExecutionResult{}).Query; got.StartDir != first || got.StartDirs != nil {
		t.Fatalf("single-root query = %#v", got)
	}

	omitted := buildJSONOutput(parseArguments([]string{"needle"}), search.ExecutionResult{})
	raw, err := json.Marshal(omitted.Query)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "start_dir") {
		t.Fatalf("default query leaked start fields: %s", raw)
	}
}

func TestJSONMaxResultsMetadata(t *testing.T) {
	root := buildOutputLimitFixture(t, 52)
	tests := []struct {
		name          string
		maxArgs       []string
		wantReturned  int
		wantTruncated bool
	}{
		{name: "default 50", wantReturned: 50, wantTruncated: true},
		{name: "explicit zero is unlimited", maxArgs: []string{"--max-results", "0"}, wantReturned: 52},
		{name: "explicit N", maxArgs: []string{"--max-results", "7"}, wantReturned: 7, wantTruncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := []string{"alpha", "--startdir", root, "--only", "txt", "--json"}
			argv = append(argv, tt.maxArgs...)
			out := captureCLIStdout(t, func() int { return runJSON(parseArguments(argv)) })

			var got jsonOutput
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode JSON output: %v", err)
			}
			if got.Matches != 52 || got.Returned != tt.wantReturned || got.Truncated != tt.wantTruncated {
				t.Fatalf("metadata = matches %d, returned %d, truncated %t; want 52, %d, %t",
					got.Matches, got.Returned, got.Truncated, tt.wantReturned, tt.wantTruncated)
			}
			if len(got.Results) != tt.wantReturned {
				t.Fatalf("results = %d, want %d", len(got.Results), tt.wantReturned)
			}
			assertJSONEnvelopeKeys(t, out, tt.wantReturned, tt.wantTruncated)
		})
	}
}

func TestJSONZeroResultsIncludesMetadataKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "miss.txt"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := parseArguments([]string{"alpha", "--startdir", root, "--only", "txt", "--json"})
	out := captureCLIStdout(t, func() int { return runJSON(args) })
	assertJSONEnvelopeKeys(t, out, 0, false)
}

func assertJSONEnvelopeKeys(t *testing.T, out []byte, returned int, truncated bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("decode raw JSON output: %v", err)
	}
	want := map[string]string{
		"query":     "",
		"matches":   "",
		"returned":  fmt.Sprint(returned),
		"truncated": fmt.Sprint(truncated),
		"results":   "",
	}
	for key, value := range want {
		got, ok := raw[key]
		if !ok {
			t.Errorf("JSON output missing top-level %q", key)
		} else if value != "" && string(got) != value {
			t.Errorf("JSON %s = %s, want %s", key, got, value)
		}
	}
}

func TestPlainMaxResultsMetadata(t *testing.T) {
	root := buildOutputLimitFixture(t, 52)

	t.Run("default 50 uses returned denominator and footer", func(t *testing.T) {
		args := parseArguments([]string{"alpha", "--startdir", root, "--only", "txt", "--plain"})
		out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
		if got := strings.Count(out, "MATCH "); got != 50 {
			t.Fatalf("MATCH count = %d, want 50", got)
		}
		if !strings.Contains(out, "MATCH 50/50 ") {
			t.Errorf("plain output missing returned denominator:\n%s", out)
		}
		const footer = "# truncated: showing 50 of 52 matching files (use --max-results 0 for all)\n"
		if !strings.HasSuffix(out, footer) {
			t.Errorf("plain output does not end with exact footer %q", footer)
		}
	})

	t.Run("explicit zero is unlimited without footer", func(t *testing.T) {
		args := parseArguments([]string{"alpha", "--startdir", root, "--only", "txt", "--plain", "--max-results", "0"})
		out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
		if !strings.Contains(out, "MATCH 52/52 ") {
			t.Errorf("plain output missing unlimited final header")
		}
		if strings.Contains(out, "# truncated:") {
			t.Errorf("untruncated plain output contains truncation footer")
		}
	})
}

func TestOutputMetadataUsesAdjustedStage3Matches(t *testing.T) {
	args := parseArguments([]string{"alpha"})
	outcome := search.ExecutionResult{
		Results: []search.SearchResult{
			{FilePath: "second.txt", Score: 1, TermCount: 1},
			{FilePath: "third.txt", Score: 1, TermCount: 1},
		},
		Matches:   3,
		Returned:  2,
		Truncated: true,
	}

	jsonResult := buildJSONOutput(args, outcome)
	if jsonResult.Matches != 3 || jsonResult.Returned != 2 || !jsonResult.Truncated {
		t.Fatalf("JSON metadata = %+v, want adjusted 3 matches, 2 returned, truncated", jsonResult)
	}

	var plain bytes.Buffer
	writePlainOutput(&plain, args, outcome)
	if !strings.Contains(plain.String(), "MATCH 2/2 ") {
		t.Errorf("plain output missing returned denominator:\n%s", plain.String())
	}
	const footer = "# truncated: showing 2 of 3 matching files (use --max-results 0 for all)\n"
	if !strings.HasSuffix(plain.String(), footer) {
		t.Errorf("plain output does not end with adjusted footer %q:\n%s", footer, plain.String())
	}
}

func TestOutputModeOrderingParity(t *testing.T) {
	root, want := buildOutputOrderingParityFixture(t)

	pathArgs := outputOrderingParityArgs(root, "--pathonly")
	if got := resolveMaxResults(pathArgs); got != 0 {
		t.Fatalf("default path-only MaxResults = %d, want unlimited", got)
	}
	pathOnly := outputPathsFromPathOnly(t, pathArgs)

	plainArgs := outputOrderingParityArgs(root, "--plain", "--max-results", "0")
	plainOutput := string(captureCLIStdout(t, func() int { return runPlain(plainArgs) }))
	plain := outputPathsFromPlainOutput(t, plainOutput)
	if got := excerptCountForPlainFile(plainOutput, want[1]); got != 1 {
		t.Fatalf("default plain excerpts for multi-chunk file = %d, want 1", got)
	}

	jsonArgs := outputOrderingParityArgs(root, "--json", "--max-results", "0", "--max-excerpts", "10")
	jsonOutput := outputJSONFromCLI(t, jsonArgs)
	jsonPaths := jsonResultPaths(jsonOutput.Results)
	if len(jsonOutput.Results) != len(want) {
		t.Fatalf("JSON result count = %d, want %d", len(jsonOutput.Results), len(want))
	}
	if got := len(jsonOutput.Results[1].Excerpts); got < 2 {
		t.Fatalf("JSON excerpts for multi-chunk file = %d, want at least 2", got)
	}

	tuiArgs := outputOrderingParityArgs(root, "--max-excerpts", "10")
	tuiModel := newTUIModel(tuiArgs)
	tuiEngine := tuiModel.newSearchEngine()
	if tuiEngine.MaxResults != 0 {
		t.Fatalf("default TUI MaxResults = %d, want unlimited", tuiEngine.MaxResults)
	}
	if tuiEngine.MaxExcerpts != 10 {
		t.Fatalf("TUI engine MaxExcerpts = %d, want 10", tuiEngine.MaxExcerpts)
	}
	tuiResults, err := tuiEngine.Execute()
	if err != nil {
		t.Fatalf("TUI engine Execute() error: %v", err)
	}
	if len(tuiResults) != len(want) {
		t.Fatalf("TUI result count = %d, want %d", len(tuiResults), len(want))
	}
	tuiPaths := searchResultPaths(tuiResults)
	if tuiResults[0].Score != tuiResults[1].Score || tuiResults[0].TermCount != tuiResults[1].TermCount {
		t.Errorf("path-relevance pair differs on higher tiers: %+v / %+v", tuiResults[0], tuiResults[1])
	}
	if tuiResults[0].PathMatchCount <= tuiResults[1].PathMatchCount {
		t.Errorf("fixture does not exercise path relevance: %d <= %d", tuiResults[0].PathMatchCount, tuiResults[1].PathMatchCount)
	}
	if tuiResults[1].Score != tuiResults[2].Score ||
		tuiResults[1].TermCount != tuiResults[2].TermCount ||
		tuiResults[1].PathMatchCount != tuiResults[2].PathMatchCount ||
		tuiResults[1].BoundaryQuality != tuiResults[2].BoundaryQuality ||
		tuiResults[1].SpanLength != tuiResults[2].SpanLength {
		t.Errorf("top-quality pair differs on higher tiers: %+v / %+v", tuiResults[1], tuiResults[2])
	}
	if tuiResults[1].TopQualityChunks <= tuiResults[2].TopQualityChunks {
		t.Errorf("fixture does not exercise top-quality count: %d <= %d", tuiResults[1].TopQualityChunks, tuiResults[2].TopQualityChunks)
	}
	if tuiResults[3].Score != tuiResults[4].Score ||
		tuiResults[3].TermCount != tuiResults[4].TermCount ||
		tuiResults[3].PathMatchCount != tuiResults[4].PathMatchCount ||
		tuiResults[3].BoundaryQuality != tuiResults[4].BoundaryQuality {
		t.Errorf("span pair differs on higher tiers: %+v / %+v", tuiResults[3], tuiResults[4])
	}
	if tuiResults[3].SpanLength >= tuiResults[4].SpanLength {
		t.Errorf("fixture does not exercise span: %d >= %d", tuiResults[3].SpanLength, tuiResults[4].SpanLength)
	}
	if tuiResults[4].Score != tuiResults[5].Score ||
		tuiResults[4].TermCount != tuiResults[5].TermCount ||
		tuiResults[4].PathMatchCount != tuiResults[5].PathMatchCount {
		t.Errorf("boundary-quality pair differs on higher tiers: %+v / %+v", tuiResults[4], tuiResults[5])
	}
	if tuiResults[4].BoundaryQuality <= tuiResults[5].BoundaryQuality {
		t.Errorf("fixture does not exercise boundary quality: %d <= %d", tuiResults[4].BoundaryQuality, tuiResults[5].BoundaryQuality)
	}
	if tuiResults[2].Score != tuiResults[3].Score ||
		tuiResults[2].TermCount != tuiResults[3].TermCount ||
		tuiResults[2].PathMatchCount != tuiResults[3].PathMatchCount ||
		tuiResults[2].BoundaryQuality != tuiResults[3].BoundaryQuality ||
		tuiResults[2].SpanLength != tuiResults[3].SpanLength ||
		tuiResults[2].TopQualityChunks != tuiResults[3].TopQualityChunks {
		t.Errorf("fixture path-tie pair differs before final path tier: %+v / %+v", tuiResults[2], tuiResults[3])
	}

	for mode, got := range map[string][]string{
		"path-only": pathOnly,
		"plain":     plain,
		"JSON":      jsonPaths,
		"TUI":       tuiPaths,
	} {
		if !slicesEqual(got, want) {
			t.Errorf("%s order = %v, want %v", mode, got, want)
		}
	}
}

func TestOutputModeTopNParity(t *testing.T) {
	root, fullOrder := buildOutputOrderingParityFixture(t)
	const cap = 3
	want := fullOrder[:cap]

	pathOnly := outputPathsFromPathOnly(t, outputOrderingParityArgs(
		root, "--pathonly", "--max-results", fmt.Sprint(cap),
	))
	plain := outputPathsFromPlain(t, outputOrderingParityArgs(
		root, "--plain", "--max-results", fmt.Sprint(cap),
	))
	jsonPaths := outputPathsFromJSON(t, outputOrderingParityArgs(
		root, "--json", "--max-results", fmt.Sprint(cap), "--max-excerpts", "10",
	))

	tuiArgs := outputOrderingParityArgs(root, "--max-results", fmt.Sprint(cap), "--max-excerpts", "10")
	tuiEngine := newTUIModel(tuiArgs).newSearchEngine()
	if tuiEngine.MaxExcerpts != 10 {
		t.Fatalf("capped TUI engine MaxExcerpts = %d, want 10", tuiEngine.MaxExcerpts)
	}
	tuiResults, err := tuiEngine.Execute()
	if err != nil {
		t.Fatalf("capped TUI engine Execute() error: %v", err)
	}
	if len(tuiResults) != cap {
		t.Fatalf("capped TUI result count = %d, want %d", len(tuiResults), cap)
	}

	for mode, got := range map[string][]string{
		"path-only": pathOnly,
		"plain":     plain,
		"JSON":      jsonPaths,
		"TUI":       searchResultPaths(tuiResults),
	} {
		if !slicesEqual(got, want) {
			t.Errorf("%s capped order = %v, want top-%d prefix %v", mode, got, cap, want)
		}
	}
}

func buildOutputOrderingParityFixture(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	fixtures := map[string]string{
		"neutral/zz-alpha.txt": "alpha beta",
		"neutral/zz-top.txt":   "alpha beta " + strings.Repeat("x", 1000) + " alpha beta",
		"neutral/aa-tie.txt":   "alpha beta",
		"neutral/ab-tie.txt":   "alpha beta",
		"neutral/01-long.txt":  "alpha " + strings.Repeat("x", 40) + " beta",
		"neutral/00-edge.txt":  "xalphay beta",
	}
	for name, content := range fixtures {
		writeCLISearchFixture(t, root, name, content)
	}

	relativeOrder := []string{
		"neutral/zz-alpha.txt",
		"neutral/zz-top.txt",
		"neutral/aa-tie.txt",
		"neutral/ab-tie.txt",
		"neutral/01-long.txt",
		"neutral/00-edge.txt",
	}
	absoluteOrder := make([]string, len(relativeOrder))
	for i, name := range relativeOrder {
		absoluteOrder[i] = filepath.Join(root, filepath.FromSlash(name))
	}
	return root, absoluteOrder
}

func outputOrderingParityArgs(root string, extra ...string) *Arguments {
	argv := []string{
		"alpha", "beta",
		"--startdir", root,
		"--only", "txt",
		"--partial=contains",
		"--distance", "500",
	}
	return parseArguments(append(argv, extra...))
}

func outputPathsFromPathOnly(t *testing.T, args *Arguments) []string {
	t.Helper()
	out := strings.TrimSpace(string(captureCLIStdout(t, func() int { return runPathOnly(args) })))
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = unescapeOutputLine(t, line)
	}
	return lines
}

func outputPathsFromPlain(t *testing.T, args *Arguments) []string {
	t.Helper()
	out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
	return outputPathsFromPlainOutput(t, out)
}

func outputPathsFromPlainOutput(t *testing.T, out string) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "FILE: ") {
			paths = append(paths, unescapeOutputLine(t, strings.TrimPrefix(line, "FILE: ")))
		}
	}
	return paths
}

func unescapeOutputLine(t *testing.T, s string) string {
	t.Helper()
	unquoted, err := strconv.Unquote(`"` + s + `"`)
	if err != nil {
		t.Fatalf("unescape output line %q: %v", s, err)
	}
	return unquoted
}

func excerptCountForPlainFile(out, path string) int {
	inFile := false
	count := 0
	wantHeader := "FILE: " + escapeOutputLine(path)
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == wantHeader:
			inFile = true
		case inFile && strings.HasPrefix(line, "EXCERPT "):
			count++
		case inFile && line == "---":
			return count
		}
	}
	return count
}

func outputPathsFromJSON(t *testing.T, args *Arguments) []string {
	t.Helper()
	return jsonResultPaths(outputJSONFromCLI(t, args).Results)
}

func outputJSONFromCLI(t *testing.T, args *Arguments) jsonOutput {
	t.Helper()
	out := captureCLIStdout(t, func() int { return runJSON(args) })
	var decoded jsonOutput
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	return decoded
}

func jsonResultPaths(results []jsonResult) []string {
	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.File
	}
	return paths
}

func searchResultPaths(results []search.SearchResult) []string {
	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.FilePath
	}
	return paths
}

func buildOutputLimitFixture(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < count; i++ {
		writeCLISearchFixture(t, root, fmt.Sprintf("match-%03d.txt", i), "alpha")
	}
	return root
}

func captureCLIStdout(t *testing.T, run func() int) []byte {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	type readResult struct {
		out []byte
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		out, readErr := io.ReadAll(reader)
		readDone <- readResult{out: out, err: readErr}
	}()

	originalStdout := os.Stdout
	os.Stdout = writer
	code := run()
	_ = writer.Close()
	os.Stdout = originalStdout
	result := <-readDone
	_ = reader.Close()

	if code != 0 {
		t.Fatalf("CLI exit code = %d, want 0", code)
	}
	if result.err != nil {
		t.Fatalf("read stdout: %v", result.err)
	}
	return result.out
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestEscapeOutputLine(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"ordinary café.txt", "ordinary café.txt"},
		{"\r\n\t\x00\x1b\x7f\u0085\u2028\u2029", `\r\n\t\x00\x1b\x7f\u0085\u2028\u2029`},
		{`literal\n "quoted"`, `literal\\n \"quoted\"`},
		{string([]byte{0xff, 0x9b}), `\xff\x9b`},
	} {
		if got := escapeOutputLine(tt.input); got != tt.want {
			t.Errorf("escapeOutputLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
	for r := rune(0); r <= 0x9f; r++ {
		if unicode.IsControl(r) && strings.ContainsRune(escapeOutputLine(string(r)), r) {
			t.Errorf("control U+%04X survived escaping", r)
		}
	}
}

func TestPlainOutputCannotForgeRecords(t *testing.T) {
	const payload = "evil\r\nMATCH 9\nFILE: forged\rSIZE: 0\nEXCERPT 9: forged\n---\x1b[2J\x00\u0085\u2028\u2029"
	const escaped = `evil\r\nMATCH 9\nFILE: forged\rSIZE: 0\nEXCERPT 9: forged\n---\x1b[2J\x00\u0085\u2028\u2029`
	args := parseArguments([]string{"alpha"})
	outcome := search.ExecutionResult{
		Returned: 1,
		Results: []search.SearchResult{{
			FilePath: payload, FileSize: 42,
			RawExcerpts:     []string{payload, payload, payload, payload},
			StartLines:      []int{2, 0, 3, 0},
			ChunkScores:     []int{1, 1, 0, 0},
			ChunkTermCounts: []int{1, 1, 0, 0},
		}},
	}
	var out bytes.Buffer
	writePlainOutput(&out, args, outcome)
	want := "MATCH 1/1 [Score: 0 (0/1 terms)]\nFILE: " + escaped + "\nSIZE: 42\n" +
		"EXCERPT 1 [L2, Score: 1 (1 terms)]: " + escaped + "\n" +
		"EXCERPT 2 [Score: 1 (1 terms)]: " + escaped + "\n" +
		"EXCERPT 3 [L3]: " + escaped + "\n" +
		"EXCERPT 4: " + escaped + "\n---\n"
	if out.String() != want {
		t.Fatalf("plain output = %q, want %q", out.String(), want)
	}
	if outcome.Results[0].FilePath != payload || outcome.Results[0].RawExcerpts[0] != payload {
		t.Fatal("output escaping mutated search results")
	}
}

func TestPathOnlyCannotForgeLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support control characters in filenames")
	}
	root := t.TempDir()
	name := "evil\r\nMATCH 9\nFILE: forged\nSIZE: 0\nEXCERPT 9\n---\t\x1b\x7f\u0085\u2028.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := parseArguments([]string{"alpha", "--startdir", root, "--only", "txt", "--pathonly"})
	out := string(captureCLIStdout(t, func() int { return runPathOnly(args) }))
	want := root + `/evil\r\nMATCH 9\nFILE: forged\nSIZE: 0\nEXCERPT 9\n---\t\x1b\x7f\u0085\u2028.txt` + "\n"
	if out != want {
		t.Fatalf("pathonly output = %q, want %q", out, want)
	}
}

func TestPlainOutputBackslashPathsRoundTripInParityHelpers(t *testing.T) {
	const winPath = `C:\Users\runner\project\neutral\zz-top.txt`
	args := parseArguments([]string{"alpha"})
	outcome := search.ExecutionResult{
		Returned: 1,
		Results: []search.SearchResult{{
			FilePath:        winPath,
			FileSize:        42,
			RawExcerpts:     []string{"alpha beta", "alpha gamma"},
			StartLines:      []int{1, 10},
			ChunkScores:     []int{10, 8},
			ChunkTermCounts: []int{2, 2},
		}},
	}
	var out bytes.Buffer
	writePlainOutput(&out, args, outcome)

	gotPaths := outputPathsFromPlainOutput(t, out.String())
	if !slicesEqual(gotPaths, []string{winPath}) {
		t.Fatalf("outputPathsFromPlainOutput = %q, want [%q]", gotPaths, winPath)
	}
	if got := excerptCountForPlainFile(out.String(), winPath); got != 2 {
		t.Fatalf("excerptCountForPlainFile(%q) = %d, want 2", winPath, got)
	}
}

func TestJSONOutputPartial(t *testing.T) {
	root := t.TempDir()
	writeCLISearchFixture(t, root, "deployment.md", "deployment completed successfully\n")

	for _, tc := range []struct {
		name     string
		partial  string
		wantJSON string
		wantMode string
	}{
		{name: "prefix", partial: "--partial=prefix", wantJSON: "prefix", wantMode: "Mode: partial (prefix)"},
		{name: "contains", partial: "--partial=contains", wantJSON: "contains", wantMode: "Mode: partial (contains)"},
		{name: "off omits partial", partial: "", wantJSON: "", wantMode: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"deploy", "--startdir", root, "--only", "md"}
			if tc.partial != "" {
				argv = append(argv, tc.partial)
			}
			args := parseArguments(argv)

			jsonOut := captureCLIStdout(t, func() int { return runJSON(args) })
			var output struct {
				Query map[string]json.RawMessage `json:"query"`
			}
			if err := json.Unmarshal(jsonOut, &output); err != nil {
				t.Fatalf("decode JSON output: %v", err)
			}
			partial, present := output.Query["partial"]
			if tc.wantJSON == "" {
				if present {
					t.Errorf("JSON query partial = %s, want omitted", partial)
				}
			} else {
				var got string
				if !present || json.Unmarshal(partial, &got) != nil || got != tc.wantJSON {
					t.Errorf("JSON query partial = %s, want %q", partial, tc.wantJSON)
				}
			}

			plainOut := string(captureCLIStdout(t, func() int { return runPlain(args) }))
			if tc.wantMode == "" {
				if strings.Contains(plainOut, "Mode: partial (") {
					t.Errorf("plain output unexpectedly includes partial mode:\n%s", plainOut)
				}
			} else if !strings.Contains(plainOut, tc.wantMode) {
				t.Errorf("plain output missing partial mode %q:\n%s", tc.wantMode, plainOut)
			}
		})
	}
}

// buildRankedSubsetFixture creates one file with two separated clusters of
// unequal quality: a three-term cluster followed by a two-term cluster.
func buildRankedSubsetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	content := strings.Join([]string{
		"First cluster begins. alpha beta gamma first cluster. First cluster ends.",
		skipAheadCLIPadding("between", 90),
		"Second cluster begins. alpha beta second cluster. Second cluster ends.",
	}, " ")
	writeCLISearchFixture(t, root, "fixture.txt", content)
	return root
}

func TestRankedSubsetJSONAndPlainOutput(t *testing.T) {
	root := buildRankedSubsetFixture(t)
	args := parseArguments([]string{
		"alpha", "beta", "gamma",
		"--startdir", root,
		"--only", "txt",
		"--distance", "100",
		"--max-excerpts", "3",
	})

	t.Run("JSON excerpts carry per-chunk score and term_count", func(t *testing.T) {
		out := captureCLIStdout(t, func() int { return runJSON(args) })
		var result jsonOutput
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("decode JSON output: %v", err)
		}
		if result.Matches != 1 || len(result.Results) != 1 {
			t.Fatalf("JSON matches/results = %d/%d, want 1/1", result.Matches, len(result.Results))
		}
		excerpts := result.Results[0].Excerpts
		if len(excerpts) != 2 {
			t.Fatalf("JSON excerpt count = %d, want 2 (both quality levels)", len(excerpts))
		}
		if excerpts[0].Score != 14 || excerpts[0].TermCount != 3 {
			t.Fatalf("first excerpt quality = (score %d, terms %d), want (14, 3)", excerpts[0].Score, excerpts[0].TermCount)
		}
		if excerpts[1].Score != 9 || excerpts[1].TermCount != 2 {
			t.Fatalf("second excerpt quality = (score %d, terms %d), want (9, 2)", excerpts[1].Score, excerpts[1].TermCount)
		}
		if !strings.Contains(excerpts[0].Text, "gamma") {
			t.Fatalf("first excerpt should be the best cluster: %q", excerpts[0].Text)
		}
		if strings.Contains(excerpts[1].Text, "gamma") {
			t.Fatalf("second excerpt should be the lower-quality cluster: %q", excerpts[1].Text)
		}
		if line := excerpts[0].StartLine; line <= 0 {
			t.Fatalf("first excerpt start_line = %d, want positive", line)
		}
	})

	t.Run("plain excerpt headers carry per-chunk quality", func(t *testing.T) {
		out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
		if got := strings.Count(out, "EXCERPT "); got != 2 {
			t.Fatalf("plain excerpt count = %d, want 2:\n%s", got, out)
		}
		if !strings.Contains(out, "EXCERPT 1 [L1, Score: 14 (3 terms)]") {
			t.Errorf("plain output missing per-chunk quality header for the best chunk:\n%s", out)
		}
		if !strings.Contains(out, "EXCERPT 2 [L1, Score: 9 (2 terms)]") {
			t.Errorf("plain output missing per-chunk quality header for the lower chunk:\n%s", out)
		}
	})
}

func TestSkipAheadJSONAndPlainOutput(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"First cluster begins. needle first marker. First cluster ends.",
		skipAheadCLIPadding("between-first-second", 90),
		"Second cluster begins. needle second marker. Second cluster ends.",
		skipAheadCLIPadding("between-second-third", 90),
		"Third cluster begins. needle third marker. Third cluster ends.",
	}, " ")
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := parseArguments([]string{
		"needle",
		"--startdir", root,
		"--only", "txt",
		"--distance", "100",
		"--max-excerpts", "3",
	})

	jsonOut := captureCLIStdout(t, func() int { return runJSON(args) })
	var got jsonOutput
	if err := json.Unmarshal(jsonOut, &got); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if got.Matches != 1 || len(got.Results) != 1 {
		t.Fatalf("JSON matches/results = %d/%d, want 1/1", got.Matches, len(got.Results))
	}
	if len(got.Results[0].Excerpts) != 3 {
		t.Fatalf("JSON excerpt count = %d, want 3", len(got.Results[0].Excerpts))
	}
	for i := range got.Results[0].Excerpts {
		for j := 0; j < i; j++ {
			common := skipAheadCLILongestCommonRun(got.Results[0].Excerpts[i].Text, got.Results[0].Excerpts[j].Text)
			shorter := len(got.Results[0].Excerpts[i].Text)
			if len(got.Results[0].Excerpts[j].Text) < shorter {
				shorter = len(got.Results[0].Excerpts[j].Text)
			}
			if common*5 >= shorter {
				t.Fatalf("JSON excerpts %d and %d share common run %d, want less than 20%% of shorter length %d", i, j, common, shorter)
			}
		}
	}

	plainOut := string(captureCLIStdout(t, func() int { return runPlain(args) }))
	if got := strings.Count(plainOut, "EXCERPT "); got != 3 {
		t.Fatalf("plain output excerpt count = %d, want 3:\n%s", got, plainOut)
	}
}

func skipAheadCLIPadding(prefix string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return strings.Join(parts, " ")
}

func skipAheadCLILongestCommonRun(a, b string) int {
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

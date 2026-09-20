package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}

func captureCLIStdout(t *testing.T, run func() int) []byte {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	code := run()
	_ = writer.Close()
	os.Stdout = originalStdout
	defer reader.Close()

	if code != 0 {
		t.Fatalf("CLI exit code = %d, want 0", code)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return out
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

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildRankedSubsetFixture creates one file with two separated clusters of
// unequal quality: a three-term cluster followed by a two-term cluster.
func buildRankedSubsetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pad := func(prefix string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = prefix + "-" + threeDigitCLI(i)
		}
		return strings.Join(parts, " ")
	}
	content := strings.Join([]string{
		"First cluster begins. alpha beta gamma first cluster. First cluster ends.",
		pad("between", 90),
		"Second cluster begins. alpha beta second cluster. Second cluster ends.",
	}, " ")
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

func threeDigitCLI(i int) string {
	switch {
	case i < 10:
		return "00" + string(rune('0'+i))
	case i < 100:
		return "0" + string(rune('0'+i/10)) + string(rune('0'+i%10))
	default:
		return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
	}
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

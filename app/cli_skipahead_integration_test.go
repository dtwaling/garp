package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

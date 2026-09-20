package search_test

import (
	"os"
	"path/filepath"
	"testing"

	"garp/search"
)

func TestHeavyBinaryPrefilterUsesPrimaryAndLongestSecondary(t *testing.T) {
	words := []string{"the", "database", "synchronization"}

	tests := []struct {
		name    string
		content string
		found   bool
	}{
		{
			name:    "primary and longest secondary survive",
			content: "the synchronization",
			found:   true,
		},
		{
			name:    "primary and short secondary are pruned",
			content: "the database",
			found:   false,
		},
		{
			name:    "longest secondary without primary is pruned",
			content: "synchronization",
			found:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "candidate.eml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write candidate: %v", err)
			}

			found, decided := search.BinaryStreamingPrefilterDecided(path, words, 0)
			if !decided {
				t.Fatal("prefilter was inconclusive for a small candidate")
			}
			if found != tt.found {
				t.Errorf("found = %v, want %v", found, tt.found)
			}
		})
	}
}

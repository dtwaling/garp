package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestStreamContainsRankedWordsDecided(t *testing.T) {
	dir := t.TempDir()
	words := []string{"alpha", "beta", "gamma"}
	tests := []struct {
		name   string
		text   string
		strict bool
		found  bool
	}{
		{
			name:  "relaxed mode accepts primary and tertiary",
			text:  "alpha gamma",
			found: true,
		},
		{
			name:  "relaxed mode rejects primary alone",
			text:  "alpha",
			found: false,
		},
		{
			name:  "relaxed mode rejects missing primary",
			text:  "beta gamma",
			found: false,
		},
		{
			name:   "strict mode rejects missing secondary",
			text:   "alpha gamma",
			strict: true,
			found:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".txt")
			if err := os.WriteFile(path, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}

			found, decided := search.StreamContainsRankedWordsDecided(path, words, tt.strict)
			if !decided {
				t.Fatal("small text file prefilter was inconclusive")
			}
			if found != tt.found {
				t.Fatalf("found = %v, want %v", found, tt.found)
			}
		})
	}
}

func TestCheckFileContainsRankedWords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.txt")
	if err := os.WriteFile(path, []byte("alpha gamma"), 0o600); err != nil {
		t.Fatal(err)
	}

	score, count, terms, span, ok, err := search.CheckFileContainsRankedWords(path, []string{"alpha", "beta", "gamma"}, 20, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || score != 10 || count != 2 || strings.Join(terms, ",") != "alpha,gamma" || span != 11 {
		t.Fatalf("ranked file match = (%d, %d, %v, %d, %v), want (10, 2, [alpha gamma], 11, true)", score, count, terms, span, ok)
	}
}

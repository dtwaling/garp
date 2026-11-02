package search

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type errorAfterDataReader struct {
	data []byte
	err  error
	read bool
}

func (r *errorAfterDataReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	return copy(p, r.data), r.err
}

func TestStreamQueryReadErrorDecision(t *testing.T) {
	readErr := errors.New("injected read failure")

	t.Run("unproven match remains undecided", func(t *testing.T) {
		query := compileQuery([]string{"missing"}, PartialModeOff)
		found, complete, err := streamMatchesQuery(
			&errorAfterDataReader{data: []byte("partial data"), err: readErr},
			query,
			true,
			1024,
		)
		gotFound, gotDecided := decideStreamQueryResult(found, complete, err, true)
		if gotFound || gotDecided {
			t.Fatalf("read error result = (%v, %v), want (false, false)", gotFound, gotDecided)
		}
	})

	t.Run("proven match survives read error", func(t *testing.T) {
		query := compileQuery([]string{"needle"}, PartialModeOff)
		found, complete, err := streamMatchesQuery(
			&errorAfterDataReader{data: []byte("needle"), err: readErr},
			query,
			true,
			1024,
		)
		gotFound, gotDecided := decideStreamQueryResult(found, complete, err, true)
		if !gotFound || !gotDecided {
			t.Fatalf("proven match result = (%v, %v), want (true, true)", gotFound, gotDecided)
		}
	})
}

func TestReaderValidatedEOF(t *testing.T) {
	readErr := errors.New("injected checksum failure")

	t.Run("validated EOF", func(t *testing.T) {
		if !readerValidatedEOF(strings.NewReader("")) {
			t.Fatal("empty reader did not validate EOF")
		}
	})
	t.Run("understated size exposes more data", func(t *testing.T) {
		if readerValidatedEOF(strings.NewReader("x")) {
			t.Fatal("reader with remaining data validated EOF")
		}
	})
	t.Run("checksum failure remains undecided", func(t *testing.T) {
		if readerValidatedEOF(&errorAfterDataReader{err: readErr}) {
			t.Fatal("reader error validated EOF")
		}
	})
}

func TestStreamPrefilterOpenFailureIsUndecided(t *testing.T) {
	missingText := filepath.Join(t.TempDir(), "missing.txt")
	missingEmail := filepath.Join(t.TempDir(), "missing.eml")
	missingRTF := filepath.Join(t.TempDir(), "missing.rtf")

	tests := []struct {
		name string
		run  func() (bool, bool)
	}{
		{
			name: "all words text",
			run: func() (bool, bool) {
				return StreamContainsAllWordsDecided(missingText, []string{"needle"})
			},
		},
		{
			name: "ranked text",
			run: func() (bool, bool) {
				return StreamContainsRankedWordsDecided(missingText, []string{"needle", "anchor"}, true)
			},
		},
		{
			name: "capped stream",
			run: func() (bool, bool) {
				return StreamContainsAllWordsDecidedWithCap(missingText, []string{"needle"}, 1024)
			},
		},
		{
			name: "email binary",
			run: func() (bool, bool) {
				return BinaryStreamingPrefilterDecided(missingEmail, []string{"needle"}, 1024)
			},
		},
		{
			name: "RTF binary",
			run: func() (bool, bool) {
				return BinaryStreamingPrefilterDecided(missingRTF, []string{"needle"}, 1024)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, decided := tt.run()
			if found || decided {
				t.Fatalf("open failure result = (%v, %v), want (false, false)", found, decided)
			}
		})
	}
}

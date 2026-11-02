package search_test

import (
	"strings"
	"testing"

	"garp/search"
)

func TestParseMaxFileSizeAcceptsSupportedGrammar(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{input: "1", want: 1},
		{input: "512B", want: 512},
		{input: "2k", want: 2 * 1024},
		{input: "2KB", want: 2 * 1024},
		{input: "2KiB", want: 2 * 1024},
		{input: "3m", want: 3 * 1024 * 1024},
		{input: "3MB", want: 3 * 1024 * 1024},
		{input: "3MiB", want: 3 * 1024 * 1024},
		{input: "100M", want: 100 * 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := search.ParseMaxFileSize(tt.input)
			if err != nil {
				t.Fatalf("ParseMaxFileSize(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseMaxFileSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMaxFileSizeRejectsInvalidValues(t *testing.T) {
	tests := []string{
		"",
		"0",
		"-1",
		"+1",
		"1.5M",
		"1T",
		"18446744073709551616",
		"101M",
		"1G",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := search.ParseMaxFileSize(input); err == nil {
				t.Errorf("ParseMaxFileSize(%q) error = nil, want rejection", input)
			}
		})
	}
}

func TestParseMaxFileSizeRejectsGigabyteUnit(t *testing.T) {
	_, err := search.ParseMaxFileSize("1GiB")
	if err == nil {
		t.Fatal("ParseMaxFileSize(\"1GiB\") error = nil, want unknown-unit rejection")
	}
	if !strings.Contains(err.Error(), "unknown unit") {
		t.Errorf("ParseMaxFileSize(\"1GiB\") error = %q, want unknown-unit error", err)
	}
}

func TestParseMaxFileSizeRejectsUnitMultiplicationOverflow(t *testing.T) {
	const input = "18446744073709551615K"
	_, err := search.ParseMaxFileSize(input)
	if err == nil {
		t.Fatalf("ParseMaxFileSize(%q) error = nil, want overflow rejection", input)
	}
	if !strings.Contains(err.Error(), "overflows bytes") {
		t.Errorf("ParseMaxFileSize(%q) error = %q, want multiplication-overflow error", input, err)
	}
}

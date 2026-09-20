package app

import "testing"

func TestParseArgumentsStrict_DefaultsFalse(t *testing.T) {
	args := parseArguments([]string{"term1", "term2", "term3"})
	if args.Strict {
		t.Error("Strict = true without --strict, want false")
	}
}

func TestParseArgumentsStrict_Enabled(t *testing.T) {
	args := parseArguments([]string{"term1", "term2", "term3", "--strict"})
	if !args.Strict {
		t.Error("Strict = false with --strict, want true")
	}
}

func TestParseArgumentsStrict_CombinedWithOutputAndSearchFlags(t *testing.T) {
	args := parseArguments([]string{
		"term1", "term2", "term3",
		"--strict", "--json", "--plain", "--code", "--distance", "321",
	})

	if !args.Strict {
		t.Error("Strict = false with --strict, want true")
	}
	if !args.JSONOutput {
		t.Error("JSONOutput = false with --json, want true")
	}
	if !args.PlainOutput {
		t.Error("PlainOutput = false with --plain, want true")
	}
	if !args.IncludeCode {
		t.Error("IncludeCode = false with --code, want true")
	}
	if args.Distance != 321 {
		t.Errorf("Distance = %d, want 321", args.Distance)
	}
}

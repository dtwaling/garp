package app

import "testing"

func TestParseArgumentsMaxFileSize(t *testing.T) {
	t.Run("valid value", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize", "12MiB"})
		if args.MaxFileSizeErr != nil {
			t.Fatalf("MaxFileSizeErr = %v, want nil", args.MaxFileSizeErr)
		}
		if args.MaxFileSize != 12*1024*1024 {
			t.Errorf("MaxFileSize = %d, want %d", args.MaxFileSize, 12*1024*1024)
		}
	})

	t.Run("missing terminal value", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize"})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want missing-value error")
		}
	})

	t.Run("following flag is invalid value", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize", "--no-ignore"})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want invalid-value error")
		}
		if args.NoIgnore {
			t.Error("NoIgnore = true, want following flag consumed as max-filesize value")
		}
	})

	t.Run("latest valid value clears prior error", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"--max-filesize", "invalid",
			"--max-filesize", "2M",
		})
		if args.MaxFileSizeErr != nil {
			t.Fatalf("MaxFileSizeErr = %v, want nil from latest value", args.MaxFileSizeErr)
		}
		if args.MaxFileSize != 2*1024*1024 {
			t.Errorf("MaxFileSize = %d, want %d", args.MaxFileSize, 2*1024*1024)
		}
	})

	t.Run("latest invalid value clears prior size", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"--max-filesize", "2M",
			"--max-filesize", "invalid",
		})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want error from latest value")
		}
		if args.MaxFileSize != 0 {
			t.Errorf("MaxFileSize = %d, want 0 after latest value failed", args.MaxFileSize)
		}
	})
}

func TestParseArgumentsNoIgnore(t *testing.T) {
	args := parseArguments([]string{"needle", "--no-ignore"})
	if !args.NoIgnore {
		t.Error("NoIgnore = false, want true")
	}
}

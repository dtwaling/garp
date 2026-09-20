package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestCLIArgumentsPartial(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		partial search.PartialMode
		wantErr bool
	}{
		{
			name:    "default is off",
			argv:    []string{"deploy"},
			partial: search.PartialModeOff,
		},
		{
			name:    "bare flag enables prefix",
			argv:    []string{"deploy", "--partial"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "prefix mode",
			argv:    []string{"deploy", "--partial=prefix"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "contains mode",
			argv:    []string{"deploy", "--partial=contains"},
			partial: search.PartialModeContains,
		},
		{
			name:    "off mode",
			argv:    []string{"deploy", "--partial=off"},
			partial: search.PartialModeOff,
		},
		{
			name:    "invalid mode reports validation error",
			argv:    []string{"deploy", "--partial=fuzzy"},
			wantErr: true,
		},
		{
			name:    "combines with other flags",
			argv:    []string{"deploy", "--partial=prefix", "--code", "--distance", "200", "--json"},
			partial: search.PartialModePrefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if tt.wantErr {
				if args.PartialErr == nil {
					t.Fatal("PartialErr = nil, want validation error")
				}
				return
			}
			if args.PartialErr != nil {
				t.Fatalf("PartialErr = %v, want nil", args.PartialErr)
			}
			if args.Partial != tt.partial {
				t.Errorf("Partial = %q, want %q", args.Partial, tt.partial)
			}
		})
	}
}

func TestRunPlainPartialConfiguresSearchEngine(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "deployment.md")
	if err := os.WriteFile(fixture, []byte("deployment is active"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := parseArguments([]string{
		"deploy",
		"--partial=prefix",
		"--startdir", root,
		"--only", "md",
		"--plain",
	})
	out := string(captureCLIStdout(t, func() int { return runPlain(args) }))
	if !strings.Contains(out, "FILE: "+fixture) {
		t.Fatalf("partial search did not return derivative-form fixture:\n%s", out)
	}
}

func TestShowUsageDocumentsPartial(t *testing.T) {
	out := string(captureCLIStdout(t, func() int {
		showUsage()
		return 0
	}))
	for _, want := range []string{"--partial[=MODE]", "prefix, contains", "deploy*"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
}

package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryByteScanner(t *testing.T) {
	tests := []struct {
		name string
		buf  string
		word string
		mode PartialMode
		want bool
	}{
		{
			name: "whole word mode rejects derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModeOff,
			want: false,
		},
		{
			name: "prefix mode accepts derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModePrefix,
			want: true,
		},
		{
			name: "glob enables prefix while mode is off",
			buf:  "deployment",
			word: "deploy*",
			mode: PartialModeOff,
			want: true,
		},
		{
			name: "contains mode accepts derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
		{
			name: "prefix mode rejects internal substring",
			buf:  "redeploy",
			word: "deploy",
			mode: PartialModePrefix,
			want: false,
		},
		{
			name: "glob rejects internal substring",
			buf:  "redeploy",
			word: "deploy*",
			mode: PartialModeOff,
			want: false,
		},
		{
			name: "contains mode accepts internal substring",
			buf:  "redeploy",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
		{
			name: "prefix mode accepts snake case token",
			buf:  "deploy_service",
			word: "deploy",
			mode: PartialModePrefix,
			want: true,
		},
		{
			name: "contains mode accepts snake case token",
			buf:  "deploy_service",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := asciiIndexPartialCI([]byte(tt.buf), []byte(tt.word), tt.mode) >= 0
			if got != tt.want {
				t.Errorf("asciiIndexPartialCI(%q, %q, %q) found = %t, want %t", tt.buf, tt.word, tt.mode, got, tt.want)
			}
		})
	}
}

func TestDiscoveryByteScannerFileWalk(t *testing.T) {
	root := t.TempDir()
	derivative := filepath.Join(root, "derivative.md")
	internal := filepath.Join(root, "internal.md")
	if err := os.WriteFile(derivative, []byte("deployment is ready"), 0o644); err != nil {
		t.Fatalf("write derivative fixture: %v", err)
	}
	if err := os.WriteFile(internal, []byte("redeploy is ready"), 0o644); err != nil {
		t.Fatalf("write internal fixture: %v", err)
	}

	for _, tt := range []struct {
		name      string
		word      string
		mode      PartialMode
		wantFiles []string
	}{
		{"prefix", "deploy", PartialModePrefix, []string{"derivative.md"}},
		{"glob", "deploy*", PartialModeOff, []string{"derivative.md"}},
		{"contains", "deploy", PartialModeContains, []string{"derivative.md", "internal.md"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files, err := FindFilesWithFirstWordPartial(tt.word, nil, root, nil, tt.mode)
			if err != nil {
				t.Fatalf("FindFilesWithFirstWordPartial: %v", err)
			}
			got := make(map[string]bool, len(files))
			for _, file := range files {
				got[filepath.Base(file)] = true
			}
			if len(got) != len(tt.wantFiles) {
				t.Fatalf("candidate count = %d, want %d: %#v", len(got), len(tt.wantFiles), got)
			}
			for _, want := range tt.wantFiles {
				if !got[want] {
					t.Errorf("missing candidate %q in %#v", want, got)
				}
			}

			progressFiles, err := FindFilesWithFirstWordProgressPartial([]string{tt.word}, nil, 1, nil, root, nil, tt.mode)
			if err != nil {
				t.Fatalf("FindFilesWithFirstWordProgressPartial: %v", err)
			}
			progressGot := make(map[string]bool, len(progressFiles))
			for _, file := range progressFiles {
				progressGot[filepath.Base(file)] = true
			}
			if len(progressGot) != len(tt.wantFiles) {
				t.Fatalf("progress candidate count = %d, want %d: %#v", len(progressGot), len(tt.wantFiles), progressGot)
			}
			for _, want := range tt.wantFiles {
				if !progressGot[want] {
					t.Errorf("progress scan missing candidate %q in %#v", want, progressGot)
				}
			}
		})
	}
}
